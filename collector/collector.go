package collector

import (
	"context"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/view"

	"github.com/fmotalleb/esxi-exporter/config"
)

// defaultCollectTimeout bounds a full Collect() call so an unreachable
// ESXi/vCenter host can't hang Prometheus scrapes indefinitely.
const defaultCollectTimeout = 60 * time.Second

// ESXiCollector is the top-level Prometheus collector. It owns descriptor
// registrations for every sub-collector (host, vm, datastore, cluster,
// network, resource pool, sensors, events, alarms) and fans out Collect()
// calls across the configured hosts.
type ESXiCollector struct {
	cfg *config.Config

	host      *HostCollector
	vm        *VMCollector
	datastore *DatastoreCollector
	cluster   *ClusterCollector
	network   *NetworkCollector
	rp        *ResourcePoolCollector
	sensors   *SensorsCollector
	events    *EventsCollector
	alarms    *AlarmsCollector

	// Shared, per-scrape perf lookup cache. Because Prometheus drives the
	// cadence (each scrape triggers Collect), we build a fresh cache each
	// scrape and discard it — no goroutine-managed refresh loop.
	// The cache is per-endpoint because counter IDs differ across vCenters.
}

func NewESXiCollector(cfg *config.Config) *ESXiCollector {
	return &ESXiCollector{
		cfg:       cfg,
		host:      NewHostCollector(cfg),
		vm:        NewVMCollector(cfg),
		datastore: NewDatastoreCollector(cfg),
		cluster:   NewClusterCollector(cfg),
		network:   NewNetworkCollector(cfg),
		rp:        NewResourcePoolCollector(cfg),
		sensors:   NewSensorsCollector(cfg),
		events:    NewEventsCollector(cfg),
		alarms:    NewAlarmsCollector(cfg),
	}
}

func (c *ESXiCollector) Describe(ch chan<- *prometheus.Desc) {
	c.host.Describe(ch)
	c.vm.Describe(ch)
	c.datastore.Describe(ch)
	c.cluster.Describe(ch)
	c.network.Describe(ch)
	c.rp.Describe(ch)
	c.sensors.Describe(ch)
	c.events.Describe(ch)
	c.alarms.Describe(ch)
}

func (c *ESXiCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCollectTimeout)
	defer cancel()

	var wg sync.WaitGroup
	for _, spec := range c.cfg.Hosts {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.collectEndpoint(ctx, ch, spec)
		}()
	}
	wg.Wait()
}

// collectEndpoint connects once per endpoint and runs every enabled
// sub-collector against that session. The govmomi session is expensive to
// build (Login roundtrip + optional TLS handshake), so we amortize it across
// all sub-collectors rather than reconnecting per file.
func (c *ESXiCollector) collectEndpoint(ctx context.Context, ch chan<- prometheus.Metric, spec config.ESXIHost) {
	u, err := url.Parse(spec.Host)
	if err != nil {
		log.Printf("invalid esxi host url: %v", err)
		return
	}
	if spec.Username != "" {
		u.User = url.UserPassword(spec.Username, spec.Password)
	}

	client, err := govmomi.NewClient(ctx, u, spec.Insecure)
	if err != nil {
		log.Printf("failed to connect to esxi %s: %v", spec.Host, err)
		return
	}
	defer func() {
		logoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Logout(logoutCtx); err != nil {
			log.Printf("esxi logout error: %v", err)
		}
	}()

	// One PerformanceManager cache per session; sub-collectors that need
	// perf counters share it so the CounterInfo dump happens at most once.
	perf := NewPerfCache(client.Client)

	// Datacenter is optional — some vCenter deployments have many; iterate
	// or fall back to DefaultDatacenter for standalone ESXi.
	finder := find.NewFinder(client.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		log.Printf("failed to get datacenter for %s: %v", spec.Host, err)
		return
	}
	finder.SetDatacenter(dc)

	// A single ContainerView per Managed Object type is cheaper than
	// repeated finder.*List calls when we later query properties in bulk.
	viewMgr := view.NewManager(client.Client)

	scrape := &scrapeContext{
		ctx:     ctx,
		ch:      ch,
		client:  client,
		finder:  finder,
		viewMgr: viewMgr,
		perf:    perf,
		spec:    spec,
	}

	// Run sub-collectors in parallel — they hit different managed objects
	// and share only read-only session state, so no locking is needed.
	var wg sync.WaitGroup
	run := func(name string, fn func(*scrapeContext)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("collector %s panic: %v", name, r)
				}
			}()
			fn(scrape)
		}()
	}

	run("host", c.host.Collect)
	run("vm", c.vm.Collect)
	if *c.cfg.Metrics.CollectDatastore {
		run("datastore", c.datastore.Collect)
	}
	if *c.cfg.Metrics.CollectCluster {
		run("cluster", c.cluster.Collect)
	}
	if *c.cfg.Metrics.CollectNetwork {
		run("network", c.network.Collect)
	}
	if *c.cfg.Metrics.CollectResourcePool {
		run("resourcepool", c.rp.Collect)
	}
	if *c.cfg.Metrics.CollectHardwareSensors {
		run("sensors", c.sensors.Collect)
	}
	if *c.cfg.Metrics.CollectEvents {
		run("events", c.events.Collect)
	}
	if *c.cfg.Metrics.CollectAlarms {
		run("alarms", c.alarms.Collect)
	}

	wg.Wait()
}

// scrapeContext bundles everything a sub-collector needs for a single scrape.
// Passing it around instead of stuffing the fields into every collector
// struct keeps the collectors stateless (safe to reuse across scrapes) and
// makes it obvious what a collector depends on.
type scrapeContext struct {
	ctx     context.Context
	ch      chan<- prometheus.Metric
	client  *govmomi.Client
	finder  *find.Finder
	viewMgr *view.Manager
	perf    *PerfCache
	spec    config.ESXIHost
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
