// Package server is mori's HTTP surface: authentication, security headers,
// the JSON API, object proxying, ZIP streaming, and the embedded UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mori/internal/backend"
	"mori/internal/cache"
	"mori/internal/config"
	"mori/internal/objectcache"
	"mori/internal/s3"
)

type App struct {
	objects            *objectcache.Proxy
	objectCacheEnabled bool
	cfg                config.Config
	store              backend.Backend
	s3                 *s3.Origin // non-nil only for the S3 backend: proxy passthrough and presigning
	cache              *cache.Cache[backend.Listing]
	slots              chan struct{}
	zipSlots           chan struct{}
	archives           *archiveStore
}

// New builds an S3-backed handler. Other backends use NewWithBackend.
func New(c config.Config) *App {
	c.Backend = "s3"
	c = withDefaults(c)
	return NewWithBackend(c, s3.New(c))
}

// NewWithBackend builds the handler on store. Presigned delivery is only
// honored when store is S3; other backends fall back to proxy delivery.
func NewWithBackend(c config.Config, store backend.Backend) *App {
	c = withDefaults(c)
	origin, _ := store.(*s3.Origin)
	if origin == nil && (c.DownloadMode == "presigned" || c.PreviewMode == "presigned") {
		c.DownloadMode, c.PreviewMode = "proxy", "proxy"
	}
	return &App{archives: newArchiveStore(), zipSlots: make(chan struct{}, c.ZipConcurrency), cfg: c, store: store, s3: origin, cache: cache.New[backend.Listing](c.ListingTTL, c.ListingMax), slots: make(chan struct{}, 64)}
}

// withDefaults fills zero-valued limits so hand-built configs behave like config.Read.
func withDefaults(c config.Config) config.Config {
	if c.ServeMode == "" {
		c.ServeMode = "browser"
	}
	if c.ZipMaxFiles == 0 {
		c.ZipMaxFiles = 200
	}
	if c.ZipMaxBytes == 0 {
		c.ZipMaxBytes = 20 << 30
	}
	if c.ZipConcurrency == 0 {
		c.ZipConcurrency = 2
	}
	if c.PresignTTL == 0 {
		c.PresignTTL = 15 * time.Minute
	}
	if c.DownloadMode == "" {
		c.DownloadMode = "proxy"
	}
	if c.PreviewMode == "" {
		c.PreviewMode = "proxy"
	}
	if c.Backend == "" {
		c.Backend = "s3"
	}
	return c
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.serve(w, r)
}

