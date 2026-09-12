package objectcache

import (
	"context"
	"io"
	"net/http"
	"time"
)

// testOriginServer is an unauthenticated HTTP origin used by the cache tests.
// Signing and credential handling belong to the storage backends, so these
// tests exercise only the caching contract: conditional reads, ranges and the
// status codes the Proxy reacts to.
type testOriginServer struct {
	baseURL string
	client  *http.Client
}

func newTestOriginServer(baseURL string) *testOriginServer {
	return &testOriginServer{baseURL: baseURL, client: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableCompression: true},
	}}
}

func (o *testOriginServer) do(ctx context.Context, method, key string, h http.Header) (*OriginResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, o.baseURL+"/"+escapeOriginPath(key), nil)
	if err != nil {
		return nil, err
	}
	req.Header = h
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	if method == http.MethodHead {
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(http.NoBody)
	}
	return &OriginResponse{StatusCode: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

func (o *testOriginServer) Head(ctx context.Context, key, ifNoneMatch string) (*OriginResponse, error) {
	h := make(http.Header)
	if ifNoneMatch != "" {
		h.Set("If-None-Match", ifNoneMatch)
	}
	return o.do(ctx, http.MethodHead, key, h)
}

func (o *testOriginServer) GetConditional(ctx context.Context, key, rangeHeader, etag string) (*OriginResponse, error) {
	h := make(http.Header)
	if rangeHeader != "" {
		h.Set("Range", rangeHeader)
	}
	if etag != "" {
		h.Set("If-Match", etag)
	}
	return o.do(ctx, http.MethodGet, key, h)
}

func escapeOriginPath(key string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(key))
	for i := 0; i < len(key); i++ {
		c := key[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			out = append(out, c)
		case c == '-' || c == '_' || c == '.' || c == '~' || c == '/':
			out = append(out, c)
		default:
			out = append(out, '%', hex[c>>4], hex[c&15])
		}
	}
	return string(out)
}
