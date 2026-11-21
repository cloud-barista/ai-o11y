package collector

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// TestRenderedConfigValidates runs the ai-o11y collector's own validate on
// the configuration rendered for each kind of target, so a renderer change
// that the collector would refuse fails here. It needs the collector binary:
// `make -C otelcol validate` builds it and sets AIO11Y_OTELCOL_BIN.
func TestRenderedConfigValidates(t *testing.T) {
	bin := os.Getenv("AIO11Y_OTELCOL_BIN")
	if bin == "" {
		t.Skip("AIO11Y_OTELCOL_BIN is not set; run make -C otelcol validate")
	}
	windows := vm()
	windows.OS = "windows"
	telemetryPorts := map[string]int{"gpu vm": 18889}
	account := &monitoring.BasicAuth{Username: "writer", PasswordEnv: WritePasswordEnv}
	authed := []monitoring.Endpoint{{URL: failover[0].URL, BasicAuth: account}}
	authedTwo := []monitoring.Endpoint{{URL: twoTraces[0].URL, BasicAuth: account}, {URL: twoTraces[1].URL, BasicAuth: account}}
	endpoints := map[string][]monitoring.Endpoint{
		"failover vm": failover, "write-all vm": failover, "write-all gpu vm": failover, "account vm": authed,
		"signals failover vm": failover, "signals write-all vm": failover,
	}
	opts := map[string]Options{
		"write-all vm":         {WriteMode: WriteAll},
		"write-all gpu vm":     {WriteMode: WriteAll},
		"signals vm":           {LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint},
		"signals failover vm":  {LogsEndpoints: twoLogs, TracesEndpoints: authedTwo},
		"signals write-all vm": {WriteMode: WriteAll, LogsEndpoints: twoLogs, TracesEndpoints: authedTwo},
		"signals windows vm":   {LogsEndpoints: logsEndpoint, TracesEndpoints: tracesEndpoint},
	}
	for name, target := range map[string]monitoring.Target{
		"linux vm": vm(), "windows vm": windows, "gpu vm": gpuVM(), "amd gpu vm": amdGPUVM(), "failover vm": vm(), "write-all vm": vm(), "write-all gpu vm": gpuVM(), "account vm": vm(),
		"signals vm": vm(), "signals failover vm": vm(), "signals write-all vm": vm(), "signals windows vm": windows,
	} {
		t.Run(name, func(t *testing.T) {
			ep := endpoint
			if e, ok := endpoints[name]; ok {
				ep = e
			}
			o := opts[name]
			o.TelemetryPort = telemetryPorts[name]
			out, err := Render(target, kinds, builtin(t), ep, o)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatal(err)
			}
			if msg, err := exec.Command(bin, "validate", "--config", path).CombinedOutput(); err != nil {
				t.Errorf("collector refused the rendered configuration: %v\n%s", err, msg)
			}
		})
	}
}
