package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/config"
	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/collector"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/query"
)

// TestBackendQueryAuthAndTLS checks each provider sends the configured
// account over https verified with the configured CA, under the address's
// path prefix.
func TestBackendQueryAuthAndTLS(t *testing.T) {
	var path, auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	}))
	defer srv.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		backend  config.BackendConfig
		wantAuth string
	}{
		{"bearer", config.BackendConfig{QueryBearerToken: "read-token"}, "Bearer read-token"},
		{"basic", config.BackendConfig{QueryUsername: "reader", QueryPassword: "pw"}, "Basic cmVhZGVyOnB3"},
		{"none", config.BackendConfig{}, ""},
	}
	for _, provider := range []string{config.BackendThanos, config.BackendVictoria} {
		for _, c := range cases {
			bc := c.backend
			bc.Provider = provider
			bc.QueryURLs = []string{srv.URL + "/metrics"}
			bc.QueryTLSCAFile = caFile
			b, err := newBackend(&config.Config{Backend: bc}, query.PromQL{}, monitoring.Endpoint{})
			if err != nil {
				t.Fatalf("%s %s: newBackend: %v", provider, c.name, err)
			}
			path, auth = "", ""
			if _, err := b.Series(context.Background(), "up"); err != nil {
				t.Fatalf("%s %s: Series: %v", provider, c.name, err)
			}
			if path != "/metrics/api/v1/series" || auth != c.wantAuth {
				t.Errorf("%s %s: path %q, Authorization %q, want /metrics/api/v1/series and %q", provider, c.name, path, auth, c.wantAuth)
			}
		}
	}
}

func TestBackendUnreadableCAFails(t *testing.T) {
	bc := config.BackendConfig{
		Provider:       config.BackendVictoria,
		QueryURLs:      []string{"https://q:8443/metrics"},
		QueryTLSCAFile: filepath.Join(t.TempDir(), "absent.pem"),
	}
	if _, err := newBackend(&config.Config{Backend: bc}, query.PromQL{}, monitoring.Endpoint{}); err == nil {
		t.Error("newBackend with a missing CA file succeeded")
	}
}

// TestCollectorOptionsSignals checks the logs and traces write addresses
// reach the renderer with the write account and the write mode.
func TestCollectorOptionsSignals(t *testing.T) {
	cfg := &config.Config{
		Backend: config.BackendConfig{
			LogsWriteURLs:   []string{"https://a:4318/insert/opentelemetry/v1/logs", "https://b:4318/insert/opentelemetry/v1/logs"},
			TracesWriteURLs: []string{"https://a:4318/insert/opentelemetry/v1/traces"},
			WriteUsername:   "writer",
			WriteMode:       config.WriteAll,
		},
		Collector: config.CollectorConfig{StorageDir: "/srv/q"},
	}
	deps, err := newDeps(cfg, monitoring.NewRegistry[monitoring.TargetSource]("target source"))
	if err != nil {
		t.Fatalf("newDeps: %v", err)
	}
	account := &monitoring.BasicAuth{Username: "writer", PasswordEnv: collector.WritePasswordEnv}
	want := collector.Options{
		StorageDir: "/srv/q",
		WriteMode:  collector.WriteAll,
		LogsEndpoints: []monitoring.Endpoint{
			{URL: "https://a:4318/insert/opentelemetry/v1/logs", BasicAuth: account},
			{URL: "https://b:4318/insert/opentelemetry/v1/logs", BasicAuth: account},
		},
		TracesEndpoints: []monitoring.Endpoint{{URL: "https://a:4318/insert/opentelemetry/v1/traces", BasicAuth: account}},
	}
	if !reflect.DeepEqual(deps.CollectorOptions, want) {
		t.Errorf("CollectorOptions = %+v, want %+v", deps.CollectorOptions, want)
	}
	cfg.Backend = config.BackendConfig{}
	deps, err = newDeps(cfg, monitoring.NewRegistry[monitoring.TargetSource]("target source"))
	if err != nil {
		t.Fatalf("newDeps: %v", err)
	}
	if len(deps.CollectorOptions.LogsEndpoints) != 0 || len(deps.CollectorOptions.TracesEndpoints) != 0 {
		t.Errorf("no logs or traces address rendered endpoints: %+v", deps.CollectorOptions)
	}
}

