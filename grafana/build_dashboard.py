"""
Programmatic Grafana dashboard builder for the ESXi exporter.

Writing a 3000-line dashboard JSON by hand is a recipe for typos. We
generate it here so the layout stays consistent and adding a panel is a
few lines instead of a manual gridPos calculation.

Grid: Grafana uses a 24-column grid. Panels below use w=12 (half) or
w=8 (third) or w=24 (full) with h=8 as the default row height. The
build_row() helper auto-flows panels left-to-right, wrapping at 24.
"""

import json

DS = "${datasource}"

panel_id = 0
def next_id():
    global panel_id
    panel_id += 1
    return panel_id

def target(expr, legend="", instant=False, ref="A"):
    return {
        "datasource": {"type": "prometheus", "uid": DS},
        "expr": expr,
        "legendFormat": legend,
        "refId": ref,
        "instant": instant,
    }

def stat(title, expr, unit="none", color_mode="thresholds", thresholds=None, decimals=0, legend=""):
    thresholds = thresholds or {"mode": "absolute", "steps": [{"color": "green", "value": None}]}
    return {
        "id": next_id(),
        "type": "stat",
        "title": title,
        "datasource": {"type": "prometheus", "uid": DS},
        "targets": [target(expr, legend, instant=True)],
        "fieldConfig": {
            "defaults": {
                "unit": unit,
                "decimals": decimals,
                "thresholds": thresholds,
                "color": {"mode": color_mode},
            },
            "overrides": [],
        },
        "options": {
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            "textMode": "auto",
            "colorMode": color_mode,
            "graphMode": "area",
            "orientation": "auto",
        },
    }

def timeseries(title, targets, unit="none", stack=False, fill=10, legend_placement="bottom", legend_calcs=None, min_val=None, max_val=None, decimals=None):
    legend_calcs = legend_calcs or ["mean", "max"]
    field_defaults = {
        "unit": unit,
        "custom": {
            "drawStyle": "line",
            "lineInterpolation": "linear",
            "lineWidth": 1,
            "fillOpacity": fill,
            "spanNulls": True,
            "stacking": {"mode": "normal" if stack else "none", "group": "A"},
            "showPoints": "never",
        },
        "color": {"mode": "palette-classic"},
    }
    if min_val is not None:
        field_defaults["min"] = min_val
    if max_val is not None:
        field_defaults["max"] = max_val
    if decimals is not None:
        field_defaults["decimals"] = decimals
    return {
        "id": next_id(),
        "type": "timeseries",
        "title": title,
        "datasource": {"type": "prometheus", "uid": DS},
        "targets": [target(t["expr"], t["legend"], ref=chr(ord("A") + i)) for i, t in enumerate(targets)],
        "fieldConfig": {"defaults": field_defaults, "overrides": []},
        "options": {
            "legend": {"displayMode": "table", "placement": legend_placement, "calcs": legend_calcs, "showLegend": True},
            "tooltip": {"mode": "multi", "sort": "desc"},
        },
    }

def table(title, expr, transformations=None, overrides=None):
    return {
        "id": next_id(),
        "type": "table",
        "title": title,
        "datasource": {"type": "prometheus", "uid": DS},
        "targets": [target(expr, "", instant=True)],
        "transformations": transformations or [
            {"id": "organize", "options": {"excludeByName": {"Time": True, "__name__": True, "job": True, "instance": True}}},
        ],
        "fieldConfig": {"defaults": {"custom": {"align": "auto"}}, "overrides": overrides or []},
        "options": {"showHeader": True, "cellHeight": "sm"},
    }

