package catalog

import (
	"fmt"

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

// Builtin returns the catalog rows ai-o11y ships with: every field the current
// host and GPU monitoring collects, with the hostmetrics or GPU exporter
// expression that reproduces it, or the gap that keeps one from existing yet.
func Builtin() []monitoring.MetricRow {
	var rows []monitoring.MetricRow
	rows = append(rows, cpuRows()...)
	rows = append(rows, memRows()...)
	rows = append(rows, swapRows()...)
	rows = append(rows, diskRows()...)
	rows = append(rows, diskioRows()...)
	rows = append(rows, netRows()...)
	rows = append(rows, systemRows()...)
	rows = append(rows, processesRows()...)
	rows = append(rows, procstatRows()...)
	rows = append(rows, gpuRows()...)
	return rows
}

func hm(expr string) map[string]string { return map[string]string{CollectorHostmetrics: expr} }

// cpuShare is the share of CPU time spent in state, across all CPUs. It holds
// whether or not the collector reports each CPU separately.
func cpuShare(state string) string {
	return fmt.Sprintf(`sum by (entity_id) (rate(system_cpu_time_seconds_total{state=%q,$sel}[$range]))`+
		` / sum by (entity_id) (rate(system_cpu_time_seconds_total{$sel}[$range]))`, state)
}

func cpuRows() []monitoring.MetricRow {
	row := func(name, state, desc string, requires ...string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.cpu." + name, Area: "cpu", Unit: "ratio", Kind: monitoring.KindRate,
			Description: desc, Exprs: hm(cpuShare(state)), Requires: requires,
		}
	}
	return []monitoring.MetricRow{
		{
			Name: "host.cpu.utilization", Area: "cpu", Unit: "ratio", Kind: monitoring.KindRate,
			Description: "Share of CPU time not idle (current 100 - usage_idle)",
			Exprs:       hm("1 - (" + cpuShare("idle") + ")"),
		},
		row("user", "user", "Share of CPU time in user mode (cpu.usage_user)"),
		row("system", "system", "Share of CPU time in kernel mode (cpu.usage_system)"),
		row("idle", "idle", "Share of CPU time idle (cpu.usage_idle)"),
		row("nice", "nice", "Share of CPU time in niced user mode (cpu.usage_nice)", RequiresLinux),
		row("iowait", "wait", "Share of CPU time waiting for I/O (cpu.usage_iowait)", RequiresLinux),
		row("irq", "interrupt", "Share of CPU time serving hardware interrupts (cpu.usage_irq)"),
		row("softirq", "softirq", "Share of CPU time serving soft interrupts (cpu.usage_softirq)", RequiresLinux),
		row("steal", "steal", "Share of CPU time taken by the hypervisor (cpu.usage_steal)", RequiresLinux),
	}
}

