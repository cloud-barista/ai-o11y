package tumblebug

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

const nodePath = "/tumblebug/ns/ns01/infra/infra01/node/node01"

func listedNode() monitoring.Target {
	return monitoring.Target{EntityID: "d44ikdomt0tac4ejrmmg", Kind: KindVM, Extra: map[string]string{
		ExtraNamespace: "ns01", ExtraInfra: "infra01", ExtraNode: "node01",
	}}
}

// The 500 bodies are what CB-Tumblebug v0.13.5 sends: EndRequestWithLog wraps
// the error of GetNodeObjectWithNodeGroups as {"message": ...}.
func TestExists(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		body    string
		want    bool
		wantErr bool
	}{
		"same node":          {200, `{"id":"node01","uid":"d44ikdomt0tac4ejrmmg"}`, true, false},
		"re-created node":    {200, `{"id":"node01","uid":"c9nqt72b9s0d3f6qe2cg"}`, false, false},
		"404":                {404, `{"message":"not found"}`, false, false},
		"500 node not found": {500, `{"message":"no Node found (ID: /ns/ns01/infra/infra01/node/node01)"}`, false, false},
		"500 store failure":  {500, `{"message":"failed to get GetNodeObject (ID: /ns/ns01/infra/infra01/node/node01)"}`, false, true},
		"500 other":          {500, `{"message":"something else"}`, false, true},
		"503":                {503, `{"message":"unavailable"}`, false, true},
		"401":                {401, `{"message":"unauthorized"}`, false, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != nodePath {
				t.Errorf("%s: path = %s", name, r.URL.Path)
			}
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		got, err := newTestSource(srv.URL).Exists(context.Background(), listedNode())
		srv.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("%s: Exists = %v, want %v", name, got, tc.want)
		}
	}
}

func TestExistsNeedsPlatformIDs(t *testing.T) {
	target := listedNode()
	delete(target.Extra, ExtraInfra)
	if _, err := newTestSource("http://unused").Exists(context.Background(), target); err == nil {
		t.Error("Exists without infraId succeeded")
	}
}

// rateLimited answers 429 to the first n requests, then serves the node.
func rateLimited(t *testing.T, n int) (*Source, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= n {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limit exceeded"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"node01","uid":"d44ikdomt0tac4ejrmmg"}`))
	}))
	t.Cleanup(srv.Close)
	s := newTestSource(srv.URL)
	s.client.retryBase = time.Millisecond
	return s, &calls
}

func TestRetryOnRateLimit(t *testing.T) {
	s, calls := rateLimited(t, maxRetries)
	got, err := s.Exists(context.Background(), listedNode())
	if err != nil || !got {
		t.Fatalf("Exists after %d 429s = %v, %v; want true", maxRetries, got, err)
	}
	if *calls != maxRetries+1 {
		t.Errorf("calls = %d, want %d", *calls, maxRetries+1)
	}
}

func TestRetryGivesUp(t *testing.T) {
	s, calls := rateLimited(t, maxRetries+1)
	_, err := s.Exists(context.Background(), listedNode())
	if StatusCode(err) != http.StatusTooManyRequests || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("err = %v, want the last 429", err)
	}
	if *calls != maxRetries+1 {
		t.Errorf("calls = %d, want %d", *calls, maxRetries+1)
	}
}
