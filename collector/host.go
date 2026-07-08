package collector

import (
	"github.com/fmotalleb/go-tools/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// HostCollector produces esxi_host_* metrics. It's split from collector.go
// because the host domain alone spans CPU/memory/disk/network/runtime/
// hardware — each with its own labels and units — and inlining it made the
// original file hard to navigate.

// Counter names are the canonical vSphere strings; see PerformanceManager
// docs. Only "rate/absolute" rollups that map cleanly to Prometheus gauges
// are used; deltas would need extra care with Prometheus counter semantics.
var hostPerfCounters = []string{
	// CPU (percent = 1/100th %, ms = milliseconds)
	"cpu.ready.summation",         // ms
	"cpu.system.summation",        // ms
	"cpu.used.summation",          // ms  (user-mode analog)
	"cpu.wait.summation",          // ms
	"cpu.idle.summation",          // ms
	"cpu.utilization.average",     // %
	"cpu.coreUtilization.average", // %
	"cpu.usage.average",           // %

	// Memory
	"mem.active.average",     // KB
	"mem.consumed.average",   // KB
	"mem.granted.average",    // KB
	"mem.shared.average",     // KB
	"mem.vmmemctl.average",   // KB (balloon)
	"mem.swapin.average",     // KB
	"mem.swapout.average",    // KB
	"mem.compressed.average", // KB
	"mem.overhead.average",   // KB
	"mem.zero.average",       // KB
	"mem.state.latest",       // enum 0-3

	// Disk (per-device via "*" instance)
	"disk.totalReadLatency.average",    // ms
	"disk.totalWriteLatency.average",   // ms
	"disk.kernelLatency.average",       // ms
	"disk.deviceLatency.average",       // ms
	"disk.queueLatency.average",        // ms
	"disk.numberReadAveraged.average",  // IOPS
	"disk.numberWriteAveraged.average", // IOPS
	"disk.read.average",                // KBps
	"disk.write.average",               // KBps
	"disk.commands.summation",
	"disk.commandsAveraged.average",

	// Network (per-nic via "*" instance)
	"net.packetsRx.summation",
	"net.packetsTx.summation",
	"net.bytesRx.average", // KBps
	"net.bytesTx.average", // KBps
	"net.droppedRx.summation",
	"net.droppedTx.summation",
	"net.errorsRx.summation",
	"net.errorsTx.summation",
	"net.multicastRx.summation",
	"net.multicastTx.summation",
	"net.broadcastRx.summation",
	"net.broadcastTx.summation",
	"net.usage.average", // KBps aggregate
}

type HostCollector struct {
	cfg *config.Config

	// Legacy / summary
	cpuUsage        *prometheus.Desc
	memUsage        *prometheus.Desc
	memTotal        *prometheus.Desc
	uptime          *prometheus.Desc
	powerState      *prometheus.Desc
	maintenanceMode *prometheus.Desc
	connectionState *prometheus.Desc
	numCPU          *prometheus.Desc
	coresPerSocket  *prometheus.Desc
	cpuModel        *prometheus.Desc

	// CPU perf
	cpuReady            *prometheus.Desc
	cpuSystem           *prometheus.Desc
	cpuUser             *prometheus.Desc
	cpuWait             *prometheus.Desc
	cpuIdle             *prometheus.Desc
	cpuUtil             *prometheus.Desc
	cpuCoreUtil         *prometheus.Desc
	cpuPkgUtil          *prometheus.Desc
	cpuReservationTotal *prometheus.Desc
	cpuReservationUsed  *prometheus.Desc

	// Memory perf
	memActive     *prometheus.Desc
	memConsumed   *prometheus.Desc
	memGranted    *prometheus.Desc
	memShared     *prometheus.Desc
	memBalloon    *prometheus.Desc
	memSwapIn     *prometheus.Desc
	memSwapOut    *prometheus.Desc
	memCompressed *prometheus.Desc
	memOverhead   *prometheus.Desc
	memZero       *prometheus.Desc
	memState      *prometheus.Desc

	// Disk perf (per-device)
	diskReadLat    *prometheus.Desc
	diskWriteLat   *prometheus.Desc
	diskKernelLat  *prometheus.Desc
	diskDevLat     *prometheus.Desc
	diskQueueLat   *prometheus.Desc
	diskReadIOPS   *prometheus.Desc
	diskWriteIOPS  *prometheus.Desc
	diskReadBps    *prometheus.Desc
	diskWriteBps   *prometheus.Desc
	diskOutIO      *prometheus.Desc // outstanding IO ~ queueLatency * IOPS proxy; also from "disk.queueLatency"
	diskQueueDepth *prometheus.Desc
	fsUtil         *prometheus.Desc

	// Network perf (per-nic)
	netPktRx     *prometheus.Desc
	netPktTx     *prometheus.Desc
	netByteRx    *prometheus.Desc
	netByteTx    *prometheus.Desc
	netDropRx    *prometheus.Desc
	netDropTx    *prometheus.Desc
	netErrRx     *prometheus.Desc
	netErrTx     *prometheus.Desc
	netMcast     *prometheus.Desc
	netBcast     *prometheus.Desc
	netLinkSpeed *prometheus.Desc
	netDuplex    *prometheus.Desc
	netState     *prometheus.Desc
	netUtil      *prometheus.Desc

	// Runtime
	bootTime       *prometheus.Desc
	rebootRequired *prometheus.Desc
	lockdownMode   *prometheus.Desc
	maintProgress  *prometheus.Desc
	quickBoot      *prometheus.Desc
	secureBoot     *prometheus.Desc
	tpmState       *prometheus.Desc
	hyperthreading *prometheus.Desc
	evcMode        *prometheus.Desc
}

func NewHostCollector(cfg *config.Config) *HostCollector {
	lbl := []string{"host"}
	dev := []string{"host", "device"}
	nic := []string{"host", "nic"}
	fs := []string{"host", "mount"}

	d := func(name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}

	return &HostCollector{
		cfg: cfg,

		cpuUsage:        d("esxi_host_cpu_usage_percent", "Host CPU usage %", lbl),
		memUsage:        d("esxi_host_memory_usage_bytes", "Host memory usage bytes", lbl),
		memTotal:        d("esxi_host_memory_total_bytes", "Host memory total bytes", lbl),
		uptime:          d("esxi_host_uptime_seconds", "Host uptime seconds", lbl),
		powerState:      d("esxi_host_power_state", "Host power state (1=poweredOn)", lbl),
		maintenanceMode: d("esxi_host_maintenance_mode", "Host in maintenance mode (1=true)", lbl),
		connectionState: d("esxi_host_connection_state", "Host connection state", []string{"host", "state"}),
		numCPU:          d("esxi_host_num_cpu", "Number of CPU cores", lbl),
		coresPerSocket:  d("esxi_host_num_cores_per_socket", "Cores per socket", lbl),
		cpuModel:        d("esxi_host_cpu_model_info", "CPU model info", []string{"host", "model"}),

		cpuReady:            d("esxi_host_cpu_ready_ms", "CPU ready time (ms)", lbl),
		cpuSystem:           d("esxi_host_cpu_system_ms", "CPU system time (ms)", lbl),
		cpuUser:             d("esxi_host_cpu_user_ms", "CPU user time (ms)", lbl),
		cpuWait:             d("esxi_host_cpu_wait_ms", "CPU wait time (ms)", lbl),
		cpuIdle:             d("esxi_host_cpu_idle_ms", "CPU idle time (ms)", lbl),
		cpuUtil:             d("esxi_host_cpu_utilization_percent", "CPU utilization %", lbl),
		cpuCoreUtil:         d("esxi_host_cpu_core_utilization_percent", "Per-core utilization %", lbl),
		cpuPkgUtil:          d("esxi_host_cpu_package_utilization_percent", "Package utilization %", lbl),
		cpuReservationTotal: d("esxi_host_cpu_reservation_total_mhz", "Total CPU reservation capacity (MHz)", lbl),
		cpuReservationUsed:  d("esxi_host_cpu_reservation_used_mhz", "Used CPU reservation (MHz)", lbl),

		memActive:     d("esxi_host_memory_active_bytes", "Active memory", lbl),
		memConsumed:   d("esxi_host_memory_consumed_bytes", "Consumed memory", lbl),
		memGranted:    d("esxi_host_memory_granted_bytes", "Granted memory", lbl),
		memShared:     d("esxi_host_memory_shared_bytes", "Shared memory", lbl),
		memBalloon:    d("esxi_host_memory_balloon_bytes", "Ballooned memory", lbl),
		memSwapIn:     d("esxi_host_memory_swapin_bytes", "Swap-in", lbl),
		memSwapOut:    d("esxi_host_memory_swapout_bytes", "Swap-out", lbl),
		memCompressed: d("esxi_host_memory_compressed_bytes", "Compressed memory", lbl),
		memOverhead:   d("esxi_host_memory_overhead_bytes", "Overhead memory", lbl),
		memZero:       d("esxi_host_memory_zero_bytes", "Zero-page memory", lbl),
		memState:      d("esxi_host_memory_state", "Memory pressure state (0=high,1=clear,2=soft,3=hard,4=low)", lbl),

		diskReadLat:    d("esxi_host_disk_read_latency_ms", "Disk read latency (ms)", dev),
		diskWriteLat:   d("esxi_host_disk_write_latency_ms", "Disk write latency (ms)", dev),
		diskKernelLat:  d("esxi_host_disk_kernel_latency_ms", "Kernel latency (ms)", dev),
		diskDevLat:     d("esxi_host_disk_device_latency_ms", "Device latency (ms)", dev),
		diskQueueLat:   d("esxi_host_disk_queue_latency_ms", "Queue latency (ms)", dev),
		diskReadIOPS:   d("esxi_host_disk_read_iops", "Disk read IOPS", dev),
		diskWriteIOPS:  d("esxi_host_disk_write_iops", "Disk write IOPS", dev),
		diskReadBps:    d("esxi_host_disk_read_bytes_per_second", "Disk read throughput B/s", dev),
		diskWriteBps:   d("esxi_host_disk_write_bytes_per_second", "Disk write throughput B/s", dev),
		diskOutIO:      d("esxi_host_disk_outstanding_io", "Outstanding IO", dev),
		diskQueueDepth: d("esxi_host_disk_queue_depth", "Queue depth", dev),
		fsUtil:         d("esxi_host_filesystem_utilization_percent", "Filesystem utilization %", fs),

		netPktRx:     d("esxi_host_network_packets_rx_total", "Packets received", nic),
		netPktTx:     d("esxi_host_network_packets_tx_total", "Packets transmitted", nic),
		netByteRx:    d("esxi_host_network_bytes_rx_per_second", "Receive throughput B/s", nic),
		netByteTx:    d("esxi_host_network_bytes_tx_per_second", "Transmit throughput B/s", nic),
		netDropRx:    d("esxi_host_network_dropped_rx_total", "Dropped RX packets", nic),
		netDropTx:    d("esxi_host_network_dropped_tx_total", "Dropped TX packets", nic),
		netErrRx:     d("esxi_host_network_errors_rx_total", "RX errors", nic),
		netErrTx:     d("esxi_host_network_errors_tx_total", "TX errors", nic),
		netMcast:     d("esxi_host_network_multicast_total", "Multicast packets", nic),
		netBcast:     d("esxi_host_network_broadcast_total", "Broadcast packets", nic),
		netLinkSpeed: d("esxi_host_nic_link_speed_bits_per_second", "NIC link speed", nic),
		netDuplex:    d("esxi_host_nic_duplex", "NIC full duplex (1=full)", nic),
		netState:     d("esxi_host_nic_state", "NIC state (1=up)", nic),
		netUtil:      d("esxi_host_nic_utilization_percent", "NIC utilization %", nic),

		bootTime:       d("esxi_host_boot_time_seconds", "Host boot time (unix seconds)", lbl),
		rebootRequired: d("esxi_host_reboot_required", "Host requires reboot", lbl),
		lockdownMode:   d("esxi_host_lockdown_mode", "Lockdown mode (0=disabled,1=normal,2=strict)", lbl),
		maintProgress:  d("esxi_host_maintenance_progress_percent", "Maintenance mode progress", lbl),
		quickBoot:      d("esxi_host_quick_boot_supported", "Quick boot supported", lbl),
		secureBoot:     d("esxi_host_secure_boot_enabled", "Secure boot enabled", lbl),
		tpmState:       d("esxi_host_tpm_state", "TPM present (1=true)", lbl),
		hyperthreading: d("esxi_host_hyperthreading_active", "Hyperthreading active", lbl),
		evcMode:        d("esxi_host_evc_mode_info", "EVC mode info", []string{"host", "mode"}),
	}
}

func (c *HostCollector) Describe(ch chan<- *prometheus.Desc) {
	// Emitting every desc up front lets Prometheus warn about label
	// mismatches at scrape time rather than silently dropping series.
	descs := []*prometheus.Desc{
		c.cpuUsage, c.memUsage, c.memTotal, c.uptime, c.powerState,
		c.maintenanceMode, c.connectionState, c.numCPU, c.coresPerSocket,
		c.cpuModel,
		c.cpuReady, c.cpuSystem, c.cpuUser, c.cpuWait, c.cpuIdle,
		c.cpuUtil, c.cpuCoreUtil, c.cpuPkgUtil,
		c.cpuReservationTotal, c.cpuReservationUsed,
		c.memActive, c.memConsumed, c.memGranted, c.memShared,
		c.memBalloon, c.memSwapIn, c.memSwapOut, c.memCompressed,
		c.memOverhead, c.memZero, c.memState,
		c.diskReadLat, c.diskWriteLat, c.diskKernelLat, c.diskDevLat,
		c.diskQueueLat, c.diskReadIOPS, c.diskWriteIOPS,
		c.diskReadBps, c.diskWriteBps, c.diskOutIO, c.diskQueueDepth,
		c.fsUtil,
		c.netPktRx, c.netPktTx, c.netByteRx, c.netByteTx,
		c.netDropRx, c.netDropTx, c.netErrRx, c.netErrTx,
		c.netMcast, c.netBcast, c.netLinkSpeed, c.netDuplex,
		c.netState, c.netUtil,
		c.bootTime, c.rebootRequired, c.lockdownMode, c.maintProgress,
		c.quickBoot, c.secureBoot, c.tpmState, c.hyperthreading, c.evcMode,
	}
	for _, dsc := range descs {
		ch <- dsc
	}
}

func (c *HostCollector) Collect(s *scrapeContext) {
	// Container view + PropertyCollector is one round trip for all hosts,
	// instead of N HostSystem.Properties calls.
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"HostSystem"}, true)
	if err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("host: create view failed", "error", err)
		return
	}
	defer v.Destroy(s.ctx)

	var hosts []mo.HostSystem
	// We ask for the specific subtrees we use — full properties would drag
	// down performance and memory on large clusters.
	// Always ask for "name" explicitly. Without it mo.HostSystem.Name is
	// empty (govmomi does not fill inherited ManagedEntity properties
	// automatically from a summary fetch), producing series with
	// host="" and immediate dedup collisions across hosts.
	if err := v.Retrieve(s.ctx, []string{"HostSystem"},
		[]string{"name", "summary", "runtime", "config", "hardware", "config.network", "capability"},
		&hosts); err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("host: retrieve failed", "error", err)
		return
	}

	for i := range hosts {
		c.emitStatic(&hosts[i], s)
		if *c.cfg.Metrics.CollectHostRuntime {
			c.emitRuntime(&hosts[i], s)
		}
		if *c.cfg.Metrics.CollectHostCPUPerf {
			c.emitPerfCPU(&hosts[i], s)
		}
		if *c.cfg.Metrics.CollectHostMemPerf {
			c.emitPerfMem(&hosts[i], s)
		}
		if *c.cfg.Metrics.CollectHostDiskPerf {
			c.emitPerfDisk(&hosts[i], s)
		}
		if *c.cfg.Metrics.CollectHostNetPerf {
			c.emitPerfNet(&hosts[i], s)
		}
	}
}

