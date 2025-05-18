package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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
)

func isNumeric(s interface{}) bool {
	_, ok := s.(json.Number)
	return ok
}

// ProxmoxNode represents a node in the Proxmox cluster
type ProxmoxNode struct {
	Name string
	ID   string
}

// proxmoxCollector implements the prometheus.Collector interface
type proxmoxCollector struct {
	baseURL           string
	token             string
	nodes             []ProxmoxNode
	nodesMutex        sync.RWMutex
	discoveryInterval time.Duration
}

// newProxmoxCollector creates a new proxmoxCollector
func newProxmoxCollector(baseURL, token string, discoveryInterval time.Duration) *proxmoxCollector {
	collector := &proxmoxCollector{
		baseURL:           baseURL,
		token:             token,
		nodes:             []ProxmoxNode{},
		discoveryInterval: discoveryInterval,
	}

	// Start node discovery in background
	go collector.startNodeDiscovery()

	return collector
}

// startNodeDiscovery periodically discovers nodes in the Proxmox cluster
func (collector *proxmoxCollector) startNodeDiscovery() {
	// Do initial discovery
	collector.discoverNodes()

	// Set up ticker for periodic discovery
	ticker := time.NewTicker(collector.discoveryInterval)
	defer ticker.Stop()

	for range ticker.C {
		collector.discoverNodes()
	}
}

// discoverNodes fetches the list of nodes from the Proxmox API
func (collector *proxmoxCollector) discoverNodes() {
	client := &http.Client{}
	nodesResponse := collector.fetchJSON(client, "/nodes")

	if nodes, ok := nodesResponse["data"].([]interface{}); ok {
		discoveredNodes := []ProxmoxNode{}

		for _, node := range nodes {
			if nodeMap, ok := node.(map[string]interface{}); ok {
				nodeName := fmt.Sprintf("%v", nodeMap["node"])
				nodeID := fmt.Sprintf("%v", nodeMap["id"])

				discoveredNodes = append(discoveredNodes, ProxmoxNode{
					Name: nodeName,
					ID:   nodeID,
				})
			}
		}

		// Update the nodes list with write lock
		collector.nodesMutex.Lock()
		collector.nodes = discoveredNodes
		collector.nodesMutex.Unlock()

		fmt.Printf("Discovered %d nodes in Proxmox cluster\n", len(discoveredNodes))
	}
}

func (collector *proxmoxCollector) Describe(ch chan<- *prometheus.Desc) {
	// Intentionally empty.
}

func (collector *proxmoxCollector) Collect(ch chan<- prometheus.Metric) {
	client := &http.Client{}

	// Collect cluster metrics
	collector.nodesMutex.RLock()
	nodes := collector.nodes
	collector.nodesMutex.RUnlock()

	for _, node := range nodes {
		clusterStatus := collector.fetchJSON(client, "/nodes/"+node.ID+"/status")
		if data, ok := clusterStatus["data"].(map[string]interface{}); ok {
			for key, value := range data {
				if isNumeric(value) {
					metricName := fmt.Sprintf("proxmox_cluster_%s", key)
					metricValue, _ := value.(json.Number).Float64()

					// Create labels map to uniquely identify the metric
					labels := []string{"node_id", "node_name"}
					labelValues := []string{node.ID, node.Name}

					ch <- prometheus.MustNewConstMetric(
						prometheus.NewDesc(metricName, "Proxmox cluster metric", labels, nil),
						prometheus.GaugeValue,
						metricValue,
						labelValues...,
					)
				}
			}
		}
	}

	// Collect storage metrics for each node
	collector.nodesMutex.RLock()
	// Use a different variable name to avoid redeclaration
	allNodes := collector.nodes
	collector.nodesMutex.RUnlock()

	// Global storage type counter across all nodes
	storageTypeCount := make(map[string]int)

	// Collect cluster-wide storage info first
	storageResponse := collector.fetchJSON(client, "/storage")
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

	// Now collect node-specific storage metrics
	for _, node := range allNodes {
		nodeStorageResponse := collector.fetchJSON(client, "/nodes/"+node.Name+"/storage")
		if data, ok := nodeStorageResponse["data"].([]interface{}); ok {
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
	}

	// Output storage type counts
	for storageType, count := range storageTypeCount {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc("proxmox_storage_type_count", "Count of storages by type", []string{"type"}, nil),
			prometheus.GaugeValue,
			float64(count),
			storageType,
		)
	}

	// Collect node and VM metrics using our discovered nodes
	for _, node := range allNodes {
		// Collect node metrics
		nodeStatus := collector.fetchJSON(client, "/nodes/"+node.Name+"/status")
		if data, ok := nodeStatus["data"].(map[string]interface{}); ok {
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
		}

		// Collect VM metrics
		vmsResponse := collector.fetchJSON(client, "/nodes/"+node.Name+"/qemu")
		if vms, ok := vmsResponse["data"].([]interface{}); ok {
			for _, vm := range vms {
				if vmMap, ok := vm.(map[string]interface{}); ok {
					vmid := fmt.Sprintf("%v", vmMap["vmid"])
					vmname := fmt.Sprintf("%v", vmMap["name"])

					// Fetch VM status
					vmStatus := collector.fetchJSON(client, "/nodes/"+node.Name+"/qemu/"+vmid+"/status/current")
					if data, ok := vmStatus["data"].(map[string]interface{}); ok {
						for key, value := range data {
							if isNumeric(value) {
								metricName := fmt.Sprintf("proxmox_vm_%s", key)
								metricValue, _ := value.(json.Number).Float64()
								ch <- prometheus.MustNewConstMetric(
									prometheus.NewDesc(metricName, "Proxmox VM metric", []string{"vmid", "vmname", "proxmox_node"}, nil),
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
		}
	}
}

func (collector *proxmoxCollector) fetchJSON(client *http.Client, path string) map[string]interface{} {
	req, _ := http.NewRequest("GET", collector.baseURL+path, nil)
	req.Header.Add("Authorization", collector.token)

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return nil
	}
	defer res.Body.Close()

	response := map[string]interface{}{}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	err = dec.Decode(&response)
	if err != nil {
		fmt.Println(err)
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
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	// Load configuration from environment variables and command line flags
	config := loadConfig()

	// Build the Proxmox API URL and token
	baseURL := "https://" + config.ProxmoxAddress + ":" + config.ProxmoxPort + "/api2/json"
	tokenString := "PVEAPIToken=" + config.ProxmoxUsername + "=" + config.ProxmoxToken

	// Create a collector with node discovery every 5 minutes
	collector := newProxmoxCollector(baseURL, tokenString, 5*time.Minute)

	// Register the collector with Prometheus
	prometheus.MustRegister(collector)

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Proxmox Exporter</title></head><body><h1>Proxmox Exporter</h1><a href=\"/metrics\">Metrics</a></body></html>"))
	})

	fmt.Printf("Server is handling requests on address \"%v\"\n", config.ListenAddress)
	http.ListenAndServe(config.ListenAddress, nil)
}
