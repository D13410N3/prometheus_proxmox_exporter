package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"
   "time"

	"gopkg.in/yaml.v2"
)

var (
	configFile *string
	listenAddress *string
	logLevel *string
)

func isNumeric(s interface{}) bool {
	_, ok := s.(json.Number)
	return ok
}

func init() {
	configFile = flag.String("config.file", "./config.yml", "a string")
	listenAddress = flag.String("listen.address", ":9914", "a string")
   logLevel = flag.String("log.level", "none", "a string")
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

	token_string := "PVEAPIToken=" + y["proxmox_username"] + "=" + y["proxmox_token"]
	proxmox_api_url_nodes := "https://" + y["proxmox_ip"] + ":" + y["proxmox_port"] + "/api2/json/nodes/" + y["proxmox_node"] + "/qemu"

	client := &http.Client{}
	req, _ := http.NewRequest("GET", proxmox_api_url_nodes, nil)
	req.Header.Add("Authorization", token_string)

	mux := http.NewServeMux()

   fmt.Printf("Server is handling requests on address \"%v\"\n", *listenAddress)

	mux.HandleFunc("/metrics", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      dt := time.Now().Format(time.RFC3339)
      if *logLevel == "debug" {
         fmt.Printf("%v Handling metrics-request from %v\n", dt, r.RemoteAddr)
      }
		res, err := client.Do(req)
      if err != nil {
         fmt.Println(err)
      }

		m := map[string][]map[string]interface{}{}
		dec := json.NewDecoder(res.Body)
		dec.UseNumber()

		err = dec.Decode(&m)
      if err != nil {
         fmt.Println(err)
      }

		var output string
		var str string
		var str_status string

		for _, v := range m["data"] {
			vmid := v["vmid"]
			vmname := v["name"]
			var vmstatus string

			output += fmt.Sprintf("\n# %v: %v\n", vmid, vmname)

			if v["status"] == "running" {
				vmstatus = "1"
			} else {
				vmstatus = "0"
			}

			str_status = fmt.Sprintf("proxmox_vm_status{vmid=\"%v\", vmname=\"%v\"} %v\n", vmid, vmname, vmstatus)
			output += str_status

			for key, value := range v {

				if isNumeric(value) {
					str = fmt.Sprintf("proxmox_vm_%v{vmid=\"%v\", vmname=\"%v\"} %v\n", key, vmid, vmname, value)
				} else if key != "status" {
					str = fmt.Sprintf("proxmox_vm_%v{vmid=\"%v\", vmname=\"%v\", value=\"%v\"} 1\n", key, vmid, vmname, value)
				}
				output += str
			}
		}

		w.Header().Set("Content-type", "text/plain")
		w.Write([]byte(output))
	}))

   mux.HandleFunc("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
      w.Header().Set("Content-type", "text/html")
      w.Write([]byte("<html><head><title>Proxmox exporter</title></head><body><h1>Proxmox exporter</h1><a href=\"/metrics\">Metrics</a></bidy></html>"))
   }))

	err = http.ListenAndServe(*listenAddress, mux)
   if err != nil {
      panic(err)
   }

}
