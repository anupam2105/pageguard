// Package store persists investigation history and human feedback in an
// embedded SQLite database. SQLite is a deliberate choice (see ADR-0001):
// pageguard should self-host as a single binary with no external database.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver keeps CGO_ENABLED=0 builds working

	"github.com/anupam2105/pageguard/internal/domain"
)

// ErrNotFound is returned when an investigation id does not exist.
var ErrNotFound = errors.New("investigation not found")

// Status is the terminal state of one investigation attempt.
type Status string

// Recognised statuses.
const (
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Verdict is a human's judgement of a hypothesis.
type Verdict string

// Recognised verdicts.
const (
	VerdictUp   Verdict = "up"
	VerdictDown Verdict = "down"
)

// Valid reports whether v is a recognised verdict.
func (v Verdict) Valid() bool { return v == VerdictUp || v == VerdictDown }

// Record is one persisted investigation attempt.
type Record struct {
	ID              string
	Alert           domain.Alert
	Status          Status
	Hypothesis      string
	Confidence      domain.Confidence
	Evidence        []domain.Evidence
	SuggestedAction string
	Error           string
	Model           string
	ToolCalls       int
	StartedAt       time.Time
	FinishedAt      time.Time
	SlackChannel    string
	SlackTS         string
	FeedbackUp      int
	FeedbackDown    int
}

// Feedback is one human verdict on an investigation. A user re-voting
// replaces their earlier verdict.
type Feedback struct {
	InvestigationID string
	Verdict         Verdict
	User            string
	Comment         string
	Source          string // "slack" | "api"
}

// Store is the SQLite-backed investigation history.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies
// migrations. Use ":memory:" for tests.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// A single connection serialises writers (SQLite allows one at a time)
	// and keeps ":memory:" databases from fragmenting across connections.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the database is usable.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{
	`CREATE TABLE investigations (
		id               TEXT PRIMARY KEY,
		fingerprint      TEXT NOT NULL DEFAULT '',
		alert_name       TEXT NOT NULL,
		severity         TEXT NOT NULL DEFAULT '',
		service          TEXT NOT NULL,
		environment      TEXT NOT NULL,
		alert_started_at TEXT NOT NULL,
		alert_json       TEXT NOT NULL,
		status           TEXT NOT NULL CHECK (status IN ('succeeded','failed')),
		hypothesis       TEXT NOT NULL DEFAULT '',
		confidence       TEXT NOT NULL DEFAULT '',
		evidence_json    TEXT NOT NULL DEFAULT '[]',
		suggested_action TEXT NOT NULL DEFAULT '',
		error            TEXT NOT NULL DEFAULT '',
		model            TEXT NOT NULL DEFAULT '',
		tool_calls       INTEGER NOT NULL DEFAULT 0,
		started_at       TEXT NOT NULL,
		finished_at      TEXT NOT NULL,
		slack_channel    TEXT NOT NULL DEFAULT '',
		slack_ts         TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX investigations_fingerprint_idx ON investigations (fingerprint, finished_at);
	CREATE INDEX investigations_finished_idx ON investigations (finished_at DESC);
	CREATE TABLE feedback (
		investigation_id TEXT NOT NULL REFERENCES investigations(id) ON DELETE CASCADE,
		user             TEXT NOT NULL,
		verdict          TEXT NOT NULL CHECK (verdict IN ('up','down')),
		comment          TEXT NOT NULL DEFAULT '',
		source           TEXT NOT NULL,
		created_at       TEXT NOT NULL,
		PRIMARY KEY (investigation_id, user)
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", i+1, err)
		}
		// PRAGMA does not accept bound parameters; i is an int we control.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("bump schema version: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", i+1, err)
		}
	}
	return nil
}

// NewID returns a random 16-hex-char investigation id.
func NewID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

const timeFormat = time.RFC3339Nano

func fmtTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) time.Time {
	t, _ := time.Parse(timeFormat, s)
	return t
}

// Insert persists a new record. r.ID must be set.
func (s *Store) Insert(ctx context.Context, r *Record) error {
	if r.ID == "" {
		return errors.New("record id is required")
	}
	alertJSON, err := json.Marshal(alertDoc(&r.Alert))
	if err != nil {
		return fmt.Errorf("marshal alert: %w", err)
	}
	evidence := r.Evidence
	if evidence == nil {
		evidence = []domain.Evidence{}
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("marshal evidence: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO investigations (
		id, fingerprint, alert_name, severity, service, environment, alert_started_at, alert_json,
		status, hypothesis, confidence, evidence_json, suggested_action, error, model, tool_calls,
		started_at, finished_at, slack_channel, slack_ts
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.Alert.Fingerprint, r.Alert.Name, r.Alert.Severity, r.Alert.Service, r.Alert.Environment,
		fmtTime(r.Alert.StartedAt), string(alertJSON),
		string(r.Status), r.Hypothesis, string(r.Confidence), string(evidenceJSON), r.SuggestedAction,
		r.Error, r.Model, r.ToolCalls, fmtTime(r.StartedAt), fmtTime(r.FinishedAt), r.SlackChannel, r.SlackTS,
	)
	if err != nil {
		return fmt.Errorf("insert investigation: %w", err)
	}
	return nil
}

// SetSlackRef records where the investigation card was posted.
func (s *Store) SetSlackRef(ctx context.Context, id, channel, ts string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE investigations SET slack_channel = ?, slack_ts = ? WHERE id = ?`, channel, ts, id)
	if err != nil {
		return fmt.Errorf("update slack ref: %w", err)
	}
	return requireOneRow(res)
}