func memRows() []monitoring.MetricRow {
	usage := func(state string) string {
		return fmt.Sprintf(`sum by (entity_id) (system_memory_usage_bytes{state=%q,$sel})`, state)
	}
	row := func(name, state, desc string, requires ...string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.memory." + name, Area: "mem", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: desc, Exprs: hm(usage(state)), Requires: requires,
		}
	}
	return []monitoring.MetricRow{
		{
			Name: "host.memory.total", Area: "mem", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Physical memory (mem.total)",
			Exprs:       hm(`sum by (entity_id) (system_memory_limit_bytes{$sel})`),
			Enables:     []string{"system.memory.limit"},
		},
		{
			Name: "host.memory.available", Area: "mem", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Memory available for new work (mem.available)",
			Exprs:       hm(`sum by (entity_id) (system_linux_memory_available_bytes{$sel})`),
			Requires:    []string{RequiresLinux},
			Enables:     []string{"system.linux.memory.available"},
		},
		{
			Name: "host.memory.available_ratio", Area: "mem", Unit: "ratio", Kind: monitoring.KindGauge,
			Description: "Available share of physical memory (mem.available_percent)",
			Exprs: hm(`sum by (entity_id) (system_linux_memory_available_bytes{$sel})` +
				` / sum by (entity_id) (system_memory_limit_bytes{$sel})`),
			Requires: []string{RequiresLinux},
			Enables:  []string{"system.linux.memory.available", "system.memory.limit"},
		},
		row("used", "used", "Memory in use (mem.used)"),
		{
			Name: "host.memory.utilization", Area: "mem", Unit: "ratio", Kind: monitoring.KindGauge,
			Description: "Used share of physical memory (mem.used_percent)",
			Exprs:       hm(`sum by (entity_id) (system_memory_utilization_ratio{state="used",$sel})`),
			Enables:     []string{"system.memory.utilization"},
		},
		row("free", "free", "Free memory (mem.free)"),
		row("buffered", "buffered", "Memory in kernel buffers (mem.buffered)", RequiresLinux),
		row("cached", "cached", "Memory in page cache (mem.cached)", RequiresLinux),
		row("inactive", "inactive", "Inactive memory (mem.inactive)", RequiresLinux),
		{
			Name: "host.memory.active", Area: "mem", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Active memory (mem.active)",
			Gap:         "hostmetrics has no active memory state",
		},
		{
			Name: "host.memory.shared", Area: "mem", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Shared memory (mem.shared)",
			Exprs:       hm(`sum by (entity_id) (system_memory_linux_shared_bytes{$sel})`),
			Requires:    []string{RequiresLinux},
			Enables:     []string{"system.memory.linux.shared"},
		},
	}
}

