package query

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/promqltest"
	"github.com/prometheus/prometheus/storage"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
)

// engineRunner evaluates queries with the Prometheus engine over test data.
type engineRunner struct {
	t       *testing.T
	engine  *promql.Engine
	storage storage.Queryable
	at      time.Time // instant queries that give no time
	calls   atomic.Int64
}

func (r *engineRunner) Instant(ctx context.Context, expr string, at time.Time) ([]monitoring.Series, error) {
	r.calls.Add(1)
	if at.IsZero() {
		at = r.at
	}
	q, err := r.engine.NewInstantQuery(ctx, r.storage, nil, expr, at)
	if err != nil {
		return nil, err
	}
	return r.result(q.Exec(ctx))
}

func (r *engineRunner) Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]monitoring.Series, error) {
	r.calls.Add(1)
	q, err := r.engine.NewRangeQuery(ctx, r.storage, nil, expr, start, end, step)
	if err != nil {
		return nil, err
	}
	return r.result(q.Exec(ctx))
}

func (r *engineRunner) result(res *promql.Result) ([]monitoring.Series, error) {
	if res.Err != nil {
		return nil, res.Err
	}
	labelsOf := func(m interface{ Map() map[string]string }) map[string]string { return m.Map() }
	var out []monitoring.Series
	switch v := res.Value.(type) {
	case promql.Vector:
		for _, s := range v {
			out = append(out, monitoring.Series{Labels: labelsOf(s.Metric), Points: []monitoring.Point{{T: time.UnixMilli(s.T).UTC(), V: s.F}}})
		}
	case promql.Matrix:
		for _, s := range v {
			ser := monitoring.Series{Labels: labelsOf(s.Metric)}
			for _, p := range s.Floats {
				ser.Points = append(ser.Points, monitoring.Point{T: time.UnixMilli(p.T).UTC(), V: p.F})
			}
			out = append(out, ser)
		}
	default:
		return nil, fmt.Errorf("unexpected result type %T", res.Value)
	}
	return out, nil
}

var fleet = []string{"a", "b", "c", "d", "e", "f", "g"}

// testData holds two hours of samples every 30s for seven VMs with distinct
// CPU busy shares, memory use and two GPUs each, so no ranking ties.
func testData() string {
	var b strings.Builder
	b.WriteString("load 30s\n")
	for i, id := range fleet {
		idle := 30 - 3*i // seconds idle per 30s step out of 30*2 CPUs
		fmt.Fprintf(&b, "  {__name__=\"system.cpu.time\",entity_id=%q,state=\"idle\",cpu=\"0\"} 0+%dx240\n", id, idle)
		fmt.Fprintf(&b, "  {__name__=\"system.cpu.time\",entity_id=%q,state=\"user\",cpu=\"0\"} 0+%dx240\n", id, 60-idle)
		fmt.Fprintf(&b, "  {__name__=\"system.memory.utilization\",entity_id=%q,state=\"used\"} %.3f+0.001x240\n", id, 0.1+0.07*float64(i))
		for g := range 2 {
			fmt.Fprintf(&b, "  nvidia_smi_utilization_gpu_ratio{entity_id=%q,uuid=\"gpu%d\"} %.3f+0.0005x240\n", id, g, 0.05+0.11*float64(i)+0.03*float64(g))
		}
	}
	return b.String()
}

func newRunner(t *testing.T) *engineRunner {
	t.Helper()
	st := promqltest.LoadedStorage(t, testData())
	t.Cleanup(func() { _ = st.Close() })
	return &engineRunner{
		t:       t,
		engine:  promqltest.NewTestEngine(t, false, 5*time.Minute, 50_000_000),
		storage: st,
		at:      graphEnd,
	}
}

// graphStart and graphEnd are the last hour of the test data.
var (
	graphEnd   = time.Unix(0, 0).UTC().Add(2 * time.Hour)
	graphStart = graphEnd.Add(-time.Hour)
)

// testRenderer renders over the built-in catalog plus a GPU-like row with a
// dimension, since the built-in GPU rows wait for the GPU receiver.
func testRenderer(t *testing.T) PromQL {
	t.Helper()
	rows := append(catalog.Builtin(), monitoring.MetricRow{
		Name: "test.gpu.utilization", Area: "gpu", Unit: "ratio", Kind: monitoring.KindGauge,
		Dimension: "uuid", DimensionName: "gpu.uuid",
		Exprs: map[string]string{catalog.CollectorNvidiaSMI: `max by (entity_id, uuid) (nvidia_smi_utilization_gpu_ratio{$sel})`},
	})
	c, err := catalog.New(rows)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return PromQL{Catalog: c}
}

