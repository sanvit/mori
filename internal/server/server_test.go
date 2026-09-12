package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mori/internal/backend"
	"mori/internal/config"
)

func testConfig() config.Config {
	u, _ := url.Parse("http://127.0.0.1:1")
	return config.Config{Endpoint: u, HealthPath: "/_mori/healthz", Title: "Test", Bucket: "test-bucket", Region: "ap-northeast-2", PathStyle: true, ListingTTL: time.Minute, ListingMax: 4, CacheEnabled: true}
}
func call(a *App, method, target string, h http.Header) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	if h != nil {
		r.Header = h
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

// Test-only S3 fixture: it is never embedded in the application or enabled by ENV.
func fixtureApp(t *testing.T) *App {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			io.WriteString(w, `<ListBucketResult><CommonPrefixes><Prefix>documents/</Prefix></CommonPrefixes><Contents><Key>README.md</Key><Size>40</Size><LastModified>2026-09-09T00:00:00Z</LastModified></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`)
			return
		}
		if r.URL.Path != "/test-bucket/README.md" && r.URL.Path != "/test-bucket/evil.html" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"fixture-v1"`)
		http.ServeContent(w, r, "README.md", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), strings.NewReader("0123456789abcdefghijklmnopqrstuvwxyz0123"))
	}))
	t.Cleanup(s.Close)
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	return New(c)
}
func TestS3ListCacheAndAssets(t *testing.T) {
	a := fixtureApp(t)
	for _, want := range []string{"MISS", "HIT"} {
		w := call(a, "GET", "/_mori/api/list", nil)
		var l backend.Listing
		if e := json.Unmarshal(w.Body.Bytes(), &l); e != nil {
			t.Fatal(e)
		}
		if w.Code != 200 || len(l.Entries) != 2 || w.Header().Get("X-Listing-Cache") != want {
			t.Fatalf("%d %s %+v", w.Code, w.Header().Get("X-Listing-Cache"), l)
		}
	}
	if w := call(a, "GET", "/_mori/api/list?refresh=1", nil); w.Header().Get("X-Listing-Cache") != "MISS" {
		t.Fatal("refresh did not bypass list cache")
	}
	for _, p := range []string{"/", "/_mori/assets/app.js", "/_mori/assets/styles.css", "/_mori/assets/favicon.svg"} {
		w := call(a, "GET", p, nil)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal(p, w.Code)
		}
	}
	for _, p := range []string{"/.env", "/config.go", "/web/demo/README.md", "/preview.html"} {
		if call(a, "GET", p, nil).Code != 404 {
			t.Fatal("non-public file exposed", p)
		}
	}
}
func TestS3RangeHEADAndConditional(t *testing.T) {
	a := fixtureApp(t)
	p := "/_mori/api/object?key=README.md"
	w := call(a, "GET", p, http.Header{"Range": {"bytes=0-15"}})
	if w.Code != 206 || w.Body.String() != "0123456789abcdef" || w.Header().Get("Content-Range") != "bytes 0-15/40" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
	w = call(a, "HEAD", p, nil)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "40" {
		t.Fatal("HEAD response", w.Code, w.Header())
	}
	w = call(a, "GET", p, http.Header{"If-None-Match": {`"fixture-v1"`}})
	if w.Code != 304 || w.Body.Len() != 0 {
		t.Fatal("conditional response", w.Code)
	}
	w = call(a, "GET", p, http.Header{"Range": {"bytes=999999999-"}})
	if w.Code != 416 {
		t.Fatal(w.Code)
	}
	w = call(a, "GET", p+"&download=1", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("X-Cache") != "BYPASS" {
		t.Fatal(w.Code, w.Header())
	}
}
func TestAuthAndReadOnly(t *testing.T) {
	c := testConfig()
	c.Username = "admin"
	c.Password = "strong-password"
	a := New(c)
	if call(a, "GET", "/_mori/api/config", nil).Code != 401 {
		t.Fatal("missing auth accepted")
	}
	r := httptest.NewRequest("GET", "/_mori/api/config", nil)
	r.SetBasicAuth(c.Username, c.Password)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("valid auth denied")
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if call(a, m, "/_mori/api/object?key=x", nil).Code != 405 {
			t.Fatal(m)
		}
	}
	if call(a, "GET", "/_mori/healthz", nil).Code != 200 {
		t.Fatal("health auth")
	}
}
func TestConfigDoesNotExposeSecrets(t *testing.T) {
	c := testConfig()
	c.AccessKey = "SECRET_ACCESS_TEST"
	c.SecretKey = "SECRET_KEY_TEST"
	c.SessionToken = "SESSION_TOKEN_TEST"
	c.Password = "BASIC_PASSWORD_TEST"
	a := New(c)
	w := call(a, "GET", "/_mori/api/config", nil)
	for _, s := range []string{c.AccessKey, c.SecretKey, c.SessionToken, c.Password} {
		if strings.Contains(w.Body.String(), s) {
			t.Fatal("secret exposed")
		}
	}
}
func TestPathValidationAndSafePreviewHeaders(t *testing.T) {
	a := fixtureApp(t)
	for _, key := range []string{"../x", "a/./b", "a//b", "a\\b", "/absolute"} {
		u := "/_mori/api/object?" + url.Values{"key": {key}}.Encode()
		if call(a, "GET", u, nil).Code != 400 {
			t.Fatal(key)
		}
	}
	w := call(a, "GET", "/_mori/api/list?prefix=not-a-directory", nil)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = call(a, "GET", "/_mori/api/object?key=evil.html", nil)
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || w.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("unsafe content headers")
	}
}
func TestArchiveRequiresPreparedToken(t *testing.T) {
	a := New(testConfig())
	if call(a, "GET", "/_mori/api/archive?key=README.md", nil).Code != 410 {
		t.Fatal("archive must require a prepared, single-use token")
	}
}

func TestHTTPProxyHeaderSafety(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Set-Cookie", "unsafe=1")
		w.Header().Set("X-Cache", "HIT")
		w.Header().Set("Content-Range", "bytes 0-3/10")
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(206)
		io.WriteString(w, "test")
	}))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	w := call(New(c), "GET", "/_mori/api/object?key=test.txt", http.Header{"Range": {"bytes=0-3"}})
	if w.Code != 206 || w.Body.String() != "test" || w.Header().Get("X-Cache") != "BYPASS" || w.Header().Get("Set-Cookie") != "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
func TestUpstreamErrorsPropagate(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			io.WriteString(w, `<Error><Code>Denied</Code></Error>`)
		}))
		c := testConfig()
		c.Endpoint, _ = url.Parse(s.URL)
		a := New(c)
		w := call(a, "GET", "/_mori/api/list", nil)
		want := status
		if status == 500 {
			want = 502
		}
		if w.Code != want {
			t.Errorf("got %d want %d", w.Code, want)
		}
		s.Close()
	}
}

func TestListFetchesOnlyOneMetadataPage(t *testing.T) {
	var listCalls, objectCalls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			objectCalls.Add(1)
			http.Error(w, "no object should be fetched", 500)
			return
		}
		listCalls.Add(1)
		if r.URL.Query().Get("max-keys") != "1000" || r.URL.Query().Get("delimiter") != "/" {
			t.Error("invalid listing parameters", r.URL.RawQuery)
		}
		if r.URL.Query().Get("continuation-token") == "next" {
			io.WriteString(w, `<ListBucketResult><Contents><Key>b.txt</Key><Size>20</Size></Contents></ListBucketResult>`)
		} else {
			io.WriteString(w, `<ListBucketResult><Contents><Key>a.txt</Key><Size>10</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken></ListBucketResult>`)
		}
	}))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	a := New(c)
	w := call(a, "GET", "/_mori/api/list", nil)
	var l backend.Listing
	if e := json.Unmarshal(w.Body.Bytes(), &l); e != nil {
		t.Fatal(e)
	}
	if l.Cursor != "next" || listCalls.Load() != 1 || objectCalls.Load() != 0 {
		t.Fatal("listing traversed pages or downloaded an object")
	}
	call(a, "GET", "/_mori/api/list", nil)
	if listCalls.Load() != 1 {
		t.Fatal("metadata cache missed")
	}
	w = call(a, "GET", "/_mori/api/list?cursor=next", nil)
	if w.Code != 200 || listCalls.Load() != 2 || objectCalls.Load() != 0 {
		t.Fatal("next metadata page", w.Code)
	}
}

func TestObjectStreamsBeforeOriginFinishes(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "65540")
		w.Write(bytes.Repeat([]byte("x"), 65536))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			io.WriteString(w, "tail")
		case <-r.Context().Done():
		}
	}))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	server := httptest.NewServer(New(c))
	defer server.Close()
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/_mori/api/object?key=large.bin&download=1", nil)
	response, e := server.Client().Do(r)
	if e != nil {
		t.Fatal("no response before origin EOF", e)
	}
	defer response.Body.Close()
	first := make([]byte, 16)
	if _, e = io.ReadFull(response.Body, first); e != nil || string(first) != strings.Repeat("x", 16) {
		t.Fatal("streaming prefix", e, string(first))
	}
	unblock()
	rest, e := io.ReadAll(response.Body)
	if e != nil || len(rest) != 65540-16 || !bytes.HasSuffix(rest, []byte("tail")) {
		t.Fatal("streaming tail", e, len(rest))
	}
}

func TestClientDisconnectCancelsOrigin(t *testing.T) {
	cancelled := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 65536))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	server := httptest.NewServer(New(c))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/_mori/api/object?key=large.bin", nil)
	response, e := server.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.ReadFull(response.Body, make([]byte, 16))
	cancel()
	response.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream continued after cancellation")
	}
}
func TestHealthNamedObjectUsesOrigin(t *testing.T) {
	originCalls := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls++
		if r.URL.Path != "/test-bucket/public/healthz" {
			t.Error(r.URL.Path)
		}
		io.WriteString(w, "actual object")
	}))
	defer origin.Close()
	c := testConfig()
	c.Prefix = "public/"
	c.Endpoint, _ = url.Parse(origin.URL)
	a := New(c)
	w := call(a, "GET", "/_mori/api/object?key=healthz", nil)
	if w.Body.String() != "actual object" || originCalls != 1 || w.Header().Get("X-Cache") != "BYPASS" {
		t.Fatal(w.Body.String(), originCalls, w.Header())
	}
}
