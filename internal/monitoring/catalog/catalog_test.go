package catalog

import (
	"strings"
	"testing"

	"github.com/prometheus/prometheus/promql/parser"

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
