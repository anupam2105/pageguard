package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/anupam2105/pageguard/internal/config"
	"github.com/anupam2105/pageguard/internal/httpserver"
	"github.com/anupam2105/pageguard/internal/integrations"
	"github.com/anupam2105/pageguard/internal/investigator"
	"github.com/anupam2105/pageguard/internal/logging"
	"github.com/anupam2105/pageguard/internal/observability"
	"github.com/anupam2105/pageguard/internal/service"
	"github.com/anupam2105/pageguard/internal/slack"
	"github.com/anupam2105/pageguard/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.New(cfg.LogLevel, cfg.LogFormat)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = st.Close() }()

	backend, err := buildBackend(&cfg, logger)
	if err != nil {
		return err
	}
	inv, err := investigator.New(&investigator.Config{
		APIKey:        cfg.AnthropicAPIKey,
		Model:         cfg.AnthropicModel,
		MaxIterations: cfg.InvestigationMaxIters,
		Timeout:       cfg.InvestigationTimeout,
		Backend:       backend,
	}, logger)
	if err != nil {
		return fmt.Errorf("build investigator: %w", err)
	}
	if cfg.AnthropicAPIKey == "" {
		logger.Warn("PAGEGUARD_ANTHROPIC_API_KEY not set: /webhooks/alertmanager will answer 503")
	}

	// A typed-nil *slack.Client in an interface would not compare to nil, so
	// only assign the interface when Slack is configured.
	var notifier service.Notifier
	if cfg.SlackBotToken != "" {
		sc, err := slack.New(slack.Config{BotToken: cfg.SlackBotToken, Channel: cfg.SlackChannel})
		if err != nil {
			return fmt.Errorf("build slack client: %w", err)
		}
		notifier = sc
	} else {
		logger.Warn("Slack not configured: investigations are stored and served via /api/v1/investigations only")
	}
	if cfg.APIToken == "" {
		logger.Warn("PAGEGUARD_API_TOKEN not set: webhook and API are unauthenticated")
	}

	dispatcher := service.New(service.Config{
		Workers:     cfg.InvestigationWorkers,
		QueueSize:   cfg.QueueSize,
		DedupWindow: cfg.AlertDedupWindow,
	}, inv, st, notifier, metrics, logger)
	dispatcher.Start()

	srv := httpserver.New(cfg.HTTPAddr, logger, metrics, &httpserver.Deps{
		Alerts:                dispatcher,
		Store:                 st,
		Slack:                 notifier,
		APIToken:              cfg.APIToken,
		SlackSigningSecret:    cfg.SlackSigningSecret,
		InvestigationsEnabled: cfg.AnthropicAPIKey != "",
	})

	errCh := make(chan error, 1)
	go func() {
		logger.Info("starting server", "addr", cfg.HTTPAddr, "version", buildVersion,
			"tools_mode", cfg.ToolsMode, "db", cfg.DBPath, "slack", notifier != nil)
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	}

	// Stop intake first, then drain investigations, then close the store
	// (deferred) so in-flight results are persisted.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "err", err)
	}
	if err := dispatcher.Shutdown(shutdownCtx); err != nil {
		logger.Error("dispatcher shutdown", "err", err)
	}
	logger.Info("shutdown complete")
	return nil
}

func buildBackend(cfg *config.Config, logger *slog.Logger) (investigator.Backend, error) {
	if cfg.ToolsMode == "mock" {
		logger.Warn("PAGEGUARD_TOOLS_MODE=mock: investigations use canned demo data, not your systems")
		return investigator.MockBackend{}, nil
	}
	b := &integrations.Backend{}
	var configured []string
	if cfg.PrometheusURL != "" {
		p, err := integrations.NewPrometheus(integrations.PrometheusConfig{URL: cfg.PrometheusURL, BearerToken: cfg.PrometheusBearerToken})
		if err != nil {
			return nil, fmt.Errorf("prometheus: %w", err)
		}
		b.Prom = p
		configured = append(configured, "prometheus")
	}
	if cfg.LokiURL != "" {
		l, err := integrations.NewLoki(integrations.LokiConfig{URL: cfg.LokiURL, BearerToken: cfg.LokiBearerToken, TenantID: cfg.LokiTenantID})
		if err != nil {
			return nil, fmt.Errorf("loki: %w", err)
		}
		b.Logs = l
		configured = append(configured, "loki")
	}
	if cfg.ShipmetricsURL != "" {
		s, err := integrations.NewShipmetrics(integrations.ShipmetricsConfig{URL: cfg.ShipmetricsURL, APIToken: cfg.ShipmetricsAPIToken})
		if err != nil {
			return nil, fmt.Errorf("shipmetrics: %w", err)
		}
		b.Deploys = s
		configured = append(configured, "shipmetrics")
	}
	if cfg.RunbooksDir != "" {
		r, err := integrations.NewRunbooks(cfg.RunbooksDir)
		if err != nil {
			return nil, fmt.Errorf("runbooks: %w", err)
		}
		b.RunbookDir = r
		configured = append(configured, "runbooks")
	}
	logger.Info("tool backends", "mode", "live", "configured", configured)
	return b, nil
}

// buildVersion is populated at build time via -ldflags.
var buildVersion = "dev"
