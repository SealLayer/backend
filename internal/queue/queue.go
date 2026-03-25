package queue

import (
	"fmt"
	"sync"
	"time"
)

type SealJob struct {
	BatchID     string
	ContentHash string
	RemoteIP    string
	EnqueuedAt  time.Time

	ResultCh chan SealJobResult
}

type SealJobResult struct {
	BatchID    string
	LedgerPath string
	CommitSHA  string
	Receipt    any
	Err        error
}

type Queue struct {
	ch chan *SealJob
}

func New(capacity int) *Queue {
	return &Queue{ch: make(chan *SealJob, capacity)}
}

func (q *Queue) TryEnqueue(job *SealJob) bool {
	select {
	case q.ch <- job:
		return true
	default:
		return false
	}
}

func (q *Queue) Drain(max int) []*SealJob {
	out := make([]*SealJob, 0, max)
	for len(out) < max {
		select {
		case j := <-q.ch:
			out = append(out, j)
		default:
			return out
		}
	}
	return out
}

func BatchIDForTime(t time.Time, interval time.Duration) string {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	sec := int64(interval.Seconds())
	if sec <= 0 {
		sec = 10
	}
	u := t.Unix()
	floored := (u / sec) * sec
	ft := time.Unix(floored, 0).UTC()
	return ft.Format("20060102-150405")
}

func RowID(batchID string, index int) string {
	return fmt.Sprintf("%s-%d", batchID, index)
}

type BatchStatus string

const (
	StatusQueued   BatchStatus = "queued"
	StatusBatching BatchStatus = "batching"
	StatusPushed   BatchStatus = "pushed"
	StatusFailed   BatchStatus = "failed"
)

type BatchState struct {
	BatchID    string
	Status     BatchStatus
	LedgerPath string
	CommitSHA  string
	Error      string
	UpdatedAt  time.Time
}

type BatchStore struct {
	mu sync.RWMutex
	m  map[string]BatchState
}

func NewBatchStore() *BatchStore {
	return &BatchStore{m: make(map[string]BatchState)}
}

func (s *BatchStore) Put(st BatchState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[st.BatchID] = st
}

func (s *BatchStore) Get(batchID string) (BatchState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.m[batchID]
	return st, ok
}
