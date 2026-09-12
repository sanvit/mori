package objectcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mori/internal/backend"
	"mori/internal/media"
)

// responseTracker records whether anything has already reached the client. Once a
// status line or a single body byte is out, the proxy can no longer replace the
// response with an error page: doing so appends the error document to a partial
// body and re-sends the status line.
type responseTracker struct {
	http.ResponseWriter
	started bool
	status  int
	written int64
	head    bool
}

func (t *responseTracker) WriteHeader(status int) {
	if !t.started {
		t.status = status
	}
	t.started = true
	t.ResponseWriter.WriteHeader(status)
}

func (t *responseTracker) Write(b []byte) (int, error) {
	if t.head {
		if !t.started {
			t.WriteHeader(http.StatusOK)
		}
		return len(b), nil
	}
	t.started = true
	n, err := t.ResponseWriter.Write(b)
	t.written += int64(n)
	return n, err
}

// Flush keeps the wrapped writer usable by the Range streaming path, which relies
// on http.Flusher to keep time-to-first-byte low.
func (t *responseTracker) Flush() {
	if f, ok := t.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

type Proxy struct {
	cfg         Config
	origin      ObjectOrigin
	cache       *DiskCache
	fills       segmentFills
	originSlots chan struct{}
}

func NewProxy(cfg Config, origin ObjectOrigin, cache *DiskCache) *Proxy {
	if cfg.CacheDownloadConcurrency <= 0 {
		cfg.CacheDownloadConcurrency = 4
	}
	if cfg.OriginMaxConcurrentRequests <= 0 {
		cfg.OriginMaxConcurrentRequests = 32
	}
	if cfg.OriginFetchMaxSize <= 0 {
		cfg.OriginFetchMaxSize = 16 * 1024 * 1024
	}
	if cfg.OriginFetchMaxSize < cfg.CacheSegmentSize {
		cfg.OriginFetchMaxSize = cfg.CacheSegmentSize
	}
	return &Proxy{cfg: cfg, origin: origin, cache: cache, originSlots: make(chan struct{}, cfg.OriginMaxConcurrentRequests)}
}

func (p *Proxy) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w := &responseTracker{ResponseWriter: rw, status: http.StatusOK, head: r.Method == http.MethodHead}
	reqPath := r.URL.Path
	if !validObjectPath(reqPath) {
		p.serveDefaultError(w, http.StatusBadRequest)
		return
	}

	// Answered before anything else and kept out of the access log, so a liveness
	// probe every few seconds does not bury the interesting lines.
	if p.cfg.HealthPath != "" && reqPath == p.cfg.HealthPath {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			p.serveDefaultError(w, http.StatusMethodNotAllowed)
			return
		}
		p.serveHealth(w, r)
		return
	}

	if p.cfg.AccessLog {
		start := time.Now()
		defer func() { p.logAccess(r, w, start) }()
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodOptions:
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
		p.serveDefaultError(w, http.StatusMethodNotAllowed)
		return
	}

	// A trailing slash addresses a directory, which object storage has no body
	// for. Resolve it to the index document the way static website hosting does,
	// so "/" serves "/index.html" and "/docs/" serves "/docs/index.html". Without
	// this the backend answers a listing, which is not a page and -- being a 200
	// -- never reaches the SPA fallback either.
	if p.cfg.IndexDocument != "" && (reqPath == "/" || strings.HasSuffix(r.URL.Path, "/")) {
		reqPath = normalizeWebPath(strings.TrimSuffix(reqPath, "/") + "/" + p.cfg.IndexDocument)
	}

	mode := p.queryModeFor(reqPath)
	ck := cacheKeyFor(reqPath, r.URL.RawQuery, mode)
	status, err := p.serveObject(w, r, reqPath, ck, 0, false)
	if err == nil {
		return
	}
	if w.started {
		// Bytes are already on the wire. net/http closes the connection because the
		// promised Content-Length is not met, which is what signals the truncation
		// to the client; anything written here would corrupt the body instead.
		log.Printf("%s %s: response failed after it started: %v", r.Method, reqPath, err)
		panic(http.ErrAbortHandler)
	}

	if status == http.StatusNotFound {
		if p.cfg.SPAMode && p.isSPANavigation(r, reqPath) && reqPath != p.cfg.SPAIndex {
			clearObjectHeaders(w.Header())
			spaKey := cacheKeyFor(p.cfg.SPAIndex, "", "ignore")
			if st, spaErr := p.serveObject(w, fallbackRequest(r), p.cfg.SPAIndex, spaKey, http.StatusOK, true); spaErr == nil && st < 400 {
				return
			}
			if w.started {
				panic(http.ErrAbortHandler)
			}
		}
		p.serveErrorPage(w, r, http.StatusNotFound)
		return
	}

	if status >= 400 {
		p.serveErrorPage(w, r, status)
		return
	}
	p.serveErrorPage(w, r, http.StatusBadGateway)
}

