package ingest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/itskrsna/Trackdown/internal/store"
)

// alwaysDenyLimiter simulates an exhausted rate-limit budget for every key.
type alwaysDenyLimiter struct{}

func (alwaysDenyLimiter) Allow(string) bool { return false }

func TestServeEnvelope_RateLimited_Returns429(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	(&Handler{Store: st, RateLimiter: alwaysDenyLimiter{}}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader([]byte("{}\n")))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("expected a Retry-After header")
	}
}

func TestServeEnvelope_NoLimiterSet_NotThrottled(t *testing.T) {
	// Confirms the nil-safe default: without a RateLimiter, ingest behaves
	// exactly as it did before rate limiting existed.
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	(&Handler{Store: st}).Register(mux, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	envelope := loadEnvelopeFixture(t, "sentry-go-exception.envelope")
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/proj1/envelope/", bytes.NewReader(envelope))
	req.Header.Set("X-Sentry-Auth", sentryAuthHeader("public"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no limiter set means unthrottled)", resp.StatusCode)
	}
}
