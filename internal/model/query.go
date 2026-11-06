package model

import "time"

// QueryReq asks for one metric over a set of targets. The targets are
// targetIds, or else every target matching selector; an empty selector ({})
// means every target. Durations are Go durations such as "5m" or "1h".
type QueryReq struct {
	// Metric and Schema name the catalog field (see GET /metrics); Schema is
	// empty for a common field.
	Metric    string            `json:"metric" example:"system.cpu.utilization"`
	Schema    string            `json:"schema,omitempty" example:""`
	TargetIDs []string          `json:"targetIds,omitempty"`
	Selector  map[string]string `json:"selector,omitempty"`
	// Where keeps the value of the field's attribute, such as
	// {"cpu.mode": "user"}; SplitBy gives one series per value of it, such as
	// ["cpu.mode"]. Without either the field answers its default value.
	Where   map[string]string `json:"where,omitempty"`
	SplitBy []string          `json:"splitBy,omitempty"`
	// Within collapses the field's dimension (devices, mount points) inside
	// each target with fn: avg, min, max, sum or count.
	Within string `json:"within,omitempty" example:""`
	// Window aggregates each series over the last windowRange with
	// windowFn: avg, min, max, sum, count or last.
	WindowFn    string `json:"windowFn,omitempty" example:""`
	WindowRange string `json:"windowRange,omitempty" example:""`
	// Step makes a graph from start to end; empty asks for one value at end
	// (now when end is empty).
	Step  string    `json:"step,omitempty" example:"1m"`
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
	// Across combines the targets with fn, keeping the groupBy attributes.
	Across  string   `json:"across,omitempty" example:""`
	GroupBy []string `json:"groupBy,omitempty"`
	TopK    *TopKReq `json:"topK,omitempty"`
	// FillZero takes a missing value as 0 for a metric whose collector does
	// not send zeros (MetricInfo.zeroFill), for targets otherwise reporting.
	FillZero bool `json:"fillZero,omitempty" example:"false"`
}

// TopKReq keeps the n highest (desc) or lowest series, per perLabel value
// when set.
type TopKReq struct {
	N        int    `json:"n" example:"5"`
	Desc     bool   `json:"desc" example:"true"`
	PerLabel string `json:"perLabel,omitempty" example:""`
}

// QueryResp is the answer to a query, with the targets its series belong to.
type QueryResp struct {
	Series []SeriesInfo `json:"series"`
	// Targets describes, by id, the targets the series carry an entity_id of.
	Targets map[string]TargetInfo `json:"targets"`
}

// SeriesInfo is one result series.
type SeriesInfo struct {
	// Labels are entity_id and the attributes the series is split by, by
	// their OpenTelemetry names.
	Labels map[string]string `json:"labels"`
	Points []PointInfo       `json:"points"`
}

// PointInfo is one sample. V is null when the value is not a number (NaN or
// infinite), which JSON cannot carry.
type PointInfo struct {
	T time.Time `json:"t"`
	V *float64  `json:"v"`
}
