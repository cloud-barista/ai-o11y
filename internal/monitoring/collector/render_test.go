package collector

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
)

var update = flag.Bool("update", false, "rewrite golden files")

func builtin(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.New(catalog.Builtin())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return c
}

func vm() monitoring.Target {
	return monitoring.Target{
		EntityID: "d44ikdomt0tac4ejrmmg",
		Kind:     "vm",
		Name:     "web-1",
		Provider: "aws",
		Region:   "ap-northeast-2",
		OS:       "linux",
		Extra:    map[string]string{"nsId": "ns01"},
	}
}

var (
	kinds    = []monitoring.CollectorKind{Hostmetrics{}, NvidiaGPU{}}
	endpoint = monitoring.Endpoint{URL: "http://prometheus:9090/api/v1/otlp"}
)

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	golden := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("config differs from %s:\n%s", golden, got)
	}
}

// TestRenderSnapshot pins the whole collector configuration for a VM. Run with
// -update to rewrite after a deliberate change.
func TestRenderSnapshot(t *testing.T) {
	got, err := Render(vm(), kinds, builtin(t), endpoint)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm.golden.yaml", got)
}

func TestRenderIsDeterministic(t *testing.T) {
	c := builtin(t)
	target := vm()
	first, err := Render(target, kinds, c, endpoint)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for range 20 {
		next, err := Render(target, kinds, c, endpoint)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if string(next) != string(first) {
			t.Fatal("Render output changed between calls")
		}
	}
}

func TestRenderProcessorOrder(t *testing.T) {
	extra := fixedKind{frag: monitoring.ConfigFragment{
		Receivers:  map[string]any{"otlp": map[string]any{}},
		Processors: map[string]any{"zzz": map[string]any{}},
	}}
	got, err := Render(vm(), []monitoring.CollectorKind{extra}, builtin(t), endpoint)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "processors:\n                - zzz\n                - " + monitoring.IdentityProcessor + "\n                - " + BatchProcessor
	if !strings.Contains(string(got), want) {
		t.Fatalf("processors are not ordered other, identity, batch:\n%s", got)
	}
}

func TestRenderRejects(t *testing.T) {
	c := builtin(t)
	unmapped := vm()
	unmapped.EntityID = ""
	cases := map[string]struct {
		target   monitoring.Target
		kinds    []monitoring.CollectorKind
		endpoint monitoring.Endpoint
	}{
		"target without key": {unmapped, kinds, endpoint},
		"no endpoint":        {vm(), kinds, monitoring.Endpoint{}},
		"no kind applies":    {monitoring.Target{Kind: "datastore", EntityID: "x"}, kinds, endpoint},
		"duplicate id":       {vm(), []monitoring.CollectorKind{Hostmetrics{}, Hostmetrics{}}, endpoint},
		"batch clashes":      {vm(), []monitoring.CollectorKind{Hostmetrics{}, fixedKind{frag: monitoring.ConfigFragment{Processors: map[string]any{BatchProcessor: nil}}}}, endpoint},
		"identity clashes":   {vm(), []monitoring.CollectorKind{Hostmetrics{}, fixedKind{frag: monitoring.ConfigFragment{Processors: map[string]any{monitoring.IdentityProcessor: nil}}}}, endpoint},
	}
	for name, tc := range cases {
		if _, err := Render(tc.target, tc.kinds, c, tc.endpoint); err == nil {
			t.Errorf("%s: Render succeeded", name)
		}
	}
}

func TestHostmetricsRejectsUnknownMetric(t *testing.T) {
	rows := []monitoring.MetricRow{{
		Name: "host.mystery", Area: "cpu", Unit: "1", Kind: monitoring.KindGauge,
		Exprs: map[string]string{catalog.CollectorHostmetrics: `sum by (entity_id) (system_mystery_total{$sel})`},
	}}
	c, err := catalog.New(rows)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := (Hostmetrics{}).Render(vm(), c); err == nil {
		t.Fatal("Render accepted a metric no scraper emits")
	}
}

func TestScraperOf(t *testing.T) {
	for name, want := range map[string]string{
		"system_cpu_load_average_1m_ratio": "load",
		"system.cpu.load_average.15m":      "load",
		"system_cpu_time_seconds_total":    "cpu",
		"system.cpu.physical.count":        "cpu",
		"system.linux.memory.available":    "memory",
		"system_memory_usage_bytes":        "memory",
		"system_paging_operations_total":   "paging",
		"system_filesystem_usage_bytes":    "filesystem",
		"system_disk_io_bytes_total":       "disk",
		"system_network_io_bytes_total":    "network",
		"system_processes_count":           "processes",
		"system_uptime_seconds":            "system",
		"process.cpu.utilization":          "process",
	} {
		got, err := scraperOf(name)
		if err != nil || got != want {
			t.Errorf("scraperOf(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

type fixedKind struct{ frag monitoring.ConfigFragment }

func (fixedKind) Name() string                     { return "fixed" }
func (fixedKind) Path() monitoring.Path            { return monitoring.PathAgent }
func (fixedKind) AppliesTo(monitoring.Target) bool { return true }
func (k fixedKind) Render(monitoring.Target, monitoring.MetricCatalog) (monitoring.ConfigFragment, error) {
	return k.frag, nil
}
func (fixedKind) Health(context.Context, monitoring.Target) (monitoring.CollectorHealth, error) {
	return monitoring.CollectorHealth{}, nil
}

func gpuVM() monitoring.Target {
	t := vm()
	t.GPUCount, t.GPUModel = 2, "NVIDIA A100-SXM4-80GB"
	return t
}

// TestRenderGPUSnapshot is a VM with GPUs: the NVIDIA GPU receiver joins
// hostmetrics in the one metrics pipeline.
func TestRenderGPUSnapshot(t *testing.T) {
	got, err := Render(gpuVM(), kinds, builtin(t), endpoint)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "gpu-vm.golden.yaml", got)
}

func TestNvidiaGPUAppliesTo(t *testing.T) {
	for name, tc := range map[string]struct {
		count int
		model string
		want  bool
	}{
		"no gpu":        {0, "", false},
		"nvidia":        {1, "NVIDIA T4", true},
		"model unknown": {1, "", true},
		"amd":           {1, "AMD Instinct MI300X", false},
		"intel":         {1, "Intel Gaudi 2", false},
	} {
		target := vm()
		target.GPUCount, target.GPUModel = tc.count, tc.model
		if got := (NvidiaGPU{}).AppliesTo(target); got != tc.want {
			t.Errorf("%s: AppliesTo = %v, want %v", name, got, tc.want)
		}
	}
}
