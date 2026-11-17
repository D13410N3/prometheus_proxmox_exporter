package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	// vmTagCacheTTL is how long VM tags are reused between scrapes. Tags change
	// rarely and one config request per VM per scrape would double the number
	// of Proxmox API calls.
	vmTagCacheTTL = 5 * time.Minute
	// vmTagFetchWorkers bounds the parallel config requests per node.
	vmTagFetchWorkers = 4
	// nodeDiscoveryTTL is how long the discovered node list is reused between
	// scrapes. It is deliberately short: API calls for a powered off node are
	// proxied by pveproxy to that node and only fail after a connect timeout,
	// so the list has to be refreshed often enough to stop querying it.
	nodeDiscoveryTTL = 30 * time.Second
	// requestTimeout bounds a single Proxmox API request.
	requestTimeout = 5 * time.Second
	// nodeFetchWorkers bounds how many nodes are collected in parallel.
	nodeFetchWorkers = 4
)

// Configuration holds all the application configuration
type Config struct {
	ProxmoxAddress  string
	ProxmoxPort     string
	ProxmoxUsername string
	ProxmoxToken    string
	ListenAddress   string
	LogLevel        string
}

var (
	listenAddress = flag.String("listen.address", "", "Address to bind (defaults to LISTEN_ADDRESS env var or 127.0.0.1:9914)")
	logLevel      = flag.String("log.level", "", "Logging level (defaults to LOG_LEVEL env var or 'none')")

	nodeStatusDesc = prometheus.NewDesc("proxmox_node_status", "Proxmox node status (1=online, 0=offline)", []string{"node"}, nil)
)

func isNumeric(s interface{}) bool {
	_, ok := s.(json.Number)
	return ok
}

// ProxmoxNode represents a node in the Proxmox cluster. Online reflects the
// status reported by the cluster: nodes that are down must not be queried,
// because every request for them is proxied and fails after a connect timeout.
type ProxmoxNode struct {
	Name   string
	Online bool
}

// vmTagEntry is one cached VM tag list.
type vmTagEntry struct {
	tags    string
	fetched time.Time
}

// vmTagCache keeps VM tags between scrapes, keyed by node and vmid.
type vmTagCache struct {
	ttl     time.Duration
	mutex   sync.Mutex
	entries map[string]vmTagEntry
}

// newVMTagCache creates an empty tag cache
func newVMTagCache(ttl time.Duration) *vmTagCache {
	return &vmTagCache{
		ttl:     ttl,
		entries: make(map[string]vmTagEntry),
	}
}

// get returns a cached tag list if it is still fresh
func (cache *vmTagCache) get(key string, now time.Time) (string, bool) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	entry, ok := cache.entries[key]
	if !ok || now.Sub(entry.fetched) > cache.ttl {
		return "", false
	}
	return entry.tags, true
}

// set stores a tag list fetched at the given time
func (cache *vmTagCache) set(key, tags string, now time.Time) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	cache.entries[key] = vmTagEntry{tags: tags, fetched: now}
}

// prune drops entries that have not been refreshed for a long time, so the
// cache does not grow without bound when VMs are removed.
func (cache *vmTagCache) prune(now time.Time) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()

	for key, entry := range cache.entries {
		if now.Sub(entry.fetched) > 4*cache.ttl {
			delete(cache.entries, key)
		}
	}
}

// proxmoxCollector implements the prometheus.Collector interface
type proxmoxCollector struct {
	baseURL string
	token   string
	client  *http.Client

	nodes      []ProxmoxNode
	nodesMutex sync.RWMutex

	// discoveryMutex serializes node discovery and guards discovered.
	discoveryMutex sync.Mutex
	discovered     time.Time
	discoveryTTL   time.Duration

	vmTags *vmTagCache
}

