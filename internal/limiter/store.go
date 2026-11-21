package limiter

import (
	"sync"
	"time"
)

// numShards is the number of independent lock domains. Keys are hashed onto
// shards so that requests from different clients rarely contend on one mutex.
const numShards = 64

type entry[T any] struct {
	val  T
	seen time.Time
}

type shard[T any] struct {
	mu sync.Mutex
	m  map[string]*entry[T]
}

// store is a sharded map of per-key limiter state.
type store[T any] struct {
	shards [numShards]shard[T]
	now    func() time.Time
}

func newStore[T any](now func() time.Time) *store[T] {
	s := &store[T]{now: now}
	for i := range s.shards {
		s.shards[i].m = make(map[string]*entry[T])
	}
	return s
}

// hash is FNV-1a, inlined so that it does not allocate.
func hash(key string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return h
}

// update runs fn on the state for key while holding only that key's shard lock.
// The state is created with create on first use.
func (s *store[T]) update(key string, create func(now time.Time) T, fn func(v *T, now time.Time)) {
	sh := &s.shards[hash(key)%numShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()

	now := s.now()
	e, ok := sh.m[key]
	if !ok {
		e = &entry[T]{val: create(now)}
		sh.m[key] = e
	}
	e.seen = now
	fn(&e.val, now)
}

// evict removes keys that have not been seen for at least idle.
func (s *store[T]) evict(idle time.Duration) int {
	cutoff := s.now().Add(-idle)
	removed := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for k, e := range sh.m {
			if e.seen.Before(cutoff) {
				delete(sh.m, k)
				removed++
			}
		}
		sh.mu.Unlock()
	}
	return removed
}

func (s *store[T]) len() int {
	n := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		n += len(sh.m)
		sh.mu.Unlock()
	}
	return n
}

// janitor periodically runs a cleanup function until closed.
type janitor struct {
	stop chan struct{}
	once sync.Once
}

func startJanitor(interval time.Duration, fn func()) *janitor {
	j := &janitor{stop: make(chan struct{})}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				fn()
			case <-j.stop:
				return
			}
		}
	}()
	return j
}

func (j *janitor) close() {
	j.once.Do(func() { close(j.stop) })
}