func (p *Proxy) serveHealth(w http.ResponseWriter, r *http.Request) {
	used, max, blocks := p.Stats()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = fmt.Fprintf(w, "{\"status\":\"ok\",\"cache_bytes\":%d,\"cache_max_bytes\":%d,\"cache_blocks\":%d}\n", used, max, blocks)
}

func (p *Proxy) logAccess(r *http.Request, w *responseTracker, start time.Time) {
	cacheStatus := w.Header().Get("X-Cache")
	if cacheStatus == "" {
		cacheStatus = "-"
	}
	// EscapedPath rather than Path: net/http has already percent-decoded Path, so a
	// request for "/%0Afake-log-line" would otherwise inject a newline into the log.
	// The query string is left out entirely because it routinely carries signed URL
	// tokens, and it is already reflected in the cache key.
	log.Printf("%s %s %d %d %s %s", r.Method, r.URL.EscapedPath(), w.status, w.written, cacheStatus, time.Since(start).Round(time.Millisecond))
}

func (p *Proxy) queryModeFor(requestPath string) string {
	for _, rule := range p.cfg.CacheRules {
		if ruleMatches(rule, requestPath) {
			if rule.IgnoreQuery {
				return "ignore"
			}
			break
		}
	}
	return p.cfg.CacheQueryMode
}

