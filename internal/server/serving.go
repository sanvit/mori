package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log"
	"net/http"
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
	// Cache stores origin headers. Per-response browser/auth policy is applied
	// only when headers reach the public connection.
	wrapped := &policyResponse{ResponseWriter: w, header: make(http.Header), apply: func(h http.Header) {
		if a.cfg.ServeMode == "browser" {
			a.objectPresentation(w, strings.TrimPrefix(r.URL.Path, "/"), false)
			w.Header().Set("Cache-Control", "private, no-store")
		} else if a.cfg.Username != "" {
			value := w.Header().Get("Cache-Control")
			if value == "" {
				value = "no-store"
			}
			directives := []string{"private"}
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				name, _, _ := strings.Cut(part, "=")
				if part != "" && !strings.EqualFold(name, "public") && !strings.EqualFold(name, "private") {
					directives = append(directives, part)
				}
			}
			w.Header().Set("Cache-Control", strings.Join(directives, ", "))
		}
	}}
	a.objects.ServeHTTP(wrapped, r)
}

func (a *App) cachedObject(w http.ResponseWriter, r *http.Request, key string, download bool) {
	a.objectPresentation(w, key, download)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Delivery-Mode", "proxy")
	if a.objects == nil {
		a.serveObject(w, r, key, download)
		return
	}
	copyReq := r.Clone(r.Context())
	copyReq.URL.RawQuery = ""
	wrapped := &policyResponse{ResponseWriter: w, header: make(http.Header), apply: func(h http.Header) {
		a.objectPresentation(w, key, download)
		w.Header().Set("Cache-Control", "private, no-store")
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
	apply   func(http.Header)
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
	w.apply(w.header)
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
