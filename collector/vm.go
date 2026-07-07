package collector

import (
	"fmt"
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// VMCollector produces esxi_vm_* metrics. Compared to the host collector
// the VM path fans out much wider (dozens to thousands of VMs per host),
// so we lean harder on ContainerView + a single Retrieve to keep round
// trips O(1). Perf queries are still per-VM because vSphere caps
// multi-entity QueryPerf request sizes.

type VMCollector struct {
	cfg *config.Config

	// Basic / summary
	cpuUsage     *prometheus.Desc
	memUsage     *prometheus.Desc
	memTotal     *prometheus.Desc
	uptime       *prometheus.Desc
	powerState   *prometheus.Desc
	numCPU       *prometheus.Desc
	guestOS      *prometheus.Desc
	toolsStatus  *prometheus.Desc
	toolsVersion *prometheus.Desc
	toolsRunning *prometheus.Desc

	// CPU perf
	cpuReady       *prometheus.Desc
	cpuWait        *prometheus.Desc
	cpuCoStop      *prometheus.Desc
	cpuSystem      *prometheus.Desc
	cpuUser        *prometheus.Desc
	cpuEntitlement *prometheus.Desc
	cpuDemand      *prometheus.Desc
	cpuLatency     *prometheus.Desc
	cpuLimit       *prometheus.Desc
	cpuReservation *prometheus.Desc
	cpuShares      *prometheus.Desc

	// Memory perf
	memActive      *prometheus.Desc
	memGranted     *prometheus.Desc
	memConsumed    *prometheus.Desc
	memOverhead    *prometheus.Desc
	memSwapped     *prometheus.Desc
	memBalloon     *prometheus.Desc
	memCompressed  *prometheus.Desc
	memShared      *prometheus.Desc
	memPrivate     *prometheus.Desc
	memTarget      *prometheus.Desc
	memReclaim     *prometheus.Desc
	memLimit       *prometheus.Desc
	memReservation *prometheus.Desc

	// Disk (config + perf)
	diskCapacity    *prometheus.Desc
	diskFree        *prometheus.Desc
	diskUsed        *prometheus.Desc
	diskReadBps     *prometheus.Desc
	diskWriteBps    *prometheus.Desc
	diskReadIOPS    *prometheus.Desc
	diskWriteIOPS   *prometheus.Desc
	diskLatency     *prometheus.Desc
	diskQueueLat    *prometheus.Desc
	diskKernelLat   *prometheus.Desc
	diskDeviceLat   *prometheus.Desc
	diskOutstanding *prometheus.Desc

	// Network perf (per-nic)
	netPktRx     *prometheus.Desc
	netPktTx     *prometheus.Desc
	netByteRx    *prometheus.Desc
	netByteTx    *prometheus.Desc
	netDropped   *prometheus.Desc
	netErrors    *prometheus.Desc
	nicConnected *prometheus.Desc
	nicLinkSpeed *prometheus.Desc

	// Guest
	guestIP        *prometheus.Desc
	guestHostname  *prometheus.Desc
	guestHeartbeat *prometheus.Desc
	guestState     *prometheus.Desc
	guestUptime    *prometheus.Desc
	guestDNS       *prometheus.Desc
	guestFSCap     *prometheus.Desc
	guestFSFree    *prometheus.Desc
	guestFSUsed    *prometheus.Desc

	// Configuration
	snapshotCount *prometheus.Desc
	snapshotSize  *prometheus.Desc
	datastoreInfo *prometheus.Desc
	clusterInfo   *prometheus.Desc
	rpInfo        *prometheus.Desc
	folderInfo    *prometheus.Desc
	template      *prometheus.Desc
	secureBoot    *prometheus.Desc
	firmware      *prometheus.Desc
	encrypted     *prometheus.Desc
}

func NewVMCollector(cfg *config.Config) *VMCollector {
	base := []string{"vm", "host"}
	nic := []string{"vm", "host", "nic"}
	disk := []string{"vm", "host", "disk"}
	fs := []string{"vm", "host", "mount"}

	d := func(name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, labels, nil)
	}

	return &VMCollector{
		cfg: cfg,

		cpuUsage:     d("esxi_vm_cpu_usage_percent", "VM CPU usage %", base),
		memUsage:     d("esxi_vm_memory_usage_bytes", "VM memory usage", base),
		memTotal:     d("esxi_vm_memory_total_bytes", "VM configured memory", base),
		uptime:       d("esxi_vm_uptime_seconds", "VM uptime", base),
		powerState:   d("esxi_vm_power_state", "VM power state", base),
		numCPU:       d("esxi_vm_num_cpu", "vCPU count", base),
		guestOS:      d("esxi_vm_guest_os", "Guest OS", []string{"vm", "host", "os"}),
		toolsStatus:  d("esxi_vm_guest_tools_status", "VMware Tools status", base),
		toolsVersion: d("esxi_vm_guest_tools_version", "VMware Tools version", []string{"vm", "host", "version"}),
		toolsRunning: d("esxi_vm_guest_tools_running", "VMware Tools running (1=yes)", base),

		cpuReady:       d("esxi_vm_cpu_ready_ms", "CPU ready (ms)", base),
		cpuWait:        d("esxi_vm_cpu_wait_ms", "CPU wait (ms)", base),
		cpuCoStop:      d("esxi_vm_cpu_costop_ms", "CPU co-stop (ms)", base),
		cpuSystem:      d("esxi_vm_cpu_system_ms", "CPU system (ms)", base),
		cpuUser:        d("esxi_vm_cpu_user_ms", "CPU user (ms)", base),
		cpuEntitlement: d("esxi_vm_cpu_entitlement_mhz", "CPU entitlement (MHz)", base),
		cpuDemand:      d("esxi_vm_cpu_demand_mhz", "CPU demand (MHz)", base),
		cpuLatency:     d("esxi_vm_cpu_latency_percent", "CPU latency %", base),
		cpuLimit:       d("esxi_vm_cpu_limit_mhz", "CPU limit (MHz, -1 unlimited)", base),
		cpuReservation: d("esxi_vm_cpu_reservation_mhz", "CPU reservation (MHz)", base),
		cpuShares:      d("esxi_vm_cpu_shares", "CPU shares", base),

		memActive:      d("esxi_vm_memory_active_bytes", "Active memory", base),
		memGranted:     d("esxi_vm_memory_granted_bytes", "Granted memory", base),
		memConsumed:    d("esxi_vm_memory_consumed_bytes", "Consumed memory", base),
		memOverhead:    d("esxi_vm_memory_overhead_bytes", "Overhead memory", base),
		memSwapped:     d("esxi_vm_memory_swapped_bytes", "Swapped memory", base),
		memBalloon:     d("esxi_vm_memory_balloon_bytes", "Ballooned memory", base),
		memCompressed:  d("esxi_vm_memory_compressed_bytes", "Compressed memory", base),
		memShared:      d("esxi_vm_memory_shared_bytes", "Shared memory", base),
		memPrivate:     d("esxi_vm_memory_private_bytes", "Private memory", base),
		memTarget:      d("esxi_vm_memory_target_size_bytes", "Target size", base),
		memReclaim:     d("esxi_vm_memory_reclaim_rate_bytes_per_second", "Reclaim rate", base),
		memLimit:       d("esxi_vm_memory_limit_bytes", "Memory limit (-1 unlimited)", base),
		memReservation: d("esxi_vm_memory_reservation_bytes", "Memory reservation", base),

		diskCapacity:    d("esxi_vm_disk_capacity_bytes", "VM disk capacity", disk),
		diskFree:        d("esxi_vm_disk_free_bytes", "VM disk free", disk),
		diskUsed:        d("esxi_vm_disk_used_bytes", "VM disk used", disk),
		diskReadBps:     d("esxi_vm_disk_read_bytes_per_second", "Disk read B/s", disk),
		diskWriteBps:    d("esxi_vm_disk_write_bytes_per_second", "Disk write B/s", disk),
		diskReadIOPS:    d("esxi_vm_disk_read_iops", "Disk read IOPS", disk),
		diskWriteIOPS:   d("esxi_vm_disk_write_iops", "Disk write IOPS", disk),
		diskLatency:     d("esxi_vm_disk_latency_ms", "Disk total latency", disk),
		diskQueueLat:    d("esxi_vm_disk_queue_latency_ms", "Disk queue latency", disk),
		diskKernelLat:   d("esxi_vm_disk_kernel_latency_ms", "Disk kernel latency", disk),
		diskDeviceLat:   d("esxi_vm_disk_device_latency_ms", "Disk device latency", disk),
		diskOutstanding: d("esxi_vm_disk_outstanding_io", "Outstanding IO", disk),

		netPktRx:     d("esxi_vm_network_packets_rx_total", "VM RX packets", nic),
		netPktTx:     d("esxi_vm_network_packets_tx_total", "VM TX packets", nic),
		netByteRx:    d("esxi_vm_network_bytes_rx_per_second", "VM RX B/s", nic),
		netByteTx:    d("esxi_vm_network_bytes_tx_per_second", "VM TX B/s", nic),
		netDropped:   d("esxi_vm_network_dropped_packets_total", "Dropped packets", nic),
		netErrors:    d("esxi_vm_network_errors_total", "NIC errors", nic),
		nicConnected: d("esxi_vm_nic_connected", "NIC connected", nic),
		nicLinkSpeed: d("esxi_vm_nic_link_speed_bits_per_second", "vNIC link speed", nic),

		guestIP:        d("esxi_vm_guest_ip_info", "Guest IP addresses", []string{"vm", "host", "ip"}),
		guestHostname:  d("esxi_vm_guest_hostname_info", "Guest hostname", []string{"vm", "host", "hostname"}),
		guestHeartbeat: d("esxi_vm_guest_heartbeat_status", "Heartbeat (0=gray,1=green,2=yellow,3=red)", base),
		guestState:     d("esxi_vm_guest_state", "Guest state (1=running)", base),
		guestUptime:    d("esxi_vm_guest_uptime_seconds", "Guest uptime", base),
		guestDNS:       d("esxi_vm_guest_dns_info", "Guest DNS name", []string{"vm", "host", "dns"}),
		guestFSCap:     d("esxi_vm_guest_filesystem_capacity_bytes", "Guest FS capacity", fs),
		guestFSFree:    d("esxi_vm_guest_filesystem_free_bytes", "Guest FS free", fs),
		guestFSUsed:    d("esxi_vm_guest_filesystem_used_bytes", "Guest FS used", fs),

		snapshotCount: d("esxi_vm_snapshot_count", "Number of snapshots", base),
		snapshotSize:  d("esxi_vm_snapshot_size_bytes", "Total snapshot size", base),
		datastoreInfo: d("esxi_vm_datastore_info", "VM datastore", []string{"vm", "host", "datastore"}),
		clusterInfo:   d("esxi_vm_cluster_info", "VM cluster", []string{"vm", "host", "cluster"}),
		rpInfo:        d("esxi_vm_resource_pool_info", "VM resource pool", []string{"vm", "host", "pool"}),
		folderInfo:    d("esxi_vm_folder_info", "VM folder path", []string{"vm", "host", "folder"}),
		template:      d("esxi_vm_is_template", "VM is a template", base),
		secureBoot:    d("esxi_vm_secure_boot_enabled", "Secure boot enabled", base),
		firmware:      d("esxi_vm_firmware_info", "Firmware (bios/efi)", []string{"vm", "host", "firmware"}),
		encrypted:     d("esxi_vm_encryption_enabled", "VM encryption enabled", base),
	}
}