func (a *App) serveBrowser(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Security-Policy", a.contentSecurityPolicy())
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Method != "GET" && r.Method != "HEAD" && !(r.Method == "POST" && r.URL.Path == "/_mori/api/archive") {
		w.Header().Set("Allow", "GET, HEAD")
		fail(w, 405, "method_not_allowed", "읽기 전용 브라우저입니다.")
		return
	}
	switch r.URL.Path {
	case "/_mori/api/config":
		a.config(w, r)
	case "/_mori/api/list":
		a.withSlot(w, r, a.list)
	case "/_mori/api/preview":
		a.withSlot(w, r, a.preview)
	case "/_mori/api/object":
		a.withSlot(w, r, a.object)
	case "/_mori/api/archive":
		if a.cfg.ZipDisabled {
			fail(w, 404, "zip_disabled", "이 서버에서는 ZIP 다운로드를 사용하지 않습니다.")
			return
		}
		if r.Method != "GET" && r.Method != "POST" {
			w.Header().Set("Allow", "GET, POST")
			fail(w, 405, "method_not_allowed", "ZIP은 POST로 준비하고 GET으로 다운로드합니다.")
			return
		}
		a.withSlot(w, r, a.archive)
	case "/", "/_mori/assets/app.js", "/_mori/assets/styles.css", "/_mori/assets/preview.js", "/_mori/assets/preview.css", "/_mori/assets/favicon.svg":
		a.static(w, r)
	default:
		if strings.HasPrefix(r.URL.Path, "/_mori/vendor/") {
			a.static(w, r)
		} else {
			fail(w, 404, "not_found", "페이지를 찾을 수 없습니다.")
		}
	}
}
func (a *App) withSlot(w http.ResponseWriter, r *http.Request, f http.HandlerFunc) {
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
		f(w, r)
	default:
		w.Header().Set("Retry-After", "2")
		fail(w, 429, "busy", "요청이 많습니다. 잠시 후 다시 시도해 주세요.")
	}
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
func (a *App) config(w http.ResponseWriter, r *http.Request) {
	// Preserve the standalone API marker; cacheMode reports internal/off,
	// while downloadMode/previewMode report proxy/presigned delivery.
	mode := "direct"
	jsonOut(w, map[string]any{"title": a.cfg.Title, "backend": a.cfg.Backend, "mode": mode, "downloadMode": a.cfg.DownloadMode,
		"serveMode": a.cfg.ServeMode, "cacheMode": a.cfg.CacheMode, "previewMode": a.cfg.PreviewMode, "zipEnabled": !a.cfg.ZipDisabled, "zipMaxFiles": a.cfg.ZipMaxFiles, "zipMaxBytes": a.cfg.ZipMaxBytes})
}
func (a *App) list(w http.ResponseWriter, r *http.Request) {
	prefix, cursor := r.URL.Query().Get("prefix"), r.URL.Query().Get("cursor")
	if config.ValidateKey(prefix, true) != nil || prefix != "" && !strings.HasSuffix(prefix, "/") || len(a.cfg.Prefix+prefix) > 1024 || len(cursor) > 8192 {
		fail(w, 400, "invalid_prefix", "올바르지 않은 폴더 경로입니다.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	value, status, e := a.cache.Get(ctx, prefix+"\x00"+cursor, r.URL.Query().Get("refresh") == "1", func(ctx context.Context) (backend.Listing, error) { return a.store.List(ctx, prefix, cursor) })
	if e != nil {
		a.upstreamFail(w, e)
		return
	}
	w.Header().Set("X-Listing-Cache", status)
	jsonOut(w, value)
}

// upstreamFail maps a storage error to a JSON response. Error codes keep their
// original s3_ names for API compatibility; messages name the configured backend.
func (a *App) upstreamFail(w http.ResponseWriter, e error) {
	var u *backend.UpstreamError
	if errors.As(e, &u) {
		switch u.Status {
		case 403:
			log.Printf("upstream denied backend=%s code=%q", a.cfg.Backend, u.Code)
			if a.s3 != nil {
				fail(w, 403, "s3_access_denied", "S3 접근이 거부되었습니다. 버킷·리전·자격 증명과 ListBucket/GetObject 권한을 확인해 주세요.")
			} else {
				fail(w, 403, "s3_access_denied", a.backendName()+" 접근이 거부되었습니다. 주소·계정·권한을 확인해 주세요.")
			}
			return
		case 404:
			fail(w, 404, "s3_not_found", "폴더 또는 파일을 찾을 수 없습니다.")
			return
		case 412:
			fail(w, 409, "object_changed", "파일이 변경되었습니다. 새로고침 후 다시 시도해 주세요.")
			return
		case 416:
			fail(w, 416, "invalid_range", "요청한 파일 범위가 올바르지 않습니다.")
			return
		}
	}
	if errors.Is(e, context.DeadlineExceeded) {
		fail(w, 504, "s3_timeout", a.backendName()+" 응답 시간이 초과되었습니다.")
		return
	}
	log.Printf("upstream error backend=%s type=%T", a.cfg.Backend, e)
	fail(w, 502, "s3_unavailable", "저장소에 연결할 수 없습니다. 서버 설정과 "+a.backendName()+" 주소를 확인해 주세요.")
}
func (a *App) backendName() string {
	switch a.cfg.Backend {
	case "webdav":
		return "WebDAV"
	case "ftp":
		return "FTP"
	case "sftp":
		return "SFTP"
	}
	return "S3"
}
func (a *App) object(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if config.ValidateKey(key, false) != nil || len(a.cfg.Prefix+key) > 1024 {
		fail(w, 400, "invalid_key", "올바르지 않은 파일 경로입니다.")
		return
	}
	download := r.URL.Query().Get("download") == "1"
	mode := a.cfg.PreviewMode
	if download {
		mode = a.cfg.DownloadMode
	}
	// Only GetObject gets a bearer URL. HEAD/listing remain server-side.
	if r.Method != http.MethodGet || a.renderHTML(key, download) {
		mode = "proxy"
	}
	w.Header().Set("X-Delivery-Mode", mode)
	if mode == "presigned" && a.s3 != nil {
		link, err := a.s3.Presign(r.Method, key, download, time.Now())
		if err != nil {
			a.upstreamFail(w, err)
			return
		}
		// No redirect body or access log contains the bearer URL. Sign the final
		// public endpoint, never replace its hostname after signing.
		w.Header().Set("Location", link)
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	if a.objectCacheEnabled {
		a.cachedObject(w, r, key, download)
		return
	}
	if a.s3 == nil {
		a.serveObject(w, r, key, download)
		return
	}
	a.objectPresentation(w, key, download)
	resp, e := a.s3.Object(r.Context(), r.Method, key, r.Header)
	if e != nil {
		a.upstreamFail(w, e)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 && resp.StatusCode != 304 {
		if resp.StatusCode == 416 {
			w.Header().Set("Content-Range", resp.Header.Get("Content-Range"))
		}
		a.upstreamFail(w, s3.ReadError(resp))
		return
	}
	for _, h := range []string{"Content-Length", "Content-Range", "ETag", "Last-Modified", "Accept-Ranges", "X-Cache"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.Header().Set("X-Cache", "BYPASS")
	w.WriteHeader(resp.StatusCode)
	if r.Method != "HEAD" && resp.StatusCode != 304 {
		if _, e = io.Copy(w, resp.Body); e != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

// serveObject delivers a file from a non-S3 backend, honoring HEAD, a single
// byte range, and ETag preconditions the way the S3 passthrough does.
func (a *App) serveObject(w http.ResponseWriter, r *http.Request, key string, download bool) {
	st, err := a.store.Stat(r.Context(), key)
	if err != nil {
		a.upstreamFail(w, err)
		return
	}
	if st.Directory {
		a.upstreamFail(w, &backend.UpstreamError{Status: 404, Code: "NoSuchKey"})
		return
	}
	h := w.Header()
	a.objectPresentation(w, key, download)
	h.Set("Accept-Ranges", "bytes")
	if st.ETag != "" {
		h.Set("ETag", st.ETag)
	}
	if !st.Modified.IsZero() {
		h.Set("Last-Modified", st.Modified.UTC().Format(http.TimeFormat))
	}
	if m := r.Header.Get("If-Match"); m != "" && !strongETagMatches(m, st.ETag) {
		a.upstreamFail(w, &backend.UpstreamError{Status: 412, Code: "PreconditionFailed"})
		return
	}
	if m := r.Header.Get("If-None-Match"); m != "" && (m == "*" || (etagMatches(m, st.ETag) && !(backend.SyntheticETag(st.ETag) && st.Modified.IsZero()))) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	offset, length, status := int64(0), st.Size, http.StatusOK
	if rng := r.Header.Get("Range"); rng != "" {
		if ir := r.Header.Get("If-Range"); ir == "" || (backend.StrongETag(ir) && ir == st.ETag) {
			start, end, ok := parseRange(rng, st.Size)
			if !ok {
				h.Set("Content-Range", fmt.Sprintf("bytes */%d", st.Size))
				a.upstreamFail(w, &backend.UpstreamError{Status: 416, Code: "InvalidRange"})
				return
			}
			offset, length, status = start, end-start+1, http.StatusPartialContent
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, st.Size))
		}
	}
	if r.Method == http.MethodHead {
		h.Set("Content-Length", strconv.FormatInt(length, 10))
		w.WriteHeader(status)
		return
	}
	body, err := a.store.Open(r.Context(), key, offset, length)
	if err != nil {
		h.Del("Content-Range")
		a.upstreamFail(w, err)
		return
	}
	defer body.Close()
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(status)
	if _, err = io.CopyN(w, body, length); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// parseRange accepts one "bytes=" range (RFC 9110 §14.1.2) and returns the
// inclusive interval. Multi-range requests are unsupported and reported as
// unsatisfiable rather than silently served in full.
func parseRange(spec string, size int64) (start, end int64, ok bool) {
	spec = strings.TrimSpace(spec)
	if !strings.HasPrefix(spec, "bytes=") || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(strings.TrimPrefix(spec, "bytes="), "-")
	if !found {
		return 0, 0, false
	}
	first, last = strings.TrimSpace(first), strings.TrimSpace(last)
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 || size == 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end = size - 1
	if last != "" {
		if end, err = strconv.ParseInt(last, 10, 64); err != nil || end < start {
			return 0, 0, false
		}
		if end > size-1 {
			end = size - 1
		}
	}
	return start, end, true
}

func strongETagMatches(list, etag string) bool {
	for _, tag := range strings.Split(list, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || (backend.StrongETag(tag) && backend.StrongETag(etag) && tag == etag) {
			return true
		}
	}
	return false
}

// etagMatches implements weak comparison of an If-None-Match list.
func etagMatches(list, etag string) bool {
	if etag == "" {
		return false
	}
	etag = strings.TrimPrefix(etag, "W/")
	for _, candidate := range strings.Split(list, ",") {
		if strings.TrimPrefix(strings.TrimSpace(candidate), "W/") == etag {
			return true
		}
	}
	return false
}
