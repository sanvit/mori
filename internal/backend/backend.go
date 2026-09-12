// Package backend defines the storage interface mori's HTTP server reads from.
// S3 is the primary implementation; WebDAV, FTP, and SFTP implement the same
// read-only contract. Keys are always slash-separated paths relative to the
// configured root, validated by config.ValidateKey before reaching a backend.
package backend

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"
)

// Entry is one row of a folder listing as shown in the UI.
type Entry struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	Folder   bool   `json:"folder"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	ETag     string `json:"etag,omitempty"`
	Type     string `json:"type"`
}

// Listing is one page of a folder. Cursor is empty on the last page.
type Listing struct {
	Prefix  string  `json:"prefix"`
	Entries []Entry `json:"entries"`
	Cursor  string  `json:"cursor,omitempty"`
}

// Object is file metadata. Native ETags retain their strength. Metadata-derived
// ETags are weak: equal metadata cannot prove byte-for-byte equality.
type Object struct {
	Key, ETag string
	Size      int64
	Modified  time.Time
	Directory bool
}

// UpstreamError reports a storage failure with an HTTP-like status:
// 403 denied, 404 missing, 412 changed, 416 bad range; anything else is 502.
type UpstreamError struct {
	Status int
	Code   string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream returned %d (%s)", e.Status, e.Code)
}

// Backend is a read-only view of remote storage.
type Backend interface {
	// List returns one page of the immediate children of prefix. prefix is
	// "" or ends with "/". Folder entries have keys ending in "/".
	List(ctx context.Context, prefix, cursor string) (Listing, error)
	// Walk visits every descendant of prefix (which ends with "/"), files and
	// folders alike, without reading bodies. It stops at the first visit error.
	Walk(ctx context.Context, prefix string, visit func(Object) error) error
	// Stat returns metadata for one file key.
	Stat(ctx context.Context, key string) (Object, error)
	// Open streams bytes [offset, offset+length) of key. A negative length
	// means until EOF. The reader must be closed.
	Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
}

// VersionTag hashes unambiguous metadata fields, never file contents. Scope
// identifies the backend endpoint/user/root; key is its full relative path.
// Do not use atime: reading a file can change it. Preserve available mtime
// precision without claiming a higher resolution than the origin provides.
func VersionTag(scope, key string, size int64, modified time.Time) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%q\n%q\n%d\n%s", scope, key, size, modified.UTC().Format(time.RFC3339Nano))))
	return fmt.Sprintf(`W/"mori-%x"`, digest)
}

func ValidETag(tag string) bool {
	tag = strings.TrimPrefix(tag, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return false
	}
	for _, b := range []byte(tag[1 : len(tag)-1]) {
		if b == 34 || b < 33 || b == 127 {
			return false
		}
	}
	return true
}

func StrongETag(tag string) bool { return ValidETag(tag) && !strings.HasPrefix(tag, "W/") }

func SyntheticETag(tag string) bool { return strings.HasPrefix(tag, `W/"mori-`) && ValidETag(tag) }

// Reader pairs a limited reader with the closer of its source.
type Reader struct {
	io.Reader
	io.Closer
}
