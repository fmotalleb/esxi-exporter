package collector

import (
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// AlarmsCollector reports the current triggered alarms across the vCenter
// inventory. Rather than exporting one series per alarm instance (which
// would balloon cardinality on transient alarms), we bucket by severity
// and expose the total. A separate info-style metric carries the entity/
// alarm name so operators can still see what's firing.

type AlarmsCollector struct {
	cfg *config.Config

	byStatus *prometheus.Desc
	byInfo   *prometheus.Desc
}

func NewAlarmsCollector(cfg *config.Config) *AlarmsCollector {
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &AlarmsCollector{
		cfg:      cfg,
		byStatus: d("esxi_alarm_count", "Triggered alarm count by status", []string{"status"}),
		byInfo:   d("esxi_alarm_info", "Triggered alarm (entity, name)", []string{"entity", "alarm", "status"}),
	}
}

func (c *AlarmsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.byStatus
	ch <- c.byInfo
}

func (c *AlarmsCollector) Collect(s *scrapeContext) {
	// Alarms are attached to managed entities via TriggeredAlarmState.
	// The root folder aggregates them, so one property fetch pulls
	// everything vCenter knows about.
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"ManagedEntity"}, true)
	if err != nil {
		log.Printf("alarms: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var ents []mo.ManagedEntity
	if err := v.Retrieve(s.ctx, []string{"ManagedEntity"},
		[]string{"name", "triggeredAlarmState"}, &ents); err != nil {
		log.Printf("alarms: retrieve failed: %v", err)
		return
	}

	counts := map[types.ManagedEntityStatus]int{}
	// Same TriggeredAlarmState can surface on multiple parent entities
	// (the entity itself + its folder chain when we walk ManagedEntity).
	// Dedup on the alarm's own Key rather than (entity, alarm) so we
	// don't double-count a single firing in the summary.
	seenAlarm := make(map[string]struct{})
	seenInfo := make(map[string]struct{})
	for _, e := range ents {
		for _, a := range e.TriggeredAlarmState {
			if _, dup := seenAlarm[a.Key]; !dup {
				counts[a.OverallStatus]++
				seenAlarm[a.Key] = struct{}{}
			}
			infoKey := e.Name + "|" + a.Alarm.Value + "|" + string(a.OverallStatus)
			if _, dup := seenInfo[infoKey]; dup {
				continue
			}
			seenInfo[infoKey] = struct{}{}
			// alarm.Alarm is a MoRef; resolving to a readable name
			// would need another fetch, so we ship the MoRef value.
			s.ch <- prometheus.MustNewConstMetric(c.byInfo, prometheus.GaugeValue, 1,
				e.Name, a.Alarm.Value, string(a.OverallStatus))
		}
	}
	// Always emit zero for green/yellow/red so alerting rules can rely on
	// the series existing even when nothing is firing.
	for _, status := range []types.ManagedEntityStatus{
		types.ManagedEntityStatusGreen,
		types.ManagedEntityStatusYellow,
		types.ManagedEntityStatusRed,
		types.ManagedEntityStatusGray,
	} {
		s.ch <- prometheus.MustNewConstMetric(c.byStatus, prometheus.GaugeValue,
			float64(counts[status]), string(status))
	}
}
