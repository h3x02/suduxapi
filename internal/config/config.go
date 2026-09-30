package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	AppEnv  string
	AppPort string

	DatabaseURL string
	RedisURL    string

	JWTAccessSecret  string
	JWTRefreshSecret string
	JWTAccessTTL    time.Duration
	JWTRefreshTTL   time.Duration

	ResendAPIKey    string
	ResendFromEmail string

	AuthCodeTTL             time.Duration
	AuthCodeMaxAttempts     int
	AuthResendCooldown      time.Duration
	AuthMaxRequestsPerEmail int
	AuthMaxRequestsPerIP    int
}

func Load() *Config {
	return &Config{
		AppEnv:                  getEnv("APP_ENV", "development"),
		AppPort:                 getEnv("APP_PORT", "8080"),
		DatabaseURL:             getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/sudux?sslmode=disable"),
		RedisURL:                getEnv("REDIS_URL", "redis://localhost:6379/0"),
		JWTAccessSecret:         getEnv("JWT_ACCESS_SECRET", "super-secret-access-key-change-in-prod"),
		JWTRefreshSecret:        getEnv("JWT_REFRESH_SECRET", "super-secret-refresh-key-change-in-prod"),
		JWTAccessTTL:            getDurationEnv("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:           getDurationEnv("JWT_REFRESH_TTL", 720*time.Hour), // 30 days
		ResendAPIKey:            getEnv("RESEND_API_KEY", "re_xxxxxxxxx"),
		ResendFromEmail:         getEnv("RESEND_FROM_EMAIL", "onboarding@resend.dev"),
		AuthCodeTTL:             getDurationEnv("AUTH_CODE_TTL", 5*time.Minute),
		AuthCodeMaxAttempts:     getIntEnv("AUTH_CODE_MAX_ATTEMPTS", 5),
		AuthResendCooldown:      getDurationEnv("AUTH_RESEND_COOLDOWN", 60*time.Second),
		AuthMaxRequestsPerEmail: getIntEnv("AUTH_MAX_REQUESTS_PER_EMAIL", 5),
		AuthMaxRequestsPerIP:    getIntEnv("AUTH_MAX_REQUESTS_PER_IP", 20),
	}
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return fallback
}

func getIntEnv(key string, fallback int) int {
	if valStr, ok := os.LookupEnv(key); ok {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return fallback
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	if valStr, ok := os.LookupEnv(key); ok {
		if val, err := time.ParseDuration(valStr); err == nil {
			return val
		}
	}
	return fallback
}