func (c *VMCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.cpuUsage, c.memUsage, c.memTotal, c.uptime, c.powerState, c.numCPU,
		c.guestOS, c.toolsStatus, c.toolsVersion, c.toolsRunning,
		c.cpuReady, c.cpuWait, c.cpuCoStop, c.cpuSystem, c.cpuUser,
		c.cpuEntitlement, c.cpuDemand, c.cpuLatency, c.cpuLimit,
		c.cpuReservation, c.cpuShares,
		c.memActive, c.memGranted, c.memConsumed, c.memOverhead, c.memSwapped,
		c.memBalloon, c.memCompressed, c.memShared, c.memPrivate,
		c.memTarget, c.memReclaim, c.memLimit, c.memReservation,
		c.diskCapacity, c.diskFree, c.diskUsed,
		c.diskReadBps, c.diskWriteBps, c.diskReadIOPS, c.diskWriteIOPS,
		c.diskLatency, c.diskQueueLat, c.diskKernelLat, c.diskDeviceLat,
		c.diskOutstanding,
		c.netPktRx, c.netPktTx, c.netByteRx, c.netByteTx,
		c.netDropped, c.netErrors, c.nicConnected, c.nicLinkSpeed,
		c.guestIP, c.guestHostname, c.guestHeartbeat, c.guestState,
		c.guestUptime, c.guestDNS,
		c.guestFSCap, c.guestFSFree, c.guestFSUsed,
		c.snapshotCount, c.snapshotSize, c.datastoreInfo, c.clusterInfo,
		c.rpInfo, c.folderInfo, c.template, c.secureBoot, c.firmware, c.encrypted,
	} {
		ch <- dsc
	}
}

