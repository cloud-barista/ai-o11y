package query

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/promql/parser"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// DefaultResolution is the step of the subquery a window needs when it cannot
// be folded into the expression. It matches the collection interval, so the
// subquery sees each sample about once.
const DefaultResolution = 30 * time.Second

// DefaultRateRange is the window a rate expression gets when the query does
// not set one: four collection intervals, so rate still has two samples when
// one scrape is missed.
const DefaultRateRange = 2 * time.Minute

// ErrTwoStage is returned by Render for a top-k that one query cannot
// express; see TwoStage. Pick the series with RenderTopK as an instant query
// first, then Render the query for the picked targets without TopK.
var ErrTwoStage = errors.New("top-k takes two stages: RenderTopK, then Render for the picked targets")

// TwoStage reports whether the top-k of q needs a query of its own: topk in a
// range query picks again at every step, so the series would change along the
// graph.
func TwoStage(q monitoring.QuerySpec) bool {
	return q.TopK != nil && q.Step > 0
}

// PromQL renders a QuerySpec into PromQL from the catalog expression of its
// metric.
type PromQL struct {
	Catalog monitoring.MetricCatalog
	// Resolution is the subquery step; zero means DefaultResolution.
	Resolution time.Duration
	// RateRange is the window of a rate expression the query does not set;
	// zero means DefaultRateRange.
	RateRange time.Duration
}

// Render returns the PromQL for q. Every target must have an entity id. The
// result is evaluated as an instant query when q.Step is zero and as a range
// query with that step otherwise; the step itself is a request parameter, not
// part of the expression.
func (r PromQL) Render(q monitoring.QuerySpec) (string, error) {
	expr, err := r.perTarget(q)
	if err != nil {
		return "", err
	}
	if q.TopK != nil {
		if TwoStage(q) {
			return "", ErrTwoStage
		}
		if expr, err = topK(expr, *q.TopK); err != nil {
			return "", err
		}
	}
	return checked(expr)
}

// RenderTopK returns the instant query of the first stage of a two-stage
// top-k: the series ranked over the whole graph range, Start to End, with the
// Window function (average when none), evaluated at End. Run it, narrow
// q.Targets to the keys it returns, and Render q with TopK cleared.
func (r PromQL) RenderTopK(q monitoring.QuerySpec) (string, error) {
	if q.TopK == nil {
		return "", errors.New("query has no top-k")
	}
	if q.Step > 0 {
		span := q.End.Sub(q.Start)
		if q.Start.IsZero() || q.End.IsZero() || span <= 0 {
			return "", fmt.Errorf("a graph top-k needs a range, got %v to %v", q.Start, q.End)
		}
		fn := q.Window.Fn
		if fn == "" {
			fn = monitoring.AggAvg
		}
		q.Window = monitoring.Agg{Fn: fn, Range: span}
	}
	expr, err := r.perTarget(q)
	if err != nil {
		return "", err
	}
	if expr, err = topK(expr, *q.TopK); err != nil {
		return "", err
	}
	return checked(expr)
}

// perTarget renders everything but the top-k: the catalog expression over the
// targets, the window, Within and Across.
func (r PromQL) perTarget(q monitoring.QuerySpec) (string, error) {
	if r.Catalog == nil {
		return "", errors.New("renderer has no catalog")
	}
	row, err := r.Catalog.Lookup(q.Metric)
	if err != nil {
		return "", err
	}
	if row.Gap != "" {
		return "", fmt.Errorf("metric %q is not collected yet: %s", row.Name, row.Gap)
	}
	if len(row.Exprs) != 1 {
		return "", fmt.Errorf("metric %q has %d expressions; the renderer cannot choose a collector yet", row.Name, len(row.Exprs))
	}
	var tmpl string
	for collector, e := range row.Exprs {
		tmpl = e
		if fill := row.ZeroFill[collector]; q.FillZero && fill != "" {
			tmpl = "(" + e + ") or (" + fill + ")"
		}
	}
	sel, err := selector(q.Targets)
	if err != nil {
		return "", err
	}

	expr, err := r.window(row, tmpl, sel, q.Window)
	if err != nil {
		return "", err
	}
	if q.Within != nil {
		if expr, err = within(row, expr, *q.Within); err != nil {
			return "", err
		}
	}
	return across(expr, q.Across, q.GroupBy)
}

// window fills the placeholders and applies the time window. A rate
// expression averaged over the window only needs its range set to the window,
// since rate is already the average over its range. Anything else goes through
// a subquery: the expression evaluated every Resolution over the window, then
// aggregated.
func (r PromQL) window(row monitoring.MetricRow, tmpl, sel string, w monitoring.Agg) (string, error) {
	rateRange := r.RateRange
	if rateRange <= 0 {
		rateRange = DefaultRateRange
	}
	fill := func(rng time.Duration) string {
		return strings.NewReplacer(
			monitoring.PlaceholderSelector, sel,
			monitoring.PlaceholderRange, duration(rng),
		).Replace(tmpl)
	}

	switch {
	case w.Fn == "" && w.Range == 0:
		return fill(rateRange), nil
	case w.Fn == "" || w.Range <= 0:
		return "", fmt.Errorf("window needs both a function and a positive range, got %q over %v", w.Fn, w.Range)
	}
	if row.Kind == monitoring.KindInfo {
		return "", fmt.Errorf("metric %q carries labels, not values, and takes no window", row.Name)
	}
	if row.Kind == monitoring.KindRate && w.Fn == monitoring.AggAvg {
		return fill(w.Range), nil
	}

	fn, ok := overTime[w.Fn]
	if !ok {
		return "", fmt.Errorf("unknown window function %q", w.Fn)
	}
	step := r.Resolution
	if step <= 0 {
		step = DefaultResolution
	}
	if w.Range < step {
		return "", fmt.Errorf("window %v is shorter than the resolution %v", w.Range, step)
	}
	return fmt.Sprintf("%s((%s)[%s:%s])", fn, fill(rateRange), duration(w.Range), duration(step)), nil
}