func planners(t *testing.T) (whole, chunked Planner, chunkedRunner *engineRunner) {
	t.Helper()
	r := testRenderer(t)
	wholeRunner := newRunner(t)
	whole = Planner{Renderer: r, Limits: ChunkLimits{MaxIDs: 255, MaxConcurrent: 4}, Runner: wholeRunner, Now: wholeRunner.now}
	chunkedRunner = newRunner(t)
	chunked = Planner{Renderer: r, Limits: ChunkLimits{MaxIDs: 2, MaxConcurrent: 3}, Runner: chunkedRunner, Now: chunkedRunner.now}
	return whole, chunked, chunkedRunner
}

func sameSeries(a, b []monitoring.Series) error {
	if len(a) != len(b) {
		return fmt.Errorf("%d series vs %d", len(a), len(b))
	}
	for i := range a {
		if labelKey(a[i].Labels) != labelKey(b[i].Labels) {
			return fmt.Errorf("series %d: labels %v vs %v", i, a[i].Labels, b[i].Labels)
		}
		if len(a[i].Points) != len(b[i].Points) {
			return fmt.Errorf("series %v: %d points vs %d", a[i].Labels, len(a[i].Points), len(b[i].Points))
		}
		for j, p := range a[i].Points {
			q := b[i].Points[j]
			if !p.T.Equal(q.T) || math.Abs(p.V-q.V) > 1e-9*math.Max(1, math.Abs(p.V)) {
				return fmt.Errorf("series %v point %d: %v=%g vs %v=%g", a[i].Labels, j, p.T, p.V, q.T, q.V)
			}
		}
	}
	return nil
}

// TestPlannerChunkedEqualsWhole runs each query once over every target and
// once split into chunks of two, and expects the same answer.
func TestPlannerChunkedEqualsWhole(t *testing.T) {
	whole, chunked, chunkedRunner := planners(t)
	hour := monitoring.Agg{Fn: monitoring.AggAvg, Range: time.Hour}
	fiveMin := monitoring.Agg{Fn: monitoring.AggAvg, Range: 5 * time.Minute}
	gpuAvg := &monitoring.Reduce{Label: "uuid", Fn: monitoring.AggAvg}

	cases := map[string]monitoring.QuerySpec{
		"per target":            {Metric: "system.memory.utilization"},
		"per target range":      {Metric: "system.cpu.utilization", Window: fiveMin, Step: 5 * time.Minute},
		"across avg":            {Metric: "system.cpu.utilization", Window: hour, Across: monitoring.AggAvg},
		"across sum":            {Metric: "system.memory.utilization", Across: monitoring.AggSum},
		"across min":            {Metric: "system.memory.utilization", Across: monitoring.AggMin},
		"across max":            {Metric: "system.memory.utilization", Across: monitoring.AggMax},
		"across count":          {Metric: "test.gpu.utilization", Across: monitoring.AggCount},
		"across avg range":      {Metric: "test.gpu.utilization", Within: gpuAvg, Window: fiveMin, Step: 5 * time.Minute, Across: monitoring.AggAvg},
		"across avg group by":   {Metric: "test.gpu.utilization", Across: monitoring.AggAvg, GroupBy: []string{"uuid"}},
		"top-k":                 {Metric: "system.cpu.utilization", Window: hour, TopK: &monitoring.TopK{N: 3, Desc: true}},
		"bottom-k":              {Metric: "system.memory.utilization", TopK: &monitoring.TopK{N: 2}},
		"top-k per label":       {Metric: "test.gpu.utilization", TopK: &monitoring.TopK{N: 1, Desc: true, PerLabel: "entity_id"}},
		"top-k of groups":       {Metric: "test.gpu.utilization", Across: monitoring.AggMax, GroupBy: []string{"uuid"}, TopK: &monitoring.TopK{N: 1, Desc: true}},
		"two-stage range top-k": {Metric: "system.cpu.utilization", Window: fiveMin, Step: 5 * time.Minute, TopK: &monitoring.TopK{N: 3, Desc: true}},
		"two-stage gpu top-k":   {Metric: "test.gpu.utilization", Window: fiveMin, Step: 5 * time.Minute, TopK: &monitoring.TopK{N: 3, Desc: true}},
	}
	for name, q := range cases {
		q.Targets = targets(fleet...)
		if q.Step > 0 {
			q.Start, q.End = graphStart, graphEnd
		}
		before := chunkedRunner.calls.Load()
		want, err := whole.Query(context.Background(), q)
		if err != nil {
			t.Errorf("%s: whole: %v", name, err)
			continue
		}
		got, err := chunked.Query(context.Background(), q)
		if err != nil {
			t.Errorf("%s: chunked: %v", name, err)
			continue
		}
		if len(want) == 0 {
			t.Errorf("%s: no series; the case tests nothing", name)
		}
		if err := sameSeries(want, got); err != nil {
			t.Errorf("%s: chunked differs: %v", name, err)
		}
		if chunkedRunner.calls.Load()-before < 4 {
			t.Errorf("%s: chunked planner sent %d queries; seven targets in chunks of two need at least four", name, chunkedRunner.calls.Load()-before)
		}
	}
}