def gauge(title, expr, unit="percent", thresholds_steps=None, max_val=100):
    thresholds_steps = thresholds_steps or [
        {"color": "green", "value": None},
        {"color": "yellow", "value": 70},
        {"color": "red", "value": 90},
    ]
    return {
        "id": next_id(),
        "type": "gauge",
        "title": title,
        "datasource": {"type": "prometheus", "uid": DS},
        "targets": [target(expr, "{{host}}", instant=True)],
        "fieldConfig": {
            "defaults": {
                "unit": unit,
                "max": max_val,
                "min": 0,
                "thresholds": {"mode": "absolute", "steps": thresholds_steps},
            },
            "overrides": [],
        },
        "options": {
            "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": True},
            "showThresholdLabels": False,
            "showThresholdMarkers": True,
        },
    }

# ---------------------------------------------------------------------------
# Layout helper — auto-flow panels into a row.
# ---------------------------------------------------------------------------

def row(title, collapsed, panels, y_start, default_h=8):
    """
    Given a list of (panel, w) tuples, position them left-to-right wrapping
    at 24 columns. Returns (row_panel, next_y).
    Row panel with collapsed=True hides children until user expands it.
    """
    row_panel = {
        "id": next_id(),
        "type": "row",
        "title": title,
        "collapsed": collapsed,
        "gridPos": {"h": 1, "w": 24, "x": 0, "y": y_start},
        "panels": [] if not collapsed else [],
    }
    x, y = 0, y_start + 1
    placed = []
    for p, w in panels:
        h = p.get("_h", default_h)
        if x + w > 24:
            x = 0
            y += default_h
        p["gridPos"] = {"h": h, "w": w, "x": x, "y": y}
        placed.append(p)
        x += w
    # In Grafana JSON, a collapsed row *contains* its children; an
    # expanded row leaves them as siblings. We always leave them as
    # siblings — collapsed just hides them in the UI initially and
    # Grafana handles re-nesting.
    if collapsed:
        row_panel["panels"] = placed
        return [row_panel], y + default_h
    return [row_panel] + placed, y + default_h

# ---------------------------------------------------------------------------
# Panels, grouped by section.
# ---------------------------------------------------------------------------

# Standard label filter fragment for host-scoped queries.
HF = 'host=~"$host"'
VF = 'vm=~"$vm",host=~"$host"'

# --- Overview (top of dashboard, always expanded) -----------------------
overview = [
    (stat("Hosts", "count(esxi_host_power_state)", "none"), 3),
    (stat("Hosts Powered On",
          "sum(esxi_host_power_state)", "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "red", "value": None},
              {"color": "green", "value": 1},
          ]}), 3),
    (stat("Hosts in Maintenance",
          "sum(esxi_host_maintenance_mode)", "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 1},
          ]}), 3),
    (stat("VMs Total", "count(esxi_vm_power_state)", "none"), 3),
    (stat("VMs Running", "sum(esxi_vm_power_state)", "none",
          color_mode="thresholds",
          thresholds={"mode": "absolute", "steps": [{"color": "green", "value": None}]}), 3),
    (stat("Datastores", "count(esxi_datastore_capacity_bytes)", "none"), 3),
    (stat("Red Alarms",
          'sum(esxi_alarm_count{status="red"}) OR on() vector(0)', "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "red", "value": 1},
          ]}), 3),
    (stat("Yellow Alarms",
          'sum(esxi_alarm_count{status="yellow"}) OR on() vector(0)', "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 1},
          ]}), 3),

    # Trend row under the stats.
    (timeseries("Cluster CPU utilization %",
                [{"expr": f"avg(esxi_host_cpu_usage_percent{{{HF}}})",
                  "legend": "avg"},
                 {"expr": f"max(esxi_host_cpu_usage_percent{{{HF}}})",
                  "legend": "max host"}],
                unit="percent", max_val=100), 12),
    (timeseries("Cluster memory usage",
                [{"expr": f"sum(esxi_host_memory_usage_bytes{{{HF}}})",
                  "legend": "used"},
                 {"expr": f"sum(esxi_host_memory_total_bytes{{{HF}}})",
                  "legend": "total"}],
                unit="bytes"), 12),
]

