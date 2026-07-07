package collector

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// defaultCollectTimeout bounds a full Collect() call so an unreachable
// ESXi/vCenter host can't hang Prometheus scrapes indefinitely.
const defaultCollectTimeout = 30 * time.Second

type ESXiCollector struct {
	cfg *config.Config

	// ===== HOST METRICS =====
	hostCPUUsage          *prometheus.Desc
	hostMemoryUsage       *prometheus.Desc
	hostMemoryTotal       *prometheus.Desc
	hostUptime            *prometheus.Desc
	hostPowerState        *prometheus.Desc
	hostMaintenanceMode   *prometheus.Desc
	hostConnectionState   *prometheus.Desc
	hostNumCPU            *prometheus.Desc
	hostNumCoresPerSocket *prometheus.Desc
	hostCPUModel          *prometheus.Desc

	// Disk / Storage
	hostDatastoreCapacity *prometheus.Desc
	hostDatastoreFree     *prometheus.Desc
	hostDatastoreUsed     *prometheus.Desc

	// Network
	hostNetRx *prometheus.Desc
	hostNetTx *prometheus.Desc

	// Hardware sensors (heavy)
	hostSensorHealth *prometheus.Desc

	// ===== VM METRICS =====
	vmCPUUsage          *prometheus.Desc
	vmMemoryUsage       *prometheus.Desc
	vmMemoryTotal       *prometheus.Desc
	vmUptime            *prometheus.Desc
	vmPowerState        *prometheus.Desc
	vmNumCPU            *prometheus.Desc
	vmGuestToolsStatus  *prometheus.Desc
	vmGuestToolsVersion *prometheus.Desc
	vmGuestOS           *prometheus.Desc

	// Disk
	vmDiskCapacity *prometheus.Desc
	vmDiskFree     *prometheus.Desc
	vmDiskUsed     *prometheus.Desc

	// Network
	vmNetRx *prometheus.Desc
	vmNetTx *prometheus.Desc

	// Performance counters (heavy)
	vmPerfCPU    *prometheus.Desc
	vmPerfMemory *prometheus.Desc
	vmPerfDisk   *prometheus.Desc
	vmPerfNet    *prometheus.Desc
}

