package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mori/internal/backend"
	"mori/internal/s3"
)

type recursiveStats struct{ Lists, Heads, Gets atomic.Int32 }

// Contract model: delimiter grouping happens BEFORE pagination. Without a
// delimiter, every descendant is returned. Only test code creates these keys.
func recursiveFixture(t *testing.T, data map[string]string, pageSize int) (*App, *recursiveStats) {
	t.Helper()
	stats := new(recursiveStats)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("list-type") == "2" {
			stats.Lists.Add(1)
			prefix := q.Get("prefix")
			if !strings.HasPrefix(prefix, "public/docs/") || q.Get("max-keys") != "1000" || q.Get("encoding-type") != "url" || r.URL.Path != "/test-bucket/" {
				t.Error("listing escaped requested scope", r.URL)
			}
			grouped := map[string]bool{}
			for key := range data {
				full := "public/" + key
				if !strings.HasPrefix(full, prefix) {
					continue
				}
				rest := strings.TrimPrefix(full, prefix)
				if idx := strings.Index(rest, "/"); q.Get("delimiter") == "/" && idx >= 0 {
					grouped[prefix+rest[:idx+1]] = true
				} else {
					grouped[full] = false
				}
			}
			names := []string{}
			for key := range grouped {
				names = append(names, key)
			}
			sort.Strings(names)
			start, _ := strconv.Atoi(q.Get("continuation-token"))
			if start > len(names) {
				t.Error("bad continuation token")
				return
			}
			end := min(start+pageSize, len(names))
			io.WriteString(w, "<ListBucketResult><EncodingType>url</EncodingType>")
			for _, name := range names[start:end] {
				encoded := strings.ReplaceAll(url.QueryEscape(name), "+", "%20")
				if grouped[name] {
					fmt.Fprintf(w, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", encoded)
				} else {
					body := data[strings.TrimPrefix(name, "public/")]
					fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size><ETag>"%x"</ETag><LastModified>2026-09-09T06:00:00Z</LastModified></Contents>`, encoded, len(body), sha256.Sum256([]byte(body)))
				}
			}
			if end < len(names) {
				fmt.Fprintf(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>%d</NextContinuationToken>", end)
			}
			io.WriteString(w, "</ListBucketResult>")
			return
		}
		if r.Method == "HEAD" {
			stats.Heads.Add(1)
		} else {
			stats.Gets.Add(1)
		}
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/public/")
		body, ok := data[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body)))
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method == "HEAD" {
			return
		}
		if r.Header.Get("If-Match") != etag {
			t.Error("missing ZIP version guard", r.Header)
			w.WriteHeader(412)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	c := testConfig()
	c.Prefix = "public/"
	c.Endpoint, _ = url.Parse(s.URL)
	return New(c), stats
}
func inspectZIP(t *testing.T, a *App, link string) map[string]string {
	t.Helper()
	w := call(a, "GET", link, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		if f.Method != zip.Store {
			t.Fatal("compressed", f.Name)
		}
		if _, ok := out[f.Name]; ok {
			t.Fatal("duplicate", f.Name)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal("CRC/read", err)
		}
		out[f.Name] = string(body)
		if strings.HasSuffix(f.Name, "/") && !f.FileInfo().IsDir() {
			t.Fatal("directory mode", f.Name)
		}
	}
	return out
}
func TestRecursiveZIPHierarchyEmptyFoldersAndScopes(t *testing.T) {
	data := map[string]string{
		"docs/a.txt": "root", "docs/sub/x.txt": "x", "docs/sub/deep/한글 +&%.txt": "안녕",
		"docs/sub/empty/": "", "docs/sub/zero.txt": "", "docs/sub-other/excluded.txt": "no",
		"elsewhere/leak.txt": "secret",
	}
	a, stats := recursiveFixture(t, data, 2)
	link := prepareZIP(t, a, "docs/sub/", "docs/a.txt", "docs/sub/")
	if stats.Lists.Load() != 2 || stats.Heads.Load() != 1 || stats.Gets.Load() != 0 {
		t.Fatal("prepare fetched bodies or relisted duplicate roots", stats)
	}
	got := inspectZIP(t, a, link)
	want := map[string]string{"a.txt": "root", "sub/x.txt": "x", "sub/deep/한글 +&%.txt": "안녕", "sub/empty/": "", "sub/zero.txt": ""}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for k, v := range want {
		if actual, ok := got[k]; !ok || actual != v {
			t.Fatal(k, actual, ok)
		}
	}
	if stats.Gets.Load() != 4 {
		t.Fatal("GET used for folder marker or missing file", stats.Gets.Load())
	}
}
func TestRecursiveZIPOver1000AndOneLevelListingIndependent(t *testing.T) {
	data := map[string]string{"docs/a-first.txt": "a", "docs/z-last.txt": "z"}
	for i := 0; i < 1005; i++ {
		data[fmt.Sprintf("docs/sub/deep/f-%04d.txt", i)] = "x"
	}
	a, stats := recursiveFixture(t, data, 1000)
	a.cfg.ZipMaxFiles = 2000
	a.s3 = s3.New(a.cfg)
	a.store = a.s3
	l, err := a.s3.List(context.Background(), "docs/", "")
	if err != nil || len(l.Entries) != 3 || l.Cursor != "" || stats.Lists.Load() != 1 {
		t.Fatal(l, err, stats.Lists.Load())
	}
	link := prepareZIP(t, a, "docs/sub/")
	if stats.Lists.Load() != 3 || stats.Heads.Load() != 0 || stats.Gets.Load() != 0 {
		t.Fatal("recursive listing did not paginate independently", stats)
	}
	got := inspectZIP(t, a, link)
	if len(got) != 1005 || got["sub/deep/f-1004.txt"] != "x" || stats.Gets.Load() != 1005 {
		t.Fatal("truncated recursive ZIP", len(got), stats.Gets.Load())
	}
}
func TestRecursiveZIPLimitsBeforeAnyBody(t *testing.T) {
	data := map[string]string{"docs/sub/a": "123", "docs/sub/b": "456", "docs/sub/c": "7"}
	for _, mode := range []string{"files", "size"} {
		t.Run(mode, func(t *testing.T) {
			a, stats := recursiveFixture(t, data, 2)
			if mode == "files" {
				a.cfg.ZipMaxFiles = 2
			} else {
				a.cfg.ZipMaxBytes = 5
			}
			a.s3 = s3.New(a.cfg)
			a.store = a.s3
			w := postArchive(a, `{"prefix":"docs/","keys":["docs/sub/"]}`, nil)
			if w.Code != 413 || stats.Gets.Load() != 0 || stats.Heads.Load() != 0 || len(a.archives.plans) != 0 {
				t.Fatal(w.Code, w.Body.String(), stats)
			}
		})
	}
}
func TestRecursiveZIPUnsafeMissingAndConflictingPaths(t *testing.T) {
	cases := []struct {
		name   string
		data   map[string]string
		keys   []string
		status int
	}{
		{"traversal", map[string]string{"docs/sub/../escape": "x"}, []string{"docs/sub/"}, 400},
		{"backslash", map[string]string{"docs/sub/a\\b": "x"}, []string{"docs/sub/"}, 400},
		{"colon", map[string]string{"docs/sub/C:stream": "x"}, []string{"docs/sub/"}, 400},
		{"slashslash", map[string]string{"docs/sub//x": "x"}, []string{"docs/sub/"}, 400},
		{"nonempty-marker", map[string]string{"docs/sub/": "body"}, []string{"docs/sub/"}, 400},
		{"missing", map[string]string{}, []string{"docs/sub/"}, 404},
		{"nested-file-dir", map[string]string{"docs/sub/a": "x", "docs/sub/a/b": "y"}, []string{"docs/sub/"}, 409},
		{"root-file-dir", map[string]string{"docs/sub": "x", "docs/sub/a": "y"}, []string{"docs/sub", "docs/sub/"}, 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, stats := recursiveFixture(t, c.data, 1000)
			b, _ := json.Marshal(map[string]any{"prefix": "docs/", "keys": c.keys})
			w := postArchive(a, string(b), nil)
			if w.Code != c.status || stats.Gets.Load() != 0 || len(a.archives.plans) != 0 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestRecursiveZIPDirectoryOnlyAndFolderMarkerLimit(t *testing.T) {
	a, stats := recursiveFixture(t, map[string]string{"docs/empty/": ""}, 1000)
	got := inspectZIP(t, a, prepareZIP(t, a, "docs/empty/"))
	if val, ok := got["empty/"]; !ok || val != "" || stats.Gets.Load() != 0 {
		t.Fatal(got, stats)
	}
	a, _ = recursiveFixture(t, map[string]string{"docs/sub/a/": "", "docs/sub/b/": ""}, 1000)
	a.cfg.ZipMaxFiles = 1
	a.s3 = s3.New(a.cfg)
	a.store = a.s3
	w := postArchive(a, `{"prefix":"docs/","keys":["docs/sub/"]}`, nil)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "zip_folder_limit") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRecursiveZIPMalformedUpstreamAndPaginationCycle(t *testing.T) {
	for _, body := range []string{
		`<ListBucketResult><Contents><Key>public/other/secret</Key><Size>1</Size><ETag>"v1"</ETag></Contents></ListBucketResult>`,
		`<ListBucketResult><Contents><Key>public/docs/sub/a</Key><ETag>"v1"</ETag></Contents></ListBucketResult>`,
		`<ListBucketResult><Contents><Key>public/docs/sub/a</Key><Size>1</Size></Contents></ListBucketResult>`,
		`<ListBucketResult><CommonPrefixes><Prefix>public/docs/sub/deep/</Prefix></CommonPrefixes></ListBucketResult>`,
		`<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`,
		`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>same</NextContinuationToken></ListBucketResult>`,
		`<ListBucketResult><EncodingType>url</EncodingType><Contents><Key>%zz</Key><Size>1</Size></Contents></ListBucketResult>`,
		`invalid xml`,
	} {
		t.Run(body, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, body) }))
			defer s.Close()
			c := testConfig()
			c.Prefix = "public/"
			c.Endpoint, _ = url.Parse(s.URL)
			a := New(c)
			w := postArchive(a, `{"prefix":"docs/","keys":["docs/sub/"]}`, nil)
			if w.Code != 502 || len(a.archives.plans) != 0 || calls.Load() > 2 {
				t.Fatal(w.Code, w.Body.String(), calls.Load())
			}
		})
	}
}
func TestRecursiveZIPCancelDuringEnumeration(t *testing.T) {
	canceled := make(chan struct{})
	started := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(canceled) }))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	a := New(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.buildArchivePlan(ctx, "docs/", []string{"docs/sub/"}); done <- err }()
	<-started
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("listing not canceled")
	}
	if err := <-done; err == nil {
		t.Fatal("canceled plan succeeded")
	}
}
func TestArchivePendingEntryBudget(t *testing.T) {
	s := newArchiveStore()
	now := time.Now()
	token, ok, err := s.put(archivePlan{Items: make([]archiveItem, archivePlanEntryLimit)}, now)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, ok, _ := s.put(archivePlan{Items: make([]archiveItem, 1)}, now); ok {
		t.Fatal("unbounded pending entries")
	}
	if _, ok := s.take(token, now); !ok {
		t.Fatal("missing plan")
	}
	if _, ok, _ := s.put(archivePlan{Items: make([]archiveItem, 1)}, now); !ok {
		t.Fatal("entry capacity not released")
	}
}

// STORAGE_BASE_PATH=docs/ on top of S3_PREFIX=public/: the browser root is the
// base folder, keys never mention it, and nothing outside it is reachable.
func TestBasePathConfinesBrowserRoot(t *testing.T) {
	data := map[string]string{"docs/a.txt": "root", "docs/sub/x.txt": "x", "elsewhere/leak.txt": "secret", "docs.txt": "sibling"}
	a, stats := recursiveFixture(t, data, 1000)
	a.cfg.Prefix, a.cfg.ProxyPrefix, a.cfg.BasePath = "public/docs/", "docs/", "docs/"
	a.s3 = s3.New(a.cfg)
	a.store = a.s3
	w := call(a, "GET", "/api/list", nil)
	var l backend.Listing
	json.Unmarshal(w.Body.Bytes(), &l)
	if w.Code != 200 || len(l.Entries) != 2 || l.Entries[0].Key != "sub/" || l.Entries[1].Key != "a.txt" || strings.Contains(w.Body.String(), "docs") || strings.Contains(w.Body.String(), "public") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call(a, "GET", "/api/config", nil); strings.Contains(w.Body.String(), "docs") {
		t.Fatal("config leaked base path", w.Body.String())
	}
	if w = call(a, "HEAD", "/api/object?key=a.txt", nil); w.Code != 200 || w.Header().Get("Content-Length") != "4" {
		t.Fatal(w.Code)
	}
	for _, key := range []string{"../elsewhere/leak.txt", "../docs.txt", "/elsewhere/leak.txt"} {
		if w = call(a, "GET", "/api/object?"+url.Values{"key": {key}}.Encode(), nil); w.Code != 400 {
			t.Fatal("escaped base path", key, w.Code)
		}
	}
	if w = call(a, "HEAD", "/api/object?key=elsewhere/leak.txt", nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	b, _ := json.Marshal(map[string]any{"prefix": "", "keys": []string{"sub/", "a.txt"}})
	w = postArchive(a, string(b), nil)
	var plan struct{ URL, Filename string }
	json.Unmarshal(w.Body.Bytes(), &plan)
	if w.Code != 200 || plan.Filename != "files.zip" {
		t.Fatal(w.Code, w.Body.String())
	}
	got := inspectZIP(t, a, plan.URL)
	if len(got) != 2 || got["a.txt"] != "root" || got["sub/x.txt"] != "x" {
		t.Fatal(got)
	}
	if stats.Gets.Load() != 2 {
		t.Fatal(stats.Gets.Load())
	}
}
