package objectcache

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func TestParallelFullAndRangeReusePartialCache(t *testing.T) {
	for _, rangeHeader := range []string{"", "bytes=0-31"} {
		t.Run(rangeHeader, func(t *testing.T) {
			body := strings.Repeat("abcd", 8)
			var mu sync.Mutex
			gets := map[string]int{}
			entered := make(chan struct{}, 8)
			gate := make(chan struct{})
			var once sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				if r.Method == "HEAD" {
					w.Header().Set("Content-Length", "32")
					return
				}
				br, ok, err := parseSingleRange(r.Header.Get("Range"), 32)
				if !ok || err != nil {
					t.Error("expected aligned Range GET")
					http.Error(w, "range", 400)
					return
				}
				if r.Header.Get("If-Match") != `"v1"` {
					t.Error("missing If-Match")
				}
				mu.Lock()
				gets[r.Header.Get("Range")]++
				mu.Unlock()
				entered <- struct{}{}
				select {
				case <-gate:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/32", br.Start, br.End))
				w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
				w.WriteHeader(206)
				io.WriteString(w, body[br.Start:br.End+1])
			}))
			defer srv.Close()
			p := newTestProxy(t, srv.URL, nil)
			// Seed a valid prefix, as if a previous download had been interrupted.
			m := &ObjectMeta{CacheKey: cacheKeyFor("/file", "", "sort"), OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 32, Cacheable: true, ExpiresAt: time.Now().Add(time.Hour)}
			if err := p.cache.SaveMeta(m); err != nil {
				t.Fatal(err)
			}
			if err := p.cache.StoreSegmentStreaming(m.CacheKey, m.Version, 0, 4, strings.NewReader("abcd"), nil); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var rec *httptest.ResponseRecorder
			go func() { rec = get(p, "http://proxy/file", map[string]string{"Range": rangeHeader}); close(done) }()
			// The first bounded batch coalesces the three missing segments that fit
			// beside the cached prefix in the four-segment window.
			awaitSignal(t, entered)
			release()
			awaitSignal(t, done)
			if rec.Body.String() != body {
				t.Fatalf("body=%q", rec.Body.String())
			}
			wantStatus := 200
			if rangeHeader != "" {
				wantStatus = 206
			}
			if rec.Code != wantStatus {
				t.Fatalf("status=%d", rec.Code)
			}
			mu.Lock()
			defer mu.Unlock()
			want := map[string]int{"bytes=4-15": 1, "bytes=16-31": 1}
			if len(gets) != len(want) {
				t.Fatalf("origin ranges=%v want=%v", gets, want)
			}
			for rg, n := range want {
				if gets[rg] != n {
					t.Fatalf("origin ranges=%v want=%v", gets, want)
				}
			}
			if !p.cache.AllSegmentsPresent(m) {
				t.Fatal("full object not cached")
			}
		})
	}
}

