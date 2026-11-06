package query

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// Runner sends rendered PromQL to a backend.
type Runner interface {
	// Instant evaluates expr at at; a zero at means now.
	Instant(ctx context.Context, expr string, at time.Time) ([]monitoring.Series, error)
	// Range evaluates expr from start to end every step.
	Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]monitoring.Series, error)
}

// Planner answers a QuerySpec over any number of targets. It renders one
// query per chunk of targets, runs the chunks through RunChunks and merges
// their results, so the answer is the one a single query over every target
// would give:
//
//   - Without Across, each target's series come from exactly one chunk, so
//     the chunks' series are put together.
//   - Across sum, min and max apply again to the chunks' results; count
//     becomes the sum of the chunks' counts, and avg the sum of the chunks'
//     sums over the sum of their counts.
//   - Top-k keeps the top k of each chunk, then the top k of those.
//
// A query whose targets fit in one chunk is sent as is.
type Planner struct {
	Renderer PromQL
	Limits   ChunkLimits
	Runner   Runner
	// Now is the time an instant query without End is evaluated at; nil
	// means time.Now.
	Now func() time.Time
}

// Query runs q.
func (p Planner) Query(ctx context.Context, q monitoring.QuerySpec) ([]monitoring.Series, error) {
	if p.Runner == nil {
		return nil, errors.New("planner has no runner")
	}
	if q.Step == 0 && q.End.IsZero() {
		// One time for every chunk: left to the backend, each chunk would be
		// evaluated at its own "now", and points merged across chunks would
		// not line up.
		now := time.Now
		if p.Now != nil {
			now = p.Now
		}
		q.End = now().UTC()
	}
	if TwoStage(q) {
		return p.twoStage(ctx, q)
	}
	var (
		out []monitoring.Series
		err error
	)
	switch {
	case q.TopK != nil && q.Across == "":
		out, err = p.concat(ctx, q, p.run(q))
		if err == nil {
			out = topKOf(out, *q.TopK)
		}
	case q.TopK != nil:
		k := *q.TopK
		q.TopK = nil
		out, err = p.across(ctx, q)
		if err == nil {
			out = topKOf(out, k)
		}
	case q.Across == "":
		out, err = p.concat(ctx, q, p.run(q))
	default:
		out, err = p.across(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	return sorted(out), nil
}

// run sends a query the way q asks: instant, or range with q.Step.
func (p Planner) run(q monitoring.QuerySpec) func(context.Context, string) ([]monitoring.Series, error) {
	return func(ctx context.Context, expr string) ([]monitoring.Series, error) {
		if q.Step > 0 {
			return p.Runner.Range(ctx, expr, q.Start, q.End, q.Step)
		}
		return p.Runner.Instant(ctx, expr, q.End)
	}
}

// perChunk renders q once per chunk of its targets with render and runs each
// with exec.
func (p Planner) perChunk(ctx context.Context, q monitoring.QuerySpec,
	render func(monitoring.QuerySpec) (string, error),
	exec func(context.Context, string) ([]monitoring.Series, error),
) ([][]monitoring.Series, error) {
	byID := make(map[string]monitoring.Target, len(q.Targets))
	ids := make([]string, 0, len(q.Targets))
	for _, t := range q.Targets {
		if t.EntityID == "" {
			return nil, fmt.Errorf("target %q has no key", t.Name)
		}
		byID[t.EntityID] = t
		ids = append(ids, t.EntityID)
	}
	if len(ids) == 0 {
		return nil, errors.New("query has no targets")
	}
	return RunChunks(ctx, ids, p.Limits, func(ctx context.Context, chunk []string) ([]monitoring.Series, error) {
		cq := q
		cq.Targets = make(monitoring.TargetSet, len(chunk))
		for i, id := range chunk {
			cq.Targets[i] = byID[id]
		}
		expr, err := render(cq)
		if err != nil {
			return nil, err
		}
		return exec(ctx, expr)
	})
}

// concat runs q per chunk and puts the series together.
func (p Planner) concat(ctx context.Context, q monitoring.QuerySpec, exec func(context.Context, string) ([]monitoring.Series, error)) ([]monitoring.Series, error) {
	parts, err := p.perChunk(ctx, q, p.Renderer.Render, exec)
	if err != nil {
		return nil, err
	}
	return slices.Concat(parts...), nil
}

// combine merges the points of series with the same labels.
type combine func(a, b float64) float64

var combines = map[monitoring.AggFn]combine{
	monitoring.AggSum: func(a, b float64) float64 { return a + b },
	monitoring.AggMin: math.Min,
	monitoring.AggMax: math.Max,
}

// across runs a query that combines targets and merges the chunks' parts.
func (p Planner) across(ctx context.Context, q monitoring.QuerySpec) ([]monitoring.Series, error) {
	if p.single(q) {
		expr, err := p.Renderer.Render(q)
		if err != nil {
			return nil, err
		}
		return p.run(q)(ctx, expr)
	}
	switch q.Across {
	case monitoring.AggSum, monitoring.AggMin, monitoring.AggMax:
		return p.merged(ctx, q, q.Across, combines[q.Across])
	case monitoring.AggCount:
		return p.merged(ctx, q, monitoring.AggCount, combines[monitoring.AggSum])
	case monitoring.AggAvg:
		sums, err := p.merged(ctx, q, monitoring.AggSum, combines[monitoring.AggSum])
		if err != nil {
			return nil, err
		}
		counts, err := p.merged(ctx, q, monitoring.AggCount, combines[monitoring.AggSum])
		if err != nil {
			return nil, err
		}
		return divide(sums, counts), nil
	default:
		return nil, fmt.Errorf("unknown function %q to combine targets", q.Across)
	}
}

// single reports whether q's targets fit in one chunk, so it needs no
// splitting and its result no merging.
func (p Planner) single(q monitoring.QuerySpec) bool {
	ids := make([]string, 0, len(q.Targets))
	for _, t := range q.Targets {
		ids = append(ids, t.EntityID)
	}
	chunks, err := Chunks(ids, p.Limits.MaxIDs)
	return err == nil && len(chunks) <= 1 && !slices.Contains(ids, "")
}

// merged runs q with Across set to fn per chunk and merges the parts with c.
func (p Planner) merged(ctx context.Context, q monitoring.QuerySpec, fn monitoring.AggFn, c combine) ([]monitoring.Series, error) {
	q.Across = fn
	parts, err := p.perChunk(ctx, q, p.Renderer.Render, p.run(q))
	if err != nil {
		return nil, err
	}
	bySeries := map[string]*seriesAcc{}
	var order []string
	for _, part := range parts {
		for _, s := range part {
			key := labelKey(s.Labels)
			acc, ok := bySeries[key]
			if !ok {
				acc = &seriesAcc{labels: s.Labels, points: map[int64]float64{}}
				bySeries[key] = acc
				order = append(order, key)
			}
			for _, pt := range s.Points {
				ms := pt.T.UnixMilli()
				if v, ok := acc.points[ms]; ok {
					acc.points[ms] = c(v, pt.V)
				} else {
					acc.points[ms] = pt.V
				}
			}
		}
	}
	out := make([]monitoring.Series, 0, len(order))
	for _, key := range order {
		out = append(out, bySeries[key].series())
	}
	return out, nil
}

type seriesAcc struct {
	labels map[string]string
	points map[int64]float64
}

func (a *seriesAcc) series() monitoring.Series {
	s := monitoring.Series{Labels: a.labels, Points: make([]monitoring.Point, 0, len(a.points))}
	for ms, v := range a.points {
		s.Points = append(s.Points, monitoring.Point{T: time.UnixMilli(ms).UTC(), V: v})
	}
	slices.SortFunc(s.Points, func(a, b monitoring.Point) int { return a.T.Compare(b.T) })
	return s
}

// divide returns sums over counts, point by point, for the series and times
// both have.
func divide(sums, counts []monitoring.Series) []monitoring.Series {
	countOf := map[string]map[int64]float64{}
	for _, s := range counts {
		pts := map[int64]float64{}
		for _, pt := range s.Points {
			pts[pt.T.UnixMilli()] = pt.V
		}
		countOf[labelKey(s.Labels)] = pts
	}
	var out []monitoring.Series
	for _, s := range sums {
		counts := countOf[labelKey(s.Labels)]
		avg := monitoring.Series{Labels: s.Labels}
		for _, pt := range s.Points {
			if n, ok := counts[pt.T.UnixMilli()]; ok && n > 0 {
				avg.Points = append(avg.Points, monitoring.Point{T: pt.T, V: pt.V / n})
			}
		}
		if len(avg.Points) > 0 {
			out = append(out, avg)
		}
	}
	return out
}

// twoStage picks the top series with an instant query ranked over the whole
// graph range, then runs q for the targets they belong to and keeps the picked
// series.
func (p Planner) twoStage(ctx context.Context, q monitoring.QuerySpec) ([]monitoring.Series, error) {
	if q.Across != "" {
		return nil, errors.New("a two-stage top-k ranks targets and cannot rank combined groups")
	}
	k := *q.TopK
	pickAt := func(ctx context.Context, expr string) ([]monitoring.Series, error) {
		return p.Runner.Instant(ctx, expr, q.End)
	}
	parts, err := p.perChunk(ctx, q, p.Renderer.RenderTopK, pickAt)
	if err != nil {
		return nil, err
	}
	picked := topKOf(slices.Concat(parts...), k)
	if len(picked) == 0 {
		return nil, nil
	}

	keep := make(map[string]bool, len(picked))
	ids := map[string]bool{}
	for _, s := range picked {
		keep[labelKey(s.Labels)] = true
		ids[s.Labels[monitoring.LabelEntityID]] = true
	}
	q.TopK = nil
	q.Targets = slices.DeleteFunc(slices.Clone(q.Targets), func(t monitoring.Target) bool { return !ids[t.EntityID] })
	if len(q.Targets) == 0 {
		return nil, fmt.Errorf("top-k picked series without a known %s", monitoring.LabelEntityID)
	}
	all, err := p.concat(ctx, q, p.run(q))
	if err != nil {
		return nil, err
	}
	// A target can hold more series than were picked, such as the other GPUs
	// of a host when the top GPUs were asked for.
	all = slices.DeleteFunc(all, func(s monitoring.Series) bool { return !keep[labelKey(s.Labels)] })
	return sorted(all), nil
}

// topKOf keeps the k.N highest (Desc) or lowest series of each k.PerLabel
// group by their last point, the way PromQL topk ranks: NaN last, then label
// order to break ties so the pick is stable.
func topKOf(series []monitoring.Series, k monitoring.TopK) []monitoring.Series {
	groups := map[string][]monitoring.Series{}
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		g := ""
		if k.PerLabel != "" {
			g = s.Labels[k.PerLabel]
		}
		groups[g] = append(groups[g], s)
	}
	var out []monitoring.Series
	for _, g := range groups {
		slices.SortFunc(g, func(a, b monitoring.Series) int {
			va, vb := a.Points[len(a.Points)-1].V, b.Points[len(b.Points)-1].V
			if na, nb := math.IsNaN(va), math.IsNaN(vb); na || nb {
				if na && nb {
					return strings.Compare(labelKey(a.Labels), labelKey(b.Labels))
				}
				if na {
					return 1
				}
				return -1
			}
			c := cmp.Compare(va, vb)
			if k.Desc {
				c = -c
			}
			if c != 0 {
				return c
			}
			return strings.Compare(labelKey(a.Labels), labelKey(b.Labels))
		})
		out = append(out, g[:min(k.N, len(g))]...)
	}
	return sorted(out)
}

func sorted(series []monitoring.Series) []monitoring.Series {
	slices.SortFunc(series, func(a, b monitoring.Series) int {
		return strings.Compare(labelKey(a.Labels), labelKey(b.Labels))
	})
	return series
}

// labelKey is a canonical string for a label set.
func labelKey(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for n := range labels {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(0xfe)
		b.WriteString(labels[n])
		b.WriteByte(0xff)
	}
	return b.String()
}