// newProxmoxCollector creates a new proxmoxCollector
func newProxmoxCollector(baseURL, token string, discoveryTTL time.Duration) *proxmoxCollector {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	}

	return &proxmoxCollector{
		baseURL:      baseURL,
		token:        token,
		client:       &http.Client{Timeout: requestTimeout, Transport: transport},
		nodes:        []ProxmoxNode{},
		discoveryTTL: discoveryTTL,
		vmTags:       newVMTagCache(vmTagCacheTTL),
	}
}

// refreshNodes reloads the node list once the cached copy is older than the
// discovery TTL. The list is served by the entry point's own pveproxy, so it
// stays cheap even when other nodes are down, and it carries each node's status.
func (collector *proxmoxCollector) refreshNodes() {
	collector.discoveryMutex.Lock()
	defer collector.discoveryMutex.Unlock()

	if !collector.discovered.IsZero() && time.Since(collector.discovered) < collector.discoveryTTL {
		return
	}

	nodesResponse := collector.fetchJSON("/nodes")
	nodes, ok := nodesResponse["data"].([]interface{})
	if !ok {
		// Keep the previous list: it is better to keep scraping the nodes we
		// know than to drop every series on one failed discovery call.
		collector.discovered = time.Now()
		fmt.Fprintf(os.Stderr, "node discovery failed, keeping %d known nodes\n", len(collector.snapshotNodes()))
		return
	}

	discoveredNodes := make([]ProxmoxNode, 0, len(nodes))
	for _, node := range nodes {
		nodeMap, ok := node.(map[string]interface{})
		if !ok {
			continue
		}
		discoveredNodes = append(discoveredNodes, ProxmoxNode{
			Name:   fmt.Sprintf("%v", nodeMap["node"]),
			Online: fmt.Sprintf("%v", nodeMap["status"]) == "online",
		})
	}

	collector.nodesMutex.Lock()
	changed := !sameNodes(collector.nodes, discoveredNodes)
	collector.nodes = discoveredNodes
	collector.nodesMutex.Unlock()

	collector.discovered = time.Now()

	if changed {
		online := 0
		for _, node := range discoveredNodes {
			if node.Online {
				online++
			}
		}
		fmt.Printf("Discovered %d nodes in Proxmox cluster (%d online)\n", len(discoveredNodes), online)
	}
}

// snapshotNodes returns a copy of the last discovered node list.
func (collector *proxmoxCollector) snapshotNodes() []ProxmoxNode {
	collector.nodesMutex.RLock()
	defer collector.nodesMutex.RUnlock()

	nodes := make([]ProxmoxNode, len(collector.nodes))
	copy(nodes, collector.nodes)
	return nodes
}

// sameNodes reports whether two node lists describe the same cluster state.
func sameNodes(a, b []ProxmoxNode) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (collector *proxmoxCollector) Describe(ch chan<- *prometheus.Desc) {
	// Intentionally empty.
}

func (collector *proxmoxCollector) Collect(ch chan<- prometheus.Metric) {
	collector.refreshNodes()

	collector.collectCluster(ch)
	collector.collectClusterStorage(ch)
	collector.collectNodes(ch, collector.snapshotNodes())
}

// collectCluster emits cluster-wide metrics from /cluster/status.
func (collector *proxmoxCollector) collectCluster(ch chan<- prometheus.Metric) {
	response := collector.fetchJSON("/cluster/status")
	entries, ok := response["data"].([]interface{})
	if !ok {
		return
	}

	for _, entry := range entries {
		entryMap, ok := entry.(map[string]interface{})
		if !ok || fmt.Sprintf("%v", entryMap["type"]) != "cluster" {
			continue
		}

		clusterName := fmt.Sprintf("%v", entryMap["name"])

		if quorate, ok := entryMap["quorate"].(json.Number); ok {
			value, _ := quorate.Float64()
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc("proxmox_cluster_quorate", "Proxmox cluster quorum status (1=quorate, 0=not quorate)", []string{"cluster"}, nil),
				prometheus.GaugeValue,
				value,
				clusterName,
			)
		}

		if nodeCount, ok := entryMap["nodes"].(json.Number); ok {
			value, _ := nodeCount.Float64()
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc("proxmox_cluster_nodes", "Number of nodes in the Proxmox cluster", []string{"cluster"}, nil),
				prometheus.GaugeValue,
				value,
				clusterName,
			)
		}
	}
}

