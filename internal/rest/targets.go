package rest

import (
	"errors"
	"maps"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"

	"github.com/cloud-barista/ai-o11y/internal/model"
	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/collector"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/mapping"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/source/tumblebug"
)

var (
	msgNoStore   = model.SimpleMsg{Message: "Target store is not configured"}
	msgNoSource  = model.SimpleMsg{Message: "Management platform is not configured"}
	msgNoBackend = model.SimpleMsg{Message: "Metrics backend is not configured"}
)

// RestPostTarget godoc
// @ID PostTarget
// @Summary Register a target
// @Description Register a target when its agent is installed. The body names the node as ai-app names a deployment target and carries its description from CB-Tumblebug; without uid the description is read from CB-Tumblebug. Registering a target again replaces its description.
// @Tags [Monitoring] Targets
// @Accept json
// @Produce json
// @Param targetReq body model.TargetReq true "Target to register"
// @Success 200 {object} model.TargetInfo
// @Failure 400 {object} model.SimpleMsg
// @Failure 404 {object} model.SimpleMsg "The node is not in CB-Tumblebug"
// @Failure 422 {object} model.SimpleMsg "Kind is not supported"
// @Failure 502 {object} model.SimpleMsg "CB-Tumblebug did not answer"
// @Failure 503 {object} model.SimpleMsg
// @Router /targets [post]
func (h *handler) RestPostTarget(c echo.Context) error {
	ctx := c.Request().Context()
	if h.Targets == nil {
		return c.JSON(http.StatusServiceUnavailable, msgNoStore)
	}
	var req model.TargetReq
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Malformed request body: check JSON syntax"})
	}
	switch req.Kind {
	case model.TargetKindNode:
	case model.TargetKindK8s:
		return c.JSON(http.StatusUnprocessableEntity, model.SimpleMsg{Message: "Kubernetes targets are not supported yet"})
	default:
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "kind is required: node"})
	}
	if req.NsID == "" || req.InfraID == "" || req.NodeID == "" {
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "nsId, infraId and nodeId are required"})
	}
	switch req.OS {
	case "", "linux", "windows":
	default:
		return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "os must be linux, windows or empty"})
	}

	var t monitoring.Target
	if req.UID != "" {
		t = targetFromReq(req)
	} else {
		if h.Source == nil {
			return c.JSON(http.StatusServiceUnavailable, model.SimpleMsg{
				Message: "Management platform is not configured: give the target's uid and description"})
		}
		var err error
		t, err = h.Source.Fetch(ctx, map[string]string{
			tumblebug.ExtraNamespace: req.NsID, tumblebug.ExtraInfra: req.InfraID, tumblebug.ExtraNode: req.NodeID,
		})
		switch {
		case errors.Is(err, monitoring.ErrTargetNotFound):
			return c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Node is not in CB-Tumblebug"})
		case err != nil:
			log.Error().Err(err).Str("nsId", req.NsID).Str("infraId", req.InfraID).Str("nodeId", req.NodeID).
				Msg("Failed to read the node from CB-Tumblebug")
			return c.JSON(http.StatusBadGateway, model.SimpleMsg{Message: "Could not read the node from CB-Tumblebug"})
		}
		// The platform's names stay what the platform reported: a sync looks
		// the target up by them.
		for k, v := range req.Attributes {
			if _, platform := t.Extra[k]; !platform {
				t.Extra[k] = v
			}
		}
	}
	if err := h.Targets.Save(ctx, t); err != nil {
		if errors.Is(err, mapping.ErrInvalidTarget) {
			log.Warn().Err(err).Str("targetId", t.EntityID).Msg("Target refused")
			return c.JSON(http.StatusBadRequest, model.SimpleMsg{Message: "Target does not fit: uid up to 64 characters, names and attribute values up to 255"})
		}
		log.Error().Err(err).Str("targetId", t.EntityID).Msg("Failed to save target")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target registration failed"})
	}
	return c.JSON(http.StatusOK, targetInfo(t))
}

