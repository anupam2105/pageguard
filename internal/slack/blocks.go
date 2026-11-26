package slack

import (
	"fmt"
	"strings"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
)

// Action ids carried on the feedback buttons; the interactions handler
// dispatches on these.
const (
	ActionFeedbackUp   = "pageguard_feedback_up"
	ActionFeedbackDown = "pageguard_feedback_down"
)

// Slack rejects section text over 3000 chars; leave room for formatting.
const maxSectionText = 2900

// Block is a Slack Block Kit block. A map keeps the encoder flexible without
// mirroring Slack's entire schema in Go types.
type Block map[string]any

func mrkdwn(text string) map[string]any {
	return map[string]any{"type": "mrkdwn", "text": truncate(text, maxSectionText)}
}

func plain(text string) map[string]any {
	return map[string]any{"type": "plain_text", "text": truncate(text, 150), "emoji": true}
}

// Escape escapes the three characters Slack mrkdwn treats as control
// characters, so alert text cannot inject @channel pings or links.
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

var confidenceEmoji = map[domain.Confidence]string{
	domain.ConfidenceHigh:   ":large_green_circle:",
	domain.ConfidenceMedium: ":large_yellow_circle:",
	domain.ConfidenceLow:    ":white_circle:",
}

// InvestigationMessage renders a completed investigation as a Block Kit card
// with thumbs-up/down feedback buttons.
func InvestigationMessage(id string, inv *domain.Investigation) *Message {
	a := &inv.Alert
	title := fmt.Sprintf("%s on %s/%s", a.Name, a.Service, a.Environment)

	var ev strings.Builder
	for _, e := range inv.Evidence {
		fmt.Fprintf(&ev, "• *%s* `%s`\n   %s\n", Escape(e.Source), Escape(truncate(e.Query, 200)), Escape(e.Summary))
	}
	if ev.Len() == 0 {
		ev.WriteString("_none reported_")
	}

	blocks := []Block{
		{"type": "header", "text": plain(":mag: " + title)},
		{"type": "section", "fields": []any{
			mrkdwn(fmt.Sprintf("*Confidence*\n%s %s", confidenceEmoji[inv.Confidence], inv.Confidence)),
			mrkdwn(fmt.Sprintf("*Severity*\n%s", Escape(orUnknown(a.Severity)))),
			mrkdwn(fmt.Sprintf("*Alert started*\n<!date^%d^{date_short_pretty} {time}|%s>", a.StartedAt.Unix(), a.StartedAt.UTC().Format(time.RFC3339))),
			mrkdwn(fmt.Sprintf("*Investigated in*\n%s · %d tool calls", inv.Duration.Round(time.Second), inv.ToolCallsMade)),
		}},
		{"type": "section", "text": mrkdwn("*Hypothesis*\n" + Escape(inv.Hypothesis))},
		{"type": "section", "text": mrkdwn("*Evidence*\n" + ev.String())},
		{"type": "section", "text": mrkdwn("*Suggested action* (not executed — your call)\n```" + Escape(inv.SuggestedAction) + "```")},
		{"type": "actions", "block_id": "pageguard_feedback", "elements": []any{
			map[string]any{"type": "button", "action_id": ActionFeedbackUp, "value": id, "text": plain(":+1: Helpful")},
			map[string]any{"type": "button", "action_id": ActionFeedbackDown, "value": id, "text": plain(":-1: Not helpful")},
		}},
		{"type": "context", "elements": []any{
			mrkdwn(fmt.Sprintf("pageguard proposes, a human decides · model %s · id `%s`", Escape(inv.Model), id)),
		}},
	}
	return &Message{
		Text:   fmt.Sprintf("pageguard: %s — %s (confidence %s)", title, truncate(inv.Hypothesis, 200), inv.Confidence),
		Blocks: blocks,
	}
}

// FailureMessage tells the channel an investigation could not complete, so
// silence is never mistaken for "nothing found".
func FailureMessage(id string, a *domain.Alert, reason string) *Message {
	title := fmt.Sprintf("%s on %s/%s", a.Name, a.Service, a.Environment)
	return &Message{
		Text: "pageguard could not investigate " + title,
		Blocks: []Block{
			{"type": "section", "text": mrkdwn(fmt.Sprintf(":warning: *pageguard could not investigate %s*\n%s\nInvestigate manually.",
				Escape(title), Escape(truncate(reason, 500))))},
			{"type": "context", "elements": []any{mrkdwn(fmt.Sprintf("id `%s`", id))}},
		},
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
