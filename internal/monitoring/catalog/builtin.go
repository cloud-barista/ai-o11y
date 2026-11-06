package catalog

import (
	"fmt"
	"slices"
	"strings"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// Collectors an expression can come from.
const (
	// CollectorHostmetrics is the OpenTelemetry hostmetrics receiver, sent over
	// OTLP. Metric names follow the Prometheus OTLP translation with unit and
	// type suffixes (system.cpu.time in seconds, a monotonic sum, becomes
	// system_cpu_time_seconds_total).
	CollectorHostmetrics = "hostmetrics"
	// CollectorGPU is the GPU receiver built into the collector.
	CollectorGPU = "gpu"
)

// Capabilities a row can require.
const (
	RequiresLinux       = "os:linux"
	RequiresPhysicalGPU = "physicalGpuMetrics"
)

// Builtin returns the catalog rows ai-o11y ships with. Common fields carry
// the OpenTelemetry semantic conventions' names (v1.45.0) and meaning and say
// how the hostmetrics receiver provides them; hostmetrics metrics with no
// counterpart there are fields of hostmetrics' own.
func Builtin() []monitoring.MetricRow {
	var rows []monitoring.MetricRow
	rows = append(rows, cpuRows()...)
	rows = append(rows, memoryRows()...)
	rows = append(rows, pagingRows()...)
	rows = append(rows, diskRows()...)
	rows = append(rows, filesystemRows()...)
	rows = append(rows, networkRows()...)
	rows = append(rows, systemRows()...)
	rows = append(rows, processRows()...)
	rows = append(rows, gpuRows()...)
	return rows
}

func hm(expr string) map[string]string { return map[string]string{CollectorHostmetrics: expr} }

// hmReporting is 0 for every target hostmetrics is reporting from: the CPU
// scraper sends CPU time every scrape. It fills the fields whose hostmetrics
// series is left out when the value is zero.
var hmReporting = map[string]string{
	CollectorHostmetrics: `0 * group by (entity_id) (system_cpu_time_seconds_total{$sel})`,
}

// relabel copies the hostmetrics label from into the attribute label of attr,
// renaming the values whose OpenTelemetry name differs (rename maps the
// hostmetrics value to it).
func relabel(expr, from, attr string, rename map[string]string) string {
	to := monitoring.AttrLabel(attr)
	out := fmt.Sprintf(`label_replace(%s, %q, "$1", %q, "(.*)")`, expr, to, from)
	keys := make([]string, 0, len(rename))
	for k := range rename {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = fmt.Sprintf(`label_replace(%s, %q, %q, %q, %q)`, out, to, rename[k], from, k)
	}
	return out
}

// by joins entity_id with the grouping labels.
func by(labels ...string) string {
	return strings.Join(append([]string{"entity_id"}, labels...), ", ")
}

func cpuRows() []monitoring.MetricRow {
	const time = `rate(system_cpu_time_seconds_total{$sel}[$range])`
	total := `sum by (entity_id) (` + time + `)`
	idle := `sum by (entity_id) (rate(system_cpu_time_seconds_total{state="idle",$sel}[$range]))`
	mode := monitoring.AttrLabel("cpu.mode")
	return []monitoring.MetricRow{
		{
			Name: "system.cpu.utilization", Area: "cpu", Unit: "1", Kind: monitoring.KindRate,
			Description: "Share of CPU time in each cpu.mode. Without one, the share not idle.",
			Exprs:       hm("1 - (" + idle + " / " + total + ")"),
			Attribute: &monitoring.Attribute{Name: "cpu.mode",
				Values: []string{"user", "system", "nice", "idle", "iowait", "interrupt", "steal", "softirq"}},
			SplitExprs: hm(`sum by (` + by(mode) + `) (` + relabel(time, "state", "cpu.mode", map[string]string{"wait": "iowait"}) + `)` +
				` / ignoring (` + mode + `) group_left ` + total),
		},
		{
			Name: "system.cpu.logical.count", Area: "cpu", Unit: "{cpu}", Kind: monitoring.KindGauge,
			Description: "Logical CPUs.",
			Exprs:       hm(`max by (entity_id) (system_cpu_logical_count{$sel})`),
		},
		{
			Name: "system.cpu.physical.count", Area: "cpu", Unit: "{cpu}", Kind: monitoring.KindGauge,
			Description: "Physical CPUs.",
			Exprs:       hm(`max by (entity_id) (system_cpu_physical_count{$sel})`),
			Enables:     []string{"system.cpu.physical.count"},
		},
	}
}

func memoryRows() []monitoring.MetricRow {
	state := monitoring.AttrLabel("system.memory.state")
	split := func(metric string) string {
		return `sum by (` + by(state) + `) (` + relabel(metric+`{$sel}`, "state", "system.memory.state",
			map[string]string{"buffered": "buffers"}) + `)`
	}
	attr := &monitoring.Attribute{Name: "system.memory.state",
		Values: []string{"used", "free", "buffers", "cached", "inactive", "slab_reclaimable", "slab_unreclaimable"}}
	return []monitoring.MetricRow{
		{
			Name: "system.memory.usage", Area: "memory", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Memory in each system.memory.state. Without one, memory used.",
			Exprs:       hm(`sum by (entity_id) (system_memory_usage_bytes{state="used",$sel})`),
			Attribute:   attr, SplitExprs: hm(split("system_memory_usage_bytes")),
		},
		{
			Name: "system.memory.limit", Area: "memory", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Total memory.",
			Exprs:       hm(`sum by (entity_id) (system_memory_limit_bytes{$sel})`),
			Enables:     []string{"system.memory.limit"},
		},
		{
			Name: "system.memory.utilization", Area: "memory", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of memory in each system.memory.state. Without one, the share used.",
			Exprs:       hm(`sum by (entity_id) (system_memory_utilization_ratio{state="used",$sel})`),
			Attribute:   attr, SplitExprs: hm(split("system_memory_utilization_ratio")),
			Enables: []string{"system.memory.utilization"},
		},
		{
			Name: "system.memory.linux.available", Area: "memory", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Memory available for new work (MemAvailable).",
			Exprs:       hm(`sum by (entity_id) (system_linux_memory_available_bytes{$sel})`),
			Requires:    []string{RequiresLinux},
			Enables:     []string{"system.linux.memory.available"},
		},
		{
			Name: "system.memory.linux.shared", Area: "memory", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Shared memory (Shmem).",
			Exprs:       hm(`sum by (entity_id) (system_memory_linux_shared_bytes{$sel})`),
			Requires:    []string{RequiresLinux},
			Enables:     []string{"system.memory.linux.shared"},
		},
	}
}

func pagingRows() []monitoring.MetricRow {
	const usage = "system_paging_usage_bytes"
	state := monitoring.AttrLabel("system.paging.state")
	total := `sum by (entity_id) (` + usage + `{$sel})`
	byState := `sum by (` + by(state) + `) (` + relabel(usage+`{$sel}`, "state", "system.paging.state", nil) + `)`
	attr := &monitoring.Attribute{Name: "system.paging.state", Values: []string{"used", "free", "cached"}}
	faultType := monitoring.AttrLabel("system.paging.fault.type")
	faults := `rate(system_paging_faults_total{$sel}[$range])`
	// hostmetrics sends no paging usage on a host without swap.
	return []monitoring.MetricRow{
		{
			Name: "system.paging.usage", Area: "paging", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Swap space in each system.paging.state. Without one, swap used.",
			Exprs:       hm(`sum by (entity_id) (` + usage + `{state="used",$sel})`),
			Attribute:   attr, SplitExprs: hm(byState), ZeroFill: hmReporting,
		},
		{
			Name: "system.paging.utilization", Area: "paging", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of swap space in each system.paging.state. Without one, the share used.",
			Exprs:       hm(`sum by (entity_id) (` + usage + `{state="used",$sel}) / ` + total),
			Attribute:   attr, SplitExprs: hm(byState + ` / ignoring (` + state + `) group_left ` + total),
			ZeroFill: hmReporting,
		},
		{
			Name: "system.paging.faults", Area: "paging", Unit: "{fault}/s", Kind: monitoring.KindRate,
			Description: "Page faults of each system.paging.fault.type. Without one, all page faults.",
			Exprs:       hm(`sum by (entity_id) (` + faults + `)`),
			Attribute:   &monitoring.Attribute{Name: "system.paging.fault.type", Values: []string{"major", "minor"}},
			SplitExprs:  hm(`sum by (` + by(faultType) + `) (` + relabel(faults, "type", "system.paging.fault.type", nil) + `)`),
		},
		{
			Name: "system.paging.operations", Area: "paging", Unit: "{operation}/s", Kind: monitoring.KindRate,
			Description: "Paging operations.",
			Gap:         "hostmetrics reports the bytes swapped as its major paging operations on Linux, not a count of operations",
		},
	}
}

// directionSplit is a rate field split by a direction attribute that
// hostmetrics carries in its direction label with the same values.
func directionSplit(name, area, unit, desc, metric, dim, dimName, attr string, values []string) monitoring.MetricRow {
	r := fmt.Sprintf(`rate(%s{$sel}[$range])`, metric)
	return monitoring.MetricRow{
		Name: name, Area: area, Unit: unit, Kind: monitoring.KindRate, Description: desc,
		Exprs:     hm(`sum by (` + by(dim) + `) (` + r + `)`),
		Attribute: &monitoring.Attribute{Name: attr, Values: values},
		SplitExprs: hm(`sum by (` + by(dim, monitoring.AttrLabel(attr)) + `) (` +
			relabel(r, "direction", attr, nil) + `)`),
		Dimension: dim, DimensionName: dimName,
	}
}

func diskRows() []monitoring.MetricRow {
	rw := []string{"read", "write"}
	const dev, devName, dir = "device", "system.device.name", "disk.io.direction"
	return []monitoring.MetricRow{
		directionSplit("system.disk.io", "disk", "By/s", "Bytes read and written. Without disk.io.direction, both.",
			"system_disk_io_bytes_total", dev, devName, dir, rw),
		directionSplit("system.disk.operations", "disk", "{operation}/s", "Completed reads and writes. Without disk.io.direction, both.",
			"system_disk_operations_total", dev, devName, dir, rw),
		directionSplit("system.disk.operation_time", "disk", "s/s", "Time spent in reads and writes. Without disk.io.direction, both.",
			"system_disk_operation_time_seconds_total", dev, devName, dir, rw),
		directionSplit("system.disk.merged", "disk", "{operation}/s", "Merged reads and writes. Without disk.io.direction, both.",
			"system_disk_merged_total", dev, devName, dir, rw),
		{
			Name: "system.disk.io_time", Area: "disk", Unit: "s/s", Kind: monitoring.KindRate,
			Description: "Time the device was busy, per second: its busy share.",
			Exprs:       hm(`sum by (entity_id, device) (rate(system_disk_io_time_seconds_total{$sel}[$range]))`),
			Dimension:   dev, DimensionName: devName,
		},
		{
			Name: "system.disk.pending_operations", Schema: CollectorHostmetrics, Area: "disk", Unit: "{operation}",
			Kind: monitoring.KindGauge, Description: "I/O operations in progress.",
			Exprs:     hm(`sum by (entity_id, device) (system_disk_pending_operations{$sel})`),
			Dimension: dev, DimensionName: "device",
		},
	}
}

func filesystemRows() []monitoring.MetricRow {
	const usage, mnt, mntName = "system_filesystem_usage_bytes", "mountpoint", "system.filesystem.mountpoint"
	state := monitoring.AttrLabel("system.filesystem.state")
	limit := `sum by (entity_id, mountpoint) (` + usage + `{$sel})`
	byState := `sum by (` + by(mnt, state) + `) (` + relabel(usage+`{$sel}`, "state", "system.filesystem.state", nil) + `)`
	used := `sum by (entity_id, mountpoint) (` + usage + `{state="used",$sel})`
	attr := &monitoring.Attribute{Name: "system.filesystem.state", Values: []string{"used", "free", "reserved"}}
	return []monitoring.MetricRow{
		{
			Name: "system.filesystem.usage", Area: "filesystem", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Space in each system.filesystem.state. Without one, space used.",
			Exprs:       hm(used), Attribute: attr, SplitExprs: hm(byState),
			Dimension: mnt, DimensionName: mntName,
		},
		{
			Name: "system.filesystem.limit", Area: "filesystem", Unit: "By", Kind: monitoring.KindGauge,
			Description: "File system size, reserved space included.",
			Exprs:       hm(limit), Dimension: mnt, DimensionName: mntName,
		},
		{
			Name: "system.filesystem.utilization", Area: "filesystem", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of the file system size in each system.filesystem.state. Without one, the share used.",
			Exprs:       hm(used + " / " + limit), Attribute: attr,
			SplitExprs: hm(byState + ` / ignoring (` + state + `) group_left ` + limit),
			Dimension:  mnt, DimensionName: mntName,
		},
		{
			Name: "system.filesystem.inodes.usage", Schema: CollectorHostmetrics, Area: "filesystem", Unit: "{inode}",
			Kind: monitoring.KindGauge, Description: "Inodes in each state. Without one, inodes used.",
			Exprs:      hm(`sum by (entity_id, mountpoint) (system_filesystem_inodes_usage{state="used",$sel})`),
			Attribute:  &monitoring.Attribute{Name: "state", Values: []string{"used", "free"}},
			SplitExprs: hm(`sum by (entity_id, mountpoint, state) (system_filesystem_inodes_usage{$sel})`),
			Dimension:  mnt, DimensionName: mnt, Requires: []string{RequiresLinux},
		},
	}
}

func networkRows() []monitoring.MetricRow {
	tr := []string{"transmit", "receive"}
	const dev, ifName, dir = "device", "network.interface.name", "network.io.direction"
	return []monitoring.MetricRow{
		directionSplit("system.network.io", "network", "By/s", "Bytes sent and received. Without network.io.direction, both.",
			"system_network_io_bytes_total", dev, ifName, dir, tr),
		directionSplit("system.network.packet.count", "network", "{packet}/s", "Packets sent and received. Without network.io.direction, both.",
			"system_network_packets_total", dev, "system.device.name", dir, tr),
		directionSplit("system.network.errors", "network", "{error}/s", "Errors sending and receiving. Without network.io.direction, both.",
			"system_network_errors_total", dev, ifName, dir, tr),
		directionSplit("system.network.packet.dropped", "network", "{packet}/s", "Packets dropped. Without network.io.direction, both.",
			"system_network_dropped_total", dev, ifName, dir, tr),
	}
}

func systemRows() []monitoring.MetricRow {
	load := func(window string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "system.cpu.load_average." + window, Schema: CollectorHostmetrics, Area: "system", Unit: "{thread}",
			Kind: monitoring.KindGauge, Description: "Load average over " + window + ".",
			Exprs:    hm(fmt.Sprintf(`max by (entity_id) (system_cpu_load_average_%s{$sel})`, window)),
			Requires: []string{RequiresLinux},
		}
	}
	return []monitoring.MetricRow{
		{
			Name: "system.uptime", Area: "system", Unit: "s", Kind: monitoring.KindGauge,
			Description: "Time since boot.",
			Exprs:       hm(`max by (entity_id) (system_uptime_seconds{$sel})`),
		},
		load("1m"), load("5m"), load("15m"),
	}
}