func NewESXiCollector(cfg *config.Config) *ESXiCollector {
	return &ESXiCollector{
		cfg: cfg,

		// Host
		hostCPUUsage:          prometheus.NewDesc("esxi_host_cpu_usage_percent", "Host CPU usage %", []string{"host"}, nil),
		hostMemoryUsage:       prometheus.NewDesc("esxi_host_memory_usage_bytes", "Host memory usage bytes", []string{"host"}, nil),
		hostMemoryTotal:       prometheus.NewDesc("esxi_host_memory_total_bytes", "Host memory total bytes", []string{"host"}, nil),
		hostUptime:            prometheus.NewDesc("esxi_host_uptime_seconds", "Host uptime seconds", []string{"host"}, nil),
		hostPowerState:        prometheus.NewDesc("esxi_host_power_state", "Host power state (1=poweredOn)", []string{"host"}, nil),
		hostMaintenanceMode:   prometheus.NewDesc("esxi_host_maintenance_mode", "Host in maintenance mode (1=true)", []string{"host"}, nil),
		hostConnectionState:   prometheus.NewDesc("esxi_host_connection_state", "Host connection state", []string{"host", "state"}, nil),
		hostNumCPU:            prometheus.NewDesc("esxi_host_num_cpu", "Number of CPUs", []string{"host"}, nil),
		hostNumCoresPerSocket: prometheus.NewDesc("esxi_host_num_cores_per_socket", "Cores per socket", []string{"host"}, nil),
		hostCPUModel:          prometheus.NewDesc("esxi_host_cpu_model_info", "CPU model info", []string{"host", "model"}, nil),

		hostDatastoreCapacity: prometheus.NewDesc("esxi_host_datastore_capacity_bytes", "Datastore capacity", []string{"host", "datastore"}, nil),
		hostDatastoreFree:     prometheus.NewDesc("esxi_host_datastore_free_bytes", "Datastore free space", []string{"host", "datastore"}, nil),
		hostDatastoreUsed:     prometheus.NewDesc("esxi_host_datastore_used_bytes", "Datastore used space", []string{"host", "datastore"}, nil),

		hostNetRx: prometheus.NewDesc("esxi_host_network_rx_bytes_total", "Host network RX bytes", []string{"host"}, nil),
		hostNetTx: prometheus.NewDesc("esxi_host_network_tx_bytes_total", "Host network TX bytes", []string{"host"}, nil),

		hostSensorHealth: prometheus.NewDesc("esxi_host_sensor_health", "Host hardware sensor status", []string{"host", "sensor"}, nil),

		// VM
		vmCPUUsage:    prometheus.NewDesc("esxi_vm_cpu_usage_percent", "VM CPU usage %", []string{"vm", "host"}, nil),
		vmMemoryUsage: prometheus.NewDesc("esxi_vm_memory_usage_bytes", "VM memory usage bytes", []string{"vm", "host"}, nil),
		vmMemoryTotal: prometheus.NewDesc("esxi_vm_memory_total_bytes", "VM memory total bytes", []string{"vm", "host"}, nil),
		vmUptime:      prometheus.NewDesc("esxi_vm_uptime_seconds", "VM uptime seconds", []string{"vm", "host"}, nil),
		vmPowerState:  prometheus.NewDesc("esxi_vm_power_state", "VM power state", []string{"vm", "host"}, nil),
		vmNumCPU:      prometheus.NewDesc("esxi_vm_num_cpu", "VM number of vCPUs", []string{"vm", "host"}, nil),
		// NOTE: status is a numeric encoding (see toolsStatusValue), version needs its own label.
		vmGuestToolsStatus:  prometheus.NewDesc("esxi_vm_guest_tools_status", "VMware Tools status (-1=notInstalled,0=notRunning,1=ok,2=old)", []string{"vm", "host"}, nil),
		vmGuestToolsVersion: prometheus.NewDesc("esxi_vm_guest_tools_version", "VMware Tools version info", []string{"vm", "host", "version"}, nil),
		vmGuestOS:           prometheus.NewDesc("esxi_vm_guest_os", "Guest OS name", []string{"vm", "host", "os"}, nil),

		vmDiskCapacity: prometheus.NewDesc("esxi_vm_disk_capacity_bytes", "VM disk capacity", []string{"vm", "host", "disk"}, nil),
		vmDiskFree:     prometheus.NewDesc("esxi_vm_disk_free_bytes", "VM disk free space", []string{"vm", "host", "disk"}, nil),
		vmDiskUsed:     prometheus.NewDesc("esxi_vm_disk_used_bytes", "VM disk used space", []string{"vm", "host", "disk"}, nil),

		vmNetRx: prometheus.NewDesc("esxi_vm_network_rx_bytes_total", "VM network RX bytes", []string{"vm", "host", "nic"}, nil),
		vmNetTx: prometheus.NewDesc("esxi_vm_network_tx_bytes_total", "VM network TX bytes", []string{"vm", "host", "nic"}, nil),

		vmPerfCPU:    prometheus.NewDesc("esxi_vm_perf_cpu_usage_percent", "VM perf CPU usage", []string{"vm", "host"}, nil),
		vmPerfMemory: prometheus.NewDesc("esxi_vm_perf_memory_usage_bytes", "VM perf memory usage", []string{"vm", "host"}, nil),
		vmPerfDisk:   prometheus.NewDesc("esxi_vm_perf_disk_io_bytes_total", "VM perf disk I/O", []string{"vm", "host"}, nil),
		vmPerfNet:    prometheus.NewDesc("esxi_vm_perf_net_bytes_total", "VM perf network I/O", []string{"vm", "host"}, nil),
	}
}

