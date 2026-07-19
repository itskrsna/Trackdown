package ratelimit

import (
	"testing"
	"time"
)

func TestLimiter_AllowsUpToBurst(t *testing.T) {
	l := New(1, 3)
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("call %d: expected allowed (within burst of 3)", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("4th call: expected denied (burst exhausted)")
	}
}

func TestLimiter_RefillsOverTime(t *testing.T) {
	l := New(1, 3) // 1 token/sec, burst 3
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return current }

	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("call %d: expected allowed", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("expected denied once burst is exhausted")
	}

	current = current.Add(2 * time.Second) // refills ~2 tokens at 1/sec
	if !l.Allow("k") {
		t.Fatal("expected allowed after refill")
	}
	if !l.Allow("k") {
		t.Fatal("expected a second allowed call (2 tokens were refilled)")
	}
	if l.Allow("k") {
		t.Fatal("expected a third call denied (only 2 tokens were refilled)")
	}
}

func TestLimiter_RefillNeverExceedsBurst(t *testing.T) {
	l := New(100, 3) // fast refill rate, small burst
	current := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return current }

	l.Allow("k")                          // consume 1 (2 left)
	current = current.Add(time.Hour)      // would refill far more than burst allows
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("call %d after long idle: expected allowed (bucket should cap at burst, not overflow)", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("expected denied — refill must cap at burst=3, not accumulate unbounded")
	}
}

func TestLimiter_DifferentKeysAreIndependent(t *testing.T) {
	l := New(1, 1)
	if !l.Allow("a") {
		t.Fatal("first call for key a should be allowed")
	}
	if l.Allow("a") {
		t.Fatal("second call for key a should be denied (burst=1)")
	}
	if !l.Allow("b") {
		t.Fatal("first call for a DIFFERENT key b should be allowed regardless of a's state")
	}
}

func TestLimiter_SweepRemovesOnlyStaleBuckets(t *testing.T) {
	l := New(1, 5)
	l.sweepAtSize = 1 // force sweeps to actually run without needing 1000 real entries
	l.staleAfter = time.Minute

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.buckets["stale"] = &bucket{tokens: 5, lastSeen: base}
	l.buckets["fresh"] = &bucket{tokens: 5, lastSeen: base.Add(59 * time.Second)}

	// "stale" is 61s old at this check time (> staleAfter=60s) and must be
	// swept; "fresh" is only 2s old (well within staleAfter) and must not.
	l.sweepLocked(base.Add(61 * time.Second))

	if _, ok := l.buckets["stale"]; ok {
		t.Fatal("stale bucket (untouched for > staleAfter) should have been swept")
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Fatal("fresh bucket (touched recently) should NOT have been swept")
	}
}
