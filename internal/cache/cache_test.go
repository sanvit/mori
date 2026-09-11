package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type listing struct{ Prefix string }

func TestListingCacheTTLRefreshAndBound(t *testing.T) {
	c := New[listing](15*time.Millisecond, 2)
	var calls int
	load := func(context.Context) (listing, error) { calls++; return listing{Prefix: "ok"}, nil }
	_, s, _ := c.Get(context.Background(), "one", false, load)
	if s != "MISS" {
		t.Fatal(s)
	}
	_, s, _ = c.Get(context.Background(), "one", false, load)
	if s != "HIT" || calls != 1 {
		t.Fatal(s, calls)
	}
	c.Get(context.Background(), "one", true, load)
	if calls != 2 {
		t.Fatal("refresh failed")
	}
	time.Sleep(20 * time.Millisecond)
	c.Get(context.Background(), "one", false, load)
	if calls != 3 {
		t.Fatal("expiry failed")
	}
	c.Get(context.Background(), "two", false, load)
	c.Get(context.Background(), "three", false, load)
	if len(c.items) > 2 {
		t.Fatal("unbounded cache")
	}
}
func TestListingCacheSingleFlight(t *testing.T) {
	c := New[listing](time.Minute, 8)
	var calls atomic.Int32
	start := make(chan struct{})
	release := make(chan struct{})
	load := func(context.Context) (listing, error) {
		if calls.Add(1) == 1 {
			close(start)
		}
		<-release
		return listing{Prefix: "shared"}, nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); c.Get(context.Background(), "same", false, load) }()
	<-start
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, _, e := c.Get(context.Background(), "same", false, load)
			if e != nil || l.Prefix != "shared" {
				t.Error(l, e)
			}
		}()
	}
	time.Sleep(15 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestListingCacheErrorsAndDisabled(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Minute} {
		c := New[listing](ttl, 2)
		var calls int
		for i := 0; i < 2; i++ {
			_, _, e := c.Get(context.Background(), "x", false, func(context.Context) (listing, error) { calls++; return listing{}, errors.New("origin") })
			if e == nil {
				t.Fatal("missing error")
			}
		}
		if calls != 2 || len(c.items) != 0 {
			t.Fatal("error cached")
		}
	}
	c := New[listing](0, 2)
	for i := 0; i < 2; i++ {
		_, s, _ := c.Get(context.Background(), "x", false, func(context.Context) (listing, error) { return listing{}, nil })
		if s != "MISS" {
			t.Fatal(s)
		}
	}
}
func TestListingCacheWaitingCancellation(t *testing.T) {
	c := New[listing](time.Minute, 2)
	started := make(chan struct{})
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.Get(context.Background(), "x", false, func(context.Context) (listing, error) { close(started); <-done; return listing{}, nil })
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, e := c.Get(ctx, "x", false, func(context.Context) (listing, error) { t.Error("duplicate load"); return listing{}, nil })
	close(done)
	wg.Wait()
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
