package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/anupam2105/pageguard/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PAGEGUARD_HTTP_ADDR", "")
	t.Setenv("PAGEGUARD_LOG_LEVEL", "")
	t.Setenv("PAGEGUARD_LOG_FORMAT", "")
	t.Setenv("PAGEGUARD_ANTHROPIC_API_KEY", "")
	t.Setenv("PAGEGUARD_ANTHROPIC_MODEL", "")
	t.Setenv("PAGEGUARD_INVESTIGATION_TIMEOUT", "")
	t.Setenv("PAGEGUARD_INVESTIGATION_MAX_ITERS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat = %q, want json", cfg.LogFormat)
	}
	if cfg.AnthropicAPIKey != "" {
		t.Errorf("AnthropicAPIKey should default empty, got %q", cfg.AnthropicAPIKey)
	}
	if cfg.AnthropicModel != "claude-sonnet-5" {
		t.Errorf("AnthropicModel = %q, want claude-sonnet-5", cfg.AnthropicModel)
	}
	if cfg.InvestigationTimeout != 60*time.Second {
		t.Errorf("InvestigationTimeout = %v, want 60s", cfg.InvestigationTimeout)
	}
	if cfg.InvestigationMaxIters != 8 {
		t.Errorf("InvestigationMaxIters = %d, want 8", cfg.InvestigationMaxIters)
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv("PAGEGUARD_LOG_LEVEL", "trace")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
}

func TestLoadRejectsInvalidLogFormat(t *testing.T) {
	t.Setenv("PAGEGUARD_LOG_FORMAT", "xml")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid log format, got nil")
	}
}

func TestLoadRejectsInvalidMaxIters(t *testing.T) {
	t.Setenv("PAGEGUARD_INVESTIGATION_MAX_ITERS", "0")

	// A parse failure or a <= 0 value both surface as "invalid ... max_iters".
	// Passing "0" produces the <=0 rejection via the validation branch.
	// Because parseIntOr falls back on parse failure, non-numeric input would
	// silently use the default — that is acceptable; users get the default
	// rather than a boot failure.
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "MAX_ITERS") {
		t.Fatalf("expected MAX_ITERS validation error, got: %v", err)
	}
}

func TestLoadHonoursEnvOverrides(t *testing.T) {
	// Not a real credential — synthetic token used only to assert env plumbing.
	const fakeKey = "sk-ant-fake-key-for-test" //nolint:gosec // G101 false positive
	t.Setenv("PAGEGUARD_HTTP_ADDR", ":9090")
	t.Setenv("PAGEGUARD_LOG_LEVEL", "DEBUG")
	t.Setenv("PAGEGUARD_LOG_FORMAT", "TEXT")
	t.Setenv("PAGEGUARD_ANTHROPIC_API_KEY", fakeKey)
	t.Setenv("PAGEGUARD_ANTHROPIC_MODEL", "claude-opus-5")
	t.Setenv("PAGEGUARD_INVESTIGATION_TIMEOUT", "45s")
	t.Setenv("PAGEGUARD_INVESTIGATION_MAX_ITERS", "12")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want :9090", cfg.HTTPAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (case-insensitive)", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text (case-insensitive)", cfg.LogFormat)
	}
	if cfg.AnthropicAPIKey != fakeKey {
		t.Errorf("AnthropicAPIKey did not pick up env override")
	}
	if cfg.AnthropicModel != "claude-opus-5" {
		t.Errorf("AnthropicModel = %q, want claude-opus-5", cfg.AnthropicModel)
	}
	if cfg.InvestigationTimeout != 45*time.Second {
		t.Errorf("InvestigationTimeout = %v, want 45s", cfg.InvestigationTimeout)
	}
	if cfg.InvestigationMaxIters != 12 {
		t.Errorf("InvestigationMaxIters = %d, want 12", cfg.InvestigationMaxIters)
	}
}

func TestLoadM3Defaults(t *testing.T) {
	for _, k := range []string{"PAGEGUARD_TOOLS_MODE", "PAGEGUARD_DB_PATH", "PAGEGUARD_SLACK_BOT_TOKEN",
		"PAGEGUARD_INVESTIGATION_WORKERS", "PAGEGUARD_ALERT_DEDUP_WINDOW"} {
		t.Setenv(k, "")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ToolsMode != "live" || cfg.DBPath != "pageguard.db" || cfg.InvestigationWorkers != 2 || cfg.AlertDedupWindow != 30*time.Minute {
		t.Errorf("defaults = mode %q db %q workers %d dedup %v", cfg.ToolsMode, cfg.DBPath, cfg.InvestigationWorkers, cfg.AlertDedupWindow)
	}
}

func TestLoadRejectsInvalidToolsMode(t *testing.T) {
	t.Setenv("PAGEGUARD_TOOLS_MODE", "fake")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "TOOLS_MODE") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadSlackRequiresChannelAndSigningSecret(t *testing.T) {
	t.Setenv("PAGEGUARD_SLACK_BOT_TOKEN", "xoxb-test")
	t.Setenv("PAGEGUARD_SLACK_CHANNEL", "")
	t.Setenv("PAGEGUARD_SLACK_SIGNING_SECRET", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "SLACK_CHANNEL") {
		t.Fatalf("missing channel: err = %v", err)
	}
	t.Setenv("PAGEGUARD_SLACK_CHANNEL", "#oncall")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "SIGNING_SECRET") {
		t.Fatalf("missing secret: err = %v", err)
	}
	t.Setenv("PAGEGUARD_SLACK_SIGNING_SECRET", "abc")
	if _, err := config.Load(); err != nil {
		t.Fatalf("complete slack config: %v", err)
	}
}
