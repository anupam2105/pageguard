package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// maxSignatureAge rejects replayed requests, per Slack's guidance.
const maxSignatureAge = 5 * time.Minute

// VerifySignature checks X-Slack-Signature over the raw request body.
// https://api.slack.com/authentication/verifying-requests-from-slack
func VerifySignature(signingSecret, timestamp, signature string, body []byte, now time.Time) error {
	if signingSecret == "" {
		return errors.New("signing secret not configured")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid X-Slack-Request-Timestamp")
	}
	if age := now.Sub(time.Unix(ts, 0)); age > maxSignatureAge || age < -maxSignatureAge {
		return errors.New("stale X-Slack-Request-Timestamp")
	}
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = fmt.Fprintf(mac, "v0:%s:", timestamp)
	_, _ = mac.Write(body)
	want := "v0=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// FeedbackAction is a parsed thumbs-up/down button click.
type FeedbackAction struct {
	InvestigationID string
	Up              bool
	UserID          string
	Channel         string
	MessageTS       string
}

type interactionPayload struct {
	Type string `json:"type"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Container struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// ParseFeedback extracts a feedback click from a form-encoded interaction
// body. ok is false for interactions pageguard doesn't handle.
func ParseFeedback(body []byte) (action FeedbackAction, ok bool, err error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return FeedbackAction{}, false, fmt.Errorf("parse interaction form: %w", err)
	}
	var p interactionPayload
	if err := json.Unmarshal([]byte(form.Get("payload")), &p); err != nil {
		return FeedbackAction{}, false, fmt.Errorf("decode interaction payload: %w", err)
	}
	if p.Type != "block_actions" {
		return FeedbackAction{}, false, nil
	}
	for _, a := range p.Actions {
		if a.ActionID != ActionFeedbackUp && a.ActionID != ActionFeedbackDown {
			continue
		}
		return FeedbackAction{
			InvestigationID: a.Value,
			Up:              a.ActionID == ActionFeedbackUp,
			UserID:          p.User.ID,
			Channel:         p.Container.ChannelID,
			MessageTS:       p.Container.MessageTS,
		}, true, nil
	}
	return FeedbackAction{}, false, nil
}
