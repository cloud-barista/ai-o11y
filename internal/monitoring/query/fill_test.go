package query

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql/promqltest"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// TestFillZero runs a zero-filled metric on the Prometheus engine: VM a has
// zombies, VM b reports but has none, so hostmetrics sends nothing for it,
// and VM c reports nothing at all.
func TestFillZero(t *testing.T) {
	st := promqltest.LoadedStorage(t, `load 30s
  system_cpu_time_seconds_total{entity_id="a",state="idle",cpu="0"} 0+30x20
  system_cpu_time_seconds_total{entity_id="b",state="idle",cpu="0"} 0+30x20
  system_processes_count{entity_id="a",status="zombies"} 2x20
`)
	t.Cleanup(func() { _ = st.Close() })
	at := time.Unix(0, 0).UTC().Add(10 * time.Minute)
	runner := &engineRunner{t: t, engine: promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000), storage: st, at: at}
	p := Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 1}, Runner: runner}

	values := func(q monitoring.QuerySpec) map[string]float64 {
		t.Helper()
		got, err := p.Query(context.Background(), q)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		out := map[string]float64{}
		for _, s := range got {
			out[s.Labels[monitoring.LabelEntityID]] = s.Points[len(s.Points)-1].V
		}
		return out
	}
	q := monitoring.QuerySpec{Metric: "host.processes.zombie", Targets: targets("a", "b", "c")}

	if got := values(q); len(got) != 1 || got["a"] != 2 {
		t.Errorf("without FillZero = %v, want only a=2", got)
	}
	q.FillZero = true
	if got := values(q); len(got) != 2 || got["a"] != 2 || got["b"] != 0 {
		t.Errorf("with FillZero = %v, want a=2 b=0 and nothing for c", got)
	}
	q.Window = monitoring.Agg{Fn: monitoring.AggMax, Range: 5 * time.Minute}
	q.Step, q.Start, q.End = time.Minute, at.Add(-5*time.Minute), at
	if got := values(q); len(got) != 2 || got["b"] != 0 {
		t.Errorf("windowed graph with FillZero = %v, want a and b=0", got)
	}

	// A metric without a zero fill is unchanged by the option.
	plain := monitoring.QuerySpec{Metric: "host.cpu.utilization", Targets: targets("a", "c"), FillZero: true}
	if got := values(plain); len(got) != 1 {
		t.Errorf("FillZero on a metric without zero fill = %v, want only a", got)
	}
}
