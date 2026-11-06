package nvidiagpureceiver

import (
	"errors"
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
}

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
	if c.CollectionInterval <= 0 {
		return errors.New("collection_interval must be positive")
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
	}
}
