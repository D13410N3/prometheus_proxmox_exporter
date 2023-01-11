# The simplest Prometheus exporter for Proxmox

### Available metrics:
* proxmox_vm_status
* proxmox_vm_diskwrite
* proxmox_vm_vmid
* proxmox_vm_pid
* proxmox_vm_cpus
* proxmox_vm_cpu
* proxmox_vm_netout
* proxmox_vm_maxdisk
* proxmox_vm_name
* proxmox_vm_diskread
* proxmox_vm_mem
* proxmox_vm_netin
* proxmox_vm_maxmem
* proxmox_vm_uptime
* proxmox_vm_disk

### Installation:
Download [preferred binary](https://github.com/D13410N3/proxmox_exporter/releases/tag/release) or clone this repository & build it by yourself with `go build`

### Configuration:
Use `config.sample.yaml` as example

### Running:
`./proxmox_exporter`

Available flags:
* `-listen.address` - address to bind (default: `:9914`)
* `-config.file` - path to a configuration file (default: `./config.yaml`)
* `-log.level` - logging level (default: `none`)

You can also use `proxmox_exporter.service` as ready unit-file if you're using systemd

### TODO (contact me if you need it):
* Support multiple "datacenters"
* Support CT
