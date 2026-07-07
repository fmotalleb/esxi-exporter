package collector

import (
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// ClusterCollector produces esxi_cluster_* metrics. Cluster objects only
// exist under vCenter; against a bare ESXi endpoint the container view
// simply returns empty and this collector emits nothing. That's cheaper
// than adding an explicit vCenter capability probe.

type ClusterCollector struct {
	cfg *config.Config

	haEnabled     *prometheus.Desc
	drsEnabled    *prometheus.Desc
	drsAutomation *prometheus.Desc
	hostCount     *prometheus.Desc
	vmCount       *prometheus.Desc
	cpuUtil       *prometheus.Desc
	memUtil       *prometheus.Desc
	cpuOvercommit *prometheus.Desc
	memOvercommit *prometheus.Desc
	failoverCap   *prometheus.Desc
	admissionCtrl *prometheus.Desc
	totalCPU      *prometheus.Desc
	totalMem      *prometheus.Desc
	usedCPU       *prometheus.Desc
	usedMem       *prometheus.Desc
	effectiveHost *prometheus.Desc
}

func NewClusterCollector(cfg *config.Config) *ClusterCollector {
	lbl := []string{"cluster"}
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &ClusterCollector{
		cfg:           cfg,
		haEnabled:     d("esxi_cluster_ha_enabled", "vSphere HA enabled", lbl),
		drsEnabled:    d("esxi_cluster_drs_enabled", "DRS enabled", lbl),
		drsAutomation: d("esxi_cluster_drs_automation_level", "DRS automation (manual/partial/full)", []string{"cluster", "level"}),
		hostCount:     d("esxi_cluster_host_count", "Hosts in cluster", lbl),
		vmCount:       d("esxi_cluster_vm_count", "VMs in cluster", lbl),
		cpuUtil:       d("esxi_cluster_cpu_utilization_percent", "Cluster CPU utilization %", lbl),
		memUtil:       d("esxi_cluster_memory_utilization_percent", "Cluster memory utilization %", lbl),
		cpuOvercommit: d("esxi_cluster_cpu_overcommit_ratio", "CPU overcommit (allocated/capacity)", lbl),
		memOvercommit: d("esxi_cluster_memory_overcommit_ratio", "Memory overcommit ratio", lbl),
		failoverCap:   d("esxi_cluster_failover_capacity", "Failover capacity slots/hosts", lbl),
		admissionCtrl: d("esxi_cluster_admission_control_enabled", "Admission control on", lbl),
		totalCPU:      d("esxi_cluster_total_cpu_mhz", "Total CPU capacity (MHz)", lbl),
		totalMem:      d("esxi_cluster_total_memory_bytes", "Total memory capacity", lbl),
		usedCPU:       d("esxi_cluster_used_cpu_mhz", "Used CPU (MHz)", lbl),
		usedMem:       d("esxi_cluster_used_memory_bytes", "Used memory", lbl),
		effectiveHost: d("esxi_cluster_effective_hosts", "Effective (connected, not maint) hosts", lbl),
	}
}

func (c *ClusterCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.haEnabled, c.drsEnabled, c.drsAutomation,
		c.hostCount, c.vmCount, c.cpuUtil, c.memUtil,
		c.cpuOvercommit, c.memOvercommit,
		c.failoverCap, c.admissionCtrl,
		c.totalCPU, c.totalMem, c.usedCPU, c.usedMem, c.effectiveHost,
	} {
		ch <- dsc
	}
}

func (c *ClusterCollector) Collect(s *scrapeContext) {
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"ClusterComputeResource"}, true)
	if err != nil {
		log.Printf("cluster: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var clusters []mo.ClusterComputeResource
	if err := v.Retrieve(s.ctx, []string{"ClusterComputeResource"},
		[]string{"summary", "configurationEx", "host", "resourcePool"},
		&clusters); err != nil {
		log.Printf("cluster: retrieve failed: %v", err)
		return
	}

	for i := range clusters {
		c.emit(&clusters[i], s)
	}
}

func (c *ClusterCollector) emit(cl *mo.ClusterComputeResource, s *scrapeContext) {
	name := cl.Name

	if sum, ok := cl.Summary.(*types.ClusterComputeResourceSummary); ok {
		s.ch <- prometheus.MustNewConstMetric(c.totalCPU, prometheus.GaugeValue,
			float64(sum.TotalCpu), name)
		s.ch <- prometheus.MustNewConstMetric(c.totalMem, prometheus.GaugeValue,
			float64(sum.TotalMemory), name)
		s.ch <- prometheus.MustNewConstMetric(c.usedCPU, prometheus.GaugeValue,
			float64(sum.TotalCpu-sum.EffectiveCpu), name)
		s.ch <- prometheus.MustNewConstMetric(c.effectiveHost, prometheus.GaugeValue,
			float64(sum.NumEffectiveHosts), name)
		if sum.TotalCpu > 0 {
			s.ch <- prometheus.MustNewConstMetric(c.cpuUtil, prometheus.GaugeValue,
				float64(sum.TotalCpu-sum.EffectiveCpu)/float64(sum.TotalCpu)*100, name)
		}
		// EffectiveMemory is MB; convert for consistency with total.
		effectiveMemBytes := int64(sum.EffectiveMemory) * 1024 * 1024
		if sum.TotalMemory > 0 {
			s.ch <- prometheus.MustNewConstMetric(c.memUtil, prometheus.GaugeValue,
				float64(sum.TotalMemory-effectiveMemBytes)/float64(sum.TotalMemory)*100, name)
			s.ch <- prometheus.MustNewConstMetric(c.usedMem, prometheus.GaugeValue,
				float64(sum.TotalMemory-effectiveMemBytes), name)
		}
	}
	s.ch <- prometheus.MustNewConstMetric(c.hostCount, prometheus.GaugeValue,
		float64(len(cl.Host)), name)

	// ConfigurationEx contains HA/DRS/admission control settings.
	if cfg, ok := cl.ConfigurationEx.(*types.ClusterConfigInfoEx); ok {
		if cfg.DasConfig.Enabled != nil {
			s.ch <- prometheus.MustNewConstMetric(c.haEnabled, prometheus.GaugeValue,
				boolToFloat(*cfg.DasConfig.Enabled), name)
		}
		if cfg.DasConfig.AdmissionControlEnabled != nil {
			s.ch <- prometheus.MustNewConstMetric(c.admissionCtrl, prometheus.GaugeValue,
				boolToFloat(*cfg.DasConfig.AdmissionControlEnabled), name)
		}
		if cfg.DrsConfig.Enabled != nil {
			s.ch <- prometheus.MustNewConstMetric(c.drsEnabled, prometheus.GaugeValue,
				boolToFloat(*cfg.DrsConfig.Enabled), name)
		}
		s.ch <- prometheus.MustNewConstMetric(c.drsAutomation, prometheus.GaugeValue, 1,
			name, string(cfg.DrsConfig.DefaultVmBehavior))
	}
	// VM count via container view scan — a simple len() on the resource
	// pool tree would double-count nested pools, so use the RP root VM
	// list if we cared. For now we report the hostCount and let the VM
	// collector aggregate.
}
