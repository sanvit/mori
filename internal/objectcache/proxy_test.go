package objectcache

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanonicalQuerySort(t *testing.T) {
	got := canonicalQuery("z=2&a=3&z=1&a=2", "sort")
	want := "a=2&a=3&z=1&z=2"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseBytes(t *testing.T) {
	got, err := parseBytes("1.5GB")
	if err != nil || got != 1500000000 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestRange(t *testing.T) {
	r, ok, err := parseSingleRange("bytes=100-199", 1000)
	if err != nil || !ok || r.Start != 100 || r.End != 199 {
		t.Fatalf("unexpected %+v %v %v", r, ok, err)
	}
	r, ok, err = parseSingleRange("bytes=-100", 1000)
	if err != nil || !ok || r.Start != 900 || r.End != 999 {
		t.Fatalf("unexpected suffix %+v %v %v", r, ok, err)
	}
}

func TestSPANavigation(t *testing.T) {
	p := &Proxy{cfg: Config{SPAMode: true}}
	req := httptest.NewRequest("GET", "http://example.test/app/users/1", nil)
	req.Header.Set("Accept", "text/html")
	if !p.isSPANavigation(req, "/app/users/1") {
		t.Fatal("expected SPA navigation")
	}
	req2 := httptest.NewRequest("GET", "http://example.test/app.js", nil)
	req2.Header.Set("Accept", "*/*")
	if p.isSPANavigation(req2, "/app.js") {
		t.Fatal("asset must not be SPA fallback")
	}
}

func TestIntegrationStreamingRangeSPAAnd404(t *testing.T) {
	var mu sync.Mutex
	gets := map[string]int{}
	heads := map[string]int{}
	big := []byte("abcdefghijklmnopqrstuvwxyz")

	originSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		mu.Lock()
		if r.Method == http.MethodHead {
			heads[key]++
		} else if r.Method == http.MethodGet {
			gets[key]++
		}
		mu.Unlock()

		var body []byte
		var ct = "application/octet-stream"
		switch key {
		case "big.bin":
			body = big
		case "index.html":
			body = []byte("SPA-INDEX")
			ct = "text/html"
		case "404.html":
			body = []byte("CUSTOM-404")
			ct = "text/html"
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"etag-1"`)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Last-Modified", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		if rg := r.Header.Get("Range"); rg != "" {
			br, ok, err := parseSingleRange(rg, int64(len(body)))
			if err != nil || !ok {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			part := body[br.Start : br.End+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(part)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(part)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer originSrv.Close()

	dir := t.TempDir()
	cfg := Config{

		CacheEnabled: true, CacheDir: dir, CacheBlockSize: 8, CacheSegmentSize: 2, CacheMaxDiskSize: 64,
		CacheQueryMode: "sort", CacheDefaultTTL: time.Hour, CacheMaxTTL: 24 * time.Hour, CacheRespectOrigin: true,
		SPAMode: true, SPAIndex: "/index.html", ErrorPages: map[int]string{404: "/404.html"},
	}
	origin := newTestOriginServer(originSrv.URL + "/bucket")
	cache, err := NewDiskCache(dir, cfg.CacheMaxDiskSize, cfg.CacheBlockSize, cfg.CacheSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	proxy := NewProxy(cfg, origin, cache)

	// Full miss streams and fills segments grouped into blocks.
	r1 := httptest.NewRequest(http.MethodGet, "http://proxy/big.bin?z=2&a=1", nil)
	w1 := httptest.NewRecorder()
	proxy.ServeHTTP(w1, r1)
	if w1.Code != 200 || !bytes.Equal(w1.Body.Bytes(), big) || w1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("full miss: code=%d cache=%s body=%q", w1.Code, w1.Header().Get("X-Cache"), w1.Body.Bytes())
	}

	// Same query in different order should hit the same cache key.
	r2 := httptest.NewRequest(http.MethodGet, "http://proxy/big.bin?a=1&z=2", nil)
	w2 := httptest.NewRecorder()
	proxy.ServeHTTP(w2, r2)
	if w2.Code != 200 || !bytes.Equal(w2.Body.Bytes(), big) || w2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("full hit: code=%d cache=%s body=%q", w2.Code, w2.Header().Get("X-Cache"), w2.Body.Bytes())
	}

	// Different query creates another cache key; sparse range fetches only needed segments.
	rr := httptest.NewRequest(http.MethodGet, "http://proxy/big.bin?v=other", nil)
	rr.Header.Set("Range", "bytes=10-14")
	wr := httptest.NewRecorder()
	proxy.ServeHTTP(wr, rr)
	if wr.Code != http.StatusPartialContent || wr.Body.String() != "klmno" || wr.Header().Get("X-Cache") != "RANGE-MISS" {
		t.Fatalf("range: code=%d cache=%s body=%q", wr.Code, wr.Header().Get("X-Cache"), wr.Body.String())
	}

	// SPA navigation falls back to index.html.
	rs := httptest.NewRequest(http.MethodGet, "http://proxy/dashboard/users/1", nil)
	rs.Header.Set("Accept", "text/html")
	ws := httptest.NewRecorder()
	proxy.ServeHTTP(ws, rs)
	if ws.Code != 200 || ws.Body.String() != "SPA-INDEX" {
		t.Fatalf("spa: code=%d body=%q", ws.Code, ws.Body.String())
	}

	// Missing asset stays 404 and uses custom page.
	rn := httptest.NewRequest(http.MethodGet, "http://proxy/missing.js", nil)
	rn.Header.Set("Accept", "*/*")
	wn := httptest.NewRecorder()
	proxy.ServeHTTP(wn, rn)
	if wn.Code != 404 || wn.Body.String() != "CUSTOM-404" {
		t.Fatalf("404: code=%d body=%q", wn.Code, wn.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if gets["big.bin"] < 2 { // one full GET + at least one segment Range GET for the second cache key
		t.Fatalf("expected origin GETs, got %v", gets)
	}
}

func TestRangeMissStreamsBeforeWholeSegmentIsCached(t *testing.T) {
	firstOriginChunk := make(chan struct{}, 1)
	lastOriginChunk := make(chan struct{}, 1)
	releaseFirst := make(chan struct{}, 1)
	releaseLast := make(chan struct{}, 1)
	defer func() {
		select {
		case releaseFirst <- struct{}{}:
		default:
		}
		select {
		case releaseLast <- struct{}{}:
		default:
		}
	}()

	body := []byte("abcdefghijklmnop") // 16 bytes; 8-byte blocks, 4-byte segments
	originSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/big.bin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"stream-etag"`)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Last-Modified", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}

		switch r.Header.Get("Range") {
		case "bytes=0-11":
			w.Header().Set("Content-Range", "bytes 0-11/16")
			w.Header().Set("Content-Length", "12")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("abc"))
			w.(http.Flusher).Flush()
			firstOriginChunk <- struct{}{}
			<-releaseFirst
			_, _ = w.Write([]byte("defghijk"))
			w.(http.Flusher).Flush()
			lastOriginChunk <- struct{}{}
			<-releaseLast
			_, _ = w.Write([]byte("l"))
		default:
			http.Error(w, "unexpected range "+r.Header.Get("Range"), http.StatusBadRequest)
		}
	}))
	defer originSrv.Close()

	dir := t.TempDir()
	cfg := Config{

		CacheEnabled: true, CacheDir: dir, CacheBlockSize: 8, CacheSegmentSize: 4, CacheMaxDiskSize: 64,
		CacheQueryMode: "sort", CacheDefaultTTL: time.Hour, CacheMaxTTL: 24 * time.Hour, CacheRespectOrigin: true,
		ErrorPages: map[int]string{},
	}
	origin := newTestOriginServer(originSrv.URL + "/bucket")
	cache, err := NewDiskCache(dir, cfg.CacheMaxDiskSize, cfg.CacheBlockSize, cfg.CacheSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := httptest.NewServer(NewProxy(cfg, origin, cache))
	defer proxySrv.Close()

	req, err := http.NewRequest(http.MethodGet, proxySrv.URL+"/big.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=2-10")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Cache"); got != "RANGE-MISS" {
		t.Fatalf("X-Cache=%q", got)
	}

	select {
	case <-firstOriginChunk:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not send first partial segment chunk")
	}

	// Only a,b,c have arrived for segment 0-3. Since this Range starts at byte 2,
	// byte c must already reach the client before the segment finishes with d.
	firstByte := make(chan byte, 1)
	firstErr := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := io.ReadFull(resp.Body, b[:])
		if err != nil {
			firstErr <- err
			return
		}
		firstByte <- b[0]
	}()
	select {
	case b := <-firstByte:
		if b != 'c' {
			t.Fatalf("first streamed byte=%q want c", b)
		}
	case err := <-firstErr:
		t.Fatalf("first body read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("client did not receive overlap before first segment completed")
	}

	releaseFirst <- struct{}{}
	select {
	case <-lastOriginChunk:
	case <-time.After(2 * time.Second):
		t.Fatal("origin did not start final segment")
	}

	// Requested bytes are c..k. The final segment is 8-11, but the Range ends at
	// k (byte 10), so the client gets its complete body before byte l is released.
	rest := make(chan []byte, 1)
	restErr := make(chan error, 1)
	go func() {
		b := make([]byte, 8)
		_, err := io.ReadFull(resp.Body, b)
		if err != nil {
			restErr <- err
			return
		}
		rest <- b
	}()
	select {
	case b := <-rest:
		if string(b) != "defghijk" {
			t.Fatalf("rest=%q", b)
		}
	case err := <-restErr:
		t.Fatalf("remaining body read: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("client waited for unused tail of final segment")
	}

	releaseLast <- struct{}{}
	_, _ = io.Copy(io.Discard, resp.Body)

	meta, err := cache.LoadMeta(cacheKeyFor("/big.bin", "", "sort"))
	if err != nil || meta == nil {
		t.Fatalf("load meta: meta=%v err=%v", meta, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cache.HasSegment(meta.CacheKey, meta.Version, 0) && cache.HasSegment(meta.CacheKey, meta.Version, 1) && cache.HasSegment(meta.CacheKey, meta.Version, 2) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected all three requested segments to be committed after stream finishes")
}

func TestRangeFetchesOnlyMissingSegments(t *testing.T) {
	body := []byte("abcdefghijklmnop")
	var mu sync.Mutex
	var ranges []string
	originSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/big.bin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"seg-etag"`)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Last-Modified", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		rg := r.Header.Get("Range")
		mu.Lock()
		ranges = append(ranges, rg)
		mu.Unlock()
		br, ok, err := parseSingleRange(rg, int64(len(body)))
		if err != nil || !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		part := body[br.Start : br.End+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
		w.Header().Set("Content-Length", strconv.Itoa(len(part)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(part)
	}))
	defer originSrv.Close()

	dir := t.TempDir()
	cfg := Config{

		CacheEnabled: true, CacheDir: dir, CacheBlockSize: 8, CacheSegmentSize: 2, CacheMaxDiskSize: 64,
		CacheQueryMode: "sort", CacheDefaultTTL: time.Hour, CacheMaxTTL: 24 * time.Hour, CacheRespectOrigin: true,
		ErrorPages: map[int]string{},
	}
	origin := newTestOriginServer(originSrv.URL + "/bucket")
	cache, err := NewDiskCache(dir, cfg.CacheMaxDiskSize, cfg.CacheBlockSize, cfg.CacheSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	proxy := NewProxy(cfg, origin, cache)

	// bytes 2-5 maps exactly to segments 1 (2-3) and 2 (4-5), not block 0-7.
	r1 := httptest.NewRequest(http.MethodGet, "http://proxy/big.bin", nil)
	r1.Header.Set("Range", "bytes=2-5")
	w1 := httptest.NewRecorder()
	proxy.ServeHTTP(w1, r1)
	if w1.Code != http.StatusPartialContent || w1.Body.String() != "cdef" {
		t.Fatalf("first range: code=%d body=%q", w1.Code, w1.Body.String())
	}

	// bytes 3-6 reuses segments 1 and 2; only segment 3 (6-7) is missing.
	r2 := httptest.NewRequest(http.MethodGet, "http://proxy/big.bin", nil)
	r2.Header.Set("Range", "bytes=3-6")
	w2 := httptest.NewRecorder()
	proxy.ServeHTTP(w2, r2)
	if w2.Code != http.StatusPartialContent || w2.Body.String() != "defg" {
		t.Fatalf("second range: code=%d body=%q", w2.Code, w2.Body.String())
	}

	mu.Lock()
	got := append([]string(nil), ranges...)
	mu.Unlock()
	sort.Strings(got)
	want := []string{"bytes=2-5", "bytes=6-7"}
	if len(got) != len(want) {
		t.Fatalf("origin ranges=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("origin ranges=%v want=%v", got, want)
		}
	}
}

func TestOriginFailureMidStreamDoesNotAppendErrorPage(t *testing.T) {
	full := strings.Repeat("A", 40)
	originSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bucket/big.bin" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"mid-stream-etag"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(full)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Range", "bytes 0-39/40")
		w.Header().Set("Content-Length", "40")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte(full[:10]))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // drop the connection mid-body
	}))
	originSrv.Config.ErrorLog = log.New(io.Discard, "", 0)
	defer originSrv.Close()

	dir := t.TempDir()
	cfg := Config{

		CacheEnabled: true, CacheDir: dir, CacheBlockSize: 32, CacheSegmentSize: 16, CacheMaxDiskSize: 1 << 20,
		CacheQueryMode: "sort", CacheDefaultTTL: time.Hour, CacheMaxTTL: time.Hour, CacheRespectOrigin: true,
		ErrorPages: map[int]string{},
	}
	origin := newTestOriginServer(originSrv.URL + "/bucket")
	cache, err := NewDiskCache(dir, cfg.CacheMaxDiskSize, cfg.CacheBlockSize, cfg.CacheSegmentSize)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("expected failed HTTP stream, got %v", got)
			}
		}()
		NewProxy(cfg, origin, cache).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://proxy/big.bin", nil))
	}()

	// The client already received a 200 and the first bytes, so the truncation can
	// only be signalled by the short body. Writing an error document here would
	// splice HTML into what the client is reading as an octet-stream.
	if got := rec.Body.String(); got != full[:10] {
		t.Fatalf("body=%q, want the %d bytes that were streamed before the failure", got, 10)
	}

	// The interrupted stream must not leave a short tail segment behind.
	meta, err := cache.LoadMeta(cacheKeyFor("/big.bin", "", "sort"))
	if err != nil || meta == nil {
		t.Fatalf("load meta: meta=%v err=%v", meta, err)
	}
	if cache.HasSegment(meta.CacheKey, meta.Version, 0) {
		t.Fatal("segment 0 was only partially received and must not be cached")
	}
}

func TestFlightGroupPanicDoesNotWedgeWaiters(t *testing.T) {
	var g FlightGroup
	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		defer func() { _ = recover() }()
		_ = g.Do("k", func() error {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started

	waiterDone := make(chan struct{})
	go func() {
		_ = g.Do("k", func() error { return nil })
		close(waiterDone)
	}()

	// Give the waiter time to join the in-flight call before the leader panics.
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case <-waiterDone:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter is still blocked after the leader panicked")
	}

	// The key must be free again so later callers can retry rather than inherit
	// the dead call.
	done := make(chan bool, 1)
	go func() {
		leader, _ := g.DoLeader("k", func() error { return nil })
		done <- leader
	}()
	select {
	case leader := <-done:
		if !leader {
			t.Fatal("expected the next caller to become leader for a released key")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("key was never released from the flight group")
	}
}

// newTestOrigin serves a small fixed object set and counts requests per key.
type testOrigin struct {
	mu      sync.Mutex
	bodies  map[string][]byte
	etags   map[string]string
	gets    map[string]int
	heads   map[string]int
	handler http.Handler
}

func newTestOrigin() *testOrigin {
	o := &testOrigin{
		bodies: map[string][]byte{},
		etags:  map[string]string{},
		gets:   map[string]int{},
		heads:  map[string]int{},
	}
	o.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		o.mu.Lock()
		body, ok := o.bodies[key]
		etag := o.etags[key]
		if r.Method == http.MethodGet {
			o.gets[key]++
		} else if r.Method == http.MethodHead {
			o.heads[key]++
		}
		o.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Last-Modified", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		if rg := r.Header.Get("Range"); rg != "" {
			br, ok, err := parseSingleRange(rg, int64(len(body)))
			if err != nil || !ok {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			part := body[br.Start : br.End+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(part)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(part)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	return o
}

func (o *testOrigin) put(key, body, etag string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.bodies[key] = []byte(body)
	o.etags[key] = etag
}

func (o *testOrigin) getCount(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gets[key]
}

func (o *testOrigin) headCount(key string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.heads[key]
}

func newTestProxy(t *testing.T, endpoint string, tweak func(*Config)) *Proxy {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{

		CacheEnabled: true, CacheDir: dir, CacheBlockSize: 8, CacheSegmentSize: 4, CacheMaxDiskSize: 1 << 20,
		CacheQueryMode: "sort", CacheDefaultTTL: time.Hour, CacheMaxTTL: 24 * time.Hour, CacheRespectOrigin: true,
		IndexDocument: "index.html", HealthPath: "/healthz", ErrorPages: map[int]string{},
	}
	if tweak != nil {
		tweak(&cfg)
	}
	origin := newTestOriginServer(endpoint + "/bucket")
	cache, err := NewDiskCache(cfg.CacheDir, cfg.CacheMaxDiskSize, cfg.CacheBlockSize, cfg.CacheSegmentSize)
	if err != nil {
		t.Fatal(err)
	}
	return NewProxy(cfg, origin, cache)
}

func serveAborted(p *Proxy, w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil && v != http.ErrAbortHandler {
			panic(v)
		}
	}()
	p.ServeHTTP(w, r)
}

func get(p *Proxy, target string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				panic(v)
			}
		}()
		p.ServeHTTP(rec, r)
	}()
	return rec
}

func TestTrailingSlashResolvesToIndexDocument(t *testing.T) {
	o := newTestOrigin()
	o.put("index.html", "ROOT-INDEX", `"root"`)
	o.put("docs/index.html", "DOCS-INDEX", `"docs"`)
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)

	for _, tc := range []struct{ path, want string }{
		{"http://proxy/", "ROOT-INDEX"},
		{"http://proxy/docs/", "DOCS-INDEX"},
		{"http://proxy/index.html", "ROOT-INDEX"},
	} {
		rec := get(p, tc.path, nil)
		if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
			t.Fatalf("%s: code=%d body=%q want %q", tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}

	// "/" and "/index.html" resolve to the same object, so they must share a cache
	// entry rather than each fetching their own copy.
	if n := o.getCount("index.html"); n != 1 {
		t.Fatalf("origin GETs for index.html = %d, want 1 coalesced fetch", n)
	}

	// Disabling the index document leaves the bare path to the origin.
	p2 := newTestProxy(t, srv.URL, func(c *Config) { c.IndexDocument = "" })
	if rec := get(p2, "http://proxy/", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("index disabled: code=%d, want 404", rec.Code)
	}
}

func TestNegativeMetadataCacheExpiresAndFindsNewObject(t *testing.T) {
	o := newTestOrigin()
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) {
		c.CacheNegativeTTL404 = 40 * time.Millisecond
	})

	for i := 0; i < 2; i++ {
		if rec := get(p, "http://proxy/created-later.txt", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("miss %d: code=%d, want 404", i, rec.Code)
		}
	}
	if n := o.headCount("created-later.txt"); n != 1 {
		t.Fatalf("HEADs during negative TTL=%d, want 1", n)
	}

	o.put("created-later.txt", "DATA", `"created"`)
	if rec := get(p, "http://proxy/created-later.txt", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("new object became visible before negative TTL: code=%d", rec.Code)
	}
	time.Sleep(50 * time.Millisecond)
	if rec := get(p, "http://proxy/created-later.txt", nil); rec.Code != http.StatusOK || rec.Body.String() != "DATA" {
		t.Fatalf("after TTL: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if n := o.headCount("created-later.txt"); n != 2 {
		t.Fatalf("HEADs after negative expiry=%d, want 2", n)
	}
}

func TestNegativeMetadataCachePreservesSPAAndCustom404(t *testing.T) {
	o := newTestOrigin()
	o.put("index.html", "SPA", `"index"`)
	o.put("404.html", "NOT-FOUND", `"404"`)
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) {
		c.SPAMode = true
		c.SPAIndex = "/index.html"
		c.ErrorPages = map[int]string{404: "/404.html"}
		c.CacheNegativeTTL404 = time.Minute
	})

	for i := 0; i < 2; i++ {
		rec := get(p, "http://proxy/app/route", map[string]string{"Accept": "text/html"})
		if rec.Code != http.StatusOK || rec.Body.String() != "SPA" {
			t.Fatalf("SPA request %d: code=%d body=%q", i, rec.Code, rec.Body.String())
		}
		rec = get(p, "http://proxy/missing.js", map[string]string{"Accept": "*/*"})
		if rec.Code != http.StatusNotFound || rec.Body.String() != "NOT-FOUND" {
			t.Fatalf("asset request %d: code=%d body=%q", i, rec.Code, rec.Body.String())
		}
	}
	if n := o.headCount("app/route"); n != 1 {
		t.Fatalf("SPA source HEADs=%d, want 1", n)
	}
	if n := o.headCount("missing.js"); n != 1 {
		t.Fatalf("missing asset HEADs=%d, want 1", n)
	}
}

func TestServerErrorsAreNotNegativeCached(t *testing.T) {
	var heads int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			mu.Lock()
			heads++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) {
		c.CacheNegativeTTL404 = time.Minute
		c.CacheNegativeTTL403 = time.Minute
	})
	for i := 0; i < 2; i++ {
		if rec := get(p, "http://proxy/unavailable", nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("request %d: code=%d", i, rec.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if heads != 2 {
		t.Fatalf("5xx HEADs=%d, want 2", heads)
	}
}

func TestForbiddenLookupIsNegativeCached(t *testing.T) {
	var heads int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			mu.Lock()
			heads++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	p := newTestProxy(t, srv.URL, func(c *Config) { c.CacheNegativeTTL403 = time.Minute })
	for i := 0; i < 2; i++ {
		if rec := get(p, "http://proxy/forbidden", nil); rec.Code != http.StatusForbidden {
			t.Fatalf("request %d: code=%d", i, rec.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if heads != 1 {
		t.Fatalf("403 HEADs=%d, want 1", heads)
	}
}

func TestObjectReplacedBetweenHeadAndGetIsNotCachedUnderOldVersion(t *testing.T) {
	o := newTestOrigin()
	o.put("page.html", "AAAAAAAA", `"v1"`) // 8 bytes, 4-byte segments
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)

	// A Range request caches only segment 0 and leaves fresh metadata behind, so
	// the next full GET still plans against v1 but has to go to the origin for the
	// rest of the body.
	if rec := get(p, "http://proxy/page.html", map[string]string{"Range": "bytes=0-3"}); rec.Body.String() != "AAAA" {
		t.Fatalf("warm: %q", rec.Body.String())
	}
	meta, err := p.cache.LoadMeta(cacheKeyFor("/page.html", "", "sort"))
	if err != nil || meta == nil {
		t.Fatalf("load meta: %v %v", meta, err)
	}
	oldVersion := meta.Version

	o.put("page.html", "BBBBBBBB", `"v2"`)

	rec := get(p, "http://proxy/page.html", nil)
	if rec.Body.String() != "AAAA" {
		t.Fatalf("body=%q, want only the cached old-version prefix", rec.Body.String())
	}
	if rec.Header().Get("X-Cache") != "MISS" {
		t.Fatal("expected MISS")
	}
	if p.cache.HasSegment(meta.CacheKey, oldVersion, 1) {
		t.Fatal("bytes from the new object were filed under the previous version")
	}

	// The stale metadata must have been expired, or every retry would keep planning
	// against v1 until the TTL ran out and hit the same mismatch.
	after, err := p.cache.LoadMeta(meta.CacheKey)
	if err != nil || after == nil {
		t.Fatalf("reload meta: %v %v", after, err)
	}
	if time.Now().Before(after.ExpiresAt) {
		t.Fatal("metadata was not invalidated after the version change")
	}
	if rec := get(p, "http://proxy/page.html", nil); rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("follow-up X-Cache=%q, want MISS once metadata is refreshed", rec.Header().Get("X-Cache"))
	}
}

func TestVanishedSegmentIsForgottenSoTheNextRequestRefetches(t *testing.T) {
	o := newTestOrigin()
	o.put("page.html", "AAAAAAAA", `"v1"`)
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)

	if rec := get(p, "http://proxy/page.html", nil); rec.Body.String() != "AAAAAAAA" {
		t.Fatalf("warm: %q", rec.Body.String())
	}
	meta, err := p.cache.LoadMeta(cacheKeyFor("/page.html", "", "sort"))
	if err != nil || meta == nil {
		t.Fatalf("load meta: %v %v", meta, err)
	}

	// Delete a segment behind the cache's back. The accounting still believes the
	// block is complete, so without self-healing every later request would keep
	// taking the HIT path and failing on the missing file.
	_, seg := p.cache.segmentLocation(meta.CacheKey, meta.Version, 1)
	if err := os.Remove(seg); err != nil {
		t.Fatal(err)
	}

	_ = get(p, "http://proxy/page.html", nil) // detects the gap and drops the block
	rec := get(p, "http://proxy/page.html", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "AAAAAAAA" {
		t.Fatalf("recovery: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestHealthEndpointIsAnsweredLocally(t *testing.T) {
	o := newTestOrigin()
	srv := httptest.NewServer(o.handler)
	defer srv.Close()
	p := newTestProxy(t, srv.URL, nil)

	rec := get(p, "http://proxy/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type=%q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) || !strings.Contains(rec.Body.String(), `"cache_max_bytes"`) {
		t.Fatalf("body=%q", rec.Body.String())
	}
	if n := o.getCount("healthz"); n != 0 {
		t.Fatalf("health check reached the origin %d times", n)
	}

	// Disabling it hands the path back to the bucket.
	p2 := newTestProxy(t, srv.URL, func(c *Config) { c.HealthPath = "" })
	if rec := get(p2, "http://proxy/healthz", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("health disabled: code=%d, want the origin's 404", rec.Code)
	}
}

func TestCatchAllCacheRuleAppliesToEveryPath(t *testing.T) {
	cfg := Config{
		CacheEnabled: true, CacheDefaultTTL: time.Hour, CacheMaxTTL: 24 * time.Hour,
		CacheRules: []CacheRule{
			{Prefix: "/assets/", TTL: "24h"},
			{TTL: "5m"}, // catch-all default for everything else
		},
	}
	if got := cachePolicyFor(cfg, "/assets/app.js", nil).TTL; got != 24*time.Hour {
		t.Fatalf("prefixed rule TTL=%s, want 24h", got)
	}
	if got := cachePolicyFor(cfg, "/anything/else", nil).TTL; got != 5*time.Minute {
		t.Fatalf("catch-all TTL=%s, want 5m", got)
	}

	// Bypass must survive a TTL set on the same rule.
	both := Config{CacheEnabled: true, CacheDefaultTTL: time.Hour, CacheMaxTTL: time.Hour,
		CacheRules: []CacheRule{{Prefix: "/private/", Bypass: true, TTL: "10m"}}}
	if cachePolicyFor(both, "/private/x", nil).Cacheable {
		t.Fatal("a rule that bypasses must not become cacheable through its own TTL")
	}
}

func TestLoadConfigRejectsMalformedRuleDuration(t *testing.T) {
	t.Setenv("CACHE_RULES_JSON", `[{"prefix":"/a/","ttl":"24hours"}]`)
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "ttl") {
		t.Fatalf("err=%v, want a CACHE_RULES_JSON ttl error", err)
	}
}

func TestLoadConfigValidatesOriginFetchAndNegativeTTLs(t *testing.T) {
	t.Setenv("CACHE_SEGMENT_SIZE", "2MiB")
	t.Setenv("ORIGIN_FETCH_MAX_SIZE", "1MiB")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "ORIGIN_FETCH_MAX_SIZE") {
		t.Fatalf("err=%v, want origin fetch size validation", err)
	}

	t.Setenv("ORIGIN_FETCH_MAX_SIZE", "16MiB")
	t.Setenv("CACHE_NEGATIVE_TTL_404", "later")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "CACHE_NEGATIVE_TTL_404") {
		t.Fatalf("err=%v, want negative TTL validation", err)
	}
}
