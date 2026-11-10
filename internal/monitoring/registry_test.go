package monitoring

import (
	"context"
	"reflect"
	"testing"
)

type fakeSource struct{ targets []Target }

func (f fakeSource) KeyField() string                              { return "Node.Uid" }
func (f fakeSource) List(context.Context, Scope) ([]Target, error) { return f.targets, nil }
func (f fakeSource) Exists(context.Context, Target) (bool, error)  { return true, nil }
func (f fakeSource) Fetch(context.Context, map[string]string) (Target, error) {
	return Target{}, ErrTargetNotFound
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry[TargetSource]("target source")
	want := fakeSource{targets: []Target{{Kind: "vm", EntityID: "a"}}}
	if err := r.Register("tumblebug", want); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("tumblebug")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	targets, _ := got.List(context.Background(), nil)
	if !reflect.DeepEqual(targets, want.targets) {
		t.Fatalf("List = %v, want %v", targets, want.targets)
	}
}

func TestRegistryRejects(t *testing.T) {
	r := NewRegistry[TargetSource]("target source")
	if _, err := r.Get("unknown"); err == nil {
		t.Fatal("Get of an unregistered name succeeded")
	}
	if err := r.Register("", fakeSource{}); err == nil {
		t.Fatal("Register with an empty name succeeded")
	}
	if err := r.Register("second", fakeSource{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register("second", fakeSource{}); err == nil {
		t.Fatal("duplicate Register succeeded")
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	r := NewRegistry[Backend]("backend")
	for _, n := range []string{"thanos", "mimir", "prometheus"} {
		if err := r.Register(n, nil); err != nil {
			t.Fatalf("Register %s: %v", n, err)
		}
	}
	if got, want := r.Names(), []string{"mimir", "prometheus", "thanos"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
}
