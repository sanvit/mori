package objectcache

import "sync"

type flightCall struct {
	done chan struct{}
	err  error
}

type FlightGroup struct {
	mu sync.Mutex
	m  map[string]*flightCall
}

func (g *FlightGroup) Do(key string, fn func() error) error {
	_, err := g.DoLeader(key, fn)
	return err
}

// DoLeader coalesces concurrent work for the same key. The caller that actually
// executes fn gets leader=true; waiters get leader=false after the leader finishes.
func (g *FlightGroup) DoLeader(key string, fn func() error) (leader bool, err error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = map[string]*flightCall{}
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		<-c.done
		return false, c.err
	}
	c := &flightCall{done: make(chan struct{})}
	g.m[key] = c
	g.mu.Unlock()

	// Release waiters and drop the key even if fn panics. Without this a single
	// panicking request would leave every later caller for that key blocked on a
	// channel that is never closed.
	defer func() {
		close(c.done)
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()

	c.err = fn()
	return true, c.err
}