func swapRows() []monitoring.MetricRow {
	total := `sum by (entity_id) (system_paging_usage_bytes{$sel})`
	state := func(s string) string {
		return fmt.Sprintf(`sum by (entity_id) (system_paging_usage_bytes{state=%q,$sel})`, s)
	}
	pages := func(direction string) string {
		return fmt.Sprintf(`sum by (entity_id) (rate(system_paging_operations_total{direction=%q,type="major",$sel}[$range]))`, direction)
	}
	return []monitoring.MetricRow{
		{Name: "host.swap.total", Area: "swap", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Swap space (swap.total)", Exprs: hm(total)},
		{Name: "host.swap.used", Area: "swap", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Swap in use (swap.used)", Exprs: hm(state("used"))},
		{Name: "host.swap.free", Area: "swap", Unit: "bytes", Kind: monitoring.KindGauge,
			Description: "Free swap (swap.free)", Exprs: hm(state("free"))},
		{Name: "host.swap.utilization", Area: "swap", Unit: "ratio", Kind: monitoring.KindGauge,
			Description: "Used share of swap (swap.used_percent)", Exprs: hm(state("used") + " / " + total)},
		{Name: "host.swap.in", Area: "swap", Unit: "pages/s", Kind: monitoring.KindRate,
			Description: "Pages swapped in (swap.in, which counts bytes)", Exprs: hm(pages("page_in")), Approx: true},
		{Name: "host.swap.out", Area: "swap", Unit: "pages/s", Kind: monitoring.KindRate,
			Description: "Pages swapped out (swap.out, which counts bytes)", Exprs: hm(pages("page_out")), Approx: true},
	}
}

func diskRows() []monitoring.MetricRow {
	const by = "entity_id, mountpoint"
	sum := func(metric, state string) string {
		if state == "" {
			return fmt.Sprintf(`sum by (%s) (%s{$sel})`, by, metric)
		}
		return fmt.Sprintf(`sum by (%s) (%s{state=%q,$sel})`, by, metric, state)
	}
	const usage, inodes = "system_filesystem_usage_bytes", "system_filesystem_inodes_usage"
	row := func(name, unit, desc, expr string, requires ...string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.filesystem." + name, Area: "disk", Unit: unit, Kind: monitoring.KindGauge,
			Description: desc, Exprs: hm(expr), Dimension: "mountpoint", Requires: requires,
		}
	}
	return []monitoring.MetricRow{
		row("total", "bytes", "File system size (disk.total)", sum(usage, "")),
		row("free", "bytes", "Free space (disk.free)", sum(usage, "free")),
		row("used", "bytes", "Used space (disk.used)", sum(usage, "used")),
		row("utilization", "ratio", "Used share of the file system (disk.used_percent)", sum(usage, "used")+" / "+sum(usage, "")),
		row("inodes.total", "inodes", "Inodes (disk.inodes_total)", sum(inodes, ""), RequiresLinux),
		row("inodes.free", "inodes", "Free inodes (disk.inodes_free)", sum(inodes, "free"), RequiresLinux),
		row("inodes.used", "inodes", "Used inodes (disk.inodes_used)", sum(inodes, "used"), RequiresLinux),
		row("inodes.utilization", "ratio", "Used share of inodes (disk.inodes_used_percent)", sum(inodes, "used")+" / "+sum(inodes, ""), RequiresLinux),
	}
}

func diskioRows() []monitoring.MetricRow {
	const by = "entity_id, device"
	rate := func(metric, direction string) string {
		if direction == "" {
			return fmt.Sprintf(`sum by (%s) (rate(%s{$sel}[$range]))`, by, metric)
		}
		return fmt.Sprintf(`sum by (%s) (rate(%s{direction=%q,$sel}[$range]))`, by, metric, direction)
	}
	const ops, io, opTime, ioTime = "system_disk_operations_total", "system_disk_io_bytes_total",
		"system_disk_operation_time_seconds_total", "system_disk_io_time_seconds_total"
	row := func(name, unit, desc, expr string, approx bool) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.disk." + name, Area: "diskio", Unit: unit, Kind: monitoring.KindRate,
			Description: desc, Exprs: hm(expr), Dimension: "device", Approx: approx,
		}
	}
	return []monitoring.MetricRow{
		row("reads", "ops/s", "Completed reads (diskio.reads)", rate(ops, "read"), false),
		row("writes", "ops/s", "Completed writes (diskio.writes)", rate(ops, "write"), false),
		row("read_bytes", "bytes/s", "Bytes read (diskio.read_bytes)", rate(io, "read"), false),
		row("write_bytes", "bytes/s", "Bytes written (diskio.write_bytes)", rate(io, "write"), false),
		row("read_time", "s/s", "Time spent reading (diskio.read_time)", rate(opTime, "read"), false),
		row("write_time", "s/s", "Time spent writing (diskio.write_time)", rate(opTime, "write"), false),
		row("io_time", "s/s", "Time the device was busy (diskio.io_time)", rate(ioTime, ""), false),
		{
			Name: "host.disk.pending_operations", Area: "diskio", Unit: "operations", Kind: monitoring.KindGauge,
			Description: "I/O in progress (diskio.iops_in_progress)",
			Exprs:       hm(`sum by (entity_id, device) (system_disk_pending_operations{$sel})`), Dimension: "device",
		},
		row("utilization", "ratio", "Busy share of the device (diskio.io_util)", rate(ioTime, ""), true),
		row("await", "s", "Average time per I/O, queueing included (diskio.io_await)", rate(opTime, "")+" / "+rate(ops, ""), true),
		row("svctm", "s", "Average service time per I/O (diskio.io_svctm)", rate(ioTime, "")+" / "+rate(ops, ""), true),
	}
}

func netRows() []monitoring.MetricRow {
	rate := func(metric, direction string) string {
		return fmt.Sprintf(`sum by (entity_id, device) (rate(%s{direction=%q,$sel}[$range]))`, metric, direction)
	}
	row := func(name, unit, metric, direction, field string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.network." + name, Area: "net", Unit: unit, Kind: monitoring.KindRate,
			Description: fmt.Sprintf("%s (net.%s)", name, field),
			Exprs:       hm(rate(metric, direction)), Dimension: "device",
		}
	}
	return []monitoring.MetricRow{
		row("transmit_bytes", "bytes/s", "system_network_io_bytes_total", "transmit", "bytes_sent"),
		row("receive_bytes", "bytes/s", "system_network_io_bytes_total", "receive", "bytes_recv"),
		row("transmit_packets", "packets/s", "system_network_packets_total", "transmit", "packets_sent"),
		row("receive_packets", "packets/s", "system_network_packets_total", "receive", "packets_recv"),
		row("transmit_errors", "errors/s", "system_network_errors_total", "transmit", "err_out"),
		row("receive_errors", "errors/s", "system_network_errors_total", "receive", "err_in"),
		row("transmit_dropped", "packets/s", "system_network_dropped_total", "transmit", "drop_out"),
		row("receive_dropped", "packets/s", "system_network_dropped_total", "receive", "drop_in"),
	}
}

