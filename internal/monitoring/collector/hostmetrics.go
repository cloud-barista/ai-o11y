// Package collector holds the CollectorKind providers of the agent path and
// the renderer that merges their fragments into one collector configuration.
package collector

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
)

// DefaultInterval matches the collection interval of the current host
// monitoring.
const DefaultInterval = 30 * time.Second

// ErrHealthUnsupported is returned by Health until collectors report their
// own state back to ai-o11y.
var ErrHealthUnsupported = errors.New("collector health is not reported yet")

// HostmetricsReceiver is the receiver id the hostmetrics kind renders. The
// collector release in use renamed "hostmetrics" to "host_metrics".
const HostmetricsReceiver = "host_metrics"

// scraperPrefixes maps a hostmetrics metric name prefix to the scraper that
// emits it. Longer prefixes come first so system_cpu_load_average resolves to
// load, not cpu. Prefixes use the Prometheus translation with underscores,
// because the dotted names keep underscores of their own (load_average), so
// only dots-to-underscores is lossless; normalize() maps dotted names over.
var scraperPrefixes = []struct{ prefix, scraper string }{
	{"system_cpu_load_average", "load"},
	{"system_cpu_", "cpu"},
	{"system_linux_memory_", "memory"},
	{"system_memory_", "memory"},
	{"system_paging_", "paging"},
	{"system_filesystem_", "filesystem"},
	{"system_disk_", "disk"},
	{"system_network_", "network"},
	{"system_processes_", "processes"},
	{"system_uptime", "system"},
	{"process_", "process"},
}

// metricNameRE finds hostmetrics series names in a PromQL expression.
var metricNameRE = regexp.MustCompile(`\b(?:system|process)_[a-z0-9_]+`)

func normalize(name string) string { return strings.ReplaceAll(name, ".", "_") }

func scraperOf(name string) (string, error) {
	n := normalize(name)
	for _, p := range scraperPrefixes {
		if strings.HasPrefix(n, p.prefix) {
			return p.scraper, nil
		}
	}
	return "", fmt.Errorf("no hostmetrics scraper emits %q", name)
}

// Hostmetrics renders the hostmetrics receiver for a VM: the scrapers the
// catalog's hostmetrics expressions read from, with the optional metrics those
// rows need turned on.
type Hostmetrics struct {
	Interval time.Duration
}

var _ monitoring.CollectorKind = Hostmetrics{}

// Path implements monitoring.CollectorKind.
func (Hostmetrics) Path() monitoring.Path { return monitoring.PathAgent }

// AppliesTo implements monitoring.CollectorKind: every VM runs hostmetrics.
func (Hostmetrics) AppliesTo(t monitoring.Target) bool { return t.Kind == "vm" }

// Render implements monitoring.CollectorKind. Rows with a gap are skipped. An
// expression or enabled metric no scraper is known to emit is an error, so a
// new catalog row cannot silently collect nothing.
func (h Hostmetrics) Render(t monitoring.Target, c monitoring.MetricCatalog) (monitoring.ConfigFragment, error) {
	interval := h.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	scrapers := map[string]map[string]any{}
	for _, row := range c.Rows() {
		expr, ok := row.Exprs[catalog.CollectorHostmetrics]
		if !ok || row.Gap != "" {
			continue
		}
		for _, name := range metricNameRE.FindAllString(expr, -1) {
			s, err := scraperOf(name)
			if err != nil {
				return monitoring.ConfigFragment{}, fmt.Errorf("catalog row %s: %w", row.Name, err)
			}
			if scrapers[s] == nil {
				scrapers[s] = map[string]any{}
			}
		}
		for _, metric := range row.Enables {
			s, err := scraperOf(metric)
			if err != nil {
				return monitoring.ConfigFragment{}, fmt.Errorf("catalog row %s: %w", row.Name, err)
			}
			if scrapers[s] == nil {
				scrapers[s] = map[string]any{}
			}
			metrics, _ := scrapers[s]["metrics"].(map[string]any)
			if metrics == nil {
				metrics = map[string]any{}
				scrapers[s]["metrics"] = metrics
			}
			metrics[metric] = map[string]any{"enabled": true}
		}
	}
	if len(scrapers) == 0 {
		return monitoring.ConfigFragment{}, errors.New("catalog has no hostmetrics expression")
	}

	scraperCfg := make(map[string]any, len(scrapers))
	for name, cfg := range scrapers {
		scraperCfg[name] = cfg
	}
	return monitoring.ConfigFragment{
		Receivers: map[string]any{
			HostmetricsReceiver: map[string]any{
				"collection_interval": interval.String(),
				"scrapers":            scraperCfg,
			},
		},
	}, nil
}

// Health implements monitoring.CollectorKind.
func (Hostmetrics) Health(context.Context, monitoring.Target) (monitoring.CollectorHealth, error) {
	return monitoring.CollectorHealth{}, ErrHealthUnsupported
}
