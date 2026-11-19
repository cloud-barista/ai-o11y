package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cloud-barista/ai-o11y/otelcol/cmd/aio11y-gpm/gpm"
)

// fakeDev is one GPU of fakeNVML.
type fakeDev struct {
	uuid      string
	deviceOK  bool // device-level GPM works (Hopper or later, not a vGPU guest)
	instances []gpm.Instance
	// value is what every metric returns; a MIG instance adds its GPU
	// instance id.
	value float64
}

// fakeNVML is a fake gpm.Lib. Sample handles are numbered; each records the
// target it was taken for.
type fakeNVML struct {
	devs   []fakeDev
	owner  map[uintptr][2]uint32 // sample -> {device index + 1, gi + 1 or 0}
	next   uintptr
	freed  int
	closed bool
}

func newFakeNVML(devs ...fakeDev) *fakeNVML {
	return &fakeNVML{devs: devs, owner: map[uintptr][2]uint32{}}
}

func (f *fakeNVML) DeviceCount() (uint32, bool)             { return uint32(len(f.devs)), true }
func (f *fakeNVML) DeviceHandle(i uint32) (uintptr, bool)   { return uintptr(i + 1), true }
func (f *fakeNVML) UUID(dev uintptr) string                 { return f.devs[dev-1].uuid }
func (f *fakeNVML) MIGInstances(dev uintptr) []gpm.Instance { return f.devs[dev-1].instances }
func (f *fakeNVML) SampleFree(uintptr)                      { f.freed++ }
func (f *fakeNVML) Shutdown()                               { f.closed = true }
func (f *fakeNVML) SampleAlloc() (uintptr, bool)            { f.next++; return f.next, true }
func (f *fakeNVML) SampleGet(dev, s uintptr) bool {
	if !f.devs[dev-1].deviceOK {
		return false
	}
	f.owner[s] = [2]uint32{uint32(dev), 0}
	return true
}
func (f *fakeNVML) MigSampleGet(dev uintptr, gi uint32, s uintptr) bool {
	f.owner[s] = [2]uint32{uint32(dev), gi + 1}
	return true
}
func (f *fakeNVML) Metrics(s1, s2 uintptr, ids []uint32) ([]gpm.Value, bool) {
	o := f.owner[s1]
	if o != f.owner[s2] {
		return nil, false
	}
	v := f.devs[o[0]-1].value
	if o[1] > 0 {
		v += float64(o[1] - 1)
	}
	out := make([]gpm.Value, len(ids))
	for i := range ids {
		out[i] = gpm.Value{OK: true, Value: v}
	}
	return out, true
}

func allFields(v string) string {
	return `{"gpm.dram.bandwidth.utilization":` + v + `,"gpm.fp16.utilization":` + v +
		`,"gpm.fp32.utilization":` + v + `,"gpm.fp64.utilization":` + v +
		`,"gpm.graphics.utilization":` + v + `,"gpm.sm.occupancy":` + v +
		`,"gpm.sm.utilization":` + v + `}`
}

func TestRun(t *testing.T) {
	cases := []struct {
		name   string
		open   func() (gpm.Lib, error)
		sleeps int
		want   string
	}{{
		name: "single GPU",
		open: func() (gpm.Lib, error) {
			return newFakeNVML(fakeDev{uuid: "GPU-h100", deviceOK: true, value: 40}), nil
		},
		sleeps: 1,
		want:   `{"results":[{"uuid":"GPU-h100","values":` + allFields("40") + `}]}`,
	}, {
		name: "two MIG instances in a vGPU guest",
		open: func() (gpm.Lib, error) {
			return newFakeNVML(fakeDev{uuid: "GPU-h100", deviceOK: false, value: 10,
				instances: []gpm.Instance{{GPUInstanceID: 1, UUID: "MIG-1"}, {GPUInstanceID: 2, UUID: "MIG-2"}}}), nil
		},
		sleeps: 1,
		want: `{"results":[` +
			`{"uuid":"GPU-h100","mig":true,"gpu_instance_id":1,"mig_uuid":"MIG-1","values":` + allFields("11") + `},` +
			`{"uuid":"GPU-h100","mig":true,"gpu_instance_id":2,"mig_uuid":"MIG-2","values":` + allFields("12") + `}]}`,
	}, {
		name:   "no library",
		open:   func() (gpm.Lib, error) { return nil, errNVMLUnavailable },
		sleeps: 0,
		want:   `{"results":[],"skipped":"cannot open libnvidia-ml"}`,
	}, {
		name: "device without GPM",
		open: func() (gpm.Lib, error) {
			return newFakeNVML(fakeDev{uuid: "GPU-t4", deviceOK: false}), nil
		},
		sleeps: 0,
		want:   `{"results":[],"skipped":"no device or MIG instance supports GPM"}`,
	}, {
		name: "symbol missing",
		open: func() (gpm.Lib, error) {
			return nil, checkSymbols(func(s string) error {
				if s == "nvmlGpmMetricsGet" {
					return errors.New("undefined symbol")
				}
				return nil
			})
		},
		sleeps: 0,
		want:   `{"results":[],"skipped":"NVML symbol missing: nvmlGpmMetricsGet"}`,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lib gpm.Lib
			open := func() (gpm.Lib, error) {
				l, err := tc.open()
				lib = l
				return l, err
			}
			var buf bytes.Buffer
			sleeps := 0
			if err := run(&buf, open, func() { sleeps++ }); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tc.want+"\n" {
				t.Errorf("output\n%s\nwant\n%s", got, tc.want)
			}
			if sleeps != tc.sleeps {
				t.Errorf("slept %d times, want %d", sleeps, tc.sleeps)
			}
			if f, ok := lib.(*fakeNVML); ok {
				if !f.closed {
					t.Error("NVML not shut down")
				}
				if int(f.next) != f.freed {
					t.Errorf("samples allocated %d, freed %d", f.next, f.freed)
				}
			}
		})
	}
}

func TestCheckSymbols(t *testing.T) {
	var asked []string
	if err := checkSymbols(func(s string) error { asked = append(asked, s); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(asked) != len(symbols) {
		t.Errorf("looked up %d symbols, want %d", len(asked), len(symbols))
	}
}
