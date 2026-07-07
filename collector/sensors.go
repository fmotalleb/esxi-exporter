package collector

import (
	"log"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/fmotalleb/esxi-exporter/config"
)

// SensorsCollector consumes HostRuntimeInfo.HealthSystemRuntime — the same
// data the Web Client's "Hardware Health" tab shows. We categorize numeric
// sensors by their SensorType so operators can build one alert per class
// (fan speed, DIMM temp, PSU health, etc.) instead of one enormous metric
// with 500 labels.

type SensorsCollector struct {
	cfg *config.Config

	// Categorized sensors (labels stay stable across firmware upgrades so
	// dashboards don't break when a vendor renames "Fan1" -> "SYS_FAN1").
	cpuTemp   *prometheus.Desc
	dimmTemp  *prometheus.Desc
	fanSpeed  *prometheus.Desc
	voltage   *prometheus.Desc
	psuState  *prometheus.Desc
	battery   *prometheus.Desc
	raidState *prometheus.Desc
	smartOK   *prometheus.Desc

	// Catch-all: raw sensor health with sensor name label.
	sensorHealth *prometheus.Desc
}

func NewSensorsCollector(cfg *config.Config) *SensorsCollector {
	lbl := []string{"host", "sensor"}
	d := func(n, h string, l []string) *prometheus.Desc {
		return prometheus.NewDesc(n, h, l, nil)
	}
	return &SensorsCollector{
		cfg:          cfg,
		cpuTemp:      d("esxi_sensor_cpu_temperature_celsius", "CPU temperature", lbl),
		dimmTemp:     d("esxi_sensor_dimm_temperature_celsius", "DIMM temperature", lbl),
		fanSpeed:     d("esxi_sensor_fan_speed_rpm", "Fan speed RPM", lbl),
		voltage:      d("esxi_sensor_voltage_volts", "Voltage reading", lbl),
		psuState:     d("esxi_sensor_psu_state", "PSU state (0=ok,1=warn,2=crit)", lbl),
		battery:      d("esxi_sensor_battery_state", "Battery/CMOS state", lbl),
		raidState:    d("esxi_sensor_raid_state", "RAID controller state", lbl),
		smartOK:      d("esxi_sensor_disk_smart_ok", "Disk SMART OK", lbl),
		sensorHealth: d("esxi_host_sensor_health", "Generic sensor health (0=green,1=yellow,2=red,-1=unknown)", lbl),
	}
}

func (c *SensorsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, dsc := range []*prometheus.Desc{
		c.cpuTemp, c.dimmTemp, c.fanSpeed, c.voltage,
		c.psuState, c.battery, c.raidState, c.smartOK, c.sensorHealth,
	} {
		ch <- dsc
	}
}

func (c *SensorsCollector) Collect(s *scrapeContext) {
	v, err := s.viewMgr.CreateContainerView(s.ctx, s.client.ServiceContent.RootFolder,
		[]string{"HostSystem"}, true)
	if err != nil {
		log.Printf("sensors: create view failed: %v", err)
		return
	}
	defer v.Destroy(s.ctx)

	var hosts []mo.HostSystem
	if err := v.Retrieve(s.ctx, []string{"HostSystem"},
		[]string{"name", "summary.config.name", "runtime.healthSystemRuntime"}, &hosts); err != nil {
		log.Printf("sensors: retrieve failed: %v", err)
		return
	}
	for i := range hosts {
		c.emit(&hosts[i], s)
	}
}

func (c *SensorsCollector) emit(h *mo.HostSystem, s *scrapeContext) {
	name := hostName(h)
	if name == "" {
		return
	}
	hsr := h.Runtime.HealthSystemRuntime
	if hsr == nil || hsr.SystemHealthInfo == nil {
		return
	}
	for _, sensor := range hsr.SystemHealthInfo.NumericSensorInfo {
		c.emitSensor(name, sensor, s)
	}
}

func (c *SensorsCollector) emitSensor(host string, sensor types.HostNumericSensorInfo, s *scrapeContext) {
	// Health label first — always safe to emit.
	health := -1.0
	if sensor.HealthState != nil {
		switch sensor.HealthState.GetElementDescription().Key {
		case "green":
			health = 0
		case "yellow":
			health = 1
		case "red":
			health = 2
		}
	}
	s.ch <- prometheus.MustNewConstMetric(c.sensorHealth, prometheus.GaugeValue,
		health, host, sensor.Name)

	// Numeric reading requires scaling: HostNumericSensorInfo reports the
	// value pre-scaled ("BaseUnits × 10^UnitModifier"). SensorType tells
	// us how to interpret it.
	value := float64(sensor.CurrentReading)
	for i := int32(0); i < sensor.UnitModifier; i++ {
		value *= 10
	}
	for i := int32(0); i > sensor.UnitModifier; i-- {
		value /= 10
	}

	switch sensor.SensorType {
	case "temperature":
		// Distinguish CPU vs DIMM by name — vendors are consistent
		// enough that a substring match beats maintaining a taxonomy.
		if containsAny(sensor.Name, "CPU", "Proc") {
			s.ch <- prometheus.MustNewConstMetric(c.cpuTemp, prometheus.GaugeValue,
				value, host, sensor.Name)
		} else if containsAny(sensor.Name, "DIMM", "Memory", "MEM") {
			s.ch <- prometheus.MustNewConstMetric(c.dimmTemp, prometheus.GaugeValue,
				value, host, sensor.Name)
		}
	case "fan":
		s.ch <- prometheus.MustNewConstMetric(c.fanSpeed, prometheus.GaugeValue,
			value, host, sensor.Name)
	case "voltage":
		s.ch <- prometheus.MustNewConstMetric(c.voltage, prometheus.GaugeValue,
			value, host, sensor.Name)
	case "power":
		// Power status sensors map to psuState based on health color.
		s.ch <- prometheus.MustNewConstMetric(c.psuState, prometheus.GaugeValue,
			health, host, sensor.Name)
	case "battery":
		s.ch <- prometheus.MustNewConstMetric(c.battery, prometheus.GaugeValue,
			health, host, sensor.Name)
	case "storage":
		// Vendors expose RAID controller + SMART under storage; split
		// so SMART OK stays as boolean and RAID gets a state code.
		if containsAny(sensor.Name, "SMART") {
			s.ch <- prometheus.MustNewConstMetric(c.smartOK, prometheus.GaugeValue,
				boolToFloat(health == 0), host, sensor.Name)
		} else {
			s.ch <- prometheus.MustNewConstMetric(c.raidState, prometheus.GaugeValue,
				health, host, sensor.Name)
		}
	}
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		for i := 0; i+len(n) <= len(hay); i++ {
			if hay[i:i+len(n)] == n {
				return true
			}
		}
	}
	return false
}