func TestPlannerTwoStagePicksTop(t *testing.T) {
	whole, _, _ := planners(t)
	got, err := whole.Query(context.Background(), monitoring.QuerySpec{
		Metric: "system.cpu.utilization", Targets: targets(fleet...),
		Window: monitoring.Agg{Fn: monitoring.AggAvg, Range: 5 * time.Minute}, Step: 5 * time.Minute,
		TopK:  &monitoring.TopK{N: 3, Desc: true},
		Start: graphStart, End: graphEnd,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// Busy share grows along the fleet, so the last three are the top.
	var ids []string
	for _, s := range got {
		ids = append(ids, s.Labels[monitoring.LabelEntityID])
		if len(s.Points) < 10 {
			t.Errorf("%v: %d points, want a graph over the hour", s.Labels, len(s.Points))
		}
	}
	if strings.Join(ids, ",") != "e,f,g" {
		t.Fatalf("picked %v, want e,f,g", ids)
	}
}

func TestPlannerRejects(t *testing.T) {
	whole, _, _ := planners(t)
	for name, q := range map[string]monitoring.QuerySpec{
		"two-stage across": {
			Metric: "system.cpu.utilization", Targets: targets(fleet...), Step: 5 * time.Minute,
			Start: graphStart, End: graphEnd,
			Window: monitoring.Agg{Fn: monitoring.AggAvg, Range: 5 * time.Minute},
			Across: monitoring.AggAvg, GroupBy: []string{"entity_id"}, TopK: &monitoring.TopK{N: 1},
		},
		"no targets":     {Metric: "system.memory.usage", Across: monitoring.AggAvg},
		"unknown across": {Metric: "system.memory.usage", Targets: targets(fleet...), Across: "median"},
	} {
		if _, err := whole.Query(context.Background(), q); err == nil {
			t.Errorf("%s: Query succeeded", name)
		}
	}
	if _, err := (Planner{Renderer: whole.Renderer}).Query(context.Background(), monitoring.QuerySpec{}); err == nil {
		t.Error("planner without a runner succeeded")
	}
}

func TestTopKOfRanksLikePromQL(t *testing.T) {
	at := time.Unix(0, 0)
	s := func(id string, v float64) monitoring.Series {
		return monitoring.Series{Labels: map[string]string{"entity_id": id}, Points: []monitoring.Point{{T: at, V: v}}}
	}
	in := []monitoring.Series{s("a", 1), s("b", math.NaN()), s("c", 3), s("d", 3), s("e", 2), {Labels: map[string]string{"entity_id": "x"}}}
	ids := func(series []monitoring.Series) string {
		var out []string
		for _, x := range series {
			out = append(out, x.Labels["entity_id"])
		}
		return strings.Join(out, ",")
	}
	if got := ids(topKOf(in, monitoring.TopK{N: 2, Desc: true})); got != "c,d" {
		t.Errorf("top 2 = %s, want c,d (tie broken by labels)", got)
	}
	if got := ids(topKOf(in, monitoring.TopK{N: 2})); got != "a,e" {
		t.Errorf("bottom 2 = %s, want a,e", got)
	}
	if got := ids(topKOf(in, monitoring.TopK{N: 5, Desc: true})); got != "a,b,c,d,e" {
		t.Errorf("top 5 = %s, want every series with a point, NaN included", got)
	}
}

func (r *engineRunner) now() time.Time { return r.at }

// shiftingRunner answers instant queries without a time at a later "now" on
// each call, the way chunks sent at different moments are evaluated by a real
// backend.
type shiftingRunner struct {
	*engineRunner
	calls int
}

func (r *shiftingRunner) Instant(ctx context.Context, expr string, at time.Time) ([]monitoring.Series, error) {
	if at.IsZero() {
		r.calls++
		at = r.at.Add(time.Duration(r.calls) * time.Second)
	}
	return r.engineRunner.Instant(ctx, expr, at)
}

// TestPlannerPinsInstantTime: an instant query without End is evaluated at
// one time across its chunks, so their points merge.
func TestPlannerPinsInstantTime(t *testing.T) {
	base := newRunner(t)
	p := Planner{Renderer: testRenderer(t), Limits: ChunkLimits{MaxIDs: 2, MaxConcurrent: 1},
		Runner: &shiftingRunner{engineRunner: base}, Now: base.now}
	for _, fn := range []monitoring.AggFn{monitoring.AggSum, monitoring.AggAvg} {
		got, err := p.Query(context.Background(), monitoring.QuerySpec{
			Metric: "system.memory.utilization", Targets: targets(fleet...), Across: fn,
		})
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		if len(got) != 1 || len(got[0].Points) != 1 {
			t.Errorf("%s across %d chunks = %+v, want one point", fn, len(fleet)/2+1, got)
		}
	}
}
