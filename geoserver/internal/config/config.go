package config

import (
	platformconfig "platform.local/platform/config"
)

// Config captures runtime settings for the GeoServer microservice.
type Config struct {
	AppHost     string
	AppPort     string
	AppEnv      string
	AppTimezone string
	Database    platformconfig.DatabaseConfig
}

// Load hydrates Config from environment variables, loading a shared .env when available.
func Load() (*Config, error) {
	if err := platformconfig.LoadEnvOnce(".", "..", "../backend", "../auth-service"); err != nil {
		return nil, err
	}

	cfg := &Config{
		AppHost:     platformconfig.GetEnv("GEOSERVER_APP_HOST", "0.0.0.0"),
		AppPort:     platformconfig.GetEnv("GEOSERVER_APP_PORT", "8083"),
		AppEnv:      platformconfig.GetEnv("APP_ENV", "development"),
		AppTimezone: platformconfig.GetEnv("APP_TIMEZONE", "UTC"),
		Database:    platformconfig.DatabaseFromEnv(),
	}

	return cfg, nil
}