# --- Host CPU ------------------------------------------------------------
host_cpu = [
    (timeseries("Host CPU usage %",
                [{"expr": f"esxi_host_cpu_usage_percent{{{HF}}}",
                  "legend": "{{host}}"}], "percent", max_val=100), 12),
    (timeseries("Host CPU ready (ms) — contention indicator",
                [{"expr": f"esxi_host_cpu_ready_ms{{{HF}}}",
                  "legend": "{{host}}"}], "ms"), 12),
    (timeseries("Host CPU utilization % (PCPU-weighted)",
                [{"expr": f"esxi_host_cpu_utilization_percent{{{HF}}} / 100",
                  "legend": "{{host}}"}], "percent"), 8),
    (timeseries("Host CPU core utilization %",
                [{"expr": f"esxi_host_cpu_core_utilization_percent{{{HF}}} / 100",
                  "legend": "{{host}}"}], "percent"), 8),
    (timeseries("Host CPU wait/idle breakdown",
                [{"expr": f"esxi_host_cpu_wait_ms{{{HF}}}",
                  "legend": "{{host}} wait"},
                 {"expr": f"esxi_host_cpu_idle_ms{{{HF}}}",
                  "legend": "{{host}} idle"}], "ms"), 8),
]

# --- Host memory ---------------------------------------------------------
host_mem = [
    (timeseries("Host memory usage",
                [{"expr": f"esxi_host_memory_usage_bytes{{{HF}}}",
                  "legend": "{{host}} used"},
                 {"expr": f"esxi_host_memory_total_bytes{{{HF}}}",
                  "legend": "{{host}} total"}], "bytes"), 12),
    (timeseries("Host memory usage %",
                [{"expr": f"esxi_host_memory_usage_bytes{{{HF}}} / esxi_host_memory_total_bytes{{{HF}}} * 100",
                  "legend": "{{host}}"}], "percent", max_val=100), 12),
    (timeseries("Host active memory",
                [{"expr": f"esxi_host_memory_active_bytes{{{HF}}}",
                  "legend": "{{host}}"}], "bytes"), 8),
    (timeseries("Host ballooned / swapped / compressed",
                [{"expr": f"esxi_host_memory_balloon_bytes{{{HF}}}",
                  "legend": "{{host}} balloon"},
                 {"expr": f"esxi_host_memory_swapout_bytes{{{HF}}}",
                  "legend": "{{host}} swap-out"},
                 {"expr": f"esxi_host_memory_compressed_bytes{{{HF}}}",
                  "legend": "{{host}} compressed"}], "bytes"), 8),
    (stat("Memory pressure state (max)",
          f'max(esxi_host_memory_state{{{HF}}})',
          "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 1},
              {"color": "orange", "value": 2},
              {"color": "red", "value": 3},
          ]}), 8),
]

# --- Host disk -----------------------------------------------------------
host_disk = [
    (timeseries("Disk read latency (ms) — top 10 devices",
                [{"expr": f"topk(10, esxi_host_disk_read_latency_ms{{{HF}}})",
                  "legend": "{{host}} / {{device}}"}], "ms"), 12),
    (timeseries("Disk write latency (ms) — top 10 devices",
                [{"expr": f"topk(10, esxi_host_disk_write_latency_ms{{{HF}}})",
                  "legend": "{{host}} / {{device}}"}], "ms"), 12),
    (timeseries("Disk IOPS — read+write",
                [{"expr": f"sum by (host) (esxi_host_disk_read_iops{{{HF}}} + esxi_host_disk_write_iops{{{HF}}})",
                  "legend": "{{host}}"}], "iops"), 12),
    (timeseries("Disk throughput",
                [{"expr": f"sum by (host) (esxi_host_disk_read_bytes_per_second{{{HF}}})",
                  "legend": "{{host}} read"},
                 {"expr": f"sum by (host) (esxi_host_disk_write_bytes_per_second{{{HF}}})",
                  "legend": "{{host}} write"}], "Bps"), 12),
]

