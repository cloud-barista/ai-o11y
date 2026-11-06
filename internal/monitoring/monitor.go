// Package monitoring holds the four seams of the monitoring control plane: where
// targets come from, how a collector is configured for them, which metrics are
// collected, and which backend stores and answers queries. Samples never pass
// through ai-o11y; it only lists targets, renders collector configuration and
// queries the backend.
package monitoring

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LabelEntityID is the one label ai-o11y puts on every series of a target. Its
// value is the target's key in the management platform, used as is; ai-o11y
// issues no identifiers of its own. Which platform field the key holds is the
// adapter's TargetSource.KeyField.
const LabelEntityID = "entity_id"

// Target is one thing to monitor in the common model every management platform
// adapter fills, the way CB-Spider maps each CSP onto one VM model: fixed
// common fields, plus the platform's own attributes in Extra. Apart from
// EntityID, none of it is put on series; it is what the mapping layer selects
// targets by.
type Target struct {
	// EntityID is the target's key in the management platform, such as a
	// Tumblebug node's Uid. It does not change over the target's life; a
	// re-created target is a different target.
	EntityID string
	// Kind is the kind of target, such as "vm" or "host".
	Kind string
	// Name is the platform's display name for the target.
	Name     string
	Provider string // cloud provider, such as "aws"
	Region   string
	// OS is the operating system family, such as "linux" or "windows"; empty
	// when the platform does not say.
	OS string
	// GPUCount and GPUModel describe the GPUs the platform reports for the
	// target; zero means none.
	GPUCount int
	GPUModel string
	// Extra holds the platform's own attributes, such as a Tumblebug
	// namespace.
	Extra map[string]string
}

// Validate reports a target an adapter must not return.
func (t Target) Validate() error {
	if t.EntityID == "" {
		return fmt.Errorf("target %q has no key", t.Name)
	}
	if t.Kind == "" {
		return fmt.Errorf("target %q has no kind", t.EntityID)
	}
	return nil
}

// Scope narrows a TargetSource listing, for example to one namespace.
type Scope map[string]string

// TargetSource is the adapter to the management platform a deployment runs
// with, such as Tumblebug. A deployment has one; it lists every target the
// platform has registered. Infrastructure platforms (OpenStack, vCenter, a
// CSP) are not target sources: they are reached through a collection path.
type TargetSource interface {
	// KeyField names the platform field Target.EntityID holds, such as
	// "NodeInfo.Uid", so what entity_id refers to can be looked up in one
	// place.
	KeyField() string
	List(ctx context.Context, scope Scope) ([]Target, error)
	// Exists reports whether the platform still has t. It returns false only
	// when the platform answers that t is gone; any other failure is an
	// error, so a caller never drops a target it could not check.
	Exists(ctx context.Context, t Target) (bool, error)
	// Fetch returns the one target ref names in the platform's own terms,
	// such as CB-Tumblebug's nsId, infraId and nodeId. It is how a target is
	// described when only its platform names are known.
	Fetch(ctx context.Context, ref map[string]string) (Target, error)
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
	// Name is the collector key catalog rows use for its expressions, such
	// as "hostmetrics".
	Name() string
	Path() Path
	AppliesTo(t Target) bool
	Render(t Target, c MetricCatalog) (ConfigFragment, error)
	Health(ctx context.Context, t Target) (CollectorHealth, error)
}

// MetricKind says how an expression relates to time, which decides how a query
// aggregates it over a window.
type MetricKind string

const (
	// KindGauge is a value at an instant; a window aggregates it with the
	// *_over_time functions.
	KindGauge MetricKind = "gauge"
	// KindRate is already an average over a window: the expression takes
	// $range itself, typically rate() of a counter.
	KindRate MetricKind = "rate"
	// KindInfo carries its value in labels, with a constant sample value.
	KindInfo MetricKind = "info"
)

// Placeholders an expression holds for the query renderer to fill in.
const (
	// PlaceholderSelector stands for the label matchers that pick the
	// targets, entity_id included. It sits inside braces.
	PlaceholderSelector = "$sel"
	// PlaceholderRange stands for the window of a rate expression.
	PlaceholderRange = "$range"
)

