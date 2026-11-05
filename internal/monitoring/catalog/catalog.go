// Package catalog holds the metric catalog: the common metric names screens
// and queries use, and how each is computed from each collector's series.
package catalog

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// Catalog is a validated set of metric rows.
type Catalog struct {
	rows   []monitoring.MetricRow
	byName map[string]monitoring.MetricRow
}

var _ monitoring.MetricCatalog = (*Catalog)(nil)

// New validates rows and returns a Catalog over them. Every problem is
// reported, not just the first, so a bad catalog is fixed in one pass.
func New(rows []monitoring.MetricRow) (*Catalog, error) {
	c := &Catalog{byName: make(map[string]monitoring.MetricRow, len(rows))}
	var problems []error
	for _, r := range rows {
		if err := validate(r); err != nil {
			problems = append(problems, err)
			continue
		}
		if _, dup := c.byName[r.Name]; dup {
			problems = append(problems, fmt.Errorf("metric %q is defined twice", r.Name))
			continue
		}
		c.byName[r.Name] = r
		c.rows = append(c.rows, r)
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	slices.SortFunc(c.rows, func(a, b monitoring.MetricRow) int { return strings.Compare(a.Name, b.Name) })
	return c, nil
}

// Lookup returns the row for a common metric name.
func (c *Catalog) Lookup(name string) (monitoring.MetricRow, error) {
	r, ok := c.byName[name]
	if !ok {
		return monitoring.MetricRow{}, fmt.Errorf("unknown metric %q", name)
	}
	return r, nil
}

// Rows returns every row, sorted by name.
func (c *Catalog) Rows() []monitoring.MetricRow {
	return slices.Clone(c.rows)
}

func validate(r monitoring.MetricRow) error {
	if r.Name == "" {
		return errors.New("metric with an empty name")
	}
	switch r.Kind {
	case monitoring.KindGauge, monitoring.KindRate, monitoring.KindInfo:
	default:
		return fmt.Errorf("metric %q: unknown kind %q", r.Name, r.Kind)
	}
	if r.Gap != "" {
		if len(r.Exprs) > 0 {
			return fmt.Errorf("metric %q has both a gap and expressions", r.Name)
		}
		return nil
	}
	if len(r.Exprs) == 0 {
		return fmt.Errorf("metric %q has no expression and no gap", r.Name)
	}
	for collector, expr := range r.Exprs {
		if !strings.Contains(expr, monitoring.PlaceholderSelector) {
			return fmt.Errorf("metric %q (%s): expression has no %s", r.Name, collector, monitoring.PlaceholderSelector)
		}
		hasRange := strings.Contains(expr, monitoring.PlaceholderRange)
		if r.Kind == monitoring.KindRate && !hasRange {
			return fmt.Errorf("metric %q (%s): rate expression has no %s", r.Name, collector, monitoring.PlaceholderRange)
		}
		if r.Kind != monitoring.KindRate && hasRange {
			return fmt.Errorf("metric %q (%s): only rate expressions take %s", r.Name, collector, monitoring.PlaceholderRange)
		}
		if !strings.Contains(expr, "entity_id") {
			return fmt.Errorf("metric %q (%s): expression does not keep %s", r.Name, collector, monitoring.LabelEntityID)
		}
	}
	return nil
}