// RecentlyInvestigated reports whether a successful investigation for the
// fingerprint finished at or after since. Failed attempts don't count, so a
// transient LLM outage doesn't suppress the retry on the next notification.
func (s *Store) RecentlyInvestigated(ctx context.Context, fingerprint string, since time.Time) (bool, error) {
	if fingerprint == "" {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM investigations
		WHERE fingerprint = ? AND status = 'succeeded' AND finished_at >= ?`, fingerprint, fmtTime(since)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("query recent investigations: %w", err)
	}
	return n > 0, nil
}

const selectRecord = `SELECT i.id, i.alert_json, i.status, i.hypothesis, i.confidence, i.evidence_json,
	i.suggested_action, i.error, i.model, i.tool_calls, i.started_at, i.finished_at, i.slack_channel, i.slack_ts,
	(SELECT COUNT(*) FROM feedback f WHERE f.investigation_id = i.id AND f.verdict = 'up'),
	(SELECT COUNT(*) FROM feedback f WHERE f.investigation_id = i.id AND f.verdict = 'down')
	FROM investigations i`

// Get returns one record by id.
func (s *Store) Get(ctx context.Context, id string) (*Record, error) {
	row := s.db.QueryRowContext(ctx, selectRecord+` WHERE i.id = ?`, id)
	r, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListFilter narrows List results. Zero values mean no filter.
type ListFilter struct {
	Service     string
	Environment string
	Limit       int
}

// List returns the most recent records first.
func (s *Store) List(ctx context.Context, f ListFilter) ([]*Record, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var (
		where []string
		args  []any
	)
	if f.Service != "" {
		where = append(where, "i.service = ?")
		args = append(args, f.Service)
	}
	if f.Environment != "" {
		where = append(where, "i.environment = ?")
		args = append(args, f.Environment)
	}
	q := selectRecord
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY i.finished_at DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate investigations: %w", err)
	}
	return out, nil
}

// RecordFeedback upserts one user's verdict. Returns ErrNotFound if the
// investigation does not exist.
func (s *Store) RecordFeedback(ctx context.Context, fb *Feedback) error {
	if !fb.Verdict.Valid() {
		return fmt.Errorf("invalid verdict %q", fb.Verdict)
	}
	if fb.User == "" {
		return errors.New("feedback user is required")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM investigations WHERE id = ?`, fb.InvestigationID).Scan(&exists); err != nil {
		return fmt.Errorf("check investigation: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO feedback (investigation_id, user, verdict, comment, source, created_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT (investigation_id, user) DO UPDATE SET
			verdict = excluded.verdict, comment = excluded.comment, source = excluded.source, created_at = excluded.created_at`,
		fb.InvestigationID, fb.User, string(fb.Verdict), fb.Comment, fb.Source, fmtTime(time.Now()))
	if err != nil {
		return fmt.Errorf("record feedback: %w", err)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanRecord(sc scanner) (*Record, error) {
	var (
		r                                     Record
		alertJSON, evidenceJSON               string
		status, confidence, started, finished string
	)
	if err := sc.Scan(&r.ID, &alertJSON, &status, &r.Hypothesis, &confidence, &evidenceJSON,
		&r.SuggestedAction, &r.Error, &r.Model, &r.ToolCalls, &started, &finished, &r.SlackChannel, &r.SlackTS,
		&r.FeedbackUp, &r.FeedbackDown); err != nil {
		return nil, fmt.Errorf("scan investigation: %w", err)
	}
	var doc alertDocument
	if err := json.Unmarshal([]byte(alertJSON), &doc); err != nil {
		return nil, fmt.Errorf("decode alert: %w", err)
	}
	r.Alert = doc.toAlert()
	if err := json.Unmarshal([]byte(evidenceJSON), &r.Evidence); err != nil {
		return nil, fmt.Errorf("decode evidence: %w", err)
	}
	r.Status = Status(status)
	r.Confidence = domain.Confidence(confidence)
	r.StartedAt = parseTime(started)
	r.FinishedAt = parseTime(finished)
	return &r, nil
}

func requireOneRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// alertDocument is the JSON shape of a stored alert; domain.Alert has no
// JSON tags because it is not a wire type.
type alertDocument struct {
	Fingerprint string            `json:"fingerprint,omitempty"`
	Name        string            `json:"name"`
	Severity    string            `json:"severity,omitempty"`
	Service     string            `json:"service"`
	Environment string            `json:"environment"`
	Summary     string            `json:"summary,omitempty"`
	Description string            `json:"description,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

func alertDoc(a *domain.Alert) alertDocument {
	return alertDocument{
		Fingerprint: a.Fingerprint, Name: a.Name, Severity: a.Severity, Service: a.Service,
		Environment: a.Environment, Summary: a.Summary, Description: a.Description,
		StartedAt: a.StartedAt, Labels: a.Labels, Annotations: a.Annotations,
	}
}

func (d *alertDocument) toAlert() domain.Alert {
	return domain.Alert{
		Fingerprint: d.Fingerprint, Name: d.Name, Severity: d.Severity, Service: d.Service,
		Environment: d.Environment, Summary: d.Summary, Description: d.Description,
		StartedAt: d.StartedAt, Labels: d.Labels, Annotations: d.Annotations,
	}
}
