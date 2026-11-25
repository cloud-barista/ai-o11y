package nvidiagpureceiver

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/collector/scraper/scraperhelper"
)

// SourceNvidiaSMI is the source that runs nvidia-smi.
const SourceNvidiaSMI = "nvidia_smi"

// Config is the receiver's configuration.
type Config struct {
	scraperhelper.ControllerConfig `mapstructure:",squash"`
	// Source picks where samples come from; only "nvidia_smi" exists yet.
	Source    string          `mapstructure:"source"`
	NvidiaSMI NvidiaSMIConfig `mapstructure:"nvidia_smi"`
	XID       XIDConfig       `mapstructure:"xid"`
	GPM       GPMConfig       `mapstructure:"gpm"`
}

// GPMConfig configures reading NVML GPM (Hopper or later GPUs), the only
// source of SM utilization per MIG instance, into nvml.gpm.<field>. Each
// scrape runs the helper aio11y-gpm, alongside nvidia-smi. Without the helper,
// or where GPM cannot be used (no libnvidia-ml, an older driver or GPU), GPM
// alone is left out even when enabled.
type GPMConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Path is the aio11y-gpm executable. Empty looks next to the collector's
	// executable, then on PATH.
	Path string `mapstructure:"path"`
	// SampleInterval is the time between the two GPM samples the values are
	// averaged over. It must be more than 100ms and fit in a scrape.
	SampleInterval time.Duration `mapstructure:"sample_interval"`
}

// minGPMSampleInterval is NVML's lower bound for the time between two GPM
// samples; aio11y-gpm refuses an interval not above it.
const minGPMSampleInterval = 100 * time.Millisecond

// XIDConfig configures counting the XID errors the NVIDIA driver logs to the
// kernel log, into nvidia_kmsg.xid.errors. It is off by default: reading
// /dev/kmsg takes root (or CAP_SYSLOG) where kernel.dmesg_restrict is set.
type XIDConfig struct {
	Enabled bool `mapstructure:"enabled"`
	// Path is the kernel log device. A regular file can stand in for it.
	Path string `mapstructure:"path"`
}

// DefaultKmsgPath is the kernel log device.
const DefaultKmsgPath = "/dev/kmsg"

// NvidiaSMIConfig configures the nvidia-smi source.
type NvidiaSMIConfig struct {
	// Path is the nvidia-smi executable.
	Path string `mapstructure:"path"`
	// Fields are the --query-gpu fields to read. Fields the installed
	// nvidia-smi does not know are left out.
	Fields []string `mapstructure:"fields"`
}

// DefaultFields are the nvidia-smi query fields read by default.
var DefaultFields = []string{
	"utilization.gpu", "utilization.memory", "utilization.encoder", "utilization.decoder",
	"memory.total", "memory.used", "memory.free",
	"temperature.gpu", "temperature.memory", "power.draw", "fan.speed",
	"clocks.sm", "clocks.mem", "pstate", "clocks_event_reasons.active",
	"ecc.errors.corrected.volatile.total", "ecc.errors.uncorrected.volatile.total",
	"ecc.errors.corrected.aggregate.total", "ecc.errors.uncorrected.aggregate.total",
	"retired_pages.double_bit.count", "retired_pages.pending",
	"remapped_rows.correctable", "remapped_rows.uncorrectable", "remapped_rows.pending", "remapped_rows.failure",
}

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.Source != SourceNvidiaSMI {
		return errors.New(`source must be "nvidia_smi"`)
	}
	if c.NvidiaSMI.Path == "" {
		return errors.New("nvidia_smi.path must not be empty")
	}
	if c.XID.Enabled && c.XID.Path == "" {
		return errors.New("xid.path must not be empty")
	}
	if c.CollectionInterval <= 0 {
		return errors.New("collection_interval must be positive")
	}
	if c.GPM.Enabled {
		if c.GPM.SampleInterval <= minGPMSampleInterval {
			return fmt.Errorf("gpm.sample_interval must be more than %s", minGPMSampleInterval)
		}
		if c.GPM.SampleInterval >= c.CollectionInterval {
			return errors.New("gpm.sample_interval must be shorter than collection_interval")
		}
		if c.Timeout > 0 && c.GPM.SampleInterval >= c.Timeout {
			return errors.New("gpm.sample_interval must be shorter than timeout")
		}
	}
	return nil
}

func createDefaultConfig() *Config {
	cc := scraperhelper.NewDefaultControllerConfig()
	cc.CollectionInterval = 30 * time.Second
	cc.Timeout = 10 * time.Second
	return &Config{
		ControllerConfig: cc,
		Source:           SourceNvidiaSMI,
		NvidiaSMI:        NvidiaSMIConfig{Path: "nvidia-smi", Fields: DefaultFields},
		XID:              XIDConfig{Path: DefaultKmsgPath},
		GPM:              GPMConfig{SampleInterval: time.Second},
	}
}
