package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"mori/internal/backend"
	"mori/internal/objectcache"
)

type weakMemory struct{ *memBackend }

func (m *weakMemory) Stat(ctx context.Context, key string) (backend.Object, error) {
	st, err := m.memBackend.Stat(ctx, key)
	if err == nil && !st.Directory {
		st.ETag = backend.VersionTag("memory", key, st.Size, st.Modified)
	}
	return st, err
}

func TestFileHeadersAcrossModesCacheAndAuthentication(t *testing.T) {
	for _, mode := range []string{"browser", "spa", "direct"} {
		for _, cache := range []bool{false, true} {
			for _, auth := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, cache, auth), func(t *testing.T) {
					_, m, c := unifiedFixture(t, mode, cache)
					key := "docs/한글 +%#😀.pdf"
					m.files[key] = "abcdefghijklmnop"
					c.ObjectCache.CacheRules = []objectcache.CacheRule{{BrowserTTL: "2m"}}
					if auth {
						c.Username = "viewer"
						c.Password = "secret"
					}
					a, err := Open(c, &weakMemory{m})
					if err != nil {
						t.Fatal(err)
					}
					defer a.Shutdown()
					u := url.URL{Path: "/" + key}
					target := u.String()
					h := make(http.Header)
					if auth {
						h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("viewer:secret")))
					}
					// browser_ttl becomes max-age; mori's own TTL becomes
					// s-maxage, but only while it actually caches the body and
					// only for a deployment a shared cache may serve.
					wantCC := "public, max-age=120"
					if cache {
						wantCC += ", s-maxage=3600"
					}
					if auth {
						wantCC = "private, max-age=120"
						if mode == "browser" {
							wantCC = "private, no-store"
						}
					}
					var tag, disposition string
					for _, method := range []string{"GET", "GET", "HEAD"} {
						w := call(a, method, target, h)
						if w.Code != 200 || w.Header().Get("Cache-Control") != wantCC {
							t.Fatal(method, w.Code, w.Header())
						}
						kind, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
						if err != nil || kind != "inline" || params["filename"] != path.Base(key) || !strings.Contains(w.Header().Get("Content-Disposition"), "filename*=utf-8''") {
							t.Fatal(w.Header(), err)
						}
						if tag != "" && method == "GET" && cache && w.Header().Get("X-Cache") != "HIT" {
							t.Fatal("browser no-store disabled disk cache", w.Header())
						}
						tag = w.Header().Get("ETag")
						disposition = w.Header().Get("Content-Disposition")
						if !backend.SyntheticETag(tag) {
							t.Fatal(tag)
						}
						if method == "HEAD" && w.Body.Len() != 0 {
							t.Fatal("HEAD has body")
						}
					}
					h.Set("If-None-Match", tag)
					for _, method := range []string{"GET", "HEAD"} {
						w := call(a, method, target, h)
						if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != wantCC || w.Header().Get("Content-Disposition") != disposition {
							t.Fatal(method, w.Code, w.Header())
						}
					}
					h.Del("If-None-Match")
					h.Set("Range", "bytes=2-5")
					h.Set("If-Range", tag)
					if w := call(a, "GET", target, h); w.Code != 200 || w.Body.String() != m.files[key] {
						t.Fatal("weak If-Range accepted", w.Code)
					}
					h.Del("If-Range")
					if w := call(a, "GET", target, h); w.Code != 206 || w.Body.String() != "cdef" {
						t.Fatal(w.Code, w.Body.String())
					}
					h.Del("Range")
					h.Set("If-Match", tag)
					if w := call(a, "GET", target, h); w.Code != 412 {
						t.Fatal("weak If-Match accepted", w.Code)
					}
					if auth && call(a, "GET", target, nil).Code != 401 {
						t.Fatal("HIT bypassed authentication")
					}
				})
			}
		}
	}
}

