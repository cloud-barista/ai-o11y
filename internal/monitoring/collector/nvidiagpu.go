package collector

import (
	"context"
	"strings"
	"time"

	"github.com/cloud-barista/ai-o11y/internal/monitoring"
	"github.com/cloud-barista/ai-o11y/internal/monitoring/catalog"
)

// NvidiaGPUReceiver is the receiver id of ai-o11y's NVIDIA GPU receiver
// (otelcol/receiver/nvidiagpureceiver).
const NvidiaGPUReceiver = "nvidia_gpu"

// otherGPUVendors are words in a GPU model that name a vendor other than
// NVIDIA.
var otherGPUVendors = []string{"amd", "radeon", "instinct", "intel", "gaudi", "habana"}

// NvidiaGPU renders the NVIDIA GPU receiver, reading nvidia-smi, for a VM with
// GPUs.
type NvidiaGPU struct {
	Interval time.Duration
}

var _ monitoring.CollectorKind = NvidiaGPU{}

// Name implements monitoring.CollectorKind.
func (NvidiaGPU) Name() string { return catalog.CollectorNvidiaSMI }

// Path implements monitoring.CollectorKind.
func (NvidiaGPU) Path() monitoring.Path { return monitoring.PathAgent }

// AppliesTo implements monitoring.CollectorKind: a VM the platform reports
// GPUs on, unless their model names another vendor. A model left empty is
// taken for NVIDIA, the only vendor the collector reads yet.
func (NvidiaGPU) AppliesTo(t monitoring.Target) bool {
	if t.Kind != "vm" || t.GPUCount <= 0 {
		return false
	}
	model := strings.ToLower(t.GPUModel)
	for _, v := range otherGPUVendors {
		if strings.Contains(model, v) {
			return false
		}
	}
	return true
}

// Render implements monitoring.CollectorKind.
func (g NvidiaGPU) Render(monitoring.Target, monitoring.MetricCatalog) (monitoring.ConfigFragment, error) {
	interval := g.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	return monitoring.ConfigFragment{
		Receivers: map[string]any{
			NvidiaGPUReceiver: map[string]any{
				"collection_interval": interval.String(),
				"source":              catalog.CollectorNvidiaSMI,
			},
		},
	}, nil
}

// Health implements monitoring.CollectorKind.
func (NvidiaGPU) Health(context.Context, monitoring.Target) (monitoring.CollectorHealth, error) {
	return monitoring.CollectorHealth{}, ErrHealthUnsupported
}
