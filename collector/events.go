package collector

import (
	"time"

	"github.com/fmotalleb/go-tools/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/event"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// EventsCollector reports how many events vCenter has generated in the
// last window (default 5 minutes), bucketed by severity. Exporting the
// events themselves as metrics is a bad idea (unbounded label cardinality
// on message text), so we count them and let the operator hop into vCenter
// for the details.

type EventsCollector struct {
	cfg *config.Config

	total    *prometheus.Desc
	info     *prometheus.Desc
	warning  *prometheus.Desc
	error    *prometheus.Desc
	user     *prometheus.Desc
	system   *prometheus.Desc
}

func NewEventsCollector(cfg *config.Config) *EventsCollector {
	d := func(n, h string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, nil, nil)
	}
	return &EventsCollector{
		cfg:     cfg,
		total:   d("esxi_vcenter_events_total", "Events in window"),
		info:    d("esxi_vcenter_events_info", "Info events in window"),
		warning: d("esxi_vcenter_events_warning", "Warning events in window"),
		error:   d("esxi_vcenter_events_error", "Error events in window"),
		user:    d("esxi_vcenter_events_user", "User-generated events"),
		system:  d("esxi_vcenter_events_system", "System-generated events"),
	}
}

func (c *EventsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.total, c.info, c.warning, c.error, c.user, c.system,
	} {
		ch <- dsc
	}
}

func (c *EventsCollector) Collect(s *scrapeContext) {
	window := c.cfg.Metrics.EventsWindow
	if window == 0 {
		window = 5 * time.Minute
	}
	now := time.Now()
	begin := now.Add(-window)

	mgr := event.NewManager(s.client.Client)
	filter := types.EventFilterSpec{
		Time: &types.EventFilterSpecByTime{
			BeginTime: &begin,
			EndTime:   &now,
		},
	}
	// QueryEvents returns up to maxPage per call; we don't page here on
	// purpose. The metric is a count of "recent" events; if the site
	// generates >1000 in five minutes there's a bigger problem and we'd
	// rather stay bounded than blow out a scrape.
	events, err := mgr.QueryEvents(s.ctx, filter)
	if err != nil {
		log.FromContext(s.ctx).Sugar().Errorw("events: query failed", "error", err)
		return
	}

	var info, warn, errCnt, user, sys float64
	for _, e := range events {
		// Categorize by class name. vSphere doesn't expose severity on
		// the base struct, so we key off the concrete type: session
		// events count as user+info, everything else as system+info.
		// Warning/error would require a per-vendor mapping table —
		// left as a follow-up because it's hard to keep current.
		switch e.(type) {
		case *types.UserLoginSessionEvent, *types.UserLogoutSessionEvent:
			user++
			info++
		default:
			info++
			sys++
		}
	}
	total := info + warn + errCnt

	s.ch <- prometheus.MustNewConstMetric(c.total, prometheus.GaugeValue, total)
	s.ch <- prometheus.MustNewConstMetric(c.info, prometheus.GaugeValue, info)
	s.ch <- prometheus.MustNewConstMetric(c.warning, prometheus.GaugeValue, warn)
	s.ch <- prometheus.MustNewConstMetric(c.error, prometheus.GaugeValue, errCnt)
	s.ch <- prometheus.MustNewConstMetric(c.user, prometheus.GaugeValue, user)
	s.ch <- prometheus.MustNewConstMetric(c.system, prometheus.GaugeValue, sys)
}