func TestMissingSegmentsCoalesceIntoContiguousRuns(t *testing.T) {
	body := []byte("abcdefghijklmn") // seven 2-byte segments
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		br, ok, err := parseSingleRange(r.Header.Get("Range"), int64(len(body)))
		if !ok || err != nil {
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[br.Start : br.End+1])
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) {
		c.CacheSegmentSize = 2
		c.CacheBlockSize = 8
		c.CacheDownloadConcurrency = 8
		c.OriginFetchMaxSize = 16
	})
	m := &ObjectMeta{
		CacheKey: cacheKeyFor("/file", "", "sort"), OriginKey: "file", Version: "v1", ETag: `"v1"`,
		Size: int64(len(body)), Cacheable: true, CachedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := p.cache.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	if err := p.cache.StoreSegmentStreaming(m.CacheKey, m.Version, 0, 2, bytes.NewReader(body[0:2]), nil); err != nil {
		t.Fatal(err)
	}
	if err := p.cache.StoreSegmentStreaming(m.CacheKey, m.Version, 3, 2, bytes.NewReader(body[6:8]), nil); err != nil {
		t.Fatal(err)
	}

	rec := get(p, "http://proxy/file", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	mu.Lock()
	got := append([]string(nil), ranges...)
	mu.Unlock()
	sort.Strings(got)
	want := []string{"bytes=2-5", "bytes=8-13"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("origin ranges=%v want=%v", got, want)
	}
}

func TestOriginFetchMaxSizeSplitsContiguousRun(t *testing.T) {
	body := []byte("abcdefghijklmnopqrst")
	var mu sync.Mutex
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		br, ok, err := parseSingleRange(r.Header.Get("Range"), int64(len(body)))
		if !ok || err != nil {
			http.Error(w, "range required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[br.Start : br.End+1])
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) {
		c.CacheSegmentSize = 2
		c.CacheBlockSize = 8
		c.CacheDownloadConcurrency = 10
		c.OriginFetchMaxSize = 6
	})
	m := &ObjectMeta{
		CacheKey: cacheKeyFor("/file", "", "sort"), OriginKey: "file", Version: "v1", ETag: `"v1"`,
		Size: int64(len(body)), Cacheable: true, CachedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := p.cache.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	rec := get(p, "http://proxy/file", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	mu.Lock()
	got := append([]string(nil), ranges...)
	mu.Unlock()
	sort.Strings(got)
	want := []string{"bytes=0-5", "bytes=12-17", "bytes=18-19", "bytes=6-11"}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("origin ranges=%v want=%v", got, want)
	}
}

func TestCoalescedRunSharesIndividualSegmentFills(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "bytes=0-11" {
			http.Error(w, "unexpected range", http.StatusBadRequest)
			return
		}
		entered <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", "bytes 0-11/12")
		w.Header().Set("Content-Length", "12")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "abcdefghijkl")
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)
	m := &ObjectMeta{CacheKey: "file", OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 12}
	subs := p.acquireSegmentRun(m, 0, 2)
	if len(subs) != 3 {
		t.Fatalf("subscriptions=%d, want 3", len(subs))
	}
	shared, releaseShared := p.acquireSegment(m, 1)
	if shared != subs[1].fill {
		t.Fatal("middle segment did not join the coalesced in-flight fill")
	}
	awaitSignal(t, entered)
	for _, sub := range subs {
		sub.release()
	}
	close(gate)
	var out strings.Builder
	if err := shared.copyTo(context.Background(), &out, 0, 4, true, func() {}); err != nil {
		t.Fatal(err)
	}
	releaseShared()
	if out.String() != "efgh" {
		t.Fatalf("shared segment=%q", out.String())
	}
}

func TestTruncatedCoalescedRunKeepsOnlyCompleteSegments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Range", "bytes 0-7/8")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "abcde")
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)
	m := &ObjectMeta{CacheKey: "file", OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 8}
	subs := p.acquireSegmentRun(m, 0, 1)
	defer func() {
		for _, sub := range subs {
			sub.release()
		}
	}()
	var out strings.Builder
	if err := subs[1].fill.copyTo(context.Background(), &out, 0, 4, true, func() {}); err == nil {
		t.Fatal("truncated second segment unexpectedly succeeded")
	}
	if !p.cache.HasSegment(m.CacheKey, m.Version, 0) {
		t.Fatal("complete first segment was not retained")
	}
	if p.cache.HasSegment(m.CacheKey, m.Version, 1) {
		t.Fatal("truncated second segment was committed")
	}
}

