package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestNormalizeVMTags(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "semicolons", raw: "important;prod;backup", want: "important,prod,backup"},
		{name: "commas", raw: "important,prod", want: "important,prod"},
		{name: "mixed separators", raw: "important;prod,backup", want: "important,prod,backup"},
		{name: "spaces and empty entries", raw: " important ; ; prod ;", want: "important,prod"},
		{name: "duplicates", raw: "prod;important;prod", want: "prod,important"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeVMTags(test.raw); got != test.want {
				t.Fatalf("normalizeVMTags(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

func TestVMTagCache(t *testing.T) {
	cache := newVMTagCache(time.Minute)
	now := time.Now()

	if _, ok := cache.get("pm1/100", now); ok {
		t.Fatal("empty cache returned a value")
	}

	cache.set("pm1/100", "important", now)
	if tags, ok := cache.get("pm1/100", now.Add(30*time.Second)); !ok || tags != "important" {
		t.Fatalf("fresh entry = %q, %v; want %q, true", tags, ok, "important")
	}
	if _, ok := cache.get("pm1/100", now.Add(2*time.Minute)); ok {
		t.Fatal("expired entry was returned")
	}

	cache.prune(now.Add(10 * time.Minute))
	if len(cache.entries) != 0 {
		t.Fatalf("prune left %d entries, want 0", len(cache.entries))
	}
}

func newTestCollector(baseURL string) *proxmoxCollector {
	return &proxmoxCollector{
		baseURL:      baseURL,
		token:        "PVEAPIToken=test",
		client:       &http.Client{Timeout: time.Second},
		nodes:        []ProxmoxNode{{Name: "pm1", Online: true}},
		discovered:   time.Now(),
		discoveryTTL: time.Hour,
		vmTags:       newVMTagCache(time.Minute),
	}
}

func newTestServer(t *testing.T, configHits *int32) *httptest.Server {
	t.Helper()

	responses := map[string]string{
		"/cluster/status":                    `{"data":[{"type":"cluster","name":"test","nodes":1,"quorate":1}]}`,
		"/nodes/pm1/status":                  `{"data":{"status":"online"}}`,
		"/nodes/pm1/storage":                 `{"data":[]}`,
		"/storage":                           `{"data":[]}`,
		"/nodes/pm1/qemu":                    `{"data":[{"vmid":100,"name":"tagged"},{"vmid":101,"name":"untagged"}]}`,
		"/nodes/pm1/qemu/100/config":         `{"data":{"tags":"important;prod"}}`,
		"/nodes/pm1/qemu/101/config":         `{"data":{"name":"untagged"}}`,
		"/nodes/pm1/qemu/100/status/current": `{"data":{"status":"running","pid":42,"uptime":100}}`,
		"/nodes/pm1/qemu/101/status/current": `{"data":{"status":"stopped","uptime":0}}`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) > len("/config") && r.URL.Path[len(r.URL.Path)-len("/config"):] == "/config" {
			atomic.AddInt32(configHits, 1)
		}

		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			body = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func gatherTagSamples(t *testing.T, collector *proxmoxCollector) map[string]string {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() != "proxmox_vm_tags" {
			continue
		}
		samples := make(map[string]string, len(family.GetMetric()))
		for _, metric := range family.GetMetric() {
			if metric.GetGauge().GetValue() != 1 {
				t.Fatalf("proxmox_vm_tags value = %v, want 1", metric.GetGauge().GetValue())
			}
			samples[labelValue(metric, "vmid")] = labelValue(metric, "vm_tags")
		}
		return samples
	}

	t.Fatal("proxmox_vm_tags metric family not found")
	return nil
}

func labelValue(metric *dto.Metric, name string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

func TestCollectorEmitsVMTags(t *testing.T) {
	var configHits int32
	server := newTestServer(t, &configHits)
	defer server.Close()

	collector := newTestCollector(server.URL)
	got := gatherTagSamples(t, collector)

	want := map[string]string{
		"100": "important,prod",
		"101": "",
	}
	for vmid, tags := range want {
		value, ok := got[vmid]
		if !ok {
			t.Fatalf("no proxmox_vm_tags series for vmid %s", vmid)
		}
		if value != tags {
			t.Fatalf("proxmox_vm_tags{vm_tags} for vmid %s = %q, want %q", vmid, value, tags)
		}
	}

	if configHits != 2 {
		t.Fatalf("config requests = %d, want 2", configHits)
	}

	// A second scrape must reuse the cache instead of asking Proxmox again.
	_ = gatherTagSamples(t, collector)
	if configHits != 2 {
		t.Fatalf("config requests after cached scrape = %d, want 2", configHits)
	}
}

// TestCollectorSkipsOfflineNodes makes sure a powered off (but still present in
// the cluster) node is never queried: pveproxy only fails for it after a
// connect timeout, which is what made scrapes exceed the scrape timeout.
func TestCollectorSkipsOfflineNodes(t *testing.T) {
	var offlineHits int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/nodes/pm2/") {
			atomic.AddInt32(&offlineHits, 1)
			w.WriteHeader(595)
			return
		}

		responses := map[string]string{
			"/cluster/status":    `{"data":[{"type":"cluster","name":"test","nodes":2,"quorate":1}]}`,
			"/storage":           `{"data":[]}`,
			"/nodes/pm1/status":  `{"data":{"status":"online"}}`,
			"/nodes/pm1/storage": `{"data":[]}`,
			"/nodes/pm1/qemu":    `{"data":[]}`,
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request path %q", r.URL.Path)
			body = `{}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	collector := newTestCollector(server.URL)
	collector.nodes = []ProxmoxNode{
		{Name: "pm1", Online: true},
		{Name: "pm2", Online: false},
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	if offlineHits != 0 {
		t.Fatalf("offline node was queried %d times, want 0", offlineHits)
	}

	found := false
	for _, family := range families {
		if family.GetName() != "proxmox_node_status" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if labelValue(metric, "node") == "pm2" {
				found = true
				if value := metric.GetGauge().GetValue(); value != 0 {
					t.Fatalf("proxmox_node_status for offline node = %v, want 0", value)
				}
			}
		}
	}
	if !found {
		t.Fatal("no proxmox_node_status series for the offline node")
	}
}
