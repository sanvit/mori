package objectcache

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type freshReadKey struct{}
type expectedReadKey struct{}

// Keep one byte pending until EOF validates the source. This preserves first
// byte streaming while preventing a late source error looking like a complete
// Content-Length response to the client.
func copyVerified(w io.Writer, r io.Reader) error {
	buf := make([]byte, 128*1024+1)
	pending := 0
	for {
		n, err := r.Read(buf[pending:])
		n += pending
		count := n
		if count > 0 && err != io.EOF {
			count--
		}
		if count > 0 {
			written, e := w.Write(buf[:count])
			if e != nil {
				return e
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		pending = n - count
		if pending > 0 {
			buf[0] = buf[count]
		}
	}
}

func validObjectPath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "//") || len(p) > 1025 {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." {
			return false
		}
	}
	for _, r := range p {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func fallbackRequest(r *http.Request) *http.Request {
	c := r.Clone(r.Context())
	for _, h := range []string{"Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since"} {
		c.Header.Del(h)
	}
	c.URL.RawQuery = ""
	return c
}

func clientPreconditions(r *http.Request, m *ObjectMeta) bool {
	if value := r.Header.Get("If-Match"); value != "" {
		matched := false
		for _, tag := range strings.Split(value, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || (m.ETag != "" && !strings.HasPrefix(tag, "W/") && !strings.HasPrefix(m.ETag, "W/") && tag == m.ETag) {
				matched = true
			}
		}
		return matched
	}
	if value := r.Header.Get("If-Unmodified-Since"); value != "" && !m.LastModified.IsZero() {
		if t, err := http.ParseTime(value); err == nil && m.LastModified.Truncate(time.Second).After(t) {
			return false
		}
	}
	return true
}

func rangeRequest(r *http.Request, m *ObjectMeta) *http.Request {
	value := r.Header.Get("If-Range")
	if value == "" || r.Header.Get("Range") == "" {
		return r
	}
	match := !strings.HasPrefix(value, "W/") && m.ETag != "" && value == m.ETag && !strings.HasPrefix(m.ETag, "W/")
	if t, err := http.ParseTime(value); err == nil && !m.LastModified.IsZero() {
		match = !strings.HasPrefix(m.ETag, "W/") && m.LastModified.Truncate(time.Second).Equal(t)
	}
	if match {
		return r
	}
	c := r.Clone(r.Context())
	c.Header.Del("Range")
	c.Header.Del("If-Range")
	return c
}

func metadataHeaders(m *ObjectMeta) http.Header {
	h := make(http.Header)
	h.Set("Content-Length", fmt.Sprint(m.Size))
	h.Set("ETag", m.ETag)
	h.Set("Content-Type", m.ContentType)
	h.Set("Content-Encoding", m.ContentEncoding)
	h.Set("Content-Disposition", m.ContentDisposition)
	h.Set("Cache-Control", m.CacheControl)
	h.Set("Expires", m.ExpiresHeader)
	if !m.LastModified.IsZero() {
		h.Set("Last-Modified", m.LastModified.UTC().Format(http.TimeFormat))
	}
	return h
}

func (p *Proxy) serveUncached(w http.ResponseWriter, r *http.Request, path, key string, forced int) (int, error) {
	resp, err := p.origin.Head(r.Context(), originKey(path), "")
	if err != nil {
		if isTimeout(err) {
			return 504, err
		}
		return 502, err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("origin HEAD returned %d", resp.StatusCode)
	}
	m := metaFromHead(p.cfg, path, key, resp.Header, time.Now())
	m.Cacheable = false
	return p.serveKnown(w, r, m, forced)
}

// ServeObject reads exactly key, sharing the site cache but never applying
// index/SPA/error-page fallbacks. Callers retain control of error presentation.
func (p *Proxy) ServeObject(w http.ResponseWriter, r *http.Request, key string) (int, error) {
	path := "/" + key
	if !validObjectPath(path) || key == "" {
		return 400, fmt.Errorf("invalid object key")
	}
	return p.serveObject(w, r, path, cacheKeyFor(path, r.URL.RawQuery, p.queryModeFor(path)), 0, false)
}

func (p *Proxy) Stats() (used, max, blocks int64) {
	if p.cache == nil {
		return 0, 0, 0
	}
	return p.cache.Stats()
}

func (p *Proxy) Health(w http.ResponseWriter, r *http.Request) { p.serveHealth(w, r) }

// Cancel stops all active shared fills after the HTTP shutdown deadline.
func (p *Proxy) Cancel() {
	p.fills.mu.Lock()
	for _, f := range p.fills.active {
		f.run.cancel()
	}
	p.fills.mu.Unlock()
}

// Response bridges streaming object reads to ZIP's io.Reader contract. The
// pipe has no body buffer; closing it cancels the producer and origin fills.
func (p *Proxy) Response(ctx context.Context, method, key string, h http.Header, fresh bool) (*http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	if fresh {
		ctx = context.WithValue(ctx, freshReadKey{}, true)
	}
	r := (&http.Request{Method: method, URL: &url.URL{Path: "/" + key}, Header: h.Clone()}).WithContext(ctx)
	if r.Header == nil {
		r.Header = make(http.Header)
	}
	// ZIP's metadata token comparison is internal, not HTTP strong If-Match.
	// This preserves weak-validator backends without accepting weak client
	// If-Match or forwarding a synthetic token to an HTTP origin.
	if fresh && r.Header.Get("If-Match") != "" {
		r = r.WithContext(context.WithValue(r.Context(), expectedReadKey{}, r.Header.Get("If-Match")))
		r.Header.Del("If-Match")
	}
	reader, writer := io.Pipe()
	ready := make(chan *http.Response, 1)
	w := &streamResponse{header: make(http.Header), writer: writer, ready: ready}
	done := make(chan struct{})
	go func() {
		defer close(done)
		status, err := p.ServeObject(w, r, key)
		if !w.started {
			if status == 0 {
				status = 502
			}
			w.WriteHeader(status)
		}
		writer.CloseWithError(err)
	}()
	select {
	case resp := <-ready:
		resp.Body = &responseBody{PipeReader: reader, cancel: cancel, done: done}
		return resp, nil
	case <-ctx.Done():
		reader.CloseWithError(ctx.Err())
		cancel()
		<-done
		return nil, ctx.Err()
	}
}

type streamResponse struct {
	header  http.Header
	writer  *io.PipeWriter
	ready   chan *http.Response
	started bool
}

func (w *streamResponse) Header() http.Header { return w.header }
func (w *streamResponse) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started = true
	w.ready <- &http.Response{StatusCode: status, Header: w.header.Clone()}
}
func (w *streamResponse) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	return w.writer.Write(b)
}
func (w *streamResponse) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
}

type responseBody struct {
	*io.PipeReader
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (b *responseBody) Close() error {
	b.once.Do(func() { b.cancel(); b.PipeReader.Close(); <-b.done })
	return nil
}
