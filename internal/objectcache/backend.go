package objectcache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"mori/internal/backend"
	"mori/internal/media"
)

// ObjectOrigin is the byte/metadata contract used by the cache. Listing and
// presigning remain on the underlying backend, outside the body cache.
// Every read is conditional: an unversioned read passes an empty tag.
type ObjectOrigin interface {
	Head(ctx context.Context, key, ifNoneMatch string) (*OriginResponse, error)
	GetConditional(ctx context.Context, key, rangeHeader, ifMatch string) (*OriginResponse, error)
}

// OriginResponse is one backend read, shaped like an HTTP response so cached
// and uncached paths share the same header and body handling.
type OriginResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

type httpObjects interface {
	Object(context.Context, string, string, http.Header) (*http.Response, error)
}

type nativeValidators interface {
	NativeObjectValidator(context.Context, string, string) (bool, error)
}

type segmentReadKey struct{}

var errRangeUnsupported = errors.New("origin does not support byte ranges")

type BackendOrigin struct{ store backend.Backend }

func NewBackendOrigin(store backend.Backend) *BackendOrigin { return &BackendOrigin{store: store} }

func objectHeaders(st backend.Object) http.Header {
	h := make(http.Header)
	h.Set("Content-Length", strconv.FormatInt(st.Size, 10))
	h.Set("ETag", st.ETag)
	h.Set("Content-Type", media.FileType(st.Key))
	h.Set("Accept-Ranges", "bytes")
	if backend.SyntheticETag(st.ETag) && st.Modified.IsZero() {
		h.Set("Cache-Control", "no-store")
	}
	if !st.Modified.IsZero() {
		h.Set("Last-Modified", st.Modified.UTC().Format(http.TimeFormat))
	}
	return h
}

func originError(err error) (*OriginResponse, error) {
	var upstream *backend.UpstreamError
	if errors.As(err, &upstream) {
		return &OriginResponse{StatusCode: upstream.Status, Header: make(http.Header), Body: http.NoBody}, nil
	}
	return nil, err
}

func (o *BackendOrigin) Head(ctx context.Context, key, tag string) (*OriginResponse, error) {
	if native, ok := o.store.(httpObjects); ok {
		h := make(http.Header)
		if tag != "" {
			h.Set("If-None-Match", tag)
		}
		resp, err := native.Object(ctx, http.MethodHead, key, h)
		if err != nil {
			return originError(err)
		}
		return &OriginResponse{resp.StatusCode, resp.Header, resp.Body}, nil
	}
	st, err := o.store.Stat(ctx, key)
	if err != nil {
		return originError(err)
	}
	if st.Directory {
		return originError(&backend.UpstreamError{Status: 404})
	}
	status := http.StatusOK
	if tag != "" && tag == st.ETag && !(backend.SyntheticETag(st.ETag) && st.Modified.IsZero()) {
		status = http.StatusNotModified
	}
	return &OriginResponse{status, objectHeaders(st), http.NoBody}, nil
}

func (o *BackendOrigin) GetConditional(ctx context.Context, key, rng, tag string) (*OriginResponse, error) {
	native, nativeOK := o.store.(httpObjects)
	if capability, ok := o.store.(nativeValidators); ok {
		var err error
		nativeOK, err = capability.NativeObjectValidator(ctx, key, tag)
		if err != nil {
			return originError(err)
		}
	}
	if nativeOK {
		h := make(http.Header)
		if rng != "" {
			h.Set("Range", rng)
		}
		if tag != "" {
			h.Set("If-Match", tag)
		}
		resp, err := native.Object(ctx, http.MethodGet, key, h)
		if err != nil {
			return originError(err)
		}
		// Some HTTP origins ignore Range. Keep bounded reads compatible with
		// those servers without committing an entire response as one segment.
		if resp.StatusCode == http.StatusOK && rng != "" && !strings.Contains(rng, ",") {
			br, has, e := parseSingleRange(rng, resp.ContentLength)
			if e == nil && has {
				if filling, _ := ctx.Value(segmentReadKey{}).(bool); filling && (br.Start != 0 || br.End != resp.ContentLength-1) {
					resp.Body.Close()
					return nil, errRangeUnsupported
				}
				if _, e = io.CopyN(io.Discard, resp.Body, br.Start); e != nil {
					resp.Body.Close()
					return nil, e
				}
				n := br.End - br.Start + 1
				resp.Header = resp.Header.Clone()
				resp.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, resp.ContentLength))
				resp.Header.Set("Content-Length", strconv.FormatInt(n, 10))
				resp.StatusCode = http.StatusPartialContent
				resp.Body = backend.Reader{Reader: io.LimitReader(resp.Body, n), Closer: resp.Body}
			}
		}
		return &OriginResponse{resp.StatusCode, resp.Header, resp.Body}, nil
	}
	st, err := o.store.Stat(ctx, key)
	if err != nil {
		return originError(err)
	}
	if st.Directory {
		return originError(&backend.UpstreamError{Status: 404})
	}
	if tag != "" && tag != "*" && tag != st.ETag {
		return originError(&backend.UpstreamError{Status: 412})
	}
	if strings.Contains(rng, ",") {
		return originError(&backend.UpstreamError{Status: 416})
	}
	br, hasRange, err := parseSingleRange(rng, st.Size)
	if err != nil {
		return originError(&backend.UpstreamError{Status: 416})
	}
	offset, length, status := int64(0), st.Size, http.StatusOK
	h := objectHeaders(st)
	if hasRange {
		offset, length, status = br.Start, br.End-br.Start+1, http.StatusPartialContent
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", br.Start, br.End, st.Size))
		h.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	body, err := o.store.Open(ctx, key, offset, length)
	if err != nil {
		return originError(err)
	}
	v := &verifiedBody{body: body, remaining: length, ctx: ctx, verify: func() error {
		now, e := o.store.Stat(ctx, key)
		if e != nil {
			return e
		}
		if now.Size != st.Size || now.ETag != st.ETag || now.Directory {
			return &backend.UpstreamError{Status: 412, Code: "ObjectChanged"}
		}
		return nil
	}}
	v.stop = context.AfterFunc(ctx, func() { v.closeBody() })
	return &OriginResponse{status, h, v}, nil
}

// Release the transfer before re-statting (important for bounded FTP pools).
// A post-read mismatch is a stream error, never a successful EOF.
type verifiedBody struct {
	body      io.ReadCloser
	remaining int64
	ctx       context.Context
	verify    func() error
	once      sync.Once
	stop      func() bool
	finished  bool
}

func (v *verifiedBody) closeBody() { v.once.Do(func() { _ = v.body.Close() }) }
func (v *verifiedBody) Close() error {
	if v.stop != nil {
		v.stop()
	}
	v.closeBody()
	return nil
}
func (v *verifiedBody) Read(p []byte) (int, error) {
	if err := v.ctx.Err(); err != nil {
		return 0, err
	}
	if v.finished {
		return 0, io.EOF
	}
	if v.remaining == 0 {
		v.Close()
		if err := v.verify(); err != nil {
			return 0, err
		}
		v.finished = true
		return 0, io.EOF
	}
	if int64(len(p)) > v.remaining {
		p = p[:v.remaining]
	}
	n, err := v.body.Read(p)
	v.remaining -= int64(n)
	if err == io.EOF {
		if v.remaining != 0 {
			return n, io.ErrUnexpectedEOF
		}
		err = nil
	}
	return n, err
}
