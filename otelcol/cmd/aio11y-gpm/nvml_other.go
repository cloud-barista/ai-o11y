//go:build !linux

package main

// NVML GPM is read on Linux only; MIG and vGPU are Linux features and
// purego has no dlopen on Windows. Elsewhere GPM is always skipped.

func loadNVML() (*nvml, error) {
	return nil, errNVMLUnavailable
}

func (n *nvml) close() {}
