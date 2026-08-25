// Package config reads every runtime setting from the environment into one
// flat struct, so the rest of the service never touches os.Getenv.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds every environment-driven setting for the service.
type Config struct {
	// Port is the public HTTP API port.
	Port string
	// MetricsPort serves /metrics and /health for Prometheus and probes.
	MetricsPort string
	// DatabaseURL is the Postgres connection string. Required.
	DatabaseURL string
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// HoldTTL is how long a rental_hold blocks the car before it expires.
	HoldTTL time.Duration
	// SearchRangeMinMeters clamps the smallest allowed availability radius.
	SearchRangeMinMeters float64
	// SearchRangeMaxMeters clamps the largest allowed availability radius.
	SearchRangeMaxMeters float64
	// InvariantCheckInterval is how often the double-booking invariant runs.
	InvariantCheckInterval time.Duration
}

// Load builds a Config from the environment, applying defaults.
func Load() Config {
	return Config{
		Port:                   envOrDefault("PORT", "3000"),
		MetricsPort:            envOrDefault("METRICS_PORT", "9090"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		LogLevel:               envOrDefault("LOG_LEVEL", "info"),
		HoldTTL:                parseDurationOr("HOLD_TTL", 10*time.Minute),
		SearchRangeMinMeters:   parseFloatOr("SEARCH_RANGE_MIN_METERS", 100),
		SearchRangeMaxMeters:   parseFloatOr("SEARCH_RANGE_MAX_METERS", 50_000),
		InvariantCheckInterval: parseDurationOr("INVARIANT_CHECK_INTERVAL", time.Minute),
	}
}

// Require returns an error naming every listed environment variable that is
// missing, so a bad deploy reports all problems at once.
func (config Config) Require(names ...string) error {
	var missing []string
	for _, name := range names {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func parseDurationOr(name string, fallback time.Duration) time.Duration {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func parseFloatOr(name string, fallback float64) float64 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	var parsed float64
	if _, err := fmt.Sscanf(value, "%g", &parsed); err != nil {
		return fallback
	}
	return parsed
}