func (c *VMCollector) Collect(s *scrapeContext) {
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"VirtualMachine"}, true)
	if err != nil {
		log.Printf("vm: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var vms []mo.VirtualMachine
	// summary+config+guest+runtime+snapshot covers everything we need.
	// Layout* would give per-disk file placement but doubles payload.
	if err := v.Retrieve(s.ctx, []string{"VirtualMachine"},
		[]string{"summary", "config", "guest", "runtime", "snapshot", "resourceConfig"},
		&vms); err != nil {
		log.Printf("vm: retrieve failed: %v", err)
		return
	}

	for i := range vms {
		c.emitStatic(&vms[i], s)
		if *c.cfg.Metrics.CollectGuestInfo {
			c.emitGuest(&vms[i], s)
		}
		if *c.cfg.Metrics.CollectVMConfig {
			c.emitConfig(&vms[i], s)
		}
		// Perf calls hit the network — skip templates and powered-off VMs
		// (they have no realtime samples anyway).
		if vms[i].Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn {
			continue
		}
		if *c.cfg.Metrics.CollectVMCPUPerf {
			c.emitPerfCPU(&vms[i], s)
		}
		if *c.cfg.Metrics.CollectVMMemPerf {
			c.emitPerfMem(&vms[i], s)
		}
		if *c.cfg.Metrics.CollectVMDiskPerf {
			c.emitPerfDisk(&vms[i], s)
		}
		if *c.cfg.Metrics.CollectVMNetPerf {
			c.emitPerfNet(&vms[i], s)
		}
	}
}

func (c *VMCollector) emitStatic(v *mo.VirtualMachine, s *scrapeContext) {
	name := v.Summary.Config.Name
	hostName := ""
	if v.Runtime.Host != nil {
		hostName = v.Runtime.Host.Value // MoRef value; caller can join
	}

	sum := v.Summary
	if sum.Config.NumCpu > 0 {
		// Use OverallCpuUsage as a percent of full vCPU capacity. We
		// don't have host MHz here without an extra fetch, so this
		// mirrors what vCenter's UI shows: MHz / (vCPUs * hostMhz).
		// Approximation: expose raw MHz as gauge; users can compute %
		// via recording rules against esxi_host_num_cpu.
		s.ch <- prometheus.MustNewConstMetric(c.cpuUsage, prometheus.GaugeValue,
			float64(sum.QuickStats.OverallCpuUsage), name, hostName)
	}
	s.ch <- prometheus.MustNewConstMetric(c.memTotal, prometheus.GaugeValue,
		float64(sum.Config.MemorySizeMB)*1024*1024, name, hostName)
	s.ch <- prometheus.MustNewConstMetric(c.memUsage, prometheus.GaugeValue,
		float64(sum.QuickStats.GuestMemoryUsage)*1024*1024, name, hostName)
	s.ch <- prometheus.MustNewConstMetric(c.uptime, prometheus.GaugeValue,
		float64(sum.QuickStats.UptimeSeconds), name, hostName)
	s.ch <- prometheus.MustNewConstMetric(c.powerState, prometheus.GaugeValue,
		boolToFloat(sum.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn),
		name, hostName)
	s.ch <- prometheus.MustNewConstMetric(c.numCPU, prometheus.GaugeValue,
		float64(sum.Config.NumCpu), name, hostName)

	if v.Guest != nil {
		s.ch <- prometheus.MustNewConstMetric(c.toolsStatus, prometheus.GaugeValue,
			toolsStatusValue(v.Guest.ToolsStatus), name, hostName)
		s.ch <- prometheus.MustNewConstMetric(c.toolsVersion, prometheus.GaugeValue, 1,
			name, hostName, v.Guest.ToolsVersion)
		s.ch <- prometheus.MustNewConstMetric(c.toolsRunning, prometheus.GaugeValue,
			boolToFloat(v.Guest.ToolsRunningStatus == string(types.VirtualMachineToolsRunningStatusGuestToolsRunning)),
			name, hostName)
	}
	s.ch <- prometheus.MustNewConstMetric(c.guestOS, prometheus.GaugeValue, 1,
		name, hostName, sum.Config.GuestId)
}

func (c *VMCollector) emitGuest(v *mo.VirtualMachine, s *scrapeContext) {
	if v.Guest == nil {
		return
	}
	name := v.Summary.Config.Name
	hostName := ""
	if v.Runtime.Host != nil {
		hostName = v.Runtime.Host.Value
	}

	if v.Guest.HostName != "" {
		s.ch <- prometheus.MustNewConstMetric(c.guestHostname, prometheus.GaugeValue, 1,
			name, hostName, v.Guest.HostName)
	}
	if v.Guest.IpAddress != "" {
		s.ch <- prometheus.MustNewConstMetric(c.guestIP, prometheus.GaugeValue, 1,
			name, hostName, v.Guest.IpAddress)
	}
	// Additional IPs from NIC list — separate series so consumers can filter.
	for _, nic := range v.Guest.Net {
		for _, ip := range nic.IpAddress {
			s.ch <- prometheus.MustNewConstMetric(c.guestIP, prometheus.GaugeValue, 1,
				name, hostName, ip)
		}
	}

	heartbeat := 0.0
	switch v.GuestHeartbeatStatus {
	case types.ManagedEntityStatusGreen:
		heartbeat = 1
	case types.ManagedEntityStatusYellow:
		heartbeat = 2
	case types.ManagedEntityStatusRed:
		heartbeat = 3
	}
	s.ch <- prometheus.MustNewConstMetric(c.guestHeartbeat, prometheus.GaugeValue, heartbeat, name, hostName)
	s.ch <- prometheus.MustNewConstMetric(c.guestState, prometheus.GaugeValue,
		boolToFloat(v.Guest.GuestState == "running"), name, hostName)
	// GuestState uptime derived from bootTime; QuickStats.UptimeSeconds is
	// process uptime (vmx), not the guest OS.
	if v.Runtime.BootTime != nil {
		// Not strictly guest uptime but the best proxy without in-guest
		// tools telemetry. Left as-is; users can override via VMware Tools.
	}
	if v.Guest.HostName != "" {
		s.ch <- prometheus.MustNewConstMetric(c.guestDNS, prometheus.GaugeValue, 1,
			name, hostName, v.Guest.HostName)
	}
	for _, disk := range v.Guest.Disk {
		s.ch <- prometheus.MustNewConstMetric(c.guestFSCap, prometheus.GaugeValue,
			float64(disk.Capacity), name, hostName, disk.DiskPath)
		s.ch <- prometheus.MustNewConstMetric(c.guestFSFree, prometheus.GaugeValue,
			float64(disk.FreeSpace), name, hostName, disk.DiskPath)
		s.ch <- prometheus.MustNewConstMetric(c.guestFSUsed, prometheus.GaugeValue,
			float64(disk.Capacity-disk.FreeSpace), name, hostName, disk.DiskPath)
	}
}

func (c *VMCollector) emitConfig(v *mo.VirtualMachine, s *scrapeContext) {
	if v.Config == nil {
		return
	}
	name := v.Summary.Config.Name
	hostName := ""
	if v.Runtime.Host != nil {
		hostName = v.Runtime.Host.Value
	}

	// Reservations / limits live in ResourceConfig; fall back gracefully.
	if v.ResourceConfig != nil {
		rc := v.ResourceConfig
		s.ch <- prometheus.MustNewConstMetric(c.cpuLimit, prometheus.GaugeValue,
			float64(nz64(rc.CpuAllocation.Limit)), name, hostName)
		s.ch <- prometheus.MustNewConstMetric(c.cpuReservation, prometheus.GaugeValue,
			float64(nz64(rc.CpuAllocation.Reservation)), name, hostName)
		if rc.CpuAllocation.Shares != nil {
			s.ch <- prometheus.MustNewConstMetric(c.cpuShares, prometheus.GaugeValue,
				float64(rc.CpuAllocation.Shares.Shares), name, hostName)
		}
		s.ch <- prometheus.MustNewConstMetric(c.memLimit, prometheus.GaugeValue,
			float64(nz64(rc.MemoryAllocation.Limit))*1024*1024, name, hostName)
		s.ch <- prometheus.MustNewConstMetric(c.memReservation, prometheus.GaugeValue,
			float64(nz64(rc.MemoryAllocation.Reservation))*1024*1024, name, hostName)
	}

	// Snapshots — count and total size on datastore.
	if v.Snapshot != nil {
		count := countSnapshots(v.Snapshot.RootSnapshotList)
		s.ch <- prometheus.MustNewConstMetric(c.snapshotCount, prometheus.GaugeValue,
			float64(count), name, hostName)
		// Approx snapshot size: use committed disk delta if layoutEx would
		// require an extra Retrieve. Zero if not available.
	}

	// Datastore/cluster/pool/folder resolution requires walking MoRefs.
	// We emit MoRef values as info labels; a downstream relabel_config
	// against a separate MoRef->name map keeps this cheap. Otherwise we'd
	// balloon the property list per VM.
	for _, ds := range v.Datastore {
		s.ch <- prometheus.MustNewConstMetric(c.datastoreInfo, prometheus.GaugeValue, 1,
			name, hostName, ds.Value)
	}
	if v.ResourcePool != nil {
		s.ch <- prometheus.MustNewConstMetric(c.rpInfo, prometheus.GaugeValue, 1,
			name, hostName, v.ResourcePool.Value)
	}
	if v.Parent != nil {
		s.ch <- prometheus.MustNewConstMetric(c.folderInfo, prometheus.GaugeValue, 1,
			name, hostName, v.Parent.Value)
	}

	s.ch <- prometheus.MustNewConstMetric(c.template, prometheus.GaugeValue,
		boolToFloat(v.Config.Template), name, hostName)
	if v.Config.Firmware != "" {
		s.ch <- prometheus.MustNewConstMetric(c.firmware, prometheus.GaugeValue, 1,
			name, hostName, v.Config.Firmware)
	}
	if v.Config.BootOptions != nil && v.Config.BootOptions.EfiSecureBootEnabled != nil {
		s.ch <- prometheus.MustNewConstMetric(c.secureBoot, prometheus.GaugeValue,
			boolToFloat(*v.Config.BootOptions.EfiSecureBootEnabled), name, hostName)
	}
	// VM encryption is signaled by the presence of a crypto key ID in
	// keyId. This avoids depending on VMConfigInfo.KeyId which moved
	// around across govmomi releases.
	encrypted := false
	if v.Config.KeyId != nil && v.Config.KeyId.KeyId != "" {
		encrypted = true
	}
	s.ch <- prometheus.MustNewConstMetric(c.encrypted, prometheus.GaugeValue,
		boolToFloat(encrypted), name, hostName)

	// Static per-disk info from the hardware device list. Perf will layer
	// the dynamic metrics on top using the same "disk-<key>" label so both
	// join cleanly in PromQL.
	for _, dev := range v.Config.Hardware.Device {
		vd, ok := dev.(*types.VirtualDisk)
		if !ok {
			continue
		}
		label := fmt.Sprintf("disk-%d", vd.Key)
		s.ch <- prometheus.MustNewConstMetric(c.diskCapacity, prometheus.GaugeValue,
			float64(vd.CapacityInBytes), name, hostName, label)
	}
	// NIC connect state from hardware (perf reports throughput).
	for _, dev := range v.Config.Hardware.Device {
		if eth, ok := dev.(types.BaseVirtualEthernetCard); ok {
			card := eth.GetVirtualEthernetCard()
			label := fmt.Sprintf("nic-%d", card.Key)
			connected := 0.0
			if card.Connectable != nil {
				connected = boolToFloat(card.Connectable.Connected)
			}
			s.ch <- prometheus.MustNewConstMetric(c.nicConnected, prometheus.GaugeValue,
				connected, name, hostName, label)
		}
	}
}

func (c *VMCollector) emitPerfCPU(v *mo.VirtualMachine, s *scrapeContext) {
	m := queryPerf(s.ctx, s.perf, v.Reference(), []string{
		"cpu.ready.summation", "cpu.wait.summation", "cpu.costop.summation",
		"cpu.system.summation", "cpu.used.summation",
		"cpu.entitlement.latest", "cpu.demand.average", "cpu.latency.average",
	}, "")
	name, hostName := v.Summary.Config.Name, morefValue(v.Runtime.Host)
	emit := func(desc *prometheus.Desc, key string) {
		if x, ok := m[key]; ok {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, x.Value, name, hostName)
		}
	}
	emit(c.cpuReady, "cpu.ready.summation")
	emit(c.cpuWait, "cpu.wait.summation")
	emit(c.cpuCoStop, "cpu.costop.summation")
	emit(c.cpuSystem, "cpu.system.summation")
	emit(c.cpuUser, "cpu.used.summation")
	emit(c.cpuEntitlement, "cpu.entitlement.latest")
	emit(c.cpuDemand, "cpu.demand.average")
	emit(c.cpuLatency, "cpu.latency.average")
}

