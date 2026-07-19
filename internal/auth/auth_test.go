package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFromEnv_DefaultsUsername(t *testing.T) {
	t.Setenv(EnvUser, "")
	t.Setenv(EnvPassword, "hunter2")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Username != "admin" {
		t.Fatalf("Username = %q, want admin (default)", cfg.Username)
	}
	if cfg.Password != "hunter2" {
		t.Fatalf("Password = %q, want hunter2", cfg.Password)
	}
}

func TestFromEnv_CustomUsername(t *testing.T) {
	t.Setenv(EnvUser, "root")
	t.Setenv(EnvPassword, "hunter2")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Username != "root" {
		t.Fatalf("Username = %q, want root", cfg.Username)
	}
}

func TestFromEnv_MissingPassword_Errors(t *testing.T) {
	t.Setenv(EnvUser, "admin")
	t.Setenv(EnvPassword, "")

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected an error when TRACKDOWN_ADMIN_PASSWORD is unset")
	}
}

func newProtectedServer(t *testing.T, mw *Middleware) *httptest.Server {
	t.Helper()
	handler := mw.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected content"))
	}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestMiddleware_Require_CorrectCredentials_Passes(t *testing.T) {
	mw := New(Config{Username: "admin", Password: "secret"})
	srv := newProtectedServer(t, mw)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.SetBasicAuth("admin", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMiddleware_Require_WrongPassword_Returns401(t *testing.T) {
	mw := New(Config{Username: "admin", Password: "secret"})
	srv := newProtectedServer(t, mw)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestMiddleware_Require_MissingAuth_Returns401WithChallenge(t *testing.T) {
	mw := New(Config{Username: "admin", Password: "secret"})
	srv := newProtectedServer(t, mw)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Fatal("expected a WWW-Authenticate challenge header")
	}
}

// alwaysDenyLimiter simulates an exhausted rate-limit budget for every key.
type alwaysDenyLimiter struct{}

func (alwaysDenyLimiter) Allow(string) bool { return false }

func TestMiddleware_Require_RateLimitedFailedAttempts_Returns429(t *testing.T) {
	mw := New(Config{Username: "admin", Password: "secret"})
	mw.Limiter = alwaysDenyLimiter{}
	srv := newProtectedServer(t, mw)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
}

func TestMiddleware_Require_CorrectCredentials_NeverThrottled(t *testing.T) {
	// A limiter that denies everything must not block a legitimate admin who
	// presents correct credentials -- only failed attempts consult it.
	mw := New(Config{Username: "admin", Password: "secret"})
	mw.Limiter = alwaysDenyLimiter{}
	srv := newProtectedServer(t, mw)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.SetBasicAuth("admin", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (correct creds must bypass the limiter entirely)", resp.StatusCode)
	}
}
