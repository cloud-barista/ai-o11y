// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

const envPrefix = "AIO11Y"

// Config holds all runtime configuration for the service.
type Config struct {
	Server ServerConfig
	Auth   AuthConfig
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

	// AutomaticEnv only resolves keys that viper already knows about.
	for _, key := range []string{
		"server.port",
		"auth.token",
	} {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("failed to bind env for %q: %w", key, err)
		}
	}

	cfg := &Config{
		Server: ServerConfig{Port: v.GetInt("server.port")},
		Auth:   AuthConfig{Token: v.GetString("auth.token")},
	}
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return nil, fmt.Errorf("%s_SERVER_PORT must be between 1 and 65535, got %d", envPrefix, cfg.Server.Port)
	}
	return cfg, nil
}
