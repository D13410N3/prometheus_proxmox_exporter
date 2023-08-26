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
	configFile    = flag.String("config.file", "/etc/proxmox_exporter.yaml", "Path to a configuration file")
	listenAddress = flag.String("listen.address", "127.0.0.1:9914", "Address to bind")
	logLevel      = flag.String("log.level", "none", "Logging level")
)

func isNumeric(s interface{}) bool {
	_, ok := s.(json.Number)
	return ok
}

type proxmoxCollector struct {
	config map[string]string
}

func (collector *proxmoxCollector) Describe(ch chan<- *prometheus.Desc) {
	// Intentionally empty.
}

func (collector *proxmoxCollector) Collect(ch chan<- prometheus.Metric) {
	token_string := "PVEAPIToken=" + collector.config["proxmox_username"] + "=" + collector.config["proxmox_token"]
	proxmox_api_url_nodes := "https://" + collector.config["proxmox_ip"] + ":" + collector.config["proxmox_port"] + "/api2/json/nodes/" + collector.config["proxmox_node"] + "/qemu"

	client := &http.Client{}
	req, _ := http.NewRequest("GET", proxmox_api_url_nodes, nil)
	req.Header.Add("Authorization", token_string)

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}

	m := map[string][]map[string]interface{}{}
	dec := json.NewDecoder(res.Body)
	dec.UseNumber()
	err = dec.Decode(&m)
	if err != nil {
		fmt.Println(err)
		return
	}

    for _, v := range m["data"] {
        vmid := fmt.Sprintf("%v", v["vmid"])
        vmname := fmt.Sprintf("%v", v["name"])
        for key, value := range v {
            if isNumeric(value) {
                metricName := fmt.Sprintf("proxmox_vm_%s", key)
                metricValue, _ := value.(json.Number).Float64()
                ch <- prometheus.MustNewConstMetric(
                    prometheus.NewDesc(metricName, "Proxmox metric", []string{"vmid", "vmname"}, nil),
                    prometheus.GaugeValue,
                    metricValue,
                    vmid,
                    vmname,
                )
            }
        }
    }
}

func main() {
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	flag.Parse()

	filename, _ := filepath.Abs(*configFile)
	yamlFile, err := ioutil.ReadFile(filename)
	if err != nil {
		panic(err)
	}

	y := map[string]string{}
	err = yaml.Unmarshal(yamlFile, &y)

	prometheus.MustRegister(&proxmoxCollector{config: y})

	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Proxmox Exporter</title></head><body><h1>Proxmox Exporter</h1><a href=\"/metrics\">Metrics</a></body></html>"))
	})

	fmt.Printf("Server is handling requests on address \"%v\"\n", *listenAddress)
	http.ListenAndServe(*listenAddress, nil)
}