# --- Host network --------------------------------------------------------
host_net = [
    (timeseries("Network throughput per host (RX)",
                [{"expr": f"sum by (host) (esxi_host_network_bytes_rx_per_second{{{HF}}})",
                  "legend": "{{host}}"}], "Bps"), 12),
    (timeseries("Network throughput per host (TX)",
                [{"expr": f"sum by (host) (esxi_host_network_bytes_tx_per_second{{{HF}}})",
                  "legend": "{{host}}"}], "Bps"), 12),
    (timeseries("NIC link state — should stay at 1",
                [{"expr": f"esxi_host_nic_state{{{HF}}}",
                  "legend": "{{host}} / {{nic}}"}], "none", min_val=0, max_val=1), 8),
    (timeseries("NIC link speed",
                [{"expr": f"esxi_host_nic_link_speed_bits_per_second{{{HF}}}",
                  "legend": "{{host}} / {{nic}}"}], "bps"), 8),
    (timeseries("Dropped packets (RX+TX)",
                [{"expr": f"sum by (host, nic) (esxi_host_network_dropped_rx_total{{{HF}}} + esxi_host_network_dropped_tx_total{{{HF}}})",
                  "legend": "{{host}} / {{nic}}"}], "pps"), 8),
]

# --- Host runtime / hardware --------------------------------------------
host_runtime = [
    (table("Host inventory",
           f"esxi_host_cpu_model_info{{{HF}}}",
           overrides=[
               {"matcher": {"id": "byName", "options": "Value"},
                "properties": [{"id": "custom.hidden", "value": True}]},
           ]), 24),
    (stat("Hosts with reboot required",
          f'sum(esxi_host_reboot_required{{{HF}}})',
          "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "orange", "value": 1},
          ]}), 6),
    (stat("Hosts in lockdown",
          f'sum(esxi_host_lockdown_mode{{{HF}}} > 0)',
          "none"), 6),
    (stat("Hyperthreading active (hosts)",
          f'sum(esxi_host_hyperthreading_active{{{HF}}})',
          "none"), 6),
    (stat("Secure boot enabled (hosts)",
          f'sum(esxi_host_secure_boot_enabled{{{HF}}}) OR on() vector(0)',
          "none"), 6),
    (timeseries("Host uptime (days)",
                [{"expr": f"esxi_host_uptime_seconds{{{HF}}} / 86400",
                  "legend": "{{host}}"}], "d", decimals=1), 24),
]

# --- Hardware sensors ---------------------------------------------------
sensors = [
    (timeseries("CPU temperature (°C)",
                [{"expr": f"esxi_sensor_cpu_temperature_celsius{{{HF}}}",
                  "legend": "{{host}} / {{sensor}}"}], "celsius"), 12),
    (timeseries("DIMM temperature (°C)",
                [{"expr": f"esxi_sensor_dimm_temperature_celsius{{{HF}}}",
                  "legend": "{{host}} / {{sensor}}"}], "celsius"), 12),
    (timeseries("Fan speed (RPM)",
                [{"expr": f"esxi_sensor_fan_speed_rpm{{{HF}}}",
                  "legend": "{{host}} / {{sensor}}"}], "rotrpm"), 12),
    (timeseries("Voltage (V)",
                [{"expr": f"esxi_sensor_voltage_volts{{{HF}}}",
                  "legend": "{{host}} / {{sensor}}"}], "volt"), 12),
    (table("Non-green sensors",
           f'esxi_host_sensor_health{{{HF}}} > 0'), 24),
]

