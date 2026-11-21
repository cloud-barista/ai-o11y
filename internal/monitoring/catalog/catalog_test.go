package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
	"github.com/prometheus/prometheus/promql/promqltest"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

func TestBuiltinIsValid(t *testing.T) {
	c, err := New(Builtin())
	if err != nil {
		t.Fatalf("New(Builtin()): %v", err)
	}

	// Every current area is covered.
	areas := map[string]int{}
	for _, r := range c.Rows() {
		areas[r.Area]++
	}
	for _, area := range []string{"cpu", "memory", "paging", "disk", "filesystem", "network", "system", "process", "gpu"} {
		if areas[area] == 0 {
			t.Errorf("no rows for area %q", area)
		}
	}

	if _, err := c.Lookup("system.cpu.utilization"); err != nil {
		t.Fatalf("Lookup common field: %v", err)
	}
	if _, err := c.Lookup("hostmetrics:system.cpu.load_average.1m"); err != nil {
		t.Fatalf("Lookup hostmetrics field: %v", err)
	}
	if _, err := c.Lookup("system.cpu.load_average.1m"); err == nil {
		t.Fatal("Lookup of a collector's own field without its schema succeeded")
	}
	if _, err := c.Lookup("system.cpu.nope"); err == nil {
		t.Fatal("Lookup of an unknown metric succeeded")
	}
}

// TestBuiltinExpressionsParse fills the placeholders the way the renderer
// will and checks every expression is valid PromQL.
func TestBuiltinExpressionsParse(t *testing.T) {
	r := strings.NewReplacer(
		monitoring.PlaceholderSelector, `entity_id=~"a|b"`,
		monitoring.PlaceholderRange, "5m",
	)
	p := parser.NewParser(parser.Options{})
	for _, row := range Builtin() {
		for _, exprs := range []map[string]string{row.Exprs, row.SplitExprs, row.ZeroFill} {
			for collector, expr := range exprs {
				if _, err := p.ParseExpr(r.Replace(expr)); err != nil {
					t.Errorf("%s (%s): %v\n%s", row.ID(), collector, err, expr)
				}
			}
		}
	}
}

func TestNewRejects(t *testing.T) {
	ok := monitoring.MetricRow{
		Name: "x", Kind: monitoring.KindGauge,
		Exprs: map[string]string{"hostmetrics": `max by (entity_id) (m{$sel})`},
	}
	for name, mutate := range map[string]func(*monitoring.MetricRow){
		"empty name":    func(r *monitoring.MetricRow) { r.Name = "" },
		"unknown kind":  func(r *monitoring.MetricRow) { r.Kind = "histogram" },
		"no selector":   func(r *monitoring.MetricRow) { r.Exprs = map[string]string{"h": `max by (entity_id) (m)`} },
		"rate no range": func(r *monitoring.MetricRow) { r.Kind = monitoring.KindRate },
		"gauge range": func(r *monitoring.MetricRow) {
			r.Exprs = map[string]string{"h": `rate(m{$sel}[$range]) by (entity_id)`}
		},
		"no entity_id":   func(r *monitoring.MetricRow) { r.Exprs = map[string]string{"h": `max(m{$sel})`} },
		"gap and exprs":  func(r *monitoring.MetricRow) { r.Gap = "not yet" },
		"nothing at all": func(r *monitoring.MetricRow) { r.Exprs = nil },
		"zero fill for another collector": func(r *monitoring.MetricRow) {
			r.ZeroFill = map[string]string{"gpu": `0 * group by (entity_id) (m{$sel})`}
		},
		"zero fill without selector": func(r *monitoring.MetricRow) {
			r.ZeroFill = map[string]string{"hostmetrics": `0 * group by (entity_id) (m)`}
		},
		"attribute without split": func(r *monitoring.MetricRow) {
			r.Attribute = &monitoring.Attribute{Name: "cpu.mode"}
		},
		"split without attribute": func(r *monitoring.MetricRow) {
			r.SplitExprs = map[string]string{"hostmetrics": `max by (entity_id, cpu_mode) (m{$sel})`}
		},
		"split loses the attribute": func(r *monitoring.MetricRow) {
			r.Attribute = &monitoring.Attribute{Name: "cpu.mode"}
			r.SplitExprs = map[string]string{"hostmetrics": `max by (entity_id) (m{$sel})`}
		},
		"dimension without name": func(r *monitoring.MetricRow) { r.Dimension = "device" },
		"zero fill with range": func(r *monitoring.MetricRow) {
			r.ZeroFill = map[string]string{"hostmetrics": `0 * group by (entity_id) (rate(m{$sel}[$range]))`}
		},
	} {
		row := ok
		mutate(&row)
		if _, err := New([]monitoring.MetricRow{row}); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}

	if _, err := New([]monitoring.MetricRow{ok, ok}); err == nil {
		t.Error("duplicate name: New succeeded")
	}
	own := ok
	own.Schema = "hostmetrics"
	if _, err := New([]monitoring.MetricRow{ok, own}); err != nil {
		t.Errorf("a common field and a collector's own field may share a name: %v", err)
	}
}