func systemRows() []monitoring.MetricRow {
	gauge := func(name, unit, desc, expr string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host." + name, Area: "system", Unit: unit, Kind: monitoring.KindGauge,
			Description: desc, Exprs: hm(expr),
		}
	}
	load := func(window, field string) monitoring.MetricRow {
		// On Windows the collector synthesises load from the processor queue
		// length, so it does not compare with Linux load.
		return gauge("load"+window, "threads", "Load average over "+window+" (system."+field+")",
			fmt.Sprintf(`max by (entity_id) (system_cpu_load_average_%s{$sel})`, window))
	}
	physical := gauge("cpu.physical_count", "cpus", "Physical CPUs (system.n_physical_cpus)",
		`max by (entity_id) (system_cpu_physical_count{$sel})`)
	physical.Enables = []string{"system.cpu.physical.count"}
	return []monitoring.MetricRow{
		load("1m", "load1"),
		load("5m", "load5"),
		load("15m", "load15"),
		gauge("cpu.logical_count", "cpus", "Logical CPUs (system.n_cpus)", `max by (entity_id) (system_cpu_logical_count{$sel})`),
		physical,
		gauge("uptime", "s", "Time since boot (system.uptime)", `max by (entity_id) (system_uptime_seconds{$sel})`),
		{Name: "host.users", Area: "system", Unit: "users", Kind: monitoring.KindGauge,
			Description: "Logged-in users (system.n_users)", Gap: "hostmetrics has no logged-in user count"},
		{Name: "host.unique_users", Area: "system", Unit: "users", Kind: monitoring.KindGauge,
			Description: "Distinct logged-in users (system.n_unique_users)", Gap: "hostmetrics has no logged-in user count"},
	}
}

func processesRows() []monitoring.MetricRow {
	status := func(name, s string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.processes." + name, Area: "processes", Unit: "processes", Kind: monitoring.KindGauge,
			Description: fmt.Sprintf("Processes %s (processes.%s)", name, name),
			Exprs:       hm(fmt.Sprintf(`sum by (entity_id) (system_processes_count{status=%q,$sel})`, s)),
			Requires:    []string{RequiresLinux},
		}
	}
	gap := func(name string) monitoring.MetricRow {
		return monitoring.MetricRow{
			Name: "host.processes." + name, Area: "processes", Unit: "processes", Kind: monitoring.KindGauge,
			Description: fmt.Sprintf("Processes %s (processes.%s)", name, name),
			Gap:         "hostmetrics has no " + name + " process status",
		}
	}
	return []monitoring.MetricRow{
		{
			Name: "host.processes.total", Area: "processes", Unit: "processes", Kind: monitoring.KindGauge,
			Description: "All processes (processes.total), summed over the statuses hostmetrics reports",
			Exprs:       hm(`sum by (entity_id) (system_processes_count{$sel})`),
			Requires:    []string{RequiresLinux}, Approx: true,
		},
		status("running", "running"),
		status("blocked", "blocked"),
		status("sleeping", "sleeping"),
		status("stopped", "stopped"),
		status("zombie", "zombies"),
		status("idle", "idle"),
		gap("dead"),
		gap("parked"),
	}
}

func procstatRows() []monitoring.MetricRow {
	const gap = "per-process collection is waiting on how process ids are kept (top-N in the collector or grouping by name)"
	return []monitoring.MetricRow{
		{Name: "process.cpu.utilization", Area: "procstat", Unit: "ratio", Kind: monitoring.KindRate,
			Description: "CPU use of a top process, one core = 1 (procstat.cpu_usage)", Dimension: "process", Gap: gap},
		{Name: "process.memory.utilization", Area: "procstat", Unit: "ratio", Kind: monitoring.KindGauge,
			Description: "Memory share of a top process (procstat.memory_usage)", Dimension: "process", Gap: gap},
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
