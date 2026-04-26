package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	PostgresDSN  string
	PollInterval time.Duration
	BatchSize    int
	RetryDelay   time.Duration
	MaxAttempts  int
}

func Load() (Config, error) {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		return Config{}, fmt.Errorf("POSTGRES_DSN is required")
	}

	pollSeconds := 5
	if value := os.Getenv("WORKER_POLL_INTERVAL_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("WORKER_POLL_INTERVAL_SECONDS must be an integer")
		}
		pollSeconds = parsed
	}

	batchSize := 10
	if value := os.Getenv("WORKER_BATCH_SIZE"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("WORKER_BATCH_SIZE must be an integer")
		}
		batchSize = parsed
	}

	retryDelaySeconds := 60
	if value := os.Getenv("WORKER_RETRY_DELAY_SECONDS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("WORKER_RETRY_DELAY_SECONDS must be an integer")
		}
		retryDelaySeconds = parsed
	}

	maxAttempts := 3
	if value := os.Getenv("WORKER_MAX_ATTEMPTS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("WORKER_MAX_ATTEMPTS must be an integer")
		}
		maxAttempts = parsed
	}

	return Config{
		PostgresDSN:  dsn,
		PollInterval: time.Duration(pollSeconds) * time.Second,
		BatchSize:    batchSize,
		RetryDelay:   time.Duration(retryDelaySeconds) * time.Second,
		MaxAttempts:  maxAttempts,
	}, nil
}
