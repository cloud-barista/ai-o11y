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
	// OTLP. The backend stores its metrics under Prometheus names (see
	// series): system.cpu.time in seconds becomes
	// system_cpu_time_seconds_total. Its attribute names have no dots and
	// stay as sent (state, device); an attribute name with dots is stored with
	// underscores like a metric name, as process.executable.name, which the
	// top_process processor puts on the process scraper's points, is stored as
	// process_executable_name.
	CollectorHostmetrics = "hostmetrics"
	// CollectorNvidiaSMI is the nvidia_gpu receiver built into the collector,
	// reading nvidia-smi; it writes nvidia_smi.<query field>, stored as
	// nvidia_smi_<field>_<unit> (see nvidiaSMI).
	CollectorNvidiaSMI = "nvidia_smi"
	// CollectorAMDSMI is the amd_gpu receiver built into the collector,
	// reading amd-smi metric --json; it writes amd_smi.<path in the JSON>,
	// stored as amd_smi_<path>_<unit> (see amdSMI).
	CollectorAMDSMI = "amd_smi"
)

// SchemaNvidiaKmsg names the fields the nvidia_gpu receiver counts from the
// kernel log (/dev/kmsg) rather than reads from nvidia-smi; it writes them as
// nvidia_kmsg.<field>, a monotonic sum the backend stores as
// nvidia_kmsg_<field>_total. Their expressions are the nvidia_smi collector's,
// the receiver that writes them.
const SchemaNvidiaKmsg = "nvidia_kmsg"

// SchemaNVML names the fields the nvidia_gpu receiver reads from NVML, through
// its helper aio11y-gpm, rather than from nvidia-smi; it writes them as
// nvml.<field> in percent, stored as nvml_<field>_percent. Their expressions
// are the nvidia_smi collector's, the receiver that writes them.
const SchemaNVML = "nvml"

// Capabilities a row can require.
const (
	RequiresLinux = "os:linux"
)

// Builtin returns the catalog rows ai-o11y ships with. Common fields carry
// the OpenTelemetry semantic conventions' names (v1.45.0) and meaning and say
// how the collectors (hostmetrics, nvidia-smi, amd-smi) provide them;
// collector metrics with no counterpart there are fields of that collector's
// own.
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
	CollectorHostmetrics: `0 * group by (entity_id) (` + series("system_cpu_time_seconds_total") + `)`,
}

// relabel copies the hostmetrics label from into the attribute label of attr,
// renaming the values whose OpenTelemetry name differs (rename maps the
// hostmetrics value to it).
func relabel(expr, from, attr string, rename map[string]string) string {
	out := fmt.Sprintf(`label_replace(%s, %q, "$1", %q, "(.*)")`, expr, attr, from)
	keys := make([]string, 0, len(rename))
	for k := range rename {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = fmt.Sprintf(`label_replace(%s, %q, %q, %q, %q)`, out, attr, rename[k], from, k)
	}
	return out
}

// by joins entity_id with the grouping labels, quoted where PromQL needs it.
func by(labels ...string) string {
	out := []string{"entity_id"}
	for _, l := range labels {
		out = append(out, monitoring.PromQLName(l))
	}
	return strings.Join(out, ", ")
}

// series selects a stored series for the targets: the matchers, then $sel.
// series("system_cpu_time_seconds_total", `state="idle"`) is
// system_cpu_time_seconds_total{state="idle", $sel}.
//
// Names are the ones the backend stores. Every supported backend turns an
// OTLP metric into the same Prometheus name: dots become underscores, then
// the unit (seconds, bytes, ratio, percent, ...) and, for a monotonic sum,
// _total are appended. That is the default of Prometheus' OTLP receiver
// (otlp.translation_strategy UnderscoreEscapingWithSuffixes), what Thanos
// Receive always does, and VictoriaMetrics with
// -opentelemetry.usePrometheusNaming; the names were compared on all three
// with the hostmetrics and nvidia_gpu receivers.
func series(name string, matchers ...string) string {
	return name + "{" + strings.Join(append(matchers, monitoring.PlaceholderSelector), ", ") + "}"
}

