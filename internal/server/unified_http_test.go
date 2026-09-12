package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mori/internal/backend"
	"mori/internal/objectcache"
)

type lateCheckBackend struct {
	*memBackend
	calls  atomic.Int32
	change bool
}

func (m *lateCheckBackend) Stat(ctx context.Context, key string) (backend.Object, error) {
	n := m.calls.Add(1)
	if n == 3 {
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return backend.Object{}, ctx.Err()
		}
	}
	st, err := m.memBackend.Stat(ctx, key)
	if m.change && n >= 3 {
		st.ETag = `"changed"`
		st.Modified = st.Modified.Add(time.Second)
	}
	return st, err
}

func TestUnifiedHTTPWaitsForFinalBackendValidation(t *testing.T) {
	for _, change := range []bool{false, true} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			_, m, c := unifiedFixture(t, "browser", true)
			c.ObjectCache.CacheBlockSize = 1024
			c.ObjectCache.CacheSegmentSize = 256
			c.ObjectCache.OriginFetchMaxSize = 1024
			store := &lateCheckBackend{memBackend: m, change: change}
			a, err := Open(c, store)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Shutdown()
			srv := httptest.NewUnstartedServer(a)
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
			resp, err := srv.Client().Get(srv.URL + "/docs/a.txt")
			if err != nil {
				t.Fatal(err)
			}
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if change {
				if readErr == nil || len(body) >= len(m.files["docs/a.txt"]) {
					t.Fatal("late error reported complete", readErr, string(body))
				}
			} else if readErr != nil || string(body) != m.files["docs/a.txt"] {
				t.Fatal(readErr, string(body))
			}
			resp, err = srv.Client().Get(srv.URL + "/docs/a.txt")
			if err != nil {
				t.Fatal(err)
			}
			body, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != m.files["docs/a.txt"] {
				t.Fatal("retry", err, string(body))
			}
			if !change && resp.Header.Get("X-Cache") != "HIT" {
				t.Fatal("client completion cancelled cache commit", resp.Header)
			}
			if change && resp.Header.Get("X-Cache") != "MISS" {
				t.Fatal("changed bytes retained in cache", resp.Header)
			}
		})
	}
}

type httpMemory struct {
	*memBackend
	ignoreRange bool
	failure     int
	control     string
	gets        atomic.Int32
}

func (m *httpMemory) Object(ctx context.Context, method, key string, h http.Header) (*http.Response, error) {
	r := httptest.NewRequest(method, "http://origin/"+key, nil).WithContext(ctx)
	r.Header = h.Clone()
	if m.ignoreRange {
		r.Header.Del("Range")
	}
	w := httptest.NewRecorder()
	if m.failure != 0 {
		w.WriteHeader(m.failure)
		return w.Result(), nil
	}
	st, err := m.Stat(ctx, key)
	if err != nil {
		w.WriteHeader(404)
		return w.Result(), nil
	}
	if method == "GET" {
		m.gets.Add(1)
	}
	w.Header().Set("ETag", st.ETag)
	w.Header().Set("Cache-Control", m.control)
	w.Header().Set("Content-Disposition", `inline; filename="origin.txt"`)
	w.Header().Set("Set-Cookie", "must-not-escape=1")
	http.ServeContent(w, r, key, m.modified, strings.NewReader(m.files[key]))
	return w.Result(), nil
}

func TestUnifiedHTTPRangeUnsupportedAndHeaders(t *testing.T) {
	_, m, c := unifiedFixture(t, "direct", true)
	native := &httpMemory{memBackend: m, ignoreRange: true, control: "max-age=3600"}
	a, err := Open(c, native)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	w := call(a, "GET", "/docs/a.txt", nil)
	if w.Code != 200 || w.Body.String() != m.files["docs/a.txt"] || w.Header().Get("X-Cache") != "BYPASS" || native.gets.Load() != 2 {
		t.Fatal(w.Code, w.Header(), w.Body.String(), native.gets.Load())
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("origin cookie escaped")
	}
	w = call(a, "GET", "/docs/a.txt", http.Header{"Range": {"bytes=8-17"}})
	if w.Code != 206 || w.Body.String() != m.files["docs/a.txt"][8:18] {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}

func TestUnifiedHTTPStaleRangeAndBypassHEAD(t *testing.T) {
	_, m, c := unifiedFixture(t, "direct", true)
	c.ObjectCache.CacheDefaultTTL = 0
	native := &httpMemory{memBackend: m, control: "no-cache"}
	a, err := Open(c, native)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	first := call(a, "GET", "/docs/a.txt", nil)
	if first.Code != 200 {
		t.Fatal(first.Code)
	}
	native.failure = 503
	w := call(a, "GET", "/docs/a.txt", http.Header{"Range": {"bytes=3-8"}})
	if w.Code != 206 || w.Body.String() != "345678" || w.Header().Get("X-Cache") != "STALE" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	if w = call(a, "GET", "/docs/a.txt", http.Header{"If-Match": {"\"wrong\""}}); w.Code != 412 {
		t.Fatal(w.Code)
	}
	native.failure = 403
	if w = call(a, "GET", "/docs/a.txt", nil); w.Code != 403 {
		t.Fatal("403 served stale", w.Code)
	}
	native.failure = 0
	native.control = "no-store"
	c.ObjectCache.CacheEnabled = false
	c.CacheMode = "off"
	b, err := Open(c, native)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()
	gets := native.gets.Load()
	w = call(b, "HEAD", "/docs/a.txt", nil)
	if w.Code != 200 || w.Body.Len() != 0 || native.gets.Load() != gets || w.Header().Get("Content-Length") != fmt.Sprint(len(m.files["docs/a.txt"])) {
		t.Fatal(w.Code, w.Header(), native.gets.Load(), gets)
	}
}

func TestUnifiedRulesFirstMatchAndZeroBrowserTTL(t *testing.T) {
	_, m, c := unifiedFixture(t, "direct", true)
	c.ObjectCache.CacheRules = []objectcache.CacheRule{{Prefix: "/docs/", TTL: "1h", BrowserTTL: "0s"}, {IgnoreQuery: true}}
	native := &httpMemory{memBackend: m, control: "private, no-store"}
	a, err := Open(c, native)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	for _, query := range []string{"?b=2&a=1", "?a=1&b=2"} {
		w := call(a, "GET", "/docs/a.txt"+query, nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=0" || w.Header().Get("Set-Cookie") != "" {
			t.Fatal(w.Code, w.Header())
		}
	}
	gets := native.gets.Load()
	w := call(a, "GET", "/docs/a.txt?b=2&a=1", nil)
	if w.Header().Get("X-Cache") != "HIT" || native.gets.Load() != gets {
		t.Fatal(w.Header(), native.gets.Load(), gets)
	}
	w = call(a, "GET", "/docs/a.txt?changed=1", nil)
	if w.Header().Get("X-Cache") != "MISS" || native.gets.Load() <= gets {
		t.Fatal("later ignore_query rule overrode first match", w.Header())
	}
}