func TestZIPAcceptsInternalWeakTokenButRejectsChangedMetadata(t *testing.T) {
	for _, cache := range []bool{false, true} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			_, m, c := unifiedFixture(t, "browser", cache)
			a, err := Open(c, &weakMemory{m})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Shutdown()
			link := prepareZIP(t, a, "docs/a.txt")
			if w := call(a, "GET", link, nil); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "PK") {
				t.Fatal(w.Code, w.Body.String())
			}
			link = prepareZIP(t, a, "docs/a.txt")
			m.modified = m.modified.Add(time.Second)
			if w := call(a, "GET", link, nil); w.Code != 409 {
				t.Fatal("changed weak token accepted", w.Code, w.Body.String())
			}
		})
	}
}

func TestMissingTimestampDoesNotCacheSyntheticVersion(t *testing.T) {
	_, m, c := unifiedFixture(t, "direct", true)
	m.modified = time.Time{}
	a, err := Open(c, &weakMemory{m})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown()
	first := call(a, "GET", "/docs/a.txt", nil)
	m.files["docs/a.txt"] = strings.Repeat("x", len(m.files["docs/a.txt"]))
	next := call(a, "GET", "/docs/a.txt", http.Header{"If-None-Match": {first.Header().Get("ETag")}})
	if next.Code != 200 || next.Body.String() != m.files["docs/a.txt"] || next.Header().Get("X-Cache") != "BYPASS" {
		t.Fatal(next.Code, next.Header(), next.Body.String())
	}
	if w := call(a, "GET", "/docs/a.txt", http.Header{"If-None-Match": {"*"}}); w.Code != 304 {
		t.Fatal("existence wildcard must not require a modification timestamp", w.Code)
	}
}

// A cache placed in front of mori can only work if mori says the response may
// be shared. s-maxage carries mori's own lifetime for the object, so the shared
// cache holds it exactly as long as mori would, and Age tells a cache that
// received the response late how much of that window is left.
func TestSharedCacheFreshness(t *testing.T) {
	fixture := func(t *testing.T, auth bool, control string, rules []objectcache.CacheRule) *App {
		t.Helper()
		_, m, c := unifiedFixture(t, "browser", true)
		c.ObjectCache.CacheRules = rules
		if auth {
			c.Username, c.Password = "viewer", "secret"
		}
		a, err := Open(c, &httpMemory{memBackend: m, control: control})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Shutdown)
		return a
	}
	credentials := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("viewer:secret"))}}
	hour := []objectcache.CacheRule{{BrowserTTL: "2m"}}

	// Public: the browser keeps it for browser_ttl, a shared cache for mori's own TTL.
	w := call(fixture(t, false, "", hour), "GET", "/docs/a.txt", nil)
	if got := w.Header().Get("Cache-Control"); got != "public, max-age=120, s-maxage=3600" {
		t.Fatal("public deployment is not shareable", got)
	}
	if w.Header().Get("Age") == "" {
		t.Fatal("no Age, so a shared cache cannot tell how much freshness is left")
	}

	// Basic auth: the response belongs to one visitor and is never stored.
	w = call(fixture(t, true, "", hour), "GET", "/docs/a.txt", credentials)
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatal("authenticated file was made cacheable", got)
	}

	// An error body belongs to the request that produced it, public or not.
	w = call(fixture(t, false, "", hour), "GET", "/missing.txt", nil)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatal("error page was made cacheable", got)
	}

	// Without a browser_ttl rule mori adds only what the origin left unsaid.
	w = call(fixture(t, false, "", nil), "GET", "/docs/a.txt", nil)
	if got := w.Header().Get("Cache-Control"); got != "public, s-maxage=3600" {
		t.Fatal(got)
	}
	// An origin that asked not to be shared is not overruled.
	for _, control := range []string{"no-store", "private, max-age=60", "no-cache", "public, s-maxage=30"} {
		w = call(fixture(t, false, control, nil), "GET", "/docs/a.txt", nil)
		if got := w.Header().Get("Cache-Control"); got != control {
			t.Errorf("origin said %q, mori sent %q", control, got)
		}
	}
}
