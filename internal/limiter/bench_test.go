package limiter

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// naive is the obvious implementation: one global mutex around one map.
// It exists only as a baseline to show what sharding buys under contention.
type naive struct {
	mu sync.Mutex
	m  map[string]*bucket
}

func (n *naive) Allow(key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	b, ok := n.m[key]
	if !ok {
		b = &bucket{tokens: 1 << 30, last: time.Now()}
		n.m[key] = b
	}
	b.tokens--
	return b.tokens >= 0
}

func keys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = fmt.Sprintf("client-%d", i)
	}
	return ks
}

func runManyKeys(b *testing.B, allow func(string)) {
	ks := keys(4096)
	var seed atomic.Uint64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := int(seed.Add(977)) // different start per goroutine
		for pb.Next() {
			allow(ks[i%len(ks)])
			i++
		}
	})
}

func BenchmarkTokenBucket_ManyKeys(b *testing.B) {
	tb := NewTokenBucket(1e9, 1<<30)
	defer tb.Close()
	runManyKeys(b, func(k string) { tb.Allow(k) })
}

func BenchmarkSlidingWindow_ManyKeys(b *testing.B) {
	sw := NewSlidingWindow(1<<30, time.Minute)
	defer sw.Close()
	runManyKeys(b, func(k string) { sw.Allow(k) })
}

func BenchmarkNaiveSingleMutex_ManyKeys(b *testing.B) {
	n := &naive{m: make(map[string]*bucket)}
	runManyKeys(b, func(k string) { n.Allow(k) })
}

// Worst case for the sharded design: every goroutine hits the same key, so
// they all contend on one shard lock.
func BenchmarkTokenBucket_SingleHotKey(b *testing.B) {
	tb := NewTokenBucket(1e9, 1<<30)
	defer tb.Close()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			tb.Allow("hot")
		}
	})
}