# --- VM CPU/mem ----------------------------------------------------------
vm_cpu_mem = [
    (timeseries("Top 10 VMs by CPU usage (MHz)",
                [{"expr": f"topk(10, esxi_vm_cpu_usage_percent{{{VF}}})",
                  "legend": "{{vm}}"}], "none"), 12),
    (timeseries("Top 10 VMs by CPU ready (contention)",
                [{"expr": f"topk(10, esxi_vm_cpu_ready_ms{{{VF}}})",
                  "legend": "{{vm}}"}], "ms"), 12),
    (timeseries("Top 10 VMs by CPU wait",
                [{"expr": f"topk(10, esxi_vm_cpu_wait_ms{{{VF}}})",
                  "legend": "{{vm}}"}], "ms"), 12),
    (timeseries("Top 10 VMs by CPU co-stop (SMP contention)",
                [{"expr": f"topk(10, esxi_vm_cpu_costop_ms{{{VF}}})",
                  "legend": "{{vm}}"}], "ms"), 12),
    (timeseries("Top 10 VMs by memory consumed",
                [{"expr": f"topk(10, esxi_vm_memory_consumed_bytes{{{VF}}})",
                  "legend": "{{vm}}"}], "bytes"), 12),
    (timeseries("Top 10 VMs by ballooned memory",
                [{"expr": f"topk(10, esxi_vm_memory_balloon_bytes{{{VF}}} > 0)",
                  "legend": "{{vm}}"}], "bytes"), 12),
    (timeseries("Top 10 VMs by swapped memory",
                [{"expr": f"topk(10, esxi_vm_memory_swapped_bytes{{{VF}}} > 0)",
                  "legend": "{{vm}}"}], "bytes"), 12),
    (timeseries("Top 10 VMs by active memory",
                [{"expr": f"topk(10, esxi_vm_memory_active_bytes{{{VF}}})",
                  "legend": "{{vm}}"}], "bytes"), 12),
]

# --- VM disk/network -----------------------------------------------------
vm_io = [
    (timeseries("Top 10 VMs by disk read IOPS",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_disk_read_iops{{{VF}}}))",
                  "legend": "{{vm}}"}], "iops"), 12),
    (timeseries("Top 10 VMs by disk write IOPS",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_disk_write_iops{{{VF}}}))",
                  "legend": "{{vm}}"}], "iops"), 12),
    (timeseries("Top 10 VMs by disk latency",
                [{"expr": f"topk(10, max by (vm) (esxi_vm_disk_latency_ms{{{VF}}}))",
                  "legend": "{{vm}}"}], "ms"), 12),
    (timeseries("Top 10 VMs by disk throughput (read+write)",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_disk_read_bytes_per_second{{{VF}}} + esxi_vm_disk_write_bytes_per_second{{{VF}}}))",
                  "legend": "{{vm}}"}], "Bps"), 12),
    (timeseries("Top 10 VMs by network RX",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_network_bytes_rx_per_second{{{VF}}}))",
                  "legend": "{{vm}}"}], "Bps"), 12),
    (timeseries("Top 10 VMs by network TX",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_network_bytes_tx_per_second{{{VF}}}))",
                  "legend": "{{vm}}"}], "Bps"), 12),
    (timeseries("Dropped packets per VM",
                [{"expr": f"topk(10, sum by (vm) (esxi_vm_network_dropped_packets_total{{{VF}}}) > 0)",
                  "legend": "{{vm}}"}], "pps"), 24),
]

# --- VM inventory / guest -----------------------------------------------
vm_inv = [
    (table("VMs with old/missing VMware Tools",
           f'esxi_vm_guest_tools_status{{{VF}}} < 1'), 12),
    (table("VMs with red heartbeat",
           f'esxi_vm_guest_heartbeat_status{{{VF}}} == 3'), 12),
    (table("Guest filesystems > 85% used",
           f'(esxi_vm_guest_filesystem_used_bytes{{{VF}}} / esxi_vm_guest_filesystem_capacity_bytes{{{VF}}}) * 100 > 85'), 24),
    (table("Snapshots older than 1 day (present count)",
           f'esxi_vm_snapshot_count{{{VF}}} > 0'), 12),
    (table("VM → IP addresses",
           f'esxi_vm_guest_ip_info{{{VF}}}'), 12),
]

