// Package config loads and validates process configuration from environment variables.
package config

import (
	"errors"
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

	// Investigator (LLM) configuration. AnthropicAPIKey is optional so the
	// process (health, metrics, history API) boots without one; the alert
	// webhook answers 503 until it is set.
	AnthropicAPIKey       string
	AnthropicModel        string
	InvestigationTimeout  time.Duration
	InvestigationMaxIters int

	// Tool backends. ToolsMode "mock" serves canned demo data; "live" uses
	// the URLs below, and any unset one reports itself as not configured.
	ToolsMode             string
	PrometheusURL         string
	PrometheusBearerToken string
	LokiURL               string
	LokiBearerToken       string
	LokiTenantID          string
	ShipmetricsURL        string
	ShipmetricsAPIToken   string
	RunbooksDir           string

	// APIToken protects /webhooks/alertmanager and /api/v1/* when set.
	APIToken string

	// Slack (bot-token mode). Disabled when SlackBotToken is empty.
	SlackBotToken      string
	SlackChannel       string
	SlackSigningSecret string

	DBPath               string
	InvestigationWorkers int
	QueueSize            int
	AlertDedupWindow     time.Duration
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

		ToolsMode:             strings.ToLower(getEnv("PAGEGUARD_TOOLS_MODE", "live")),
		PrometheusURL:         getEnv("PAGEGUARD_PROMETHEUS_URL", ""),
		PrometheusBearerToken: getEnv("PAGEGUARD_PROMETHEUS_BEARER_TOKEN", ""),
		LokiURL:               getEnv("PAGEGUARD_LOKI_URL", ""),
		LokiBearerToken:       getEnv("PAGEGUARD_LOKI_BEARER_TOKEN", ""),
		LokiTenantID:          getEnv("PAGEGUARD_LOKI_TENANT_ID", ""),
		ShipmetricsURL:        getEnv("PAGEGUARD_SHIPMETRICS_URL", ""),
		ShipmetricsAPIToken:   getEnv("PAGEGUARD_SHIPMETRICS_API_TOKEN", ""),
		RunbooksDir:           getEnv("PAGEGUARD_RUNBOOKS_DIR", ""),

		APIToken: getEnv("PAGEGUARD_API_TOKEN", ""),

		SlackBotToken:      getEnv("PAGEGUARD_SLACK_BOT_TOKEN", ""),
		SlackChannel:       getEnv("PAGEGUARD_SLACK_CHANNEL", ""),
		SlackSigningSecret: getEnv("PAGEGUARD_SLACK_SIGNING_SECRET", ""),

		DBPath:               getEnv("PAGEGUARD_DB_PATH", "pageguard.db"),
		InvestigationWorkers: parseIntOr("PAGEGUARD_INVESTIGATION_WORKERS", 2),
		QueueSize:            parseIntOr("PAGEGUARD_QUEUE_SIZE", 64),
		AlertDedupWindow:     parseDurationOr("PAGEGUARD_ALERT_DEDUP_WINDOW", 30*time.Minute),
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

	switch cfg.ToolsMode {
	case "live", "mock":
	default:
		return Config{}, fmt.Errorf("invalid PAGEGUARD_TOOLS_MODE: %q (want live|mock)", cfg.ToolsMode)
	}

	if cfg.SlackBotToken != "" {
		if cfg.SlackChannel == "" {
			return Config{}, errors.New("PAGEGUARD_SLACK_CHANNEL is required when PAGEGUARD_SLACK_BOT_TOKEN is set")
		}
		if cfg.SlackSigningSecret == "" {
			return Config{}, errors.New("PAGEGUARD_SLACK_SIGNING_SECRET is required when PAGEGUARD_SLACK_BOT_TOKEN is set (feedback buttons are verified with it)")
		}
	}

	if cfg.InvestigationWorkers <= 0 || cfg.InvestigationWorkers > 32 {
		return Config{}, fmt.Errorf("invalid PAGEGUARD_INVESTIGATION_WORKERS: %d (want 1-32)", cfg.InvestigationWorkers)
	}
	if cfg.QueueSize <= 0 {
		return Config{}, fmt.Errorf("invalid PAGEGUARD_QUEUE_SIZE: %d (must be > 0)", cfg.QueueSize)
	}
	if cfg.DBPath == "" {
		return Config{}, errors.New("PAGEGUARD_DB_PATH must not be empty")
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
