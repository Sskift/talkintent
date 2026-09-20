package feishu

import (
	"sync"
	"time"
)

// fragmentEntry tracks in-flight fragments for a message.
type fragmentEntry struct {
	parts     [][]byte
	received  int
	sum       int
	expiresAt time.Time
}

// FragmentReassembler reassembles multi-part data frames keyed by message_id
// with a 5-second TTL sliding window.
type FragmentReassembler struct {
	mu      sync.Mutex
	entries map[string]*fragmentEntry
	ttl     time.Duration
}

// NewFragmentReassembler creates a new reassembler with the given TTL (default 5s).
func NewFragmentReassembler(ttl time.Duration) *FragmentReassembler {
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	return &FragmentReassembler{
		entries: make(map[string]*fragmentEntry),
		ttl:     ttl,
	}
}

// AddFragment inserts a fragment for msgID.
// If all fragments (0..sum-1) have been received, it returns the concatenated payload.
// Otherwise, it returns nil.
func (r *FragmentReassembler) AddFragment(msgID string, sum, seq int, payload []byte) []byte {
	if sum <= 1 {
		return payload
	}
	if sum > 1024 {
		return nil
	}
	if seq < 0 || seq >= sum {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	// Sweep expired entries occasionally
	r.sweepLocked(now)

	entry, ok := r.entries[msgID]
	if !ok {
		entry = &fragmentEntry{
			parts:     make([][]byte, sum),
			sum:       sum,
			expiresAt: now.Add(r.ttl),
		}
		r.entries[msgID] = entry
	} else {
		if entry.sum != sum || seq >= len(entry.parts) {
			return nil
		}
		// Extend sliding TTL
		entry.expiresAt = now.Add(r.ttl)
	}

	if entry.parts[seq] == nil {
		// Copy payload slice to avoid retention of reader buffers
		partCopy := make([]byte, len(payload))
		copy(partCopy, payload)
		entry.parts[seq] = partCopy
		entry.received++
	}

	if entry.received == entry.sum {
		delete(r.entries, msgID)
		totalLen := 0
		for _, p := range entry.parts {
			totalLen += len(p)
		}
		combined := make([]byte, 0, totalLen)
		for _, p := range entry.parts {
			combined = append(combined, p...)
		}
		return combined
	}

	return nil
}

func (r *FragmentReassembler) sweepLocked(now time.Time) {
	for id, entry := range r.entries {
		if now.After(entry.expiresAt) {
			delete(r.entries, id)
		}
	}
}
