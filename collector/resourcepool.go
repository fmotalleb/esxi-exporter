package collector

import (
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"

	"github.com/fmotalleb/esxi-exporter/config"
)

// ResourcePoolCollector produces esxi_resource_pool_* metrics. Resource
// pools are hierarchical — nested pools inherit their parent's shares —
// but we emit each pool as an independent series with its own MoRef, and
// leave the parent/child relationship to a downstream join.

// Root resource pools are literally named "Resources" on every ClusterComputeResource
// and standalone host. Emitting only the pool name causes N-way dedup collisions in
// vCenter environments. We add an "owner" label that carries the parent compute
// resource's MoRef so identical names on different hosts/clusters stay distinct.
type ResourcePoolCollector struct {
	cfg *config.Config

	cpuReservation *prometheus.Desc
	cpuLimit       *prometheus.Desc
	cpuShares      *prometheus.Desc
	memReservation *prometheus.Desc
	memLimit       *prometheus.Desc
	memShares      *prometheus.Desc
	cpuUsage       *prometheus.Desc
	memUsage       *prometheus.Desc
	vmCount        *prometheus.Desc
}

func NewResourcePoolCollector(cfg *config.Config) *ResourcePoolCollector {
	lbl := []string{"pool", "owner"}
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &ResourcePoolCollector{
		cfg:            cfg,
		cpuReservation: d("esxi_resource_pool_cpu_reservation_mhz", "CPU reservation (MHz)", lbl),
		cpuLimit:       d("esxi_resource_pool_cpu_limit_mhz", "CPU limit (MHz, -1 unlimited)", lbl),
		cpuShares:      d("esxi_resource_pool_cpu_shares", "CPU shares", lbl),
		memReservation: d("esxi_resource_pool_memory_reservation_bytes", "Memory reservation", lbl),
		memLimit:       d("esxi_resource_pool_memory_limit_bytes", "Memory limit", lbl),
		memShares:      d("esxi_resource_pool_memory_shares", "Memory shares", lbl),
		cpuUsage:       d("esxi_resource_pool_cpu_usage_mhz", "Current CPU usage (MHz)", lbl),
		memUsage:       d("esxi_resource_pool_memory_usage_bytes", "Current memory usage", lbl),
		vmCount:        d("esxi_resource_pool_vm_count", "VMs in pool", lbl),
	}
}

func (c *ResourcePoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.cpuReservation, c.cpuLimit, c.cpuShares,
		c.memReservation, c.memLimit, c.memShares,
		c.cpuUsage, c.memUsage, c.vmCount,
	} {
		ch <- dsc
	}
}

func (c *ResourcePoolCollector) Collect(s *scrapeContext) {
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"ResourcePool"}, true)
	if err != nil {
		log.Printf("resource_pool: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var rps []mo.ResourcePool
	if err := v.Retrieve(s.ctx, []string{"ResourcePool"},
		[]string{"name", "config", "summary", "vm", "owner"}, &rps); err != nil {
		log.Printf("resource_pool: retrieve failed: %v", err)
		return
	}
	for i := range rps {
		c.emit(&rps[i], s)
	}
}

func (c *ResourcePoolCollector) emit(rp *mo.ResourcePool, s *scrapeContext) {
	name := rp.Name
	// Owner is a *ManagedObjectReference. It's non-nil for every real
	// pool; we use its Value (e.g. "domain-c123") so identically-named
	// root pools on different clusters/hosts don't collide.
	owner := ""
	if rp.Owner.Value != "" {
		owner = rp.Owner.Value
	}

	cpu := rp.Config.CpuAllocation
	mem := rp.Config.MemoryAllocation

	s.ch <- prometheus.MustNewConstMetric(c.cpuReservation, prometheus.GaugeValue,
		float64(nz64(cpu.Reservation)), name, owner)
	s.ch <- prometheus.MustNewConstMetric(c.cpuLimit, prometheus.GaugeValue,
		float64(nz64(cpu.Limit)), name, owner)
	if cpu.Shares != nil {
		s.ch <- prometheus.MustNewConstMetric(c.cpuShares, prometheus.GaugeValue,
			float64(cpu.Shares.Shares), name, owner)
	}
	s.ch <- prometheus.MustNewConstMetric(c.memReservation, prometheus.GaugeValue,
		float64(nz64(mem.Reservation))*1024*1024, name, owner)
	s.ch <- prometheus.MustNewConstMetric(c.memLimit, prometheus.GaugeValue,
		float64(nz64(mem.Limit))*1024*1024, name, owner)
	if mem.Shares != nil {
		s.ch <- prometheus.MustNewConstMetric(c.memShares, prometheus.GaugeValue,
			float64(mem.Shares.Shares), name, owner)
	}

	if rp.Summary != nil {
		qs := rp.Summary.GetResourcePoolSummary().QuickStats
		if qs != nil {
			s.ch <- prometheus.MustNewConstMetric(c.cpuUsage, prometheus.GaugeValue,
				float64(qs.OverallCpuUsage), name, owner)
			s.ch <- prometheus.MustNewConstMetric(c.memUsage, prometheus.GaugeValue,
				float64(qs.GuestMemoryUsage)*1024*1024, name, owner)
		}
	}
	s.ch <- prometheus.MustNewConstMetric(c.vmCount, prometheus.GaugeValue,
		float64(len(rp.Vm)), name, owner)
}