func (c *ESXiCollector) Describe(ch chan<- *prometheus.Desc) {
	descs := []*prometheus.Desc{
		c.hostCPUUsage, c.hostMemoryUsage, c.hostMemoryTotal, c.hostUptime,
		c.hostPowerState, c.hostMaintenanceMode, c.hostConnectionState,
		c.hostNumCPU, c.hostNumCoresPerSocket, c.hostCPUModel,
		c.hostDatastoreCapacity, c.hostDatastoreFree, c.hostDatastoreUsed,
		c.hostNetRx, c.hostNetTx,
		c.vmCPUUsage, c.vmMemoryUsage, c.vmMemoryTotal, c.vmUptime,
		c.vmPowerState, c.vmNumCPU, c.vmGuestToolsStatus, c.vmGuestToolsVersion, c.vmGuestOS,
		c.vmDiskCapacity, c.vmDiskFree, c.vmDiskUsed,
		c.vmNetRx, c.vmNetTx,
	}

	if c.cfg.Metrics.CollectHardwareSensors {
		descs = append(descs, c.hostSensorHealth)
	}
	if c.cfg.Metrics.CollectPerformance {
		descs = append(descs, c.vmPerfCPU, c.vmPerfMemory, c.vmPerfDisk, c.vmPerfNet)
	}

	for _, d := range descs {
		ch <- d
	}
}

func (c *ESXiCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCollectTimeout)
	defer cancel()
	wg := new(sync.WaitGroup)
	for _, spec := range c.cfg.Hosts {
		wg.Go(func() {
			c.collect(ctx, ch, spec)
		})
	}
	wg.Wait()
}

func (c *ESXiCollector) collect(ctx context.Context, ch chan<- prometheus.Metric, cfg config.ESXIHost) {
	u, err := url.Parse(cfg.Host)
	if err != nil {
		log.Printf("invalid esxi host url: %v", err)
		return
	}

	if cfg.Username != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	client, err := govmomi.NewClient(ctx, u, cfg.Insecure)
	if err != nil {
		log.Printf("failed to connect to esxi: %v", err)
		return
	}
	defer func() {
		// Use a fresh short-lived context: the parent ctx may already be
		// near its deadline, and logout shouldn't be skipped because of that.
		logoutCtx, logoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer logoutCancel()
		if err := client.Logout(logoutCtx); err != nil {
			log.Printf("esxi logout error: %v", err)
		}
	}()

	finder := find.NewFinder(client.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		log.Printf("failed to get datacenter: %v", err)
		return
	}
	finder.SetDatacenter(dc)

	hosts, err := finder.HostSystemList(ctx, "*")
	if err != nil {
		log.Printf("failed to list hosts: %v", err)
		return
	}

	for _, host := range hosts {
		c.collectHostMetrics(ctx, host, ch)
		c.collectVMMetrics(ctx, finder, host, ch)
	}
}