// collectClusterStorage emits cluster-wide storage information.
func (collector *proxmoxCollector) collectClusterStorage(ch chan<- prometheus.Metric) {
	storageTypeCount := make(map[string]int)

	storageResponse := collector.fetchJSON("/storage")
	if data, ok := storageResponse["data"].([]interface{}); ok {
		for _, item := range data {
			if itemMap, ok := item.(map[string]interface{}); ok {
				storageName := fmt.Sprintf("%v", itemMap["storage"])
				storageType := fmt.Sprintf("%v", itemMap["type"])
				isShared := "0"
				if shared, ok := itemMap["shared"].(json.Number); ok && shared.String() == "1" {
					isShared = "1"
				}

				storageTypeCount[storageType]++

				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc("proxmox_storage_info", "Information about Proxmox storages", []string{"storage", "type", "shared", "node"}, nil),
					prometheus.GaugeValue,
					1,
					storageName,
					storageType,
					isShared,
					"cluster",
				)
			}
		}
	}

	for storageType, count := range storageTypeCount {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc("proxmox_storage_type_count", "Count of storages by type", []string{"type"}, nil),
			prometheus.GaugeValue,
			float64(count),
			storageType,
		)
	}
}

// collectNodes collects every node concurrently, so a single slow or
// unreachable node cannot use up the whole scrape budget on its own.
func (collector *proxmoxCollector) collectNodes(ch chan<- prometheus.Metric, nodes []ProxmoxNode) {
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, nodeFetchWorkers)

	for _, node := range nodes {
		if !node.Online {
			// Never query an offline node: pveproxy would try to reach it and
			// answer with an empty 595 only after a connect timeout. Its other
			// series just stop being exported, which is what we want.
			ch <- prometheus.MustNewConstMetric(nodeStatusDesc, prometheus.GaugeValue, 0, node.Name)
			continue
		}

		wg.Add(1)
		semaphore <- struct{}{}
		go func(node ProxmoxNode) {
			defer wg.Done()
			defer func() { <-semaphore }()
			collector.collectNode(ch, node)
		}(node)
	}

	wg.Wait()
}

// collectNode collects all metrics of one online node.
func (collector *proxmoxCollector) collectNode(ch chan<- prometheus.Metric, node ProxmoxNode) {
	var (
		statusResponse  map[string]interface{}
		storageResponse map[string]interface{}
		vmsResponse     map[string]interface{}
		fetchGroup      sync.WaitGroup
	)

	fetchGroup.Add(3)
	go func() {
		defer fetchGroup.Done()
		statusResponse = collector.fetchJSON("/nodes/" + node.Name + "/status")
	}()
	go func() {
		defer fetchGroup.Done()
		storageResponse = collector.fetchJSON("/nodes/" + node.Name + "/storage")
	}()
	go func() {
		defer fetchGroup.Done()
		vmsResponse = collector.fetchJSON("/nodes/" + node.Name + "/qemu")
	}()
	fetchGroup.Wait()

	// The node is listed as online, so a failed status request means it is
	// unreachable right now.
	nodeStatusValue := float64(0)
	if _, ok := statusResponse["data"].(map[string]interface{}); ok {
		nodeStatusValue = 1
	}
	ch <- prometheus.MustNewConstMetric(nodeStatusDesc, prometheus.GaugeValue, nodeStatusValue, node.Name)

	collector.emitNodeStatus(ch, node, statusResponse)
	collector.emitNodeStorage(ch, node, storageResponse)
	collector.emitNodeVMs(ch, node, vmsResponse)
}

