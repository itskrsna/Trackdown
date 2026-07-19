// Package ratelimit implements a minimal per-key token bucket, hand-rolled
// rather than pulling in golang.org/x/time/rate — the algorithm is small
// enough (refill-on-access, no background goroutine needed) that a
// dependency isn't justified for it.
package ratelimit

import (
	"sync"
	"time"
)

// defaultStaleAfter and defaultSweepAtSize bound the buckets map's memory
// growth from many distinct keys (e.g. IPs) over a long-running process.
// Sweeping is opportunistic (checked on every new-key insert) rather than a
// background goroutine, and only does work once the map has grown large
// enough that bothering is worthwhile.
const (
	defaultStaleAfter  = 10 * time.Minute
	defaultSweepAtSize = 1000
)

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// Limiter is a per-key token bucket rate limiter. The zero value is not
// usable — construct with New.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	rate  float64 // tokens added per second
	burst float64 // max tokens a single bucket can hold

	now         func() time.Time // overridable in tests for deterministic refill timing
	staleAfter  time.Duration
	sweepAtSize int
}

// New creates a Limiter permitting `rate` requests per second per key, with
// bursts up to `burst` requests before throttling kicks in.
func New(rate float64, burst int) *Limiter {
	return &Limiter{
		buckets:     make(map[string]*bucket),
		rate:        rate,
		burst:       float64(burst),
		now:         time.Now,
		staleAfter:  defaultStaleAfter,
		sweepAtSize: defaultSweepAtSize,
	}
}

// Allow reports whether a request for key is permitted right now. If so, it
// consumes one token from that key's bucket.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		l.sweepLocked(now)
		// A brand new key starts with a full bucket minus the token this
		// call consumes.
		l.buckets[key] = &bucket{tokens: l.burst - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked removes buckets untouched for staleAfter, called with mu
// already held. Only runs once the map has grown past sweepAtSize, so it
// doesn't do wasted work on every single new key in the common case of few
// distinct keys.
func (l *Limiter) sweepLocked(now time.Time) {
	if len(l.buckets) < l.sweepAtSize {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.staleAfter {
			delete(l.buckets, k)
		}
	}
}
