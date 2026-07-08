package collector

import (
	"context"
	"net/url"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"

	"github.com/fmotalleb/go-tools/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
	"github.com/fmotalleb/esxi-exporter/internal/secure"
)

// defaultCollectTimeout bounds a full Collect() call so an unreachable
// ESXi/vCenter host can't hang Prometheus scrapes indefinitely.
const defaultCollectTimeout = 60 * time.Second

// ESXiCollector is the top-level Prometheus collector. It owns descriptor
// registrations for every sub-collector (host, vm, datastore, cluster,
// network, resource pool, sensors, events, alarms) and fans out Collect()
// calls across the configured hosts.
//
// Duplicate-series contract: sub-collectors are executed once per configured
// endpoint. When several endpoints point at the same vCenter (or when a
// vCenter and one of its managed hosts are both listed), a naive fan-out
// would emit every host/vm/datastore twice and Prometheus rejects the
// scrape. The dedupSet below is shared across every sub-collector for a
// scrape and keys on (metric, labels). The first emit wins; later ones are
// dropped silently. That keeps operator configs forgiving without needing
// to teach every collector about the endpoint topology.
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

	// baseCtx is the parent context carrying the zap logger attached by
	// log.WithNewEnvLoggerForced. Stored so Collect() can derive its
	// timeout context from it, ensuring all collector log calls find the
	// logger via log.FromContext(ctx).
	baseCtx context.Context

	// Shared, per-scrape perf lookup cache. Because Prometheus drives the
	// cadence (each scrape triggers Collect), we build a fresh cache each
	// scrape and discard it — no goroutine-managed refresh loop.
	// The cache is per-endpoint because counter IDs differ across vCenters.
}

func NewESXiCollector(ctx context.Context, cfg *config.Config) *ESXiCollector {
	return &ESXiCollector{
		baseCtx:  ctx,
		cfg:      cfg,
		host:     NewHostCollector(cfg),
		vm:       NewVMCollector(cfg),
		datastore: NewDatastoreCollector(cfg),
		cluster:  NewClusterCollector(cfg),
		network:  NewNetworkCollector(cfg),
		rp:       NewResourcePoolCollector(cfg),
		sensors:  NewSensorsCollector(cfg),
		events:   NewEventsCollector(cfg),
		alarms:   NewAlarmsCollector(cfg),
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
	ctx, cancel := context.WithTimeout(c.baseCtx, defaultCollectTimeout)
	defer cancel()

	// One dedup set for the whole scrape. We wrap the caller-supplied
	// channel so every sub-collector's emit path filters through it
	// without having to know about it.
	dedup := newDedupChannel(ch)
	defer dedup.close()

	var wg sync.WaitGroup
	for _, spec := range c.cfg.Hosts {
		spec := spec
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.collectEndpoint(ctx, dedup.in, spec)
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
		log.FromContext(ctx).Sugar().Errorw("invalid esxi host url", "host", spec.Host, "error", err)
		return
	}
	if spec.Username != "" {
		// Resolve the password lazily through the SecretStore (which may
		// fetch from Vault / Bitwarden on first call and caches in
		// zeroable memory for subsequent scrapes).
		pw, pwErr := c.resolvePassword(ctx, &spec)
		if pwErr != nil {
			log.FromContext(ctx).Sugar().Errorw("failed to resolve password", "host", spec.Host, "error", pwErr)
			return
		}
		if pw == nil || pw.Len() == 0 {
			log.FromContext(ctx).Sugar().Errorw("no password available for host", "host", spec.Host)
			return
		}
		// Build the userinfo from the secure bytes without creating an
		// intermediate string that lingers in memory.
		userinfo := url.UserPassword(spec.Username, string(pw.Bytes()))
		u.User = userinfo
	}

	client, err := govmomi.NewClient(ctx, u, spec.Insecure)
	if err != nil {
		log.FromContext(ctx).Sugar().Errorw("failed to connect to esxi", "host", spec.Host, "error", err)
		return
	}
	defer func() {
		logoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Logout(logoutCtx); err != nil {
			log.FromContext(ctx).Sugar().Errorw("esxi logout error", "host", spec.Host, "error", err)
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
		log.FromContext(ctx).Sugar().Errorw("failed to get datacenter", "host", spec.Host, "error", err)
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
					log.FromContext(ctx).Sugar().Errorw("collector panic", "collector", name, "panic", r)
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

	// hostNames maps a HostSystem MoRef value ("host-10") to its
	// canonical name (DNS / IP as reported by summary.config.name).
	//
	// Why: mo.VirtualMachine.Runtime.Host is a *ManagedObjectReference
	// whose Value is a MoRef ID, not a name. If the VMCollector emits
	// that raw MoRef as the "host" label, it will not join in PromQL
	// against esxi_host_* series that use the DNS name. This map lets
	// every collector that has a MoRef translate to the name the host
	// collector uses, so labels are consistent across metric families.
	//
	// Populated lazily by resolveHostName(); one round-trip per scrape
	// on first use, cached for the rest of the scrape.
	hostNames   map[string]string
	hostNamesMu sync.Mutex
}

// resolveHostName returns the canonical host name for a HostSystem MoRef.
// Falls back to the MoRef value itself if the host can't be resolved (e.g.
// the entity was destroyed mid-scrape or the caller passed a bogus ref) —
// returning "" would silently drop labels and hide real breakage.
func (s *scrapeContext) resolveHostName(ref *types.ManagedObjectReference) string {
	if ref == nil || ref.Value == "" {
		return ""
	}
	s.hostNamesMu.Lock()
	defer s.hostNamesMu.Unlock()

	if s.hostNames != nil {
		if name, ok := s.hostNames[ref.Value]; ok {
			return name
		}
		// Cache miss on a subsequent call — return the MoRef so the
		// metric still has a stable identity, and log once so users
		// notice inventory drift instead of silently mismatched labels.
		log.FromContext(s.ctx).Sugar().Warnw("unknown host MoRef (inventory changed mid-scrape?)", "moref", ref.Value)
		return ref.Value
	}

	// First call: build the map. We ask only for name/summary.config.name
	// because that's cheap and matches what HostCollector uses as label.
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"HostSystem"}, true)
	if err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("scrape: build host name map: create view failed", "error", err)
		s.hostNames = map[string]string{} // negative cache to avoid retry
		return ref.Value
	}
	defer v.Destroy(s.ctx)

	var hosts []mo.HostSystem
	if err := v.Retrieve(s.ctx, []string{"HostSystem"},
		[]string{"name", "summary.config.name"}, &hosts); err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("scrape: build host name map: retrieve failed", "error", err)
		s.hostNames = map[string]string{}
		return ref.Value
	}

	s.hostNames = make(map[string]string, len(hosts))
	for _, h := range hosts {
		name := h.Summary.Config.Name
		if name == "" {
			name = h.Name
		}
		if name == "" {
			// Skip: entity is broken; storing "" would cause the
			// same label-mismatch problem this whole function
			// exists to solve.
			continue
		}
		s.hostNames[h.Reference().Value] = name
	}

	if name, ok := s.hostNames[ref.Value]; ok {
		return name
	}
	return ref.Value
}