var overTime = map[monitoring.AggFn]string{
	monitoring.AggAvg:   "avg_over_time",
	monitoring.AggMin:   "min_over_time",
	monitoring.AggMax:   "max_over_time",
	monitoring.AggSum:   "sum_over_time",
	monitoring.AggCount: "count_over_time",
	monitoring.AggLast:  "last_over_time",
}

// aggregators are the functions that combine series, inside a target or
// across targets.
var aggregators = map[monitoring.AggFn]bool{
	monitoring.AggAvg: true, monitoring.AggMin: true, monitoring.AggMax: true,
	monitoring.AggSum: true, monitoring.AggCount: true,
}

// within collapses the row's dimension, keeping every other label the
// expression groups by.
func within(row monitoring.MetricRow, expr string, w monitoring.Reduce) (string, error) {
	if !aggregators[w.Fn] {
		return "", fmt.Errorf("unknown function %q to reduce within a target", w.Fn)
	}
	if row.Dimension == "" {
		return "", fmt.Errorf("metric %q has one series per target; there is nothing to reduce", row.Name)
	}
	if w.Label != row.Dimension {
		return "", fmt.Errorf("metric %q splits by %q, not %q", row.Name, row.Dimension, w.Label)
	}
	keep, err := groupingOf(expr)
	if err != nil {
		return "", err
	}
	keep = slices.DeleteFunc(keep, func(l string) bool { return l == w.Label })
	if !slices.Contains(keep, monitoring.LabelEntityID) {
		return "", fmt.Errorf("metric %q: expression does not group by %s", row.Name, monitoring.LabelEntityID)
	}
	return fmt.Sprintf("%s by (%s) (%s)", w.Fn, strings.Join(keep, ", "), expr), nil
}

// across combines the targets.
func across(expr string, fn monitoring.AggFn, groupBy []string) (string, error) {
	if fn == "" {
		if len(groupBy) > 0 {
			return "", errors.New("group by needs a function to combine the targets with")
		}
		return expr, nil
	}
	if !aggregators[fn] {
		return "", fmt.Errorf("unknown function %q to combine targets", fn)
	}
	if len(groupBy) == 0 {
		return fmt.Sprintf("%s(%s)", fn, expr), nil
	}
	for _, l := range groupBy {
		if err := monitoring.ValidateLabelName(l); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s by (%s) (%s)", fn, strings.Join(groupBy, ", "), expr), nil
}

func topK(expr string, k monitoring.TopK) (string, error) {
	if k.N <= 0 {
		return "", fmt.Errorf("top-k needs a positive count, got %d", k.N)
	}
	op := "bottomk"
	if k.Desc {
		op = "topk"
	}
	if k.PerLabel == "" {
		return fmt.Sprintf("%s(%d, %s)", op, k.N, expr), nil
	}
	if err := monitoring.ValidateLabelName(k.PerLabel); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s by (%s) (%d, %s)", op, k.PerLabel, k.N, expr), nil
}

// selector matches the targets by entity id. The ids are sorted and
// deduplicated, so one set of targets always renders the same text and a
// results cache keyed by query text keeps hitting.
func selector(targets monitoring.TargetSet) (string, error) {
	if len(targets) == 0 {
		return "", errors.New("query has no targets")
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.EntityID == "" {
			return "", fmt.Errorf("target %q has no key", t.Name)
		}
		ids = append(ids, t.EntityID)
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 1 {
		return monitoring.LabelEntityID + "=" + strconv.Quote(ids[0]), nil
	}
	for i, id := range ids {
		ids[i] = regexp.QuoteMeta(id)
	}
	return monitoring.LabelEntityID + "=~" + strconv.Quote(strings.Join(ids, "|")), nil
}

// groupingOf returns the labels the outermost aggregation of expr keeps,
// looking through parentheses, functions, subqueries and the left side of
// arithmetic.
func groupingOf(expr string) ([]string, error) {
	node, err := parser.NewParser(parser.Options{}).ParseExpr(expr)
	if err != nil {
		return nil, err
	}
	for node != nil {
		switch n := node.(type) {
		case *parser.AggregateExpr:
			if n.Without {
				return nil, fmt.Errorf("cannot tell the labels of %q: it aggregates without labels", expr)
			}
			return slices.Clone(n.Grouping), nil
		case *parser.ParenExpr:
			node = n.Expr
		case *parser.SubqueryExpr:
			node = n.Expr
		case *parser.Call:
			node = firstVector(n.Args)
		case *parser.BinaryExpr:
			if _, ok := n.LHS.(*parser.NumberLiteral); ok {
				node = n.RHS
			} else {
				node = n.LHS
			}
		default:
			node = nil
		}
	}
	return nil, fmt.Errorf("cannot tell the labels of %q", expr)
}

func firstVector(args parser.Expressions) parser.Expr {
	for _, a := range args {
		switch a.Type() {
		case parser.ValueTypeVector, parser.ValueTypeMatrix:
			return a
		}
	}
	return nil
}

// checked parses expr, so a renderer bug shows up here rather than as a
// backend error.
func checked(expr string) (string, error) {
	if _, err := parser.NewParser(parser.Options{}).ParseExpr(expr); err != nil {
		return "", fmt.Errorf("rendered invalid PromQL: %w\n%s", err, expr)
	}
	return expr, nil
}

func duration(d time.Duration) string { return model.Duration(d).String() }
