package mapping

import (
	"context"
	"strings"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

func fleet() []monitoring.Target {
	return []monitoring.Target{
		{EntityID: "k2", Kind: "vm", Name: "train-1", Provider: "gcp", OS: "linux", GPUCount: 4, Extra: map[string]string{"namespace": "team-b"}},
		{EntityID: "k1", Kind: "vm", Name: "web-1", Provider: "aws", OS: "linux", Extra: map[string]string{"namespace": "team-a"}},
		{EntityID: "k3", Kind: "vm", Name: "web-2", Provider: "aws", OS: "linux", GPUCount: 1, Extra: map[string]string{"namespace": "team-a"}},
	}
}

func keys(targets []monitoring.Target) []string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.EntityID
	}
	return out
}

func TestSelect(t *testing.T) {
	s := NewSnapshot()
	if err := s.Replace(fleet()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	for name, tc := range map[string]struct {
		sel  monitoring.Selector
		want string
	}{
		"everything":         {nil, "k1,k2,k3"},
		"platform attribute": {monitoring.Selector{"namespace": "team-a"}, "k1,k3"},
		"common field":       {monitoring.Selector{monitoring.AttrProvider: "gcp"}, "k2"},
		"all conditions":     {monitoring.Selector{"namespace": "team-a", monitoring.AttrGPUCount: "1"}, "k3"},
		"no GPU":             {monitoring.Selector{monitoring.AttrGPUCount: "0"}, "k1"},
		"nothing matches":    {monitoring.Selector{"namespace": "team-z"}, ""},
		"unknown attribute":  {monitoring.Selector{"rack": "r1"}, ""},
	} {
		got, err := s.Select(context.Background(), tc.sel)
		if err != nil {
			t.Fatalf("%s: Select: %v", name, err)
		}
		if g := join(keys(got)); g != tc.want {
			t.Errorf("%s: Select = %s, want %s", name, g, tc.want)
		}
	}
}

func TestLookup(t *testing.T) {
	s := NewSnapshot()
	if err := s.Replace(fleet()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := s.Lookup(context.Background(), "k2")
	if err != nil || got.Name != "train-1" {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	got.Extra["namespace"] = "changed"
	again, _ := s.Lookup(context.Background(), "k2")
	if again.Extra["namespace"] != "team-b" {
		t.Fatal("Lookup returned the snapshot's own map")
	}
	if _, err := s.Lookup(context.Background(), "nope"); err == nil {
		t.Fatal("Lookup of an unknown key succeeded")
	}
}

func TestReplaceKeepsPreviousOnBadList(t *testing.T) {
	s := NewSnapshot()
	if err := s.Replace(fleet()); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	for name, bad := range map[string][]monitoring.Target{
		"duplicate key": {{EntityID: "x", Kind: "vm"}, {EntityID: "x", Kind: "vm"}},
		"no key":        {{Kind: "vm"}},
	} {
		if err := s.Replace(bad); err == nil {
			t.Errorf("%s: Replace succeeded", name)
		}
	}
	all, _ := s.Select(context.Background(), nil)
	if len(all) != 3 {
		t.Fatalf("a rejected list replaced the snapshot: %d targets", len(all))
	}
}

func join(s []string) string { return strings.Join(s, ",") }
