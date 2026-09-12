package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"mori/internal/config"
	"mori/internal/objectcache"
)

func unifiedFixture(t *testing.T, mode string, enabled bool) (*App, *memBackend, config.Config) {
	t.Helper()
	m := &memBackend{modified: time.Now().Truncate(time.Second), files: map[string]string{
		"docs/a.txt": "0123456789abcdefghijklmnopqrstuvwxyz", "index.html": "<h1>SITE</h1>",
		"404.html": "<h1>MISSING</h1>", "empty.txt": "", "docs/index.html": "<h1>DOCS</h1>",
		"a..b": "dots", "api/object": "user object", "app.js": "user script",
	}}
	c := testConfig()
	c.Backend = "ftp"
	c.ServeMode = mode
	c.CacheMode = "internal"
	if !enabled {
		c.CacheMode = "off"
	}
	c.ObjectCache = objectcache.Config{CacheEnabled: enabled, CacheDir: t.TempDir(), CacheMaxDiskSize: 1024, CacheBlockSize: 16, CacheSegmentSize: 4, CacheDownloadConcurrency: 4, OriginFetchMaxSize: 16, CacheDefaultTTL: time.Hour, CacheMaxTTL: time.Hour, CacheRespectOrigin: true, CacheStaleIfError: time.Hour, CacheNegativeTTL404: time.Minute, CacheNegativeTTL403: time.Minute, CacheQueryMode: "sort", SPAMode: mode == "spa", SPAIndex: "/index.html", ErrorPages: map[int]string{404: "/404.html"}}
	if mode != "browser" {
		c.ObjectCache.IndexDocument = "index.html"
	}
	a, err := Open(c, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Shutdown)
	return a, m, c
}

func TestUnifiedModesAndExactObjectEscape(t *testing.T) {
	for _, mode := range []string{"browser", "spa", "direct"} {
		for _, enabled := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "/cache", false: "/off"}[enabled], func(t *testing.T) {
				a, m, _ := unifiedFixture(t, mode, enabled)
				root := call(a, "GET", "/", nil)
				if root.Code != 200 || (mode != "browser" && root.Body.String() != "<h1>SITE</h1>") || (mode == "browser" && !strings.Contains(root.Body.String(), "/_mori/assets/app.js")) {
					t.Fatal(root.Code, root.Body.String())
				}
				for _, path := range []string{"/docs/a.txt", "/a..b", "/api/object", "/app.js"} {
					w := call(a, "GET", path, nil)
					if w.Code != 200 || w.Body.String() != m.files[strings.TrimPrefix(path, "/")] {
						t.Fatal(path, w.Code, w.Body.String())
					}
				}
				w := call(a, "GET", "/missing", http.Header{"Accept": {"text/html"}, "Range": {"bytes=9-"}})
				want := 404
				body := "<h1>MISSING</h1>"
				if mode == "spa" {
					want = 200
					body = "<h1>SITE</h1>"
				}
				if w.Code != want || w.Body.String() != body || w.Header().Get("Content-Range") != "" {
					t.Fatal(w.Code, w.Header(), w.Body.String())
				}
				if w = call(a, "GET", "/_mori/api/object?key=missing", nil); w.Code != 404 || strings.Contains(w.Body.String(), "<h1>") {
					t.Fatal("exact API used fallback", w.Code, w.Body.String())
				}
				if w = call(a, "GET", "/missing.js", http.Header{"Accept": {"text/html"}}); w.Code != 404 {
					t.Fatal("SPA captured asset", w.Code)
				}
				if w = call(a, "GET", "/_mori/api/config", nil); (mode == "browser" && w.Code != 200) || (mode != "browser" && w.Code != 404) {
					t.Fatal(w.Code)
				}
				if w = call(a, "HEAD", "/missing.js", nil); w.Code != 404 || w.Body.Len() != 0 {
					t.Fatal("HEAD error body", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestUnifiedSharedCacheAuthRangeAndRestart(t *testing.T) {
	a, m, c := unifiedFixture(t, "browser", true)
	w := call(a, "GET", "/docs/a.txt", nil)
	if w.Header().Get("X-Cache") != "MISS" {
		t.Fatal(w.Header())
	}
	opens := m.opens.Load()
	w = call(a, "GET", "/_mori/api/object?key=docs/a.txt&download=1", nil)
	if w.Header().Get("X-Cache") != "HIT" || m.opens.Load() != opens || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Header(), m.opens.Load(), opens)
	}
	w = call(a, "GET", "/docs/a.txt", http.Header{"Range": {"bytes=5-12"}})
	if w.Code != 206 || w.Body.String() != "56789abc" || w.Header().Get("X-Cache") != "RANGE-HIT" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	for _, headers := range []http.Header{{"If-None-Match": {"W/" + w.Header().Get("Etag")}}, {"If-None-Match": {"*"}}} {
		if got := call(a, "GET", "/docs/a.txt", headers); got.Code != 304 {
			t.Fatal(got.Code)
		}
	}
	if got := call(a, "GET", "/docs/a.txt", http.Header{"If-Match": {"\"wrong\""}}); got.Code != 412 {
		t.Fatal(got.Code)
	}
	if got := call(a, "GET", "/docs/a.txt", http.Header{"Range": {"bytes=5-12"}, "If-Range": {"\"wrong\""}}); got.Code != 200 || got.Body.String() != m.files["docs/a.txt"] {
		t.Fatal(got.Code, got.Body.String())
	}
	if got := call(a, "GET", "/empty.txt", nil); got.Code != 200 || got.Body.Len() != 0 {
		t.Fatal(got.Code, got.Body.String())
	}
	a.cfg.Username = "tester"
	a.cfg.Password = "secret"
	if got := call(a, "GET", "/docs/a.txt", nil); got.Code != 401 {
		t.Fatal("cache bypassed auth", got.Code)
	}
	a.Shutdown()
	m.files["docs/a.txt"] = "changed object after restart"
	b, err := Open(c, m)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Shutdown()
	got := call(b, "GET", "/docs/a.txt", nil)
	if got.Code != 200 || got.Body.String() != m.files["docs/a.txt"] {
		t.Fatal("restart served stale", got.Code, got.Body.String())
	}
}

func TestUnifiedZIPUsesCacheAndFreshValidators(t *testing.T) {
	a, m, _ := unifiedFixture(t, "browser", true)
	call(a, "GET", "/docs/a.txt", nil)
	opens := m.opens.Load()
	link := prepareZIP(t, a, "docs/a.txt")
	w := call(a, "GET", link, nil)
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err, w.Code, w.Body.String())
	}
	r, err := z.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(body) != m.files["docs/a.txt"] || m.opens.Load() != opens {
		t.Fatal(err, string(body), m.opens.Load(), opens)
	}
	link = prepareZIP(t, a, "docs/a.txt")
	m.files["docs/a.txt"] = "changed after planning"
	if got := call(a, "GET", link, nil); got.Code != 409 {
		t.Fatal("ZIP reused stale body", got.Code, got.Body.String())
	}
	var stats map[string]any
	health := call(a, "GET", "/_mori/healthz", nil)
	if json.Unmarshal(health.Body.Bytes(), &stats) != nil || stats["status"] != "ok" {
		t.Fatal(health.Body.String())
	}
}
