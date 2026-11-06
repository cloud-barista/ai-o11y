package rest

import (
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/model"
	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/backend/promapi"
)

// RestGetAllMetrics godoc
// @ID GetAllMetrics
// @Summary List metrics
// @Description List the metric catalog: every common metric name a query can ask for, and the ones no collector produces yet with the reason.
// @Tags [Monitoring] Metrics
// @Produce json
// @Success 200 {object} model.MetricListResp
// @Router /metrics [get]
func (h *handler) RestGetAllMetrics(c echo.Context) error {
	rows := h.Catalog.Rows()
	resp := model.MetricListResp{Metrics: make([]model.MetricInfo, 0, len(rows))}
	for _, r := range rows {
		resp.Metrics = append(resp.Metrics, model.MetricInfo{
			Name: r.Name, Area: r.Area, Unit: r.Unit, Kind: string(r.Kind), Description: r.Description,
			Dimension: r.Dimension, Requires: r.Requires,
			Supported: r.Gap == "", Gap: r.Gap, ZeroFill: len(r.ZeroFill) > 0,
		})
	}
	return c.JSON(http.StatusOK, resp)
}

// RestPostQuery godoc
// @ID PostQuery
// @Summary Query a metric
// @Description Query one metric over targets named by id or selected by their attributes, as one value per series or as a graph from start to end. The answer carries the series and the targets they belong to.
// @Tags [Monitoring] Metrics
// @Accept json
// @Produce json
// @Param queryReq body model.QueryReq true "Query"
// @Success 200 {object} model.QueryResp
// @Failure 400 {object} model.SimpleMsg
// @Failure 404 {object} model.SimpleMsg "Unknown metric or target"
// @Failure 422 {object} model.SimpleMsg "Metric is not supported"
// @Failure 502 {object} model.SimpleMsg "The metrics backend did not answer"
// @Failure 503 {object} model.SimpleMsg
// @Router /query [post]
func (h *handler) RestPostQuery(c echo.Context) error {
	ctx := c.Request().Context()
	if h.Backend == nil {
		return c.JSON(http.StatusServiceUnavailable, msgNoBackend)
	}
	if h.Targets == nil {
		return c.JSON(http.StatusServiceUnavailable, msgNoStore)
	}
	var req model.QueryReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Malformed request body: check JSON syntax"})
	}
	row, err := h.Catalog.Lookup(req.Metric)
	if err != nil {
		return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Unknown metric: see GET /metrics"})
	}
	if row.Gap != "" {
		return c.JSON(http.StatusUnprocessableEntity, model.SimpleMsg{Message: "Metric is not supported: " + row.Gap})
	}
	q, msg := querySpec(req)
	if msg != "" {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: msg})
	}

	switch {
	case len(req.TargetIDs) > 0:
		for _, id := range req.TargetIDs {
			t, err := h.Targets.Lookup(ctx, id)
			if errors.Is(err, monitoring.ErrTargetNotFound) {
				return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Target is not registered: " + id})
			}
			if err != nil {
				log.Error().Err(err).Str("targetId", id).Msg("Failed to read target")
				return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target lookup failed"})
			}
			q.Targets = append(q.Targets, t)
		}
	case req.Selector != nil:
		if q.Targets, err = h.Targets.Select(ctx, req.Selector); err != nil {
			log.Error().Err(err).Msg("Failed to select targets")
			return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target selection failed"})
		}
	default:
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "targetIds or selector is required; selector {} means every target"})
	}
	if len(q.Targets) == 0 {
		return c.JSON(http.StatusOK, model.QueryResp{Series: []model.SeriesInfo{}, Targets: map[string]model.TargetInfo{}})
	}

	series, err := h.Backend.Query(ctx, q)
	if err != nil {
		var apiErr *promapi.APIError
		if errors.As(err, &apiErr) || errors.Is(err, promapi.ErrUnavailable) {
			log.Error().Err(err).Str("metric", q.Metric).Msg("Metrics backend query failed")
			return c.JSON(http.StatusBadGateway, model.SimpleMsg{Message: "Metrics backend query failed"})
		}
		log.Warn().Err(err).Str("metric", q.Metric).Msg("Query could not be built")
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Query could not be built: " + err.Error()})
	}
	return c.JSON(http.StatusOK, queryResp(series, q.Targets))
}

// querySpec turns a request into a QuerySpec without its targets, or returns
// what is wrong with it.
func querySpec(req model.QueryReq) (monitoring.QuerySpec, string) {
	q := monitoring.QuerySpec{
		Metric: req.Metric, Start: req.Start, End: req.End,
		Across: monitoring.AggFn(req.Across), GroupBy: req.GroupBy, FillZero: req.FillZero,
	}
	if req.Within != "" {
		q.Within = &monitoring.Reduce{Fn: monitoring.AggFn(req.Within)}
	}
	if req.WindowFn != "" || req.WindowRange != "" {
		d, err := time.ParseDuration(req.WindowRange)
		if err != nil || d <= 0 {
			return q, "windowRange must be a positive duration such as 5m"
		}
		q.Window = monitoring.Agg{Fn: monitoring.AggFn(req.WindowFn), Range: d}
	}
	if req.Step != "" {
		d, err := time.ParseDuration(req.Step)
		if err != nil || d <= 0 {
			return q, "step must be a positive duration such as 1m"
		}
		if req.Start.IsZero() || req.End.IsZero() || !req.End.After(req.Start) {
			return q, "a graph needs start before end"
		}
		q.Step = d
	}
	if req.TopK != nil {
		if req.TopK.N <= 0 {
			return q, "topK.n must be positive"
		}
		q.TopK = &monitoring.TopK{N: req.TopK.N, Desc: req.TopK.Desc, PerLabel: req.TopK.PerLabel}
	}
	return q, ""
}

func queryResp(series []monitoring.Series, targets monitoring.TargetSet) model.QueryResp {
	byID := make(map[string]monitoring.Target, len(targets))
	for _, t := range targets {
		byID[t.EntityID] = t
	}
	resp := model.QueryResp{Series: make([]model.SeriesInfo, 0, len(series)), Targets: map[string]model.TargetInfo{}}
	for _, s := range series {
		info := model.SeriesInfo{Labels: s.Labels, Points: make([]model.PointInfo, 0, len(s.Points))}
		for _, p := range s.Points {
			pt := model.PointInfo{T: p.T}
			if !math.IsNaN(p.V) && !math.IsInf(p.V, 0) {
				v := p.V
				pt.V = &v
			}
			info.Points = append(info.Points, pt)
		}
		resp.Series = append(resp.Series, info)
		if id := s.Labels[monitoring.LabelEntityID]; id != "" {
			if t, ok := byID[id]; ok {
				resp.Targets[id] = targetInfo(t)
			}
		}
	}
	return resp
}
