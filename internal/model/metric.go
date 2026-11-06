package model

// MetricInfo is one field of the metric catalog.
//
// A common field (schema empty) means the same whichever collector provides
// it; its name and meaning are OpenTelemetry's. A field of one collector's own
// names that collector in schema and keeps the collector's name.
type MetricInfo struct {
	Name string `json:"name" example:"system.cpu.utilization"`
	// Schema is empty for a common field and names the collector otherwise.
	Schema      string `json:"schema,omitempty" example:""`
	Area        string `json:"area" example:"cpu"`
	Unit        string `json:"unit" example:"1"`
	Kind        string `json:"kind" enums:"gauge,rate,info" example:"rate"`
	Description string `json:"description" example:"Share of CPU time in each cpu.mode. Without one, the share not idle."`
	// Attribute splits the field: a query names it in where or splitBy.
	Attribute *AttributeInfo `json:"attribute,omitempty"`
	// Dimension is the attribute that splits the field within a target, such
	// as system.device.name; within collapses it.
	Dimension string `json:"dimension,omitempty" example:""`
	// Requires lists what a target needs to have the field, such as "os:linux".
	Requires []string `json:"requires,omitempty"`
	// Supported is false when no collector provides the field yet; Gap says why.
	Supported bool   `json:"supported" example:"true"`
	Gap       string `json:"gap,omitempty" example:""`
	// ZeroFill is true when a query can take a missing value as 0 (fillZero).
	ZeroFill bool `json:"zeroFill" example:"false"`
}

// AttributeInfo is an attribute that splits a field.
type AttributeInfo struct {
	Name   string   `json:"name" example:"cpu.mode"`
	Values []string `json:"values"`
}

// MetricListResp is the catalog listing body.
type MetricListResp struct {
	Metrics []MetricInfo `json:"metrics"`
}

// TargetMetricInfo is a catalog field as it stands for one target.
type TargetMetricInfo struct {
	Name      string         `json:"name" example:"system.cpu.utilization"`
	Schema    string         `json:"schema,omitempty" example:""`
	Area      string         `json:"area" example:"cpu"`
	Unit      string         `json:"unit" example:"1"`
	Attribute *AttributeInfo `json:"attribute,omitempty"`
	Dimension string         `json:"dimension,omitempty" example:""`
	// Supported is whether the target has the field; Reason says why not.
	Supported bool   `json:"supported" example:"true"`
	Reason    string `json:"reason,omitempty" example:""`
}

// TargetMetricListResp lists the catalog for one target: the common fields,
// the fields of the collectors it runs, and what it does not support.
type TargetMetricListResp struct {
	TargetID string             `json:"targetId" example:"d44ikdomt0tac4ejrmmg"`
	Metrics  []TargetMetricInfo `json:"metrics"`
}
