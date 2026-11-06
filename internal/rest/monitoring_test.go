package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/config"
	"github.com/cloud-barista/ai-o11y/internal/model"
	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/backend/promapi"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/collector"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/mapping"
)

type memStore struct{ targets map[string]monitoring.Target }

func (m *memStore) Save(_ context.Context, t monitoring.Target) error {
	m.targets[t.EntityID] = t
	return nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	delete(m.targets, key)
	return nil
}

func (m *memStore) Select(_ context.Context, sel monitoring.Selector) ([]monitoring.Target, error) {
	var out []monitoring.Target
	for _, t := range m.targets {
		if sel.Matches(t) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b monitoring.Target) int { return strings.Compare(a.EntityID, b.EntityID) })
	return out, nil
}

func (m *memStore) Lookup(_ context.Context, key string) (monitoring.Target, error) {
	t, ok := m.targets[key]
	if !ok {
		return t, fmt.Errorf("target %q: %w", key, monitoring.ErrTargetNotFound)
	}
	return t, nil
}

type fakeSource struct{}

func (fakeSource) KeyField() string { return "NodeInfo.Uid" }
func (fakeSource) List(context.Context, monitoring.Scope) ([]monitoring.Target, error) {
	return nil, nil
}
func (fakeSource) Exists(context.Context, monitoring.Target) (bool, error) { return true, nil }
func (fakeSource) Fetch(_ context.Context, ref map[string]string) (monitoring.Target, error) {
	if ref["nodeId"] != "node01" {
		return monitoring.Target{}, monitoring.ErrTargetNotFound
	}
	return monitoring.Target{EntityID: "uid-fetched", Kind: "vm", Name: "web-1", OS: "linux",
		Extra: map[string]string{"nsId": ref["nsId"], "infraId": ref["infraId"], "nodeId": ref["nodeId"]}}, nil
}

type fakeSyncer struct{ err error }

func (f fakeSyncer) Sync(context.Context) (mapping.SyncResult, error) {
	return mapping.SyncResult{Updated: []string{"k1"}, Kept: map[string]string{}}, f.err
}

type fakeBackend struct {
	got    monitoring.QuerySpec
	err    error
	series []monitoring.Series
}

func (f *fakeBackend) WriteEndpoint() monitoring.Endpoint {
	return monitoring.Endpoint{URL: "http://receive:19291/otlp", Protocol: "otlp_http"}
}
func (f *fakeBackend) Query(_ context.Context, q monitoring.QuerySpec) ([]monitoring.Series, error) {
	f.got = q
	return f.series, f.err
}
func (f *fakeBackend) Series(context.Context, string) ([]map[string]string, error) { return nil, nil }
func (f *fakeBackend) Capabilities() monitoring.BackendCaps                        { return nil }

func newTestServer(t *testing.T, deps Deps) *Server {
	t.Helper()
	cat, err := catalog.New(catalog.Builtin())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	deps.Catalog = cat
	deps.Collectors = []monitoring.CollectorKind{collector.Hostmetrics{}}
	return NewServer(&config.Config{Server: config.ServerConfig{Port: 8094}}, deps)
}

