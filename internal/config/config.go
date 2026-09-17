// Package config loads and validates process configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config holds all process configuration derived from the environment.
type Config struct {
	HTTPAddr        string
	LogLevel        string
	LogFormat       string
	ShutdownTimeout time.Duration

	// Investigator (LLM) configuration. AnthropicAPIKey is optional in M1:
	// the process boots without one, but Investigate() calls return an error.
	// M3 (Alertmanager webhook) will require it at that point.
	AnthropicAPIKey       string
	AnthropicModel        string
	InvestigationTimeout  time.Duration
	InvestigationMaxIters int
}

// Load reads configuration from environment variables and validates it.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:              getEnv("PAGEGUARD_HTTP_ADDR", ":8080"),
		LogLevel:              strings.ToLower(getEnv("PAGEGUARD_LOG_LEVEL", "info")),
		LogFormat:             strings.ToLower(getEnv("PAGEGUARD_LOG_FORMAT", "json")),
		ShutdownTimeout:       15 * time.Second,
		AnthropicAPIKey:       getEnv("PAGEGUARD_ANTHROPIC_API_KEY", ""),
		AnthropicModel:        getEnv("PAGEGUARD_ANTHROPIC_MODEL", "claude-sonnet-5"),
		InvestigationTimeout:  parseDurationOr("PAGEGUARD_INVESTIGATION_TIMEOUT", 60*time.Second),
		InvestigationMaxIters: parseIntOr("PAGEGUARD_INVESTIGATION_MAX_ITERS", 8),
	}

	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("invalid PAGEGUARD_LOG_LEVEL: %q (want debug|info|warn|error)", cfg.LogLevel)
	}

	switch cfg.LogFormat {
	case "json", "text":
	default:
		return Config{}, fmt.Errorf("invalid PAGEGUARD_LOG_FORMAT: %q (want json|text)", cfg.LogFormat)
	}

	if cfg.InvestigationMaxIters <= 0 {
		return Config{}, fmt.Errorf("invalid PAGEGUARD_INVESTIGATION_MAX_ITERS: %d (must be > 0)", cfg.InvestigationMaxIters)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func parseDurationOr(key string, fallback time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func parseIntOr(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}
