package collector

import (
	"github.com/fmotalleb/go-tools/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// NetworkCollector produces esxi_network_* metrics covering standard
// switches, distributed switches, portgroups, and physical NIC state at
// the host level. Per-NIC throughput lives in host.go (it comes from perf
// counters on the HostSystem, not the Network objects).

type NetworkCollector struct {
	cfg *config.Config

	stdSwitches      *prometheus.Desc
	dvSwitches       *prometheus.Desc
	portgroups       *prometheus.Desc
	activePorts      *prometheus.Desc
	blockedPorts     *prometheus.Desc
	mtu              *prometheus.Desc
	vlanInfo         *prometheus.Desc
	uplinks          *prometheus.Desc
	nicFailures      *prometheus.Desc
}

func NewNetworkCollector(cfg *config.Config) *NetworkCollector {
	host := []string{"host"}
	sw := []string{"host", "switch"}
	pg := []string{"host", "switch", "portgroup"}
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &NetworkCollector{
		cfg:          cfg,
		stdSwitches:  d("esxi_network_standard_switches", "Standard vSwitches per host", host),
		dvSwitches:   d("esxi_network_distributed_switches", "DVS uplinks per host", host),
		portgroups:   d("esxi_network_portgroups", "Portgroups per switch", sw),
		activePorts:  d("esxi_network_active_ports", "Active ports on switch", sw),
		blockedPorts: d("esxi_network_blocked_ports", "Blocked ports on switch", sw),
		mtu:          d("esxi_network_switch_mtu_bytes", "Switch MTU", sw),
		vlanInfo:     d("esxi_network_portgroup_vlan", "Portgroup VLAN ID", pg),
		uplinks:      d("esxi_network_switch_uplinks", "Number of uplinks", sw),
		nicFailures:  d("esxi_network_nic_failures_total", "Teaming NIC failures", []string{"host", "nic"}),
	}
}

func (c *NetworkCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.stdSwitches, c.dvSwitches, c.portgroups, c.activePorts,
		c.blockedPorts, c.mtu, c.vlanInfo, c.uplinks, c.nicFailures,
	} {
		ch <- dsc
	}
}

func (c *NetworkCollector) Collect(s *scrapeContext) {
	// Standard switches + portgroups live under HostSystem.config.network,
	// so we iterate hosts. Distributed switches are their own MoRef type
	// and we retrieve them separately (only present in vCenter).
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"HostSystem"}, true)
	if err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("network: create view failed", "error", err)
		return
	}
	defer v.Destroy(s.ctx)

	var hosts []mo.HostSystem
	if err := v.Retrieve(s.ctx, []string{"HostSystem"},
		[]string{"name", "summary.config.name", "config.network"}, &hosts); err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("network: retrieve hosts failed", "error", err)
		return
	}
	for i := range hosts {
		c.emitHost(&hosts[i], s)
	}

	// Distributed switches — best effort.
	dv, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"DistributedVirtualSwitch"}, true)
	if err == nil {
		defer dv.Destroy(s.ctx)
		var dvss []mo.DistributedVirtualSwitch
		// Ignore error: standalone ESXi 404s here, which is expected.
		_ = dv.Retrieve(s.ctx, []string{"DistributedVirtualSwitch"},
			[]string{"summary", "config"}, &dvss)
		for i := range dvss {
			c.emitDVS(&dvss[i], s)
		}
	}
}

func (c *NetworkCollector) emitHost(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" || h.Config == nil || h.Config.Network == nil {
		return
	}
	net := h.Config.Network

	s.ch <- prometheus.MustNewConstMetric(c.stdSwitches, prometheus.GaugeValue,
		float64(len(net.Vswitch)), name)
	s.ch <- prometheus.MustNewConstMetric(c.dvSwitches, prometheus.GaugeValue,
		float64(len(net.ProxySwitch)), name)

	// Standard vSwitches
	for _, sw := range net.Vswitch {
		s.ch <- prometheus.MustNewConstMetric(c.activePorts, prometheus.GaugeValue,
			float64(sw.NumPorts-sw.NumPortsAvailable), name, sw.Name)
		s.ch <- prometheus.MustNewConstMetric(c.mtu, prometheus.GaugeValue,
			float64(sw.Mtu), name, sw.Name)
		s.ch <- prometheus.MustNewConstMetric(c.uplinks, prometheus.GaugeValue,
			float64(len(sw.Pnic)), name, sw.Name)
		// Portgroups attached to this vSwitch. We match by Key so a
		// single Portgroup entry gets counted against exactly one
		// vSwitch even if names collide.
		pgCount := 0
		for _, pg := range net.Portgroup {
			if pg.Vswitch == sw.Key {
				pgCount++
				s.ch <- prometheus.MustNewConstMetric(c.vlanInfo, prometheus.GaugeValue,
					float64(pg.Spec.VlanId), name, sw.Name, pg.Spec.Name)
			}
		}
		s.ch <- prometheus.MustNewConstMetric(c.portgroups, prometheus.GaugeValue,
			float64(pgCount), name, sw.Name)
	}

	// Distributed vSwitch proxy on this host (uplink info only; central
	// portgroup lists come from emitDVS).
	for _, ps := range net.ProxySwitch {
		s.ch <- prometheus.MustNewConstMetric(c.uplinks, prometheus.GaugeValue,
			float64(len(ps.Pnic)), name, ps.DvsName)
		s.ch <- prometheus.MustNewConstMetric(c.mtu, prometheus.GaugeValue,
			float64(ps.Mtu), name, ps.DvsName)
	}
}

func (c *NetworkCollector) emitDVS(dvs *mo.DistributedVirtualSwitch, s *scrapeContext) {
	// The DVS lives cluster-wide, not per-host — we use "" as the host
	// label to keep cardinality low. Portgroup VLANs are best-effort:
	// govmomi surfaces them via DVPortgroup, not DVS.Config, so we skip
	// unless the type assertion below succeeds.
	if cfg, ok := dvs.Config.(*types.VMwareDVSConfigInfo); ok {
		s.ch <- prometheus.MustNewConstMetric(c.mtu, prometheus.GaugeValue,
			float64(cfg.MaxMtu), "", dvs.Name)
		s.ch <- prometheus.MustNewConstMetric(c.uplinks, prometheus.GaugeValue,
			float64(len(cfg.UplinkPortPolicy.(*types.DVSNameArrayUplinkPortPolicy).UplinkPortName)),
			"", dvs.Name)
	}
	s.ch <- prometheus.MustNewConstMetric(c.portgroups, prometheus.GaugeValue,
		float64(len(dvs.Portgroup)), "", dvs.Name)
}
