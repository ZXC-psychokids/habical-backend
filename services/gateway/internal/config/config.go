package config

import "os"

type Config struct {
	Port                     string
	JWTSecret                string
	AuthURL                  string
	CoreURL                  string
	SocialURL                string
	HTTPClientTimeoutSeconds int
}

func Load() Config {
	return Config{
		Port:                     getenv("GATEWAY_PORT", "4010"),
		JWTSecret:                getenv("JWT_SECRET", "dev_secret_change_me"),
		AuthURL:                  getenv("AUTH_URL", "http://127.0.0.1:4011"),
		CoreURL:                  getenv("CORE_URL", "http://127.0.0.1:4012"),
		SocialURL:                getenv("SOCIAL_URL", "http://127.0.0.1:4013"),
		HTTPClientTimeoutSeconds: getenvInt("GATEWAY_HTTP_TIMEOUT_SECONDS", 15),
	}
}

func getenv(key string, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func getenvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return defaultValue
		}
		n = n*10 + int(r-'0')
	}
	if n <= 0 {
		return defaultValue
	}
	return n
}
