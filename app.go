package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"
)

var (
	configFile    = flag.String("config.file", "./config.yaml", "Path to a configuration file")
	listenAddress = flag.String("listen.address", "127.0.0.1:9914", "Address to bind")
	logLevel      = flag.String("log.level", "none", "Logging level")
)

func isNumeric(s interface{}) bool {
	_, ok := s.(json.Number)
	return ok
}

type proxmoxCollector struct {
	config   map[string]string
	baseURL  string
	token    string
}

func (collector *proxmoxCollector) Describe(ch chan<- *prometheus.Desc) {
	// Intentionally empty.
}

func (collector *proxmoxCollector) Collect(ch chan<- prometheus.Metric) {
	client := &http.Client{}

	// Collect cluster metrics
	clusterStatus := collector.fetchJSON(client, "/cluster/status")
	if data, ok := clusterStatus["data"].([]interface{}); ok {
		for _, item := range data {
			if itemMap, ok := item.(map[string]interface{}); ok {
				for key, value := range itemMap {
					if isNumeric(value) {
						metricName := fmt.Sprintf("proxmox_cluster_%s", key)
						metricValue, _ := value.(json.Number).Float64()
						ch <- prometheus.MustNewConstMetric(
							prometheus.NewDesc(metricName, "Proxmox cluster metric", nil, nil),
							prometheus.GaugeValue,
							metricValue,
						)
					}
				}
			}
		}
	}

	// Collect storage metrics
	storageResponse := collector.fetchJSON(client, "/storage")
	if data, ok := storageResponse["data"].([]interface{}); ok {
		storageTypeCount := make(map[string]int)
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
					prometheus.NewDesc("proxmox_storage_info", "Information about Proxmox storages", []string{"storage", "type", "shared"}, nil),
					prometheus.GaugeValue,
					1,
					storageName,
					storageType,
					isShared,
				)
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

	// Collect node and VM metrics
	nodesResponse := collector.fetchJSON(client, "/nodes")
	if nodes, ok := nodesResponse["data"].([]interface{}); ok {
		for _, node := range nodes {
			if nodeMap, ok := node.(map[string]interface{}); ok {
				nodeName := fmt.Sprintf("%v", nodeMap["node"])

				// Collect node metrics
				nodeStatus := collector.fetchJSON(client, "/nodes/"+nodeName+"/status")
				if data, ok := nodeStatus["data"].(map[string]interface{}); ok {
					if cpu, ok := data["cpu"].(json.Number); ok {
						cpuValue, _ := cpu.Float64()
						ch <- prometheus.MustNewConstMetric(
							prometheus.NewDesc("proxmox_node_cpu_usage", "Proxmox node CPU usage", []string{"node"}, nil),
							prometheus.GaugeValue,
							cpuValue,
							nodeName,
						)
					}
					if memory, ok := data["memory"].(map[string]interface{}); ok {
						memTotal, _ := memory["total"].(json.Number).Float64()
						memFree, _ := memory["free"].(json.Number).Float64()
						ch <- prometheus.MustNewConstMetric(
							prometheus.NewDesc("proxmox_node_memory_total_bytes", "Proxmox node total memory in bytes", []string{"node"}, nil),
							prometheus.GaugeValue,
							memTotal,
							nodeName,
						)
						ch <- prometheus.MustNewConstMetric(
							prometheus.NewDesc("proxmox_node_memory_free_bytes", "Proxmox node free memory in bytes", []string{"node"}, nil),
							prometheus.GaugeValue,
							memFree,
							nodeName,
						)
					}
				}

				// Collect VM metrics
				vmsResponse := collector.fetchJSON(client, "/nodes/"+nodeName+"/qemu")
				if vms, ok := vmsResponse["data"].([]interface{}); ok {
					for _, vm := range vms {
						if vmMap, ok := vm.(map[string]interface{}); ok {
							vmid := fmt.Sprintf("%v", vmMap["vmid"])
							vmname := fmt.Sprintf("%v", vmMap["name"])

							// Fetch VM status
							vmStatus := collector.fetchJSON(client, "/nodes/"+nodeName+"/qemu/"+vmid+"/status/current")
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
											nodeName,
										)
									}
								}
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

func main() {
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	flag.Parse()

	filename, _ := filepath.Abs(*configFile)
	yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
		panic(err)
	}

	config := map[string]string{}
	err = yaml.Unmarshal(yamlFile, &config)
	if err != nil {
		panic(err)
	}

	baseURL := "https://" + config["proxmox_address"] + ":" + config["proxmox_port"] + "/api2/json"
	token := "PVEAPIToken=" + config["proxmox_username"] + "=" + config["proxmox_token"]

	prometheus.MustRegister(&proxmoxCollector{config: config, baseURL: baseURL, token: token})

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Proxmox Exporter</title></head><body><h1>Proxmox Exporter</h1><a href=\"/metrics\">Metrics</a></body></html>"))
	})

	fmt.Printf("Server is handling requests on address \"%v\"\n", *listenAddress)
	http.ListenAndServe(*listenAddress, nil)
}
