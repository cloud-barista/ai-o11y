package query

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// TestAttributeSplit runs fields split by an attribute on the Prometheus
// engine. VM a spends its CPU time 50% idle, 30% user and 20% waiting on I/O,
// which hostmetrics calls state="wait" and OpenTelemetry cpu.mode="iowait".
func TestAttributeSplit(t *testing.T) {
	st := promqltest.LoadedStorage(t, `load 30s
  system_cpu_time_seconds_total{entity_id="a",cpu="0",state="idle"} 0+15x20
  system_cpu_time_seconds_total{entity_id="a",cpu="0",state="user"} 0+9x20
  system_cpu_time_seconds_total{entity_id="a",cpu="0",state="wait"} 0+6x20
  system_memory_usage_bytes{entity_id="a",state="used"} 600x20
  system_memory_usage_bytes{entity_id="a",state="buffered"} 100x20
  system_memory_usage_bytes{entity_id="a",state="free"} 300x20
  system_processes_count{entity_id="a",status="running"} 3x20
  system_processes_count{entity_id="a",status="zombies"} 1x20
`)
	t.Cleanup(func() { _ = st.Close() })
	at := time.Unix(0, 0).UTC().Add(10 * time.Minute)
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st, at: at}
	p := Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner}

	// values answers q as label value of attr -> value; "" for no attr.
	values := func(q monitoring.QuerySpec, label string) map[string]float64 {
		t.Helper()
		q.Targets = targets("a")
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", q.Metric, err)
		}
		out := map[string]float64{}
		for _, s := range got {
			out[s.Labels[label]] = s.Points[len(s.Points)-1].V
		}
		return out
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	const cpu = "system.cpu.utilization"

	if got := values(monitoring.QuerySpec{Metric: cpu}, ""); len(got) != 1 || !near(got[""], 0.5) {
		t.Errorf("CPU default = %v, want the share not idle, 0.5", got)
	}
	split := values(monitoring.QuerySpec{Metric: cpu, SplitBy: []string{"cpu.mode"}}, "cpu_mode")
	if len(split) != 3 || !near(split["idle"], 0.5) || !near(split["user"], 0.3) || !near(split["iowait"], 0.2) {
		t.Errorf("CPU by cpu.mode = %v, want idle 0.5, user 0.3, iowait 0.2", split)
	}
	if got := values(monitoring.QuerySpec{Metric: cpu, Where: map[string]string{"cpu.mode": "iowait"}}, "cpu_mode"); len(got) != 1 || !near(got["iowait"], 0.2) {
		t.Errorf("CPU where cpu.mode=iowait = %v", got)
	}

	if got := values(monitoring.QuerySpec{Metric: "system.memory.usage"}, ""); !near(got[""], 600) {
		t.Errorf("memory default = %v, want used 600", got)
	}
	mem := values(monitoring.QuerySpec{Metric: "system.memory.usage", SplitBy: []string{"system.memory.state"}}, "system_memory_state")
	if !near(mem["buffers"], 100) || !near(mem["used"], 600) || !near(mem["free"], 300) {
		t.Errorf("memory by state = %v, want buffered renamed buffers", mem)
	}

	const procs = "system.process.count"
	if got := values(monitoring.QuerySpec{Metric: procs}, ""); !near(got[""], 4) {
		t.Errorf("processes default = %v, want all 4", got)
	}
	if got := values(monitoring.QuerySpec{Metric: procs, Where: map[string]string{"process.state": "defunct"}}, "process_state"); !near(got["defunct"], 1) {
		t.Errorf("defunct = %v, want zombies renamed defunct", got)
	}
	stopped := monitoring.QuerySpec{Metric: procs, Where: map[string]string{"process.state": "stopped"}}
	if got := values(stopped, "process_state"); len(got) != 0 {
		t.Errorf("stopped without fill = %v, want nothing", got)
	}
	stopped.FillZero = true
	if got := values(stopped, "process_state"); len(got) != 1 || got["stopped"] != 0 {
		t.Errorf("stopped with fill = %v, want stopped=0", got)
	}

	if _, err := p.Query(context.Background(), monitoring.QuerySpec{Metric: cpu, Targets: targets("a"),
		SplitBy: []string{"system.memory.state"}}); err == nil {
		t.Error("splitting by another field's attribute succeeded")
	}
}