// targetFromReq is the target a request describes in full.
func targetFromReq(req model.TargetReq) monitoring.Target {
	extra := maps.Clone(req.Attributes)
	if extra == nil {
		extra = map[string]string{}
	}
	extra[tumblebug.ExtraNamespace] = req.NsID
	extra[tumblebug.ExtraInfra] = req.InfraID
	extra[tumblebug.ExtraNode] = req.NodeID
	return monitoring.Target{
		EntityID: req.UID, Kind: tumblebug.KindVM, Name: req.Name,
		Provider: req.Provider, Region: req.Region, OS: req.OS,
		GPUCount: req.GPUCount, GPUModel: req.GPUModel, Extra: extra,
	}
}

func targetInfo(t monitoring.Target) model.TargetInfo {
	attrs := maps.Clone(t.Extra)
	if attrs == nil {
		attrs = map[string]string{}
	}
	return model.TargetInfo{
		ID: t.EntityID, Kind: t.Kind, Name: t.Name, Provider: t.Provider, Region: t.Region,
		OS: t.OS, GPUCount: t.GPUCount, GPUModel: t.GPUModel, Attributes: attrs,
	}
}

// RestGetAllTargets godoc
// @ID GetAllTargets
// @Summary List targets
// @Description List the registered targets. Every query parameter is a condition on an attribute, all of which must match exactly: a common field (kind, name, provider, region, os, gpuCount, gpuModel) or a platform attribute such as nsId, infraId or nodeId.
// @Tags [Monitoring] Targets
// @Produce json
// @Param nsId query string false "CB-Tumblebug namespace"
// @Param infraId query string false "CB-Tumblebug Infra"
// @Success 200 {object} model.TargetListResp
// @Failure 500 {object} model.SimpleMsg
// @Failure 503 {object} model.SimpleMsg
// @Router /targets [get]
func (h *handler) RestGetAllTargets(c echo.Context) error {
	ctx := c.Request().Context()
	if h.Targets == nil {
		return c.JSON(http.StatusServiceUnavailable, msgNoStore)
	}
	sel := monitoring.Selector{}
	for name, values := range c.QueryParams() {
		if len(values) > 0 {
			sel[name] = values[len(values)-1]
		}
	}
	targets, err := h.Targets.Select(ctx, sel)
	if err != nil {
		log.Error().Err(err).Msg("Failed to list targets")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target listing failed"})
	}
	resp := model.TargetListResp{Targets: make([]model.TargetInfo, 0, len(targets))}
	for _, t := range targets {
		resp.Targets = append(resp.Targets, targetInfo(t))
	}
	return c.JSON(http.StatusOK, resp)
}

// lookup answers the target of the targetId path parameter, or writes the
// error response and returns ok false.
func (h *handler) lookup(c echo.Context) (monitoring.Target, bool, error) {
	if h.Targets == nil {
		return monitoring.Target{}, false, c.JSON(http.StatusServiceUnavailable, msgNoStore)
	}
	id := c.Param("targetId")
	t, err := h.Targets.Lookup(c.Request().Context(), id)
	switch {
	case errors.Is(err, monitoring.ErrTargetNotFound):
		return t, false, c.JSON(http.StatusNotFound, model.SimpleMsg{Message: "Target is not registered"})
	case err != nil:
		log.Error().Err(err).Str("targetId", id).Msg("Failed to read target")
		return t, false, c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target lookup failed"})
	}
	return t, true, nil
}

// RestGetTarget godoc
// @ID GetTarget
// @Summary Get a target
// @Description Get one registered target by its id.
// @Tags [Monitoring] Targets
// @Produce json
// @Param targetId path string true "Target id (CB-Tumblebug node Uid)"
// @Success 200 {object} model.TargetInfo
// @Failure 404 {object} model.SimpleMsg
// @Failure 500 {object} model.SimpleMsg
// @Failure 503 {object} model.SimpleMsg
// @Router /targets/{targetId} [get]
func (h *handler) RestGetTarget(c echo.Context) error {
	t, ok, err := h.lookup(c)
	if !ok {
		return err
	}
	return c.JSON(http.StatusOK, targetInfo(t))
}