func TestSharedFillSurvivesOneSubscriberCancellation(t *testing.T) {
	entered := make(chan struct{}, 1)
	gate := make(chan struct{})
	var once sync.Once
	releaseGate := func() { once.Do(func() { close(gate) }) }
	defer releaseGate()
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		entered <- struct{}{}
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Range", "bytes 0-3/8")
		w.Header().Set("Content-Length", "4")
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(206)
		io.WriteString(w, "abcd")
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)
	m := &ObjectMeta{CacheKey: "file", OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 8}
	first, releaseFirst := p.acquireSegment(m, 0)
	second, releaseSecond := p.acquireSegment(m, 0)
	defer releaseSecond()
	if first != second {
		t.Fatal("fill not shared")
	}
	awaitSignal(t, entered)
	releaseFirst()
	releaseGate()
	var out strings.Builder
	if err := second.copyTo(context.Background(), &out, 0, 4, true, func() {}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "abcd" {
		t.Fatal(out.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("origin requests=%d", requests)
	}
}

type stopWriter struct {
	http.ResponseWriter
	cancel context.CancelFunc
}

func (w stopWriter) Write(b []byte) (int, error) { w.cancel(); return 0, context.Canceled }

func TestDisconnectCancelsReadAheadAndPreservesCompletedSegments(t *testing.T) {
	started := make(chan struct{}, 4)
	cancelled := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "40")
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)
	m := &ObjectMeta{CacheKey: cacheKeyFor("/file", "", "sort"), OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 40, Cacheable: true, ExpiresAt: time.Now().Add(time.Hour)}
	if err := p.cache.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	if err := p.cache.StoreSegmentStreaming(m.CacheKey, m.Version, 0, 4, strings.NewReader("abcd"), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	serveAborted(p, stopWriter{rec, cancel}, httptest.NewRequest("GET", "http://proxy/file", nil).WithContext(ctx))
	if !p.cache.HasSegment(m.CacheKey, m.Version, 0) {
		t.Fatal("completed prefix removed")
	}
	if p.cache.AllSegmentsPresent(m) {
		t.Fatal("disconnect filled entire object")
	}
	p.fills.mu.Lock()
	remaining := len(p.fills.active)
	p.fills.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d abandoned fills", remaining)
	}
	if len(started) > 3 {
		t.Fatal("read ahead exceeded three segments behind cached prefix")
	}
}

func TestGlobalFillLimitAndQueuedCancellation(t *testing.T) {
	entered := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { entered <- struct{}{}; <-r.Context().Done() }))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) { c.OriginMaxConcurrentRequests = 2 })
	var releases []func()
	for i := 0; i < 6; i++ {
		_, release := p.acquireSegment(&ObjectMeta{CacheKey: strconv.Itoa(i), OriginKey: "file", Version: "v1", Size: 8}, 0)
		releases = append(releases, release)
	}
	awaitSignal(t, entered)
	awaitSignal(t, entered)
	select {
	case <-entered:
		t.Fatal("exceeded global limit")
	case <-time.After(50 * time.Millisecond):
	}
	// Cancel all concurrently, including workers waiting for an origin slot.
	var wg sync.WaitGroup
	for _, release := range releases {
		wg.Add(1)
		go func(release func()) { defer wg.Done(); release() }(release)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	awaitSignal(t, done)
	if len(p.originSlots) != 0 {
		t.Fatal("origin slot leaked")
	}
}

func TestSegmentOriginValidation(t *testing.T) {
	for _, kind := range []string{"precondition", "etag", "range", "length", "oversize", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Content-Range", "bytes 0-3/8")
				w.Header().Set("Content-Length", "4")
				switch kind {
				case "precondition":
					w.WriteHeader(412)
					return
				case "etag":
					w.Header().Set("ETag", `"v2"`)
				case "range":
					w.Header().Set("Content-Range", "bytes 4-7/8")
				case "length":
					w.Header().Set("Content-Length", "5")
				case "oversize":
					w.Header().Del("Content-Length")
				}
				w.WriteHeader(206)
				if kind == "oversize" {
					w.(http.Flusher).Flush()
					io.WriteString(w, "abcde")
					return
				}
				if kind == "truncated" {
					io.WriteString(w, "ab")
					return
				}
				io.WriteString(w, "abcd")
			}))
			defer srv.Close()
			p := newTestProxy(t, srv.URL, nil)
			m := &ObjectMeta{CacheKey: "file", OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: 8, ExpiresAt: time.Now().Add(time.Hour)}
			if err := p.cache.SaveMeta(m); err != nil {
				t.Fatal(err)
			}
			f, release := p.acquireSegment(m, 0)
			defer release()
			var out strings.Builder
			if err := f.copyTo(context.Background(), &out, 0, 4, true, func() {}); err == nil {
				t.Fatal("accepted invalid origin response")
			}
			if p.cache.HasSegment(m.CacheKey, m.Version, 0) {
				t.Fatal("invalid segment committed")
			}
			if kind == "precondition" || kind == "etag" {
				meta, err := p.cache.LoadMeta(m.CacheKey)
				if err != nil || meta == nil || time.Now().Before(meta.ExpiresAt) {
					t.Fatal("metadata not invalidated")
				}
			}
		})
	}
}