# --- Datastores ---------------------------------------------------------
ds = [
    (timeseries("Datastore free % — alert threshold at 15%",
                [{"expr": "esxi_datastore_free_percent",
                  "legend": "{{datastore}}"}], "percent", max_val=100), 12),
    (timeseries("Datastore capacity vs used",
                [{"expr": "esxi_datastore_capacity_bytes",
                  "legend": "{{datastore}} capacity"},
                 {"expr": "esxi_datastore_used_bytes",
                  "legend": "{{datastore}} used"}], "bytes"), 12),
    (table("Datastore summary",
           "esxi_datastore_capacity_bytes",
           overrides=[
               {"matcher": {"id": "byName", "options": "Value"},
                "properties": [{"id": "unit", "value": "bytes"}, {"id": "displayName", "value": "Capacity"}]},
           ]), 24),
    (timeseries("Datastore read latency (5-min rollup)",
                [{"expr": "esxi_datastore_read_latency_ms",
                  "legend": "{{datastore}}"}], "ms"), 12),
    (timeseries("Datastore write latency (5-min rollup)",
                [{"expr": "esxi_datastore_write_latency_ms",
                  "legend": "{{datastore}}"}], "ms"), 12),
    (timeseries("Datastore IOPS",
                [{"expr": "esxi_datastore_iops",
                  "legend": "{{datastore}}"}], "iops"), 12),
    (timeseries("Datastore throughput",
                [{"expr": "esxi_datastore_read_bytes_per_second",
                  "legend": "{{datastore}} read"},
                 {"expr": "esxi_datastore_write_bytes_per_second",
                  "legend": "{{datastore}} write"}], "Bps"), 12),
]

# --- Cluster / RP -------------------------------------------------------
cluster = [
    (stat("Clusters", "count(esxi_cluster_host_count) OR on() vector(0)", "none"), 6),
    (stat("Clusters with HA on",
          'sum(esxi_cluster_ha_enabled) OR on() vector(0)', "none"), 6),
    (stat("Clusters with DRS on",
          'sum(esxi_cluster_drs_enabled) OR on() vector(0)', "none"), 6),
    (stat("Total cluster VMs",
          'sum(esxi_cluster_vm_count) OR on() vector(0)', "none"), 6),
    (timeseries("Cluster CPU utilization",
                [{"expr": "esxi_cluster_cpu_utilization_percent",
                  "legend": "{{cluster}}"}], "percent", max_val=100), 12),
    (timeseries("Cluster memory utilization",
                [{"expr": "esxi_cluster_memory_utilization_percent",
                  "legend": "{{cluster}}"}], "percent", max_val=100), 12),
    (table("Resource pool usage",
           "esxi_resource_pool_cpu_usage_mhz"), 24),
]

# --- Alarms / events ----------------------------------------------------
alarms = [
    (stat("Red alarms",
          'sum(esxi_alarm_count{status="red"}) OR on() vector(0)',
          "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "red", "value": 1},
          ]}), 6),
    (stat("Yellow alarms",
          'sum(esxi_alarm_count{status="yellow"}) OR on() vector(0)',
          "none",
          thresholds={"mode": "absolute", "steps": [
              {"color": "green", "value": None},
              {"color": "yellow", "value": 1},
          ]}), 6),
    (stat("Gray alarms",
          'sum(esxi_alarm_count{status="gray"}) OR on() vector(0)', "none"), 6),
    (stat("vCenter events (5m window)",
          'sum(esxi_vcenter_events_total) OR on() vector(0)', "none"), 6),
    (table("Currently firing alarms",
           'esxi_alarm_info',
           overrides=[
               {"matcher": {"id": "byName", "options": "status"},
                "properties": [{"id": "custom.cellOptions", "value": {"type": "color-background"}},
                               {"id": "mappings", "value": [
                                   {"type": "value", "options": {"red": {"color": "red"}, "yellow": {"color": "yellow"}, "green": {"color": "green"}}}
                               ]}]},
           ]), 24),
]

