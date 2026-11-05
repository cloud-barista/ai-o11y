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
	for _, area := range []string{"cpu", "mem", "swap", "disk", "diskio", "net", "system", "processes", "procstat", "gpu"} {
		if areas[area] == 0 {
			t.Errorf("no rows for area %q", area)
		}
	}

	if _, err := c.Lookup("host.cpu.utilization"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := c.Lookup("host.cpu.nope"); err == nil {
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
		for collector, expr := range row.Exprs {
			if _, err := p.ParseExpr(r.Replace(expr)); err != nil {
				t.Errorf("%s (%s): %v\n%s", row.Name, collector, err, expr)
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
}