func (p *Proxy) serveObject(w http.ResponseWriter, r *http.Request, requestPath, cacheKey string, forcedStatus int, internal bool) (int, error) {
	if !p.cfg.CacheEnabled || p.cache == nil || !cachePolicyFor(p.cfg, requestPath, nil).Cacheable || (requestPath == "/" && p.cfg.IndexDocument == "") {
		return p.serveUncached(w, r, requestPath, cacheKey, forcedStatus)
	}
	meta, _ := p.cache.LoadMeta(cacheKey)
	fresh, _ := r.Context().Value(freshReadKey{}).(bool)
	now := time.Now()
	if !fresh && meta != nil && meta.isNegative() && now.Before(meta.ExpiresAt) {
		return meta.StatusCode, fmt.Errorf("cached origin HEAD returned %d", meta.StatusCode)
	}
	if fresh || meta == nil || (!meta.isNegative() && !meta.Cacheable) || !now.Before(meta.ExpiresAt) {
		ifNoneMatch := ""
		if meta != nil && !meta.isNegative() {
			ifNoneMatch = meta.ETag
		}
		head, err := p.origin.Head(r.Context(), originKey(requestPath), ifNoneMatch)
		if err != nil {
			if !fresh && meta != nil && !meta.isNegative() && now.Before(meta.ExpiresAt.Add(p.cfg.CacheStaleIfError)) && p.cache.AllSegmentsPresent(meta) {
				setMetaHeaders(w, meta)
				w.Header().Set("X-Cache", "STALE")
				return p.serveKnown(w, r, meta, forcedStatus)
			}
			if isTimeout(err) {
				return http.StatusGatewayTimeout, err
			}
			return http.StatusBadGateway, err
		}
		// A HEAD carries no body, so release the pooled connection now rather than
		// holding it for the lifetime of a possibly long streaming response.
		_ = head.Body.Close()

		if head.StatusCode == http.StatusNotModified && meta != nil && !meta.isNegative() {
			merged := metadataHeaders(meta)
			for k, v := range head.Header {
				merged[k] = v
			}
			updated := metaFromHead(p.cfg, requestPath, cacheKey, merged, now)
			updated.Version = meta.Version
			meta = updated
			policy := cachePolicyFor(p.cfg, requestPath, map[string][]string(merged))
			meta.CachedAt = now
			meta.ExpiresAt = now.Add(policy.TTL)
			_ = p.cache.SaveMeta(meta)
		} else if head.StatusCode >= 200 && head.StatusCode < 300 {
			meta = metaFromHead(p.cfg, requestPath, cacheKey, head.Header, now)
			if err := p.cache.SaveMeta(meta); err != nil {
				log.Printf("cache metadata write failed: %v", err)
			}
		} else {
			if head.StatusCode >= 500 && !fresh && meta != nil && !meta.isNegative() && now.Before(meta.ExpiresAt.Add(p.cfg.CacheStaleIfError)) && p.cache.AllSegmentsPresent(meta) {
				setMetaHeaders(w, meta)
				w.Header().Set("X-Cache", "STALE")
				return p.serveKnown(w, r, meta, forcedStatus)
			}
			if ttl := p.negativeTTL(head.StatusCode); ttl > 0 {
				negative := &ObjectMeta{
					CacheKey: cacheKey, OriginKey: originKey(requestPath),
					CachedAt: now, ExpiresAt: now.Add(ttl), StatusCode: head.StatusCode,
				}
				if err := p.cache.SaveMeta(negative); err != nil {
					log.Printf("negative cache metadata write failed: %v", err)
				}
			}
			return head.StatusCode, fmt.Errorf("origin HEAD returned %d", head.StatusCode)
		}
	}

	if meta == nil || meta.isNegative() {
		return http.StatusBadGateway, errors.New("metadata unavailable")
	}

	return p.serveKnown(w, r, meta, forcedStatus)
}

func (p *Proxy) serveKnown(w http.ResponseWriter, r *http.Request, meta *ObjectMeta, forcedStatus int) (int, error) {
	if expected, ok := r.Context().Value(expectedReadKey{}).(string); ok && expected != meta.ETag {
		return http.StatusPreconditionFailed, errors.New("object changed since ZIP planning")
	}
	setMetaHeaders(w, meta)
	if meta.BrowserTTLSet || meta.BrowserTTL > 0 {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(meta.BrowserTTL, 10))
	}
	if forcedStatus == 0 && !clientPreconditions(r, meta) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusPreconditionFailed)
		return http.StatusPreconditionFailed, nil
	}
	if clientNotModified(r, meta) && forcedStatus == 0 {
		w.WriteHeader(http.StatusNotModified)
		return http.StatusNotModified, nil
	}

	if !meta.Cacheable || !p.cfg.CacheEnabled {
		return p.streamDirect(w, r, meta, forcedStatus)
	}

	if r.Method == http.MethodHead {
		st := forcedStatus
		if st == 0 {
			st = http.StatusOK
		}
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
		w.Header().Set("X-Cache", "META")
		w.WriteHeader(st)
		return st, nil
	}

	r = rangeRequest(r, meta)
	rawRange := r.Header.Get("Range")
	if strings.Contains(rawRange, ",") {
		return p.streamDirect(w, r, meta, forcedStatus)
	}
	br, hasRange, rangeErr := parseSingleRange(rawRange, meta.Size)
	if rangeErr != nil && rawRange != "" {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", meta.Size))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return http.StatusRequestedRangeNotSatisfiable, nil
	}
	if hasRange {
		return p.serveRange(w, r, meta, br, forcedStatus)
	}

	if p.cache.AllSegmentsPresent(meta) {
		if w.Header().Get("X-Cache") != "STALE" {
			w.Header().Set("X-Cache", "HIT")
		}
		return p.serveCachedFull(w, r, meta, forcedStatus)
	}
	return p.streamAndPopulate(w, r, meta, forcedStatus)
}

