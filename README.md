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
`go build`

### Configuration:
Use `config.sample.yaml` as example

### Running:
`./proxmox_exporter`

Available flags:
* `-listen.address` - address to bind (default: `:9914`)
* `-config.file` - path to a configuration file (default: `/etc/proxmox_exporter.yaml`)
* `-log.level` - logging level (default: `none`)
