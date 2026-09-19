package hub

import (
	"sync"
	"time"
)

// rateLimiter implements a per-key token-bucket rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	qpm     int
	burst   int
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
}

func newRateLimiter(qpm, burst int) *rateLimiter {
	if qpm <= 0 {
		qpm = 30
	}
	if burst <= 0 {
		burst = 10
	}
	return &rateLimiter{
		qpm:     qpm,
		burst:   burst,
		buckets: make(map[string]*tokenBucket),
	}
}

// Allow reports whether a single request from the given key is permitted under the rate limit.
func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, exists := rl.buckets[key]
	if !exists {
		rl.buckets[key] = &tokenBucket{
			tokens:     float64(rl.burst) - 1.0,
			lastRefill: now,
		}
		return true
	}

	elapsed := now.Sub(b.lastRefill).Seconds()
	fillRate := float64(rl.qpm) / 60.0
	b.tokens += elapsed * fillRate
	if b.tokens > float64(rl.burst) {
		b.tokens = float64(rl.burst)
	}
	b.lastRefill = now

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}
	return false
}
