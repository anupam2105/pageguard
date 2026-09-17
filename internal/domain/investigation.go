package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Confidence expresses how sure the co-pilot is about its hypothesis. Only
// three levels — humans read this at 3 AM and finer granularity is noise.
type Confidence string

// Recognised confidence levels. The LLM is asked to pick one of these
// verbatim in its final structured output.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Valid reports whether c is one of the recognised levels.
func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	}
	return false
}

// Evidence is one supporting finding from a tool call. Keeping Source as a
// bounded string keeps downstream metric cardinality safe if we ever surface
// per-source counters.
type Evidence struct {
	Source  string `json:"source"`  // "promql" | "loki" | "shipmetrics" | "runbook"
	Query   string `json:"query"`   // exact query/argument the tool was called with
	Summary string `json:"summary"` // human-readable finding
}

// Investigation is the full structured output the co-pilot returns for one
// alert. It is what gets rendered into the Slack card in M3.
type Investigation struct {
	Alert           Alert         `json:"-"`
	Hypothesis      string        `json:"hypothesis"`
	Confidence      Confidence    `json:"confidence"`
	Evidence        []Evidence    `json:"evidence"`
	SuggestedAction string        `json:"suggested_action"`
	ToolCallsMade   int           `json:"-"`
	Model           string        `json:"-"`
	StartedAt       time.Time     `json:"-"`
	FinishedAt      time.Time     `json:"-"`
	Duration        time.Duration `json:"-"`
}

// Validate enforces that the LLM produced a usable investigation. Any missing
// required field is a bug in the prompt or a refusal — either way, don't ship
// it to Slack silently.
func (i *Investigation) Validate() error {
	var errs []error
	if i.Hypothesis == "" {
		errs = append(errs, errors.New("hypothesis is required"))
	}
	if !i.Confidence.Valid() {
		errs = append(errs, fmt.Errorf("invalid confidence %q", i.Confidence))
	}
	if i.SuggestedAction == "" {
		errs = append(errs, errors.New("suggested_action is required"))
	}
	return errors.Join(errs...)
}

// ParseInvestigation decodes the LLM's final JSON payload. Returned errors
// surface both malformed JSON and validation failures with the same shape,
// so callers can log a single line.
func ParseInvestigation(raw []byte) (*Investigation, error) {
	inv := &Investigation{}
	if err := json.Unmarshal(raw, inv); err != nil {
		return nil, fmt.Errorf("decode investigation: %w", err)
	}
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("validate investigation: %w", err)
	}
	return inv, nil
}