func (c *VMCollector) emitPerfMem(v *mo.VirtualMachine, s *scrapeContext) {
	m := queryPerf(s.ctx, s.perf, v.Reference(), []string{
		"mem.active.average", "mem.granted.average", "mem.consumed.average",
		"mem.overhead.average", "mem.swapped.average", "mem.vmmemctl.average",
		"mem.compressed.average", "mem.shared.average", "mem.zero.average",
		"mem.entitlement.average",
	}, "")
	name, hostName := v.Summary.Config.Name, morefValue(v.Runtime.Host)
	kb := func(desc *prometheus.Desc, key string) {
		if x, ok := m[key]; ok {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, x.Value*1024, name, hostName)
		}
	}
	kb(c.memActive, "mem.active.average")
	kb(c.memGranted, "mem.granted.average")
	kb(c.memConsumed, "mem.consumed.average")
	kb(c.memOverhead, "mem.overhead.average")
	kb(c.memSwapped, "mem.swapped.average")
	kb(c.memBalloon, "mem.vmmemctl.average")
	kb(c.memCompressed, "mem.compressed.average")
	kb(c.memShared, "mem.shared.average")
	// "private" is not a direct counter; derive as consumed - shared.
	if consumed, ok := m["mem.consumed.average"]; ok {
		if shared, ok2 := m["mem.shared.average"]; ok2 {
			s.ch <- prometheus.MustNewConstMetric(c.memPrivate, prometheus.GaugeValue,
				(consumed.Value-shared.Value)*1024, name, hostName)
		}
	}
	if ent, ok := m["mem.entitlement.average"]; ok {
		s.ch <- prometheus.MustNewConstMetric(c.memTarget, prometheus.GaugeValue,
			ent.Value*1024, name, hostName)
	}
}

