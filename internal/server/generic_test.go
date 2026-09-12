package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mori/internal/backend"
)

// memBackend is a non-S3 backend model: no ETags of its own, no ranges, no
// pagination. It exercises the generic object and ZIP paths.
type memBackend struct {
	files    map[string]string
	modified time.Time
	stats    atomic.Int32
	opens    atomic.Int32
}

func (m *memBackend) isDir(prefix string) bool {
	if prefix == "" {
		return true
	}
	for k := range m.files {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
func (m *memBackend) children(prefix string) (files, dirs []string) {
	seen := map[string]bool{}
	for k := range m.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		if i := strings.Index(rest, "/"); i >= 0 {
			if d := prefix + rest[:i+1]; !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		} else {
			files = append(files, k)
		}
	}
	sort.Strings(files)
	sort.Strings(dirs)
	return files, dirs
}
func (m *memBackend) List(ctx context.Context, prefix, cursor string) (backend.Listing, error) {
	if !m.isDir(prefix) {
		return backend.Listing{}, &backend.UpstreamError{Status: 404, Code: "NotFound"}
	}
	l := backend.Listing{Prefix: prefix, Entries: []backend.Entry{}}
	files, dirs := m.children(prefix)
	for _, d := range dirs {
		l.Entries = append(l.Entries, backend.Entry{Key: d, Name: strings.TrimSuffix(strings.TrimPrefix(d, prefix), "/"), Folder: true, Type: "folder"})
	}
	for _, f := range files {
		l.Entries = append(l.Entries, backend.Entry{Key: f, Name: strings.TrimPrefix(f, prefix), Size: int64(len(m.files[f])), ETag: strings.TrimPrefix(backend.VersionTag("fixture", f, int64(len(m.files[f])), m.modified), "W/"), Type: "text/plain"})
	}
	return l, nil
}
func (m *memBackend) Walk(ctx context.Context, prefix string, visit func(backend.Object) error) error {
	if !m.isDir(prefix) {
		return &backend.UpstreamError{Status: 404, Code: "NotFound"}
	}
	if err := visit(backend.Object{Key: prefix, Directory: true}); err != nil {
		return err
	}
	files, dirs := m.children(prefix)
	for _, f := range files {
		st, _ := m.Stat(ctx, f)
		if err := visit(st); err != nil {
			return err
		}
	}
	for _, d := range dirs {
		if err := m.Walk(ctx, d, visit); err != nil {
			return err
		}
	}
	return nil
}
func (m *memBackend) Stat(ctx context.Context, key string) (backend.Object, error) {
	m.stats.Add(1)
	if body, ok := m.files[key]; ok {
		return backend.Object{Key: key, Size: int64(len(body)), ETag: strings.TrimPrefix(backend.VersionTag("fixture", key, int64(len(body)), m.modified), "W/"), Modified: m.modified}, nil
	}
	if m.isDir(key) {
		return backend.Object{Key: key, Directory: true}, nil
	}
	return backend.Object{}, &backend.UpstreamError{Status: 404, Code: "NotFound"}
}
func (m *memBackend) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.opens.Add(1)
	body, ok := m.files[key]
	if !ok {
		return nil, &backend.UpstreamError{Status: 404, Code: "NotFound"}
	}
	if offset > int64(len(body)) {
		return nil, &backend.UpstreamError{Status: 416, Code: "InvalidRange"}
	}
	body = body[offset:]
	if length >= 0 && length < int64(len(body)) {
		body = body[:length]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

func genericApp(t *testing.T) (*App, *memBackend) {
	t.Helper()
	m := &memBackend{modified: time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC), files: map[string]string{
		"README.md": "0123456789abcdefghijklmnopqrstuvwxyz0123", "evil.html": "<script>1</script>",
		"docs/a.txt": "root", "docs/sub/x.txt": "x", "docs/sub/deep/한글 +&%.txt": "안녕", "docs/sub/zero.txt": "",
	}}
	c := testConfig()
	c.Backend = "webdav"
	return NewWithBackend(c, m), m
}

func TestGenericObjectRangeHEADAndConditional(t *testing.T) {
	a, m := genericApp(t)
	p := "/_mori/api/object?key=README.md"
	w := call(a, "GET", p, nil)
	if w.Code != 200 || w.Body.Len() != 40 || w.Header().Get("Accept-Ranges") != "bytes" || w.Header().Get("ETag") == "" || w.Header().Get("X-Cache") != "" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal(w.Code, w.Header())
	}
	etag := w.Header().Get("ETag")
	w = call(a, "GET", p, http.Header{"Range": {"bytes=0-15"}})
	if w.Code != 206 || w.Body.String() != "0123456789abcdef" || w.Header().Get("Content-Range") != "bytes 0-15/40" || w.Header().Get("Content-Length") != "16" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	w = call(a, "GET", p, http.Header{"Range": {"bytes=-4"}})
	if w.Code != 206 || w.Body.String() != "0123" || w.Header().Get("Content-Range") != "bytes 36-39/40" {
		t.Fatal("suffix range", w.Code, w.Body.String())
	}
	w = call(a, "GET", p, http.Header{"Range": {"bytes=30-"}})
	if w.Code != 206 || w.Body.String() != "uvwxyz0123" {
		t.Fatal("open range", w.Code, w.Body.String())
	}
	w = call(a, "GET", p, http.Header{"Range": {"bytes=0-15"}, "If-Range": {`"stale"`}})
	if w.Code != 200 || w.Body.Len() != 40 {
		t.Fatal("If-Range mismatch must serve the full file", w.Code)
	}
	opens := m.opens.Load()
	w = call(a, "HEAD", p, nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "40" || m.opens.Load() != opens {
		t.Fatal("HEAD response", w.Code, w.Header())
	}
	w = call(a, "GET", p, http.Header{"If-None-Match": {etag}})
	if w.Code != 304 || w.Body.Len() != 0 || m.opens.Load() != opens {
		t.Fatal("conditional response", w.Code)
	}
	w = call(a, "GET", p, http.Header{"If-Match": {`"other"`}})
	if w.Code != 409 {
		t.Fatal("If-Match mismatch", w.Code)
	}
	for _, bad := range []string{"bytes=999999999-", "bytes=5-2", "bytes=0-1,3-4", "items=0-1"} {
		w = call(a, "GET", p, http.Header{"Range": {bad}})
		if w.Code != 416 || w.Header().Get("Content-Range") != "bytes */40" {
			t.Fatal(bad, w.Code, w.Header())
		}
	}
	w = call(a, "GET", p+"&download=1", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(w.Code, w.Header())
	}
	w = call(a, "GET", "/_mori/api/object?key=evil.html", nil)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("unsafe content type", w.Header())
	}
	if w = call(a, "GET", "/_mori/api/object?key=missing.txt", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = call(a, "GET", "/_mori/api/object?key=docs", nil); w.Code != 404 {
		t.Fatal("directory served as file", w.Code)
	}
	if w = call(a, "GET", "/_mori/api/object?key=docs/sub/zero.txt", nil); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "0" {
		t.Fatal("empty file", w.Code, w.Header())
	}
}

func TestGenericListPreviewAndPresignedFallback(t *testing.T) {
	a, _ := genericApp(t)
	a.cfg.DownloadMode, a.cfg.PreviewMode = "presigned", "presigned"
	c := a.cfg
	a = NewWithBackend(c, a.store)
	var cfg map[string]any
	json.Unmarshal(call(a, "GET", "/_mori/api/config", nil).Body.Bytes(), &cfg)
	if cfg["backend"] != "webdav" || cfg["downloadMode"] != "proxy" || cfg["previewMode"] != "proxy" {
		t.Fatal("non-S3 backend must not advertise presigned delivery", cfg)
	}
	if strings.Contains(a.contentSecurityPolicy(), "http") {
		t.Fatal("CSP whitelisted a remote origin without S3", a.contentSecurityPolicy())
	}
	w := call(a, "GET", "/_mori/api/object?key=README.md&download=1", nil)
	if w.Code != 200 || w.Header().Get("X-Delivery-Mode") != "proxy" || w.Header().Get("Location") != "" {
		t.Fatal("presigned redirect without S3", w.Code, w.Header())
	}
	w = call(a, "GET", "/_mori/api/preview?key=README.md", nil)
	var preview map[string]any
	json.Unmarshal(w.Body.Bytes(), &preview)
	if w.Code != 200 || preview["mode"] != "proxy" || preview["url"] != "/_mori/api/object?key=README.md" || preview["kind"] != "text" {
		t.Fatal(w.Code, preview)
	}
	for _, want := range []string{"MISS", "HIT"} {
		w = call(a, "GET", "/_mori/api/list?prefix=docs/", nil)
		var l backend.Listing
		json.Unmarshal(w.Body.Bytes(), &l)
		if w.Code != 200 || len(l.Entries) != 2 || !l.Entries[0].Folder || l.Entries[0].Key != "docs/sub/" || l.Entries[1].Key != "docs/a.txt" || w.Header().Get("X-Listing-Cache") != want {
			t.Fatal(w.Code, w.Header().Get("X-Listing-Cache"), l)
		}
	}
	if w = call(a, "GET", "/_mori/api/list?prefix=nope/", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestGenericArchiveRecursiveAndVersionGuard(t *testing.T) {
	a, m := genericApp(t)
	link := prepareZIP(t, a, "docs/sub/", "docs/a.txt")
	if m.opens.Load() != 0 {
		t.Fatal("prepare read bodies")
	}
	got := inspectZIP(t, a, link)
	want := map[string]string{"a.txt": "root", "sub/": "", "sub/x.txt": "x", "sub/deep/": "", "sub/deep/한글 +&%.txt": "안녕", "sub/zero.txt": ""}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k, v := range want {
		if actual, ok := got[k]; !ok || actual != v {
			t.Fatal(k, actual, ok)
		}
	}
	if call(a, "GET", link, nil).Code != 410 {
		t.Fatal("ZIP ticket reused")
	}
	link = prepareZIP(t, a, "docs/a.txt")
	m.files["docs/a.txt"] = "changed"
	if w := call(a, "GET", link, nil); w.Code != 409 || strings.Contains(w.Body.String(), "PK") {
		t.Fatal("changed file accepted", w.Code)
	}
	m.files["docs/a.txt"] = "root"
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/missing/"]}`, nil); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	a.cfg.ZipMaxBytes = 3
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/a.txt"]}`, nil); w.Code != 413 {
		t.Fatal("size limit", w.Code)
	}
}
