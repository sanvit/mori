package webdav

import (
	"context"
	"net/http"
	"strconv"

	"mori/internal/backend"
	"mori/internal/media"
)

// Object preserves native HTTP validators/cache headers for the common cache.
func (c *Client) Object(ctx context.Context, method, key string, h http.Header) (*http.Response, error) {
	resp, err := c.request(ctx, method, key, "", h)
	if err != nil {
		return nil, err
	}
	if method == http.MethodHead && (resp.StatusCode == 405 || resp.StatusCode == 501 || resp.StatusCode == 200) {
		resp.Body.Close()
		st, e := c.Stat(ctx, key)
		if e != nil {
			return nil, e
		}
		header := resp.Header.Clone()
		header.Set("Content-Length", strconv.FormatInt(st.Size, 10))
		header.Set("ETag", st.ETag)
		if header.Get("Content-Type") == "" || resp.StatusCode != http.StatusOK {
			header.Set("Content-Type", media.FileType(key))
		}
		if backend.SyntheticETag(st.ETag) && st.Modified.IsZero() {
			header.Set("Cache-Control", "no-store")
		}
		if !st.Modified.IsZero() {
			header.Set("Last-Modified", st.Modified.UTC().Format(http.TimeFormat))
		} else {
			header.Del("Last-Modified")
		}
		status := http.StatusOK
		if st.Directory {
			status = http.StatusNotFound
		} else if h.Get("If-None-Match") == st.ETag && st.ETag != "" && !(backend.SyntheticETag(st.ETag) && st.Modified.IsZero()) {
			status = http.StatusNotModified
		}
		return &http.Response{StatusCode: status, Header: header, Body: http.NoBody, ContentLength: st.Size}, nil
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.StatusCode = http.StatusForbidden
	}
	return resp, nil
}

// PROPFIND's synthetic size/time identifier cannot be sent as a native
// If-Match. Those servers use the adapter's stat/open/stat validation instead.
func (c *Client) NativeObjectValidator(ctx context.Context, key, tag string) (bool, error) {
	return backend.StrongETag(tag), nil
}