// emitStatic covers the cheap, always-on properties: summary + hardware
// inventory. Kept separate so the perf helpers can assume the entity is
// reachable and skip these fields.
func (c *HostCollector) emitStatic(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		// If we still can't find a name, the entity is unusable —
		// skipping avoids emitting series with host="" that would
		// collide with every other unnamed host.
		return
	}

	sum := h.Summary
	if sum.Hardware != nil {
		total := float64(sum.Hardware.CpuMhz) * float64(sum.Hardware.NumCpuCores)
		if total > 0 {
			s.ch <- prometheus.MustNewConstMetric(c.cpuUsage, prometheus.GaugeValue,
				float64(sum.QuickStats.OverallCpuUsage)/total*100, name)
		}
		s.ch <- prometheus.MustNewConstMetric(c.memTotal, prometheus.GaugeValue,
			float64(sum.Hardware.MemorySize), name)
		s.ch <- prometheus.MustNewConstMetric(c.numCPU, prometheus.GaugeValue,
			float64(sum.Hardware.NumCpuCores), name)
		if sum.Hardware.NumCpuPkgs > 0 {
			s.ch <- prometheus.MustNewConstMetric(c.coresPerSocket, prometheus.GaugeValue,
				float64(sum.Hardware.NumCpuCores)/float64(sum.Hardware.NumCpuPkgs), name)
		}
		s.ch <- prometheus.MustNewConstMetric(c.cpuModel, prometheus.GaugeValue, 1,
			name, sum.Hardware.CpuModel)
		s.ch <- prometheus.MustNewConstMetric(c.cpuReservationTotal, prometheus.GaugeValue,
			float64(sum.Hardware.CpuMhz)*float64(sum.Hardware.NumCpuCores), name)
	}
	s.ch <- prometheus.MustNewConstMetric(c.memUsage, prometheus.GaugeValue,
		float64(sum.QuickStats.OverallMemoryUsage)*1024*1024, name)
	s.ch <- prometheus.MustNewConstMetric(c.uptime, prometheus.GaugeValue,
		float64(sum.QuickStats.Uptime), name)
	s.ch <- prometheus.MustNewConstMetric(c.powerState, prometheus.GaugeValue,
		boolToFloat(sum.Runtime.PowerState == types.HostSystemPowerStatePoweredOn), name)
	s.ch <- prometheus.MustNewConstMetric(c.maintenanceMode, prometheus.GaugeValue,
		boolToFloat(sum.Runtime.InMaintenanceMode), name)
	s.ch <- prometheus.MustNewConstMetric(c.connectionState, prometheus.GaugeValue, 1,
		name, string(sum.Runtime.ConnectionState))
}