func cpuRows() []monitoring.MetricRow {
	const time = `rate(system_cpu_time_seconds_total{$sel}[$range])`
	total := `sum by (entity_id) (` + time + `)`
	idle := `sum by (entity_id) (rate(system_cpu_time_seconds_total{state="idle", $sel}[$range]))`
	mode := "cpu.mode"
	return []monitoring.MetricRow{
		{
			Name: "system.cpu.utilization", Area: "cpu", Unit: "1", Kind: monitoring.KindRate,
			Description: "Share of CPU time in each cpu.mode. Without one, the share not idle.",
			Exprs:       hm("1 - (" + idle + " / " + total + ")"),
			Attribute: &monitoring.Attribute{Name: "cpu.mode",
				Values: []string{"user", "system", "nice", "idle", "iowait", "interrupt", "steal", "softirq"}},
			SplitExprs: hm(`sum by (` + by(mode) + `) (` + relabel(time, "state", "cpu.mode", map[string]string{"wait": "iowait"}) + `)` +
				` / ignoring (` + monitoring.PromQLName(mode) + `) group_left ` + total),
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
	state := "system.memory.state"
	split := func(metric string) string {
		return `sum by (` + by(state) + `) (` + relabel(series(metric), "state", "system.memory.state",
			map[string]string{"buffered": "buffers"}) + `)`
	}
	attr := &monitoring.Attribute{Name: "system.memory.state",
		Values: []string{"used", "free", "buffers", "cached", "inactive", "slab_reclaimable", "slab_unreclaimable"}}
	return []monitoring.MetricRow{
		{
			Name: "system.memory.usage", Area: "memory", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Memory in each system.memory.state. Without one, memory used.",
			Exprs:       hm(`sum by (entity_id) (system_memory_usage_bytes{state="used", $sel})`),
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
			Exprs:       hm(`sum by (entity_id) (system_memory_utilization_ratio{state="used", $sel})`),
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
	state := "system.paging.state"
	total := `sum by (entity_id) (` + series(usage) + `)`
	byState := `sum by (` + by(state) + `) (` + relabel(series(usage), "state", "system.paging.state", nil) + `)`
	attr := &monitoring.Attribute{Name: "system.paging.state", Values: []string{"used", "free", "cached"}}
	// hostmetrics sends no paging usage on a host without swap.
	return []monitoring.MetricRow{
		{
			Name: "system.paging.usage", Area: "paging", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Swap space in each system.paging.state. Without one, swap used.",
			Exprs:       hm(`sum by (entity_id) (` + series(usage, `state="used"`) + `)`),
			Attribute:   attr, SplitExprs: hm(byState), ZeroFill: hmReporting,
		},
		{
			Name: "system.paging.utilization", Area: "paging", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of swap space in each system.paging.state. Without one, the share used.",
			Exprs:       hm(`sum by (entity_id) (` + series(usage, `state="used"`) + `) / ` + total),
			Attribute:   attr, SplitExprs: hm(byState + ` / ignoring (` + monitoring.PromQLName(state) + `) group_left ` + total),
			ZeroFill: hmReporting,
		},
		{
			Name: "system.paging.faults", Area: "paging", Unit: "{fault}/s", Kind: monitoring.KindRate,
			Description: "Page faults.",
			Gap:         "hostmetrics reports Linux page faults multiplied by 4096 (gopsutil), not a count of faults",
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
	r := `rate(` + series(metric) + `[$range])`
	return monitoring.MetricRow{
		Name: name, Area: area, Unit: unit, Kind: monitoring.KindRate, Description: desc,
		Exprs:     hm(`sum by (` + by(dim) + `) (` + r + `)`),
		Attribute: &monitoring.Attribute{Name: attr, Values: values},
		SplitExprs: hm(`sum by (` + by(dim, attr) + `) (` +
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
	state := "system.filesystem.state"
	limit := `sum by (entity_id, mountpoint) (` + series(usage) + `)`
	byState := `sum by (` + by(mnt, state) + `) (` + relabel(series(usage), "state", "system.filesystem.state", nil) + `)`
	used := `sum by (entity_id, mountpoint) (` + series(usage, `state="used"`) + `)`
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
			SplitExprs: hm(byState + ` / ignoring (` + monitoring.PromQLName(state) + `) group_left ` + limit),
			Dimension:  mnt, DimensionName: mntName,
		},
		{
			Name: "system.filesystem.inodes.usage", Schema: CollectorHostmetrics, Area: "filesystem", Unit: "{inode}",
			Kind: monitoring.KindGauge, Description: "Inodes in each state. Without one, inodes used.",
			Exprs:      hm(`sum by (entity_id, mountpoint) (system_filesystem_inodes_usage{state="used", $sel})`),
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
			Exprs:    hm(`max by (entity_id) (` + series("system_cpu_load_average_"+window) + `)`),
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

// processRows are the system's process count and the per-process fields. The
// collector sends per-process series only for the processes using the most
// CPU or memory (the top_process processor), with process.executable.name and
// process.pid on every point, stored as process_executable_name and
// process_pid; the fields add up the processes of one name.
func processRows() []monitoring.MetricRow {
	pstate := "process.state"
	const label, name = "process_executable_name", "process.executable.name"
	perName := func(metric string) string { return `sum by (` + by(label) + `) (` + series(metric) + `)` }
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
		{
			Name: "process.cpu.utilization", Area: "process", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of the time of all CPUs the processes of one name used since the previous collection, all CPU states. Only the processes using the most CPU or memory are collected.",
			// hostmetrics divides by the host's logical CPUs and splits it by state.
			Exprs:     hm(perName("process_cpu_utilization_ratio")),
			Dimension: label, DimensionName: name,
			Enables: []string{"process.cpu.utilization"},
		},
		{
			Name: "process.memory.utilization", Area: "process", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of the physical memory the processes of one name hold resident. Only the processes using the most CPU or memory are collected.",
			// hostmetrics reports a percentage (gopsutil MemoryPercent) under unit 1.
			Exprs:     hm(perName("process_memory_utilization_ratio") + " / 100"),
			Dimension: label, DimensionName: name,
			Enables: []string{"process.memory.utilization"},
		},
	}
}

// nvidiaSMI is the stored series name of an nvidia-smi query field. The
// receiver sends it as nvidia_smi.<field> with the unit nvidia-smi's header
// gives (nvidiagpureceiver's unitOf), which the backend appends: utilization.gpu
// [%] is stored as nvidia_smi_utilization_gpu_percent.
func nvidiaSMI(field string) string {
	name := CollectorNvidiaSMI + "_" + strings.ReplaceAll(field, ".", "_")
	for _, u := range smiUnits {
		if strings.HasPrefix(field, u.prefix) {
			return name + "_" + u.suffix
		}
	}
	return name
}

// smiUnits are the name suffixes of the nvidia-smi fields with a unit: the
// header unit as the receiver maps it (% to %, MiB to MiBy, ...) and as the
// backend writes it.
var smiUnits = []struct{ prefix, suffix string }{
	{"utilization.", "percent"},
	{"fan.speed", "percent"},
	{"memory.", "mebibytes"},
	{"power.", "watts"},
	{"temperature.", "celsius"},
	{"clocks.", "MHz"},
}

// amdSMI is the stored series name of an amd-smi value. The receiver sends it
// as amd_smi.<path in amd-smi metric --json> with the unit of its field
// (amdgpureceiver's fields), which the backend appends: usage.gfx_activity in
// % is stored as amd_smi_usage_gfx_activity_percent.
func amdSMI(path string) string {
	name := CollectorAMDSMI + "_" + strings.ReplaceAll(path, ".", "_")
	for _, u := range amdUnits {
		if strings.HasPrefix(path, u.prefix) {
			return name + "_" + u.suffix
		}
	}
	return name
}

// amdUnits are the name suffixes of the amd-smi values the rows read that have
// a unit: the unit as the receiver maps it (MB to MiBy, C to Cel, ...) and as
// the backend writes it. Counts (pcie.replay_count, ecc.*) have none.
var amdUnits = []struct{ prefix, suffix string }{
	{"usage.", "percent"},
	{"mem_usage.", "mebibytes"},
	{"power.socket_power", "watts"},
	{"energy.", "joules"},
	{"temperature.", "celsius"},
	{"clock.", "MHz"},
}

// smi maps a GPU field to its nvidia-smi expression.
func smi(expr string) map[string]string { return map[string]string{CollectorNvidiaSMI: expr} }

// perGPU is the value of a GPU receiver's series per GPU.
func perGPU(name string) string {
	return `max by (entity_id, uuid) (` + series(name) + `)`
}

// gpus maps a GPU field to its nvidia-smi and its amd-smi expression.
func gpus(nvidia, amd string) map[string]string {
	return map[string]string{CollectorNvidiaSMI: nvidia, CollectorAMDSMI: amd}
}

// gpuRows are the GPU fields. The common ones carry the OpenTelemetry
// hardware semantic conventions' names (hw.gpu.*, hw.power), split within a
// target by hw.id, the GPU's UUID, and are read from nvidia-smi and amd-smi
// alike; the rest are nvidia-smi's own query fields and amd-smi's own values.
// Both receivers write percentages and MiB, which the common fields turn into
// a share (unit 1) and bytes.
func gpuRows() []monitoring.MetricRow {
	const dim, dimName = "uuid", "hw.id"
	task := "hw.gpu.task"
	util := func(field string) string { return perGPU(nvidiaSMI(field)) + " / 100" }
	amdUtil := func(path string) string { return perGPU(amdSMI(path)) + " / 100" }
	tagged := func(expr, value string) string {
		return fmt.Sprintf(`label_replace(%s, %q, %q, "", "")`, expr, task, value)
	}
	byTask := func(field, value string) string { return tagged(util(field), value) }
	const mib = " * 1048576"
	used, total := perGPU(nvidiaSMI("memory.used")), perGPU(nvidiaSMI("memory.total"))
	amdUsed, amdTotal := perGPU(amdSMI("mem_usage.used_vram")), perGPU(amdSMI("mem_usage.total_vram"))

	rows := []monitoring.MetricRow{
		{
			Name: "hw.gpu.utilization", Area: "gpu", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of time the GPU spent on each hw.gpu.task. Without one, general work " +
				"(nvidia-smi utilization.gpu, amd-smi usage.gfx_activity). amd-smi gives general work only.",
			Exprs:     gpus(util("utilization.gpu"), amdUtil("usage.gfx_activity")),
			Attribute: &monitoring.Attribute{Name: "hw.gpu.task", Values: []string{"general", "encoder", "decoder"}},
			SplitExprs: gpus(`sum by (`+by("uuid", task)+`) (`+byTask("utilization.gpu", "general")+` or `+
				byTask("utilization.encoder", "encoder")+` or `+byTask("utilization.decoder", "decoder")+`)`,
				`sum by (`+by("uuid", task)+`) (`+tagged(amdUtil("usage.gfx_activity"), "general")+`)`),
			Dimension: dim, DimensionName: dimName,
		},
		{
			Name: "hw.gpu.memory.usage", Area: "gpu", Unit: "By", Kind: monitoring.KindGauge,
			Description: "GPU memory used.", Exprs: gpus(used+mib, amdUsed+mib), Dimension: dim, DimensionName: dimName,
		},
		{
			Name: "hw.gpu.memory.limit", Area: "gpu", Unit: "By", Kind: monitoring.KindGauge,
			Description: "Size of the GPU memory.", Exprs: gpus(total+mib, amdTotal+mib), Dimension: dim, DimensionName: dimName,
		},
		{
			Name: "hw.gpu.memory.utilization", Area: "gpu", Unit: "1", Kind: monitoring.KindGauge,
			Description: "Share of the GPU memory used.", Exprs: gpus(used+" / "+total, amdUsed+" / "+amdTotal),
			Dimension: dim, DimensionName: dimName,
		},
		{
			Name: "hw.power", Area: "gpu", Unit: "W", Kind: monitoring.KindGauge,
			Description: "Power drawn by the GPU (hw.type gpu).",
			Exprs:       gpus(perGPU(nvidiaSMI("power.draw")), perGPU(amdSMI("power.socket_power"))),
			Dimension:   dim, DimensionName: dimName,
		},
	}

	// nvidia-smi's own fields, under its query field names.
	own := []struct{ field, unit, desc string }{
		{"utilization.memory", "%", "Share of time the memory was read or written."},
		{"temperature.gpu", "Cel", "GPU core temperature."},
		{"temperature.memory", "Cel", "GPU memory temperature."},
		{"fan.speed", "%", "Fan speed the GPU asks for."},
		{"clocks.sm", "MHz", "SM clock."},
		{"clocks.mem", "MHz", "Memory clock."},
		{"pstate", "", "Performance state, 0 (P0, highest) to 15 (P15, lowest)."},
		{"clocks_event_reasons.active", "", "Bit mask of the reasons the clocks are held down."},
		{"ecc.errors.corrected.volatile.total", "{error}", "Corrected ECC errors since the driver loaded."},
		{"ecc.errors.uncorrected.volatile.total", "{error}", "Uncorrected ECC errors since the driver loaded."},
		{"ecc.errors.corrected.aggregate.total", "{error}", "Corrected ECC errors over the GPU's life."},
		{"ecc.errors.uncorrected.aggregate.total", "{error}", "Uncorrected ECC errors over the GPU's life."},
		{"retired_pages.double_bit.count", "{page}", "Pages retired for double-bit ECC errors."},
		{"retired_pages.pending", "", "1 when a page waits to be retired at the next reboot."},
		{"remapped_rows.correctable", "{row}", "Rows remapped for correctable errors."},
		{"remapped_rows.uncorrectable", "{row}", "Rows remapped for uncorrectable errors."},
		{"remapped_rows.pending", "", "1 when a row remap waits for a GPU reset."},
		{"remapped_rows.failure", "", "1 when a row remap failed."},
	}
	for _, f := range own {
		rows = append(rows, monitoring.MetricRow{
			Name: f.field, Schema: CollectorNvidiaSMI, Area: "gpu", Unit: f.unit, Kind: monitoring.KindGauge,
			Description: f.desc, Exprs: smi(perGPU(nvidiaSMI(f.field))), Dimension: dim, DimensionName: dim,
		})
	}

	// amd-smi's own fields, under their path in amd-smi metric --json.
	amdOwn := []struct{ field, unit, desc string }{
		{"usage.umc_activity", "%", "Share of time the memory controller was busy."},
		{"usage.mm_activity", "%", "Share of time the multimedia engine was busy."},
		{"temperature.edge", "Cel", "GPU temperature at the edge of the die."},
		{"temperature.hotspot", "Cel", "Hottest point of the GPU die (junction)."},
		{"temperature.mem", "Cel", "GPU memory temperature."},
		{"clock.gfx_0.clk", "MHz", "Clock of the first graphics domain."},
		{"clock.mem_0.clk", "MHz", "Memory clock."},
		{"mem_usage.used_gtt", "MiBy", "Host memory mapped for the GPU (GTT) in use."},
		{"mem_usage.total_gtt", "MiBy", "Size of the host memory the GPU can map (GTT)."},
		{"energy.total_energy_consumption", "J", "Energy the GPU has used, a counter that only grows until it resets."},
		{"pcie.replay_count", "", "PCIe packets replayed, a counter."},
		{"ecc.total_correctable_count", "{error}", "Correctable ECC errors the driver counted."},
		{"ecc.total_uncorrectable_count", "{error}", "Uncorrectable ECC errors the driver counted."},
		{"ecc.total_deferred_count", "{error}", "Deferred ECC errors the driver counted."},
	}
	for _, f := range amdOwn {
		rows = append(rows, monitoring.MetricRow{
			Name: f.field, Schema: CollectorAMDSMI, Area: "gpu", Unit: f.unit, Kind: monitoring.KindGauge,
			Description: f.desc, Exprs: map[string]string{CollectorAMDSMI: perGPU(amdSMI(f.field))}, Dimension: dim, DimensionName: dim,
		})
	}

	// XID errors the receiver counts from the kernel log: a cumulative count
	// per GPU and XID number since boot, summed over the XID numbers.
	rows = append(rows, monitoring.MetricRow{
		Name: "xid.errors", Schema: SchemaNvidiaKmsg, Area: "gpu", Unit: "{error}", Kind: monitoring.KindGauge,
		Description: "XID errors the NVIDIA driver logged to the kernel log since boot, all XID numbers together, " +
			"as far back as the kernel's log buffer holds. A GPU with none has no series.",
		Exprs:     smi(`sum by (entity_id, uuid) (` + series(SchemaNvidiaKmsg+"_xid_errors_total") + `)`),
		Dimension: dim, DimensionName: dim,
	})

	// SM utilization per MIG instance, which only NVML GPM gives. The receiver
	// writes GPM in percent for the whole GPU and for each MIG instance, the
	// latter with mig "true" and the GPU instance id GPM sampled; the parent
	// GPU's uuid and gpu_instance_id keep the instances apart, also when NVML
	// gave no MIG UUID.
	const gi = "gpu_instance_id"
	rows = append(rows, monitoring.MetricRow{
		Name: "mig.sm.utilization", Schema: SchemaNVML, Area: "gpu", Unit: "1", Kind: monitoring.KindGauge,
		Description: "Share of time the SMs of a MIG instance were busy, per GPU and GPU instance (NVML GPM). " +
			"Only Hopper and later GPUs have GPM; a GPU without it or without MIG has no series.",
		Exprs:     smi(`max by (` + by("uuid", gi) + `) (` + series(SchemaNVML+"_gpm_sm_utilization_percent", `mig="true"`) + `) / 100`),
		Dimension: gi, DimensionName: gi,
	})
	return rows
}
