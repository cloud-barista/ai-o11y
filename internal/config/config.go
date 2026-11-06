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
	Thanos    ThanosConfig
}

// ThanosConfig is the metrics backend. An empty QueryURL leaves queries and
// collector configuration unavailable.
type ThanosConfig struct {
	// QueryURL is the Thanos Querier HTTP address.
	QueryURL string
	// WriteURL is where collectors send samples over OTLP/HTTP: a Prometheus
	// with a Sidecar or a Thanos Receive, as the deployment is built.
	WriteURL string
	// PartialResponse lets a query answer from the stores that respond when
	// another fails. Off by default: any failing store fails the query.
	PartialResponse bool
	Timeout         time.Duration
}

// DBConfig is the MariaDB that keeps the target table. An empty Host leaves
// the database out: the service starts, but nothing that needs the target
// table is available.
type DBConfig struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
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
	v.SetDefault("thanos.partial_response", false)
	v.SetDefault("thanos.timeout", 2*time.Minute)

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
		"thanos.query_url",
		"thanos.write_url",
		"thanos.partial_response",
		"thanos.timeout",
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
			Host:     v.GetString("db.host"),
			Port:     v.GetInt("db.port"),
			Name:     v.GetString("db.name"),
			User:     v.GetString("db.user"),
			Password: v.GetString("db.password"),
		},
		Thanos: ThanosConfig{
			QueryURL:        strings.TrimRight(v.GetString("thanos.query_url"), "/"),
			WriteURL:        v.GetString("thanos.write_url"),
			PartialResponse: v.GetBool("thanos.partial_response"),
			Timeout:         v.GetDuration("thanos.timeout"),
		},
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("%s_SERVER_PORT must be between 1 and 65535, got %d", envPrefix, cfg.Server.Port)
	}
	if cfg.DB.Host != "" && (cfg.DB.Port <= 0 || cfg.DB.Port > 65535) {
		return nil, fmt.Errorf("%s_DB_PORT must be between 1 and 65535, got %d", envPrefix, cfg.DB.Port)
	}
	return cfg, nil
}
