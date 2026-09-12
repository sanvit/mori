// Package s3 is a minimal SigV4 client for listing and fetching objects,
// optionally through a caching reverse proxy for object bodies.
package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"mori/internal/backend"
	"mori/internal/config"
	"mori/internal/media"
)

const emptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type Origin struct {
	cfg    config.Config
	client *http.Client
}

func New(c config.Config) *Origin {
	return &Origin{c, &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 128, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, ForceAttemptHTTP2: true, DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func uriEscape(s string) string {
	const h = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", rune(c)) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(h[c>>4])
			b.WriteByte(h[c&15])
		}
	}
	return b.String()
}
func escapedPath(s string) string {
	p := strings.Split(s, "/")
	for i := range p {
		p[i] = uriEscape(p[i])
	}
	return strings.Join(p, "/")
}
func awsQuery(v url.Values) string {
	type pair struct{ key, value string }
	pairs := []pair{}
	for k, vs := range v {
		for _, s := range vs {
			pairs = append(pairs, pair{uriEscape(k), uriEscape(s)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.key + "=" + p.value
	}
	return strings.Join(out, "&")
}

func mac(k []byte, s string) []byte {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(s))
	return h.Sum(nil)
}
func signRequest(r *http.Request, c config.Config, now time.Time) {
	if c.AccessKey == "" {
		return
	}
	date := now.UTC().Format("20060102")
	stamp := now.UTC().Format("20060102T150405Z")
	r.Header.Set("X-Amz-Date", stamp)
	r.Header.Set("X-Amz-Content-Sha256", emptyHash)
	hdr := map[string]string{"host": r.URL.Host, "x-amz-date": stamp, "x-amz-content-sha256": emptyHash}
	if c.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", c.SessionToken)
		hdr["x-amz-security-token"] = c.SessionToken
	}
	keys := make([]string, 0, len(hdr))
	for k := range hdr {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var canon strings.Builder
	for _, k := range keys {
		canon.WriteString(k + ":" + strings.Join(strings.Fields(hdr[k]), " ") + "\n")
	}
	names := strings.Join(keys, ";")
	p := r.URL.EscapedPath()
	if p == "" {
		p = "/"
	}
	cr := strings.Join([]string{r.Method, p, awsQuery(r.URL.Query()), canon.String(), names, emptyHash}, "\n")
	hash := sha256.Sum256([]byte(cr))
	scope := date + "/" + c.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	k := mac(mac(mac(mac([]byte("AWS4"+c.SecretKey), date), c.Region), "s3"), "aws4_request")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+names+", Signature="+hex.EncodeToString(mac(k, toSign)))
}

// Request performs one signed S3 request for a key including the configured prefix.
func (o *Origin) Request(ctx context.Context, method, key string, q url.Values, headers http.Header) (*http.Response, error) {
	u := *o.cfg.Endpoint
	if o.cfg.PathStyle {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + o.cfg.Bucket + "/" + key
	} else {
		u.Host = o.cfg.Bucket + "." + u.Host
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + key
	}
	u.RawPath = escapedPath(u.Path)
	u.RawQuery = awsQuery(q)
	req, e := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if e != nil {
		return nil, e
	}
	for _, k := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since", "If-Match"} {
		if v := headers.Get(k); v != "" {
			req.Header.Set(k, v)
		}
	}
	signRequest(req, o.cfg, time.Now())
	return o.client.Do(req)
}

// Object fetches key (relative to the configured prefix) with GET or HEAD.
func (o *Origin) Object(ctx context.Context, method, key string, h http.Header) (*http.Response, error) {
	return o.Request(ctx, method, o.cfg.Prefix+key, nil, h)
}

// List returns one page of the folder at prefix.
// Delimiter groups descendants in S3 BEFORE MaxKeys; every subfolder counts once.
func (o *Origin) List(ctx context.Context, prefix, cursor string) (backend.Listing, error) {
	q := url.Values{"list-type": {"2"}, "delimiter": {"/"}, "prefix": {o.cfg.Prefix + prefix}, "max-keys": {"1000"}, "encoding-type": {"url"}}
	if cursor != "" {
		q.Set("continuation-token", cursor)
	}
	r, e := o.Request(ctx, "GET", "", q, nil)
	if e != nil {
		return backend.Listing{}, e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return backend.Listing{}, ReadError(r)
	}
	var x struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Contents []struct {
			Key, LastModified, ETag string
			Size                    int64
		}
		CommonPrefixes        []struct{ Prefix string }
		NextContinuationToken string
		IsTruncated           bool
		EncodingType          string
	}
	if e = xml.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&x); e != nil {
		return backend.Listing{}, fmt.Errorf("invalid S3 listing: %w", e)
	}
	l := backend.Listing{Prefix: prefix, Entries: []backend.Entry{}}
	decode := func(s string) (string, error) {
		if x.EncodingType == "url" {
			return url.PathUnescape(s)
		}
		return s, nil
	}
	for _, f := range x.CommonPrefixes {
		key, err := decode(f.Prefix)
		if err != nil {
			return backend.Listing{}, err
		}
		if !strings.HasPrefix(key, o.cfg.Prefix+prefix) {
			continue
		}
		key = strings.TrimPrefix(key, o.cfg.Prefix)
		l.Entries = append(l.Entries, backend.Entry{Key: key, Name: strings.TrimSuffix(strings.TrimPrefix(key, prefix), "/"), Folder: true, Type: "folder"})
	}
	for _, f := range x.Contents {
		key, err := decode(f.Key)
		if err != nil {
			return backend.Listing{}, err
		}
		if key == o.cfg.Prefix+prefix || !strings.HasPrefix(key, o.cfg.Prefix+prefix) {
			continue
		}
		key = strings.TrimPrefix(key, o.cfg.Prefix)
		l.Entries = append(l.Entries, backend.Entry{Key: key, Name: strings.TrimPrefix(key, prefix), Size: f.Size, Modified: f.LastModified, ETag: f.ETag, Type: media.FileType(key)})
	}
	if x.IsTruncated {
		if x.NextContinuationToken == "" || x.NextContinuationToken == cursor {
			return backend.Listing{}, fmt.Errorf("upstream pagination token is missing or repeated")
		}
		l.Cursor = x.NextContinuationToken
	}
	return l, nil
}

// ReadError converts a non-2xx S3 response into a *backend.UpstreamError.
func ReadError(r *http.Response) error {
	var x struct{ Code string }
	_ = xml.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&x)
	return &backend.UpstreamError{Status: r.StatusCode, Code: x.Code}
}

// Stat is a HEAD request; it never reads the body.
func (o *Origin) Stat(ctx context.Context, key string) (backend.Object, error) {
	resp, err := o.Request(ctx, http.MethodHead, o.cfg.Prefix+key, nil, nil)
	if err != nil {
		return backend.Object{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return backend.Object{}, ReadError(resp)
	}
	obj := backend.Object{Key: key, Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}
	obj.Modified, _ = http.ParseTime(resp.Header.Get("Last-Modified"))
	return obj, nil
}

// Open streams a byte range with a Range header. A server that ignores the
// range is tolerated by discarding the leading bytes.
func (o *Origin) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
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
	resp, err := o.Object(ctx, http.MethodGet, key, h)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		err = ReadError(resp)
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
