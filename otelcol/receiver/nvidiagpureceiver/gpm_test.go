package nvidiagpureceiver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/scraper/scrapererror"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// fakeGPM writes a stand-in for aio11y-gpm: a shell script that records its
// arguments next to itself and runs body.
func fakeGPM(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, gpmBinary)
	script := "#!/bin/sh\necho \"$@\" >\"$(dirname \"$0\")/args\"\n" + body + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// gpmScraper is a scraper reading one H100 from nvidia-smi and GPM from the
// helper at path, logging to the returned observer.
func gpmScraper(t *testing.T, path string, source Source) (*gpuScraper, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	g := newGPMReader(GPMConfig{Enabled: true, Path: path, SampleInterval: time.Second}, 5*time.Second, zap.New(core))
	if err := g.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	if source == nil {
		source = fixedSource{gpus: []GPU{{Attributes: map[string]string{"index": "0", "uuid": "GPU-a", "name": "H100"},
			Values: []Value{{Field: "utilization.gpu", Unit: "%", Value: 40}}}}}
	}
	at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	return &gpuScraper{source: source, now: func() time.Time { return at }, gpm: g}, logs
}

func metricByName(md pmetric.Metrics, name string) (pmetric.Metric, bool) {
	if md.ResourceMetrics().Len() == 0 {
		return pmetric.Metric{}, false
	}
	ms := md.ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
	for i := 0; i < ms.Len(); i++ {
		if ms.At(i).Name() == name {
			return ms.At(i), true
		}
	}
	return pmetric.Metric{}, false
}

func attrsOf(dp pmetric.NumberDataPoint) map[string]any {
	return dp.Attributes().AsRaw()
}

// TestScrapeEmitsGPMOfDeviceAndMIG: one device and one MIG instance of GPU
// instance 0, which the helper writes without gpu_instance_id.
func TestScrapeEmitsGPMOfDeviceAndMIG(t *testing.T) {
	path := fakeGPM(t, `cat <<'EOF'
{"results":[
 {"uuid":"GPU-a","values":{"gpm.graphics.utilization":55,"gpm.sm.utilization":50}},
 {"uuid":"GPU-a","mig":true,"mig_uuid":"MIG-x","values":{"gpm.sm.utilization":12.5}}]}
EOF`)
	s, logs := gpmScraper(t, path, nil)
	md, err := s.scrape(context.Background())
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if args, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "args")); strings.TrimSpace(string(args)) != "-interval 1s" {
		t.Errorf("helper arguments = %q", args)
	}
	if _, ok := metricByName(md, "nvidia_smi.utilization.gpu"); !ok {
		t.Error("nvidia_smi.utilization.gpu is missing")
	}
	sm, ok := metricByName(md, "nvml.gpm.sm.utilization")
	if !ok || sm.Unit() != "%" || sm.Type() != pmetric.MetricTypeGauge || sm.Gauge().DataPoints().Len() != 2 {
		t.Fatalf("nvml.gpm.sm.utilization = %v %v", ok, sm.Unit())
	}
	dev, mig := sm.Gauge().DataPoints().At(0), sm.Gauge().DataPoints().At(1)
	if dev.DoubleValue() != 50 || mig.DoubleValue() != 12.5 {
		t.Errorf("values = %v, %v", dev.DoubleValue(), mig.DoubleValue())
	}
	wantDev := map[string]any{"index": "0", "uuid": "GPU-a", "name": "H100"}
	if got := attrsOf(dev); !equalAttrs(got, wantDev) {
		t.Errorf("device attributes = %v, want %v", got, wantDev)
	}
	wantMIG := map[string]any{"index": "0", "uuid": "GPU-a", "name": "H100", "mig": "true", "gpu_instance_id": int64(0), "mig.uuid": "MIG-x"}
	if got := attrsOf(mig); !equalAttrs(got, wantMIG) {
		t.Errorf("MIG attributes = %v, want %v", got, wantMIG)
	}
	if mig.Timestamp().AsTime() != time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) {
		t.Errorf("timestamp = %v", mig.Timestamp().AsTime())
	}
	gr, ok := metricByName(md, "nvml.gpm.graphics.utilization")
	if !ok || gr.Gauge().DataPoints().Len() != 1 || gr.Gauge().DataPoints().At(0).DoubleValue() != 55 {
		t.Errorf("nvml.gpm.graphics.utilization = %v", ok)
	}
	if logs.Len() != 0 {
		t.Errorf("logged %v", logs.All())
	}
}

