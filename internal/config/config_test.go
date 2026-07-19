package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/itskrsna/Trackdown/internal/alert"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trackdown.json")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLoad_ValidSMTPAndWebhook(t *testing.T) {
	path := writeConfig(t, `{
		"smtp": {"host": "smtp.example.com", "port": 587, "username": "u", "password": "p", "from": "trackdown@example.com", "to": ["ops@example.com"]},
		"webhooks": [{"url": "https://example.com/hook", "secret": "shh"}]
	}`)

	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.SMTP == nil {
		t.Fatal("expected SMTP config to be non-nil")
	}
	if f.SMTP.Host != "smtp.example.com" || f.SMTP.Port != 587 {
		t.Fatalf("SMTP = %+v", f.SMTP)
	}
	if len(f.Webhooks) != 1 || f.Webhooks[0].URL != "https://example.com/hook" {
		t.Fatalf("Webhooks = %+v", f.Webhooks)
	}
}

func TestLoad_EmptyConfig_ValidNoAlerting(t *testing.T) {
	path := writeConfig(t, `{}`)
	f, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.SMTP != nil || len(f.Webhooks) != 0 {
		t.Fatalf("expected nothing configured, got %+v", f)
	}
	if n := f.BuildNotifier(); n != nil {
		t.Fatalf("BuildNotifier on empty config = %v, want nil", n)
	}
}

func TestLoad_MissingFile_Errors(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestLoad_InvalidJSON_Errors(t *testing.T) {
	path := writeConfig(t, `{not valid json`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestLoad_SMTPMissingRequiredFields_Errors(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{"missing host", `{"smtp": {"port": 587, "from": "a@b.com", "to": ["c@d.com"]}}`},
		{"missing port", `{"smtp": {"host": "smtp.example.com", "from": "a@b.com", "to": ["c@d.com"]}}`},
		{"missing from", `{"smtp": {"host": "smtp.example.com", "port": 587, "to": ["c@d.com"]}}`},
		{"missing recipients", `{"smtp": {"host": "smtp.example.com", "port": 587, "from": "a@b.com"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.json)
			if _, err := Load(path); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLoad_WebhookMissingURL_Errors(t *testing.T) {
	path := writeConfig(t, `{"webhooks": [{"secret": "shh"}]}`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected a validation error for a webhook with no URL")
	}
}

func TestBuildNotifier_ComposesMultiNotifier(t *testing.T) {
	f := &File{
		SMTP:     &SMTPConfig{Host: "h", Port: 587, From: "a@b.com", To: []string{"c@d.com"}},
		Webhooks: []WebhookConfig{{URL: "https://example.com/hook"}},
	}
	n := f.BuildNotifier()
	if n == nil {
		t.Fatal("expected a non-nil Notifier")
	}
	multi, ok := n.(alert.MultiNotifier)
	if !ok {
		t.Fatalf("expected alert.MultiNotifier, got %T", n)
	}
	if len(multi) != 2 {
		t.Fatalf("len(multi) = %d, want 2 (one SMTP + one webhook)", len(multi))
	}
}

func TestBuildNotifier_SMTPOnly(t *testing.T) {
	f := &File{SMTP: &SMTPConfig{Host: "h", Port: 587, From: "a@b.com", To: []string{"c@d.com"}}}
	multi, ok := f.BuildNotifier().(alert.MultiNotifier)
	if !ok || len(multi) != 1 {
		t.Fatalf("expected a 1-element MultiNotifier, got %v", f.BuildNotifier())
	}
}
