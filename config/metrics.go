package config

import "time"

// MetricsConfig is the granular toggle set consumed by the collector
// package. It is intentionally verbose: every heavy sub-collector or perf
// group has its own bool so operators can disable exactly what's noisy or
// expensive on their environment without giving up unrelated data.
//
// Add these fields to your existing config.Config.Metrics struct — the
// collector code assumes they exist. Sensible defaults are documented on
// each field; wire them through your mapstructure loader as you prefer.
type MetricsConfig struct {
	// Legacy toggles (kept for backward compatibility with existing
	// deployments). Default: true.
	CollectPerformance     *bool `mapstructure:"collect_performance" default:"true"`
	CollectHardwareSensors *bool `mapstructure:"collect_hardware_sensors" default:"true"`
	CollectGuestInfo       *bool `mapstructure:"collect_guest_info" default:"true"`
	CollectVMDiskDetails   *bool `mapstructure:"collect_vm_disk_details" default:"true"`

	// Host granular toggles. Default: true.
	CollectHostRuntime  *bool `mapstructure:"collect_host_runtime" default:"true"`
	CollectHostCPUPerf  *bool `mapstructure:"collect_host_cpu_perf" default:"true"`
	CollectHostMemPerf  *bool `mapstructure:"collect_host_mem_perf" default:"true"`
	CollectHostDiskPerf *bool `mapstructure:"collect_host_disk_perf" default:"true"`
	CollectHostNetPerf  *bool `mapstructure:"collect_host_net_perf" default:"true"`

	// VM granular toggles. Default: true, except CollectVMConfig which
	// can add O(vms) property fetches and is default false on very
	// large fleets.
	CollectVMConfig   *bool `mapstructure:"collect_vm_config" default:"true"`
	CollectVMCPUPerf  *bool `mapstructure:"collect_vm_cpu_perf" default:"true"`
	CollectVMMemPerf  *bool `mapstructure:"collect_vm_mem_perf" default:"true"`
	CollectVMDiskPerf *bool `mapstructure:"collect_vm_disk_perf" default:"true"`
	CollectVMNetPerf  *bool `mapstructure:"collect_vm_net_perf" default:"true"`

	// Sub-collector master switches. Default: true for datastore/network,
	// false for the vCenter-only collectors (cluster/resource_pool/events/
	// alarms) so a bare ESXi endpoint doesn't log warnings on every scrape.
	CollectDatastore     *bool `mapstructure:"collect_datastore" default:"true"`
	CollectDatastorePerf *bool `mapstructure:"collect_datastore_perf" default:"true"`
	CollectCluster       *bool `mapstructure:"collect_cluster" default:"true"`
	CollectNetwork       *bool `mapstructure:"collect_network" default:"true"`
	CollectResourcePool  *bool `mapstructure:"collect_resource_pool" default:"true"`
	CollectEvents        *bool `mapstructure:"collect_events" default:"true"`
	CollectAlarms        *bool `mapstructure:"collect_alarms" default:"true"`

	// EventsWindow controls how far back the events collector looks per
	// scrape. Keep it near the scrape interval to avoid double-counting.
	EventsWindow time.Duration `mapstructure:"events_window" default:"60s"`
}
