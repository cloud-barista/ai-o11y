// Package monitoring holds the four seams of the monitoring control plane: where
// targets come from, how a collector is configured for them, which metrics are
// collected, and which backend stores and answers queries. Samples never pass
// through ai-o11y; it only lists targets, renders collector configuration and
// queries the backend.
package monitoring

import (
	"context"
	"time"
)

// Target is one thing to monitor. Labels carry the identity labels; which keys
// are canonical is not fixed here, so providers fill what they know.
type Target struct {
	Kind         string
	ID           string
	Labels       map[string]string
	Capabilities map[string]string
}

// Scope narrows a TargetSource listing, for example to one namespace.
type Scope map[string]string

// TargetSource is the adapter to the management platform a deployment runs
// with, such as Tumblebug. A deployment has one; it lists every target the
// platform has registered. Infrastructure platforms (OpenStack, vCenter, a
// CSP) are not target sources: they are reached through a collection path.
type TargetSource interface {
	List(ctx context.Context, scope Scope) ([]Target, error)
}

// Path is how samples are collected.
type Path string

const (
	// PathAgent runs a collector inside the node.
	PathAgent Path = "agent"
	// PathAPIPoll polls a management API (vSphere, a CSP) from outside the node.
	PathAPIPoll Path = "api_poll"
)

// ConfigFragment is one piece of collector configuration; fragments for a node
// are merged into one collector config file.
type ConfigFragment struct {
	Receivers  map[string]any
	Processors map[string]any
	Pipelines  map[string]any
}

// CollectorHealth reports whether a collector for a target is delivering.
type CollectorHealth struct {
	Up     bool
	Reason string
}

// CollectorKind turns a target and the catalog into collector configuration.
type CollectorKind interface {
	Path() Path
	AppliesTo(t Target) bool
	Render(t Target, c MetricCatalog) (ConfigFragment, error)
	Health(ctx context.Context, t Target) (CollectorHealth, error)
}

// MetricRow is one catalog entry: a common metric name, the expression per
// backend or provider, and the capabilities a target needs to emit it.
type MetricRow struct {
	Name           string
	Exprs          map[string]string
	Requires       []string
	ExpectedSeries int
}

// MetricCatalog resolves common metric names.
type MetricCatalog interface {
	Lookup(name string) (MetricRow, error)
	Rows() []MetricRow
}

// Endpoint is where collectors write.
type Endpoint struct {
	URL      string
	Protocol string
	Headers  map[string]string
}

// BackendCaps lists what a backend supports, such as "promql" or "targetsAPI".
type BackendCaps map[string]bool

// TargetSet is the set of targets a query covers.
type TargetSet []Target

// AggFn names an aggregation.
type AggFn string

// Reduce collapses a sub-axis inside one target, such as GPUs on a host.
type Reduce struct {
	Label string
	Fn    AggFn
}

// Agg aggregates over a time range.
type Agg struct {
	Fn    AggFn
	Range time.Duration
}

// TopK keeps the N highest (or lowest) series after per-target aggregation.
type TopK struct {
	N        int
	Desc     bool
	PerLabel string
}

// QuerySpec is the backend-neutral query template; a renderer per backend
// turns it into the backend's query language.
type QuerySpec struct {
	Metric  string
	Targets TargetSet
	Within  *Reduce
	Window  Agg
	Step    time.Duration // 0 means an instant query
	GroupBy []string      // nil means the whole set
	TopK    *TopK
}

// Point is one sample.
type Point struct {
	T time.Time
	V float64
}

// Series is one labelled result series.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Backend stores samples and answers queries.
type Backend interface {
	WriteEndpoint() Endpoint
	Query(ctx context.Context, q QuerySpec) ([]Series, error)
	Series(ctx context.Context, match string) ([]map[string]string, error)
	Capabilities() BackendCaps
}
