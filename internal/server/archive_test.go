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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func archiveFixture(t *testing.T, hook func(http.ResponseWriter, *http.Request) bool) (*App, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	heads, gets := new(atomic.Int32), new(atomic.Int32)
	files := map[string]string{"docs/a.txt": "hello", "docs/한글 +&.txt": "안녕하세요", "docs/empty.txt": ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			heads.Add(1)
		} else {
			gets.Add(1)
		}
		if hook != nil && hook(w, r) {
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/test-bucket/public/") {
			http.NotFound(w, r)
			return
		}
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/test-bucket/public/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(data)))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", "Wed, 09 Sep 2026 06:00:00 GMT")
		if r.Method == "HEAD" {
			return
		}
		if r.Header.Get("If-Match") != etag {
			t.Error("ZIP GetObject lost version condition", r.Header)
		}
		io.WriteString(w, data)
	}))
	t.Cleanup(server.Close)
	c := testConfig()
	c.Prefix = "public/"
	c.Endpoint, _ = url.Parse(server.URL)
	return New(c), heads, gets
}
func postArchive(a *App, body string, headers http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/_mori/api/archive", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mori-Request", "1")
	for k, v := range headers {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	return w
}
func prepareZIP(t *testing.T, a *App, keys ...string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"prefix": "docs/", "keys": keys})
	w := postArchive(a, string(b), nil)
	if w.Code != 200 {
		t.Fatalf("prepare ZIP: %d %s", w.Code, w.Body.String())
	}
	var result struct{ URL string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.URL
}
func TestArchiveStoreNativeZIPAndSingleUse(t *testing.T) {
	a, heads, gets := archiveFixture(t, nil)
	// Presigned individual modes do not change ZIP's server-side delivery path.
	a.cfg.DownloadMode = "presigned"
	a.cfg.PreviewMode = "presigned"
	link := prepareZIP(t, a, "docs/a.txt", "docs/한글 +&.txt", "docs/empty.txt", "docs/a.txt")
	if heads.Load() != 3 || gets.Load() != 0 {
		t.Fatal("preparation fetched bodies or duplicate metadata", heads.Load(), gets.Load())
	}
	w := call(a, "GET", link, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("X-Delivery-Mode") != "proxy" || w.Header().Get("Content-Length") != "" {
		t.Fatal(w.Code, w.Header())
	}
	z, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 3 {
		t.Fatal("duplicate keys not removed", len(z.File))
	}
	want := map[string]string{"a.txt": "hello", "한글 +&.txt": "안녕하세요", "empty.txt": ""}
	for _, f := range z.File {
		if f.Method != zip.Store || f.CompressedSize64 != f.UncompressedSize64 {
			t.Fatal("not Store", f.FileHeader)
		}
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal("ZIP CRC/read failed", err)
		}
		if expected, ok := want[f.Name]; !ok || expected != string(b) {
			t.Fatal("ZIP payload/name mismatch", f.Name, string(b))
		}
	}
	if gets.Load() != 3 || call(a, "GET", link, nil).Code != 410 {
		t.Fatal("ZIP ticket reused")
	}
}
func TestArchiveValidationAndServerSideLimits(t *testing.T) {
	for _, body := range []string{
		`{"prefix":"docs/","keys":[]}`,
		`{"prefix":"docs/","keys":["docs/sub/a.txt"]}`,
		`{"prefix":"docs/","keys":["other/a.txt"]}`,
		`{"prefix":"docs/","keys":["docs/../a.txt"]}`,
		`{"prefix":"docs/","keys":["docs/C:stream"]}`,
		`{"prefix":"docs","keys":["docs/a.txt"]}`,
		`{"prefix":"docs/","keys":["docs/a.txt"],"size":0}`,
		`{"prefix":"docs/","keys":["docs/a.txt"]} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			a, heads, gets := archiveFixture(t, nil)
			w := postArchive(a, body, nil)
			if w.Code != 400 || heads.Load() != 0 || gets.Load() != 0 {
				t.Fatal(w.Code, w.Body.String(), heads.Load(), gets.Load())
			}
		})
	}
	a, _, gets := archiveFixture(t, nil)
	a.cfg.ZipMaxFiles = 1
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/a.txt","docs/empty.txt"]}`, nil); w.Code != 400 {
		t.Fatal(w.Code)
	}
	a.cfg.ZipMaxFiles = 200
	a.cfg.ZipMaxBytes = 4
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/a.txt"]}`, nil); w.Code != 413 || gets.Load() != 0 {
		t.Fatal("actual HEAD size not enforced", w.Code)
	}
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/missing.txt"]}`, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestArchiveCSRFAndAuth(t *testing.T) {
	a, heads, _ := archiveFixture(t, nil)
	body := `{"prefix":"docs/","keys":["docs/a.txt"]}`
	for _, h := range []http.Header{{"X-Mori-Request": {""}}, {"Content-Type": {"text/plain"}}, {"Origin": {"https://evil.example"}}, {"Sec-Fetch-Site": {"cross-site"}}, {"Sec-Fetch-Site": {"same-site"}}, {"Origin": {"null"}}} {
		if w := postArchive(a, body, h); w.Code != 403 {
			t.Fatal("cross-origin accepted", h, w.Code)
		}
	}
	if heads.Load() != 0 {
		t.Fatal("CSRF reached origin")
	}
	a.cfg.Username = "admin"
	a.cfg.Password = "test-password"
	if postArchive(a, body, nil).Code != 401 || call(a, "GET", "/_mori/api/archive?token=x", nil).Code != 401 {
		t.Fatal("ZIP bypassed auth")
	}
	if call(a, "HEAD", "/_mori/api/archive", nil).Code != 401 {
		t.Fatal("HEAD bypassed auth")
	}
}
func TestArchiveLimitBusyDoesNotConsumeTicket(t *testing.T) {
	a, _, _ := archiveFixture(t, nil)
	link := prepareZIP(t, a, "docs/a.txt")
	for i := 0; i < cap(a.zipSlots); i++ {
		a.zipSlots <- struct{}{}
	}
	if w := call(a, "GET", link, nil); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Header())
	}
	for i := 0; i < cap(a.zipSlots); i++ {
		<-a.zipSlots
	}
	if w := call(a, "GET", link, http.Header{"Range": {"bytes=0-1"}}); w.Code != 416 {
		t.Fatal(w.Code)
	}
	if w := call(a, "GET", link, nil); w.Code != 200 {
		t.Fatal("ticket lost while busy/ranged", w.Code)
	}
	if w := call(a, "HEAD", "/_mori/api/archive", nil); w.Code != 405 {
		t.Fatal(w.Code)
	}
}
func TestArchivePlanBoundedExpiry(t *testing.T) {
	s := newArchiveStore()
	now := time.Now()
	for i := 0; i < archivePlanLimit; i++ {
		if _, ok, err := s.put(archivePlan{}, now); !ok || err != nil {
			t.Fatal(ok, err)
		}
	}
	if _, ok, _ := s.put(archivePlan{}, now); ok {
		t.Fatal("unbounded plans")
	}
	token, ok, err := s.put(archivePlan{}, now.Add(archivePlanTTL))
	if !ok || err != nil {
		t.Fatal("expired plans not reclaimed")
	}
	if _, ok = s.take(token, now.Add(2*archivePlanTTL)); ok {
		t.Fatal("expired ticket accepted")
	}
	token, _, _ = s.put(archivePlan{}, now)
	if _, ok = s.take(token, now); !ok {
		t.Fatal("valid ticket rejected")
	}
	if _, ok = s.take(token, now); ok {
		t.Fatal("ticket not single use")
	}
}
func TestArchiveVersionChangeFailsBeforeZIP(t *testing.T) {
	a, _, _ := archiveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "GET" {
			return false
		}
		w.Header().Set("ETag", `"new-version"`)
		w.Header().Set("Content-Length", "5")
		io.WriteString(w, "world")
		return true
	})
	link := prepareZIP(t, a, "docs/a.txt")
	w := call(a, "GET", link, nil)
	if w.Code != 409 || w.Header().Get("Content-Type") == "application/zip" || strings.Contains(w.Body.String(), "PK") {
		t.Fatal("changed version accepted", w.Code, w.Header())
	}
}
func TestArchiveMidStreamErrorDoesNotFinalizePartialZIP(t *testing.T) {
	a, _, _ := archiveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "empty.txt") {
			return false
		}
		http.Error(w, "failed", 500)
		return true
	})
	link := prepareZIP(t, a, "docs/a.txt", "docs/empty.txt")
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if recovered := recover(); recovered != http.ErrAbortHandler {
				t.Errorf("stream not aborted: %v", recovered)
			}
		}()
		a.ServeHTTP(w, httptest.NewRequest("GET", link, nil))
	}()
	if _, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len())); err == nil {
		t.Fatal("partial archive was finalized")
	}
}
func TestArchiveStreamsAndCancelsOrigin(t *testing.T) {
	originCanceled := make(chan struct{})
	a, _, _ := archiveFixture(t, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "large.bin") {
			return false
		}
		w.Header().Set("ETag", `"large"`)
		w.Header().Set("Content-Length", "104857600")
		if r.Method == "HEAD" {
			return true
		}
		w.Write(bytes.Repeat([]byte("x"), 65536))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(originCanceled)
		return true
	})
	link := prepareZIP(t, a, "docs/large.bin")
	server := httptest.NewServer(a)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+link, nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 8192)
	if _, err = io.ReadFull(resp.Body, first); err != nil {
		resp.Body.Close()
		t.Fatal("no bytes before origin completion", err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-originCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancel did not reach ZIP origin")
	}
}
func TestArchiveDisabled(t *testing.T) {
	a, heads, gets := archiveFixture(t, nil)
	link := prepareZIP(t, a, "docs/a.txt")
	a.cfg.ZipDisabled = true
	var cfg map[string]any
	json.Unmarshal(call(a, "GET", "/_mori/api/config", nil).Body.Bytes(), &cfg)
	if cfg["zipEnabled"] != false {
		t.Fatal("config must report ZIP disabled", cfg)
	}
	before := heads.Load()
	for _, w := range []*httptest.ResponseRecorder{
		postArchive(a, `{"prefix":"docs/","keys":["docs/a.txt"]}`, nil),
		call(a, "GET", link, nil),
		call(a, "HEAD", "/_mori/api/archive", nil),
	} {
		if w.Code != 404 || !strings.Contains(w.Body.String(), "zip_disabled") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if heads.Load() != before || gets.Load() != 0 {
		t.Fatal("disabled ZIP reached storage")
	}
	a.cfg.Username, a.cfg.Password = "admin", "test-password"
	if w := postArchive(a, `{"prefix":"docs/","keys":["docs/a.txt"]}`, nil); w.Code != 401 {
		t.Fatal("disabled ZIP must still require auth first", w.Code)
	}
	a.cfg.Username, a.cfg.Password = "", ""
	if w := call(a, "HEAD", "/_mori/api/object?key=docs/a.txt&download=1", nil); w.Code != 200 || w.Header().Get("Content-Length") != "5" {
		t.Fatal("single-file download must keep working", w.Code)
	}
	a.cfg.ZipDisabled = false
	json.Unmarshal(call(a, "GET", "/_mori/api/config", nil).Body.Bytes(), &cfg)
	if cfg["zipEnabled"] != true {
		t.Fatal(cfg)
	}
}