// MetricRow is one field of the metric catalog, in the way Spider abstracts
// clouds: collectors keep their own names, and the catalog says how each field
// is computed from them.
//
//   - A common field (Schema empty) means the same thing whichever collector
//     provides it; its name and meaning are the OpenTelemetry semantic
//     conventions', such as system.cpu.utilization.
//   - A field of one collector's own (Schema set to that collector) has no
//     OpenTelemetry counterpart, such as hostmetrics' load average; it keeps
//     the collector's name, and Schema tells why its shape differs.
type MetricRow struct {
	// Name is the field's name: the OpenTelemetry metric name for a common
	// field, the collector's own name otherwise.
	Name string
	// Schema is empty for a common field and names the collector for a field
	// of its own.
	Schema string
	// Area groups rows for listing: cpu, memory, paging, disk, filesystem,
	// network, system, process, gpu.
	Area        string
	Unit        string
	Kind        MetricKind
	Description string
	// Exprs maps a collector (such as "hostmetrics") to the PromQL that
	// computes the field's default value from its series: one series per
	// entity_id and Dimension. For a field with an Attribute the default
	// value is the one screens usually show, which Description states, such
	// as CPU time not idle for system.cpu.utilization.
	Exprs map[string]string
	// Attribute, when set, splits the field, such as cpu.mode for
	// system.cpu.utilization. SplitExprs computes it with one series per
	// attribute value, in the label AttrLabel(Attribute.Name).
	Attribute  *Attribute
	SplitExprs map[string]string
	// Dimension is the label that splits the field within one target, such as
	// "device" or "mountpoint"; empty means one series per target.
	// DimensionName is its OpenTelemetry attribute name, such as
	// system.device.name, which answers carry.
	Dimension     string
	DimensionName string
	// Requires lists the capabilities a target needs to emit the metric, such
	// as "os:linux". A target without one reports the
	// metric as unsupported rather than missing.
	Requires []string
	// Enables lists the collector metrics that are off by default and must be
	// turned on in the collector configuration for Exprs to have data.
	Enables []string
	// ZeroFill maps a collector that leaves a series out when its value is
	// zero to an expression that is 0 for every target that collector is
	// reporting from. A query with FillZero takes the field as 0 where it is
	// missing and this is present.
	ZeroFill map[string]string
	// Gap says why no collector provides the field yet. A row with a gap has
	// no expressions; it stays in the catalog so the gap is visible.
	Gap string
}

// ID is how a query names the row: the name of a common field, and
// "schema:name" for a field of one collector's own.
func (r MetricRow) ID() string {
	if r.Schema == "" {
		return r.Name
	}
	return r.Schema + ":" + r.Name
}

// Attribute is an OpenTelemetry attribute that splits a field.
type Attribute struct {
	// Name is the OpenTelemetry attribute name, such as cpu.mode.
	Name string
	// Values are the values the attribute is known to take.
	Values []string
}

// AttrLabel is the series label an attribute travels in: its name with dots
// made underscores, the way OTLP names reach Prometheus.
func AttrLabel(name string) string { return strings.ReplaceAll(name, ".", "_") }

// MetricCatalog resolves catalog fields by MetricRow.ID.
type MetricCatalog interface {
	Lookup(id string) (MetricRow, error)
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

// Aggregations a query can ask for. Last only applies to a time window.
const (
	AggAvg   AggFn = "avg"
	AggMin   AggFn = "min"
	AggMax   AggFn = "max"
	AggSum   AggFn = "sum"
	AggCount AggFn = "count"
	AggLast  AggFn = "last"
)

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
// For a graph (Step > 0) the series are ranked over the whole graph range,
// Start to End, with the Window function, as the current screens rank them.
type TopK struct {
	N        int
	Desc     bool
	PerLabel string
}

// QuerySpec is the backend-neutral query template; a renderer per backend
// turns it into the backend's query language. The steps apply in field order:
// the window over time per series, Within inside each target, Across over the
// targets, then TopK.
type QuerySpec struct {
	Metric  string
	Targets TargetSet
	// Within collapses the metric's dimension (GPUs, devices) inside each
	// target; nil keeps one series per dimension value.
	Within *Reduce
	// Window aggregates each series over time; the zero value takes the
	// current value.
	Window Agg
	// Step is the graph's point interval; 0 means an instant query.
	Step time.Duration
	// Start and End are the graph's range when Step > 0. End alone is the
	// evaluation time of an instant query; a zero End means now.
	Start, End time.Time
	// Across combines the targets, keeping the GroupBy labels; nil GroupBy
	// gives one value for the whole set. Empty Across keeps one result per
	// target and allows no GroupBy.
	Across  AggFn
	GroupBy []string
	TopK    *TopK
	// FillZero takes a missing value as 0 for a metric whose collector leaves
	// zeros out (MetricRow.ZeroFill), for the targets that collector is
	// otherwise reporting from. Off, a zero shows as no data, the same as a
	// target that reports nothing.
	FillZero bool
	// Where keeps the series whose attribute (OpenTelemetry name) has the
	// value, such as cpu.mode=user; SplitBy gives one series per value of the
	// attribute. Either takes the field apart by its Attribute instead of
	// answering its default value.
	Where   map[string]string
	SplitBy []string
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
