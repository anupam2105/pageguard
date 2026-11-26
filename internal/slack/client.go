// Package slack posts investigation cards via the Slack Web API and verifies
// interactive (button) callbacks.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAPIBase = "https://slack.com/api"

// Client posts messages with a bot token (chat:write scope).
type Client struct {
	token   string
	channel string
	apiBase string
	http    *http.Client
}

// Config configures a Client. APIBase is overridable for tests.
type Config struct {
	BotToken   string
	Channel    string
	APIBase    string
	HTTPClient *http.Client
}

// New builds a Client. BotToken and Channel are required.
func New(cfg Config) (*Client, error) {
	if cfg.BotToken == "" || cfg.Channel == "" {
		return nil, errors.New("slack: bot token and channel are required")
	}
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPIBase
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{token: cfg.BotToken, channel: cfg.Channel, apiBase: strings.TrimRight(cfg.APIBase, "/"), http: cfg.HTTPClient}, nil
}

// Message is a chat.postMessage request body.
type Message struct {
	Channel  string  `json:"channel"`
	Text     string  `json:"text"` // notification / accessibility fallback
	Blocks   []Block `json:"blocks,omitempty"`
	ThreadTS string  `json:"thread_ts,omitempty"`
}

// Posted identifies a posted message.
type Posted struct {
	Channel string
	TS      string
}

// Post sends msg to the configured channel unless msg.Channel is set.
func (c *Client) Post(ctx context.Context, msg *Message) (Posted, error) {
	if msg.Channel == "" {
		msg.Channel = c.channel
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return Posted{}, fmt.Errorf("marshal slack message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return Posted{}, fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return Posted{}, fmt.Errorf("slack chat.postMessage: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Posted{}, fmt.Errorf("read slack response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Posted{}, fmt.Errorf("slack chat.postMessage: HTTP %d", resp.StatusCode)
	}
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Posted{}, fmt.Errorf("decode slack response: %w", err)
	}
	if !out.OK {
		return Posted{}, fmt.Errorf("slack chat.postMessage: %s", out.Error)
	}
	return Posted{Channel: out.Channel, TS: out.TS}, nil
}
