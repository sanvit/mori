package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"mori/internal/backend"
	"mori/internal/config"
	"mori/internal/objectcache"
)

// Open wires the shared cache into a single application, returning disk/config
// errors before the public listener is started.
func Open(c config.Config, store backend.Backend) (*App, error) {
	a := NewWithBackend(c, store)
	pc := c.ObjectCache
	pc.HealthPath = ""
	pc.AccessLog = false
	var disk *objectcache.DiskCache
	if c.CacheMode == "internal" && pc.CacheEnabled {
		identity := fmt.Sprintf("%s|%v|%s|%s|%s|%v|%s|%s|%s|%s|%s|%s|%d|%d", c.Backend, c.Endpoint, c.Bucket, c.Prefix, c.AccessKey, c.WebDAV.URL, c.WebDAV.Username, c.FTP.Addr, c.FTP.Root, c.FTP.Username, c.SFTP.Addr, c.SFTP.Root, pc.CacheBlockSize, pc.CacheSegmentSize)
		identity += "|" + c.SFTP.Username
		namespace := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
		var err error
		disk, err = objectcache.NewDiskCache(filepath.Join(pc.CacheDir, namespace), pc.CacheMaxDiskSize, pc.CacheBlockSize, pc.CacheSegmentSize)
		if err != nil {
			return nil, err
		}
		a.objectCacheEnabled = true
	}
	a.objects = objectcache.NewProxy(pc, objectcache.NewBackendOrigin(store), disk)
	return a, nil
}

func (a *App) Shutdown() {
	if a.objects != nil {
		a.objects.Cancel()
	}
	if closer, ok := a.store.(io.Closer); ok {
		_ = closer.Close()
	}
}