// TestMIGSMUtilization runs nvml:mig.sm.utilization on series as the
// backend stores what the nvidia_gpu receiver writes as
// nvml.gpm.sm.utilization (nvml_gpm_sm_utilization_percent): a device point and MIG
// instance points with mig "true", two GPUs each with a GPU instance 1, one
// MIG instance NVML gave no MIG UUID.
func TestMIGSMUtilization(t *testing.T) {
	c, err := New(Builtin())
	if err != nil {
		t.Fatal(err)
	}
	row, err := c.Lookup(SchemaNVML + ":mig.sm.utilization")
	if err != nil {
		t.Fatal(err)
	}
	if row.Gap != "" || len(row.Exprs) != 1 || row.Exprs[CollectorNvidiaSMI] == "" {
		t.Fatalf("row = %+v, want one nvidia_smi expression and no gap", row)
	}
	st := promqltest.LoadedStorage(t, `load 30s
  {__name__="nvml_gpm_sm_utilization_percent",entity_id="a",uuid="GPU-1",index="0"} 90x10
  {__name__="nvml_gpm_sm_utilization_percent",entity_id="a",uuid="GPU-1",index="0",mig="true",gpu_instance_id="1",mig_uuid="MIG-11"} 40x10
  {__name__="nvml_gpm_sm_utilization_percent",entity_id="a",uuid="GPU-1",index="0",mig="true",gpu_instance_id="2",mig_uuid="MIG-12"} 12.5x10
  {__name__="nvml_gpm_sm_utilization_percent",entity_id="a",uuid="GPU-2",index="1",mig="true",gpu_instance_id="1"} 70x10
  {__name__="nvml_gpm_sm_utilization_percent",entity_id="b",uuid="GPU-3",index="0",mig="true",gpu_instance_id="1",mig_uuid="MIG-31"} 5x10
`)
	t.Cleanup(func() { _ = st.Close() })
	expr := strings.ReplaceAll(row.Exprs[CollectorNvidiaSMI], monitoring.PlaceholderSelector, `entity_id="a"`)
	engine := promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000)
	q, err := engine.NewInstantQuery(context.Background(), st, nil, expr, time.Unix(0, 0).Add(4*time.Minute))
	if err != nil {
		t.Fatalf("%v\n%s", err, expr)
	}
	res := q.Exec(context.Background())
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	vec, err := res.Vector()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, s := range vec {
		if s.Metric.Get("entity_id") != "a" {
			t.Errorf("series of another target: %v", s.Metric)
		}
		got[s.Metric.Get("uuid")+"/"+s.Metric.Get(row.Dimension)] = s.F
	}
	want := map[string]float64{"GPU-1/1": 0.4, "GPU-1/2": 0.125, "GPU-2/1": 0.7}
	if len(got) != len(want) {
		t.Fatalf("mig.sm.utilization = %v, want %v", got, want)
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("mig.sm.utilization[%s] = %v, want %v", k, g, v)
		}
	}
}