func processRows() []monitoring.MetricRow {
	pstate := monitoring.AttrLabel("process.state")
	const gap = "per-process collection is waiting on how process ids are kept (top-N in the collector or grouping by name)"
	return []monitoring.MetricRow{
		{
			Name: "system.process.count", Area: "process", Unit: "{process}", Kind: monitoring.KindGauge,
			Description: "Processes in each process.state. Without one, all processes.",
			Exprs:       hm(`sum by (entity_id) (system_processes_count{$sel})`),
			Attribute: &monitoring.Attribute{Name: "process.state",
				Values: []string{"running", "sleeping", "stopped", "defunct", "blocked", "idle"}},
			SplitExprs: hm(`sum by (` + by(pstate) + `) (` + relabel(`system_processes_count{$sel}`, "status", "process.state",
				map[string]string{"zombies": "defunct"}) + `)`),
			Requires: []string{RequiresLinux},
			// hostmetrics leaves a status with no processes out.
			ZeroFill: hmReporting,
		},
		{Name: "process.cpu.utilization", Area: "process", Unit: "1", Kind: monitoring.KindRate,
			Description: "CPU use of a process, one core = 1.", Dimension: "process", DimensionName: "process.executable.name", Gap: gap},
		{Name: "process.memory.utilization", Area: "process", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Memory share of a process.", Dimension: "process", DimensionName: "process.executable.name", Gap: gap},
	}
}

// gpuPending is the gap of every GPU row until the GPU receiver built into
// the collector exists and its metric names are known.
const gpuPending = "GPU receiver not built yet; expression follows its metric names"

// gpuRows lists the current GPU fields. They carry no expression yet: GPU
// metrics will come from a GPU receiver built into the collector, and the
// expressions are written against its metric names once it exists.
func gpuRows() []monitoring.MetricRow {
	g := func(name, unit, desc string, requires ...string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "gpu." + name, Area: "gpu", Unit: unit, Kind: monitoring.KindGauge,
			Description: desc, Dimension: "uuid", Requires: requires, Gap: gpuPending,
		}
	}
	const phys = RequiresPhysicalGPU
	rows := []monitoring.MetricRow{
		g("utilization", "ratio", "GPU utilization (gpu_util)"),
		g("memory_copy.utilization", "ratio", "Memory copy utilization (mem_copy_util)"),
		g("encoder.utilization", "ratio", "Encoder utilization (enc_util)"),
		g("decoder.utilization", "ratio", "Decoder utilization (dec_util)"),
		g("temperature", "celsius", "GPU temperature (gpu_temp)", phys),
		g("memory.temperature", "celsius", "Memory temperature (memory_temp)", phys),
		g("fan_speed", "ratio", "Fan speed (fan_speed)", phys),
		g("power", "watts", "Power draw (power_usage)", phys),
		g("memory.total", "bytes", "Frame buffer size (fb_total)"),
		g("memory.used", "bytes", "Frame buffer used (fb_used)"),
		g("memory.free", "bytes", "Frame buffer free (fb_free)"),
		g("clock.sm", "hertz", "SM clock (sm_clock)"),
		g("clock.memory", "hertz", "Memory clock (mem_clock)"),
		g("clock.event_reasons", "bitmask", "Active clock event reasons (clocks_event_reasons)", phys),
		g("remapped_rows.correctable", "rows", "Correctable remapped rows (correctable_remapped_rows)", phys),
		g("remapped_rows.uncorrectable", "rows", "Uncorrectable remapped rows (uncorrectable_remapped_rows)", phys),
		g("remapped_rows.failure", "bool", "Row remap failure (row_remap_failure)", phys),
		g("remapped_rows.pending", "bool", "Row remap pending (row_remap_pending)", phys),
		g("ecc.corrected.volatile", "errors", "Single-bit ECC errors since boot (ecc_sbe_vol_total)"),
		g("ecc.uncorrected.volatile", "errors", "Double-bit ECC errors since boot (ecc_dbe_vol_total)"),
		g("ecc.corrected.aggregate", "errors", "Single-bit ECC errors, lifetime (ecc_sbe_agg_total)"),
		g("ecc.uncorrected.aggregate", "errors", "Double-bit ECC errors, lifetime (ecc_dbe_agg_total)"),
		g("pstate", "state", "Performance state (p_state / pstate)"),
		g("retired_pages.double_bit", "pages", "Pages retired for double-bit errors (retired_pages_dbe)"),
		g("retired_pages.pending", "bool", "Page retirement pending (retired_pages_pending)"),
		g("pcie.replay", "events", "PCIe replay counter (pcie_replay_counter)"),
		g("vgpu.license", "bool", "vGPU license status (vgpu_license_status)"),
		{
			Name: "gpu.mig.sm.utilization", Area: "gpu", Unit: "ratio", Kind: monitoring.KindGauge,
			Description: "SM activity of a MIG instance (MIG gpu_util)", Dimension: "gpu_instance_id",
			Gap: gpuPending,
		},
		{
			Name: "gpu.xid_errors", Area: "gpu", Unit: "errors/s", Kind: monitoring.KindRate,
			Description: "XID errors (xid_errors)", Dimension: "uuid", Gap: gpuPending,
		},
		{
			Name: "gpu.info", Area: "gpu", Unit: "", Kind: monitoring.KindInfo,
			Description: "Driver, CUDA, VBIOS versions and PCI bus id (info tags, cuda_probe)", Dimension: "uuid",
			Gap: gpuPending,
		},
	}
	return rows
}
