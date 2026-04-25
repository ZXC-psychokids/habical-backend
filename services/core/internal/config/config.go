package config

import "os"

type Config struct {
	Port        string
	PostgresDSN string
	JWTSecret   string
}

func Load() Config {
	return Config{
		Port:        getenv("CORE_PORT", "4012"),
		PostgresDSN: getenv("POSTGRES_DSN", "postgres://postgres:postgres@127.0.0.1:5432/habical?sslmode=disable"),
		JWTSecret:   getenv("JWT_SECRET", "dev_secret_change_me"),
	}
}

func getenv(key string, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}
