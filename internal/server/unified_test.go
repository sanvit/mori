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

// A file path serves the same body as the object API and may ask for an
// attachment, without caching that body twice or letting a fallback page be
// labelled as the object that was missing.
func TestFilePathDownloadFlagAndFallbackPresentation(t *testing.T) {
	a, m, _ := unifiedFixture(t, "browser", true)
	first := call(a, "GET", "/docs/a.txt", nil)
	if first.Code != 200 || first.Header().Get("X-Cache") != "MISS" || first.Header().Get("Content-Disposition") != "inline; filename=a.txt" {
		t.Fatal(first.Code, first.Header())
	}
	opens := m.opens.Load()

	attachment := call(a, "GET", "/docs/a.txt?download=1", nil)
	if attachment.Code != 200 || attachment.Body.String() != m.files["docs/a.txt"] {
		t.Fatal(attachment.Code, attachment.Body.String())
	}
	if !strings.HasPrefix(attachment.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal("download flag ignored on a file path", attachment.Header().Get("Content-Disposition"))
	}
	// The flag must not reach the cache key: the body is already stored.
	if attachment.Header().Get("X-Cache") != "HIT" || m.opens.Load() != opens {
		t.Fatal("download flag cached the body again", attachment.Header().Get("X-Cache"), m.opens.Load(), opens)
	}
	if plain := call(a, "GET", "/docs/a.txt", nil); plain.Header().Get("Content-Disposition") != "inline; filename=a.txt" {
		t.Fatal("flag leaked into a later request", plain.Header().Get("Content-Disposition"))
	}

	// The 404 body is 404.html, so it must not be typed or named as the file
	// that was requested and could not be found.
	missing := call(a, "GET", "/missing.txt", nil)
	if missing.Code != 404 || missing.Body.String() != m.files["404.html"] {
		t.Fatal(missing.Code, missing.Body.String())
	}
	if ct := missing.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatal("error page served as the missing file's type", ct)
	}
	if cd := missing.Header().Get("Content-Disposition"); strings.Contains(cd, "missing.txt") {
		t.Fatal("error page named after the missing file", cd)
	}
	if csp := missing.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatal("error page is not sandboxed", csp)
	}
}

// The file path and the object API are two ways to read the same object, so a
// change to one must not quietly give the other different headers. X-Cache is
// the only expected difference: the second read is served from the cache.
func TestFilePathAndObjectAPIAgreeOnHeaders(t *testing.T) {
	for _, tc := range []struct{ name, file, api string }{
		{"inline", "/docs/a.txt", "/_mori/api/object?key=docs/a.txt"},
		{"download", "/docs/a.txt?download=1", "/_mori/api/object?key=docs/a.txt&download=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := unifiedFixture(t, "browser", true)
			file, api := call(a, "GET", tc.file, nil), call(a, "GET", tc.api, nil)
			if file.Code != 200 || api.Code != 200 || file.Body.String() != api.Body.String() {
				t.Fatal(file.Code, api.Code)
			}
			names := map[string]bool{}
			for k := range file.Header() {
				names[k] = true
			}
			for k := range api.Header() {
				names[k] = true
			}
			for k := range names {
				if k == "X-Cache" {
					continue
				}
				if got, want := file.Header().Get(k), api.Header().Get(k); got != want {
					t.Errorf("%s: file path %q, object API %q", k, got, want)
				}
			}
		})
	}
}

// The listing is complete HTML before any script runs, so a terminal, a
// crawler or an agent sees the same folder a person does and can walk it by
// following links alone.
func TestListingIsUsableWithoutScripting(t *testing.T) {
	a, m, _ := unifiedFixture(t, "browser", true)
	m.files["docs/한글 +&.txt"] = "hi"

	root := call(a, "GET", "/", nil)
	if root.Code != 200 {
		t.Fatal(root.Code)
	}
	body := root.Body.String()
	for _, want := range []string{`href="/docs/"`, `href="/a..b"`, `href="/a..b?download=1"`, `<title>Test</title>`} {
		if !strings.Contains(body, want) {
			t.Errorf("root listing is missing %s", want)
		}
	}
	if strings.Contains(body, "#/") {
		t.Error("listing still routes folders through the fragment")
	}

	// Following the folder link alone reaches the folder, with a way back up.
	docs := call(a, "GET", "/docs/", nil)
	if docs.Code != 200 {
		t.Fatal(docs.Code)
	}
	body = docs.Body.String()
	for _, want := range []string{`href="/docs/a.txt"`, `href="/"`, `<title>/docs/ · Test</title>`, "0개 폴더 · 3개 파일"} {
		if !strings.Contains(body, want) {
			t.Errorf("folder listing is missing %s", want)
		}
	}
	// A name needing escaping survives the round trip through its own link.
	escaped := objectPath("docs/한글 +&.txt", false)
	if !strings.Contains(body, `href="`+escaped+`"`) {
		t.Fatalf("escaped name is not linked as %s", escaped)
	}
	if got := call(a, "GET", escaped, nil); got.Code != 200 || got.Body.String() != "hi" {
		t.Fatal("linked path does not resolve", got.Code, got.Body.String())
	}

	// Column headers order the listing without scripting.
	desc := call(a, "GET", "/docs/?sort=size&dir=desc", nil)
	if desc.Code != 200 {
		t.Fatal(desc.Code)
	}
	first := strings.Index(desc.Body.String(), `data-key="docs/a.txt"`)
	second := strings.Index(desc.Body.String(), `data-key="docs/index.html"`)
	if first < 0 || second < 0 || first > second {
		t.Error("size ordering was not applied server-side")
	}

	// A folder is not reachable as an object, and a file is not a folder.
	if got := call(a, "GET", "/docs", nil); got.Code == 200 {
		t.Error("folder served as an object body")
	}
	if got := call(a, "GET", "/docs/a.txt/", nil); got.Code == 200 {
		t.Error("file served as a folder listing")
	}
}
