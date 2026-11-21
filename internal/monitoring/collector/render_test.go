package collector

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

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
	kinds    = []monitoring.CollectorKind{Hostmetrics{}, NvidiaGPU{}, AMDGPU{}}
	endpoint = []monitoring.Endpoint{{URL: "http://prometheus:9090/api/v1/otlp/v1/metrics"}}
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
	got, err := Render(vm(), kinds, builtin(t), endpoint, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm.golden.yaml", got)
}

func TestRenderIsDeterministic(t *testing.T) {
	c := builtin(t)
	target := vm()
	first, err := Render(target, kinds, c, endpoint, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for range 20 {
		next, err := Render(target, kinds, c, endpoint, Options{})
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
	got, err := Render(vm(), []monitoring.CollectorKind{extra}, builtin(t), endpoint, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	want := "processors:\n                - " + MemoryLimiterProcessor + "\n                - zzz\n                - " + monitoring.IdentityProcessor + "\n                - " + BatchProcessor
	if !strings.Contains(string(got), want) {
		t.Fatalf("processors are not ordered memory limiter, other, identity, batch:\n%s", got)
	}
}

func TestRenderRejects(t *testing.T) {
	c := builtin(t)
	unmapped := vm()
	unmapped.EntityID = ""
	cases := map[string]struct {
		target   monitoring.Target
		kinds    []monitoring.CollectorKind
		endpoint []monitoring.Endpoint
	}{
		"target without key": {unmapped, kinds, endpoint},
		"no endpoint":        {vm(), kinds, nil},
		"endpoint no URL":    {vm(), kinds, []monitoring.Endpoint{endpoint[0], {}}},
		"no kind applies":    {monitoring.Target{Kind: "datastore", EntityID: "x"}, kinds, endpoint},
		"duplicate id":       {vm(), []monitoring.CollectorKind{Hostmetrics{}, Hostmetrics{}}, endpoint},
		"batch clashes":      {vm(), []monitoring.CollectorKind{Hostmetrics{}, fixedKind{frag: monitoring.ConfigFragment{Processors: map[string]any{BatchProcessor: nil}}}}, endpoint},
		"identity clashes":   {vm(), []monitoring.CollectorKind{Hostmetrics{}, fixedKind{frag: monitoring.ConfigFragment{Processors: map[string]any{monitoring.IdentityProcessor: nil}}}}, endpoint},
		"limiter clashes":    {vm(), []monitoring.CollectorKind{Hostmetrics{}, fixedKind{frag: monitoring.ConfigFragment{Processors: map[string]any{MemoryLimiterProcessor: nil}}}}, endpoint},
	}
	for name, tc := range cases {
		if _, err := Render(tc.target, tc.kinds, c, tc.endpoint, Options{}); err == nil {
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

// The top process processor and the process scraper's mutes come with the
// process scraper and only with it.
func TestHostmetricsTopProcess(t *testing.T) {
	cpu := monitoring.MetricRow{
		Name: "system.uptime", Area: "system", Unit: "s", Kind: monitoring.KindGauge,
		Exprs: map[string]string{catalog.CollectorHostmetrics: `max by (entity_id) (system_uptime_seconds{$sel})`},
	}
	proc := monitoring.MetricRow{
		Name: "process.memory.utilization", Area: "process", Unit: "1", Kind: monitoring.KindGauge,
		Exprs:     map[string]string{catalog.CollectorHostmetrics: `sum by (entity_id) (process_memory_utilization_ratio{$sel})`},
		Dimension: "x", DimensionName: "x", Enables: []string{"process.memory.utilization"},
	}
	for name, tc := range map[string]struct {
		rows        []monitoring.MetricRow
		withProcess bool
	}{
		"no process row": {[]monitoring.MetricRow{cpu}, false},
		"process row":    {[]monitoring.MetricRow{cpu, proc}, true},
	} {
		c, err := catalog.New(tc.rows)
		if err != nil {
			t.Fatalf("%s: catalog: %v", name, err)
		}
		frag, err := (Hostmetrics{}).Render(vm(), c)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		scrapers := frag.Receivers[HostmetricsReceiver].(map[string]any)["scrapers"].(map[string]any)
		process, hasScraper := scrapers["process"].(map[string]any)
		top, hasTop := frag.Processors[TopProcessProcessor]
		if hasScraper != tc.withProcess || hasTop != tc.withProcess {
			t.Fatalf("%s: process scraper %v, %s %v; want both %v", name, hasScraper, TopProcessProcessor, hasTop, tc.withProcess)
		}
		if !tc.withProcess {
			continue
		}
		for _, mute := range processScraperMutes {
			if process[mute] != true {
				t.Errorf("%s: %s = %v", name, mute, process[mute])
			}
		}
		want := map[string]any{"top_n": TopProcesses, "by": []string{"cpu", "memory"}}
		if !reflect.DeepEqual(top, want) {
			t.Errorf("%s: %s = %v, want %v", name, TopProcessProcessor, top, want)
		}
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
		"process_cpu_utilization_ratio":    "process",
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
	got, err := Render(gpuVM(), kinds, builtin(t), endpoint, Options{})
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
		"count unknown": {0, "NVIDIA Tesla T4", true},
		"model unknown": {1, "", false},
		"tumblebug id":  {1, "nvidia-a100-80gb", true},
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

func amdGPUVM() monitoring.Target {
	t := vm()
	t.GPUCount, t.GPUModel = 1, "AMD RADEON PRO V710"
	return t
}

// TestRenderAMDGPUSnapshot is a VM with an AMD GPU: the AMD GPU receiver,
// not the NVIDIA one, joins hostmetrics.
func TestRenderAMDGPUSnapshot(t *testing.T) {
	got, err := Render(amdGPUVM(), kinds, builtin(t), endpoint, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "amd-gpu-vm.golden.yaml", got)
}

func TestAMDGPUAppliesTo(t *testing.T) {
	for name, tc := range map[string]struct {
		kind  string
		count int
		model string
		want  bool
	}{
		"no gpu":        {"vm", 0, "", false},
		"count unknown": {"vm", 0, "AMD RADEON PRO V710", true},
		"radeon":        {"vm", 1, "AMD RADEON PRO V710", true},
		"instinct":      {"vm", 1, "AMD INSTINCT", true},
		"mi300x":        {"vm", 8, "AMD MI300X", true},
		"no vendor":     {"vm", 1, "Radeon Pro V520", true},
		"product only":  {"vm", 1, "Instinct MI250", true},
		"nvidia":        {"vm", 1, "NVIDIA T4", false},
		"model unknown": {"vm", 1, "", false},
		"not a vm":      {"k8s", 1, "AMD MI300X", false},
	} {
		target := vm()
		target.Kind, target.GPUCount, target.GPUModel = tc.kind, tc.count, tc.model
		if got := (AMDGPU{}).AppliesTo(target); got != tc.want {
			t.Errorf("%s: AppliesTo = %v, want %v", name, got, tc.want)
		}
	}
}

// TestRenderTelemetry: the collector's own metrics are off by default, so it
// opens no port; a port turns them on, on localhost.
func TestRenderTelemetry(t *testing.T) {
	off, err := Render(vm(), kinds, builtin(t), endpoint, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(off), "level: none") || strings.Contains(string(off), "port:") {
		t.Errorf("default telemetry:\n%s", off)
	}
	on, err := Render(vm(), kinds, builtin(t), endpoint, Options{TelemetryPort: 18889})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(on), "port: 18889") || !strings.Contains(string(on), "host: 127.0.0.1") {
		t.Errorf("telemetry on a port:\n%s", on)
	}
}

// failover is two write endpoints, as two vmauths in front of replicated
// storage.
var failover = []monitoring.Endpoint{
	{URL: "http://vmauth-1:8427/opentelemetry/v1/metrics"},
	{URL: "http://vmauth-2:8427/opentelemetry/v1/metrics"},
}

// TestRenderFailover pins the configuration for several write endpoints: an
// exporter per endpoint with its queue and retries off, a pipeline per
// priority, and the failover connector between them and the metrics pipeline.
func TestRenderFailover(t *testing.T) {
	got, err := Render(vm(), kinds, builtin(t), failover, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm-failover.golden.yaml", got)
}

// TestRenderWriteAll pins the configuration that sends to every endpoint: an
// exporter per endpoint with its own queue and retries, all of them in the
// metrics pipeline, and no failover connector. An unknown mode is an error.
func TestRenderWriteAll(t *testing.T) {
	got, err := Render(vm(), kinds, builtin(t), failover, Options{WriteMode: WriteAll})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm-write-all.golden.yaml", got)
	if _, err := Render(vm(), kinds, builtin(t), failover, Options{WriteMode: "round-robin"}); err == nil {
		t.Error("an unknown write mode rendered")
	}
}

// TestRenderWriteAccount pins the configuration for write endpoints with an
// account: a basic auth extension per exporter whose password is an
// environment reference, never the password itself.
func TestRenderWriteAccount(t *testing.T) {
	withAuth := make([]monitoring.Endpoint, len(failover))
	for i, e := range failover {
		e.BasicAuth = &monitoring.BasicAuth{Username: "writer", PasswordEnv: WritePasswordEnv}
		withAuth[i] = e
	}
	got, err := Render(vm(), kinds, builtin(t), withAuth, Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm-failover-auth.golden.yaml", got)
	if !strings.Contains(string(got), "${env:"+WritePasswordEnv+"}") {
		t.Error("the password is not taken from the environment")
	}
	single, err := Render(vm(), kinds, builtin(t), withAuth[:1], Options{})
	if err != nil || !strings.Contains(string(single), "authenticator: "+ExtensionBasicAuth+"\n") {
		t.Errorf("single endpoint with an account = %v\n%s", err, single)
	}
	bad := []monitoring.Endpoint{{URL: "http://vmauth:8427/opentelemetry/v1/metrics", BasicAuth: &monitoring.BasicAuth{Username: "writer"}}}
	if _, err := Render(vm(), kinds, builtin(t), bad, Options{}); err == nil {
		t.Error("an account without a password variable rendered")
	}
}

// renderedConfig is the parts of a rendered configuration the disk queue
// tests look at.
type renderedConfig struct {
	Exporters  map[string]map[string]any `yaml:"exporters"`
	Extensions map[string]map[string]any `yaml:"extensions"`
	Service    struct {
		Extensions []string `yaml:"extensions"`
	} `yaml:"service"`
}

// TestRenderDiskQueue: one endpoint keeps what it cannot send on disk and
// retries it with no time limit; with several in failover, only the last
// does, and the others fail fast so the failover connector moves on; with
// several in WriteAll, every one does, in the one file storage extension. The
// queue directory comes from the options, or the default.
func TestRenderDiskQueue(t *testing.T) {
	diskQueue := map[string]any{
		"enabled":       true,
		"storage":       ExtensionQueueStorage,
		"num_consumers": 1,
		"sizer":         "bytes",
		"queue_size":    QueueSizeBytes,
	}
	forever := map[string]any{"enabled": true, "max_elapsed_time": "0s"}
	off := map[string]any{"enabled": false}
	three := append(failover[:2:2], monitoring.Endpoint{URL: "http://vmauth-3:8427/opentelemetry"})
	e := func(ids ...int) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = fmt.Sprintf("%s/%d", ExporterOTLPHTTP, id)
		}
		return out
	}
	for name, tc := range map[string]struct {
		endpoints []monitoring.Endpoint
		mode      WriteMode
		dir       string
		queued    []string
		failFast  []string
		wantDir   string
	}{
		"single":        {endpoint, "", "", []string{ExporterOTLPHTTP}, nil, DefaultStorageDir},
		"single dir":    {endpoint, "", "/srv/queue", []string{ExporterOTLPHTTP}, nil, "/srv/queue"},
		"single all":    {endpoint, WriteAll, "", []string{ExporterOTLPHTTP}, nil, DefaultStorageDir},
		"two":           {failover, "", "", e(1), e(0), DefaultStorageDir},
		"three":         {three, WriteFailover, "/srv/queue", e(2), e(0, 1), "/srv/queue"},
		"two all":       {failover, WriteAll, "", e(0, 1), nil, DefaultStorageDir},
		"three all dir": {three, WriteAll, "/srv/queue", e(0, 1, 2), nil, "/srv/queue"},
	} {
		out, err := Render(vm(), kinds, builtin(t), tc.endpoints, Options{StorageDir: tc.dir, WriteMode: tc.mode})
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		var cfg renderedConfig
		if err := yaml.Unmarshal(out, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cfg.Exporters) != len(tc.queued)+len(tc.failFast) {
			t.Errorf("%s: %d exporters, want %d", name, len(cfg.Exporters), len(tc.queued)+len(tc.failFast))
		}
		for _, id := range tc.queued {
			q := cfg.Exporters[id]
			if !reflect.DeepEqual(q["sending_queue"], diskQueue) || !reflect.DeepEqual(q["retry_on_failure"], forever) {
				t.Errorf("%s: %s queue = %v, retry = %v", name, id, q["sending_queue"], q["retry_on_failure"])
			}
		}
		for _, id := range tc.failFast {
			e := cfg.Exporters[id]
			if !reflect.DeepEqual(e["sending_queue"], off) || !reflect.DeepEqual(e["retry_on_failure"], off) {
				t.Errorf("%s: %s queue = %v, retry = %v; want both off", name, id, e["sending_queue"], e["retry_on_failure"])
			}
		}
		storage := cfg.Extensions[ExtensionQueueStorage]
		if storage["directory"] != tc.wantDir || storage["create_directory"] != true {
			t.Errorf("%s: storage = %v, want directory %s", name, storage, tc.wantDir)
		}
		if !slices.Contains(cfg.Service.Extensions, ExtensionQueueStorage) {
			t.Errorf("%s: service extensions %v miss %s", name, cfg.Service.Extensions, ExtensionQueueStorage)
		}
	}
}

// TestRenderWriteModeKeepsSources: the write mode changes only where samples
// go. The receivers, the processors (memory limiter, top processes) and the
// metrics pipeline's receivers and processors of a GPU VM render the same with
// one endpoint, with several in failover and with several in WriteAll.
func TestRenderWriteModeKeepsSources(t *testing.T) {
	type sources struct {
		Receivers  map[string]any `yaml:"receivers"`
		Processors map[string]any `yaml:"processors"`
		Service    struct {
			Pipelines struct {
				Metrics struct {
					Receivers  []string `yaml:"receivers"`
					Processors []string `yaml:"processors"`
				} `yaml:"metrics"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	render := func(endpoints []monitoring.Endpoint, mode WriteMode) sources {
		out, err := Render(gpuVM(), kinds, builtin(t), endpoints, Options{WriteMode: mode})
		if err != nil {
			t.Fatalf("Render %s: %v", mode, err)
		}
		var s sources
		if err := yaml.Unmarshal(out, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	want := render(endpoint, "")
	if _, ok := want.Processors[MemoryLimiterProcessor]; !ok || want.Service.Pipelines.Metrics.Processors[0] != MemoryLimiterProcessor {
		t.Fatalf("single endpoint: memory limiter missing or not first: %v", want.Service.Pipelines.Metrics.Processors)
	}
	for _, mode := range []WriteMode{WriteFailover, WriteAll} {
		if got := render(failover, mode); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: sources = %+v, want %+v", mode, got, want)
		}
	}
}

// logsEndpoint and tracesEndpoint are a logs and a traces write endpoint, as
// VictoriaLogs and VictoriaTraces take OTLP.
var (
	logsEndpoint   = []monitoring.Endpoint{{URL: "http://vlinsert:9481/insert/opentelemetry/v1/logs"}}
	tracesEndpoint = []monitoring.Endpoint{{URL: "http://vtinsert:10481/insert/opentelemetry/v1/traces"}}
)

// twoLogs and twoTraces are two logs and two traces write endpoints, as a
// VictoriaLogs and a VictoriaTraces on each of two nodes.
var (
	twoLogs = []monitoring.Endpoint{
		{URL: "http://vl-a:9428/insert/opentelemetry/v1/logs"},
		{URL: "http://vl-b:9428/insert/opentelemetry/v1/logs"},
	}
	twoTraces = []monitoring.Endpoint{
		{URL: "http://vt-a:10428/insert/opentelemetry/v1/traces"},
		{URL: "http://vt-b:10428/insert/opentelemetry/v1/traces"},
	}
)

// TestRenderLogsTracesSnapshot pins the logs and traces pipelines: one
// endpoint each on Linux; two each with a write account in failover; two each
// in WriteAll; and a Windows target, which gets traces but no logs.
func TestRenderLogsTracesSnapshot(t *testing.T) {
	account := &monitoring.BasicAuth{Username: "writer", PasswordEnv: WritePasswordEnv}
	two := func(a, b string) []monitoring.Endpoint {
		return []monitoring.Endpoint{{URL: a, BasicAuth: account}, {URL: b, BasicAuth: account}}
	}
	windows := vm()
	windows.OS = "windows"
	for golden, tc := range map[string]struct {
		target    monitoring.Target
		endpoints []monitoring.Endpoint
		opts      Options
	}{
		"vm-logs-traces.golden.yaml": {vm(), endpoint, Options{LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint}},
		"vm-logs-traces-failover-auth.golden.yaml": {vm(), two("https://vm-a:8427/opentelemetry/v1/metrics", "https://vm-b:8427/opentelemetry/v1/metrics"), Options{
			LogsEndpoints:   two("https://vm-a:9428/insert/opentelemetry/v1/logs", "https://vm-b:9428/insert/opentelemetry/v1/logs"),
			TracesEndpoints: two("https://vm-a:10428/insert/opentelemetry/v1/traces", "https://vm-b:10428/insert/opentelemetry/v1/traces"),
		}},
		"vm-logs-traces-write-all.golden.yaml": {vm(), failover, Options{WriteMode: WriteAll, LogsEndpoints: twoLogs, TracesEndpoints: twoTraces}},
		"windows-logs-traces.golden.yaml":      {windows, endpoint, Options{LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint}},
	} {
		got, err := Render(tc.target, kinds, builtin(t), tc.endpoints, tc.opts)
		if err != nil {
			t.Fatalf("%s: Render: %v", golden, err)
		}
		checkGolden(t, golden, got)
	}
}

// signalsConfig is the parts of a rendered configuration the signal tests
// look at.
type signalsConfig struct {
	Receivers  map[string]any            `yaml:"receivers"`
	Exporters  map[string]map[string]any `yaml:"exporters"`
	Extensions map[string]map[string]any `yaml:"extensions"`
	Connectors map[string]any            `yaml:"connectors"`
	Service    struct {
		Pipelines map[string]struct {
			Receivers  []string `yaml:"receivers"`
			Processors []string `yaml:"processors"`
			Exporters  []string `yaml:"exporters"`
		} `yaml:"pipelines"`
	} `yaml:"service"`
}

// TestRenderSignals: each signal is a pipeline of its own only when it has
// endpoints, with its own disk queue directory, the memory limiter first, the
// identity and batch processors, and the journal on Linux only.
func TestRenderSignals(t *testing.T) {
	windows := vm()
	windows.OS = "Windows"
	for name, tc := range map[string]struct {
		target     monitoring.Target
		opts       Options
		wantLogs   bool
		wantTraces bool
	}{
		"metrics only": {vm(), Options{}, false, false},
		"logs":         {vm(), Options{LogsEndpoints: logsEndpoint}, true, false},
		"traces":       {vm(), Options{TracesEndpoints: tracesEndpoint}, false, true},
		"both":         {vm(), Options{LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint, StorageDir: "/srv/q"}, true, true},
		"windows":      {windows, Options{LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint}, false, true},
	} {
		out, err := Render(tc.target, kinds, builtin(t), endpoint, tc.opts)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		var cfg signalsConfig
		if err := yaml.Unmarshal(out, &cfg); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		logs, hasLogs := cfg.Service.Pipelines["logs"]
		traces, hasTraces := cfg.Service.Pipelines["traces"]
		_, hasJournald := cfg.Receivers[JournaldReceiver]
		_, hasOTLP := cfg.Receivers[OTLPReceiver]
		if hasLogs != tc.wantLogs || hasJournald != tc.wantLogs || hasTraces != tc.wantTraces || hasOTLP != tc.wantTraces {
			t.Errorf("%s: logs %v journald %v traces %v otlp %v; want logs %v traces %v", name, hasLogs, hasJournald, hasTraces, hasOTLP, tc.wantLogs, tc.wantTraces)
		}
		if slices.Contains(cfg.Service.Pipelines["metrics"].Receivers, JournaldReceiver) || slices.Contains(cfg.Service.Pipelines["metrics"].Receivers, OTLPReceiver) {
			t.Errorf("%s: metrics pipeline takes %v", name, cfg.Service.Pipelines["metrics"].Receivers)
		}
		if p := cfg.Service.Pipelines["metrics"].Processors; len(p) == 0 || p[0] != MemoryLimiterProcessor {
			t.Errorf("%s: metrics processors %v do not start with the memory limiter", name, p)
		}
		want := []string{MemoryLimiterProcessor, monitoring.IdentityProcessor, BatchProcessor}
		if hasLogs && (!slices.Equal(logs.Processors, want) || !slices.Equal(logs.Receivers, []string{JournaldReceiver}) || !slices.Equal(logs.Exporters, []string{ExporterOTLPHTTP + "/logs"})) {
			t.Errorf("%s: logs pipeline %+v", name, logs)
		}
		if hasTraces && (!slices.Equal(traces.Processors, want) || !slices.Equal(traces.Receivers, []string{OTLPReceiver}) || !slices.Equal(traces.Exporters, []string{ExporterOTLPHTTP + "/traces"})) {
			t.Errorf("%s: traces pipeline %+v", name, traces)
		}
		dirs := map[string]string{}
		for id, ext := range cfg.Extensions {
			if !strings.HasPrefix(id, "file_storage/") {
				continue
			}
			dir, _ := ext["directory"].(string)
			if other, ok := dirs[dir]; ok {
				t.Errorf("%s: %s and %s share %s", name, id, other, dir)
			}
			dirs[dir] = id
		}
		base := tc.opts.StorageDir
		if base == "" {
			base = DefaultStorageDir
		}
		if tc.wantLogs && (dirs[base+"/logs"] != ExtensionQueueStorage+"_logs" || dirs[base+"/journald"] != ExtensionJournaldStorage) {
			t.Errorf("%s: logs queue directories %v", name, dirs)
		}
		if tc.wantTraces && dirs[base+"/traces"] != ExtensionQueueStorage+"_traces" {
			t.Errorf("%s: traces queue directories %v", name, dirs)
		}
		for sig, url := range map[string]string{"logs": logsEndpoint[0].URL, "traces": tracesEndpoint[0].URL} {
			e, ok := cfg.Exporters[ExporterOTLPHTTP+"/"+sig]
			if !ok {
				continue
			}
			q, _ := e["sending_queue"].(map[string]any)
			if q["storage"] != ExtensionQueueStorage+"_"+sig || e[sig+"_endpoint"] != url || e["metrics_endpoint"] != nil {
				t.Errorf("%s: %s exporter %v", name, sig, e)
			}
		}
	}
}

// TestRenderSignalsWriteMode: logs and traces use several endpoints as the
// metrics do. In failover only the last exporter of each signal keeps what it
// cannot send on disk, behind a failover connector of its own; in WriteAll
// every exporter does, all of them in the signal's pipeline, and there is no
// connector. Each signal's queues stay in its own file storage extension.
func TestRenderSignalsWriteMode(t *testing.T) {
	for _, mode := range []WriteMode{WriteFailover, WriteAll} {
		out, err := Render(vm(), kinds, builtin(t), failover, Options{WriteMode: mode, LogsEndpoints: twoLogs, TracesEndpoints: twoTraces})
		if err != nil {
			t.Fatalf("%s: Render: %v", mode, err)
		}
		var cfg signalsConfig
		if err := yaml.Unmarshal(out, &cfg); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if len(cfg.Exporters) != 6 {
			t.Errorf("%s: %d exporters, want 6", mode, len(cfg.Exporters))
		}
		for _, sig := range []string{"logs", "traces"} {
			e0, e1 := ExporterOTLPHTTP+"/"+sig+"_0", ExporterOTLPHTTP+"/"+sig+"_1"
			storage := ExtensionQueueStorage + "_" + sig
			queueOf := func(id string) any {
				q, _ := cfg.Exporters[id]["sending_queue"].(map[string]any)
				return q["storage"]
			}
			connector := ConnectorFailover + "/" + sig
			_, hasConnector := cfg.Connectors[connector]
			heads := cfg.Service.Pipelines[sig].Exporters
			switch mode {
			case WriteFailover:
				if queueOf(e0) != nil || queueOf(e1) != storage {
					t.Errorf("%s %s: queues %v, %v; want none and %s", mode, sig, queueOf(e0), queueOf(e1), storage)
				}
				if !hasConnector || !slices.Equal(heads, []string{connector}) {
					t.Errorf("%s %s: pipeline exports to %v, connector %v", mode, sig, heads, hasConnector)
				}
				if p := cfg.Service.Pipelines[sig+"/1"]; !slices.Equal(p.Receivers, []string{connector}) || !slices.Equal(p.Exporters, []string{e1}) {
					t.Errorf("%s %s: priority pipeline %+v", mode, sig, p)
				}
			case WriteAll:
				if queueOf(e0) != storage || queueOf(e1) != storage {
					t.Errorf("%s %s: queues %v, %v; want %s on both", mode, sig, queueOf(e0), queueOf(e1), storage)
				}
				if hasConnector || !slices.Equal(heads, []string{e0, e1}) {
					t.Errorf("%s %s: pipeline exports to %v, connector %v", mode, sig, heads, hasConnector)
				}
			}
			if p := cfg.Service.Pipelines[sig].Processors; len(p) == 0 || p[0] != MemoryLimiterProcessor {
				t.Errorf("%s %s: processors %v do not start with the memory limiter", mode, sig, p)
			}
			if d := cfg.Extensions[storage]["directory"]; d != DefaultStorageDir+"/"+sig {
				t.Errorf("%s %s: queue directory %v", mode, sig, d)
			}
		}
	}
}

// TestRenderSignalsAccount: the write account signs the logs and traces
// exporters too, each with a basic auth extension of its own whose password
// is an environment reference.
func TestRenderSignalsAccount(t *testing.T) {
	account := &monitoring.BasicAuth{Username: "writer", PasswordEnv: WritePasswordEnv}
	signed := func(in []monitoring.Endpoint) []monitoring.Endpoint {
		out := slices.Clone(in)
		for i := range out {
			out[i].BasicAuth = account
		}
		return out
	}
	out, err := Render(vm(), kinds, builtin(t), signed(endpoint), Options{LogsEndpoints: signed(twoLogs), TracesEndpoints: signed(tracesEndpoint)})
	if err != nil {
		t.Fatal(err)
	}
	var cfg signalsConfig
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		ExporterOTLPHTTP:             ExtensionBasicAuth,
		ExporterOTLPHTTP + "/logs_0": ExtensionBasicAuth + "/logs_0",
		ExporterOTLPHTTP + "/logs_1": ExtensionBasicAuth + "/logs_1",
		ExporterOTLPHTTP + "/traces": ExtensionBasicAuth + "/traces",
	}
	if len(cfg.Exporters) != len(want) {
		t.Errorf("%d exporters, want %d", len(cfg.Exporters), len(want))
	}
	for exporter, authID := range want {
		auth, _ := cfg.Exporters[exporter]["auth"].(map[string]any)
		client, _ := cfg.Extensions[authID]["client_auth"].(map[string]any)
		if auth["authenticator"] != authID || client["username"] != "writer" || client["password"] != "${env:"+WritePasswordEnv+"}" {
			t.Errorf("%s: auth %v, %s %v", exporter, auth, authID, cfg.Extensions[authID])
		}
	}
}

func TestRenderSignalRejects(t *testing.T) {
	otlpKind := fixedKind{frag: monitoring.ConfigFragment{Receivers: map[string]any{OTLPReceiver: map[string]any{}}}}
	for name, tc := range map[string]struct {
		kinds []monitoring.CollectorKind
		opts  Options
	}{
		"logs endpoint no URL":  {kinds, Options{LogsEndpoints: []monitoring.Endpoint{{}}}},
		"traces account no env": {kinds, Options{TracesEndpoints: []monitoring.Endpoint{{URL: "http://vt", BasicAuth: &monitoring.BasicAuth{Username: "writer"}}}}},
		"otlp receiver clashes": {[]monitoring.CollectorKind{Hostmetrics{}, otlpKind}, Options{TracesEndpoints: tracesEndpoint}},
		"logs unknown mode":     {kinds, Options{LogsEndpoints: twoLogs, WriteMode: "round-robin"}},
	} {
		if _, err := Render(vm(), tc.kinds, builtin(t), endpoint, tc.opts); err == nil {
			t.Errorf("%s: Render succeeded", name)
		}
	}
}

// writeCA reads the test CA, a certificate made for these tests only.
func writeCA(t *testing.T) string {
	t.Helper()
	pem, err := os.ReadFile(filepath.Join("testdata", "write-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	return string(pem)
}

// withAccess is in with every endpoint written to as access says, URL kept.
func withAccess(in []monitoring.Endpoint, access monitoring.Endpoint) []monitoring.Endpoint {
	out := make([]monitoring.Endpoint, len(in))
	for i, e := range in {
		out[i] = access
		out[i].URL = e.URL
	}
	return out
}

// TestRenderBearerCASnapshot pins two endpoints per signal in failover, each
// signed with a bearer token from the environment and verified against a
// private CA given inline.
func TestRenderBearerCASnapshot(t *testing.T) {
	access := monitoring.Endpoint{BearerTokenEnv: WriteTokenEnv, TLS: &monitoring.EndpointTLS{CAPEM: writeCA(t)}}
	got, err := Render(vm(), kinds, builtin(t), withAccess(failover, access), Options{
		LogsEndpoints:   withAccess(twoLogs, access),
		TracesEndpoints: withAccess(twoTraces, access),
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	checkGolden(t, "vm-logs-traces-failover-bearer-ca.golden.yaml", got)
}

// TestRenderBearerTLS: with a bearer token and a CA, every exporter of every
// signal, in either mode and with one endpoint or several, has a bearer token
// extension of its own, named as the basic auth one would be, whose token is
// an environment reference, and the CA inline; no basic auth extension is
// rendered. insecure_skip_verify is rendered only when asked for.
func TestRenderBearerTLS(t *testing.T) {
	ca := writeCA(t)
	for _, tc := range []struct {
		name   string
		access monitoring.Endpoint
		tls    map[string]any
	}{
		{"ca", monitoring.Endpoint{BearerTokenEnv: WriteTokenEnv, TLS: &monitoring.EndpointTLS{CAPEM: ca}}, map[string]any{"ca_pem": ca}},
		{"insecure", monitoring.Endpoint{BearerTokenEnv: WriteTokenEnv, TLS: &monitoring.EndpointTLS{InsecureSkipVerify: true}}, map[string]any{"insecure_skip_verify": true}},
		{"no tls", monitoring.Endpoint{BearerTokenEnv: WriteTokenEnv}, nil},
	} {
		for _, mode := range []WriteMode{WriteFailover, WriteAll} {
			for _, several := range []bool{false, true} {
				metrics, logs, traces := endpoint, logsEndpoint, tracesEndpoint
				suffixes := []string{""}
				if several {
					metrics, logs, traces = failover, twoLogs, twoTraces
					suffixes = []string{"0", "1"}
				}
				out, err := Render(vm(), kinds, builtin(t), withAccess(metrics, tc.access), Options{
					WriteMode:       mode,
					LogsEndpoints:   withAccess(logs, tc.access),
					TracesEndpoints: withAccess(traces, tc.access),
				})
				if err != nil {
					t.Fatalf("%s %s several=%v: Render: %v", tc.name, mode, several, err)
				}
				var cfg signalsConfig
				if err := yaml.Unmarshal(out, &cfg); err != nil {
					t.Fatal(err)
				}
				want := map[string]string{}
				for _, sig := range []string{"metrics", "logs", "traces"} {
					for _, n := range suffixes {
						var id string
						switch {
						case sig == "metrics" && n == "":
						case sig == "metrics":
							id = "/" + n
						case n == "":
							id = "/" + sig
						default:
							id = "/" + sig + "_" + n
						}
						want[ExporterOTLPHTTP+id] = ExtensionBearerTokenAuth + id
					}
				}
				if len(cfg.Exporters) != len(want) {
					t.Errorf("%s %s several=%v: %d exporters, want %d", tc.name, mode, several, len(cfg.Exporters), len(want))
				}
				for exporter, authID := range want {
					e := cfg.Exporters[exporter]
					auth, _ := e["auth"].(map[string]any)
					if auth["authenticator"] != authID || !reflect.DeepEqual(cfg.Extensions[authID], map[string]any{"token": "${env:" + WriteTokenEnv + "}"}) {
						t.Errorf("%s %s several=%v: %s: auth %v, %s %v", tc.name, mode, several, exporter, auth, authID, cfg.Extensions[authID])
					}
					tls, _ := e["tls"].(map[string]any)
					if !reflect.DeepEqual(tls, tc.tls) {
						t.Errorf("%s %s several=%v: %s: tls %v, want %v", tc.name, mode, several, exporter, tls, tc.tls)
					}
				}
				for id := range cfg.Extensions {
					if strings.HasPrefix(id, ExtensionBasicAuth) {
						t.Errorf("%s %s several=%v: basic auth extension %s rendered", tc.name, mode, several, id)
					}
				}
			}
		}
	}
}

// TestRenderWriteAccountTLS: the CA goes with a basic auth account as well,
// and an endpoint with both an account and a bearer token is refused, of any
// signal.
func TestRenderWriteAccountTLS(t *testing.T) {
	ca := writeCA(t)
	account := monitoring.Endpoint{
		BasicAuth: &monitoring.BasicAuth{Username: "writer", PasswordEnv: WritePasswordEnv},
		TLS:       &monitoring.EndpointTLS{CAPEM: ca},
	}
	out, err := Render(vm(), kinds, builtin(t), withAccess(endpoint, account), Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var cfg signalsConfig
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatal(err)
	}
	e := cfg.Exporters[ExporterOTLPHTTP]
	auth, _ := e["auth"].(map[string]any)
	tls, _ := e["tls"].(map[string]any)
	if auth["authenticator"] != ExtensionBasicAuth || tls["ca_pem"] != ca {
		t.Errorf("exporter %v", e)
	}
	both := account
	both.BearerTokenEnv = WriteTokenEnv
	for name, tc := range map[string]struct {
		metrics []monitoring.Endpoint
		opts    Options
	}{
		"metrics": {withAccess(endpoint, both), Options{}},
		"logs":    {endpoint, Options{LogsEndpoints: withAccess(logsEndpoint, both)}},
		"traces":  {endpoint, Options{TracesEndpoints: withAccess(twoTraces, both), WriteMode: WriteAll}},
	} {
		if _, err := Render(vm(), kinds, builtin(t), tc.metrics, tc.opts); err == nil {
			t.Errorf("%s: an endpoint with both an account and a bearer token rendered", name)
		}
	}
}
