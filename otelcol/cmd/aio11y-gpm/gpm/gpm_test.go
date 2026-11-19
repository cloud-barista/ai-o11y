package gpm

import (
	"encoding/json"
	"math"
	"testing"
)

// fakeLib is a fake Lib. Each device sets whether device-level sampling
// works, its MIG instances and the values to return.
type fakeLib struct {
	devs   []fakeDev
	sleeps int
	alloc  int
	freed  int
	// sample handle -> target it was taken for (dev*100+gi, gi 0 for the device)
	owner map[uintptr]int
	next  uintptr
}

type fakeDev struct {
	uuid        string
	deviceOK    bool               // device-level sampling works (not in a vGPU guest)
	instances   []Instance         // MIG devices
	values      map[uint32]float64 // device values by GPM metric id
	migValues   map[uint32]float64 // MIG values; each instance adds its gi
	unsupported map[uint32]bool    // metric ids whose nvmlReturn fails
}

func (f *fakeLib) DeviceCount() (uint32, bool)           { return uint32(len(f.devs)), true }
func (f *fakeLib) DeviceHandle(i uint32) (uintptr, bool) { return uintptr(i + 1), true }
func (f *fakeLib) UUID(dev uintptr) string               { return f.devs[dev-1].uuid }
func (f *fakeLib) MIGInstances(dev uintptr) []Instance   { return f.devs[dev-1].instances }
func (f *fakeLib) SampleAlloc() (uintptr, bool) {
	f.next++
	f.alloc++
	return f.next, true
}
func (f *fakeLib) SampleFree(uintptr) { f.freed++ }
func (f *fakeLib) SampleGet(dev, s uintptr) bool {
	if !f.devs[dev-1].deviceOK {
		return false
	}
	f.owner[s] = int(dev) * 100
	return true
}
func (f *fakeLib) MigSampleGet(dev uintptr, gi uint32, s uintptr) bool {
	f.owner[s] = int(dev)*100 + int(gi)
	return true
}
func (f *fakeLib) Metrics(s1, s2 uintptr, ids []uint32) ([]Value, bool) {
	o := f.owner[s1]
	if o != f.owner[s2] {
		return nil, false
	}
	d := f.devs[o/100-1]
	gi := uint32(o % 100)
	out := make([]Value, len(ids))
	for i, id := range ids {
		if d.unsupported[id] {
			continue
		}
		if gi == 0 {
			v, ok := d.values[id]
			out[i] = Value{OK: ok, Value: v}
		} else {
			v, ok := d.migValues[id]
			out[i] = Value{OK: ok, Value: v + float64(gi)}
		}
	}
	return out, true
}
func (f *fakeLib) Shutdown() {}

func newFakeLib(devs ...fakeDev) *fakeLib {
	return &fakeLib{devs: devs, owner: map[uintptr]int{}}
}

func TestSample(t *testing.T) {
	f := newFakeLib(
		// MIG GPU on a hypervisor: the device and two instances.
		fakeDev{uuid: "GPU-a", deviceOK: true, instances: []Instance{{1, "MIG-a1"}, {2, "MIG-a2"}},
			values:    map[uint32]float64{1: 55, 2: 50, 10: math.NaN()},
			migValues: map[uint32]float64{2: 10}},
		// vGPU guest: no device-level sampling, only its own instance.
		fakeDev{uuid: "GPU-b", deviceOK: false, instances: []Instance{{3, ""}},
			migValues: map[uint32]float64{1: 30, 2: 20}, unsupported: map[uint32]bool{1: true}},
	)
	res := Sample(f, func() { f.sleeps++ })

	if f.sleeps != 1 {
		t.Errorf("slept %d times, want 1 regardless of target count", f.sleeps)
	}
	if f.alloc != f.freed {
		t.Errorf("samples allocated %d, freed %d", f.alloc, f.freed)
	}
	type key struct {
		uuid  string
		mig   bool
		gi    uint32
		migID string
	}
	got := map[key]map[string]float64{}
	for _, r := range res {
		got[key{r.UUID, r.MIG, r.GPUInstanceID, r.MIGUUID}] = r.Values
	}
	want := map[key]map[string]float64{
		{"GPU-a", false, 0, ""}:      {"gpm.graphics.utilization": 55, SMUtilization: 50},
		{"GPU-a", true, 1, "MIG-a1"}: {SMUtilization: 11},
		{"GPU-a", true, 2, "MIG-a2"}: {SMUtilization: 12},
		{"GPU-b", true, 3, ""}:       {SMUtilization: 23},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, w := range want {
		g := got[k]
		if len(g) != len(w) {
			t.Errorf("%v = %v, want %v (NaN and unsupported must be dropped)", k, g, w)
			continue
		}
		for n, v := range w {
			if g[n] != v {
				t.Errorf("%v %s = %v, want %v", k, n, g[n], v)
			}
		}
	}
}

func TestSampleNoDevice(t *testing.T) {
	f := newFakeLib()
	if res := Sample(f, func() { f.sleeps++ }); res != nil || f.sleeps != 0 {
		t.Errorf("no device must not sleep: %v sleeps=%d", res, f.sleeps)
	}
}

// A GPU without device-level GPM and without MIG (a T4 or A10, say) gives
// no result and does not sleep.
func TestSampleUnsupportedDevice(t *testing.T) {
	f := newFakeLib(fakeDev{uuid: "GPU-t4", deviceOK: false})
	if res := Sample(f, func() { f.sleeps++ }); res != nil || f.sleeps != 0 {
		t.Errorf("GPM unsupported device: %v sleeps=%d", res, f.sleeps)
	}
	if f.alloc != f.freed {
		t.Errorf("samples allocated %d, freed %d", f.alloc, f.freed)
	}
}

// The JSON aio11y-gpm writes must read back into Output.
func TestOutputJSON(t *testing.T) {
	in := Output{Results: []Result{
		{UUID: "GPU-a", Values: map[string]float64{SMUtilization: 55}},
		{UUID: "GPU-a", MIG: true, GPUInstanceID: 3, MIGUUID: "MIG-x", Values: map[string]float64{SMUtilization: 12.5}},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"results":[{"uuid":"GPU-a","values":{"gpm.sm.utilization":55}},` +
		`{"uuid":"GPU-a","mig":true,"gpu_instance_id":3,"mig_uuid":"MIG-x","values":{"gpm.sm.utilization":12.5}}]}`
	if string(b) != wantJSON {
		t.Errorf("JSON\n%s\nwant\n%s", b, wantJSON)
	}
	var out Output
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	r := out.Results[1]
	if len(out.Results) != 2 || r.GPUInstanceID != 3 || !r.MIG || r.MIGUUID != "MIG-x" || r.Values[SMUtilization] != 12.5 {
		t.Errorf("read back %+v (JSON %s)", out, b)
	}
}
