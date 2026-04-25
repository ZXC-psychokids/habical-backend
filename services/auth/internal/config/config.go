package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port             string
	PostgresDSN      string
	JWTSecret        string
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	PasswordResetTTL time.Duration
	AvatarDir        string
	AvatarBaseURL    string
}

func Load() Config {
	return Config{
		Port:             getenv("AUTH_PORT", "4011"),
		PostgresDSN:      getenv("POSTGRES_DSN", "postgres://postgres:postgres@127.0.0.1:5432/habical?sslmode=disable"),
		JWTSecret:        getenv("JWT_SECRET", "dev_secret_change_me"),
		AccessTTL:        getDuration("ACCESS_TTL_MINUTES", 30, time.Minute),
		RefreshTTL:       getDuration("REFRESH_TTL_HOURS", 720, time.Hour),
		PasswordResetTTL: getDuration("PASSWORD_RESET_TTL_MINUTES", 30, time.Minute),
		AvatarDir:        getenv("AVATAR_STORAGE_DIR", "backend/storage/avatars"),
		AvatarBaseURL:    getenv("AVATAR_BASE_URL", "https://cdn.habical.app/avatars"),
	}
}

func getDuration(key string, defaultValue int, unit time.Duration) time.Duration {
	raw := getenv(key, "")
	if raw == "" {
		return time.Duration(defaultValue) * unit
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return time.Duration(defaultValue) * unit
	}
	return time.Duration(value) * unit
}

func getenv(key string, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
