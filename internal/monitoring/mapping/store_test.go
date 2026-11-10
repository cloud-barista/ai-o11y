package mapping

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

func fleet() []monitoring.Target {
	return []monitoring.Target{
		{EntityID: "k2", Kind: "vm", Name: "train-1", Provider: "gcp", OS: "linux", GPUCount: 4, GPUModel: "A100", Extra: map[string]string{"namespace": "team-b"}},
		{EntityID: "k1", Kind: "vm", Name: "web-1", Provider: "aws", OS: "linux", Extra: map[string]string{"namespace": "team-a"}},
		{EntityID: "k3", Kind: "vm", Name: "web-2", Provider: "aws", OS: "linux", GPUCount: 1, Extra: map[string]string{"namespace": "team-a"}},
	}
}

func keys(targets []monitoring.Target) string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.EntityID
	}
	return strings.Join(out, ",")
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(testDB(t), "tumblebug")
	for _, target := range fleet() {
		if err := s.Save(context.Background(), target); err != nil {
			t.Fatalf("Save %s: %v", target.EntityID, err)
		}
	}
	return s
}

// TestRowsRoundTrip runs without a database: a target written to rows and
// read back is the same target.
func TestRowsRoundTrip(t *testing.T) {
	for _, want := range fleet() {
		row := targetRow{
			EntityID: want.EntityID, Kind: want.Kind, Name: want.Name, Provider: want.Provider,
			Region: want.Region, OS: want.OS, GPUCount: want.GPUCount, GPUModel: want.GPUModel,
		}
		if got := targetFromRows(row, attributeRows(want)); !reflect.DeepEqual(got, want) {
			t.Errorf("round trip of %s:\n got %+v\nwant %+v", want.EntityID, got, want)
		}
	}
}

func TestStoreSelect(t *testing.T) {
	s := testStore(t)
	for name, tc := range map[string]struct {
		sel  monitoring.Selector
		want string
	}{
		"everything":         {nil, "k1,k2,k3"},
		"platform attribute": {monitoring.Selector{"namespace": "team-a"}, "k1,k3"},
		"common field":       {monitoring.Selector{monitoring.AttrProvider: "gcp"}, "k2"},
		"all conditions":     {monitoring.Selector{"namespace": "team-a", monitoring.AttrGPUCount: "1"}, "k3"},
		"no GPU":             {monitoring.Selector{monitoring.AttrGPUCount: "0"}, "k1"},
		"case matters":       {monitoring.Selector{"namespace": "Team-A"}, ""},
		"nothing matches":    {monitoring.Selector{"namespace": "team-z"}, ""},
		"unknown attribute":  {monitoring.Selector{"rack": "r1"}, ""},
	} {
		got, err := s.Select(context.Background(), tc.sel)
		if err != nil {
			t.Fatalf("%s: Select: %v", name, err)
		}
		if g := keys(got); g != tc.want {
			t.Errorf("%s: Select = %s, want %s", name, g, tc.want)
		}
	}
}

func TestStoreLookup(t *testing.T) {
	s := testStore(t)
	got, err := s.Lookup(context.Background(), "k2")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if want := fleet()[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("Lookup =\n%+v\nwant\n%+v", got, want)
	}
	if _, err := s.Lookup(context.Background(), "nope"); err == nil {
		t.Fatal("Lookup of an unknown key succeeded")
	}
}

// TestStoreSaveReplaces checks that saving a target again replaces its fields
// and attributes, and keeps when it was first saved.
func TestStoreSaveReplaces(t *testing.T) {
	s := testStore(t)
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return first }
	target := monitoring.Target{EntityID: "k9", Kind: "vm", Name: "old", Extra: map[string]string{"namespace": "team-a", "infraId": "i1"}}
	if err := s.Save(context.Background(), target); err != nil {
		t.Fatalf("Save: %v", err)
	}
	s.now = func() time.Time { return first.Add(time.Hour) }
	target.Name = "new"
	target.Extra = map[string]string{"namespace": "team-b"}
	if err := s.Save(context.Background(), target); err != nil {
		t.Fatalf("Save again: %v", err)
	}
	got, _ := s.Lookup(context.Background(), "k9")
	if got.Name != "new" || !reflect.DeepEqual(got.Extra, map[string]string{"namespace": "team-b"}) {
		t.Fatalf("second save did not replace: %+v", got)
	}
	if old, _ := s.Select(context.Background(), monitoring.Selector{"infraId": "i1"}); len(old) != 0 {
		t.Fatal("an attribute of the first save is still selectable")
	}
	var row targetRow
	if err := s.db.First(&row, "entity_id = ?", "k9").Error; err != nil {
		t.Fatalf("read row: %v", err)
	}
	if !row.RegisteredAt.Equal(first) || !row.UpdatedAt.Equal(first.Add(time.Hour)) {
		t.Fatalf("registered %v updated %v, want %v and %v", row.RegisteredAt, row.UpdatedAt, first, first.Add(time.Hour))
	}
}

func TestStoreDeleteAndKeys(t *testing.T) {
	s := testStore(t)
	if err := s.Delete(context.Background(), "k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(context.Background(), "never-stored"); err != nil {
		t.Fatalf("Delete of an unknown key: %v", err)
	}
	got, err := s.Keys(context.Background())
	if err != nil || strings.Join(got, ",") != "k2,k3" {
		t.Fatalf("Keys = %v, %v", got, err)
	}
	if left, _ := s.Select(context.Background(), monitoring.Selector{"namespace": "team-a"}); keys(left) != "k3" {
		t.Fatalf("attributes of the deleted target remain: %s", keys(left))
	}
}

func TestStoreRejectsTargetWithoutKey(t *testing.T) {
	s := NewStore(nil, "tumblebug")
	if err := s.Save(context.Background(), monitoring.Target{Kind: "vm"}); err == nil {
		t.Fatal("Save accepted a target without a key")
	}
}
