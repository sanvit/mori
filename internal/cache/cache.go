// Package cache is a small bounded TTL cache that coalesces concurrent loads
// of the same key into a single upstream request.
package cache

import (
	"context"
	"sync"
	"time"
)

type item[V any] struct {
	value         V
	expires, used time.Time
}
type flight[V any] struct {
	done  chan struct{}
	value V
	err   error
}
type Cache[V any] struct {
	mu           sync.Mutex
	items        map[string]item[V]
	flights      map[string]*flight[V]
	ttl          time.Duration
	max          int
	hits, misses uint64
}

// New returns a cache holding at most max entries for ttl. A zero ttl disables
// storage but still coalesces in-flight loads.
func New[V any](ttl time.Duration, max int) *Cache[V] {
	return &Cache[V]{items: map[string]item[V]{}, flights: map[string]*flight[V]{}, ttl: ttl, max: max}
}

// Get returns the cached value for key or loads it. The status is HIT, MISS,
// or COALESCED (this call waited on another caller's load).
func (c *Cache[V]) Get(ctx context.Context, key string, refresh bool, load func(context.Context) (V, error)) (V, string, error) {
	c.mu.Lock()
	if e, ok := c.items[key]; ok && !refresh && time.Now().Before(e.expires) {
		e.used = time.Now()
		c.items[key] = e
		c.hits++
		c.mu.Unlock()
		return e.value, "HIT", nil
	}
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			var zero V
			return zero, "COALESCED", ctx.Err()
		case <-f.done:
			return f.value, "COALESCED", f.err
		}
	}
	c.misses++
	f := &flight[V]{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()
	// The initiating request owns the fetch; its cancellation also releases waiters.
	value, err := load(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	f.value, f.err = value, err
	if err == nil && c.ttl > 0 {
		if len(c.items) >= c.max {
			var victim string
			var oldest time.Time
			for k, v := range c.items {
				if victim == "" || v.used.Before(oldest) {
					victim = k
					oldest = v.used
				}
			}
			delete(c.items, victim)
		}
		c.items[key] = item[V]{value, time.Now().Add(c.ttl), time.Now()}
	}
	delete(c.flights, key)
	close(f.done)
	return value, "MISS", err
}

// Stats reports entry count and hit/miss counters.
func (c *Cache[V]) Stats() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"entries": len(c.items), "hits": c.hits, "misses": c.misses, "ttl": c.ttl.String(), "maxEntries": c.max}
}
