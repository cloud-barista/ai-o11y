// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const envPrefix = "AIO11Y"

// Config holds all runtime configuration for the service.
type Config struct {
	Server    ServerConfig
	Auth      AuthConfig
	Tumblebug TumblebugConfig
	DB        DBConfig
	// Backend names the metrics backend provider: "thanos" or "victoria".
	// Only that provider's settings are read.
	Backend   string
	Thanos    ThanosConfig
	Victoria  VictoriaConfig
	Collector CollectorConfig
}

// CollectorConfig is about the collector configurations ai-o11y renders.
type CollectorConfig struct {
	// TelemetryPort is the localhost port collectors serve their own metrics
	// on; 0 turns them off.
	TelemetryPort int
}

// ThanosConfig is the metrics backend. An empty QueryURL leaves queries and
// collector configuration unavailable.
type ThanosConfig struct {
	// QueryURLs are Thanos Querier HTTP addresses in order of preference,
	// given as a comma-separated list; a query moves to the next when one
	// fails.
	QueryURLs []string
	// WriteURL is where collectors send samples over OTLP/HTTP: a Prometheus
	// with a Sidecar or a Thanos Receive, as the deployment is built.
	WriteURL string
	// PartialResponse lets a query answer from the stores that respond when
	// another fails. Off by default: any failing store fails the query.
	PartialResponse bool
	Timeout         time.Duration
}

// VictoriaConfig is the VictoriaMetrics metrics backend. An empty query URL
// leaves queries and collector configuration unavailable.
type VictoriaConfig struct {
	// QueryURLs are addresses serving the Prometheus query API (such as
	// vmauth or VictoriaMetrics itself) in order of preference, given as a
	// comma-separated list; a query moves to the next when one fails.
	QueryURLs []string
	// WriteURL is where collectors send samples over OTLP/HTTP, such as a
	// vmauth or vmagent; the collector appends /v1/metrics.
	WriteURL string
	Timeout  time.Duration
	// MaxConcurrent is how many query chunks run at once; MaxIDs is how many
	// target ids one chunk selects. Zero takes the provider's default.
	MaxConcurrent int
	MaxIDs        int
}

// Metrics backend providers AIO11Y_BACKEND selects.
const (
	BackendThanos   = "thanos"
	BackendVictoria = "victoria"
)

// DBConfig is the MariaDB that keeps the target table. An empty Host leaves
// the database out: the service starts, but nothing that needs the target
// table is available.
type DBConfig struct {
	Host           string
	Port           int
	Name           string
	User           string
	Password       string
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
}

// TumblebugConfig is where CB-Tumblebug is reached. An empty BaseURL leaves the
// tumblebug target source unregistered.
type TumblebugConfig struct {
	BaseURL          string
	Username         string
	Password         string
	CredentialHolder string
	Timeout          time.Duration
}

// ServerConfig is where the REST API listens.
type ServerConfig struct {
	Port int
}

// AuthConfig guards the REST API.
type AuthConfig struct {
	// Token is the bearer token every route but readiness and the API
	// documentation requires. Empty turns the check off.
	Token string
}