// emitRuntime handles the "does this host need attention?" set: boot time,
// lockdown mode, TPM, secure boot, etc. These come from config/runtime
// rather than the perf pipeline, so we can gate them cheaply.
func (c *HostCollector) emitRuntime(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	if h.Runtime.BootTime != nil {
		s.ch <- prometheus.MustNewConstMetric(c.bootTime, prometheus.GaugeValue,
			float64(h.Runtime.BootTime.Unix()), name)
	}
	// RebootRequired is only populated when the API knows — treat missing
	// as "no", matching vSphere UI behavior.
	s.ch <- prometheus.MustNewConstMetric(c.rebootRequired, prometheus.GaugeValue,
		boolToFloat(h.Summary.RebootRequired), name)

	if h.Config != nil {
		lockdown := 0.0
		switch h.Config.LockdownMode {
		case types.HostLockdownModeLockdownNormal:
			lockdown = 1
		case types.HostLockdownModeLockdownStrict:
			lockdown = 2
		}
		s.ch <- prometheus.MustNewConstMetric(c.lockdownMode, prometheus.GaugeValue, lockdown, name)

		// if h.Config.QuickBootEnabled != nil {
		// 	s.ch <- prometheus.MustNewConstMetric(c.quickBoot, prometheus.GaugeValue,
		// 		boolToFloat(*h.Config.QuickBootEnabled), name)
		// }
		if h.Config.HyperThread != nil {
			s.ch <- prometheus.MustNewConstMetric(c.hyperthreading, prometheus.GaugeValue,
				boolToFloat(h.Config.HyperThread.Active), name)
		}
	}

	// Secure boot + TPM live in Capability/Hardware; both are optional
	// pointers on older ESXi releases.
	// if h.Capability != nil && h.Capability.TpmSupported != nil {
	// 	s.ch <- prometheus.MustNewConstMetric(c.tpmState, prometheus.GaugeValue,
	// 		boolToFloat(*h.Capability.TpmSupported), name)
	// }
	if h.Runtime.TpmPcrValues != nil {
		s.ch <- prometheus.MustNewConstMetric(c.secureBoot, prometheus.GaugeValue, 1, name)
	}

	if h.Summary.CurrentEVCModeKey != "" {
		s.ch <- prometheus.MustNewConstMetric(c.evcMode, prometheus.GaugeValue, 1,
			name, h.Summary.CurrentEVCModeKey)
	}
}