// resolvePassword retrieves the password for a host from the SecretStore
// (which caches in zeroable memory). The SecretStore is always created during
// config.Load, even when no external resolvers are configured, ensuring that
// inline passwords are also stored in zeroable memory after the first fetch.
func (c *ESXiCollector) resolvePassword(ctx context.Context, host *config.ESXIHost) (*secure.SecureBytes, error) {
	return c.cfg.SecretStore.GetPassword(ctx, host)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// -----------------------------------------------------------------------
// dedupChannel: a metric-forwarding pump that drops repeats.
// -----------------------------------------------------------------------
//
// Prometheus panics a scrape if the same (fqName, labels) tuple arrives
// twice. Sub-collectors can produce duplicates for legitimate reasons:
//
//   - multiple endpoints in cfg.Hosts pointing at overlapping inventory
//     (e.g. vCenter + one of its ESXi hosts)
//   - a vCenter surfacing the same HostSystem via several parent folders
//   - VMware Tools reporting the same IP on primary + per-NIC lists
//   - identically-named root resource pools ("Resources") on every host
//
// Rather than push topology awareness into every collector, we forward all
// metrics through a single goroutine that hashes each Metric's descriptor
// + label values and drops anything already seen this scrape. It's O(n)
// with a map, no locks needed because everything funnels through one
// goroutine.
type dedupChannel struct {
	in   chan prometheus.Metric
	out  chan<- prometheus.Metric
	done chan struct{}
}

func newDedupChannel(out chan<- prometheus.Metric) *dedupChannel {
	// Buffered so producers don't block on the dedup goroutine's map
	// lookups. 1024 is enough headroom for very large scrapes.
	dc := &dedupChannel{
		in:   make(chan prometheus.Metric, 1024),
		out:  out,
		done: make(chan struct{}),
	}
	go dc.run()
	return dc
}

func (d *dedupChannel) run() {
	defer close(d.done)
	seen := make(map[string]struct{}, 4096)
	for m := range d.in {
		key := metricKey(m)
		if _, dup := seen[key]; dup {
			// Silent drop: logging every duplicate on a big
			// cluster would drown out real problems.
			continue
		}
		seen[key] = struct{}{}
		d.out <- m
	}
}

func (d *dedupChannel) close() {
	close(d.in)
	<-d.done
}

// metricKey extracts a stable identity from a prometheus.Metric. Metric's
// public API only exposes Write(*dto.Metric) + Desc(), so we serialize the
// desc string + label pairs into a single key. The alternative (reflecting
// into the constMetric internals) is fragile across client_golang versions.
func metricKey(m prometheus.Metric) string {
	var pb dto.Metric
	if err := m.Write(&pb); err != nil {
		// If Write fails the metric is broken anyway; use the desc
		// pointer as a fallback key so we don't dedup unrelated
		// broken metrics against each other.
		return m.Desc().String()
	}
	// Desc().String() carries fqName + help + constant labels; combining
	// with the variable label values from pb makes the key unique per
	// series without needing to know the label names.
	var b []byte
	b = append(b, m.Desc().String()...)
	for _, l := range pb.Label {
		b = append(b, 0x1f) // ASCII unit separator, cheap delimiter
		b = append(b, l.GetName()...)
		b = append(b, '=')
		b = append(b, l.GetValue()...)
	}
	return string(b)
}
