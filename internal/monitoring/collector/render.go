package collector

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// ExporterOTLPHTTP is the exporter id the renderer writes. The collector
// release in use renamed "otlphttp" to "otlp_http".
const ExporterOTLPHTTP = "otlp_http"

// Render builds the collector configuration for one target: the fragments of
// every agent-path kind that applies to it, the processor that stamps its
// identity labels, and one metrics pipeline to endpoint. Two fragments that
// define the same component id are an error rather than one silently
// replacing the other. The output is YAML with sorted keys, so the same
// inputs always render the same bytes.
func Render(t monitoring.Target, kinds []monitoring.CollectorKind, c monitoring.MetricCatalog, endpoint monitoring.Endpoint) ([]byte, error) {
	if endpoint.URL == "" {
		return nil, errors.New("collector endpoint has no URL")
	}
	identity, err := monitoring.IdentityFragment(t)
	if err != nil {
		return nil, err
	}

	receivers := map[string]any{}
	processors := map[string]any{}
	for _, k := range kinds {
		if k.Path() != monitoring.PathAgent || !k.AppliesTo(t) {
			continue
		}
		frag, err := k.Render(t, c)
		if err != nil {
			return nil, fmt.Errorf("render target %q: %w", t.EntityID, err)
		}
		if err := mergeInto(receivers, frag.Receivers, "receiver"); err != nil {
			return nil, err
		}
		if err := mergeInto(processors, frag.Processors, "processor"); err != nil {
			return nil, err
		}
	}
	if len(receivers) == 0 {
		return nil, fmt.Errorf("no collector kind applies to target %q", t.EntityID)
	}
	if err := mergeInto(processors, identity.Processors, "processor"); err != nil {
		return nil, err
	}

	exporter := map[string]any{"endpoint": endpoint.URL}
	if len(endpoint.Headers) > 0 {
		exporter["headers"] = maps.Clone(endpoint.Headers)
	}
	// The identity processor runs last so no other processor can change the
	// labels it stamps.
	pipelineProcessors := slices.Sorted(maps.Keys(processors))
	pipelineProcessors = slices.DeleteFunc(pipelineProcessors, func(p string) bool { return p == monitoring.IdentityProcessor })
	pipelineProcessors = append(pipelineProcessors, monitoring.IdentityProcessor)

	cfg := map[string]any{
		"receivers":  receivers,
		"processors": processors,
		"exporters":  map[string]any{ExporterOTLPHTTP: exporter},
		"service": map[string]any{
			"pipelines": map[string]any{
				"metrics": map[string]any{
					"receivers":  slices.Sorted(maps.Keys(receivers)),
					"processors": pipelineProcessors,
					"exporters":  []string{ExporterOTLPHTTP},
				},
			},
		},
	}
	return yaml.Marshal(cfg)
}

func mergeInto(dst, src map[string]any, kind string) error {
	for id, v := range src {
		if _, ok := dst[id]; ok {
			return fmt.Errorf("%s %q is defined by two collector kinds", kind, id)
		}
		dst[id] = v
	}
	return nil
}
