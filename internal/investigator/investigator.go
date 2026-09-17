package investigator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/anupam2105/pageguard/internal/domain"
)

// Config controls the investigator's runtime shape. All fields have safe
// zero-value defaults except APIKey — no key means Investigate() returns
// an error at call time rather than at construction, so the process can
// still boot without one (M0 semantics).
type Config struct {
	APIKey        string
	Model         string        // e.g. "claude-sonnet-5"
	MaxIterations int           // ceiling on tool-use loop iterations
	MaxTokens     int           // per-response cap; SDK requires it
	Timeout       time.Duration // budget for one full investigation
}

// DefaultConfig returns production-shaped defaults. Callers overlay their
// own values on top; empty fields are filled from here.
func DefaultConfig() Config {
	return Config{
		Model:         "claude-sonnet-5",
		MaxIterations: 8,
		MaxTokens:     16000,
		Timeout:       investigationTimeoutFallback,
	}
}

// Investigator turns an alert into a structured Investigation via a Claude
// tool-use loop. Safe to reuse across calls — the underlying SDK client
// handles its own concurrency and retries.
type Investigator struct {
	client *anthropic.Client
	tools  []anthropic.BetaTool
	cfg    Config
	logger *slog.Logger
}

// New constructs an Investigator. Returns an error only if tool registration
// fails (an internal bug — the mocks all compile). Missing APIKey is not a
// construction error; Investigate() surfaces it if actually invoked.
func New(cfg Config, logger *slog.Logger) (*Investigator, error) {
	defaults := DefaultConfig()
	if cfg.Model == "" {
		cfg.Model = defaults.Model
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaults.MaxIterations
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaults.MaxTokens
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaults.Timeout
	}

	tools, err := defaultTools()
	if err != nil {
		return nil, fmt.Errorf("register tools: %w", err)
	}

	// The SDK builds its own auth from ANTHROPIC_API_KEY when no option is
	// provided; passing an explicit key overrides that. We wrap the client
	// construction so pageguard can supply the key from its own config
	// rather than requiring an env var.
	var client *anthropic.Client
	if cfg.APIKey != "" {
		c := anthropic.NewClient(option.WithAPIKey(cfg.APIKey))
		client = &c
	} else {
		c := anthropic.NewClient()
		client = &c
	}

	return &Investigator{
		client: client,
		tools:  tools,
		cfg:    cfg,
		logger: logger,
	}, nil
}

// Investigate runs the tool-use loop for a single alert and returns the
// structured result. Uses ctx for the outer deadline; per-invocation the
// configured Timeout wraps it so a hung LLM never blocks a webhook worker
// indefinitely.
func (i *Investigator) Investigate(ctx context.Context, alert *domain.Alert) (*domain.Investigation, error) {
	if alert == nil {
		return nil, errors.New("alert is required")
	}
	if i.cfg.APIKey == "" {
		return nil, errors.New("no Anthropic API key configured; set PAGEGUARD_ANTHROPIC_API_KEY")
	}
	if err := alert.Validate(); err != nil {
		return nil, fmt.Errorf("validate alert: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, i.cfg.Timeout)
	defer cancel()

	startedAt := time.Now()

	runner := i.client.Beta.Messages.NewToolRunner(
		i.tools,
		anthropic.BetaToolRunnerParams{
			BetaMessageNewParams: anthropic.BetaMessageNewParams{
				Model:     i.cfg.Model,
				MaxTokens: int64(i.cfg.MaxTokens),
				System: []anthropic.BetaTextBlockParam{{
					Text: systemPrompt,
				}},
				Messages: []anthropic.BetaMessageParam{
					anthropic.NewBetaUserMessage(
						anthropic.NewBetaTextBlock(buildUserPrompt(alert)),
					),
				},
			},
			MaxIterations: i.cfg.MaxIterations,
		},
	)

	final, err := runner.RunToCompletion(ctx)
	if err != nil {
		return nil, fmt.Errorf("tool-use loop: %w", err)
	}

	finishedAt := time.Now()

	// Refusal handling — surface it as an error rather than trying to parse
	// the response as an investigation.
	if final.StopReason == anthropic.BetaStopReasonRefusal {
		return nil, fmt.Errorf("model refused: %s", stopDetailsSummary(final))
	}

	body, err := extractFinalText(final)
	if err != nil {
		return nil, err
	}

	inv, err := domain.ParseInvestigation([]byte(body))
	if err != nil {
		i.logger.Warn("investigation parse failed",
			"alert", alert.Name,
			"raw_body_prefix", firstN(body, 200),
			"error", err,
		)
		return nil, fmt.Errorf("parse investigation: %w", err)
	}

	inv.Alert = *alert
	inv.Model = i.cfg.Model
	inv.StartedAt = startedAt
	inv.FinishedAt = finishedAt
	inv.Duration = finishedAt.Sub(startedAt)
	inv.ToolCallsMade = countToolCalls(final)

	return inv, nil
}

// extractFinalText pulls the assistant's terminal text out of the final
// message. The LLM was instructed to reply with a pure JSON object, so any
// text block content should be the JSON payload. If the response contains
// no text block, that is a prompt/model failure worth surfacing.
func extractFinalText(msg *anthropic.BetaMessage) (string, error) {
	var b strings.Builder
	// Index-based iteration is required — the SDK's BetaContentBlockUnion is
	// a flattened union type ~2.5 KiB wide; ranging by value would copy it
	// on every iteration.
	for idx := range msg.Content {
		if tb, ok := msg.Content[idx].AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	body := strings.TrimSpace(b.String())
	if body == "" {
		return "", errors.New("model returned no text content")
	}
	return body, nil
}

// countToolCalls counts tool_use blocks across the final assistant message.
// Note: this only counts calls in the terminal turn, not the whole session —
// full history is available inside the runner if a caller wants a total, but
// the terminal-turn count is the interesting signal for cost accounting.
func countToolCalls(msg *anthropic.BetaMessage) int {
	n := 0
	for idx := range msg.Content {
		if _, ok := msg.Content[idx].AsAny().(anthropic.BetaToolUseBlock); ok {
			n++
		}
	}
	return n
}

// stopDetailsSummary formats StopDetails for logs.
func stopDetailsSummary(msg *anthropic.BetaMessage) string {
	sd := msg.StopDetails
	if sd.Category == "" && sd.Explanation == "" {
		return "no details"
	}
	return fmt.Sprintf("category=%q explanation=%q", sd.Category, sd.Explanation)
}

// firstN safely truncates a string for log lines.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