// emitNodeStatus emits node level metrics from /nodes/<node>/status.
func (collector *proxmoxCollector) emitNodeStatus(ch chan<- prometheus.Metric, node ProxmoxNode, nodeStatus map[string]interface{}) {
	data, ok := nodeStatus["data"].(map[string]interface{})
	if !ok {
		return
	}

	// Collect all numeric node metrics
	for key, value := range data {
		if isNumeric(value) {
			metricName := fmt.Sprintf("proxmox_node_%s", key)
			metricValue, _ := value.(json.Number).Float64()
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc(metricName, "Proxmox node metric", []string{"node"}, nil),
				prometheus.GaugeValue,
				metricValue,
				node.Name,
			)
		}
	}

	// Special handling for memory metrics which are nested
	if memory, ok := data["memory"].(map[string]interface{}); ok {
		for memKey, memValue := range memory {
			if isNumeric(memValue) {
				metricName := fmt.Sprintf("proxmox_node_memory_%s_bytes", memKey)
				metricValue, _ := memValue.(json.Number).Float64()
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(metricName, "Proxmox node memory metric in bytes", []string{"node"}, nil),
					prometheus.GaugeValue,
					metricValue,
					node.Name,
				)
			}
		}
	}

	// Special handling for CPU metrics
	if cpu, ok := data["cpu"].(json.Number); ok {
		cpuValue, _ := cpu.Float64()
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc("proxmox_node_cpu_usage", "Proxmox node CPU usage", []string{"node"}, nil),
			prometheus.GaugeValue,
			cpuValue,
			node.Name,
		)
	}

	if cpuModel, ok := data["cpuinfo"].(map[string]interface{}); ok {
		if model, ok := cpuModel["model"].(string); ok {
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc("proxmox_node_cpu_model_info", "Proxmox node CPU model information",
					[]string{"node", "model"}, nil),
				prometheus.GaugeValue,
				1,
				node.Name,
				model,
			)
		}

		if cores, ok := cpuModel["cores"].(json.Number); ok {
			coresValue, _ := cores.Float64()
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc("proxmox_node_cpu_cores", "Number of CPU cores in the node",
					[]string{"node"}, nil),
				prometheus.GaugeValue,
				coresValue,
				node.Name,
			)
		}
	}

	if kernel, ok := data["kernel"].(string); ok {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc("proxmox_node_kernel_info", "Proxmox node kernel version",
				[]string{"node", "kernel"}, nil),
			prometheus.GaugeValue,
			1,
			node.Name,
			kernel,
		)
	}

	if pveVersion, ok := data["pveversion"].(string); ok {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc("proxmox_node_pve_version_info", "Proxmox Virtual Environment version",
				[]string{"node", "version"}, nil),
			prometheus.GaugeValue,
			1,
			node.Name,
			pveVersion,
		)
	}

	if loadavg, ok := data["loadavg"].([]interface{}); ok && len(loadavg) >= 3 {
		for i, name := range []string{"proxmox_node_load1", "proxmox_node_load5", "proxmox_node_load15"} {
			if load, ok := loadavg[i].(json.Number); ok {
				loadValue, _ := load.Float64()
				ch <- prometheus.MustNewConstMetric(
					prometheus.NewDesc(name, fmt.Sprintf("Node load average for %s", strings.TrimPrefix(name, "proxmox_node_load")), []string{"node"}, nil),
					prometheus.GaugeValue,
					loadValue,
					node.Name,
				)
			}
		}
	}
}

// emitNodeStorage emits storage metrics of a single node.
func (collector *proxmoxCollector) emitNodeStorage(ch chan<- prometheus.Metric, node ProxmoxNode, nodeStorageResponse map[string]interface{}) {
	data, ok := nodeStorageResponse["data"].([]interface{})
	if !ok {
		return
	}

	for _, item := range data {
		if itemMap, ok := item.(map[string]interface{}); ok {
			storageName := fmt.Sprintf("%v", itemMap["storage"])

			// Collect detailed storage metrics if available
			for key, value := range itemMap {
				if isNumeric(value) {
					metricName := fmt.Sprintf("proxmox_storage_%s", key)
					metricValue, _ := value.(json.Number).Float64()

					ch <- prometheus.MustNewConstMetric(
						prometheus.NewDesc(metricName, "Proxmox storage metric", []string{"storage", "node"}, nil),
						prometheus.GaugeValue,
						metricValue,
						storageName,
						node.Name,
					)
				}
			}
		}
	}
}

