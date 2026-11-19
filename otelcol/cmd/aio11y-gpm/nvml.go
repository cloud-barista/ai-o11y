package main

import (
	"errors"
	"fmt"

	"github.com/cloud-barista/ai-o11y/otelcol/cmd/aio11y-gpm/gpm"
)

// libnvidia-ml binding without cgo: nvml_linux.go opens it with purego's
// dlopen and dlsym. A host without the library gets errNVMLUnavailable and
// the binary reports GPM as skipped.

const (
	nvmlSuccess = 0

	// Values from pynvml (nvidia-ml-py); do not guess them. A wrong value
	// makes nvmlGpmMetricsGet fail with ARGUMENT_VERSION_MISMATCH (25).
	//   - the metrics array holds 333 entries
	//   - version is plain 1, not the sizeof|(ver<<24) form other NVML
	//     structs use
	gpmMetricMax         = 333
	gpmMetricsGetVersion = 1

	// NVML_DEVICE_UUID_V2_BUFFER_SIZE.
	uuidBufferSize = 96
)

type gpmMetricInfo struct {
	shortName uintptr
	longName  uintptr
	unit      uintptr
}

type gpmMetric struct {
	metricID   uint32
	nvmlReturn int32
	value      float64
	metricInfo gpmMetricInfo
}

type gpmMetricsGet struct {
	version    uint32
	numMetrics uint32
	sample1    uintptr
	sample2    uintptr
	metrics    [gpmMetricMax]gpmMetric
}

var errNVMLUnavailable = errors.New("cannot open libnvidia-ml")

// symbols are the NVML functions aio11y-gpm binds.
var symbols = []string{
	"nvmlInit_v2", "nvmlShutdown", "nvmlDeviceGetCount_v2",
	"nvmlDeviceGetHandleByIndex_v2", "nvmlDeviceGetUUID",
	"nvmlDeviceGetMaxMigDeviceCount", "nvmlDeviceGetMigDeviceHandleByIndex",
	"nvmlDeviceGetGpuInstanceId", "nvmlGpmSampleAlloc", "nvmlGpmSampleFree",
	"nvmlGpmSampleGet", "nvmlGpmMigSampleGet", "nvmlGpmMetricsGet",
}

// checkSymbols reports the first symbol lookup cannot find. Drivers older
// than GPM lack the nvmlGpm* functions, and binding a missing symbol panics,
// so this runs before binding.
func checkSymbols(lookup func(string) error) error {
	for _, s := range symbols {
		if err := lookup(s); err != nil {
			return fmt.Errorf("NVML symbol missing: %s", s)
		}
	}
	return nil
}

// nvml holds the functions bound from libnvidia-ml. It implements gpm.Lib.
type nvml struct {
	handle uintptr

	initV2                    func() int32
	shutdownFn                func() int32
	deviceGetCount            func(*uint32) int32
	deviceGetHandleByIndex    func(uint32, *uintptr) int32
	deviceGetUUID             func(uintptr, *byte, uint32) int32
	deviceGetMaxMigDeviceCnt  func(uintptr, *uint32) int32
	deviceGetMigDeviceByIndex func(uintptr, uint32, *uintptr) int32
	deviceGetGpuInstanceID    func(uintptr, *uint32) int32
	gpmSampleAlloc            func(*uintptr) int32
	gpmSampleFree             func(uintptr) int32
	gpmSampleGet              func(uintptr, uintptr) int32
	gpmMigSampleGet           func(uintptr, uint32, uintptr) int32
	gpmMetricsGet             func(*gpmMetricsGet) int32
}

var _ gpm.Lib = (*nvml)(nil)

func (n *nvml) DeviceCount() (uint32, bool) {
	var c uint32
	return c, n.deviceGetCount(&c) == nvmlSuccess
}

func (n *nvml) DeviceHandle(i uint32) (uintptr, bool) {
	var d uintptr
	return d, n.deviceGetHandleByIndex(i, &d) == nvmlSuccess
}

func (n *nvml) UUID(dev uintptr) string {
	buf := make([]byte, uuidBufferSize)
	if n.deviceGetUUID(dev, &buf[0], uint32(len(buf))) != nvmlSuccess {
		return ""
	}
	end := 0
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	return string(buf[:end])
}

func (n *nvml) MIGInstances(dev uintptr) []gpm.Instance {
	var count uint32
	if n.deviceGetMaxMigDeviceCnt(dev, &count) != nvmlSuccess {
		return nil
	}
	var out []gpm.Instance
	for i := uint32(0); i < count; i++ {
		var mig uintptr
		if n.deviceGetMigDeviceByIndex(dev, i, &mig) != nvmlSuccess {
			continue
		}
		var gi uint32
		if n.deviceGetGpuInstanceID(mig, &gi) == nvmlSuccess {
			out = append(out, gpm.Instance{GPUInstanceID: gi, UUID: n.UUID(mig)})
		}
	}
	return out
}

func (n *nvml) SampleAlloc() (uintptr, bool) {
	var s uintptr
	return s, n.gpmSampleAlloc(&s) == nvmlSuccess
}

func (n *nvml) SampleFree(s uintptr) { n.gpmSampleFree(s) }

func (n *nvml) SampleGet(dev, s uintptr) bool { return n.gpmSampleGet(dev, s) == nvmlSuccess }

func (n *nvml) MigSampleGet(dev uintptr, gi uint32, s uintptr) bool {
	return n.gpmMigSampleGet(dev, gi, s) == nvmlSuccess
}

func (n *nvml) Metrics(s1, s2 uintptr, ids []uint32) ([]gpm.Value, bool) {
	if len(ids) > gpmMetricMax {
		return nil, false
	}
	var mg gpmMetricsGet
	mg.version = gpmMetricsGetVersion
	mg.numMetrics = uint32(len(ids))
	mg.sample1 = s1
	mg.sample2 = s2
	for i, id := range ids {
		mg.metrics[i].metricID = id
	}
	if n.gpmMetricsGet(&mg) != nvmlSuccess {
		return nil, false
	}
	out := make([]gpm.Value, len(ids))
	for i := range ids {
		m := mg.metrics[i]
		out[i] = gpm.Value{OK: m.nvmlReturn == nvmlSuccess, Value: m.value}
	}
	return out, true
}

func (n *nvml) Shutdown() {
	n.shutdownFn()
	n.close()
}

// openGPM opens and initializes NVML. Any failure is returned for the
// caller to report as skipped.
func openGPM() (gpm.Lib, error) {
	n, err := loadNVML()
	if err != nil {
		return nil, err
	}
	if rc := n.initV2(); rc != nvmlSuccess {
		n.close()
		return nil, fmt.Errorf("nvmlInit_v2 failed: return code %d", rc)
	}
	return n, nil
}