func (p *Proxy) negativeTTL(status int) time.Duration {
	switch status {
	case http.StatusNotFound:
		return p.cfg.CacheNegativeTTL404
	case http.StatusForbidden:
		return p.cfg.CacheNegativeTTL403
	default:
		return 0
	}
}

func metaFromHead(cfg Config, requestPath, cacheKey string, h http.Header, now time.Time) *ObjectMeta {
	size, sizeErr := strconv.ParseInt(h.Get("Content-Length"), 10, 64)
	lm, _ := http.ParseTime(h.Get("Last-Modified"))
	policy := cachePolicyFor(cfg, requestPath, map[string][]string(h))
	if sizeErr != nil || size < 0 {
		size = -1
		policy.Cacheable = false
	}
	etag := h.Get("ETag")
	if backend.SyntheticETag(etag) && lm.IsZero() {
		policy.Cacheable = false
	}
	version := hashString(etag + "|" + strconv.FormatInt(size, 10) + "|" + h.Get("Last-Modified"))[:24]
	m := &ObjectMeta{
		CacheKey: cacheKey, OriginKey: originKey(requestPath), ETag: etag,
		LastModified: lm, Size: size, ContentType: h.Get("Content-Type"), CacheControl: h.Get("Cache-Control"),
		ContentEncoding: h.Get("Content-Encoding"), ContentDisposition: h.Get("Content-Disposition"),
		ExpiresHeader: h.Get("Expires"), CachedAt: now, ExpiresAt: now.Add(policy.TTL), Version: version, Cacheable: policy.Cacheable,
	}
	if policy.BrowserTTL != nil {
		m.BrowserTTLSet = true
		m.BrowserTTL = int64(policy.BrowserTTL.Seconds())
	}
	if m.ContentType == "" {
		m.ContentType = mime.TypeByExtension(filepath.Ext(requestPath))
	}
	return m
}

func setMetaHeaders(w http.ResponseWriter, m *ObjectMeta) {
	if m.ContentEncoding != "" {
		w.Header().Set("Content-Encoding", m.ContentEncoding)
	}
	w.Header().Set("Content-Disposition", media.Disposition(m.ContentDisposition, m.OriginKey))
	if m.ContentType != "" {
		w.Header().Set("Content-Type", m.ContentType)
	}
	if m.ETag != "" {
		w.Header().Set("ETag", m.ETag)
	}
	if !m.LastModified.IsZero() {
		w.Header().Set("Last-Modified", m.LastModified.UTC().Format(http.TimeFormat))
	}
	if m.CacheControl != "" {
		w.Header().Set("Cache-Control", m.CacheControl)
	}
	if m.ExpiresHeader != "" {
		w.Header().Set("Expires", m.ExpiresHeader)
	}
	w.Header().Set("Accept-Ranges", "bytes")
	age := int64(time.Since(m.CachedAt).Seconds())
	if age < 0 {
		age = 0
	}
	w.Header().Set("Age", strconv.FormatInt(age, 10))
}

