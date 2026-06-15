package config

import (
	"fmt"
	"os"

	platformconfig "platform.local/platform/config"

	goredis "github.com/redis/go-redis/v9"
)

type Config struct {
	AppHost     string
	AppPort     string
	AppEnv      string
	AppTimezone string
	Database    platformconfig.DatabaseConfig
	Redis       goredis.Options
	Dispatch    DispatchConfig
	Scheduler   SchedulerConfig
	Backend     BackendConfig
}

// BackendConfig points the webservice at the backend's internal lifecycle API.
type BackendConfig struct {
	URL            string
	CallbackSecret string
}

type DispatchConfig struct {
	NoCapacityRetryMs  int
	CpuThresholdPercent float64
}

type SchedulerConfig struct {
	IntervalSeconds          int
	StuckModelTimeoutMinutes int
}

func Load() (*Config, error) {
	if err := platformconfig.LoadEnvOnce(".", "..", "../backend", "../auth-service"); err != nil {
		return nil, err
	}

	redisDB, err := platformconfig.GetEnvInt("REDIS_DATABASE", 0)
	if err != nil {
		return nil, fmt.Errorf("invalid redis db: %w", err)
	}

	noCapacityRetryMs, err := platformconfig.GetEnvInt("WEBSERVICE_NO_CAPACITY_RETRY_MS", 2000)
	if err != nil {
		return nil, fmt.Errorf("invalid no capacity retry ms: %w", err)
	}
	if noCapacityRetryMs < 200 {
		noCapacityRetryMs = 200
	}

	cpuThreshold, err := platformconfig.GetEnvInt("WEBSERVICE_CPU_THRESHOLD_PERCENT", 80)
	if err != nil {
		return nil, fmt.Errorf("invalid cpu threshold percent: %w", err)
	}
	if cpuThreshold < 10 {
		cpuThreshold = 10
	}
	if cpuThreshold > 100 {
		cpuThreshold = 100
	}

	schedulerIntervalSeconds, err := platformconfig.GetEnvInt("WEBSERVICE_SCHEDULER_INTERVAL_SECONDS", 30)
	if err != nil {
		return nil, fmt.Errorf("invalid scheduler interval seconds: %w", err)
	}
	if schedulerIntervalSeconds < 5 {
		schedulerIntervalSeconds = 5
	}

	stuckModelTimeoutMinutes, err := platformconfig.GetEnvInt("WEBSERVICE_STUCK_MODEL_TIMEOUT_MINUTES", 720)
	if err != nil {
		return nil, fmt.Errorf("invalid stuck model timeout minutes: %w", err)
	}
	if stuckModelTimeoutMinutes < 15 {
		stuckModelTimeoutMinutes = 15
	}

	cfg := &Config{
		AppHost:     platformconfig.GetEnv("WEBSERVICE_APP_HOST", "0.0.0.0"),
		AppPort:     platformconfig.GetEnv("WEBSERVICE_APP_PORT", "8082"),
		AppEnv:      platformconfig.GetEnv("APP_ENV", "development"),
		AppTimezone: platformconfig.GetEnv("APP_TIMEZONE", "UTC"),
		Database:    platformconfig.DatabaseFromEnv(),
		Redis: goredis.Options{
			Addr:     fmt.Sprintf("%s:%s", platformconfig.GetEnv("REDIS_HOST", "localhost"), platformconfig.GetEnv("REDIS_PORT", "6379")),
			Username: os.Getenv("REDIS_USERNAME"),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       redisDB,
		},
		Dispatch: DispatchConfig{
			NoCapacityRetryMs:  noCapacityRetryMs,
			CpuThresholdPercent: float64(cpuThreshold),
		},
		Scheduler: SchedulerConfig{
			IntervalSeconds:          schedulerIntervalSeconds,
			StuckModelTimeoutMinutes: stuckModelTimeoutMinutes,
		},
		Backend: BackendConfig{
			URL:            platformconfig.GetEnv("BACKEND_INTERNAL_URL", "http://app-backend:8000"),
			CallbackSecret: platformconfig.GetEnv("CALLBACK_SECRET", ""),
		},
	}

	return cfg, nil
}
