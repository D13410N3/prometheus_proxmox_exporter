# The simplest Prometheus exporter for Proxmox

## Available Metrics

### VM Metrics

| Metric | Description | Type | Labels |
|--------|-------------|------|--------|
| `proxmox_vm_status` | VM running status (1=running, 0=stopped) | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_diskwrite` | Disk write throughput in bytes per second | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_diskread` | Disk read throughput in bytes per second | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_vmid` | VM ID number | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_pid` | Process ID of the VM | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_cpus` | Number of virtual CPUs allocated to the VM | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_cpu` | CPU usage percentage | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_netout` | Network output in bytes per second | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_netin` | Network input in bytes per second | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_maxdisk` | Maximum disk size in bytes | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_disk` | Current disk usage in bytes | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_mem` | Current memory usage in bytes | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_maxmem` | Maximum memory allocated in bytes | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_uptime` | VM uptime in seconds | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_balloon` | Ballooned memory (if enabled) in bytes | Gauge | `vmid`, `vmname`, `proxmox_node` |
| `proxmox_vm_running` | Whether the VM is currently running (1=yes, 0=no) | Gauge | `vmid`, `vmname`, `proxmox_node` |

### Node Metrics

| Metric | Description | Type | Labels |
|--------|-------------|------|--------|
| `proxmox_node_cpu_usage` | CPU usage percentage | Gauge | `node` |
| `proxmox_node_memory_total_bytes` | Total memory in bytes | Gauge | `node` |
| `proxmox_node_memory_free_bytes` | Free memory in bytes | Gauge | `node` |
| `proxmox_node_memory_used_bytes` | Used memory in bytes | Gauge | `node` |
| `proxmox_node_cpu_model_info` | CPU model information | Gauge | `node`, `model` |
| `proxmox_node_cpu_cores` | Number of CPU cores in the node | Gauge | `node` |
| `proxmox_node_kernel_info` | Kernel version information | Gauge | `node`, `kernel` |
| `proxmox_node_pve_version_info` | Proxmox Virtual Environment version | Gauge | `node`, `version` |
| `proxmox_node_load1` | Node load average for 1 minute | Gauge | `node` |
| `proxmox_node_load5` | Node load average for 5 minutes | Gauge | `node` |
| `proxmox_node_load15` | Node load average for 15 minutes | Gauge | `node` |
| `proxmox_node_uptime` | Node uptime in seconds | Gauge | `node` |
| `proxmox_node_swap_total_bytes` | Total swap space in bytes | Gauge | `node` |
| `proxmox_node_swap_free_bytes` | Free swap space in bytes | Gauge | `node` |

### Storage Metrics

| Metric | Description | Type | Labels |
|--------|-------------|------|--------|
| `proxmox_storage_info` | Information about Proxmox storages | Gauge | `storage`, `type`, `shared`, `node` |
| `proxmox_storage_type_count` | Count of storages by type | Gauge | `type` |
| `proxmox_storage_total` | Total storage space in bytes | Gauge | `storage`, `node` |
| `proxmox_storage_used` | Used storage space in bytes | Gauge | `storage`, `node` |
| `proxmox_storage_avail` | Available storage space in bytes | Gauge | `storage`, `node` |

### Cluster Metrics

| Metric | Description | Type | Labels |
|--------|-------------|------|--------|
| `proxmox_cluster_quorate` | Cluster quorum status (1=quorate, 0=not quorate) | Gauge | `node_id`, `node_name` |
| `proxmox_cluster_nodes` | Number of nodes in the cluster | Gauge | `node_id`, `node_name` |

### Installation:

#### Building from source:
```bash
go build
```

#### Using Docker:
*WARNING! Do not run docker directly on proxmox-node - it may (and will) break your cluster networking!*

```bash
# Build the Docker image
docker build -t proxmox-exporter .

# Run the container
docker run -d \
  -p 9914:9914 \
  -e PROXMOX_ADDRESS=your-proxmox-server \
  -e PROXMOX_PORT=8006 \
  -e PROXMOX_USERNAME=root@pam!username \
  -e PROXMOX_TOKEN=your-token-here \
  --name proxmox-exporter \
  proxmox-exporter
```

#### Using Docker Compose:
```bash
# Edit the docker-compose.yml file to set your Proxmox credentials
# Then run:
docker-compose up -d
```

### Configuration:
This exporter uses environment variables for configuration:

**Required Environment Variables:**
* `PROXMOX_USERNAME` - Proxmox API token username (e.g., `root@pam!username`)
* `PROXMOX_TOKEN` - Proxmox API token value

**Optional Environment Variables:**
* `PROXMOX_ADDRESS` - Proxmox server address (default: `127.0.0.1`)
* `PROXMOX_PORT` - Proxmox API port (default: `8006`)
* `LISTEN_ADDRESS` - Address to bind the exporter (default: `127.0.0.1:9914`)
* `LOG_LEVEL` - Logging level (default: `none`)

### Running:
```bash
# Set required environment variables
export PROXMOX_USERNAME="root@pam!username"
export PROXMOX_TOKEN="12345678-90ab-cdef-1234-567890abcdef"

# Optional: override defaults
export PROXMOX_ADDRESS="proxmox.example.com"
export PROXMOX_PORT="8006"
export LISTEN_ADDRESS="0.0.0.0:9914"

# Run the exporter
./proxmox_exporter
```

Available flags (override environment variables):
* `-listen.address` - address to bind (overrides `LISTEN_ADDRESS` environment variable)
* `-log.level` - logging level (overrides `LOG_LEVEL` environment variable)
