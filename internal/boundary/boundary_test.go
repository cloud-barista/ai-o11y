// Package boundary holds checks on what ai-o11y may depend on.
package boundary

import (
	"os/exec"
	"strings"
	"testing"
)

// forbidden are import paths the ai-o11y service must not use: the collector
// is built on its own from otelcol/ (OpenTelemetry Collector Builder), and the
// service only renders its configuration.
var forbidden = []string{
	"github.com/cloud-barista/ai-o11y/otelcol",
	"go.opentelemetry.io/collector/",
	"github.com/open-telemetry/opentelemetry-collector-contrib/",
}

// TestServiceDoesNotImportCollector checks every package of the module,
// tests included, imports none of the collector's packages.
func TestServiceDoesNotImportCollector(t *testing.T) {
	out, err := exec.Command("go", "list", "-f",
		`{{.ImportPath}}{{range .Imports}} {{.}}{{end}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}`,
		"github.com/cloud-barista/ai-o11y/...").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	pkgs := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pkgs++
		if strings.HasPrefix(fields[0], "github.com/cloud-barista/ai-o11y/otelcol") {
			t.Errorf("package %s is part of the service module; otelcol/ must stay outside it", fields[0])
		}
		for _, imp := range fields[1:] {
			for _, f := range forbidden {
				if strings.HasPrefix(imp, f) || imp == strings.TrimSuffix(f, "/") {
					t.Errorf("%s imports %s", fields[0], imp)
				}
			}
		}
	}
	if pkgs < 5 {
		t.Fatalf("go list found only %d packages; the check is not looking at the module", pkgs)
	}
}
