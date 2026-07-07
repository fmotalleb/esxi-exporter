package collector

import (
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"

	"github.com/fmotalleb/esxi-exporter/config"
)

// DatastoreCollector produces esxi_datastore_* metrics. Datastore health
// isn't just capacity — VMFS/NFS/vSAN each expose different perf counters
// (read/write latency, IOPS) that only surface via QueryPerf against the
// Datastore MoRef. Keeping the collector separate lets us gate the
// expensive per-datastore perf calls independently from the free/used
// snapshot most operators already scrape.

type DatastoreCollector struct {
	cfg *config.Config

	capacity    *prometheus.Desc
	free        *prometheus.Desc
	used        *prometheus.Desc
	provisioned *prometheus.Desc
	uncommitted *prometheus.Desc
	freePercent *prometheus.Desc
	accessible  *prometheus.Desc
	maintenance *prometheus.Desc
	dsType      *prometheus.Desc
	vmCount     *prometheus.Desc
	hostCount   *prometheus.Desc

	readLatency  *prometheus.Desc
	writeLatency *prometheus.Desc
	readBps      *prometheus.Desc
	writeBps     *prometheus.Desc
	iops         *prometheus.Desc
	congestion   *prometheus.Desc
}

func NewDatastoreCollector(cfg *config.Config) *DatastoreCollector {
	lbl := []string{"datastore"}
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &DatastoreCollector{
		cfg: cfg,

		capacity:    d("esxi_datastore_capacity_bytes", "Datastore capacity", lbl),
		free:        d("esxi_datastore_free_bytes", "Datastore free", lbl),
		used:        d("esxi_datastore_used_bytes", "Datastore used", lbl),
		provisioned: d("esxi_datastore_provisioned_bytes", "Provisioned (thin+thick sum)", lbl),
		uncommitted: d("esxi_datastore_uncommitted_bytes", "Uncommitted (thin overprovision)", lbl),
		freePercent: d("esxi_datastore_free_percent", "Free space percent", lbl),
		accessible:  d("esxi_datastore_accessible", "Datastore accessible", lbl),
		maintenance: d("esxi_datastore_maintenance_mode", "Datastore maintenance mode (1=yes)", lbl),
		dsType:      d("esxi_datastore_type_info", "Datastore type", []string{"datastore", "type"}),
		vmCount:     d("esxi_datastore_vm_count", "VMs on datastore", lbl),
		hostCount:   d("esxi_datastore_host_count", "Hosts mounting datastore", lbl),

		readLatency:  d("esxi_datastore_read_latency_ms", "Datastore read latency", lbl),
		writeLatency: d("esxi_datastore_write_latency_ms", "Datastore write latency", lbl),
		readBps:      d("esxi_datastore_read_bytes_per_second", "Datastore read throughput", lbl),
		writeBps:     d("esxi_datastore_write_bytes_per_second", "Datastore write throughput", lbl),
		iops:         d("esxi_datastore_iops", "Datastore total IOPS", lbl),
		congestion:   d("esxi_datastore_congestion", "Datastore congestion (SIOC)", lbl),
	}
}

func (c *DatastoreCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.capacity, c.free, c.used, c.provisioned, c.uncommitted,
		c.freePercent, c.accessible, c.maintenance, c.dsType,
		c.vmCount, c.hostCount,
		c.readLatency, c.writeLatency, c.readBps, c.writeBps,
		c.iops, c.congestion,
	} {
		ch <- dsc
	}
}