// emitNodeVMs emits VM metrics of a single node.
func (collector *proxmoxCollector) emitNodeVMs(ch chan<- prometheus.Metric, node ProxmoxNode, vmsResponse map[string]interface{}) {
	vms, ok := vmsResponse["data"].([]interface{})
	if !ok {
		return
	}

	type vmRef struct {
		vmid string
		name string
	}

	vmRefs := make([]vmRef, 0, len(vms))
	vmids := make([]string, 0, len(vms))
	for _, vm := range vms {
		if vmMap, ok := vm.(map[string]interface{}); ok {
			ref := vmRef{
				vmid: fmt.Sprintf("%v", vmMap["vmid"]),
				name: fmt.Sprintf("%v", vmMap["name"]),
			}
			vmRefs = append(vmRefs, ref)
			vmids = append(vmids, ref.vmid)
		}
	}

	tagsByVMID := collector.resolveVMTags(node.Name, vmids)

	for _, vmRef := range vmRefs {
		// The tag metric is emitted for every VM, with an empty
		// vm_tags label when the VM has no tags.
		if tags, ok := tagsByVMID[vmRef.vmid]; ok {
			ch <- prometheus.MustNewConstMetric(
				prometheus.NewDesc("proxmox_vm_tags", "Proxmox VM tags as a comma separated list",
					[]string{"vmid", "vmname", "node", "vm_tags"}, nil),
				prometheus.GaugeValue,
				1,
				vmRef.vmid,
				vmRef.name,
				node.Name,
				tags,
			)
		}

		vmid := vmRef.vmid
		vmname := vmRef.name

		// Fetch VM status
		vmStatus := collector.fetchJSON("/nodes/" + node.Name + "/qemu/" + vmid + "/status/current")
		if data, ok := vmStatus["data"].(map[string]interface{}); ok {
			for key, value := range data {
				if isNumeric(value) {
					metricName := fmt.Sprintf("proxmox_vm_%s", key)
					metricValue, _ := value.(json.Number).Float64()
					ch <- prometheus.MustNewConstMetric(
						prometheus.NewDesc(metricName, "Proxmox VM metric", []string{"vmid", "vmname", "node"}, nil),
						prometheus.GaugeValue,
						metricValue,
						vmid,
						vmname,
						node.Name,
					)
				}
			}
		}
	}
}

// resolveVMTags returns normalized tags for every requested VM. Fresh cached
// values are reused; stale or unknown VMs are fetched with bounded parallelism.
// VMs whose config request failed are absent from the result, so their previous
// series stay in Prometheus instead of being replaced by an empty label.
func (collector *proxmoxCollector) resolveVMTags(node string, vmids []string) map[string]string {
	now := time.Now()
	tags := make(map[string]string, len(vmids))

	pending := make([]string, 0, len(vmids))
	for _, vmid := range vmids {
		if cached, ok := collector.vmTags.get(node+"/"+vmid, now); ok {
			tags[vmid] = cached
			continue
		}
		pending = append(pending, vmid)
	}

	if len(pending) > 0 {
		var (
			mutex sync.Mutex
			wg    sync.WaitGroup
		)

		semaphore := make(chan struct{}, vmTagFetchWorkers)
		for _, vmid := range pending {
			vmid := vmid

			wg.Add(1)
			semaphore <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-semaphore }()

				raw, ok := collector.fetchVMTags(node, vmid)
				if !ok {
					return
				}
				normalized := normalizeVMTags(raw)
				collector.vmTags.set(node+"/"+vmid, normalized, now)

				mutex.Lock()
				tags[vmid] = normalized
				mutex.Unlock()
			}()
		}
		wg.Wait()
	}

	collector.vmTags.prune(now)
	return tags
}