func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	if config.ValidateKey(strings.TrimPrefix(r.URL.Path, "/"), true) != nil || !strings.HasPrefix(r.URL.Path, "/") {
		fail(w, 400, "invalid_path", "올바르지 않은 파일 경로입니다.")
		return
	}
	internal := r.URL.Path == "/_mori" || strings.HasPrefix(r.URL.Path, "/_mori/")
	if r.Method != "GET" && r.Method != "HEAD" && !(a.cfg.ServeMode == "browser" && r.Method == "POST" && r.URL.Path == "/_mori/api/archive") && !(r.Method == "OPTIONS" && !internal) {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.cfg.HealthPath != "" && r.URL.Path == a.cfg.HealthPath {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(405)
			return
		}
		if a.objects != nil {
			a.objects.Health(w, r)
		} else {
			w.Header().Set("Content-Type", "application/json")
			if r.Method != "HEAD" {
				io.WriteString(w, `{"status":"ok","cache_bytes":0,"cache_max_bytes":0,"cache_blocks":0}`)
			}
		}
		return
	}
	t := &trackedResponse{ResponseWriter: w, status: 200}
	w = t
	if a.cfg.ObjectCache.AccessLog {
		start := time.Now()
		defer func() {
			log.Printf("%s %s %d %d %s %s", r.Method, r.URL.EscapedPath(), t.status, t.written, w.Header().Get("X-Cache"), time.Since(start).Round(time.Millisecond))
		}()
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if a.cfg.Username != "" {
		u, p, ok := r.BasicAuth()
		uh, ph := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
		wantU, wantP := sha256.Sum256([]byte(a.cfg.Username)), sha256.Sum256([]byte(a.cfg.Password))
		if !ok || subtle.ConstantTimeCompare(uh[:], wantU[:])&subtle.ConstantTimeCompare(ph[:], wantP[:]) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="mori", charset="UTF-8"`)
			w.Header().Set("Cache-Control", "private, no-store")
			fail(w, 401, "unauthorized", "로그인이 필요합니다.")
			return
		}
	}
	if a.cfg.ServeMode == "browser" && (r.URL.Path == "/" || internal) {
		a.serveBrowser(w, r)
		return
	}
	if internal {
		if r.URL.Path == "/_mori/api/object" && (r.Method == "GET" || r.Method == "HEAD") {
			key := r.URL.Query().Get("key")
			if config.ValidateKey(key, false) != nil {
				fail(w, 400, "invalid_key", "올바르지 않은 파일 경로입니다.")
				return
			}
			a.cachedObject(w, r, key, r.URL.Query().Get("download") == "1")
			return
		}
		fail(w, 404, "not_found", "페이지를 찾을 수 없습니다.")
		return
	}
	if a.objects == nil {
		key := strings.TrimPrefix(r.URL.Path, "/")
		if key == "" {
			fail(w, 404, "not_found", "파일을 찾을 수 없습니다.")
			return
		}
		clone := r.Clone(r.Context())
		q := clone.URL.Query()
		q.Set("key", key)
		clone.URL.RawQuery = q.Encode()
		a.object(w, clone)
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		w.WriteHeader(405)
		return
	}
	// A file path may ask for an attachment the way the object API does, and
	// follows the same proxy/presigned delivery choice. The flag only selects
	// presentation, so it is removed before the cache key is derived and the
	// same body is not stored a second time under it.
	download := false
	if a.cfg.ServeMode == "browser" {
		if q := r.URL.Query(); q.Has("download") {
			download = q.Get("download") == "1"
			r = r.Clone(r.Context())
			q.Del("download")
			r.URL.RawQuery = q.Encode()
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		mode := a.deliveryMode(r, key, download)
		w.Header().Set("X-Delivery-Mode", mode)
		if mode == "presigned" {
			a.presignRedirect(w, r, key, download)
			return
		}
	}
	// Cache stores origin headers. Per-response browser/auth policy is applied
	// only when headers reach the public connection.
	wrapped := &policyResponse{ResponseWriter: w, header: make(http.Header), apply: func(status int, h http.Header) {
		if a.cfg.ServeMode == "browser" {
			// Stored files carry the same guarantees on either route. An HTML
			// preview relaxes framing for itself inside objectPresentation.
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			if status < 400 {
				a.objectPresentation(w, strings.TrimPrefix(r.URL.Path, "/"), download)
			} else {
				// The body is an index, SPA or error page, not the requested
				// object. Presenting it as that object would label HTML with the
				// missing file's type and name. Keep the served page's own type
				// and still deny it scripts.
				w.Header().Del("Content-Disposition")
				w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
			}
			a.restrictCaching(w, status)
		} else if a.cfg.Username != "" {
			value := w.Header().Get("Cache-Control")
			if value == "" {
				value = "no-store"
			}
			directives := []string{"private"}
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				name, _, _ := strings.Cut(part, "=")
				// s-maxage only speaks to shared caches, which private just shut
				// out; leaving it would advertise a lifetime nothing can use.
				if part != "" && !strings.EqualFold(name, "public") && !strings.EqualFold(name, "private") && !strings.EqualFold(name, "s-maxage") {
					directives = append(directives, part)
				}
			}
			w.Header().Set("Cache-Control", strings.Join(directives, ", "))
		}
	}}
	a.objects.ServeHTTP(wrapped, r)
}

// encodeSegment escapes one path segment exactly the way the browser's
// encodeURIComponent does. A link built in the UI and a URL built here are then
// byte-identical, so they address one entry in the browser cache and in any
// cache placed in front of mori.
func encodeSegment(s string) string {
	const unreserved = "-_.!~*'()"
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(unreserved, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// objectPath addresses a stored object by its own path, so one object has one
// URL for the browser, the disk cache and anything caching in front of mori.
// The reserved prefix has no path form, so those keys keep the object API.
func objectPath(key string, download bool) string {
	if key == "_mori" || strings.HasPrefix(key, "_mori/") {
		// Written in the UI's parameter order, not sorted, so both spellings of
		// this URL are the same string too.
		path := "/_mori/api/object?key=" + url.QueryEscape(key)
		if download {
			path += "&download=1"
		}
		return path
	}
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = encodeSegment(segment)
	}
	path := "/" + strings.Join(segments, "/")
	if download {
		path += "?download=1"
	}
	return path
}

// deliveryMode picks proxy or presigned delivery for one read. Only a plain
// object GET can be handed to storage directly: HEAD and listing stay
// server-side, and an HTML preview needs mori's own sandbox headers.
func (a *App) deliveryMode(r *http.Request, key string, download bool) string {
	if r.Method != http.MethodGet || a.s3 == nil || a.renderHTML(key, download) {
		return "proxy"
	}
	if download {
		return a.cfg.DownloadMode
	}
	return a.cfg.PreviewMode
}

// presignRedirect sends the visitor to a bearer URL. The object cache is
// bypassed, and so are the index, SPA and custom error pages, because the
// storage answers a miss itself.
func (a *App) presignRedirect(w http.ResponseWriter, r *http.Request, key string, download bool) {
	link, err := a.s3.Presign(r.Method, key, download, time.Now())
	if err != nil {
		a.upstreamFail(w, err)
		return
	}
	// No redirect body or access log contains the bearer URL. Sign the final
	// public endpoint, never replace its hostname after signing.
	w.Header().Set("Location", link)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

// restrictCaching decides what downstream may keep of a stored file. Behind
// Basic auth every response is one visitor's, and an error body belongs to the
// request that produced it, so both are withheld. A public deployment keeps the
// freshness the cache policy computed, which is what lets a cache in front of
// mori serve the file at all.
func (a *App) restrictCaching(w http.ResponseWriter, status int) {
	if a.cfg.Username != "" || status >= 400 {
		w.Header().Set("Cache-Control", "private, no-store")
	}
}

func (a *App) cachedObject(w http.ResponseWriter, r *http.Request, key string, download bool) {
	a.objectPresentation(w, key, download)
	a.restrictCaching(w, http.StatusOK)
	w.Header().Set("X-Delivery-Mode", "proxy")
	if a.objects == nil {
		a.serveObject(w, r, key, download)
		return
	}
	copyReq := r.Clone(r.Context())
	copyReq.URL.RawQuery = ""
	wrapped := &policyResponse{ResponseWriter: w, header: make(http.Header), apply: func(status int, h http.Header) {
		a.objectPresentation(w, key, download)
		a.restrictCaching(w, status)
	}}
	status, err := a.objects.ServeObject(wrapped, copyReq, key)
	if err != nil {
		if wrapped.started {
			panic(http.ErrAbortHandler)
		}
		a.upstreamFail(w, &backend.UpstreamError{Status: status, Code: "ObjectReadFailed"})
	}
}

type trackedResponse struct {
	http.ResponseWriter
	status  int
	written int64
	started bool
}

func (w *trackedResponse) WriteHeader(code int) {
	if w.started {
		return
	}
	w.started = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *trackedResponse) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	n, e := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, e
}
func (w *trackedResponse) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *trackedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type policyResponse struct {
	http.ResponseWriter
	header  http.Header
	apply   func(int, http.Header)
	started bool
}

func (w *policyResponse) Header() http.Header { return w.header }
func (w *policyResponse) WriteHeader(status int) {
	if w.started {
		return
	}
	w.started = true
	for k, v := range w.header {
		w.ResponseWriter.Header()[k] = v
	}
	w.apply(status, w.header)
	w.ResponseWriter.WriteHeader(status)
}
func (w *policyResponse) Write(b []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *policyResponse) Flush() {
	if !w.started {
		w.WriteHeader(200)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *policyResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
