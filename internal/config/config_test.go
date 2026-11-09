package config

import (
	"testing"
	"time"
)

func TestQueryAccountSetTogether(t *testing.T) {
	t.Setenv("AIO11Y_BACKEND_QUERY_USERNAME", "reader")
	t.Setenv("AIO11Y_BACKEND_QUERY_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Error("a query username without a password loaded")
	}
	t.Setenv("AIO11Y_BACKEND_QUERY_PASSWORD", "pw")
	if cfg, err := Load(); err != nil || cfg.Backend.QueryUsername != "reader" {
		t.Errorf("Load = %v", err)
	}
}

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
	t.Setenv("AIO11Y_BACKEND_MAX_IDS", "")
	t.Setenv("AIO11Y_BACKEND_MAX_CONCURRENT", "8")
	if cfg, err = Load(); err != nil || cfg.Backend.MaxIDs != 0 || cfg.Backend.MaxConcurrent != 8 {
		t.Errorf("counts = %+v, %v, want empty for the default and 8", cfg, err)
	}
	for _, bad := range []string{"0", "-1", "many"} {
		t.Setenv("AIO11Y_BACKEND_MAX_IDS", bad)
		if _, err := Load(); err == nil {
			t.Errorf("AIO11Y_BACKEND_MAX_IDS=%q loaded", bad)
		}
	}
	t.Setenv("AIO11Y_BACKEND_MAX_IDS", "")
	for _, bad := range []string{"0", "0s", "-1s", "soon"} {
		t.Setenv("AIO11Y_BACKEND_CONNECT_TIMEOUT", bad)
		if _, err := Load(); err == nil {
			t.Errorf("AIO11Y_BACKEND_CONNECT_TIMEOUT=%q loaded", bad)
		}
	}
}