func call(t *testing.T, s *Server, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, BasePath+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func fleet() *memStore {
	return &memStore{targets: map[string]monitoring.Target{
		"k1": {EntityID: "k1", Kind: "vm", Name: "web-1", OS: "linux", Extra: map[string]string{"nsId": "ns01", "infraId": "i1", "nodeId": "n1"}},
		"k2": {EntityID: "k2", Kind: "vm", Name: "web-2", OS: "linux", Extra: map[string]string{"nsId": "ns02", "infraId": "i2", "nodeId": "n2"}},
	}}
}

func TestPostTarget(t *testing.T) {
	store := &memStore{targets: map[string]monitoring.Target{}}
	s := newTestServer(t, Deps{Targets: store, Source: fakeSource{}})

	code, body := call(t, s, http.MethodPost, "/targets",
		`{"kind":"node","nsId":"ns01","infraId":"infra01","nodeId":"vm1","uid":"uid-given","name":"web-9","os":"linux","gpuCount":2,"gpuModel":"A100"}`)
	if code != http.StatusOK {
		t.Fatalf("register with description = %d %s", code, body)
	}
	got := store.targets["uid-given"]
	if got.Name != "web-9" || got.GPUCount != 2 || got.Extra["nsId"] != "ns01" || got.Extra["nodeId"] != "vm1" {
		t.Errorf("stored %+v", got)
	}

	if code, body = call(t, s, http.MethodPost, "/targets", `{"nsId":"ns01","infraId":"infra01","nodeId":"node01"}`); code != http.StatusOK {
		t.Fatalf("register by platform names = %d %s", code, body)
	}
	if _, ok := store.targets["uid-fetched"]; !ok {
		t.Error("target read from the platform was not stored")
	}

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"node not in platform": {`{"nsId":"ns01","infraId":"infra01","nodeId":"gone"}`, http.StatusNotFound},
		"kubernetes":           {`{"kind":"k8s","nsId":"ns01"}`, http.StatusUnprocessableEntity},
		"missing nodeId":       {`{"nsId":"ns01","infraId":"infra01"}`, http.StatusBadRequest},
		"bad os":               {`{"nsId":"a","infraId":"b","nodeId":"c","uid":"u","os":"mac"}`, http.StatusBadRequest},
		"bad json":             {`{`, http.StatusBadRequest},
	} {
		if code, body := call(t, s, http.MethodPost, "/targets", tc.body); code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, code, body, tc.want)
		}
	}

	noSource := newTestServer(t, Deps{Targets: store})
	if code, _ := call(t, noSource, http.MethodPost, "/targets", `{"nsId":"a","infraId":"b","nodeId":"c"}`); code != http.StatusServiceUnavailable {
		t.Errorf("register by names without a platform = %d, want 503", code)
	}
}

func TestTargetReads(t *testing.T) {
	s := newTestServer(t, Deps{Targets: fleet(), Backend: &fakeBackend{}})

	code, body := call(t, s, http.MethodGet, "/targets?nsId=ns01", "")
	var list model.TargetListResp
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || len(list.Targets) != 1 || list.Targets[0].ID != "k1" {
		t.Errorf("list by nsId = %d %s", code, body)
	}
	if code, body = call(t, s, http.MethodGet, "/targets", ""); code != http.StatusOK || !strings.Contains(body, `"k2"`) {
		t.Errorf("list all = %d %s", code, body)
	}
	if code, _ = call(t, s, http.MethodGet, "/targets/k2", ""); code != http.StatusOK {
		t.Errorf("get = %d", code)
	}
	if code, _ = call(t, s, http.MethodGet, "/targets/nope", ""); code != http.StatusNotFound {
		t.Errorf("get unknown = %d", code)
	}
	code, body = call(t, s, http.MethodGet, "/targets/k1/collectorConfig", "")
	if code != http.StatusOK || !strings.Contains(body, "entity_id") || !strings.Contains(body, "http://receive:19291/otlp") {
		t.Errorf("collector config = %d %s", code, body)
	}
	if code, _ = call(t, s, http.MethodDelete, "/targets/k1", ""); code != http.StatusOK {
		t.Errorf("delete = %d", code)
	}
	if code, _ = call(t, s, http.MethodDelete, "/targets/k1", ""); code != http.StatusNotFound {
		t.Errorf("delete again = %d", code)
	}

	none := newTestServer(t, Deps{})
	for _, path := range []string{"/targets", "/targets/k1", "/targets/k1/collectorConfig"} {
		if code, _ := call(t, none, http.MethodGet, path, ""); code != http.StatusServiceUnavailable {
			t.Errorf("GET %s without a store = %d, want 503", path, code)
		}
	}
}

func TestSync(t *testing.T) {
	ok := newTestServer(t, Deps{Targets: fleet(), Syncer: fakeSyncer{}})
	if code, body := call(t, ok, http.MethodPost, "/targets/sync", ""); code != http.StatusOK ||
		body != `{"updated":["k1"],"deleted":[],"kept":{}}`+"\n" {
		t.Errorf("sync = %d %s", code, body)
	}
	for err, want := range map[error]int{
		mapping.ErrSyncRunning:                        http.StatusConflict,
		fmt.Errorf("%w: boom", mapping.ErrListFailed): http.StatusBadGateway,
		errors.New("db down"):                         http.StatusInternalServerError,
	} {
		s := newTestServer(t, Deps{Targets: fleet(), Syncer: fakeSyncer{err: err}})
		if code, _ := call(t, s, http.MethodPost, "/targets/sync", ""); code != want {
			t.Errorf("sync with %v = %d, want %d", err, code, want)
		}
	}
	if code, _ := call(t, newTestServer(t, Deps{Targets: fleet()}), http.MethodPost, "/targets/sync", ""); code != http.StatusServiceUnavailable {
		t.Errorf("sync without a platform = %d, want 503", code)
	}
}

