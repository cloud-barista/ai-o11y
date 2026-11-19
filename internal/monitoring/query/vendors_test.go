package query

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
)

// TestGPUFieldsOfBothVendors runs the common GPU fields, which nvidia-smi and
// amd-smi both provide, on series as the two receivers write them: target n
// has an NVIDIA GPU, m an AMD GPU, x one of each, and v an AMD Radeon PRO V710
// virtual function, whose amd-smi gives usage and power as N/A.
func TestGPUFieldsOfBothVendors(t *testing.T) {
	st := promqltest.LoadedStorage(t, `load 30s
  {__name__="nvidia_smi.utilization.gpu",entity_id="n",uuid="GPU-N1",index="0"} 70x20
  {__name__="nvidia_smi.utilization.encoder",entity_id="n",uuid="GPU-N1",index="0"} 10x20
  {__name__="nvidia_smi.utilization.decoder",entity_id="n",uuid="GPU-N1",index="0"} 0x20
  {__name__="nvidia_smi.memory.used",entity_id="n",uuid="GPU-N1",index="0"} 1024x20
  {__name__="nvidia_smi.memory.total",entity_id="n",uuid="GPU-N1",index="0"} 4096x20
  {__name__="nvidia_smi.power.draw",entity_id="n",uuid="GPU-N1",index="0"} 70x20
  {__name__="amd_smi.usage.gfx_activity",entity_id="m",uuid="amd-1",index="0"} 60x20
  {__name__="amd_smi.usage.mm_activity",entity_id="m",uuid="amd-1",index="0"} 30x20
  {__name__="amd_smi.mem_usage.used_vram",entity_id="m",uuid="amd-1",index="0"} 2048x20
  {__name__="amd_smi.mem_usage.total_vram",entity_id="m",uuid="amd-1",index="0"} 8192x20
  {__name__="amd_smi.power.socket_power",entity_id="m",uuid="amd-1",index="0"} 100x20
  {__name__="nvidia_smi.utilization.gpu",entity_id="x",uuid="GPU-X1",index="0"} 20x20
  {__name__="nvidia_smi.utilization.encoder",entity_id="x",uuid="GPU-X1",index="0"} 5x20
  {__name__="nvidia_smi.utilization.decoder",entity_id="x",uuid="GPU-X1",index="0"} 0x20
  {__name__="nvidia_smi.memory.used",entity_id="x",uuid="GPU-X1",index="0"} 512x20
  {__name__="nvidia_smi.memory.total",entity_id="x",uuid="GPU-X1",index="0"} 4096x20
  {__name__="nvidia_smi.power.draw",entity_id="x",uuid="GPU-X1",index="0"} 30x20
  {__name__="amd_smi.usage.gfx_activity",entity_id="x",uuid="amd-x",index="0"} 80x20
  {__name__="amd_smi.mem_usage.used_vram",entity_id="x",uuid="amd-x",index="0"} 1024x20
  {__name__="amd_smi.mem_usage.total_vram",entity_id="x",uuid="amd-x",index="0"} 4096x20
  {__name__="amd_smi.power.socket_power",entity_id="x",uuid="amd-x",index="0"} 50x20
  {__name__="amd_smi.mem_usage.used_vram",entity_id="v",uuid="amd-v",index="0"} 227x20
  {__name__="amd_smi.mem_usage.total_vram",entity_id="v",uuid="amd-v",index="0"} 4448x20
`)
	t.Cleanup(func() { _ = st.Close() })
	at := time.Unix(0, 0).UTC().Add(10 * time.Minute)
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st, at: at}
	planner := func(maxIDs int) Planner {
		return Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: maxIDs, MaxConcurrent: 1}, Runner: runner, Now: runner.now}
	}
	const mib = 1048576
	all := []string{"m", "n", "v", "x"}

	// answer runs q over ids, one query or one per target, and keys each
	// series by the labels named.
	answer := func(p Planner, q monitoring.QuerySpec, ids []string, keys ...string) map[string]float64 {
		t.Helper()
		q.Targets = targets(ids...)
		q = ToSeriesLabels(mustRow(t, p, q.Metric), q)
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("%s: %v", q.Metric, err)
		}
		out := map[string]float64{}
		for _, s := range got {
			k := ""
			for i, l := range keys {
				if i > 0 {
					k += "/"
				}
				k += s.Labels[l]
			}
			if _, dup := out[k]; dup {
				t.Errorf("%s: two series for %q", q.Metric, k)
			}
			out[k] = s.Points[len(s.Points)-1].V
		}
		return out
	}
	check := func(what string, got, want map[string]float64) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
		for k, w := range want {
			if g, ok := got[k]; !ok || math.Abs(g-w) > 1e-9 {
				t.Errorf("%s[%s] = %v, want %v (all: %v)", what, k, g, w, got)
			}
		}
	}

	for _, maxIDs := range []int{255, 1} {
		p := planner(maxIDs)
		// Per target, every GPU of either vendor, both GPUs of x included.
		perGPU := func(metric string) map[string]float64 {
			return answer(p, monitoring.QuerySpec{Metric: metric}, all, "entity_id", "uuid")
		}
		check("utilization", perGPU("hw.gpu.utilization"),
			map[string]float64{"n/GPU-N1": 0.7, "m/amd-1": 0.6, "x/GPU-X1": 0.2, "x/amd-x": 0.8})
		check("memory usage", perGPU("hw.gpu.memory.usage"),
			map[string]float64{"n/GPU-N1": 1024 * mib, "m/amd-1": 2048 * mib, "x/GPU-X1": 512 * mib, "x/amd-x": 1024 * mib, "v/amd-v": 227 * mib})
		check("memory limit", perGPU("hw.gpu.memory.limit"),
			map[string]float64{"n/GPU-N1": 4096 * mib, "m/amd-1": 8192 * mib, "x/GPU-X1": 4096 * mib, "x/amd-x": 4096 * mib, "v/amd-v": 4448 * mib})
		check("memory utilization", perGPU("hw.gpu.memory.utilization"),
			map[string]float64{"n/GPU-N1": 0.25, "m/amd-1": 0.25, "x/GPU-X1": 0.125, "x/amd-x": 0.25, "v/amd-v": 227.0 / 4448})
		check("power", perGPU("hw.power"),
			map[string]float64{"n/GPU-N1": 70, "m/amd-1": 100, "x/GPU-X1": 30, "x/amd-x": 50})

		// Across the targets, both vendors together.
		check("power sum", answer(p, monitoring.QuerySpec{Metric: "hw.power", Across: monitoring.AggSum}, all),
			map[string]float64{"": 250})
		check("utilization avg", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization", Across: monitoring.AggAvg}, all),
			map[string]float64{"": (0.7 + 0.6 + 0.2 + 0.8) / 4})
		check("memory count", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.memory.usage", Across: monitoring.AggCount}, all),
			map[string]float64{"": 5})
		check("power sum by target", answer(p, monitoring.QuerySpec{Metric: "hw.power", Across: monitoring.AggSum,
			GroupBy: []string{"entity_id"}}, all, "entity_id"),
			map[string]float64{"n": 70, "m": 100, "x": 80})

		// Within a target: x's two GPUs, one of each vendor, together.
		check("power within", answer(p, monitoring.QuerySpec{Metric: "hw.power",
			Within: &monitoring.Reduce{Label: "hw.id", Fn: monitoring.AggSum}}, all, "entity_id"),
			map[string]float64{"n": 70, "m": 100, "x": 80})
		check("utilization within avg", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			Within: &monitoring.Reduce{Label: "hw.id", Fn: monitoring.AggAvg}}, []string{"x"}, "entity_id"),
			map[string]float64{"x": 0.5})
		check("utilization within, avg across", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			Within: &monitoring.Reduce{Label: "hw.id", Fn: monitoring.AggMax}, Across: monitoring.AggAvg}, all),
			map[string]float64{"": (0.7 + 0.6 + 0.8) / 3})

		// Top-k ranks the GPUs of both vendors together.
		check("top 2", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			TopK: &monitoring.TopK{N: 2, Desc: true}}, all, "entity_id", "uuid"),
			map[string]float64{"x/amd-x": 0.8, "n/GPU-N1": 0.7})
		check("bottom 2", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			TopK: &monitoring.TopK{N: 2}}, all, "entity_id", "uuid"),
			map[string]float64{"x/GPU-X1": 0.2, "m/amd-1": 0.6})
		check("graph top 2", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			Window: monitoring.Agg{Fn: monitoring.AggAvg, Range: 5 * time.Minute}, Step: 5 * time.Minute,
			Start: at.Add(-5 * time.Minute), End: at, TopK: &monitoring.TopK{N: 2, Desc: true}}, all, "entity_id", "uuid"),
			map[string]float64{"x/amd-x": 0.8, "n/GPU-N1": 0.7})

		// amd-smi gives general work only.
		check("by task", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			SplitBy: []string{"hw.gpu.task"}}, []string{"m", "x"}, "uuid", "hw.gpu.task"),
			map[string]float64{"amd-1/general": 0.6, "GPU-X1/general": 0.2, "GPU-X1/encoder": 0.05, "GPU-X1/decoder": 0, "amd-x/general": 0.8})
		check("encoder", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			Where: map[string]string{"hw.gpu.task": "encoder"}}, all, "uuid"),
			map[string]float64{"GPU-N1": 0.1, "GPU-X1": 0.05})
		check("general within avg", answer(p, monitoring.QuerySpec{Metric: "hw.gpu.utilization",
			Where: map[string]string{"hw.gpu.task": "general"}, Within: &monitoring.Reduce{Label: "hw.id", Fn: monitoring.AggAvg}},
			[]string{"x"}, "entity_id", "hw.gpu.task"),
			map[string]float64{"x/general": 0.5})
	}
}

