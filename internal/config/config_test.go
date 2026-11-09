package config

import (
	"testing"
	"time"
)

func TestBackendTimeouts(t *testing.T) {
	t.Setenv("AIO11Y_BACKEND_TIMEOUT", "")
	t.Setenv("AIO11Y_BACKEND_CONNECT_TIMEOUT", "3s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend.Timeout != 0 || cfg.Backend.ConnectTimeout != 3*time.Second {
		t.Errorf("timeouts = %s, %s, want empty for the default and 3s", cfg.Backend.Timeout, cfg.Backend.ConnectTimeout)
	}
	for _, bad := range []string{"0", "0s", "-1s", "soon"} {
		t.Setenv("AIO11Y_BACKEND_CONNECT_TIMEOUT", bad)
		if _, err := Load(); err == nil {
			t.Errorf("AIO11Y_BACKEND_CONNECT_TIMEOUT=%q loaded", bad)
		}
	}
}