func TestMetrics(t *testing.T) {
	code, body := call(t, newTestServer(t, Deps{}), http.MethodGet, "/metrics", "")
	var list model.MetricListResp
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil {
		t.Fatalf("metrics = %d", code)
	}
	byName := map[string]model.MetricInfo{}
	for _, m := range list.Metrics {
		byName[m.Name] = m
	}
	if m := byName["host.cpu.utilization"]; !m.Supported {
		t.Errorf("cpu utilization = %+v", m)
	}
	if m := byName["host.cpu.user"]; m.Supported || m.Gap == "" {
		t.Errorf("cpu user = %+v, want unsupported with a reason", m)
	}
	if m := byName["host.processes.zombie"]; !m.ZeroFill {
		t.Errorf("zombie = %+v, want zeroFill", m)
	}
}

func TestQuery(t *testing.T) {
	at := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	backend := &fakeBackend{series: []monitoring.Series{
		{Labels: map[string]string{"entity_id": "k1"}, Points: []monitoring.Point{{T: at, V: 0.5}, {T: at.Add(time.Minute), V: math.NaN()}}},
	}}
	s := newTestServer(t, Deps{Targets: fleet(), Backend: backend})

	code, body := call(t, s, http.MethodPost, "/query", `{"metric":"host.cpu.utilization","selector":{"nsId":"ns01"},
		"step":"1m","start":"2026-11-06T00:00:00Z","end":"2026-11-06T01:00:00Z","windowFn":"max","windowRange":"5m",
		"topK":{"n":3,"desc":true},"fillZero":true}`)
	if code != http.StatusOK {
		t.Fatalf("query = %d %s", code, body)
	}
	q := backend.got
	if len(q.Targets) != 1 || q.Targets[0].EntityID != "k1" || q.Step != time.Minute || q.Window.Range != 5*time.Minute ||
		q.Window.Fn != monitoring.AggMax || q.TopK == nil || q.TopK.N != 3 || !q.FillZero {
		t.Errorf("spec sent = %+v", q)
	}
	want := `{"series":[{"labels":{"entity_id":"k1"},"points":[{"t":"2026-11-06T00:00:00Z","v":0.5},{"t":"2026-11-06T00:01:00Z","v":null}]}],` +
		`"targets":{"k1":{"id":"k1","kind":"vm","name":"web-1","provider":"","region":"","os":"linux","gpuCount":0,"gpuModel":"","attributes":{"infraId":"i1","nodeId":"n1","nsId":"ns01"}}}}` + "\n"
	if body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}

	if code, _ = call(t, s, http.MethodPost, "/query", `{"metric":"host.cpu.utilization","targetIds":["k2"]}`); code != http.StatusOK ||
		backend.got.Targets[0].EntityID != "k2" {
		t.Errorf("query by id = %d", code)
	}
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"unknown metric":   {`{"metric":"nope","targetIds":["k1"]}`, http.StatusNotFound},
		"unsupported":      {`{"metric":"host.cpu.user","targetIds":["k1"]}`, http.StatusUnprocessableEntity},
		"unknown target":   {`{"metric":"host.cpu.utilization","targetIds":["zz"]}`, http.StatusNotFound},
		"no targets named": {`{"metric":"host.cpu.utilization"}`, http.StatusBadRequest},
		"bad step":         {`{"metric":"host.cpu.utilization","targetIds":["k1"],"step":"x"}`, http.StatusBadRequest},
		"graph no range":   {`{"metric":"host.cpu.utilization","targetIds":["k1"],"step":"1m"}`, http.StatusBadRequest},
	} {
		if code, body := call(t, s, http.MethodPost, "/query", tc.body); code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, code, body, tc.want)
		}
	}

	backend.err = &promapi.APIError{StatusCode: 422, Type: "execution", Message: "store down"}
	if code, _ := call(t, s, http.MethodPost, "/query", `{"metric":"host.cpu.utilization","targetIds":["k1"]}`); code != http.StatusBadGateway {
		t.Errorf("backend failure = %d, want 502", code)
	}
	backend.err = errors.New("window needs both a function and a positive range")
	if code, _ := call(t, s, http.MethodPost, "/query", `{"metric":"host.cpu.utilization","targetIds":["k1"]}`); code != http.StatusBadRequest {
		t.Errorf("unbuildable query = %d, want 400", code)
	}
}