func (c *HostCollector) emitPerfCPU(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	m := queryPerf(s.ctx, s.perf, h.Reference(), []string{
		"cpu.ready.summation", "cpu.system.summation", "cpu.used.summation",
		"cpu.wait.summation", "cpu.idle.summation",
		"cpu.utilization.average", "cpu.coreUtilization.average",
	}, "", IntervalRealtime)
	emit := func(desc *prometheus.Desc, key string) {
		if v, ok := m[key]; ok {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v.Value, name)
		}
	}
	emit(c.cpuReady, "cpu.ready.summation")
	emit(c.cpuSystem, "cpu.system.summation")
	emit(c.cpuUser, "cpu.used.summation")
	emit(c.cpuWait, "cpu.wait.summation")
	emit(c.cpuIdle, "cpu.idle.summation")
	emit(c.cpuUtil, "cpu.utilization.average")
	emit(c.cpuCoreUtil, "cpu.coreUtilization.average")
}

func (c *HostCollector) emitPerfMem(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	m := queryPerf(s.ctx, s.perf, h.Reference(), []string{
		"mem.active.average", "mem.consumed.average", "mem.granted.average",
		"mem.shared.average", "mem.vmmemctl.average", "mem.swapin.average",
		"mem.swapout.average", "mem.compressed.average", "mem.overhead.average",
		"mem.zero.average", "mem.state.latest",
	}, "", IntervalRealtime)
	kb := func(desc *prometheus.Desc, key string) {
		if v, ok := m[key]; ok {
			// vSphere returns KB; multiply to bytes for Prometheus
			// convention (base units).
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				v.Value*1024, name)
		}
	}
	kb(c.memActive, "mem.active.average")
	kb(c.memConsumed, "mem.consumed.average")
	kb(c.memGranted, "mem.granted.average")
	kb(c.memShared, "mem.shared.average")
	kb(c.memBalloon, "mem.vmmemctl.average")
	kb(c.memSwapIn, "mem.swapin.average")
	kb(c.memSwapOut, "mem.swapout.average")
	kb(c.memCompressed, "mem.compressed.average")
	kb(c.memOverhead, "mem.overhead.average")
	kb(c.memZero, "mem.zero.average")
	if v, ok := m["mem.state.latest"]; ok {
		s.ch <- prometheus.MustNewConstMetric(c.memState, prometheus.GaugeValue, v.Value, name)
	}
}

