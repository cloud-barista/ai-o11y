package mapping

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
)

// syncSource is a platform that lists fixed targets and answers existence
// checks from fixed answers.
type syncSource struct {
	listed   []monitoring.Target
	listErr  error
	gone     map[string]bool
	checkErr map[string]error
	checked  []string
}

func (*syncSource) KeyField() string { return "test.key" }

func (s *syncSource) List(context.Context, monitoring.Scope) ([]monitoring.Target, error) {
	return s.listed, s.listErr
}

func (*syncSource) Fetch(context.Context, map[string]string) (monitoring.Target, error) {
	return monitoring.Target{}, monitoring.ErrTargetNotFound
}

func (s *syncSource) Exists(_ context.Context, t monitoring.Target) (bool, error) {
	s.checked = append(s.checked, t.EntityID)
	if err := s.checkErr[t.EntityID]; err != nil {
		return false, err
	}
	return !s.gone[t.EntityID], nil
}

func TestSync(t *testing.T) {
	store := testStore(t) // k1, k2, k3
	ctx := context.Background()
	renamed := fleet()[1]
	renamed.Name = "web-1-renamed"
	src := &syncSource{
		listed: []monitoring.Target{
			renamed,
			{EntityID: "k9", Kind: "vm", Name: "never-installed"},
		},
		gone:     map[string]bool{"k2": true},
		checkErr: map[string]error{"k3": errors.New("tumblebug responded 500: failed to get GetNodeObject")},
	}

	got, err := NewSyncer(store, src, 0).Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := SyncResult{
		Updated: []string{"k1"},
		Deleted: []string{"k2"},
		Kept:    map[string]string{"k3": "tumblebug responded 500: failed to get GetNodeObject"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sync = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(src.checked, []string{"k2", "k3"}) {
		t.Errorf("checked %v, want only the targets missing from the listing", src.checked)
	}

	keys, err := store.Keys(ctx)
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if !reflect.DeepEqual(keys, []string{"k1", "k3"}) {
		t.Errorf("keys after sync = %v, want k1,k3 (k2 deleted, k9 not added)", keys)
	}
	k1, err := store.Lookup(ctx, "k1")
	if err != nil {
		t.Fatalf("Lookup k1: %v", err)
	}
	if k1.Name != "web-1-renamed" {
		t.Errorf("k1 name = %q, want the listed name", k1.Name)
	}
}

func TestSyncListFailureChangesNothing(t *testing.T) {
	store := testStore(t)
	src := &syncSource{listErr: errors.New("tumblebug responded 503"), gone: map[string]bool{"k1": true, "k2": true, "k3": true}}

	if _, err := NewSyncer(store, src, 0).Sync(context.Background()); err == nil {
		t.Fatal("Sync succeeded with a failed listing")
	}
	if len(src.checked) != 0 {
		t.Errorf("checked %v after a failed listing", src.checked)
	}
	keys, _ := store.Keys(context.Background())
	if len(keys) != 3 {
		t.Errorf("keys after failed sync = %v, want all three kept", keys)
	}
}

func TestSyncOneAtATime(t *testing.T) {
	s := NewSyncer(nil, &syncSource{}, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Sync(context.Background()); !errors.Is(err, ErrSyncRunning) {
		t.Errorf("Sync while running = %v, want ErrSyncRunning", err)
	}
}
