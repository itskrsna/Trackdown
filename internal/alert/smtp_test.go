package alert

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serveFakeSMTP is a minimal SMTP responder (EHLO/MAIL/RCPT/DATA/QUIT) that
// does NOT advertise STARTTLS, so smtp.SendMail proceeds in plaintext --
// exactly what's needed to test SMTPNotifier's non-implicit-TLS path
// end-to-end over a real TCP connection, the same technique Go's own
// net/smtp tests use.
func serveFakeSMTP(t *testing.T, conn net.Conn) string {
	t.Helper()
	defer conn.Close()
	r := bufio.NewReader(conn)

	write := func(s string) {
		if _, err := conn.Write([]byte(s + "\r\n")); err != nil {
			t.Logf("fake SMTP server: write error: %v", err)
		}
	}
	write("220 localhost ESMTP fake")

	var data strings.Builder
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return data.String()
		}
		line = strings.TrimRight(line, "\r\n")

		if inData {
			if line == "." {
				inData = false
				write("250 OK: message accepted")
				continue
			}
			data.WriteString(line)
			data.WriteString("\n")
			continue
		}

		switch upper := strings.ToUpper(line); {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			write("250-localhost greets you")
			write("250 OK")
		case strings.HasPrefix(upper, "MAIL FROM"):
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO"):
			write("250 OK")
		case upper == "DATA":
			write("354 Start mail input; end with <CRLF>.<CRLF>")
			inData = true
		case upper == "QUIT":
			write("221 Bye")
			return data.String()
		default:
			write("500 unrecognized command")
		}
	}
}

func TestSMTPNotifier_Notify_SendsExpectedMessage(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			received <- ""
			return
		}
		received <- serveFakeSMTP(t, conn)
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	n := &SMTPNotifier{
		Host: host,
		Port: port,
		From: "trackdown@example.com",
		To:   []string{"ops@example.com"},
	}
	ev := NotifyEvent{
		ProjectID:  "proj1",
		IssueID:    7,
		Title:      "boom goes the dynamite",
		Level:      "error",
		EventCount: 3,
		OccurredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var body string
	select {
	case body = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server to receive a message")
	}

	if !strings.Contains(body, "boom goes the dynamite") {
		t.Fatalf("expected the issue title in the message body, got: %s", body)
	}
	if !strings.Contains(body, "From: trackdown@example.com") {
		t.Fatalf("expected a From header, got: %s", body)
	}
	if !strings.Contains(body, "To: ops@example.com") {
		t.Fatalf("expected a To header, got: %s", body)
	}
	if !strings.Contains(body, "[Trackdown] New issue:") {
		t.Fatalf("expected a 'New issue' subject line (IsRegression was false), got: %s", body)
	}
}

func TestSMTPNotifier_Notify_RegressionSubjectLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	received := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			received <- ""
			return
		}
		received <- serveFakeSMTP(t, conn)
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	n := &SMTPNotifier{Host: host, Port: port, From: "trackdown@example.com", To: []string{"ops@example.com"}}
	if err := n.Notify(context.Background(), NotifyEvent{Title: "it's back", IsRegression: true}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	var body string
	select {
	case body = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fake SMTP server")
	}
	if !strings.Contains(body, "[Trackdown] Regression:") {
		t.Fatalf("expected a 'Regression' subject line, got: %s", body)
	}
}

// TestSMTPNotifier_ImplicitTLS_AttemptsTLSHandshake proves the ImplicitTLS
// code path is actually reachable and distinct from the plain path: dialing
// a non-TLS listener with ImplicitTLS=true must fail with a TLS handshake
// error, not silently fall back to plaintext.
func TestSMTPNotifier_ImplicitTLS_AttemptsTLSHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// A plain (non-TLS) server won't understand a TLS ClientHello --
		// just read and discard until the client gives up.
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	n := &SMTPNotifier{Host: host, Port: port, From: "a@example.com", To: []string{"b@example.com"}, ImplicitTLS: true}
	err = n.Notify(context.Background(), NotifyEvent{Title: "x"})
	if err == nil {
		t.Fatal("expected an error: a plain TCP server cannot complete a TLS handshake")
	}
	if !strings.Contains(err.Error(), "TLS") && !strings.Contains(err.Error(), "tls") {
		t.Fatalf("expected a TLS-related error, got: %v", err)
	}
}
