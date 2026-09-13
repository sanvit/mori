package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPreviewSourceModesAndNoOriginBodyRead(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("preview descriptor must not request any origin object/listing")
		w.WriteHeader(500)
	}))
	defer s.Close()
	for _, mode := range []string{"proxy", "presigned"} {
		c := testConfig()
		c.Endpoint, _ = url.Parse(s.URL)
		c.PreviewMode = mode
		c.AccessKey = "preview-test-access"
		c.SecretKey = "preview-test-secret"
		c.SessionToken = "preview-test-session"
		a := New(c)
		key := "하위 폴더/이미지 +&.png"
		w := call(a, "GET", "/_mori/api/preview?"+url.Values{"key": {key}}.Encode(), nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var source map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
			t.Fatal(err)
		}
		if source["kind"] != "image" || source["mode"] != mode {
			t.Fatal(source)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Error("bearer response must not be cached")
		}
		if strings.Contains(w.Body.String(), c.SecretKey) {
			t.Error("secret leaked")
		}
		u, err := url.Parse(source["url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if mode == "proxy" {
			if u.Host != "" || u.Path != "/"+key || u.RawQuery != "" {
				t.Fatal(u)
			}
		} else {
			if u.Query().Get("X-Amz-Signature") == "" || u.Query().Get("X-Amz-SignedHeaders") != "host" {
				t.Fatal("missing GetObject signature")
			}
			if u.Query().Get("response-content-type") != "image/png" || !strings.HasPrefix(u.Query().Get("response-content-disposition"), "inline") {
				t.Error("incorrect signed presentation")
			}
			if source["expiresAt"] == nil {
				t.Error("missing expiry")
			}
		}
		w = call(a, "GET", "/_mori/api/preview?key=archive.zip", nil)
		if strings.Contains(w.Body.String(), "X-Amz-") || strings.Contains(w.Body.String(), `"url"`) {
			t.Error("unsupported preview was signed")
		}
		for _, bad := range []string{"", "../test.jpg", "folder/", "/test.jpg", "folder//test.jpg"} {
			w = call(a, "GET", "/_mori/api/preview?"+url.Values{"key": {bad}}.Encode(), nil)
			if w.Code != 400 {
				t.Errorf("accepted key %q", bad)
			}
		}
		w = call(a, "HEAD", "/_mori/api/preview?key=test.jpg", nil)
		if w.Code != 405 || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "X-Amz-") {
			t.Error("HEAD presign must not be exposed")
		}
	}
}
func TestPreviewSourceAuthentication(t *testing.T) {
	c := testConfig()
	c.Username = "reader"
	c.Password = "test-password"
	a := New(c)
	for _, route := range []string{"/_mori/api/preview?key=test.png", "/_mori/assets/preview.js", "/_mori/assets/preview.css", "/_mori/vendor/fake.js"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 401 {
			t.Errorf("unauthenticated %s status %d", route, w.Code)
		}
	}
}
func TestPreviewCSPAndAssetRouting(t *testing.T) {
	for _, pathStyle := range []bool{true, false} {
		c := testConfig()
		c.PreviewMode = "presigned"
		c.PathStyle = pathStyle
		c.Endpoint, _ = url.Parse("http://private-storage:9000")
		c.PresignEndpoint, _ = url.Parse("https://objects.example.test:8443/base")
		c.Bucket = "assets"
		a := New(c)
		policy := a.contentSecurityPolicy()
		host := "https://objects.example.test:8443"
		if !pathStyle {
			host = "https://assets.objects.example.test:8443"
		}
		if !strings.Contains(policy, host) || strings.Contains(policy, "private-storage") || strings.Contains(policy, "https:") && !strings.Contains(policy, host) {
			t.Fatal(policy)
		}
		if strings.Contains(policy, "'unsafe-eval'") || !strings.Contains(policy, "worker-src 'self'") || !strings.Contains(policy, "frame-src 'none'") {
			t.Fatal("unsafe preview CSP", policy)
		}
		c.PreviewMode = "proxy"
		if strings.Contains(New(c).contentSecurityPolicy(), "objects.example") {
			t.Error("proxy mode whitelists unnecessary origin")
		}
	}
	a := New(testConfig())
	for _, route := range []string{"/", "/_mori/assets/preview.js", "/_mori/assets/preview.css", "/_mori/assets/app.js", "/_mori/assets/styles.css"} {
		w := call(a, "GET", route, nil)
		if w.Code != 200 || w.Body.Len() == 0 || w.Header().Get("ETag") == "" {
			t.Fatal(route, w.Code)
		}
		if strings.HasSuffix(route, ".js") && !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
			t.Fatal("invalid module MIME")
		}
		cached := call(a, "GET", route, http.Header{"If-None-Match": {w.Header().Get("ETag")}})
		if cached.Code != 304 {
			t.Error("static ETag not respected")
		}
	}
	for _, route := range []string{"/_mori/vendor/", "/_mori/assets/package.json", "/_mori/assets/.env"} {
		if call(a, "GET", route, nil).Code != 404 {
			t.Error("unexpected asset access", route)
		}
	}
	for _, route := range []string{"/_mori/vendor/../app.js", "/_mori/vendor/../../config.go"} {
		if call(a, "GET", route, nil).Code != http.StatusBadRequest {
			t.Errorf("accepted traversal %s", route)
		}
	}
}

