// Package domain contains the core types shared across pageguard packages.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Alert is the canonical representation of a firing Prometheus alert. The
// Alertmanager webhook receiver (M3) will populate this from the payload;
// the Investigator only ever reads it, so the shape stays consumer-first.
type Alert struct {
	Name        string
	Severity    string // e.g. "page", "ticket", "info"
	Service     string
	Environment string
	Summary     string // short one-line human description
	Description string // longer explanation, often the alertmanager annotation body
	StartedAt   time.Time
	Labels      map[string]string
	Annotations map[string]string
}

// Validate enforces the minimum fields the investigator relies on. Aggregated
// error reporting via errors.Join so multiple violations surface together.
func (a *Alert) Validate() error {
	var errs []error
	if a.Name == "" {
		errs = append(errs, errors.New("name is required"))
	}
	if a.Service == "" {
		errs = append(errs, errors.New("service is required"))
	}
	if a.Environment == "" {
		errs = append(errs, errors.New("environment is required"))
	}
	if a.StartedAt.IsZero() {
		errs = append(errs, errors.New("started_at is required"))
	}
	return errors.Join(errs...)
}

// Header formats a short one-liner suitable for the LLM's first user message.
// Keeping it tight helps token budgets on the initial request.
func (a *Alert) Header() string {
	sev := a.Severity
	if sev == "" {
		sev = "unknown"
	}
	summary := a.Summary
	if summary == "" {
		summary = a.Description
	}
	return fmt.Sprintf("[%s] %s on %s/%s: %s",
		sev, a.Name, a.Service, a.Environment, summary)
}
