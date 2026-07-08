package collector

import (
	"context"
	"sync"

	"github.com/fmotalleb/go-tools/log"
	"github.com/vmware/govmomi/performance"
	"github.com/vmware/govmomi/vim25"
)

// PerfCache resolves counter names ("cpu.ready.summation") to numeric
// counter IDs. The mapping is stable for the lifetime of a vCenter/ESXi
// process, so we cache it for the whole session. Without this cache each
// scrape would spend a full round trip on CounterInfo just to translate
// ~50 names.
//
// One instance is created per endpoint session (see collector.go). It is
// intentionally NOT global: different vCenters can (and do) hand out
// different counter IDs, and mixing them corrupts every subsequent query.
type PerfCache struct {
	client *vim25.Client

	mu     sync.Mutex
	loaded bool
	// byName maps "group.name.rollup" -> counter ID.
	byName map[string]int32
	// warned tracks unknown counter names we've already logged, so a
	// misconfigured host doesn't spam the log every scrape.
	warned map[string]struct{}
}

func NewPerfCache(client *vim25.Client) *PerfCache {
	return &PerfCache{
		client: client,
		byName: make(map[string]int32),
		warned: make(map[string]struct{}),
	}
}

// load pulls the entire counter dictionary from the PerformanceManager. It's
// idempotent and lazy: callers just invoke IDs() and the first call triggers
// the fetch. On failure we mark the cache loaded anyway so we don't retry
// on every counter lookup within the same scrape.
func (p *PerfCache) load(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return
	}
	p.loaded = true

	mgr := performance.NewManager(p.client)
	counters, err := mgr.CounterInfoByName(ctx)
	if err != nil {
		log.FromContext(ctx).Sugar().Errorw("perf: CounterInfoByName failed", "error", err)
		return
	}
	for name, c := range counters {
		p.byName[name] = c.Key
	}
}

// IDs resolves a list of counter names to their numeric IDs, silently
// dropping anything unknown (after logging once). The returned slice
// preserves the order of the input where possible so callers that care
// about mapping response positions back to names can still do so.
func (p *PerfCache) IDs(ctx context.Context, names []string) []int32 {
	p.load(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]int32, 0, len(names))
	for _, n := range names {
		id, ok := p.byName[n]
		if !ok {
			if _, warned := p.warned[n]; !warned {
				log.FromContext(ctx).Sugar().Warnw("perf: counter not available on this endpoint", "counter", n)
				p.warned[n] = struct{}{}
			}
			continue
		}
		out = append(out, id)
	}
	return out
}
