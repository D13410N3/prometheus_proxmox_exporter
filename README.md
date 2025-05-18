# The simplest Prometheus exporter for Proxmox

### Available metrics
## VM Metrics
  * `proxmox_vm_status`
  * `proxmox_vm_diskwrite`
  * `proxmox_vm_vmid`
  * `proxmox_vm_pid`
  * `proxmox_vm_cpus`
  * `proxmox_vm_cpu`
  * `proxmox_vm_netout`
  * `proxmox_vm_maxdisk`
  * `proxmox_vm_name`
  * `proxmox_vm_diskread`
  * `proxmox_vm_mem`
  * `proxmox_vm_netin`
  * `proxmox_vm_maxmem`
  * `proxmox_vm_uptime`
  * `proxmox_vm_disk`

## Node Metrics
  * `proxmox_node_cpu_usage`
  * `proxmox_node_memory_total_bytes`
  * `proxmox_node_memory_free_bytes`

## Storage Metrics
  * `proxmox_storage_info` - Information about Proxmox storages (labels: `storage`, `type`, `shared`)
  * `proxmox_storage_type_count` - Count of storages by type (labels: `type`)

## Cluster Metrics
  * `proxmox_cluster_*` - Various cluster metrics based on the Proxmox API response


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
