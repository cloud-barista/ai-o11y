package query

import (
	"context"
	"errors"
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
  {__name__="system.cpu.time",entity_id="a",cpu="0",state="idle"} 0+15x20
  {__name__="system.cpu.time",entity_id="a",cpu="0",state="user"} 0+9x20
  {__name__="system.cpu.time",entity_id="a",cpu="0",state="wait"} 0+6x20
  {__name__="system.memory.usage",entity_id="a",state="used"} 600x20
  {__name__="system.memory.usage",entity_id="a",state="buffered"} 100x20
  {__name__="system.memory.usage",entity_id="a",state="free"} 300x20
  {__name__="system.processes.count",entity_id="a",status="running"} 3x20
  {__name__="system.processes.count",entity_id="a",status="zombies"} 1x20
`)
	t.Cleanup(func() { _ = st.Close() })
	at := time.Unix(0, 0).UTC().Add(10 * time.Minute)
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st, at: at}
	p := Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner, Now: runner.now}

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
	split := values(monitoring.QuerySpec{Metric: cpu, SplitBy: []string{"cpu.mode"}}, "cpu.mode")
	if len(split) != 3 || !near(split["idle"], 0.5) || !near(split["user"], 0.3) || !near(split["iowait"], 0.2) {
		t.Errorf("CPU by cpu.mode = %v, want idle 0.5, user 0.3, iowait 0.2", split)
	}
	if got := values(monitoring.QuerySpec{Metric: cpu, Where: map[string]string{"cpu.mode": "iowait"}}, "cpu.mode"); len(got) != 1 || !near(got["iowait"], 0.2) {
		t.Errorf("CPU where cpu.mode=iowait = %v", got)
	}

	if got := values(monitoring.QuerySpec{Metric: "system.memory.usage"}, ""); !near(got[""], 600) {
		t.Errorf("memory default = %v, want used 600", got)
	}
	mem := values(monitoring.QuerySpec{Metric: "system.memory.usage", SplitBy: []string{"system.memory.state"}}, "system.memory.state")
	if !near(mem["buffers"], 100) || !near(mem["used"], 600) || !near(mem["free"], 300) {
		t.Errorf("memory by state = %v, want buffered renamed buffers", mem)
	}

	const procs = "system.process.count"
	if got := values(monitoring.QuerySpec{Metric: procs}, ""); !near(got[""], 4) {
		t.Errorf("processes default = %v, want all 4", got)
	}
	if got := values(monitoring.QuerySpec{Metric: procs, Where: map[string]string{"process.state": "defunct"}}, "process.state"); !near(got["defunct"], 1) {
		t.Errorf("defunct = %v, want zombies renamed defunct", got)
	}
	stopped := monitoring.QuerySpec{Metric: procs, Where: map[string]string{"process.state": "stopped"}}
	if got := values(stopped, "process.state"); len(got) != 0 {
		t.Errorf("stopped without fill = %v, want nothing", got)
	}
	stopped.FillZero = true
	if got := values(stopped, "process.state"); len(got) != 1 || got["stopped"] != 0 {
		t.Errorf("stopped with fill = %v, want stopped=0", got)
	}

	if _, err := p.Query(context.Background(), monitoring.QuerySpec{Metric: cpu, Targets: targets("a"),
		SplitBy: []string{"system.memory.state"}}); err == nil {
		t.Error("splitting by another field's attribute succeeded")
	}
}

// TestGPUFields runs the GPU fields on series as the nvidia_gpu receiver's
// nvidia-smi source writes them.
func TestGPUFields(t *testing.T) {
	st := promqltest.LoadedStorage(t, `load 30s
  {__name__="nvidia_smi.utilization.gpu",entity_id="a",uuid="GPU-1",index="0",name="T4"} 40x20
  {__name__="nvidia_smi.utilization.encoder",entity_id="a",uuid="GPU-1",index="0",name="T4"} 10x20
  {__name__="nvidia_smi.utilization.decoder",entity_id="a",uuid="GPU-1",index="0",name="T4"} 0x20
  {__name__="nvidia_smi.memory.used",entity_id="a",uuid="GPU-1",index="0",name="T4"} 1024x20
  {__name__="nvidia_smi.memory.total",entity_id="a",uuid="GPU-1",index="0",name="T4"} 4096x20
  {__name__="nvidia_smi.pstate",entity_id="a",uuid="GPU-1",index="0",name="T4"} 8x20
`)
	t.Cleanup(func() { _ = st.Close() })
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st,
		at: time.Unix(0, 0).UTC().Add(10 * time.Minute)}
	p := Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner, Now: runner.now}
	one := func(q monitoring.QuerySpec, label string) map[string]float64 {
		t.Helper()
		q.Targets = targets("a")
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", q.Metric, err)
		}
		out := map[string]float64{}
		for _, s := range got {
			if s.Labels["uuid"] != "GPU-1" {
				t.Errorf("%s: series without the GPU's uuid: %v", q.Metric, s.Labels)
			}
			out[s.Labels[label]] = s.Points[len(s.Points)-1].V
		}
		return out
	}
	if got := one(monitoring.QuerySpec{Metric: "hw.gpu.utilization"}, ""); got[""] != 0.4 {
		t.Errorf("utilization = %v, want 0.4", got)
	}
	tasks := one(monitoring.QuerySpec{Metric: "hw.gpu.utilization", SplitBy: []string{"hw.gpu.task"}}, "hw.gpu.task")
	if tasks["general"] != 0.4 || tasks["encoder"] != 0.1 || tasks["decoder"] != 0 || len(tasks) != 3 {
		t.Errorf("utilization by task = %v", tasks)
	}
	if got := one(monitoring.QuerySpec{Metric: "hw.gpu.memory.usage"}, ""); got[""] != 1024*1048576 {
		t.Errorf("memory usage = %v, want bytes", got)
	}
	if got := one(monitoring.QuerySpec{Metric: "hw.gpu.memory.utilization"}, ""); got[""] != 0.25 {
		t.Errorf("memory utilization = %v", got)
	}
	if got := one(monitoring.QuerySpec{Metric: "nvidia_smi:pstate"}, ""); got[""] != 8 {
		t.Errorf("pstate = %v", got)
	}
}

// TestWhereTakesKnownValuesOnly: a Where value goes into the expression, so
// only the values the attribute is known to take are accepted; "$sel" would
// expand into a selector and "$1" into a replacement reference.
func TestWhereTakesKnownValuesOnly(t *testing.T) {
	r := testRenderer(t)
	for _, v := range []string{"$sel", "$range", "user$1", "typo"} {
		_, err := r.Render(monitoring.QuerySpec{Metric: "system.cpu.utilization", Targets: targets("a"),
			Where: map[string]string{"cpu.mode": v}})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("where cpu.mode=%q: err = %v, want ErrInvalidQuery", v, err)
		}
	}
	if _, err := r.Render(monitoring.QuerySpec{Metric: "system.cpu.utilization", Targets: targets("a"),
		Where: map[string]string{"cpu.mode": "user"}}); err != nil {
		t.Errorf("known value: %v", err)
	}
	if _, err := r.Render(monitoring.QuerySpec{Metric: "system.nope", Targets: targets("a")}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("unknown metric: err = %v, want ErrInvalidQuery", err)
	}
}
