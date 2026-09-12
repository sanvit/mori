package objectcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// A fill is shared by all readers of the same version and aligned segment.
// Published bytes are immutable. Each request retains only its read-ahead window.
// The origin context belongs to the subscribers, not to the first client.
type segmentFill struct {
	mu      sync.Mutex
	data    []byte
	changed chan struct{}
	done    bool
	err     error
	run     *segmentRun
	refs    int // guarded by segmentFills.mu
}

// One origin request may populate several contiguous segment fills. The run is
// cancelled only after every subscriber to every segment in it has left.
type segmentRun struct {
	cancel   context.CancelFunc
	finished chan struct{}
	refs     int // guarded by segmentFills.mu
}
type segmentFills struct {
	mu     sync.Mutex
	active map[string]*segmentFill
}

func (f *segmentFill) Write(b []byte) (int, error) {
	f.mu.Lock()
	f.data = append(f.data, b...)
	close(f.changed)
	f.changed = make(chan struct{})
	f.mu.Unlock()
	return len(b), nil
}

type segmentSubscription struct {
	index   int64
	fill    *segmentFill
	release func()
}

func (p *Proxy) acquireSegment(m *ObjectMeta, idx int64) (*segmentFill, func()) {
	subs := p.acquireSegmentRun(m, idx, idx)
	if len(subs) != 1 {
		// A segment can become cached between OpenSegment and this fallback. Publish
		// that immutable cached copy through a normal fill so callers keep one path.
		run := &segmentRun{cancel: func() {}, finished: make(chan struct{}), refs: 1}
		f := &segmentFill{changed: make(chan struct{}), run: run, refs: 1}
		go func() {
			defer close(run.finished)
			start := idx * p.cfg.CacheSegmentSize
			end := minInt64(start+p.cfg.CacheSegmentSize-1, m.Size-1)
			err := p.streamCachedSegmentRange(f, m, idx, 0, end-start+1)
			finishSegmentFill(f, err)
		}()
		return f, func() {
			<-run.finished
		}
	}
	return subs[0].fill, subs[0].release
}

func segmentFillKey(m *ObjectMeta, idx int64) string {
	return fmt.Sprintf("%s/%s/%d", m.CacheKey, m.Version, idx)
}

// acquireSegmentRun subscribes the caller to a contiguous set of missing
// segments. An already-active fill or a segment that became cached splits the
// run; this prevents overlapping origin requests while retaining per-segment
// sharing for requests whose ranges only partly overlap.
func (p *Proxy) acquireSegmentRun(m *ObjectMeta, first, last int64) []segmentSubscription {
	p.fills.mu.Lock()
	defer p.fills.mu.Unlock()
	if p.fills.active == nil {
		p.fills.active = make(map[string]*segmentFill)
	}

	var fills []*segmentFill
	var indices []int64
	for idx := first; idx <= last; idx++ {
		key := segmentFillKey(m, idx)
		if active := p.fills.active[key]; active != nil {
			if len(fills) > 0 {
				break
			}
			active.refs++
			active.run.refs++
			return []segmentSubscription{{index: idx, fill: active, release: p.segmentRelease(key, active)}}
		}
		if p.cache.HasSegment(m.CacheKey, m.Version, idx) {
			break
		}
		f := &segmentFill{changed: make(chan struct{}), refs: 1}
		p.fills.active[key] = f
		fills = append(fills, f)
		indices = append(indices, idx)
	}
	if len(fills) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	run := &segmentRun{cancel: cancel, finished: make(chan struct{}), refs: len(fills)}
	subs := make([]segmentSubscription, len(fills))
	for i, f := range fills {
		f.run = run
		key := segmentFillKey(m, indices[i])
		subs[i] = segmentSubscription{index: indices[i], fill: f, release: p.segmentRelease(key, f)}
	}
	go p.populateSegmentRun(ctx, run, m, indices, fills)
	return subs
}

func (p *Proxy) segmentRelease(key string, f *segmentFill) func() {
	return func() {
		var finished <-chan struct{}
		p.fills.mu.Lock()
		f.refs--
		f.run.refs--
		if f.refs == 0 && p.fills.active[key] == f {
			delete(p.fills.active, key)
		}
		if f.run.refs == 0 {
			f.run.cancel()
			finished = f.run.finished
		}
		p.fills.mu.Unlock()
		if finished != nil {
			<-finished
		}
	}
}

func finishSegmentFill(f *segmentFill, err error) {
	f.mu.Lock()
	if !f.done {
		f.err, f.done = err, true
		close(f.changed)
	}
	f.mu.Unlock()
}

