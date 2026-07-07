# collector/

Split-out ESXi/vCenter Prometheus collector. `collector.go` is the top-level
`prometheus.Collector`; every other file owns one metric domain.

```
collector.go             fan-out, session mgmt, scrapeContext
performance.go           QueryPerf helpers (aggregate + per-instance)
performance_cache.go     counter name → id cache, per session
host.go                  esxi_host_*  (cpu/mem/disk/net perf, runtime)
vm.go                    esxi_vm_*    (cpu/mem/disk/net perf, guest, config)
datastore.go             esxi_datastore_*  (capacity + perf + type)
cluster.go               esxi_cluster_*    (vCenter only)
network.go               esxi_network_*    (vSS/DVS/portgroups)
resourcepool.go          esxi_resource_pool_*
sensors.go               esxi_sensor_*     (CPU/DIMM temp, fans, PSU, RAID, SMART)
events.go                esxi_vcenter_events_*  (counts by category)
alarms.go                esxi_alarm_*      (triggered alarm counts + info)
```

## Design notes

- **Prometheus is the scheduler.** Every scrape opens a session (or reuses
  govmomi's one), pulls the freshest realtime perf sample, and closes.
  No background goroutines, no ticker-driven refresh, no double buffering.
- **`scrapeContext` per scrape** carries `ctx`, `client`, `finder`,
  `viewMgr`, and a fresh `PerfCache`. Sub-collectors are stateless and
  safe to reuse.
- **ContainerView + one `Retrieve`** per domain, then N cheap in-memory
  iterations. Beats N property fetches on large inventories by an order
  of magnitude.
- **Perf counters are queried per-entity** because vSphere throttles
  multi-entity `QueryPerf` requests hard. `PerfCache` still amortizes the
  counter-name → id lookup across every call in a scrape.
- **KB → bytes** conversion happens at emit time. Prometheus convention
  is base units; vSphere returns KB for memory/network throughput and ms
  for latency.
- **vCenter-only collectors** (cluster/rp/events/alarms) fail soft: the
  container view is empty against a bare ESXi endpoint, so we skip the
  loop with no error.

## What you still need to wire

1. **`config.Config.Metrics`** — merge the fields from `config/metrics.go`
   into your existing struct (or embed it). Every new toggle defaults to
   the safe conservative value in the comments there.
2. **YAML defaults** — add the granular toggles to your loader; suggested
   defaults:
   ```yaml
   metrics:
     collect_hardware_sensors: true
     collect_guest_info: true
     collect_vm_disk_details: true
     collect_host_runtime: true
     collect_host_cpu_perf: true
     collect_host_mem_perf: true
     collect_host_disk_perf: true
     collect_host_net_perf: true
     collect_vm_config: false     # expensive on large fleets
     collect_vm_cpu_perf: true
     collect_vm_mem_perf: true
     collect_vm_disk_perf: true
     collect_vm_net_perf: true
     collect_datastore: true
     collect_datastore_perf: true
     collect_cluster: false       # vCenter only
     collect_network: true
     collect_resource_pool: false # vCenter only
     collect_events: false        # vCenter only
     collect_alarms: false        # vCenter only
     events_window: 5m
   ```
3. **govmomi version pin.** A couple of fields (`HostConfigInfo.QuickBootEnabled`,
   `HostCapability.TpmSupported`, `VirtualMachineConfigInfo.KeyId`,
   `HostConfigInfo.HyperThread`) moved around across govmomi minor
   releases. If your pin predates ~`v0.30`, expect one or two `nil`
   guards to need adjusting; the diagnostics from `go build` will point
   right at them.

## Perf intervals

vSphere accepts a fixed set of `IntervalId` values per entity type. Getting
this wrong isn't a soft failure — the whole query fails with
`ServerFaultCode: A specified parameter was not correct: querySpec.interval`.

| Entity                    | Realtime (20s) | Historic (300s+) | Where lives |
|---------------------------|:--------------:|:----------------:|-------------|
| `HostSystem`              | ✅             | ✅               | ESXi + vCenter |
| `VirtualMachine`          | ✅             | ✅               | ESXi + vCenter |
| `Datastore`               | ❌             | ✅               | vCenter only |
| `ClusterComputeResource`  | ❌             | ✅               | vCenter only |
| `ResourcePool`            | ❌             | ✅               | vCenter only |

The two helpers in `performance.go` take an explicit `interval` argument;
use `IntervalRealtime` for host/vm and `IntervalHistoric` for everything
else. Historic values lag ~5 min but that's the best the API offers.

## Duplicate series

The collector runs sub-collectors once per configured endpoint. If your
`cfg.Hosts` lists the same vCenter twice, or lists a vCenter plus one of
its managed ESXi hosts, the raw metric stream will contain duplicates for
overlapping inventory. `collector.go` funnels every metric through a
`dedupChannel` that drops repeats within a single scrape, so Prometheus
sees a clean stream.

That's a safety net, **not** an efficiency win — you still pay the network
cost twice. If you see lots of drops, list each vCenter/ESXi only once
in your config.

Common source-level duplicates that are handled at the emit site (so we
also save bandwidth downstream):

- `esxi_vm_guest_ip_info` — primary `Guest.IpAddress` is repeated inside
  `Guest.Net[].IpAddress`; dedupd per-VM.
- `esxi_vm_datastore_info` — same datastore listed twice for shared VMs.
- `esxi_alarm_count` — same alarm surfacing via multiple parent entities.
- `esxi_resource_pool_*` — every host has a root pool literally named
  `"Resources"`, disambiguated with an added `owner` label.

## Cardinality

The heavy hitters, in descending order:

- `esxi_vm_disk_*` — one series per VM × vDisk × metric
- `esxi_vm_network_*` — one series per VM × vNIC × metric
- `esxi_host_disk_*` — one series per host × device × metric
- `esxi_sensor_*` — one series per sensor (100-500 per host)

If your Prometheus starts sweating, disable the per-instance perf toggles
first (`collect_vm_disk_perf`, `collect_host_disk_perf`) — they explode
much faster than the aggregate ones.