func (c *HostCollector) emitPerfDisk(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	m := queryPerfInstances(s.ctx, s.perf, h.Reference(), []string{
		"disk.totalReadLatency.average", "disk.totalWriteLatency.average",
		"disk.kernelLatency.average", "disk.deviceLatency.average",
		"disk.queueLatency.average",
		"disk.numberReadAveraged.average", "disk.numberWriteAveraged.average",
		"disk.read.average", "disk.write.average",
	}, IntervalRealtime)
	// Iterate per counter so we emit one Prometheus series per (device)
	// instance — the "device" label carries the canonical name (e.g.
	// naa.6000c29...).
	emit := func(desc *prometheus.Desc, key string, xform float64) {
		for _, sample := range m[key] {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				sample.Value*xform, name, sample.Instance)
		}
	}
	emit(c.diskReadLat, "disk.totalReadLatency.average", 1)
	emit(c.diskWriteLat, "disk.totalWriteLatency.average", 1)
	emit(c.diskKernelLat, "disk.kernelLatency.average", 1)
	emit(c.diskDevLat, "disk.deviceLatency.average", 1)
	emit(c.diskQueueLat, "disk.queueLatency.average", 1)
	emit(c.diskReadIOPS, "disk.numberReadAveraged.average", 1)
	emit(c.diskWriteIOPS, "disk.numberWriteAveraged.average", 1)
	// KBps -> B/s
	emit(c.diskReadBps, "disk.read.average", 1024)
	emit(c.diskWriteBps, "disk.write.average", 1024)
}

