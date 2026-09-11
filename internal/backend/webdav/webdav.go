// Package webdav reads a WebDAV collection with PROPFIND (Depth 0/1) and
// ranged GET. Only HTTP Basic authentication is supported, so use HTTPS.
package webdav

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"mori-s3/internal/backend"
	"mori-s3/internal/config"
	"mori-s3/internal/media"
)

const propfindBody = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/><D:getcontentlength/><D:getlastmodified/><D:getetag/></D:prop></D:propfind>`

// maxDirectories bounds a recursive walk so a hostile server cannot make the
// ZIP planner enumerate forever.
const maxDirectories = 10000

type Client struct {
	base     url.URL // scheme://host/base-path with no trailing slash
	user     string
	password string
	http     *http.Client
}

func New(c config.WebDAVConfig) *Client {
	base := *c.URL
	base.Path = strings.TrimSuffix(base.Path, "/")
	base.RawPath = ""
	return &Client{base: base, user: c.Username, password: c.Password, http: &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 64, MaxIdleConnsPerHost: 32, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, ForceAttemptHTTP2: true, DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type resource struct {
	path     string // cleaned, decoded absolute URL path ("/" for the server root)
	dir      bool
	size     int64
	modified time.Time
	etag     string
}

// target is the cleaned absolute URL path of key, comparable with resource.path.
func (c *Client) target(key string) string {
	return path.Clean("/" + c.base.Path + "/" + key)
}
func (c *Client) request(ctx context.Context, method, key string, body string, header http.Header) (*http.Response, error) {
	u := c.base
	u.Path = c.base.Path + "/" + key
	segments := strings.Split(u.Path, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	u.RawPath = strings.Join(segments, "/")
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.password)
	}
	return c.http.Do(req)
}
func statusError(resp *http.Response) error {
	code := resp.Status
	switch resp.StatusCode {
	case 401, 403:
		return &backend.UpstreamError{Status: 403, Code: code}
	case 404, 405, 410:
		return &backend.UpstreamError{Status: 404, Code: code}
	case 412, 416:
		return &backend.UpstreamError{Status: resp.StatusCode, Code: code}
	}
	return &backend.UpstreamError{Status: 502, Code: code}
}

func (c *Client) propfind(ctx context.Context, key, depth string) ([]resource, error) {
	resp, err := c.request(ctx, "PROPFIND", key, propfindBody, http.Header{"Depth": {depth}, "Content-Type": {"application/xml; charset=utf-8"}})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, statusError(resp)
	}
	var ms struct {
		XMLName   xml.Name `xml:"DAV: multistatus"`
		Responses []struct {
			Href      string `xml:"DAV: href"`
			Propstats []struct {
				Status string `xml:"DAV: status"`
				Prop   struct {
					ResourceType struct {
						Collection *struct{} `xml:"DAV: collection"`
					} `xml:"DAV: resourcetype"`
					Length   string `xml:"DAV: getcontentlength"`
					Modified string `xml:"DAV: getlastmodified"`
					ETag     string `xml:"DAV: getetag"`
				} `xml:"DAV: prop"`
			} `xml:"DAV: propstat"`
		} `xml:"DAV: response"`
	}
	if err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&ms); err != nil {
		return nil, fmt.Errorf("invalid WebDAV multistatus: %w", err)
	}
	out := make([]resource, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		href, err := url.Parse(strings.TrimSpace(r.Href))
		if err != nil {
			return nil, fmt.Errorf("invalid WebDAV href: %w", err)
		}
		if href.Host != "" && !strings.EqualFold(href.Host, c.base.Host) {
			continue
		}
		res := resource{path: path.Clean("/" + href.Path)}
		for _, ps := range r.Propstats {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200 ") {
				continue
			}
			p := ps.Prop
			if p.ResourceType.Collection != nil {
				res.dir = true
			}
			if p.Length != "" {
				res.size, _ = strconv.ParseInt(strings.TrimSpace(p.Length), 10, 64)
			}
			if p.Modified != "" {
				res.modified, _ = http.ParseTime(strings.TrimSpace(p.Modified))
			}
			if t := strings.TrimSpace(p.ETag); t != "" && !strings.HasPrefix(t, "W/") {
				res.etag = t
			}
		}
		if !res.dir && res.etag == "" {
			res.etag = backend.VersionTag(res.size, res.modified)
		}
		out = append(out, res)
	}
	return out, nil
}

// children returns the direct children of the collection at prefix.
func (c *Client) children(ctx context.Context, prefix string) ([]resource, error) {
	resources, err := c.propfind(ctx, prefix, "1")
	if err != nil {
		return nil, err
	}
	parent := c.target(prefix)
	out := resources[:0]
	for _, r := range resources {
		if r.path == parent || path.Dir(r.path) != parent {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dir != out[j].dir {
			return out[i].dir
		}
		return out[i].path < out[j].path
	})
	return out, nil
}

func (c *Client) List(ctx context.Context, prefix, cursor string) (backend.Listing, error) {
	l := backend.Listing{Prefix: prefix, Entries: []backend.Entry{}}
	if cursor != "" {
		return l, nil
	}
	children, err := c.children(ctx, prefix)
	if err != nil {
		return l, err
	}
	for _, r := range children {
		name := path.Base(r.path)
		if r.dir {
			l.Entries = append(l.Entries, backend.Entry{Key: prefix + name + "/", Name: name, Folder: true, Type: "folder"})
			continue
		}
		e := backend.Entry{Key: prefix + name, Name: name, Size: r.size, ETag: r.etag, Type: media.FileType(name)}
		if !r.modified.IsZero() {
			e.Modified = r.modified.UTC().Format(time.RFC3339)
		}
		l.Entries = append(l.Entries, e)
	}
	return l, nil
}

func (c *Client) Walk(ctx context.Context, prefix string, visit func(backend.Object) error) error {
	if _, err := c.Stat(ctx, prefix); err != nil {
		return err
	}
	if err := visit(backend.Object{Key: prefix, Directory: true}); err != nil {
		return err
	}
	queue := []string{prefix}
	for visited := 0; len(queue) > 0; visited++ {
		if visited >= maxDirectories {
			return fmt.Errorf("webdav walk exceeded %d directories", maxDirectories)
		}
		dir := queue[0]
		queue = queue[1:]
		children, err := c.children(ctx, dir)
		if err != nil {
			return err
		}
		for _, r := range children {
			name := path.Base(r.path)
			if r.dir {
				key := dir + name + "/"
				if err := visit(backend.Object{Key: key, Directory: true}); err != nil {
					return err
				}
				queue = append(queue, key)
				continue
			}
			if err := visit(backend.Object{Key: dir + name, ETag: r.etag, Size: r.size, Modified: r.modified}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) Stat(ctx context.Context, key string) (backend.Object, error) {
	resources, err := c.propfind(ctx, key, "0")
	if err != nil {
		return backend.Object{}, err
	}
	want := c.target(key)
	for _, r := range resources {
		if r.path == want {
			return backend.Object{Key: key, ETag: r.etag, Size: r.size, Modified: r.modified, Directory: r.dir}, nil
		}
	}
	return backend.Object{}, &backend.UpstreamError{Status: 404, Code: "NotFound"}
}

func (c *Client) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	h := http.Header{}
	if offset > 0 || length > 0 {
		end := ""
		if length > 0 {
			end = strconv.FormatInt(offset+length-1, 10)
		}
		h.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-"+end)
	}
	resp, err := c.request(ctx, http.MethodGet, key, "", h)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		err = statusError(resp)
		resp.Body.Close()
		return nil, err
	}
	var r io.Reader = resp.Body
	if resp.StatusCode == http.StatusOK && offset > 0 {
		if _, err = io.CopyN(io.Discard, r, offset); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	if length > 0 {
		r = io.LimitReader(r, length)
	}
	return backend.Reader{Reader: r, Closer: resp.Body}, nil
}