// storedNames are the series names a VictoriaMetrics 1.155 with
// -opentelemetry.usePrometheusNaming stored for the metrics of the ai-o11y
// collector's receivers (hostmetrics, nvidia_gpu with XID and GPM, amd_gpu)
// and top_process processor, as far as the catalog reads them. Most came from
// the collector itself; the GPU fields the GPUs at hand left empty (ECC,
// remapped rows, some temperatures) were sent over OTLP under the name and
// unit the receiver gives them.
var storedNames = []string{
	"amd_smi_clock_gfx_0_clk_MHz",
	"amd_smi_clock_mem_0_clk_MHz",
	"amd_smi_ecc_total_correctable_count",
	"amd_smi_ecc_total_deferred_count",
	"amd_smi_ecc_total_uncorrectable_count",
	"amd_smi_energy_total_energy_consumption_joules",
	"amd_smi_mem_usage_total_gtt_mebibytes",
	"amd_smi_mem_usage_total_vram_mebibytes",
	"amd_smi_mem_usage_used_gtt_mebibytes",
	"amd_smi_mem_usage_used_vram_mebibytes",
	"amd_smi_pcie_replay_count",
	"amd_smi_power_socket_power_watts",
	"amd_smi_temperature_edge_celsius",
	"amd_smi_temperature_hotspot_celsius",
	"amd_smi_temperature_mem_celsius",
	"amd_smi_usage_gfx_activity_percent",
	"amd_smi_usage_mm_activity_percent",
	"amd_smi_usage_umc_activity_percent",
	"nvidia_kmsg_xid_errors_total",
	"nvidia_smi_clocks_event_reasons_active",
	"nvidia_smi_clocks_mem_MHz",
	"nvidia_smi_clocks_sm_MHz",
	"nvidia_smi_ecc_errors_corrected_aggregate_total",
	"nvidia_smi_ecc_errors_corrected_volatile_total",
	"nvidia_smi_ecc_errors_uncorrected_aggregate_total",
	"nvidia_smi_ecc_errors_uncorrected_volatile_total",
	"nvidia_smi_fan_speed_percent",
	"nvidia_smi_memory_total_mebibytes",
	"nvidia_smi_memory_used_mebibytes",
	"nvidia_smi_power_draw_watts",
	"nvidia_smi_pstate",
	"nvidia_smi_remapped_rows_correctable",
	"nvidia_smi_remapped_rows_failure",
	"nvidia_smi_remapped_rows_pending",
	"nvidia_smi_remapped_rows_uncorrectable",
	"nvidia_smi_retired_pages_double_bit_count",
	"nvidia_smi_retired_pages_pending",
	"nvidia_smi_temperature_gpu_celsius",
	"nvidia_smi_temperature_memory_celsius",
	"nvidia_smi_utilization_decoder_percent",
	"nvidia_smi_utilization_encoder_percent",
	"nvidia_smi_utilization_gpu_percent",
	"nvidia_smi_utilization_memory_percent",
	"nvml_gpm_sm_utilization_percent",
	"process_cpu_utilization_ratio",
	"process_memory_utilization_ratio",
	"system_cpu_load_average_15m",
	"system_cpu_load_average_1m",
	"system_cpu_load_average_5m",
	"system_cpu_logical_count",
	"system_cpu_physical_count",
	"system_cpu_time_seconds_total",
	"system_disk_io_bytes_total",
	"system_disk_io_time_seconds_total",
	"system_disk_merged_total",
	"system_disk_operations_total",
	"system_disk_operation_time_seconds_total",
	"system_disk_pending_operations",
	"system_filesystem_inodes_usage",
	"system_filesystem_usage_bytes",
	"system_linux_memory_available_bytes",
	"system_memory_limit_bytes",
	"system_memory_linux_shared_bytes",
	"system_memory_usage_bytes",
	"system_memory_utilization_ratio",
	"system_network_dropped_total",
	"system_network_errors_total",
	"system_network_io_bytes_total",
	"system_network_packets_total",
	"system_paging_usage_bytes",
	"system_processes_count",
	"system_uptime_seconds",
}

// TestExprsReadStoredNames: every series name an expression selects is one the
// backend was seen to store, so a row does not read a name no backend has.
func TestExprsReadStoredNames(t *testing.T) {
	stored := map[string]bool{}
	for _, n := range storedNames {
		stored[n] = true
	}
	fill := strings.NewReplacer(monitoring.PlaceholderSelector, `entity_id="x"`, monitoring.PlaceholderRange, "1m")
	for _, r := range Builtin() {
		for _, exprs := range []map[string]string{r.Exprs, r.SplitExprs, r.ZeroFill} {
			for collector, expr := range exprs {
				node, err := parser.NewParser(parser.Options{}).ParseExpr(fill.Replace(expr))
				if err != nil {
					t.Fatalf("%s (%s): %v", r.Name, collector, err)
				}
				parser.Inspect(node, func(n parser.Node, _ []parser.Node) error {
					vs, ok := n.(*parser.VectorSelector)
					if !ok {
						return nil
					}
					name := vs.Name
					for _, m := range vs.LabelMatchers {
						if m.Name == model.MetricNameLabel && m.Type == labels.MatchEqual {
							name = m.Value
						}
					}
					if !stored[name] {
						t.Errorf("%s (%s) reads %q, which the backend does not store", r.Name, collector, name)
					}
					return nil
				})
			}
		}
	}
}
