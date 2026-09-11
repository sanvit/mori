package s3

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"mori-s3/internal/config"
	"mori-s3/internal/media"
)

// Presign does not fetch the object and never exposes the secret access key.
// The URL itself is a bearer credential until expiry; do not log it.
func (o *Origin) Presign(method, key string, download bool, now time.Time) (string, error) {
	if method != http.MethodGet {
		return "", fmt.Errorf("only GetObject can be presigned")
	}
	c := o.cfg
	if config.ValidateKey(key, false) != nil || len(c.Prefix+key) > 1024 {
		return "", fmt.Errorf("invalid presign key")
	}
	u := *c.Endpoint
	if c.PresignEndpoint != nil {
		u = *c.PresignEndpoint
	}
	if c.PathStyle {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + c.Bucket + "/" + c.Prefix + key
	} else {
		u.Host = c.Bucket + "." + u.Host
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + c.Prefix + key
	}
	u.RawPath = escapedPath(u.Path)
	contentType, disposition := media.Presentation(key, download)
	q := url.Values{}
	// Response overrides are GetObject parameters.
	q.Set("response-content-type", contentType)
	q.Set("response-content-disposition", disposition)
	q.Set("response-cache-control", "private, no-store")
	u.RawQuery = awsQuery(q)
	return PresignURL(u, method, c, now)
}

// PresignURL signs u with SigV4 query parameters. It uses UNSIGNED-PAYLOAD and
// only signs host so normal browser Range requests can vary without
// invalidating the signed URL.
func PresignURL(u url.URL, method string, c config.Config, now time.Time) (string, error) {
	if method != http.MethodGet {
		return "", fmt.Errorf("only GET can be presigned")
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return "", fmt.Errorf("presigning requires credentials")
	}
	if c.PresignTTL < time.Second || c.PresignTTL > 7*24*time.Hour || c.PresignTTL%time.Second != 0 {
		return "", fmt.Errorf("invalid presign lifetime")
	}
	date, stamp := now.UTC().Format("20060102"), now.UTC().Format("20060102T150405Z")
	scope := date + "/" + c.Region + "/s3/aws4_request"
	// Browsers normalize hostname case and remove default ports before sending
	// requests; sign that final host so a URL with :443 does not break on click.
	u.Host = strings.ToLower(u.Host)
	if (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80") {
		hostname := u.Hostname()
		if strings.Contains(hostname, ":") {
			hostname = "[" + hostname + "]"
		}
		u.Host = hostname
	}
	q := u.Query()
	q.Del("X-Amz-Signature")
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", c.AccessKey+"/"+scope)
	q.Set("X-Amz-Date", stamp)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(c.PresignTTL/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	if c.SessionToken != "" {
		q.Set("X-Amz-Security-Token", c.SessionToken)
	}
	canonicalPath := u.EscapedPath()
	if canonicalPath == "" {
		canonicalPath = "/"
	}
	canonical := strings.Join([]string{method, canonicalPath, awsQuery(q), "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	hash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(hash[:])
	signingKey := mac(mac(mac(mac([]byte("AWS4"+c.SecretKey), date), c.Region), "s3"), "aws4_request")
	q.Set("X-Amz-Signature", hex.EncodeToString(mac(signingKey, toSign)))
	u.RawQuery = awsQuery(q)
	return u.String(), nil
}
