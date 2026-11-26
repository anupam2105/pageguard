package integrations

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const maxRunbookBytes = 32 << 10

// Runbooks serves Markdown runbooks from a local directory. A runbook matches
// an alert when its file stem equals the alert name ignoring case and
// punctuation, so HighErrorRate matches high-error-rate.md or
// HighErrorRate.md. Lookup never joins the alert name into a path, so a
// hostile alert name cannot escape the directory.
type Runbooks struct {
	dir string
}

// NewRunbooks validates dir and returns a Runbooks reader.
func NewRunbooks(dir string) (*Runbooks, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("runbooks: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("runbooks: %s is not a directory", dir)
	}
	return &Runbooks{dir: dir}, nil
}

func normaliseName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Lookup returns the runbook text for alertName, or a "not found" message
// listing what is available.
func (r *Runbooks) Lookup(alertName string) (string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return "", fmt.Errorf("read runbook dir: %w", err)
	}
	want := normaliseName(alertName)
	var available []string
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".md" && ext != ".markdown" {
			continue
		}
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		available = append(available, stem)
		if want == "" || normaliseName(stem) != want {
			continue
		}
		body, err := readCapped(filepath.Join(r.dir, name))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Runbook %s for alert %q:\n\n%s", name, alertName, body), nil
	}
	sort.Strings(available)
	return fmt.Sprintf("No runbook found for %q. Available runbooks: %s. Follow the generic incident playbook: correlate with recent deploys, gather evidence, prefer reversible mitigations.",
		alertName, strings.Join(available, ", ")), nil
}

func readCapped(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path comes from os.ReadDir of the configured dir, not user input
	if err != nil {
		return "", fmt.Errorf("open runbook: %w", err)
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, maxRunbookBytes+1))
	if err != nil {
		return "", fmt.Errorf("read runbook: %w", err)
	}
	if len(body) > maxRunbookBytes {
		return string(body[:maxRunbookBytes]) + "\n…(truncated)", nil
	}
	return string(body), nil
}