func TestReadAheadWindowStopsAtSlowClient(t *testing.T) {
	started := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if r.Method == "HEAD" {
			w.Header().Set("Content-Length", "40")
			return
		}
		br, _, _ := parseSingleRange(r.Header.Get("Range"), 40)
		started <- r.Header.Get("Range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/40", br.Start, br.End))
		w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
		w.WriteHeader(206)
		io.WriteString(w, strings.Repeat("a", int(br.End-br.Start+1)))
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gate := make(chan struct{})
	blocked := make(chan struct{}, 1)
	w := &blockedClient{ResponseWriter: httptest.NewRecorder(), gate: gate, blocked: blocked}
	done := make(chan struct{})
	go func() {
		serveAborted(p, w, httptest.NewRequest("GET", "http://proxy/file", nil).WithContext(ctx))
		close(done)
	}()
	awaitSignal(t, blocked)
	select {
	case rg := <-started:
		if rg != "bytes=0-15" {
			t.Fatalf("origin range=%q, want one four-segment fetch", rg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("origin fetch did not start")
	}
	select {
	case <-started:
		t.Error("scheduled beyond read-ahead window")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	close(gate)
	awaitSignal(t, done)
}

type blockedClient struct {
	http.ResponseWriter
	gate    <-chan struct{}
	blocked chan<- struct{}
}

type discardResponseWriter struct {
	header http.Header
}

func (w *discardResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}
func (w *discardResponseWriter) WriteHeader(int)             {}
func (w *discardResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func BenchmarkSegmentFetchCoalescing(b *testing.B) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 32*1024) // 512 KiB
	const segmentSize = int64(64 * 1024)
	for _, tc := range []struct {
		name        string
		maxFetch    int64
		originSlots int
	}{
		{name: "per_segment_parallel", maxFetch: segmentSize, originSlots: 32},
		{name: "per_segment_constrained", maxFetch: segmentSize, originSlots: 1},
		{name: "coalesced_window", maxFetch: int64(len(body)), originSlots: 32},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var mu sync.Mutex
			originGets := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				br, ok, err := parseSingleRange(r.Header.Get("Range"), int64(len(body)))
				if !ok || err != nil {
					http.Error(w, "range required", http.StatusBadRequest)
					return
				}
				mu.Lock()
				originGets++
				mu.Unlock()
				time.Sleep(time.Millisecond) // model non-zero origin request latency
				w.Header().Set("ETag", `"v1"`)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
				w.Header().Set("Content-Length", fmt.Sprint(br.End-br.Start+1))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[br.Start : br.End+1])
			}))
			defer srv.Close()
			cfg := Config{
				CacheEnabled: true, CacheDir: b.TempDir(), CacheBlockSize: 256 * 1024, CacheSegmentSize: segmentSize,
				CacheDownloadConcurrency: 8, OriginFetchMaxSize: tc.maxFetch, OriginMaxConcurrentRequests: tc.originSlots,
			}
			origin := newTestOriginServer(srv.URL + "/bucket")
			cache, err := NewDiskCache(cfg.CacheDir, 0, cfg.CacheBlockSize, cfg.CacheSegmentSize)
			if err != nil {
				b.Fatal(err)
			}
			p := NewProxy(cfg, origin, cache)
			b.SetBytes(int64(len(body)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := &ObjectMeta{CacheKey: fmt.Sprintf("bench-%d", i), OriginKey: "file", Version: "v1", ETag: `"v1"`, Size: int64(len(body))}
				req := httptest.NewRequest(http.MethodGet, "http://proxy/file", nil)
				if _, err := p.serveSegments(&discardResponseWriter{}, req, m, ByteRange{Start: 0, End: int64(len(body)) - 1}, http.StatusOK); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			mu.Lock()
			b.ReportMetric(float64(originGets)/float64(b.N), "origin_gets/op")
			mu.Unlock()
		})
	}
}

func (w *blockedClient) Write(b []byte) (int, error) {
	w.blocked <- struct{}{}
	<-w.gate
	return 0, context.Canceled
}