// RestDeleteTarget godoc
// @ID DeleteTarget
// @Summary Delete a target
// @Description Delete a registered target, as when its agent is removed. Its stored series stay in the backend.
// @Tags [Monitoring] Targets
// @Produce json
// @Param targetId path string true "Target id (CB-Tumblebug node Uid)"
// @Success 200 {object} model.SimpleMsg
// @Failure 404 {object} model.SimpleMsg
// @Failure 500 {object} model.SimpleMsg
// @Failure 503 {object} model.SimpleMsg
// @Router /targets/{targetId} [delete]
func (h *handler) RestDeleteTarget(c echo.Context) error {
	t, ok, err := h.lookup(c)
	if !ok {
		return err
	}
	if err := h.Targets.Delete(c.Request().Context(), t.EntityID); err != nil {
		log.Error().Err(err).Str("targetId", t.EntityID).Msg("Failed to delete target")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target deletion failed"})
	}
	return c.JSON(http.StatusOK, model.SimpleMsg{Message: "Target deleted"})
}

// RestGetTargetCollectorConfig godoc
// @ID GetTargetCollectorConfig
// @Summary Get a target's collector configuration
// @Description Render the OpenTelemetry Collector configuration the target's agent runs: the collectors that apply to it, its entity_id stamped on every series, and the metrics backend to send them to.
// @Tags [Monitoring] Targets
// @Produce application/yaml
// @Param targetId path string true "Target id (CB-Tumblebug node Uid)"
// @Success 200 {string} string "Collector configuration (YAML)"
// @Failure 404 {object} model.SimpleMsg
// @Failure 500 {object} model.SimpleMsg
// @Failure 503 {object} model.SimpleMsg
// @Router /targets/{targetId}/collectorConfig [get]
func (h *handler) RestGetTargetCollectorConfig(c echo.Context) error {
	if h.Backend == nil || h.Backend.WriteEndpoint().URL == "" {
		return c.JSON(http.StatusServiceUnavailable, model.SimpleMsg{Message: "Metrics backend write address is not configured"})
	}
	t, ok, err := h.lookup(c)
	if !ok {
		return err
	}
	out, err := collector.Render(t, h.Collectors, h.Catalog, h.Backend.WriteEndpoint())
	if err != nil {
		log.Error().Err(err).Str("targetId", t.EntityID).Msg("Failed to render collector configuration")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Collector configuration could not be built"})
	}
	return c.Blob(http.StatusOK, "application/yaml", out)
}

// RestPostTargetSync godoc
// @ID PostTargetSync
// @Summary Sync targets with the management platform
// @Description Bring the target table in step with CB-Tumblebug: describe every listed target again, and delete a target missing from the listing only when CB-Tumblebug answers that it is gone. Targets are never added here; they are registered at agent install. If the listing fails, nothing changes.
// @Tags [Monitoring] Targets
// @Produce json
// @Success 200 {object} model.TargetSyncResp
// @Failure 409 {object} model.SimpleMsg "A sync is already running"
// @Failure 500 {object} model.SimpleMsg
// @Failure 502 {object} model.SimpleMsg "The management platform could not be listed; nothing changed"
// @Failure 503 {object} model.SimpleMsg
// @Router /targets/sync [post]
func (h *handler) RestPostTargetSync(c echo.Context) error {
	if h.Syncer == nil {
		if h.Targets == nil {
			return c.JSON(http.StatusServiceUnavailable, msgNoStore)
		}
		return c.JSON(http.StatusServiceUnavailable, msgNoSource)
	}
	res, err := h.Syncer.Sync(c.Request().Context())
	switch {
	case errors.Is(err, mapping.ErrSyncRunning):
		return c.JSON(http.StatusConflict, model.SimpleMsg{Message: "A sync is already running"})
	case errors.Is(err, mapping.ErrListFailed):
		log.Error().Err(err).Msg("Target sync could not list the platform's targets")
		return c.JSON(http.StatusBadGateway, model.SimpleMsg{Message: "Could not list targets from the management platform; nothing changed"})
	case err != nil:
		log.Error().Err(err).Msg("Target sync failed")
		return c.JSON(http.StatusInternalServerError, model.SimpleMsg{Message: "Target sync failed"})
	}
	if res.Updated == nil {
		res.Updated = []string{}
	}
	if res.Deleted == nil {
		res.Deleted = []string{}
	}
	return c.JSON(http.StatusOK, model.TargetSyncResp{Updated: res.Updated, Deleted: res.Deleted, Kept: res.Kept})
}
