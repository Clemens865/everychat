package api

import (
	"sync"
	"time"
)

// rateLimiter is a tiny in-memory sliding-window limiter, keyed by an
// arbitrary string (the visitor IP). Phase 3 ships this; Phase 5 adds a
// Redis-backed implementation when multi-instance deploys arrive.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string][]time.Time
}

// newRateLimiter returns a limiter that allows `limit` requests per
// `windowSeconds` per key.
func newRateLimiter(limit, windowSeconds int) *rateLimiter {
	return &rateLimiter{
		limit:  limit,
		window: time.Duration(windowSeconds) * time.Second,
		hits:   make(map[string][]time.Time),
	}
}

// allow records a hit for `key` and returns false if the key is over the
// limit. Old entries outside the window are pruned on each call.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-r.window)

	hits := r.hits[key]
	keep := hits[:0]
	for _, t := range hits {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= r.limit {
		r.hits[key] = keep
		return false
	}
	r.hits[key] = append(keep, now)
	return true
}
