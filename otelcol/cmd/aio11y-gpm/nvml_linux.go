//go:build linux

package main

import "github.com/ebitengine/purego"

// loadNVML opens libnvidia-ml and binds the symbols aio11y-gpm uses. A host
// without an NVIDIA driver has no library and gets errNVMLUnavailable.
func loadNVML() (*nvml, error) {
	var handle uintptr
	for _, name := range []string{"libnvidia-ml.so.1", "libnvidia-ml.so"} {
		h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err == nil {
			handle = h
			break
		}
	}
	if handle == 0 {
		return nil, errNVMLUnavailable
	}
	if err := checkSymbols(func(s string) error {
		_, err := purego.Dlsym(handle, s)
		return err
	}); err != nil {
		_ = purego.Dlclose(handle)
		return nil, err
	}

	n := &nvml{handle: handle}
	purego.RegisterLibFunc(&n.initV2, handle, "nvmlInit_v2")
	purego.RegisterLibFunc(&n.shutdownFn, handle, "nvmlShutdown")
	purego.RegisterLibFunc(&n.deviceGetCount, handle, "nvmlDeviceGetCount_v2")
	purego.RegisterLibFunc(&n.deviceGetHandleByIndex, handle, "nvmlDeviceGetHandleByIndex_v2")
	purego.RegisterLibFunc(&n.deviceGetUUID, handle, "nvmlDeviceGetUUID")
	purego.RegisterLibFunc(&n.deviceGetMaxMigDeviceCnt, handle, "nvmlDeviceGetMaxMigDeviceCount")
	purego.RegisterLibFunc(&n.deviceGetMigDeviceByIndex, handle, "nvmlDeviceGetMigDeviceHandleByIndex")
	purego.RegisterLibFunc(&n.deviceGetGpuInstanceID, handle, "nvmlDeviceGetGpuInstanceId")
	purego.RegisterLibFunc(&n.gpmSampleAlloc, handle, "nvmlGpmSampleAlloc")
	purego.RegisterLibFunc(&n.gpmSampleFree, handle, "nvmlGpmSampleFree")
	purego.RegisterLibFunc(&n.gpmSampleGet, handle, "nvmlGpmSampleGet")
	purego.RegisterLibFunc(&n.gpmMigSampleGet, handle, "nvmlGpmMigSampleGet")
	purego.RegisterLibFunc(&n.gpmMetricsGet, handle, "nvmlGpmMetricsGet")
	return n, nil
}

func (n *nvml) close() {
	if n.handle != 0 {
		_ = purego.Dlclose(n.handle)
		n.handle = 0
	}
}
