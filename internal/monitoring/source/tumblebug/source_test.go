package tumblebug

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// fakeTumblebug serves the two listing routes with fixed bodies and records
// what the client sent.
func fakeTumblebug(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	mux := http.NewServeMux()
	check := func(r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "admin" || pass != "secret" {
			t.Errorf("%s: basic auth = %q/%q, %v", r.URL.Path, user, pass, ok)
		}
		if got := r.Header.Get(headerCredentialHolder); got != "holder01" {
			t.Errorf("%s: credential holder = %q", r.URL.Path, got)
		}
		paths = append(paths, r.URL.Path)
	}
	mux.HandleFunc("GET /tumblebug/ns", func(w http.ResponseWriter, r *http.Request) {
		check(r)
		_, _ = w.Write([]byte(`{"ns":[{"id":"ns01","name":"ns01"},{"id":"ns02","name":"ns02"}]}`))
	})
	mux.HandleFunc("GET /tumblebug/ns/ns01/resources/node", func(w http.ResponseWriter, r *http.Request) {
		check(r)
		_, _ = w.Write([]byte(`{"node":[{"infraId":"infra01","id":"node01","uid":"d44ikdomt0tac4ejrmmg","name":"web-1",
			"cspResourceId":"i-0d6c7d2a0f4d0ba2a","connectionName":"aws-ap-northeast-2",
			"connectionConfig":{"providerName":"aws"},"region":{"region":"ap-northeast-2","zone":"ap-northeast-2a"},
			"spec":{"acceleratorType":"GPU","acceleratorModel":"NVIDIA T4","acceleratorCount":1},
			"image":{"osType":"ubuntu 22.04"}}]}`))
	})
	mux.HandleFunc("GET /tumblebug/ns/ns02/resources/node", func(w http.ResponseWriter, r *http.Request) {
		check(r)
		_, _ = w.Write([]byte(`{"node":[{"infraId":"infra09","id":"node09","uid":"c9nqt72b9s0d3f6qe2cg"},
			{"infraId":"infra09","id":"half-made"}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &paths
}

func newTestSource(baseURL string) *Source {
	return NewSource(NewClient(Config{
		BaseURL:          baseURL + "/tumblebug",
		Username:         "admin",
		Password:         "secret",
		CredentialHolder: "holder01",
		Timeout:          5 * time.Second,
	}))
}

func TestListAllNamespaces(t *testing.T) {
	srv, _ := fakeTumblebug(t)

	got, err := newTestSource(srv.URL).List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []monitoring.Target{
		{
			EntityID: "d44ikdomt0tac4ejrmmg",
			Kind:     KindVM,
			Name:     "web-1",
			Provider: "aws",
			Region:   "ap-northeast-2",
			OS:       "linux",
			GPUCount: 1,
			GPUModel: "NVIDIA T4",
			Extra: map[string]string{
				ExtraNamespace:     "ns01",
				ExtraInfra:         "infra01",
				ExtraNode:          "node01",
				ExtraConnection:    "aws-ap-northeast-2",
				ExtraZone:          "ap-northeast-2a",
				ExtraCSPResourceID: "i-0d6c7d2a0f4d0ba2a",
				ExtraOSType:        "ubuntu 22.04",
			},
		},
		{
			// A node with only the fields it must have: no name, GPU or OS
			// is made up for it.
			EntityID: "c9nqt72b9s0d3f6qe2cg",
			Kind:     KindVM,
			Extra: map[string]string{
				ExtraNamespace: "ns02",
				ExtraInfra:     "infra09",
				ExtraNode:      "node09",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List =\n%+v\nwant\n%+v", got, want)
	}
}

func TestListOneNamespace(t *testing.T) {
	srv, paths := fakeTumblebug(t)

	got, err := newTestSource(srv.URL).List(context.Background(), monitoring.Scope{ScopeNamespace: "ns02"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].EntityID != "c9nqt72b9s0d3f6qe2cg" {
		t.Fatalf("List = %+v, want only the node with a Uid", got)
	}
	if want := []string{"/tumblebug/ns/ns02/resources/node"}; !reflect.DeepEqual(*paths, want) {
		t.Fatalf("paths = %v, want %v (no namespace listing)", *paths, want)
	}
}

func TestListReportsTumblebugError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"namespace ns404 does not exist"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := newTestSource(srv.URL).List(context.Background(), monitoring.Scope{ScopeNamespace: "ns404"})
	if StatusCode(err) != http.StatusNotFound {
		t.Fatalf("StatusCode(%v) = %d, want 404", err, StatusCode(err))
	}
}

func TestKeyField(t *testing.T) {
	if got := (&Source{}).KeyField(); got != "NodeInfo.Uid" {
		t.Fatalf("KeyField = %q", got)
	}
}

func TestOSFamily(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"", ""}:                     "",
		{"ubuntu 22.04", ""}:         "linux",
		{"rocky 9", "Rocky Linux 9"}: "linux",
		{"Windows 2022", ""}:         "windows",
		{"windows 2019", ""}:         "windows",
		// osType is empty when no CB-Tumblebug extraction pattern matched.
		{"", "Windows Server 2022 Datacenter"}: "windows",
		{"", "Ubuntu 22.04"}:                   "linux",
		{"ubuntu 22.04", "Microsoft Windows"}:  "windows",
	} {
		if got := osFamily(in[0], in[1]); got != want {
			t.Errorf("osFamily(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}
