package nvidiagpureceiver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.uber.org/zap"
)

// gpmBinary is the helper that reads NVML GPM (otelcol/cmd/aio11y-gpm). NVML
// is opened with dlopen, which links libc, so it is a separate executable and
// the collector stays statically linked.
const gpmBinary = "aio11y-gpm"

// gpmNamespace prefixes the helper's fields: they come from NVML, so
// gpm.sm.utilization is written as nvml.gpm.sm.utilization.
const gpmNamespace = "nvml"

// gpmOutput is the JSON aio11y-gpm writes (gpm.Output in its module). It is
// copied rather than imported so this module does not require the helper's
// module and, through it, purego.
type gpmOutput struct {
	Results []gpmResult `json:"results"`
	Skipped string      `json:"skipped"`
}

// gpmResult is one device or MIG instance. UUID is always the GPU's, as
// nvidia-smi reports it; GPUInstanceID is left out of the JSON when it is 0.
type gpmResult struct {
	UUID          string             `json:"uuid"`
	MIG           bool               `json:"mig"`
	GPUInstanceID uint32             `json:"gpu_instance_id"`
	MIGUUID       string             `json:"mig_uuid"`
	Values        map[string]float64 `json:"values"`
}

// gpmReader runs aio11y-gpm once per scrape and reads its output.
type gpmReader struct {
	cfg    GPMConfig
	logger *zap.Logger
	// limit bounds a run when the scrape has no deadline.
	limit time.Duration
	// path is the helper found at Start, empty when GPM is not read.
	path string
	run  runFunc

	warn sync.Once
	skip sync.Once
}

func newGPMReader(cfg GPMConfig, limit time.Duration, logger *zap.Logger) *gpmReader {
	return &gpmReader{cfg: cfg, limit: limit, logger: logger, run: runCommand}
}

// Start finds the helper. Without it GPM is not read and the receiver goes on
// with the rest: the configuration that enables GPM is the same on hosts that
// were installed without the helper.
func (g *gpmReader) Start(context.Context, component.Host) error {
	p, err := findGPM(g.cfg.Path)
	if err != nil {
		g.logger.Debug("NVML GPM is not read: "+gpmBinary+" was not found", zap.Error(err))
		return nil
	}
	g.path = p
	return nil
}

// findGPM finds the helper: path when given, else next to the collector's
// executable, else on PATH. The installer puts it next to the collector, so a
// service's short PATH does not matter.
func findGPM(path string) (string, error) {
	isFile := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && !st.IsDir()
	}
	if path != "" {
		if !isFile(path) {
			return "", fmt.Errorf("%s is not a file", path)
		}
		return path, nil
	}
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), gpmBinary); isFile(p) {
			return p, nil
		}
	}
	return exec.LookPath(gpmBinary)
}

// Read runs the helper once and returns its results; nil when the helper is
// not found, fails or GPM cannot be used. A failure only leaves GPM out of
// this scrape: it is logged as a warning the first time and at debug level
// after, rather than returned, which would log an error every scrape.
// Unsupported GPUs (older than Hopper) are normal and logged once at debug.
func (g *gpmReader) Read(ctx context.Context) []gpmResult {
	if g.path == "" {
		return nil
	}
	out, err := g.read(ctx)
	if err != nil {
		warned := false
		g.warn.Do(func() {
			warned = true
			g.logger.Warn("running "+gpmBinary+" failed; NVML GPM is left out of this scrape", zap.String("path", g.path), zap.Error(err))
		})
		if !warned {
			g.logger.Debug("running "+gpmBinary+" failed", zap.Error(err))
		}
		return nil
	}
	if out.Skipped != "" {
		g.skip.Do(func() {
			g.logger.Debug("NVML GPM cannot be used on this host", zap.String("reason", out.Skipped))
		})
	}
	return out.Results
}

func (g *gpmReader) read(ctx context.Context) (gpmOutput, error) {
	var out gpmOutput
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.limit)
		defer cancel()
	}
	data, err := g.run(ctx, g.path, "-interval", g.cfg.SampleInterval.String())
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, fmt.Errorf("%s answered no JSON: %w: %s", g.path, err, strings.TrimSpace(string(firstLine(data))))
	}
	return out, nil
}

// firstLine is data up to its first line break, at most 200 bytes, to show in
// an error.
func firstLine(data []byte) []byte {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[:i]
	}
	if len(data) > 200 {
		data = data[:200]
	}
	return data
}

// appendGPM writes the GPM values as gauges in percent named nvml.<field>,
// one point per device or MIG instance. A device point carries the GPU's
// attributes as nvidia-smi read them (index, uuid, name), found by uuid; a MIG
// instance point adds mig "true", gpu_instance_id and, when NVML gave it,
// mig.uuid.
func appendGPM(sm pmetric.ScopeMetrics, ts pcommon.Timestamp, results []gpmResult, gpus []GPU) {
	attrs := map[string]map[string]string{}
	for _, g := range gpus {
		if u := g.Attributes["uuid"]; u != "" {
			attrs[u] = g.Attributes
		}
	}
	byName := map[string]pmetric.Gauge{}
	for _, r := range results {
		if r.UUID == "" {
			continue
		}
		fields := make([]string, 0, len(r.Values))
		for f := range r.Values {
			fields = append(fields, f)
		}
		slices.Sort(fields)
		for _, f := range fields {
			name := gpmNamespace + "." + f
			gauge, ok := byName[name]
			if !ok {
				m := sm.Metrics().AppendEmpty()
				m.SetName(name)
				m.SetUnit("%")
				gauge = m.SetEmptyGauge()
				byName[name] = gauge
			}
			dp := gauge.DataPoints().AppendEmpty()
			dp.SetTimestamp(ts)
			dp.SetDoubleValue(r.Values[f])
			for k, v := range attrs[r.UUID] {
				dp.Attributes().PutStr(k, v)
			}
			dp.Attributes().PutStr("uuid", r.UUID)
			if r.MIG {
				dp.Attributes().PutStr("mig", "true")
				dp.Attributes().PutInt("gpu_instance_id", int64(r.GPUInstanceID))
				if r.MIGUUID != "" {
					dp.Attributes().PutStr("mig.uuid", r.MIGUUID)
				}
			}
		}
	}
}