// fetchVMTags reads the VM config, which is the only Proxmox endpoint that
// exposes tags. It returns the raw tag list and whether the request succeeded.
func (collector *proxmoxCollector) fetchVMTags(node, vmid string) (string, bool) {
	response := collector.fetchJSON("/nodes/" + node + "/qemu/" + vmid + "/config")
	data, ok := response["data"].(map[string]interface{})
	if !ok {
		return "", false
	}

	raw, _ := data["tags"].(string)
	return raw, true
}

// normalizeVMTags turns a Proxmox tag list into a comma separated list. Proxmox
// returns tags separated by semicolons; commas are accepted too because some
// endpoints use them. Duplicates and empty entries are dropped.
func normalizeVMTags(raw string) string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == ','
	})

	seen := make(map[string]struct{}, len(parts))
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}

	return strings.Join(tags, ",")
}

func (collector *proxmoxCollector) fetchJSON(path string) map[string]interface{} {
	req, err := http.NewRequest("GET", collector.baseURL+path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return nil
	}
	req.Header.Add("Authorization", collector.token)

	res, err := collector.client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		return nil
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: read body: %v\n", path, err)
		return nil
	}

	// Proxmox answers with an empty body when pveproxy cannot reach the target
	// node (595/596/598), so report the status instead of a bare JSON EOF.
	if res.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = http.StatusText(res.StatusCode)
		}
		if len(message) > 200 {
			message = message[:200] + "..."
		}
		fmt.Fprintf(os.Stderr, "%s: HTTP %d: %s\n", path, res.StatusCode, message)
		return nil
	}

	response := map[string]interface{}{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&response); err != nil {
		fmt.Fprintf(os.Stderr, "%s: decode body: %v\n", path, err)
		return nil
	}
	return response
}

// getEnv retrieves an environment variable or returns a fallback value
func getEnv(key, fallback string) string {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback
	}
	return value
}

// loadConfig loads configuration from environment variables and command line flags
func loadConfig() Config {
	// Parse command line flags
	flag.Parse()

	// Load configuration from environment variables with defaults
	config := Config{
		ProxmoxAddress:  getEnv("PROXMOX_ADDRESS", "127.0.0.1"),
		ProxmoxPort:     getEnv("PROXMOX_PORT", "8006"),
		ProxmoxUsername: getEnv("PROXMOX_USERNAME", ""),
		ProxmoxToken:    getEnv("PROXMOX_TOKEN", ""),
		LogLevel:        getEnv("LOG_LEVEL", "none"),
		ListenAddress:   getEnv("LISTEN_ADDRESS", "127.0.0.1:9914"),
	}

	// Override with command line flags if provided
	if *listenAddress != "" {
		config.ListenAddress = *listenAddress
	}

	if *logLevel != "" {
		config.LogLevel = *logLevel
	}

	// Validate required configuration
	if config.ProxmoxUsername == "" || config.ProxmoxToken == "" {
		panic("PROXMOX_USERNAME and PROXMOX_TOKEN environment variables must be set")
	}

	return config
}

func main() {
	// Load configuration from environment variables and command line flags
	config := loadConfig()

	// Build the Proxmox API URL and token
	baseURL := "https://" + config.ProxmoxAddress + ":" + config.ProxmoxPort + "/api2/json"
	tokenString := "PVEAPIToken=" + config.ProxmoxUsername + "=" + config.ProxmoxToken

	// Create a collector that refreshes the node list (and the online/offline
	// flag of every node) at most every nodeDiscoveryTTL.
	collector := newProxmoxCollector(baseURL, tokenString, nodeDiscoveryTTL)

	// Register the collector with Prometheus
	prometheus.MustRegister(collector)

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Proxmox Exporter</title></head><body><h1>Proxmox Exporter</h1><a href=\"/metrics\">Metrics</a></body></html>"))
	})

	fmt.Printf("Server is handling requests on address \"%v\"\n", config.ListenAddress)
	http.ListenAndServe(config.ListenAddress, nil)
}