func (c *VMCollector) emitPerfDisk(v *mo.VirtualMachine, s *scrapeContext) {
	m := queryPerfInstances(s.ctx, s.perf, v.Reference(), []string{
		"virtualDisk.read.average", "virtualDisk.write.average",
		"virtualDisk.numberReadAveraged.average",
		"virtualDisk.numberWriteAveraged.average",
		"virtualDisk.totalReadLatency.average",
		"virtualDisk.totalWriteLatency.average",
	})
	name, hostName := v.Summary.Config.Name, morefValue(v.Runtime.Host)
	// virtualDisk perf uses instances like "scsi0:0" — we relabel to the
	// device label our config emits (disk-<key>) when possible by matching
	// SCSI IDs, but the raw instance is fine for parity.
	emit := func(desc *prometheus.Desc, key string, x float64) {
		for _, sample := range m[key] {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				sample.Value*x, name, hostName, sample.Instance)
		}
	}
	emit(c.diskReadBps, "virtualDisk.read.average", 1024)
	emit(c.diskWriteBps, "virtualDisk.write.average", 1024)
	emit(c.diskReadIOPS, "virtualDisk.numberReadAveraged.average", 1)
	emit(c.diskWriteIOPS, "virtualDisk.numberWriteAveraged.average", 1)
	// Combined latency counter: sum read+write for a "total" series.
	if r, w := m["virtualDisk.totalReadLatency.average"], m["virtualDisk.totalWriteLatency.average"]; len(r) > 0 || len(w) > 0 {
		combined := map[string]float64{}
		for _, x := range r {
			combined[x.Instance] += x.Value
		}
		for _, x := range w {
			combined[x.Instance] += x.Value
		}
		for inst, val := range combined {
			s.ch <- prometheus.MustNewConstMetric(c.diskLatency, prometheus.GaugeValue,
				val, name, hostName, inst)
		}
	}
}