func (c *ESXiCollector) collectHostMetrics(ctx context.Context, host *object.HostSystem, ch chan<- prometheus.Metric) {
	var hs mo.HostSystem
	err := host.Properties(ctx, host.Reference(), []string{"summary", "runtime", "config", "hardware"}, &hs)
	if err != nil {
		log.Printf("host properties error (%s): %v", host.Name(), err)
		return
	}

	name := host.Name()
	s := hs.Summary

	// Hardware is a pointer and can be nil for a disconnected/unreachable host.
	if s.Hardware == nil {
		log.Printf("host %s: hardware summary unavailable, skipping cpu/memory/model metrics", name)
	} else {
		totalMhz := float64(s.Hardware.CpuMhz) * float64(s.Hardware.NumCpuCores)
		if totalMhz > 0 {
			ch <- prometheus.MustNewConstMetric(c.hostCPUUsage, prometheus.GaugeValue,
				float64(s.QuickStats.OverallCpuUsage)/totalMhz*100, name)
		}
		ch <- prometheus.MustNewConstMetric(c.hostMemoryTotal, prometheus.GaugeValue,
			float64(s.Hardware.MemorySize), name)
		ch <- prometheus.MustNewConstMetric(c.hostNumCPU, prometheus.GaugeValue, float64(s.Hardware.NumCpuCores), name)
		if s.Hardware.NumCpuPkgs > 0 {
			ch <- prometheus.MustNewConstMetric(c.hostNumCoresPerSocket, prometheus.GaugeValue,
				float64(s.Hardware.NumCpuCores)/float64(s.Hardware.NumCpuPkgs), name)
		}
		ch <- prometheus.MustNewConstMetric(c.hostCPUModel, prometheus.GaugeValue, 1, name, s.Hardware.CpuModel)
	}

	ch <- prometheus.MustNewConstMetric(c.hostMemoryUsage, prometheus.GaugeValue,
		float64(s.QuickStats.OverallMemoryUsage)*1024*1024, name)
	ch <- prometheus.MustNewConstMetric(c.hostUptime, prometheus.GaugeValue,
		float64(s.QuickStats.Uptime), name)
	ch <- prometheus.MustNewConstMetric(c.hostPowerState, prometheus.GaugeValue,
		boolToFloat(s.Runtime.PowerState == types.HostSystemPowerStatePoweredOn), name)
	ch <- prometheus.MustNewConstMetric(c.hostMaintenanceMode, prometheus.GaugeValue,
		boolToFloat(s.Runtime.InMaintenanceMode), name)
	ch <- prometheus.MustNewConstMetric(c.hostConnectionState, prometheus.GaugeValue, 1, name, string(s.Runtime.ConnectionState))

	// // Datastores - not yet implemented; see TODO in config for CollectDatastore.
	// if c.cfg.Metrics.CollectDatastore { ... }

	if c.cfg.Metrics.CollectHardwareSensors {
		c.collectHardwareSensors(ctx, hs.Runtime, ch, name)
	}
}

// collectHardwareSensors reports per-sensor health using the numeric sensor
// info embedded in HostRuntimeInfo, which is already fetched as part of the
// "runtime" property in collectHostMetrics - no separate API call needed.
// (There is no object.HostConfigManager.HealthStatusSystem() helper in
// govmomi; HealthStatusSystem is just a ManagedObjectReference field, and
// reading it directly would require another round trip via the
// HostHealthStatusSystem managed object, which this avoids entirely.)
func (c *ESXiCollector) collectHardwareSensors(ctx context.Context, runtime types.HostRuntimeInfo, ch chan<- prometheus.Metric, hostName string) {
	hsr := runtime.HealthSystemRuntime
	if hsr == nil || hsr.SystemHealthInfo == nil {
		log.Printf("host %s: no sensor health info available (host may not be vCenter-managed)", hostName)
		return
	}

	for _, sensor := range hsr.SystemHealthInfo.NumericSensorInfo {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var value float64
		state := "unknown"
		if sensor.HealthState != nil {
			state = sensor.HealthState.GetElementDescription().Key
		}
		switch state {
		case "green":
			value = 0
		case "yellow":
			value = 1
		case "red":
			value = 2
		default:
			value = -1
		}
		ch <- prometheus.MustNewConstMetric(c.hostSensorHealth, prometheus.GaugeValue, value, hostName, sensor.Name)
	}
}

