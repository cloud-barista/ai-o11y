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

func TestBackendProvider(t *testing.T) {
	t.Setenv("AIO11Y_BACKEND_PROVIDER", "")
	cfg, err := Load()
	if err != nil || cfg.Backend.Provider != BackendThanos {
		t.Fatalf("unset = %+v, %v, want thanos", cfg, err)
	}
	// Set beside the other backend settings, the provider still reads.
	t.Setenv("AIO11Y_BACKEND_PROVIDER", "victoria")
	t.Setenv("AIO11Y_BACKEND_QUERY_URL", "http://vmauth:8427")
	if cfg, err = Load(); err != nil || cfg.Backend.Provider != BackendVictoria || len(cfg.Backend.QueryURLs) != 1 {
		t.Fatalf("victoria = %+v, %v", cfg, err)
	}
	t.Setenv("AIO11Y_BACKEND_PROVIDER", "mimir")
	if _, err := Load(); err == nil {
		t.Error("an unknown provider loaded")
	}
}
