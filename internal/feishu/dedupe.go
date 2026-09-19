package feishu

import (
	"sync"
	"time"
)

// Deduplicator tracks processed event and message IDs to prevent duplicate processing.
type Deduplicator struct {
	mu      sync.Mutex
	seen    map[string]int64 // id -> expiresAt (Unix seconds)
	ttl     time.Duration
	maxSize int
}

// NewDeduplicator initializes a Deduplicator with the given retention TTL and max capacity.
func NewDeduplicator(ttl time.Duration, maxSize int) *Deduplicator {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 10000
	}
	return &Deduplicator{
		seen:    make(map[string]int64),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

// CheckAndRecord checks if an ID has been seen within its TTL.
// Returns true if the ID is a duplicate; otherwise records it and returns false.
func (d *Deduplicator) CheckAndRecord(id string) bool {
	if id == "" {
		return false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UnixMilli()

	// Check if already present and still valid
	if exp, exists := d.seen[id]; exists {
		if now < exp {
			return true
		}
		// Expired
		delete(d.seen, id)
	}

	// Evict expired entries if approaching capacity
	if len(d.seen) >= d.maxSize {
		d.cleanupExpiredLocked(now)
		// If still at capacity, drop arbitrary half of the cache
		if len(d.seen) >= d.maxSize {
			count := 0
			limit := d.maxSize / 2
			for k := range d.seen {
				delete(d.seen, k)
				count++
				if count >= limit {
					break
				}
			}
		}
	}

	ttlMs := d.ttl.Milliseconds()
	if ttlMs <= 0 {
		ttlMs = 1
	}
	d.seen[id] = now + ttlMs
	return false
}

func (d *Deduplicator) cleanupExpiredLocked(now int64) {
	for k, exp := range d.seen {
		if now >= exp {
			delete(d.seen, k)
		}
	}
}

// Clear removes all tracked IDs.
func (d *Deduplicator) Clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seen = make(map[string]int64)
}

// Size returns the count of currently tracked IDs.
func (d *Deduplicator) Size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.seen)
}