func (p *Proxy) populateSegmentRun(ctx context.Context, run *segmentRun, m *ObjectMeta, indices []int64, fills []*segmentFill) {
	defer close(run.finished)
	select {
	case p.originSlots <- struct{}{}:
		defer func() { <-p.originSlots }()
	case <-ctx.Done():
		for _, f := range fills {
			finishSegmentFill(f, ctx.Err())
		}
		return
	}

	for pos := 0; pos < len(indices); {
		if err := ctx.Err(); err != nil {
			for _, f := range fills[pos:] {
				finishSegmentFill(f, err)
			}
			return
		}

		idx := indices[pos]
		start := idx * p.cfg.CacheSegmentSize
		end := minInt64(start+p.cfg.CacheSegmentSize-1, m.Size-1)
		// Recheck after waiting for the origin slot. A segment may have been
		// committed just before this run registered its fills.
		if p.cache.HasSegment(m.CacheKey, m.Version, idx) {
			err := p.streamCachedSegmentRange(fills[pos], m, idx, 0, end-start+1)
			finishSegmentFill(fills[pos], err)
			if err != nil {
				for _, f := range fills[pos+1:] {
					finishSegmentFill(f, err)
				}
				return
			}
			pos++
			continue
		}

		runEnd := pos
		for runEnd+1 < len(indices) && indices[runEnd+1] == indices[runEnd]+1 {
			if p.cache.HasSegment(m.CacheKey, m.Version, indices[runEnd+1]) {
				break
			}
			runEnd++
		}
		if err := p.fetchSegmentRunStreaming(ctx, m, indices[pos:runEnd+1], fills[pos:runEnd+1]); err != nil {
			for _, f := range fills[pos:] {
				finishSegmentFill(f, err)
			}
			return
		}
		pos = runEnd + 1
	}
}

// copyTo exposes each published chunk immediately, even before a segment has
// finished. Wait for commit as well so a successful request leaves usable cache.
func (f *segmentFill) copyTo(ctx context.Context, w io.Writer, offset, length int64, completeSegment bool, start func()) error {
	end := offset + length
	firstChunk := true
	for {
		f.mu.Lock()
		available := minInt64(int64(len(f.data)), end)
		done, err, changed := f.done, f.err, f.changed
		// Do not let Content-Length signal successful completion before the
		// origin validation and disk commit finish. Otherwise a fast client can
		// close/cancel the request while FTP's final Stat is still in progress.
		// A Range ending inside a segment retains upstream's early delivery:
		// it need not wait for bytes beyond the requested range to arrive.
		if completeSegment && (!done || err != nil) && available >= end {
			available = end - 1
		}
		var b []byte
		if available > offset {
			b = f.data[offset:available]
		}
		f.mu.Unlock()
		if len(b) > 0 {
			start()
			n, writeErr := w.Write(b)
			if writeErr != nil {
				return writeErr
			}
			if n != len(b) {
				return io.ErrShortWrite
			}
			offset += int64(n)
			if firstChunk {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				firstChunk = false
			}
		}
		if done {
			if err != nil {
				return err
			}
			if offset != end {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (p *Proxy) serveSegments(w http.ResponseWriter, r *http.Request, m *ObjectMeta, br ByteRange, status int) (int, error) {
	w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
	first, last := br.Start/p.cfg.CacheSegmentSize, br.End/p.cfg.CacheSegmentSize
	type pending struct {
		fill    *segmentFill
		release func()
	}
	jobs := make(map[int64]pending)
	defer func() {
		for _, job := range jobs {
			job.release()
		}
	}()
	next := first
	started := false
	start := func() {
		if !started {
			started = true
			w.WriteHeader(status)
		}
	}
	for idx := first; idx <= last; idx++ {
		if err := r.Context().Err(); err != nil {
			return status, err
		}
		// Refill only after the previous batch has drained. Sliding the window by
		// one segment would turn every refill into a one-segment origin GET and
		// defeat contiguous coalescing.
		if len(jobs) == 0 {
			windowEnd := minInt64(last, idx+int64(p.cfg.CacheDownloadConcurrency)-1)
			for next <= windowEnd {
				if !p.cache.HasSegment(m.CacheKey, m.Version, next) {
					maxSegments := p.cfg.OriginFetchMaxSize / p.cfg.CacheSegmentSize
					if maxSegments < 1 {
						maxSegments = 1
					}
					runEnd := minInt64(windowEnd, next+maxSegments-1)
					subs := p.acquireSegmentRun(m, next, runEnd)
					if len(subs) == 0 {
						next++
						continue
					}
					for _, sub := range subs {
						jobs[sub.index] = pending{sub.fill, sub.release}
						next = sub.index + 1
					}
					continue
				}
				next++
			}
		}
		segmentStart := idx * p.cfg.CacheSegmentSize
		offset := maxInt64(br.Start, segmentStart) - segmentStart
		length := minInt64(br.End+1, segmentStart+p.cfg.CacheSegmentSize) - segmentStart - offset
		completeSegment := offset+length == minInt64(p.cfg.CacheSegmentSize, m.Size-segmentStart)
		var err error
		if job, ok := jobs[idx]; ok {
			err = job.fill.copyTo(r.Context(), w, offset, length, completeSegment, start)
			job.release()
			delete(jobs, idx)
		} else {
			// Eviction can remove a segment after the look-ahead cache check.
			f, openErr := p.cache.OpenSegment(m.CacheKey, m.Version, idx)
			if openErr == nil {
				start()
				_, err = io.CopyN(w, io.NewSectionReader(f, offset, length), length)
				f.Close()
			} else {
				fill, release := p.acquireSegment(m, idx)
				err = fill.copyTo(r.Context(), w, offset, length, completeSegment, start)
				release()
			}
		}
		if err != nil {
			if !started {
				w.Header().Del("Content-Length")
				w.Header().Del("Content-Range")
				if errors.Is(err, errRangeUnsupported) {
					for idx, job := range jobs {
						job.release()
						delete(jobs, idx)
					}
					return p.streamDirect(w, r, m, statusForFallback(status))
				}
			}
			if isTimeout(err) {
				return http.StatusGatewayTimeout, err
			}
			return http.StatusBadGateway, err
		}
	}
	start()
	return status, nil
}

func statusForFallback(status int) int {
	if status == 200 || status == 206 {
		return 0
	}
	return status
}