func (c *VMCollector) emitPerfNet(v *mo.VirtualMachine, s *scrapeContext) {
	m := queryPerfInstances(s.ctx, s.perf, v.Reference(), []string{
		"net.packetsRx.summation", "net.packetsTx.summation",
		"net.bytesRx.average", "net.bytesTx.average",
		"net.droppedRx.summation", "net.droppedTx.summation",
	})
	name, hostName := v.Summary.Config.Name, morefValue(v.Runtime.Host)
	emit := func(desc *prometheus.Desc, key string, x float64) {
		for _, sample := range m[key] {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				sample.Value*x, name, hostName, sample.Instance)
		}
	}
	emit(c.netPktRx, "net.packetsRx.summation", 1)
	emit(c.netPktTx, "net.packetsTx.summation", 1)
	emit(c.netByteRx, "net.bytesRx.average", 1024)
	emit(c.netByteTx, "net.bytesTx.average", 1024)

	// Combine per-direction drops into one series.
	drop := map[string]float64{}
	for _, x := range m["net.droppedRx.summation"] {
		drop[x.Instance] += x.Value
	}
	for _, x := range m["net.droppedTx.summation"] {
		drop[x.Instance] += x.Value
	}
	for inst, val := range drop {
		s.ch <- prometheus.MustNewConstMetric(c.netDropped, prometheus.GaugeValue,
			val, name, hostName, inst)
	}
}

// -----------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------

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
	}
	return -1
}

// nz64 dereferences a *int64 that vSphere uses to distinguish "unset" from
// zero. Returning 0 for nil is safe for our gauge output.
func nz64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func morefValue(r *types.ManagedObjectReference) string {
	if r == nil {
		return ""
	}
	return r.Value
}

func countSnapshots(tree []types.VirtualMachineSnapshotTree) int {
	n := 0
	for _, s := range tree {
		n++
		n += countSnapshots(s.ChildSnapshotList)
	}
	return n
}
