package collector

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// BatchProcessor is the processor that groups data points before they are
// sent. It runs with its defaults: it sends what it has 200ms after the first
// point arrives, so a collector still sends right after each collection and
// the send times of different collectors stay as spread as their collection
// times.
const BatchProcessor = "batch"

// ExporterOTLPHTTP is the exporter id the renderer writes. The collector
// release in use renamed "otlphttp" to "otlp_http".
const ExporterOTLPHTTP = "otlp_http"

// ConnectorFailover is the connector that sends to the first write endpoint
// and moves to the next while it fails, used when there are several.
const ConnectorFailover = "failover"

// ExtensionBasicAuth is the extension that signs an exporter's requests with
// a basic auth account.
const ExtensionBasicAuth = "basicauth"

// WritePasswordEnv is the environment variable on a collector's host that
// holds the password of the write account (AIO11Y_BACKEND_WRITE_USERNAME).
const WritePasswordEnv = "AIO11Y_WRITE_PASSWORD"

// Render builds the collector configuration for one target: the fragments of
// every agent-path kind that applies to it, the processor that stamps its
// identity labels, and one metrics pipeline to the endpoints: one endpoint as
// one exporter, several behind a failover connector (see exportTo). Two fragments that
// define the same component id are an error rather than one silently
// replacing the other. The output is YAML with sorted keys, so the same
// inputs always render the same bytes.
func Render(t monitoring.Target, kinds []monitoring.CollectorKind, c monitoring.MetricCatalog, endpoints []monitoring.Endpoint, opts Options) ([]byte, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("collector has no endpoint")
	}
	for _, e := range endpoints {
		if e.URL == "" {
			return nil, errors.New("collector endpoint has no URL")
		}
		if a := e.BasicAuth; a != nil && (a.Username == "" || a.PasswordEnv == "") {
			return nil, errors.New("collector endpoint account needs a username and a password variable")
		}
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

	// The identity processor runs after every other processor that may change
	// data points, so none can change the label it stamps; batch only groups
	// them and goes last, as the collector documentation recommends.
	if err := mergeInto(processors, map[string]any{BatchProcessor: map[string]any{}}, "processor"); err != nil {
		return nil, err
	}
	pipelineProcessors := slices.Sorted(maps.Keys(processors))
	pipelineProcessors = slices.DeleteFunc(pipelineProcessors, func(p string) bool {
		return p == monitoring.IdentityProcessor || p == BatchProcessor
	})
	pipelineProcessors = append(pipelineProcessors, monitoring.IdentityProcessor, BatchProcessor)

	out := exportTo(endpoints)
	pipelines := map[string]any{
		"metrics": map[string]any{
			"receivers":  slices.Sorted(maps.Keys(receivers)),
			"processors": pipelineProcessors,
			"exporters":  []string{out.head},
		},
	}
	maps.Copy(pipelines, out.pipelines)
	cfg := map[string]any{
		"receivers":  receivers,
		"processors": processors,
		"exporters":  out.exporters,
		"service": map[string]any{
			"telemetry": telemetry(opts.TelemetryPort),
			"pipelines": pipelines,
		},
	}
	if out.connectors != nil {
		cfg["connectors"] = out.connectors
	}
	if len(out.extensions) > 0 {
		cfg["extensions"] = out.extensions
		cfg["service"].(map[string]any)["extensions"] = slices.Sorted(maps.Keys(out.extensions))
	}
	return yaml.Marshal(cfg)
}

// output is where the metrics pipeline ends: head is the component it exports
// to, with the exporters, connectors, extensions and extra pipelines that come
// with it.
type output struct {
	head       string
	exporters  map[string]any
	connectors map[string]any
	extensions map[string]any
	pipelines  map[string]any
}

// exportTo writes the endpoints. One endpoint is one exporter, with the
// exporter's own queue and retries. Several are one exporter each, in a
// pipeline per priority behind the failover connector, as the connector
// documentation sets them up: the exporters' queues and retries are off, so a
// failure reaches the connector, which then moves to the next endpoint and
// goes back to the first once it answers again. While every endpoint fails,
// nothing is kept.
func exportTo(endpoints []monitoring.Endpoint) output {
	if len(endpoints) == 1 {
		out := output{head: ExporterOTLPHTTP, extensions: map[string]any{}}
		out.exporters = map[string]any{ExporterOTLPHTTP: exporterConfig(endpoints[0], ExtensionBasicAuth, out.extensions)}
		return out
	}
	out := output{head: ConnectorFailover, exporters: map[string]any{}, extensions: map[string]any{}, pipelines: map[string]any{}}
	levels := make([]any, 0, len(endpoints))
	for i, e := range endpoints {
		exporter := fmt.Sprintf("%s/%d", ExporterOTLPHTTP, i)
		pipeline := fmt.Sprintf("metrics/%d", i)
		cfg := exporterConfig(e, fmt.Sprintf("%s/%d", ExtensionBasicAuth, i), out.extensions)
		cfg["sending_queue"] = map[string]any{"enabled": false}
		cfg["retry_on_failure"] = map[string]any{"enabled": false}
		out.exporters[exporter] = cfg
		out.pipelines[pipeline] = map[string]any{
			"receivers": []string{ConnectorFailover},
			"exporters": []string{exporter},
		}
		levels = append(levels, []string{pipeline})
	}
	out.connectors = map[string]any{ConnectorFailover: map[string]any{"priority_levels": levels}}
	return out
}

// exporterConfig is the exporter for e. An account adds a basic auth
// extension under authID to extensions, its password taken from the
// collector host's environment when the collector starts.
func exporterConfig(e monitoring.Endpoint, authID string, extensions map[string]any) map[string]any {
	cfg := map[string]any{"endpoint": e.URL}
	if len(e.Headers) > 0 {
		cfg["headers"] = maps.Clone(e.Headers)
	}
	if a := e.BasicAuth; a != nil {
		extensions[authID] = map[string]any{"client_auth": map[string]any{
			"username": a.Username,
			"password": "${env:" + a.PasswordEnv + "}",
		}}
		cfg["auth"] = map[string]any{"authenticator": authID}
	}
	return cfg
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

// Options are deployment-wide settings of the rendered configuration.
type Options struct {
	// TelemetryPort is the localhost port the collector serves its own
	// metrics on (Prometheus format). Zero turns them off, so the collector
	// opens no port another collector on the host may already hold; nothing
	// reads them yet.
	TelemetryPort int
}

// telemetry is the collector's own telemetry section.
func telemetry(port int) map[string]any {
	if port <= 0 {
		return map[string]any{"metrics": map[string]any{"level": "none"}}
	}
	return map[string]any{"metrics": map[string]any{
		"level": "basic",
		"readers": []any{map[string]any{"pull": map[string]any{"exporter": map[string]any{
			"prometheus": map[string]any{"host": "127.0.0.1", "port": port},
		}}}},
	}}
}
