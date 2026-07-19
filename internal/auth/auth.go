// Package auth protects Trackdown's management API and web UI with a
// single admin credential — HTTP Basic Auth, stdlib-only. There is
// deliberately no multi-user model, no sessions, no cookies: a self-hosted,
// single-operator tool doesn't need that machinery yet. Ingest
// (POST .../envelope/) is never wrapped by this — DSN keys are meant to be
// public-ish, matching real Sentry semantics, so SDKs keep working
// unauthenticated by design.
package auth

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
)

const (
	// EnvUser and EnvPassword are the environment variables credentials are
	// read from — env vars, not flags, because flags are visible in
	// process listings (ps/Task Manager) on shared machines, while env vars
	// set through a service manager's environment mechanism aren't.
	EnvUser     = "TRACKDOWN_ADMIN_USER"
	EnvPassword = "TRACKDOWN_ADMIN_PASSWORD"

	defaultUser = "admin"
)

// Config holds the single admin credential.
type Config struct {
	Username string
	Password string
}

// FromEnv reads credentials from the environment. Username defaults to
// "admin" if EnvUser is unset; Password is required — there is no
// insecure default, since shipping one would inevitably end up in a
// forgotten-about production deployment.
func FromEnv() (Config, error) {
	user := os.Getenv(EnvUser)
	if user == "" {
		user = defaultUser
	}
	password := os.Getenv(EnvPassword)
	if password == "" {
		return Config{}, fmt.Errorf("%s is required (set it to a strong admin password)", EnvPassword)
	}
	return Config{Username: user, Password: password}, nil
}

// FailedAttemptLimiter is satisfied by internal/ratelimit.Limiter. Declared
// here as a small interface (rather than importing internal/ratelimit
// directly) so auth has no compile-time dependency on it — Middleware works
// standalone, unthrottled, until a caller sets Limiter.
type FailedAttemptLimiter interface {
	Allow(key string) bool
}

// Middleware enforces HTTP Basic Auth against a fixed admin credential.
type Middleware struct {
	cfg Config
	// Limiter, if set, throttles repeated failed attempts per client IP —
	// a crude brute-force mitigation. Correct credentials are never
	// throttled, only failed ones.
	Limiter FailedAttemptLimiter
}

// New builds a Middleware from cfg.
func New(cfg Config) *Middleware {
	return &Middleware{cfg: cfg}
}

// Require wraps next, rejecting any request that doesn't present the
// correct Basic Auth credentials.
func (m *Middleware) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && m.credentialsMatch(user, pass) {
			next.ServeHTTP(w, r)
			return
		}

		if m.Limiter != nil && !m.Limiter.Allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
			return
		}

		w.Header().Set("WWW-Authenticate", `Basic realm="trackdown"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

// credentialsMatch compares in constant time so response timing can't leak
// how much of the credential was correct.
func (m *Middleware) credentialsMatch(user, pass string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(m.cfg.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(m.cfg.Password)) == 1
	return userOK && passOK
}

// clientIP extracts the host portion of RemoteAddr, falling back to the raw
// value if it isn't in host:port form.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
