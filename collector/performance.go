package collector

import (
	"context"

	"github.com/fmotalleb/go-tools/log"
	"github.com/vmware/govmomi/performance"
	"github.com/vmware/govmomi/vim25/types"
)

// Interval constants for QueryPerf. vSphere only accepts a fixed set of
// IntervalIds, and — crucially — which ones are valid depends on the
// entity type:
//
//   - HostSystem / VirtualMachine: 20 (realtime) works, plus all historical rollups.
//   - Datastore / ClusterComputeResource / ResourcePool: realtime is NOT
//     available. The API returns "A specified parameter was not correct:
//     querySpec.interval" if you pass 20. The shortest valid interval is
//     300s (past-day rollup) and it only exists against vCenter, not a
//     standalone ESXi host.
//
// Callers pick the right one. Getting this wrong isn't a soft failure —
// vCenter rejects the whole request and returns nothing.
const (
	IntervalRealtime = 20  // HostSystem, VirtualMachine only
	IntervalHistoric = 300 // Datastore, Cluster, ResourcePool (vCenter only)
)

// queryPerf pulls the most recent sample for the given counters against a
// single managed object. It returns a flat name->value map so callers
// don't have to walk EntityMetric slices.
//
// Design notes:
//   - We request MaxSample=1 because Prometheus is the scheduler here; we
//     only ever want the latest observation. Anything older is either stale
//     or a job for a TSDB, not the exporter.
//   - Missing counters are silently skipped: PerfCache.IDs already logs
//     unknowns once, and we don't want a scrape to fail because a specific
//     host doesn't expose, say, memory.compressed on very old hardware.
//   - StartTime is intentionally nil: with MaxSample=1 vCenter returns the
//     most recent completed interval for the requested IntervalId. Setting
//     StartTime on historical intervals is another way to trip the
//     "parameter not correct" fault.
func queryPerf(
	ctx context.Context,
	perf *PerfCache,
	entity types.ManagedObjectReference,
	counters []string,
	instance string, // "" = aggregate, "*" = per-instance
	interval int32,
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
		IntervalId: interval,
	}

	mgr := performance.NewManager(perf.client)
	series, err := mgr.Query(ctx, []types.PerfQuerySpec{query})
	if err != nil {
		log.FromContext(ctx).Sugar().Errorw("perf query failed", "entity", entity.Value, "error", err)
		return nil
	}
	result, err := mgr.ToMetricSeries(ctx, series)
	if err != nil {
		log.FromContext(ctx).Sugar().Errorw("perf series decode failed", "entity", entity.Value, "error", err)
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
	interval int32,
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
		IntervalId: interval,
	}})
	if err != nil {
		log.FromContext(ctx).Sugar().Errorw("perf query failed", "entity", entity.Value, "error", err)
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
