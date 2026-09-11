// Package backend defines the storage interface mori's HTTP server reads from.
// S3 is the primary implementation; WebDAV, FTP, and SFTP implement the same
// read-only contract. Keys are always slash-separated paths relative to the
// configured root, validated by config.ValidateKey before reaching a backend.
package backend

import (
	"context"
	"fmt"
	"io"
	"strconv"
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

// Object is file metadata. ETag is an opaque, quoted version token: a change
// in content must change it. Backends without native ETags derive one from
// size and modification time (see VersionTag).
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

// VersionTag builds a quoted ETag from size and modification time for
// backends that do not provide one.
func VersionTag(size int64, modified time.Time) string {
	return `"` + strconv.FormatInt(size, 10) + "-" + strconv.FormatInt(modified.UnixNano(), 36) + `"`
}

// Reader pairs a limited reader with the closer of its source.
type Reader struct {
	io.Reader
	io.Closer
}