func (c *DatastoreCollector) Collect(s *scrapeContext) {
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"Datastore"}, true)
	if err != nil {
		log.Printf("datastore: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var dss []mo.Datastore
	if err := v.Retrieve(s.ctx, []string{"Datastore"},
		[]string{"summary", "info", "host", "vm"}, &dss); err != nil {
		log.Printf("datastore: retrieve failed: %v", err)
		return
	}

	for i := range dss {
		c.emit(&dss[i], s)
	}
}

func (c *DatastoreCollector) emit(ds *mo.Datastore, s *scrapeContext) {
	name := ds.Summary.Name
	sum := ds.Summary

	s.ch <- prometheus.MustNewConstMetric(c.capacity, prometheus.GaugeValue,
		float64(sum.Capacity), name)
	s.ch <- prometheus.MustNewConstMetric(c.free, prometheus.GaugeValue,
		float64(sum.FreeSpace), name)
	used := sum.Capacity - sum.FreeSpace
	s.ch <- prometheus.MustNewConstMetric(c.used, prometheus.GaugeValue,
		float64(used), name)
	if sum.Capacity > 0 {
		s.ch <- prometheus.MustNewConstMetric(c.freePercent, prometheus.GaugeValue,
			float64(sum.FreeSpace)/float64(sum.Capacity)*100, name)
	}
	s.ch <- prometheus.MustNewConstMetric(c.accessible, prometheus.GaugeValue,
		boolToFloat(sum.Accessible), name)
	// MaintenanceMode is a *string; empty means normal, "inMaintenance"
	// / "enteringMaintenance" mean not-normal.
	inMaint := sum.MaintenanceMode != "" && sum.MaintenanceMode != "normal"
	s.ch <- prometheus.MustNewConstMetric(c.maintenance, prometheus.GaugeValue,
		boolToFloat(inMaint), name)
	s.ch <- prometheus.MustNewConstMetric(c.dsType, prometheus.GaugeValue, 1, name, sum.Type)

	// Uncommitted only meaningful for thin-provisioned VMFS/NFS.
	if sum.Uncommitted != 0 {
		s.ch <- prometheus.MustNewConstMetric(c.uncommitted, prometheus.GaugeValue,
			float64(sum.Uncommitted), name)
		// Provisioned = used + uncommitted (industry-standard formula).
		s.ch <- prometheus.MustNewConstMetric(c.provisioned, prometheus.GaugeValue,
			float64(used+sum.Uncommitted), name)
	}

	s.ch <- prometheus.MustNewConstMetric(c.vmCount, prometheus.GaugeValue,
		float64(len(ds.Vm)), name)
	s.ch <- prometheus.MustNewConstMetric(c.hostCount, prometheus.GaugeValue,
		float64(len(ds.Host)), name)

	if !*c.cfg.Metrics.CollectDatastorePerf {
		return
	}
	// Datastore perf counters are NOT exposed at the 20s realtime
	// interval — vCenter returns "querySpec.interval" fault. Use the
	// 300s historical rollup, which is the shortest interval available
	// for Datastore/Cluster/ResourcePool entities. This means the values
	// lag ~5 min behind live activity; acceptable for capacity/latency
	// monitoring, and there's no lower-latency alternative in the API.
	//
	// It also means this collector is effectively vCenter-only: a
	// standalone ESXi host has no historical database and will return
	// empty results. The DatastoreCollector's capacity/space metrics
	// above still work standalone; only the perf block requires vCenter.
	m := queryPerf(s.ctx, s.perf, ds.Reference(), []string{
		"datastore.totalReadLatency.average",
		"datastore.totalWriteLatency.average",
		"datastore.read.average",
		"datastore.write.average",
		"datastore.numberReadAveraged.average",
		"datastore.numberWriteAveraged.average",
		"datastore.datastoreIops.average",
		"datastore.sizeNormalizedDatastoreLatency.average",
	}, "", IntervalHistoric)
	emit := func(desc *prometheus.Desc, key string, x float64) {
		if v, ok := m[key]; ok {
			s.ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue,
				v.Value*x, name)
		}
	}
	emit(c.readLatency, "datastore.totalReadLatency.average", 1)
	emit(c.writeLatency, "datastore.totalWriteLatency.average", 1)
	emit(c.readBps, "datastore.read.average", 1024)
	emit(c.writeBps, "datastore.write.average", 1024)

	// IOPS derived from read+write averaged (per counter).
	var iops float64
	if r, ok := m["datastore.numberReadAveraged.average"]; ok {
		iops += r.Value
	}
	if w, ok := m["datastore.numberWriteAveraged.average"]; ok {
		iops += w.Value
	}
	if iops > 0 {
		s.ch <- prometheus.MustNewConstMetric(c.iops, prometheus.GaugeValue, iops, name)
	}
	// SIOC congestion: only present when storage IO control is enabled.
	emit(c.congestion, "datastore.sizeNormalizedDatastoreLatency.average", 1)
}
