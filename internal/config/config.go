// Package config loads a narrow JSON configuration file scoped ONLY to
// alerting settings (SMTP + webhooks). Everything else about Trackdown is
// configured via flags/env vars — those already cover every other setting
// adequately, and a general server-config file would be a new dependency
// (a YAML/TOML parser) that buys nothing over what's already expressible.
// Alerting is the one place with enough related knobs (host/port/user/pass/
// from/to, one or more webhook URLs) that flags stop being readable, which
// is the entire justification for this package existing. Uses only the
// standard library's encoding/json — no new dependency.
package config

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/itskrsna/Trackdown/internal/alert"
)

// SMTPConfig configures a single SMTP notifier.
type SMTPConfig struct {
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username,omitempty"`
	Password    string   `json:"password,omitempty"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	ImplicitTLS bool     `json:"implicit_tls,omitempty"`
}

// WebhookConfig configures a single webhook notifier.
type WebhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

// File is the top-level shape of the alerting config file. Both fields are
// optional and independent — set either, both, or neither (an empty/absent
// config file simply means no alerting).
type File struct {
	SMTP     *SMTPConfig     `json:"smtp,omitempty"`
	Webhooks []WebhookConfig `json:"webhooks,omitempty"`
}

// Load reads and parses the config file at path, validating that any
// section present has its required fields set — failing fast at startup on
// an obvious misconfiguration (e.g. an SMTP block with no host) beats
// silently failing every single alert delivery later.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}
	if err := f.validate(); err != nil {
		return nil, fmt.Errorf("invalid config file %q: %w", path, err)
	}
	return &f, nil
}

func (f *File) validate() error {
	if f.SMTP != nil {
		s := f.SMTP
		switch {
		case s.Host == "":
			return fmt.Errorf("smtp.host is required")
		case s.Port == 0:
			return fmt.Errorf("smtp.port is required")
		case s.From == "":
			return fmt.Errorf("smtp.from is required")
		case len(s.To) == 0:
			return fmt.Errorf("smtp.to must have at least one recipient")
		}
	}
	for i, wh := range f.Webhooks {
		if wh.URL == "" {
			return fmt.Errorf("webhooks[%d].url is required", i)
		}
	}
	return nil
}

// BuildNotifier turns the parsed config into a live alert.Notifier, fanning
// out to every configured SMTP/webhook target via alert.MultiNotifier.
// Returns nil if nothing is configured — callers should treat a nil
// Notifier as "alerting disabled," not an error.
func (f *File) BuildNotifier() alert.Notifier {
	var notifiers alert.MultiNotifier
	if f.SMTP != nil {
		notifiers = append(notifiers, &alert.SMTPNotifier{
			Host:        f.SMTP.Host,
			Port:        f.SMTP.Port,
			Username:    f.SMTP.Username,
			Password:    f.SMTP.Password,
			From:        f.SMTP.From,
			To:          f.SMTP.To,
			ImplicitTLS: f.SMTP.ImplicitTLS,
		})
	}
	for i := range f.Webhooks {
		wh := f.Webhooks[i]
		notifiers = append(notifiers, &alert.WebhookNotifier{URL: wh.URL, Secret: wh.Secret})
	}
	if len(notifiers) == 0 {
		return nil
	}
	return notifiers
}