// Load reads configuration from environment variables and validates it.
// Every setting has a default, so the service starts with no configuration.
func Load() (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	v.SetDefault("server.port", 8094)
	v.SetDefault("tumblebug.timeout", 2*time.Minute)
	v.SetDefault("db.port", 3306)
	v.SetDefault("db.name", "aio11y")
	v.SetDefault("db.connect_timeout", 5*time.Second)
	v.SetDefault("db.read_timeout", 30*time.Second)
	v.SetDefault("db.write_timeout", 30*time.Second)
	v.SetDefault("backend", BackendThanos)
	v.SetDefault("thanos.partial_response", false)
	v.SetDefault("thanos.timeout", 2*time.Minute)
	v.SetDefault("victoria.timeout", 30*time.Second)

	// AutomaticEnv only resolves keys that viper already knows about.
	for _, key := range []string{
		"server.port",
		"auth.token",
		"tumblebug.base_url",
		"tumblebug.username",
		"tumblebug.password",
		"tumblebug.credential_holder",
		"tumblebug.timeout",
		"db.host",
		"db.port",
		"db.name",
		"db.user",
		"db.password",
		"db.connect_timeout",
		"db.read_timeout",
		"db.write_timeout",
		"backend",
		"thanos.query_url",
		"thanos.write_url",
		"thanos.partial_response",
		"thanos.timeout",
		"victoria.query_url",
		"victoria.write_url",
		"victoria.timeout",
		"victoria.max_concurrent",
		"victoria.max_ids",
		"collector.telemetry_port",
	} {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("failed to bind env for %q: %w", key, err)
		}
	}

	cfg := &Config{
		Server: ServerConfig{Port: v.GetInt("server.port")},
		Auth:   AuthConfig{Token: v.GetString("auth.token")},
		Tumblebug: TumblebugConfig{
			BaseURL:          strings.TrimRight(v.GetString("tumblebug.base_url"), "/"),
			Username:         v.GetString("tumblebug.username"),
			Password:         v.GetString("tumblebug.password"),
			CredentialHolder: v.GetString("tumblebug.credential_holder"),
			Timeout:          v.GetDuration("tumblebug.timeout"),
		},
		DB: DBConfig{
			Host:           v.GetString("db.host"),
			Port:           v.GetInt("db.port"),
			Name:           v.GetString("db.name"),
			User:           v.GetString("db.user"),
			Password:       v.GetString("db.password"),
			ConnectTimeout: v.GetDuration("db.connect_timeout"),
			ReadTimeout:    v.GetDuration("db.read_timeout"),
			WriteTimeout:   v.GetDuration("db.write_timeout"),
		},
		Collector: CollectorConfig{TelemetryPort: v.GetInt("collector.telemetry_port")},
		Backend:   strings.ToLower(strings.TrimSpace(v.GetString("backend"))),
		Thanos: ThanosConfig{
			QueryURLs:       splitURLs(v.GetString("thanos.query_url")),
			WriteURL:        v.GetString("thanos.write_url"),
			PartialResponse: v.GetBool("thanos.partial_response"),
			Timeout:         v.GetDuration("thanos.timeout"),
		},
		Victoria: VictoriaConfig{
			QueryURLs:     splitURLs(v.GetString("victoria.query_url")),
			WriteURL:      v.GetString("victoria.write_url"),
			Timeout:       v.GetDuration("victoria.timeout"),
			MaxConcurrent: v.GetInt("victoria.max_concurrent"),
			MaxIDs:        v.GetInt("victoria.max_ids"),
		},
	}
	if cfg.Backend != BackendThanos && cfg.Backend != BackendVictoria {
		return nil, fmt.Errorf("%s_BACKEND must be %q or %q, got %q", envPrefix, BackendThanos, BackendVictoria, cfg.Backend)
	}
	if cfg.Victoria.MaxConcurrent < 0 || cfg.Victoria.MaxIDs < 0 {
		return nil, fmt.Errorf("%s_VICTORIA_MAX_CONCURRENT and %s_VICTORIA_MAX_IDS must not be negative", envPrefix, envPrefix)
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("%s_SERVER_PORT must be between 1 and 65535, got %d", envPrefix, cfg.Server.Port)
	}
	if cfg.Collector.TelemetryPort < 0 || cfg.Collector.TelemetryPort > 65535 {
		return nil, fmt.Errorf("%s_COLLECTOR_TELEMETRY_PORT must be between 0 and 65535, got %d", envPrefix, cfg.Collector.TelemetryPort)
	}
	if cfg.DB.Host != "" && (cfg.DB.Port <= 0 || cfg.DB.Port > 65535) {
		return nil, fmt.Errorf("%s_DB_PORT must be between 1 and 65535, got %d", envPrefix, cfg.DB.Port)
	}
	return cfg, nil
}

// splitURLs reads a comma-separated list of addresses, dropping empty entries
// and trailing slashes.
func splitURLs(s string) []string {
	var out []string
	for _, u := range strings.Split(s, ",") {
		if u = strings.TrimRight(strings.TrimSpace(u), "/"); u != "" {
			out = append(out, u)
		}
	}
	return out
}