func equalAttrs(got, want map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestScrapeGPMSkippedLogsOnceAtDebug(t *testing.T) {
	path := fakeGPM(t, `echo '{"results":[],"skipped":"no device or MIG instance supports GPM"}'`)
	s, logs := gpmScraper(t, path, nil)
	for range 2 {
		md, err := s.scrape(context.Background())
		if err != nil {
			t.Fatalf("scrape: %v", err)
		}
		if _, ok := metricByName(md, "nvml.gpm.sm.utilization"); ok {
			t.Error("GPM metric emitted for a skipped run")
		}
		if _, ok := metricByName(md, "nvidia_smi.utilization.gpu"); !ok {
			t.Error("nvidia_smi.utilization.gpu is missing")
		}
	}
	all := logs.All()
	if len(all) != 1 || all[0].Level != zapcore.DebugLevel || all[0].ContextMap()["reason"] != "no device or MIG instance supports GPM" {
		t.Errorf("logs = %v, want one debug entry with the reason", all)
	}
}

// TestScrapeGPMFailureKeepsTheRest: a helper that fails, answers no JSON or
// does not finish in time leaves GPM out, warns the first time and logs at
// debug after, and the scrape still succeeds with nvidia-smi's values.
func TestScrapeGPMFailureKeepsTheRest(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		timeout time.Duration
		want    string
	}{
		"exit 1":   {body: "echo 'writing the output failed' >&2; exit 1", want: "writing the output failed"},
		"not JSON": {body: "echo 'Segmentation fault'", want: "answered no JSON"},
		"timeout":  {body: "exec sleep 30", timeout: 300 * time.Millisecond, want: "signal: killed"},
	} {
		t.Run(name, func(t *testing.T) {
			s, logs := gpmScraper(t, fakeGPM(t, tc.body), nil)
			for i := range 2 {
				ctx := context.Background()
				if tc.timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.timeout)
					defer cancel()
				}
				start := time.Now()
				md, err := s.scrape(ctx)
				if err != nil {
					t.Fatalf("scrape %d: %v", i, err)
				}
				if d := time.Since(start); d > 5*time.Second {
					t.Errorf("scrape %d took %v", i, d)
				}
				if _, ok := metricByName(md, "nvidia_smi.utilization.gpu"); !ok {
					t.Errorf("scrape %d: nvidia_smi.utilization.gpu is missing", i)
				}
				if _, ok := metricByName(md, "nvml.gpm.sm.utilization"); ok {
					t.Errorf("scrape %d: GPM metric emitted", i)
				}
			}
			all := logs.All()
			if len(all) != 2 || all[0].Level != zapcore.WarnLevel || all[1].Level != zapcore.DebugLevel {
				t.Fatalf("logs = %v, want a warning then a debug entry", all)
			}
			if err, _ := all[0].ContextMap()["error"].(string); !strings.Contains(err, tc.want) {
				t.Errorf("warning error = %q, want it to hold %q", err, tc.want)
			}
		})
	}
}

// TestScrapeGPMWhenSourceFails: GPM values go out without nvidia-smi's, as a
// partial scrape, with the uuid the helper gave.
func TestScrapeGPMWhenSourceFails(t *testing.T) {
	path := fakeGPM(t, `echo '{"results":[{"uuid":"GPU-a","mig":true,"gpu_instance_id":3,"values":{"gpm.sm.utilization":7}}]}'`)
	s, _ := gpmScraper(t, path, failingSource{})
	md, err := s.scrape(context.Background())
	var partial scrapererror.PartialScrapeError
	if !errors.As(err, &partial) {
		t.Fatalf("err = %v, want a partial scrape error", err)
	}
	sm, ok := metricByName(md, "nvml.gpm.sm.utilization")
	if !ok || sm.Gauge().DataPoints().Len() != 1 {
		t.Fatalf("nvml.gpm.sm.utilization = %v", ok)
	}
	want := map[string]any{"uuid": "GPU-a", "mig": "true", "gpu_instance_id": int64(3)}
	if got := attrsOf(sm.Gauge().DataPoints().At(0)); !equalAttrs(got, want) {
		t.Errorf("attributes = %v, want %v", got, want)
	}
}

// TestGPMHelperNotFound: without the helper GPM is off, logged once at debug
// at start, and the scrape never runs anything for it.
func TestGPMHelperNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for name, path := range map[string]string{"given path": filepath.Join(t.TempDir(), gpmBinary), "looked up": ""} {
		t.Run(name, func(t *testing.T) {
			s, logs := gpmScraper(t, path, nil)
			if s.gpm.path != "" {
				t.Fatalf("found %q", s.gpm.path)
			}
			all := logs.All()
			if len(all) != 1 || all[0].Level != zapcore.DebugLevel {
				t.Errorf("logs = %v, want one debug entry", all)
			}
			md, err := s.scrape(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := metricByName(md, "nvidia_smi.utilization.gpu"); !ok {
				t.Error("nvidia_smi.utilization.gpu is missing")
			}
		})
	}
}

func TestFindGPMOnPath(t *testing.T) {
	path := fakeGPM(t, "true")
	t.Setenv("PATH", filepath.Dir(path))
	got, err := findGPM("")
	if err != nil || got != path {
		t.Errorf("findGPM = %q, %v; want %q", got, err, path)
	}
}

func TestAppendGPMSkipsResultWithoutUUID(t *testing.T) {
	sm := pmetric.NewMetrics().ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty()
	appendGPM(sm, pcommon.Timestamp(0), []gpmResult{{Values: map[string]float64{"gpm.sm.utilization": 1}}}, nil)
	if sm.Metrics().Len() != 0 {
		t.Errorf("metrics = %d, want none", sm.Metrics().Len())
	}
}
