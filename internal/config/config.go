package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv  string
	AppPort string

	DatabaseURL string
	RedisURL    string

	JWTAccessSecret  string
	JWTRefreshSecret string
	JWTAccessTTL     time.Duration
	JWTRefreshTTL    time.Duration

	ResendAPIKey    string
	ResendFromEmail string

	AuthCodeTTL             time.Duration
	AuthCodeMaxAttempts     int
	AuthResendCooldown      time.Duration
	AuthMaxRequestsPerEmail int
	AuthMaxRequestsPerIP    int

	// TrustProxyHeaders: when true, the first X-Forwarded-For entry is trusted
	// for rate limiting. Only enable behind a trusted reverse proxy / LB.
	TrustProxyHeaders bool

	// Match & room lifecycle
	MatchMaxDuration time.Duration // hard server-side deadline for a match
	DisconnectGrace  time.Duration // grace before a match with no connected humans is finalized
	RoomEvictAfter   time.Duration // how long finished rooms stay in memory before eviction

	// Anti brute-force move throttling
	MoveMinInterval   time.Duration // minimum interval between any two moves of one player
	WrongMoveCooldown time.Duration // extra cooldown after a wrong move
	WrongMovePenalty  int           // points subtracted on a wrong move (score floors at 0)
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
		TrustProxyHeaders:       getBoolEnv("TRUST_PROXY_HEADERS", false),
		MatchMaxDuration:        getDurationEnv("MATCH_MAX_DURATION", 30*time.Minute),
		DisconnectGrace:         getDurationEnv("DISCONNECT_GRACE", 60*time.Second),
		RoomEvictAfter:          getDurationEnv("ROOM_EVICT_AFTER", 10*time.Minute),
		MoveMinInterval:         getDurationEnv("MOVE_MIN_INTERVAL", 250*time.Millisecond),
		WrongMoveCooldown:       getDurationEnv("WRONG_MOVE_COOLDOWN", 2*time.Second),
		WrongMovePenalty:        getIntEnv("WRONG_MOVE_PENALTY", 50),
	}
}

// Validate fails startup on configurations that are unsafe in production.
func (c *Config) Validate() error {
	var errs []error
	if strings.EqualFold(c.AppEnv, "production") {
		if isWeakSecret(c.JWTAccessSecret) {
			errs = append(errs, errors.New("JWT_ACCESS_SECRET is missing, too short (<16 chars) or a known default"))
		}
		if isWeakSecret(c.JWTRefreshSecret) {
			errs = append(errs, errors.New("JWT_REFRESH_SECRET is missing, too short (<16 chars) or a known default"))
		}
		if c.ResendAPIKey == "" || c.ResendAPIKey == "re_xxxxxxxxx" {
			errs = append(errs, errors.New("RESEND_API_KEY must be set in production"))
		}
	}
	if c.MatchMaxDuration <= 0 {
		errs = append(errs, errors.New("MATCH_MAX_DURATION must be positive"))
	}
	if c.MoveMinInterval < 0 || c.WrongMoveCooldown < 0 || c.WrongMovePenalty < 0 {
		errs = append(errs, errors.New("move throttle values must be non-negative"))
	}
	return errors.Join(errs...)
}

func isWeakSecret(s string) bool {
	if s == "" || len(s) < 16 {
		return true
	}
	weakDefaults := []string{
		"super-secret-access-key-change-in-prod",
		"super-secret-refresh-key-change-in-prod",
		"dev-access-secret",
		"dev-refresh-secret",
	}
	for _, w := range weakDefaults {
		if s == w {
			return true
		}
	}
	return strings.Contains(strings.ToLower(s), "change-me") || strings.Contains(strings.ToLower(s), "change-in-prod")
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

func getBoolEnv(key string, fallback bool) bool {
	if valStr, ok := os.LookupEnv(key); ok {
		if val, err := strconv.ParseBool(valStr); err == nil {
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

func (c *Config) String() string {
	return fmt.Sprintf("env=%s port=%s matchMaxDuration=%s", c.AppEnv, c.AppPort, c.MatchMaxDuration)
}
