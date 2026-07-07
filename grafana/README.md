# Grafana dashboard for the ESXi exporter

`esxi-dashboard.json` — single-file dashboard covering everything the
exporter produces.

## Import

1. Grafana → Dashboards → **New** → **Import**.
2. Upload `esxi-dashboard.json` (or paste the contents).
3. Pick your Prometheus datasource when prompted.
4. Save.

## Structure

| Row | Default | Panels |
|---|---|---|
| Overview | expanded | Inventory stats, cluster CPU/mem trends |
| Host — CPU | expanded | Usage %, ready time, wait/idle, utilization |
| Host — Memory | collapsed | Usage, active, balloon/swap/compressed, pressure state |
| Host — Disk | collapsed | Read/write latency, IOPS, throughput |
| Host — Network | collapsed | Throughput, link state, drops, speeds |
| Host — Runtime & Hardware | collapsed | Inventory table, reboot/lockdown/HT/SB, uptime |
| Hardware Sensors | collapsed | CPU/DIMM temp, fans, voltage, non-green sensors |
| VMs — CPU & Memory | expanded | Top-10 by usage/ready/costop/balloon/swap |
| VMs — Disk & Network | collapsed | Top-10 by IOPS/latency/throughput/drops |
| VMs — Inventory & Guest | collapsed | Tools status, heartbeat, FS >85%, snapshots, IPs |
| Datastores | expanded | Free %, capacity vs used, latency, IOPS |
| Cluster & Resource Pools | collapsed | HA/DRS state, cluster utilization, RP usage |
| Alarms & Events | expanded | Red/yellow counts, firing alarms table, event window |

## Template variables

- **Datasource** — Prometheus datasource picker.
- **Host** — multi-select, populated from `esxi_host_power_state`.
- **VM** — multi-select, scoped by the currently selected hosts.

All host/vm panels use `host=~"$host"` and `vm=~"$vm"` matchers, so
filtering cascades correctly.

## Notes on units and quirks

- **`esxi_host_cpu_utilization_percent`** — the raw counter is in
  hundredths of a percent (vSphere convention). Panels divide by 100.
- **Datastore latency panels** — 5-min rollup only (vSphere doesn't
  expose realtime for datastore/cluster/RP entities). Values may lag
  behind current I/O by up to 5 minutes.
- **`esxi_vm_cpu_usage_percent`** — despite the name this is MHz, not
  a percent (the exporter comment notes this). Divide by
  `esxi_vm_num_cpu * host_mhz` if you need true %.
- **Alarms table `status` column** — cell background is colored by
  status value (red/yellow/green).

## Regenerating

The dashboard is built by `build_dashboard.py`. Edit that file to add
panels or reorganize — hand-editing the 129 KB JSON is not recommended.

```
python3 build_dashboard.py
```
