package model

// MetricInfo is one metric of the catalog.
type MetricInfo struct {
	Name        string `json:"name" example:"host.cpu.utilization"`
	Area        string `json:"area" example:"cpu"`
	Unit        string `json:"unit" example:"ratio"`
	Kind        string `json:"kind" enums:"gauge,rate,info" example:"rate"`
	Description string `json:"description" example:"Share of CPU time not idle (current 100 - usage_idle)"`
	// Dimension splits the metric within a target, such as "device".
	Dimension string `json:"dimension,omitempty" example:""`
	// Requires lists what a target needs to have the metric, such as "os:linux".
	Requires []string `json:"requires,omitempty"`
	// Supported is false when no collector produces the metric as the current
	// field means it; Gap says why.
	Supported bool   `json:"supported" example:"true"`
	Gap       string `json:"gap,omitempty" example:""`
	// ZeroFill is true when a query can take a missing value as 0 (fillZero).
	ZeroFill bool `json:"zeroFill" example:"false"`
}

// MetricListResp is the catalog listing body.
type MetricListResp struct {
	Metrics []MetricInfo `json:"metrics"`
}