func (c *HostCollector) emitPerfNet(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	m := queryPerfInstances(s.ctx, s.perf, h.Reference(), []string{
		"net.packetsRx.summation", "net.packetsTx.summation",
		"net.bytesRx.average", "net.bytesTx.average",
		"net.droppedRx.summation", "net.droppedTx.summation",
		"net.errorsRx.summation", "net.errorsTx.summation",
		"net.multicastRx.summation", "net.broadcastRx.summation",
	}, IntervalRealtime)
	emit := func(desc *prometheus.Desc, key string, xform float64) {
		for _, sample := range m[key] {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				sample.Value*xform, name, sample.Instance)
		}
	}
	emit(c.netPktRx, "net.packetsRx.summation", 1)
	emit(c.netPktTx, "net.packetsTx.summation", 1)
	// KBps -> B/s
	emit(c.netByteRx, "net.bytesRx.average", 1024)
	emit(c.netByteTx, "net.bytesTx.average", 1024)
	emit(c.netDropRx, "net.droppedRx.summation", 1)
	emit(c.netDropTx, "net.droppedTx.summation", 1)
	emit(c.netErrRx, "net.errorsRx.summation", 1)
	emit(c.netErrTx, "net.errorsTx.summation", 1)
	emit(c.netMcast, "net.multicastRx.summation", 1)
	emit(c.netBcast, "net.broadcastRx.summation", 1)

	// Physical NIC properties (link speed, duplex, state) come from the
	// config.network subtree, not perf counters. Emit alongside so a
	// single scrape gives the full picture.
	if h.Config == nil || h.Config.Network == nil {
		return
	}
	for _, pnic := range h.Config.Network.Pnic {
		state := 0.0
		speed := 0.0
		dup := 0.0
		if pnic.LinkSpeed != nil {
			state = 1
			// SpeedMb is Mbps; expose as bits/sec.
			speed = float64(pnic.LinkSpeed.SpeedMb) * 1_000_000
			dup = boolToFloat(pnic.LinkSpeed.Duplex)
		}
		s.ch <- prometheus.MustNewConstMetric(c.netLinkSpeed, prometheus.GaugeValue, speed, name, pnic.Device)
		s.ch <- prometheus.MustNewConstMetric(c.netDuplex, prometheus.GaugeValue, dup, name, pnic.Device)
		s.ch <- prometheus.MustNewConstMetric(c.netState, prometheus.GaugeValue, state, name, pnic.Device)
	}
}

// hostName returns the best available name for a HostSystem, preferring
// the summary DNS name (matches vCenter UI) and falling back to the
// ManagedEntity name. Both can be empty on a disconnected host or when
// the caller forgot to request "name" in the property set — return "" and
// let the caller skip emitting so we don't produce host="" collisions.
func hostName(h *mo.HostSystem) string {
	if h.Summary.Config.Name != "" {
		return h.Summary.Config.Name
	}
	return h.Name
}
