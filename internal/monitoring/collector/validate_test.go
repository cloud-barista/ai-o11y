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
	for name, target := range map[string]monitoring.Target{"linux vm": vm(), "windows vm": windows} {
		out, err := Render(target, kinds, builtin(t), endpoint)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, out, 0o600); err != nil {
			t.Fatal(err)
		}
		if msg, err := exec.Command(bin, "validate", "--config", path).CombinedOutput(); err != nil {
			t.Errorf("%s: collector refused the rendered configuration: %v\n%s", name, err, msg)
		}
	}
}
