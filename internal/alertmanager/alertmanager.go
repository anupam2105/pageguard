// Package alertmanager decodes Alertmanager webhook (version 4) payloads into
// domain.Alerts.
package alertmanager

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/anupam2105/pageguard/internal/domain"
)

// Payload is the subset of the Alertmanager webhook v4 body pageguard reads.
// https://prometheus.io/docs/alerting/latest/configuration/#webhook_config
type Payload struct {
	Version           string            `json:"version"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert is one alert inside a Payload.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Label keys consulted, in order, to find the affected service and
// environment. Teams label these differently; the first non-empty wins.
var (
	serviceLabels     = []string{"service", "app", "app_kubernetes_io_name", "job"}
	environmentLabels = []string{"environment", "env", "namespace", "cluster"}
)

// Skipped describes an alert that was not converted, and why.
type Skipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Decode reads a webhook body.
func Decode(r io.Reader) (*Payload, error) {
	var p Payload
	if err := json.NewDecoder(r).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode alertmanager payload: %w", err)
	}
	return &p, nil
}

// FiringAlerts converts every firing alert into a domain.Alert. Resolved
// alerts and alerts missing a service/environment are reported as skipped.
func (p *Payload) FiringAlerts() ([]domain.Alert, []Skipped) {
	var (
		out     []domain.Alert
		skipped []Skipped
	)
	for i := range p.Alerts {
		a := &p.Alerts[i]
		name := a.Labels["alertname"]
		if a.Status != "firing" {
			skipped = append(skipped, Skipped{Name: name, Reason: "status " + a.Status})
			continue
		}
		da := domain.Alert{
			Fingerprint: a.Fingerprint,
			Name:        name,
			Severity:    a.Labels["severity"],
			Service:     firstLabel(a.Labels, serviceLabels),
			Environment: firstLabel(a.Labels, environmentLabels),
			Summary:     a.Annotations["summary"],
			Description: a.Annotations["description"],
			StartedAt:   a.StartsAt,
			Labels:      a.Labels,
			Annotations: a.Annotations,
		}
		if err := da.Validate(); err != nil {
			skipped = append(skipped, Skipped{Name: name, Reason: err.Error()})
			continue
		}
		out = append(out, da)
	}
	return out, skipped
}

func firstLabel(labels map[string]string, keys []string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}
