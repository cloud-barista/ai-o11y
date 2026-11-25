// Package gpm takes NVML GPM (GPU Performance Monitoring) samples of every
// device and MIG instance and turns them into utilization values.
//
// Opening NVML is the job of the aio11y-gpm binary; a reader of its output
// only needs Output. This package does not import purego, so a statically
// linked collector can use these types without linking libc.
package gpm

import "math"

// Lib is the set of NVML calls GPM sampling uses. aio11y-gpm implements it
// with libnvidia-ml opened through purego; tests use a fake. A bool result
// reports whether the call returned NVML_SUCCESS.
type Lib interface {
	DeviceCount() (uint32, bool)
	DeviceHandle(i uint32) (uintptr, bool)
	UUID(dev uintptr) string
	// MIGInstances lists the MIG devices of dev. It is empty when MIG is off.
	MIGInstances(dev uintptr) []Instance
	SampleAlloc() (uintptr, bool)
	SampleFree(s uintptr)
	SampleGet(dev, s uintptr) bool
	MigSampleGet(dev uintptr, gpuInstanceID uint32, s uintptr) bool
	// Metrics computes the metrics in ids between two samples, in the order
	// of ids. Each value carries its own success flag.
	Metrics(s1, s2 uintptr, ids []uint32) ([]Value, bool)
	Shutdown()
}

// Instance is one MIG device: its GPU instance id and its own UUID
// ("MIG-..."), which may be empty when NVML does not report it.
type Instance struct {
	GPUInstanceID uint32
	UUID          string
}

// Value is one metric NVML computed and whether it succeeded.
type Value struct {
	OK    bool
	Value float64
}

// Metrics maps GPM metric ids (NVML_GPM_METRIC_*) to the field names written
// in Result.Values. All values are percent, 0 to 100. A collector reading the
// output prefixes the field with its source: nvml.gpm.sm.utilization.
var Metrics = []struct {
	ID   uint32
	Name string
}{
	{1, "gpm.graphics.utilization"},
	{2, "gpm.sm.utilization"},
	{3, "gpm.sm.occupancy"},
	{10, "gpm.dram.bandwidth.utilization"},
	{11, "gpm.fp64.utilization"},
	{12, "gpm.fp32.utilization"},
	{13, "gpm.fp16.utilization"},
}

// SMUtilization is the field the mig.sm.utilization catalog row reads.
const SMUtilization = "gpm.sm.utilization"

// Result is the GPM values of one device or one MIG instance. UUID is always
// the parent GPU ("GPU-..."), as nvidia-smi reports it, so the two sources
// join on it; a MIG instance adds its GPU instance id and its MIG UUID.
type Result struct {
	UUID          string             `json:"uuid"`
	MIG           bool               `json:"mig,omitempty"`
	GPUInstanceID uint32             `json:"gpu_instance_id,omitempty"`
	MIGUUID       string             `json:"mig_uuid,omitempty"`
	Values        map[string]float64 `json:"values"`
}

// Output is the JSON aio11y-gpm writes to standard output. GPM being
// unavailable (no library, no symbols, no supporting device) is not an
// error: Results is empty and Skipped says why.
type Output struct {
	Results []Result `json:"results"`
	Skipped string   `json:"skipped,omitempty"`
}

type target struct {
	dev    uintptr
	uuid   string
	mig    bool
	gi     uint32
	migID  string
	s1, s2 uintptr
	ok     bool
}

// Sample takes the first sample of every device and MIG instance, sleeps
// once, takes the second and computes the metrics. It sleeps once no matter
// how many targets there are, so eight GPUs with seven MIG instances each
// still cost one interval.
//
// A device-level sample failing is normal: a vGPU guest gets NOT_SUPPORTED
// for the device and sees only its own MIG instance, while the hypervisor
// sees the device and all instances.
func Sample(lib Lib, sleep func()) []Result {
	count, ok := lib.DeviceCount()
	if !ok {
		return nil
	}
	var targets []*target
	for i := uint32(0); i < count; i++ {
		dev, ok := lib.DeviceHandle(i)
		if !ok {
			continue
		}
		// Keep the "GPU-" prefix NVML gives; nvidia-smi reports it too.
		uuid := lib.UUID(dev)
		if uuid == "" {
			continue
		}
		targets = append(targets, &target{dev: dev, uuid: uuid})
		for _, in := range lib.MIGInstances(dev) {
			targets = append(targets, &target{dev: dev, uuid: uuid, mig: true, gi: in.GPUInstanceID, migID: in.UUID})
		}
	}

	get := func(t *target, s uintptr) bool {
		if t.mig {
			return lib.MigSampleGet(t.dev, t.gi, s)
		}
		return lib.SampleGet(t.dev, s)
	}
	defer func() {
		for _, t := range targets {
			if t.s1 != 0 {
				lib.SampleFree(t.s1)
			}
			if t.s2 != 0 {
				lib.SampleFree(t.s2)
			}
		}
	}()

	live := 0
	for _, t := range targets {
		var ok1, ok2 bool
		t.s1, ok1 = lib.SampleAlloc()
		t.s2, ok2 = lib.SampleAlloc()
		if !ok1 || !ok2 {
			continue
		}
		t.ok = get(t, t.s1)
		if t.ok {
			live++
		}
	}
	if live == 0 {
		return nil
	}
	sleep()

	ids := make([]uint32, len(Metrics))
	for i, m := range Metrics {
		ids[i] = m.ID
	}
	var out []Result
	for _, t := range targets {
		if !t.ok || !get(t, t.s2) {
			continue
		}
		vals, ok := lib.Metrics(t.s1, t.s2, ids)
		if !ok {
			continue
		}
		r := Result{UUID: t.uuid, MIG: t.mig, GPUInstanceID: t.gi, MIGUUID: t.migID, Values: map[string]float64{}}
		for i, v := range vals {
			// DRAM bandwidth utilization comes back NaN on combinations
			// that do not support it; drop it rather than write NaN.
			if i >= len(Metrics) || !v.OK || math.IsNaN(v.Value) || math.IsInf(v.Value, 0) {
				continue
			}
			r.Values[Metrics[i].Name] = v.Value
		}
		if len(r.Values) > 0 {
			out = append(out, r)
		}
	}
	return out
}
