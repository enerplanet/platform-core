package config

import (
	"os"

	"platform.local/platform/auth"
	platformconfig "platform.local/platform/config"

	goredis "github.com/redis/go-redis/v9"
)

type Config struct {
	Auth              auth.Config
	RedisConfig       goredis.Options
	AppPort           string
	AppHost           string
	AppURL            string
	FrontendURL       string
	AppEnv            string
	AppTimezone       string
	CookieDomain      string
	Database          platformconfig.DatabaseConfig
	SessionTTLMinutes int // Session timeout in minutes
	Email             EmailConfig
}

type EmailConfig struct {
	platformconfig.EmailSettings
	EnableNotifications           bool
	EnableBrowserNotifications    bool
	EmailVerificationRequired     bool
	VerificationTokenExpiryHours  int
	PasswordResetTokenExpiryHours int
}

func LoadFromEnv() (*Config, error) {
	if err := platformconfig.LoadEnvOnce(".", ".."); err != nil {
		return nil, err
	}

	redisDB, err := platformconfig.RequireEnvInt("REDIS_DATABASE")
	if err != nil {
		return nil, err
	}

	sessionTTL, err := platformconfig.GetEnvInt("SESSION_TTL_MINUTES", 60)
	if err != nil {
		return nil, err
	}

	emailSettings := platformconfig.EmailSettingsFromEnv()

	cfg := &Config{
		Auth:              platformconfig.AuthConfigFromEnv(),
		RedisConfig:       platformconfig.RedisOptionsFromEnv(redisDB),
		AppPort:           os.Getenv("APP_PORT"),
		AppHost:           os.Getenv("APP_HOST"),
		AppURL:            os.Getenv("APP_URL"),
		FrontendURL:       platformconfig.GetEnv("FRONTEND_URL", "http://localhost:3000"),
		AppEnv:            platformconfig.GetEnv("APP_ENV", "development"),
		AppTimezone:       platformconfig.GetEnv("APP_TIMEZONE", "UTC"),
		CookieDomain:      os.Getenv("COOKIE_DOMAIN"),
		SessionTTLMinutes: sessionTTL,
		Email:             buildEmailConfig(emailSettings),
		Database:          platformconfig.DatabaseFromEnv(),
	}
	return cfg, nil
}

// buildEmailConfig constructs email configuration from environment
func buildEmailConfig(base platformconfig.EmailSettings) EmailConfig {
	verificationHours, err := platformconfig.GetEnvInt("VERIFICATION_TOKEN_EXPIRY_HOURS", 24)
	if err != nil {
		verificationHours = 24
	}
	resetHours, err := platformconfig.GetEnvInt("PASSWORD_RESET_TOKEN_EXPIRY_HOURS", 1)
	if err != nil {
		resetHours = 1
	}

	return EmailConfig{
		EmailSettings:                 base,
		EnableNotifications:           platformconfig.GetEnvBool("ENABLE_EMAIL_NOTIFICATIONS", false),
		EnableBrowserNotifications:    platformconfig.GetEnvBool("ENABLE_BROWSER_NOTIFICATIONS", false),
		EmailVerificationRequired:     platformconfig.GetEnvBool("EMAIL_VERIFICATION_REQUIRED", false),
		VerificationTokenExpiryHours:  verificationHours,
		PasswordResetTokenExpiryHours: resetHours,
	}
}