// TestWriteAccess checks how collectors are told to write: the account's
// username or the bearer token's variable, never a secret, and the CA file's
// contents inline. An account with a bearer token, a CA file that cannot be
// read and one with no PEM certificate are refused.
func TestWriteAccess(t *testing.T) {
	dir := t.TempDir()
	caPEM := testCAPEM()
	caFile := filepath.Join(dir, "ca.pem")
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notPEM, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := []struct {
		name    string
		backend config.BackendConfig
		want    monitoring.Endpoint
	}{
		{"none", config.BackendConfig{}, monitoring.Endpoint{}},
		{"basic", config.BackendConfig{WriteUsername: "writer"},
			monitoring.Endpoint{BasicAuth: &monitoring.BasicAuth{Username: "writer", PasswordEnv: collector.WritePasswordEnv}}},
		{"bearer", config.BackendConfig{WriteBearer: true}, monitoring.Endpoint{BearerTokenEnv: collector.WriteTokenEnv}},
		{"bearer ca", config.BackendConfig{WriteBearer: true, WriteTLSCAFile: caFile},
			monitoring.Endpoint{BearerTokenEnv: collector.WriteTokenEnv, TLS: &monitoring.EndpointTLS{CAPEM: string(caPEM)}}},
		{"basic insecure", config.BackendConfig{WriteUsername: "writer", WriteTLSInsecureSkipVerify: true}, monitoring.Endpoint{
			BasicAuth: &monitoring.BasicAuth{Username: "writer", PasswordEnv: collector.WritePasswordEnv},
			TLS:       &monitoring.EndpointTLS{InsecureSkipVerify: true},
		}},
	}
	for _, c := range ok {
		got, err := writeAccess(c.backend)
		if err != nil {
			t.Errorf("%s: writeAccess: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: writeAccess = %+v, want %+v", c.name, got, c.want)
		}
	}
	for name, bc := range map[string]config.BackendConfig{
		"basic and bearer": {WriteUsername: "writer", WriteBearer: true},
		"missing CA file":  {WriteBearer: true, WriteTLSCAFile: filepath.Join(dir, "absent.pem")},
		"CA file not PEM":  {WriteBearer: true, WriteTLSCAFile: notPEM},
	} {
		if _, err := writeAccess(bc); err == nil {
			t.Errorf("%s: writeAccess succeeded", name)
		}
		if _, err := newDeps(&config.Config{Backend: bc}, monitoring.NewRegistry[monitoring.TargetSource]("target source")); err == nil {
			t.Errorf("%s: newDeps succeeded", name)
		}
	}
}

// TestCollectorWriteBearerCA checks the bearer token and the CA reach every
// write address of every signal: the metrics ones through the backend, the
// logs and traces ones through the collector options.
func TestCollectorWriteBearerCA(t *testing.T) {
	caPEM := testCAPEM()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Backend: config.BackendConfig{
		Provider:        config.BackendVictoria,
		QueryURLs:       []string{"https://a:8443/metrics"},
		WriteURLs:       []string{"https://a:8443/opentelemetry/v1/metrics", "https://b:8443/opentelemetry/v1/metrics"},
		LogsWriteURLs:   []string{"https://a:8443/insert/opentelemetry/v1/logs"},
		TracesWriteURLs: []string{"https://a:8443/insert/opentelemetry/v1/traces"},
		WriteBearer:     true,
		WriteTLSCAFile:  caFile,
	}}
	deps, err := newDeps(cfg, monitoring.NewRegistry[monitoring.TargetSource]("target source"))
	if err != nil {
		t.Fatalf("newDeps: %v", err)
	}
	if deps.Backend == nil {
		t.Fatal("no backend")
	}
	all := append(deps.Backend.WriteEndpoints(), deps.CollectorOptions.LogsEndpoints...)
	all = append(all, deps.CollectorOptions.TracesEndpoints...)
	if len(all) != 4 {
		t.Fatalf("%d write endpoints, want 4: %+v", len(all), all)
	}
	for _, e := range all {
		if e.BasicAuth != nil || e.BearerTokenEnv != collector.WriteTokenEnv || e.TLS == nil || e.TLS.CAPEM != string(caPEM) || e.TLS.InsecureSkipVerify {
			t.Errorf("write endpoint %s: %+v", e.URL, e)
		}
	}
}

// testCAPEM is the PEM of the certificate httptest serves TLS with.
func testCAPEM() []byte {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}