func (c *ESXiCollector) collectVMMetrics(ctx context.Context, finder *find.Finder, host *object.HostSystem, ch chan<- prometheus.Metric) {
	vms, err := finder.VirtualMachineList(ctx, "*")
	if err != nil {
		log.Printf("host %s: failed to list VMs: %v", host.Name(), err)
		return
	}

	hostName := host.Name()

	// Need the host's per-core MHz to turn VM MHz usage into a percentage.
	var hs mo.HostSystem
	hostCPUMhz := 0.0
	if err := host.Properties(ctx, host.Reference(), []string{"summary"}, &hs); err == nil && hs.Summary.Hardware != nil {
		hostCPUMhz = float64(hs.Summary.Hardware.CpuMhz)
	}

	for _, vm := range vms {
		var vmo mo.VirtualMachine
		if err := vm.Properties(ctx, vm.Reference(), []string{"summary", "config", "guest"}, &vmo); err != nil {
			log.Printf("host %s: vm properties error: %v", hostName, err)
			continue
		}

		vmName := vm.Name()
		s := vmo.Summary

		// VM CPU usage (MHz) as a percentage of its vCPU allocation's real capacity,
		// i.e. numCpu * host per-core MHz - previously divided MHz by a raw core
		// count, mixing units and producing meaningless numbers.
		if hostCPUMhz > 0 && s.Config.NumCpu > 0 {
			capacityMhz := float64(s.Config.NumCpu) * hostCPUMhz
			ch <- prometheus.MustNewConstMetric(c.vmCPUUsage, prometheus.GaugeValue,
				float64(s.QuickStats.OverallCpuUsage)/capacityMhz*100, vmName, hostName)
		}

		ch <- prometheus.MustNewConstMetric(c.vmMemoryTotal, prometheus.GaugeValue,
			float64(s.Config.MemorySizeMB)*1024*1024, vmName, hostName)
		ch <- prometheus.MustNewConstMetric(c.vmMemoryUsage, prometheus.GaugeValue,
			float64(s.QuickStats.GuestMemoryUsage)*1024*1024, vmName, hostName)
		ch <- prometheus.MustNewConstMetric(c.vmUptime, prometheus.GaugeValue,
			float64(s.QuickStats.UptimeSeconds), vmName, hostName)

		ch <- prometheus.MustNewConstMetric(c.vmPowerState, prometheus.GaugeValue,
			boolToFloat(s.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn), vmName, hostName)
		ch <- prometheus.MustNewConstMetric(c.vmNumCPU, prometheus.GaugeValue, float64(s.Config.NumCpu), vmName, hostName)

		if c.cfg.Metrics.CollectGuestInfo {
			toolsStatus, toolsVersion := 0.0, "unknown"
			if vmo.Guest != nil {
				toolsVersion = vmo.Guest.ToolsVersion
				toolsStatus = toolsStatusValue(vmo.Guest.ToolsStatus)
			}
			ch <- prometheus.MustNewConstMetric(c.vmGuestToolsStatus, prometheus.GaugeValue, toolsStatus, vmName, hostName)
			ch <- prometheus.MustNewConstMetric(c.vmGuestToolsVersion, prometheus.GaugeValue, 1, vmName, hostName, toolsVersion)
			ch <- prometheus.MustNewConstMetric(c.vmGuestOS, prometheus.GaugeValue, 1, vmName, hostName, s.Config.GuestId)
		}

		// Disk details (medium cost)
		if c.cfg.Metrics.CollectVMDiskDetails && vmo.Config != nil {
			for _, disk := range vmo.Config.Hardware.Device {
				if d, ok := disk.(*types.VirtualDisk); ok {
					label := fmt.Sprintf("disk-%d", d.Key)
					ch <- prometheus.MustNewConstMetric(c.vmDiskCapacity, prometheus.GaugeValue,
						float64(d.CapacityInKB)*1024, vmName, hostName, label)
				}
			}
		}

		// TODO: Network adapters
		// if c.cfg.Metrics.CollectNetworkAdapters { ... }
	}
}

// toolsStatusValue maps VMware Tools status to a stable numeric encoding
// so the metric documents its meaning rather than always reading "1".
func toolsStatusValue(status types.VirtualMachineToolsStatus) float64 {
	switch status {
	case types.VirtualMachineToolsStatusToolsOk:
		return 1
	case types.VirtualMachineToolsStatusToolsOld:
		return 2
	case types.VirtualMachineToolsStatusToolsNotRunning:
		return 0
	case types.VirtualMachineToolsStatusToolsNotInstalled:
		return -1
	default:
		return -1
	}
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
