package collector

import (
	"context"
	"log"

	"github.com/vmware/govmomi/performance"
	"github.com/vmware/govmomi/vim25/types"
)

// queryPerf pulls the most recent realtime sample (20-second interval on
// ESXi) for the given counters against a single managed object. It returns
// a flat name->value map so callers don't have to walk EntityMetric slices.
//
// Design notes:
//   - We request MaxSample=1 because Prometheus is the scheduler here; we
//     only ever want the latest observation. Anything older is either stale
//     or a job for a TSDB, not the exporter.
//   - Realtime interval (IntervalId=20) works against standalone ESXi. For
//     vCenter-only aggregates you'd use the 300s historical interval, but
//     that's out of scope for per-entity metrics like cpu.ready.
//   - Missing counters are silently skipped: PerfCache.IDs already logs
//     unknowns once, and we don't want a scrape to fail because a specific
//     host doesn't expose, say, memory.compressed on very old hardware.
func queryPerf(
	ctx context.Context,
	perf *PerfCache,
	entity types.ManagedObjectReference,
	counters []string,
	instance string, // "" = aggregate, "*" = per-instance
) map[string]perfSample {
	ids := perf.IDs(ctx, counters)
	if len(ids) == 0 {
		return nil
	}

	metricIDs := make([]types.PerfMetricId, 0, len(ids))
	for _, id := range ids {
		metricIDs = append(metricIDs, types.PerfMetricId{
			CounterId: id,
			Instance:  instance,
		})
	}

	query := types.PerfQuerySpec{
		Entity:     entity,
		MaxSample:  1,
		MetricId:   metricIDs,
		IntervalId: 20, // realtime
	}

	mgr := performance.NewManager(perf.client)
	series, err := mgr.Query(ctx, []types.PerfQuerySpec{query})
	if err != nil {
		log.Printf("perf query failed for %s: %v", entity.Value, err)
		return nil
	}
	result, err := mgr.ToMetricSeries(ctx, series)
	if err != nil {
		log.Printf("perf series decode failed for %s: %v", entity.Value, err)
		return nil
	}

	out := make(map[string]perfSample)
	for _, em := range result {
		for _, v := range em.Value {
			if len(v.Value) == 0 {
				continue
			}
			// Latest sample = last element. QueryPerf returns
			// chronological order.
			out[v.Name] = perfSample{
				Value:    float64(v.Value[len(v.Value)-1]),
				Instance: v.Instance,
				Unit:     v.Unit,
			}
		}
	}
	return out
}

type perfSample struct {
	Value    float64
	Instance string
	Unit     string
}

// queryPerfInstances is the "*" flavor: returns a slice per counter, one
// entry per instance (e.g. per-vNIC, per-vDisk, per-LUN). Needed for metrics
// like disk.read.iops that are only meaningful when broken out by device.
func queryPerfInstances(
	ctx context.Context,
	perf *PerfCache,
	entity types.ManagedObjectReference,
	counters []string,
) map[string][]perfSample {
	ids := perf.IDs(ctx, counters)
	if len(ids) == 0 {
		return nil
	}

	metricIDs := make([]types.PerfMetricId, 0, len(ids))
	for _, id := range ids {
		metricIDs = append(metricIDs, types.PerfMetricId{
			CounterId: id,
			Instance:  "*",
		})
	}

	mgr := performance.NewManager(perf.client)
	series, err := mgr.Query(ctx, []types.PerfQuerySpec{{
		Entity:     entity,
		MaxSample:  1,
		MetricId:   metricIDs,
		IntervalId: 20,
	}})
	if err != nil {
		return nil
	}
	result, err := mgr.ToMetricSeries(ctx, series)
	if err != nil {
		return nil
	}

	out := make(map[string][]perfSample)
	for _, em := range result {
		for _, v := range em.Value {
			if len(v.Value) == 0 || v.Instance == "" {
				// Skip aggregate rows; caller wanted per-instance
				// breakdowns and the "" row is a duplicate sum.
				continue
			}
			out[v.Name] = append(out[v.Name], perfSample{
				Value:    float64(v.Value[len(v.Value)-1]),
				Instance: v.Instance,
				Unit:     v.Unit,
			})
		}
	}
	return out
}
