package alert

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// SMTPNotifier sends a plain-text email per notification via stdlib
// net/smtp — no external mail library needed.
//
// It supports both the common case (STARTTLS submission, typically port
// 587 — smtp.SendMail negotiates STARTTLS automatically if the server
// advertises it) and implicit TLS (port 465, where the TLS handshake
// happens before any SMTP command — smtp.SendMail cannot do this, so
// ImplicitTLS routes through a hand-built client via crypto/tls +
// smtp.NewClient instead).
type SMTPNotifier struct {
	Host        string
	Port        int
	Username    string
	Password    string
	From        string
	To          []string
	ImplicitTLS bool
}

func (s *SMTPNotifier) Notify(_ context.Context, ev NotifyEvent) error {
	// net/smtp has no context-aware API; ctx is accepted only to satisfy
	// the Notifier interface uniformly with WebhookNotifier.
	subject, body := formatEmail(ev)
	msg := buildMessage(s.From, s.To, subject, body)

	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	var auth smtp.Auth
	if s.Username != "" {
		auth = smtp.PlainAuth("", s.Username, s.Password, s.Host)
	}

	if s.ImplicitTLS {
		return s.sendImplicitTLS(addr, auth, msg)
	}
	return smtp.SendMail(addr, auth, s.From, s.To, msg)
}

func (s *SMTPNotifier) sendImplicitTLS(addr string, auth smtp.Auth, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: s.Host})
	if err != nil {
		return fmt.Errorf("dialing SMTP over implicit TLS: %w", err)
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("creating SMTP client: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := client.Mail(s.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, to := range s.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO %q: %w", to, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("closing message: %w", err)
	}
	return client.Quit()
}

func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func formatEmail(ev NotifyEvent) (subject, body string) {
	kind := "New issue"
	if ev.IsRegression {
		kind = "Regression"
	}
	subject = fmt.Sprintf("[Trackdown] %s: %s", kind, ev.Title)
	body = fmt.Sprintf(
		"Project: %s\nIssue: %s\nLevel: %s\nEvent count: %d\nOccurred at: %s\n\nView at: /projects/%s/issues/%d\n",
		ev.ProjectID, ev.Title, ev.Level, ev.EventCount, ev.OccurredAt.Format(time.RFC3339), ev.ProjectID, ev.IssueID)
	return subject, body
}
