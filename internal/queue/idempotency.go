package queue

import (
	"sync"
	"time"
)

type IdempotencyEntry struct {
	Key         string
	ContentHash string
	BatchID     string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

type IdempotencyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]IdempotencyEntry
}

func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &IdempotencyStore{
		ttl:     ttl,
		entries: make(map[string]IdempotencyEntry),
	}
}

func (s *IdempotencyStore) Get(key string, now time.Time) (IdempotencyEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	ent, ok := s.entries[key]
	return ent, ok
}

func (s *IdempotencyStore) Put(key, contentHash, batchID string, now time.Time) IdempotencyEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	ent := IdempotencyEntry{
		Key:         key,
		ContentHash: contentHash,
		BatchID:     batchID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.ttl),
	}
	s.entries[key] = ent
	return ent
}

func (s *IdempotencyStore) purgeLocked(now time.Time) {
	for k, v := range s.entries {
		if now.After(v.ExpiresAt) {
			delete(s.entries, k)
		}
	}
}