func mustRow(t *testing.T, p Planner, id string) monitoring.MetricRow {
	t.Helper()
	row, err := p.Renderer.Catalog.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// TestRenderJoinsCollectors: the collectors' expressions are joined in
// collector name order, each with its window, under Across.
func TestRenderJoinsCollectors(t *testing.T) {
	r := renderer(t)
	amd := `max by (entity_id, uuid) ({"amd_smi.power.socket_power", entity_id=~"a|b"})`
	nvidia := `max by (entity_id, uuid) ({"nvidia_smi.power.draw", entity_id=~"a|b"})`
	for i := 0; i < 10; i++ {
		got, err := r.Render(monitoring.QuerySpec{Metric: "hw.power", Targets: targets("b", "a"),
			Window: monitoring.Agg{Fn: monitoring.AggMax, Range: time.Hour}, Across: monitoring.AggSum})
		want := `sum((max_over_time((` + amd + `)[1h:30s])) or (max_over_time((` + nvidia + `)[1h:30s])))`
		if err != nil || got != want {
			t.Fatalf("Render = %s, %v\nwant %s", got, err, want)
		}
	}
}

// TestSplitLeavesOutACollectorWithoutIt: a collector with no split
// expression is left out of a split query and kept in the default one.
func TestSplitLeavesOutACollectorWithoutIt(t *testing.T) {
	gpu := func(name string) string { return `max by (entity_id, uuid) ({"` + name + `", $sel}) / 100` }
	row := monitoring.MetricRow{
		Name: "test.gpu.busy", Area: "gpu", Unit: "1", Kind: monitoring.KindGauge,
		Exprs: map[string]string{
			catalog.CollectorNvidiaSMI: gpu("nvidia_smi.utilization.gpu"),
			catalog.CollectorAMDSMI:    gpu("amd_smi.usage.gfx_activity"),
		},
		Attribute: &monitoring.Attribute{Name: "hw.gpu.task", Values: []string{"general"}},
		SplitExprs: map[string]string{catalog.CollectorNvidiaSMI: `label_replace(` + gpu("nvidia_smi.utilization.gpu") +
			`, "hw.gpu.task", "general", "", "")`},
		Dimension: "uuid", DimensionName: "hw.id",
	}
	mismatched := row
	mismatched.Name, mismatched.Attribute, mismatched.SplitExprs = "test.gpu.mismatched", nil, nil
	mismatched.Exprs = map[string]string{
		catalog.CollectorNvidiaSMI: gpu("nvidia_smi.utilization.gpu"),
		catalog.CollectorAMDSMI:    `max by (entity_id) ({"amd_smi.usage.gfx_activity", $sel}) / 100`,
	}
	c, err := catalog.New([]monitoring.MetricRow{row, mismatched})
	if err != nil {
		t.Fatal(err)
	}
	st := promqltest.LoadedStorage(t, `load 30s
  {__name__="nvidia_smi.utilization.gpu",entity_id="x",uuid="GPU-X1"} 20x20
  {__name__="amd_smi.usage.gfx_activity",entity_id="x",uuid="amd-x"} 80x20
`)
	t.Cleanup(func() { _ = st.Close() })
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st,
		at: time.Unix(0, 0).UTC().Add(10 * time.Minute)}
	p := Planner{Renderer: PromQL{Catalog: c}, Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner, Now: runner.now}
	byGPU := func(q monitoring.QuerySpec) map[string]float64 {
		t.Helper()
		q.Metric, q.Targets = "test.gpu.busy", targets("x")
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]float64{}
		for _, s := range got {
			out[s.Labels["uuid"]+"/"+s.Labels["hw.gpu.task"]] = s.Points[len(s.Points)-1].V
		}
		return out
	}
	if got := byGPU(monitoring.QuerySpec{}); len(got) != 2 || got["GPU-X1/"] != 0.2 || got["amd-x/"] != 0.8 {
		t.Errorf("default = %v, want both GPUs", got)
	}
	for _, q := range []monitoring.QuerySpec{
		{SplitBy: []string{"hw.gpu.task"}},
		{Where: map[string]string{"hw.gpu.task": "general"}},
	} {
		if got := byGPU(q); len(got) != 1 || got["GPU-X1/general"] != 0.2 {
			t.Errorf("%+v = %v, want the NVIDIA GPU alone", q, got)
		}
	}

	if got, err := p.Renderer.Render(monitoring.QuerySpec{Metric: "test.gpu.mismatched", Targets: targets("x"),
		Within: &monitoring.Reduce{Label: "uuid", Fn: monitoring.AggSum}}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("within over collectors grouping by different labels = %s, %v; want ErrInvalidQuery", got, err)
	}
}