func (p *Proxy) streamDirect(w http.ResponseWriter, r *http.Request, m *ObjectMeta, forcedStatus int) (int, error) {
	if r.Method == http.MethodHead {
		status := forcedStatus
		if status == 0 {
			status = http.StatusOK
		}
		if m.Size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
		} else {
			w.Header().Del("Content-Length")
		}
		w.Header().Set("X-Cache", "BYPASS")
		w.WriteHeader(status)
		return status, nil
	}
	r = rangeRequest(r, m)
	resp, err := p.origin.GetConditional(r.Context(), m.OriginKey, r.Header.Get("Range"), m.ETag)
	if err != nil {
		if isTimeout(err) {
			return http.StatusGatewayTimeout, err
		}
		return http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return resp.StatusCode, fmt.Errorf("origin GET returned %d", resp.StatusCode)
	}
	if originVersionChanged(m, resp.Header) {
		return http.StatusPreconditionFailed, fmt.Errorf("origin object changed")
	}
	copyOriginHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Disposition", media.Disposition(w.Header().Get("Content-Disposition"), m.OriginKey))
	if m.BrowserTTLSet {
		w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(m.BrowserTTL, 10))
	}
	status := resp.StatusCode
	if forcedStatus != 0 && resp.StatusCode < 400 {
		status = forcedStatus
	}
	w.Header().Set("X-Cache", "BYPASS")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		err = copyVerified(w, resp.Body)
	}
	return status, err
}

func (p *Proxy) streamAndPopulate(w http.ResponseWriter, r *http.Request, m *ObjectMeta, forcedStatus int) (int, error) {
	w.Header().Set("X-Cache", "MISS")
	status := forcedStatus
	if status == 0 {
		status = http.StatusOK
	}
	return p.serveSegments(w, r, m, ByteRange{Start: 0, End: m.Size - 1}, status)
}

// originVersionChanged reports whether a body response came from a different
// object version than the metadata this request was planned against.
func originVersionChanged(m *ObjectMeta, h http.Header) bool {
	etag := h.Get("ETag")
	return etag != "" && m.ETag != "" && etag != m.ETag
}

// invalidateMeta expires the cached metadata so the next request revalidates
// against the origin. Without it a detected version change would repeat on every
// retry until the TTL ran out, since each retry would plan against the same
// stale metadata and reach the same mismatch.
func (p *Proxy) invalidateMeta(m *ObjectMeta) {
	if err := p.cache.ExpireMetaIfVersion(m.CacheKey, m.Version); err != nil {
		log.Printf("cache metadata invalidation failed for %s: %v", m.CacheKey, err)
	}
}

func (p *Proxy) serveCachedFull(w http.ResponseWriter, r *http.Request, m *ObjectMeta, forcedStatus int) (int, error) {
	status := forcedStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead || m.Size == 0 {
		return status, nil
	}
	n := (m.Size + p.cfg.CacheSegmentSize - 1) / p.cfg.CacheSegmentSize
	for i := int64(0); i < n; i++ {
		f, err := p.cache.OpenSegment(m.CacheKey, m.Version, i)
		if err != nil {
			return status, err
		}
		_, cpErr := io.CopyBuffer(w, f, make([]byte, 128*1024))
		f.Close()
		if cpErr != nil {
			return status, cpErr
		}
	}
	return status, nil
}

func (p *Proxy) serveRange(w http.ResponseWriter, r *http.Request, m *ObjectMeta, br ByteRange, forcedStatus int) (int, error) {
	length := br.End - br.Start + 1
	status := http.StatusPartialContent
	if forcedStatus != 0 {
		status = forcedStatus
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, m.Size))
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))

	first := br.Start / p.cfg.CacheSegmentSize
	last := br.End / p.cfg.CacheSegmentSize
	allHit := true
	for idx := first; idx <= last; idx++ {
		if !p.cache.HasSegment(m.CacheKey, m.Version, idx) {
			allHit = false
			break
		}
	}
	if w.Header().Get("X-Cache") != "STALE" {
		if allHit {
			w.Header().Set("X-Cache", "RANGE-HIT")
		} else {
			w.Header().Set("X-Cache", "RANGE-MISS")
		}
	}

	return p.serveSegments(w, r, m, br, status)
}