func TestHTMLRenderingOption(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<h1>Hello</h1>")) }))
	defer origin.Close()
	for _, enabled := range []bool{false, true} {
		for _, mode := range []string{"proxy", "presigned"} {
			c := testConfig()
			c.Endpoint, _ = url.Parse(origin.URL)
			c.HTMLPreviewEnabled = enabled
			c.PreviewMode = mode
			c.AccessKey, c.SecretKey = "test", "test"
			a := New(c)
			w := call(a, "GET", "/_mori/api/preview?key=page.HTML", nil)
			var source map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &source); err != nil {
				t.Fatal(err)
			}
			if source["renderHTML"] != enabled {
				t.Fatal(source)
			}
			if enabled && source["mode"] != "proxy" {
				t.Fatal(source)
			}
			for _, method := range []string{"GET", "HEAD"} {
				w = call(a, method, "/_mori/api/object?key=page.HTML", nil)
				if !enabled && mode == "presigned" && method == "GET" {
					continue
				}
				want := "text/plain; charset=utf-8"
				if enabled {
					want = "text/html; charset=utf-8"
				}
				if w.Code != 200 || w.Header().Get("Content-Type") != want || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "inline;") {
					t.Fatal(w.Code, w.Header())
				}
				if !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
					t.Fatal(w.Header())
				}
			}
			w = call(a, "GET", "/_mori/api/object?key=page.HTML&download=1", nil)
			if !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
				t.Fatal(w.Header())
			}
		}
	}
}

// The UI builds object links itself, so its encoding and the descriptor's must
// agree exactly. A mismatch still resolves to the same object here but splits
// it into two entries in the browser cache and in any cache in front of mori.
// These expectations are what encodeURIComponent produces for each segment.
func TestObjectPathMatchesBrowserEncoding(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{"plain.txt", "/plain.txt"},
		{"docs/a b.txt", "/docs/a%20b.txt"},
		{"한글.txt", "/%ED%95%9C%EA%B8%80.txt"},
		{"a+b.txt", "/a%2Bb.txt"},
		{"q?x.txt", "/q%3Fx.txt"},
		{"hash#1.txt", "/hash%231.txt"},
		{"pct%20.txt", "/pct%2520.txt"},
		{"amp&eq=.txt", "/amp%26eq%3D.txt"},
		{"brack[1].txt", "/brack%5B1%5D.txt"},
		{"keep-_.!~*'().txt", "/keep-_.!~*'().txt"},
	} {
		if got := objectPath(tc.key, false); got != tc.want {
			t.Errorf("objectPath(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
	if got := objectPath("docs/a b.txt", true); got != "/docs/a%20b.txt?download=1" {
		t.Error(got)
	}
	// The reserved prefix has no path form and keeps the exact-key API.
	if got := objectPath("_mori/x.txt", true); got != "/_mori/api/object?key=_mori%2Fx.txt&download=1" {
		t.Error(got)
	}
}