# ---------------------------------------------------------------------------
# Assemble.
# ---------------------------------------------------------------------------

all_panels = []
y = 0

sections = [
    ("Overview",          False, overview),
    ("Host — CPU",        False, host_cpu),
    ("Host — Memory",     True,  host_mem),
    ("Host — Disk",       True,  host_disk),
    ("Host — Network",    True,  host_net),
    ("Host — Runtime & Hardware", True, host_runtime),
    ("Hardware Sensors",  True,  sensors),
    ("VMs — CPU & Memory",False, vm_cpu_mem),
    ("VMs — Disk & Network", True, vm_io),
    ("VMs — Inventory & Guest", True, vm_inv),
    ("Datastores",        False, ds),
    ("Cluster & Resource Pools", True, cluster),
    ("Alarms & Events",   False, alarms),
]

for title, collapsed, panels in sections:
    row_panels, y = row(title, collapsed, panels, y)
    all_panels.extend(row_panels)

dashboard = {
    "annotations": {"list": [
        {"builtIn": 1, "datasource": {"type": "grafana", "uid": "-- Grafana --"},
         "enable": True, "hide": True, "iconColor": "rgba(0, 211, 255, 1)", "name": "Annotations & Alerts", "type": "dashboard"}
    ]},
    "editable": True,
    "fiscalYearStartMonth": 0,
    "graphTooltip": 1,
    "id": None,
    "links": [],
    "liveNow": False,
    "panels": all_panels,
    "refresh": "30s",
    "schemaVersion": 39,
    "tags": ["esxi", "vmware", "vsphere"],
    "templating": {
        "list": [
            {
                "current": {"selected": False, "text": "Prometheus", "value": "prometheus"},
                "hide": 0,
                "includeAll": False,
                "label": "Datasource",
                "multi": False,
                "name": "datasource",
                "options": [],
                "query": "prometheus",
                "queryValue": "",
                "refresh": 1,
                "regex": "",
                "skipUrlSync": False,
                "type": "datasource",
            },
            {
                "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
                "datasource": {"type": "prometheus", "uid": DS},
                "definition": "label_values(esxi_host_power_state, host)",
                "hide": 0,
                "includeAll": True,
                "label": "Host",
                "multi": True,
                "name": "host",
                "options": [],
                "query": {"query": "label_values(esxi_host_power_state, host)", "refId": "StandardVariableQuery"},
                "refresh": 2,
                "regex": "",
                "skipUrlSync": False,
                "sort": 1,
                "type": "query",
            },
            {
                "current": {"selected": True, "text": ["All"], "value": ["$__all"]},
                "datasource": {"type": "prometheus", "uid": DS},
                "definition": "label_values(esxi_vm_power_state{host=~\"$host\"}, vm)",
                "hide": 0,
                "includeAll": True,
                "label": "VM",
                "multi": True,
                "name": "vm",
                "options": [],
                "query": {"query": "label_values(esxi_vm_power_state{host=~\"$host\"}, vm)", "refId": "StandardVariableQuery"},
                "refresh": 2,
                "regex": "",
                "skipUrlSync": False,
                "sort": 1,
                "type": "query",
            },
        ]
    },
    "time": {"from": "now-1h", "to": "now"},
    "timepicker": {},
    "timezone": "browser",
    "title": "ESXi / vSphere",
    "uid": "esxi-exporter",
    "version": 1,
    "weekStart": "",
}

with open("./esxi-dashboard.json", "w") as f:
    json.dump(dashboard, f, indent=2)

print(f"Wrote {len(all_panels)} top-level panel entries")
print(f"Total unique panel IDs: {panel_id}")