func (p *Proxy) streamCachedSegmentRange(w io.Writer, m *ObjectMeta, idx, offset, length int64) error {
	f, err := p.cache.OpenSegment(m.CacheKey, m.Version, idx)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buf := make([]byte, 128*1024)
	_, err = io.CopyBuffer(w, io.LimitReader(f, length), buf)
	return err
}

// fetchSegmentRunStreaming validates one contiguous origin response, splits it
// at aligned segment boundaries, and commits each complete segment separately.
// Earlier segments remain usable if a later segment is truncated or cancelled.
func (p *Proxy) fetchSegmentRunStreaming(ctx context.Context, m *ObjectMeta, indices []int64, fills []*segmentFill) (retErr error) {
	defer func() {
		var changed *backend.UpstreamError
		if errors.As(retErr, &changed) && (changed.Status == 412 || changed.Status == 404) {
			p.invalidateMeta(m)
			for _, idx := range indices {
				p.cache.DiscardSegment(m.CacheKey, m.Version, idx)
			}
		}
	}()
	if len(indices) == 0 || len(indices) != len(fills) {
		return errors.New("invalid empty or mismatched segment run")
	}
	first, last := indices[0], indices[len(indices)-1]
	segmentStart := first * p.cfg.CacheSegmentSize
	segmentEnd := minInt64((last+1)*p.cfg.CacheSegmentSize-1, m.Size-1)
	rangeHeader := fmt.Sprintf("bytes=%d-%d", segmentStart, segmentEnd)
	resp, err := p.origin.GetConditional(context.WithValue(ctx, segmentReadKey{}, true), m.OriginKey, rangeHeader, m.ETag)
	if err != nil {
		if errors.Is(err, errRangeUnsupported) {
			updated := *m
			updated.Cacheable = false
			_ = p.cache.SaveMeta(&updated)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusPreconditionFailed {
		p.invalidateMeta(m)
		return fmt.Errorf("origin object changed (If-Match failed)")
	}
	if resp.StatusCode == http.StatusNotFound {
		p.invalidateMeta(m)
		return fmt.Errorf("origin object disappeared during range fetch")
	}
	wholeObject := segmentStart == 0 && segmentEnd == m.Size-1
	if resp.StatusCode != http.StatusPartialContent && !(wholeObject && resp.StatusCode == http.StatusOK) {
		return fmt.Errorf("origin segment range GET returned %d", resp.StatusCode)
	}

	// Segments already cached under this version came from the object the HEAD
	// described. Splicing a segment from a newer object in beside them would hand
	// the client a body assembled from two different objects, which is worse than
	// failing: the request is dropped and the next one replans against fresh
	// metadata.
	if originVersionChanged(m, resp.Header) {
		p.invalidateMeta(m)
		return fmt.Errorf("object %s changed while a range response was being assembled", m.OriginKey)
	}

	if resp.StatusCode == http.StatusPartialContent && resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", segmentStart, segmentEnd, m.Size) {
		return fmt.Errorf("invalid origin Content-Range")
	}
	if length := resp.Header.Get("Content-Length"); length != "" && length != strconv.FormatInt(segmentEnd-segmentStart+1, 10) {
		return fmt.Errorf("invalid origin Content-Length")
	}
	for i, idx := range indices {
		start := idx * p.cfg.CacheSegmentSize
		end := minInt64(start+p.cfg.CacheSegmentSize-1, m.Size-1)
		expectedSize := end - start + 1
		var body io.Reader = resp.Body
		if len(indices) > 1 {
			body = &io.LimitedReader{R: resp.Body, N: expectedSize}
		}
		err := p.cache.StoreSegmentStreaming(m.CacheKey, m.Version, idx, expectedSize, body, func(_ int64, chunk []byte) error {
			n, writeErr := fills[i].Write(chunk)
			if writeErr == nil && n != len(chunk) {
				return io.ErrShortWrite
			}
			return writeErr
		})
		if err != nil {
			return err
		}
		// The final fill waits until the entire run length is validated below.
		// All preceding segment boundaries are already exact and may be released.
		if i+1 < len(indices) {
			finishSegmentFill(fills[i], nil)
		}
	}

	if len(indices) > 1 {
		var extra [1]byte
		n, readErr := io.ReadFull(resp.Body, extra[:])
		if n != 0 || readErr != io.EOF {
			// The advertised run ended at a segment boundary. Extra bytes make the
			// origin response ambiguous, so do not retain any segment from this GET.
			for _, idx := range indices {
				p.cache.DiscardSegment(m.CacheKey, m.Version, idx)
			}
			if readErr != nil && readErr != io.EOF {
				return fmt.Errorf("origin fetch validation: %w", readErr)
			}
			return fmt.Errorf("origin fetch run exceeds expected size %d", segmentEnd-segmentStart+1)
		}
	}
	finishSegmentFill(fills[len(fills)-1], nil)
	return nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (p *Proxy) isSPANavigation(r *http.Request, reqPath string) bool {
	if !p.cfg.SPAMode || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	wantsHTML := strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") || strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document")
	if !wantsHTML {
		return false
	}
	if p.cfg.SPAAllowDottedRoutes {
		return true
	}
	ext := strings.ToLower(filepath.Ext(reqPath))
	return ext == "" || ext == ".html"
}

func (p *Proxy) serveErrorPage(w *responseTracker, r *http.Request, status int) {
	if w.started {
		return
	}
	clearObjectHeaders(w.Header())
	if page, ok := p.cfg.ErrorPages[status]; ok && page != "" {
		ck := cacheKeyFor("__error__"+page, "", "ignore")
		if st, err := p.serveObject(w, fallbackRequest(r), page, ck, status, true); err == nil && st < 600 {
			return
		}
		if w.started {
			panic(http.ErrAbortHandler)
		}
	}
	p.serveDefaultError(w, status)
}

// serveDefaultError writes the built-in error document. It is a no-op once the
// response has started, so a failed custom error page cannot be appended to the
// partial body it already emitted.
func (p *Proxy) serveDefaultError(w *responseTracker, status int) {
	if w.started {
		return
	}
	clearObjectHeaders(w.Header())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if w.head {
		return
	}
	_, _ = fmt.Fprintf(w, "<!doctype html><html><head><title>%d %s</title></head><body><h1>%d %s</h1></body></html>", status, http.StatusText(status), status, http.StatusText(status))
}

func clearObjectHeaders(h http.Header) {
	for _, k := range []string{"Content-Length", "Content-Range", "Content-Encoding", "Content-Disposition", "ETag", "Last-Modified", "Expires", "Age", "X-Cache", "Accept-Ranges"} {
		h.Del(k)
	}
}

func copyOriginHeaders(dst, src http.Header) {
	allowed := []string{"Content-Type", "Content-Encoding", "Content-Length", "Content-Range", "ETag", "Last-Modified", "Cache-Control", "Expires", "Content-Disposition", "Accept-Ranges"}
	for _, k := range allowed {
		if v := src.Values(k); len(v) > 0 {
			dst.Del(k)
			for _, x := range v {
				dst.Add(k, x)
			}
		}
	}
}

func clientNotModified(r *http.Request, m *ObjectMeta) bool {
	if backend.SyntheticETag(m.ETag) && m.LastModified.IsZero() && strings.TrimSpace(r.Header.Get("If-None-Match")) != "*" {
		return false
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, tag := range strings.Split(inm, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || (m.ETag != "" && strings.TrimPrefix(tag, "W/") == strings.TrimPrefix(m.ETag, "W/")) {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" && !m.LastModified.IsZero() {
		if t, err := http.ParseTime(ims); err == nil && !m.LastModified.Truncate(time.Second).After(t) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
