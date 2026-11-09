package thanos

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/query"
)

func renderer(t *testing.T) query.PromQL {
	t.Helper()
	c, err := catalog.New(catalog.Builtin())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	return query.PromQL{Catalog: c}
}

func TestQuerySendsThanosParams(t *testing.T) {
	var (
		mu    sync.Mutex
		forms []url.Values
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		forms = append(forms, r.PostForm)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	b, err := New(Config{QueryURLs: []string{srv.URL}, Limits: query.ChunkLimits{MaxIDs: 2}}, renderer(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q := monitoring.QuerySpec{Metric: "system.cpu.utilization", Targets: monitoring.TargetSet{
		{EntityID: "k1", Kind: "vm"}, {EntityID: "k2", Kind: "vm"}, {EntityID: "k3", Kind: "vm"},
	}}
	if _, err := b.Query(context.Background(), q); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(forms) != 2 {
		t.Fatalf("requests = %d, want 2 chunks of at most 2 ids", len(forms))
	}
	for _, f := range forms {
		if f.Get("dedup") != "true" || f.Get("partial_response") != "false" || f.Get("timeout") != "120" {
			t.Errorf("params = %v, want dedup on, partial response off, default timeout", f)
		}
	}
}

func TestDefaults(t *testing.T) {
	b, err := New(Config{QueryURLs: []string{"http://thanos-query:10902"}, Write: monitoring.Endpoint{URL: "http://receive:19291/otlp"}}, renderer(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.planner.Limits; got.MaxIDs != DefaultMaxIDs || got.MaxConcurrent != DefaultMaxConcurrent {
		t.Errorf("limits = %+v", got)
	}
	if got := b.WriteEndpoint(); got.Protocol != "otlp_http" || got.URL != "http://receive:19291/otlp" {
		t.Errorf("write endpoint = %+v", got)
	}
	if _, err := New(Config{}, renderer(t)); err == nil {
		t.Error("New without a query URL succeeded")
	}
}
