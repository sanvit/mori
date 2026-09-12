// Package ftp reads an FTP or explicit-FTPS server. Every transfer occupies a
// control connection, so a small pool of logged-in connections is kept and
// grown on demand.
package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"path"
	"sort"
	"strings"
	"time"

	ftpclient "github.com/jlaffaye/ftp"

	"mori/internal/backend"
	"mori/internal/config"
	"mori/internal/media"
)

const poolSize = 4
const maxDirectories = 10000

type Client struct {
	cfg  config.FTPConfig
	idle chan *ftpclient.ServerConn
}

func New(c config.FTPConfig) *Client {
	return &Client{cfg: c, idle: make(chan *ftpclient.ServerConn, poolSize)}
}

func (c *Client) remote(key string) string {
	p := path.Join(c.cfg.Root, strings.TrimSuffix(key, "/"))
	if p == "" {
		return "."
	}
	return p
}
func (c *Client) conn(ctx context.Context) (*ftpclient.ServerConn, error) {
	for {
		select {
		case sc := <-c.idle:
			if sc.NoOp() == nil {
				return sc, nil
			}
			sc.Quit()
			continue
		default:
		}
		break
	}
	opts := []ftpclient.DialOption{ftpclient.DialWithContext(ctx), ftpclient.DialWithTimeout(15 * time.Second), ftpclient.DialWithShutTimeout(15 * time.Second)}
	if c.cfg.TLS {
		host, _, _ := net.SplitHostPort(c.cfg.Addr)
		opts = append(opts, ftpclient.DialWithExplicitTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}))
	}
	sc, err := ftpclient.Dial(c.cfg.Addr, opts...)
	if err != nil {
		return nil, err
	}
	if err = sc.Login(c.cfg.Username, c.cfg.Password); err != nil {
		sc.Quit()
		return nil, mapError(err)
	}
	return sc, nil
}
func (c *Client) release(sc *ftpclient.ServerConn, healthy bool) {
	if healthy {
		select {
		case c.idle <- sc:
			return
		default:
		}
	}
	sc.Quit()
}

// reusable reports whether the control connection is still in sync: true on
// success or a normal server reply such as 550, false on transport failures.
func reusable(err error) bool {
	var te *textproto.Error
	return err == nil || errors.As(err, &te)
}
func mapError(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		switch te.Code {
		case 530, 532, 533:
			return &backend.UpstreamError{Status: 403, Code: fmt.Sprint(te.Code)}
		case 550, 551, 553:
			return &backend.UpstreamError{Status: 404, Code: fmt.Sprint(te.Code)}
		}
		return &backend.UpstreamError{Status: 502, Code: fmt.Sprint(te.Code)}
	}
	return err
}

// entries lists a directory without dot entries or symbolic links, folders first.
func (c *Client) entries(ctx context.Context, dir string) ([]*ftpclient.Entry, error) {
	sc, err := c.conn(ctx)
	if err != nil {
		return nil, err
	}
	list, err := sc.List(c.remote(dir))
	c.release(sc, reusable(err))
	if err != nil {
		return nil, mapError(err)
	}
	out := list[:0]
	for _, e := range list {
		if e.Name == "." || e.Name == ".." || e.Name == "" || strings.ContainsAny(e.Name, "/\\") || e.Type == ftpclient.EntryTypeLink {
			continue
		}
		out = append(out, e)
	}
	// Many servers (vsftpd among them) answer LIST of a missing path with an
	// empty listing, and LIST of a file with that file's own line. Confirm the
	// folder exists in its parent before trusting either shape.
	remote := c.remote(dir)
	if remote != "/" && remote != "." && (len(out) == 0 || (len(out) == 1 && out[0].Type != ftpclient.EntryTypeFolder && out[0].Name == path.Base(remote))) {
		if err := c.requireFolder(ctx, remote); err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Type == ftpclient.EntryTypeFolder) != (out[j].Type == ftpclient.EntryTypeFolder) {
			return out[i].Type == ftpclient.EntryTypeFolder
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// requireFolder reports 404 unless the remote directory is a folder in its
// parent. This also covers the configured root, so a mistyped FTP_PATH or
// STORAGE_BASE_PATH is reported instead of shown as an empty folder.
func (c *Client) requireFolder(ctx context.Context, remote string) error {
	name, parent := path.Base(remote), path.Dir(remote)
	sc, err := c.conn(ctx)
	if err != nil {
		return err
	}
	list, err := sc.List(parent)
	c.release(sc, reusable(err))
	if err != nil {
		return mapError(err)
	}
	for _, e := range list {
		if e.Name == name && e.Type == ftpclient.EntryTypeFolder {
			return nil
		}
	}
	return &backend.UpstreamError{Status: 404, Code: "NoSuchDirectory"}
}

func (c *Client) List(ctx context.Context, prefix, cursor string) (backend.Listing, error) {
	l := backend.Listing{Prefix: prefix, Entries: []backend.Entry{}}
	if cursor != "" {
		return l, nil
	}
	list, err := c.entries(ctx, prefix)
	if err != nil {
		return l, err
	}
	for _, e := range list {
		if e.Type == ftpclient.EntryTypeFolder {
			l.Entries = append(l.Entries, backend.Entry{Key: prefix + e.Name + "/", Name: e.Name, Folder: true, Type: "folder"})
			continue
		}
		entry := backend.Entry{Key: prefix + e.Name, Name: e.Name, Size: int64(e.Size), ETag: backend.VersionTag(int64(e.Size), e.Time), Type: media.FileType(e.Name)}
		if !e.Time.IsZero() {
			entry.Modified = e.Time.UTC().Format(time.RFC3339)
		}
		l.Entries = append(l.Entries, entry)
	}
	return l, nil
}

func (c *Client) Walk(ctx context.Context, prefix string, visit func(backend.Object) error) error {
	if err := visit(backend.Object{Key: prefix, Directory: true}); err != nil {
		return err
	}
	queue := []string{prefix}
	for visited := 0; len(queue) > 0; visited++ {
		if visited >= maxDirectories {
			return fmt.Errorf("ftp walk exceeded %d directories", maxDirectories)
		}
		dir := queue[0]
		queue = queue[1:]
		list, err := c.entries(ctx, dir)
		if err != nil {
			return err
		}
		for _, e := range list {
			if e.Type == ftpclient.EntryTypeFolder {
				key := dir + e.Name + "/"
				if err := visit(backend.Object{Key: key, Directory: true}); err != nil {
					return err
				}
				queue = append(queue, key)
				continue
			}
			if err := visit(backend.Object{Key: dir + e.Name, ETag: backend.VersionTag(int64(e.Size), e.Time), Size: int64(e.Size), Modified: e.Time}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Stat must report the same size and time as List and Walk, because the ZIP
// planner compares version tags across them. MLST (RFC 3659) matches MLSD
// listings; servers without it are answered from the parent's LIST, since
// MDTM is more precise than LIST and LIST of a single file is not portable.
func (c *Client) Stat(ctx context.Context, key string) (backend.Object, error) {
	sc, err := c.conn(ctx)
	if err != nil {
		return backend.Object{}, err
	}
	e, err := sc.GetEntry(c.remote(key))
	c.release(sc, reusable(err))
	if err != nil {
		if e, err = c.parentEntry(ctx, key); err != nil {
			return backend.Object{}, err
		}
	}
	obj := backend.Object{Key: key, Size: int64(e.Size), Modified: e.Time, Directory: e.Type == ftpclient.EntryTypeFolder}
	if !obj.Directory {
		obj.ETag = backend.VersionTag(obj.Size, obj.Modified)
	}
	return obj, nil
}

func (c *Client) parentEntry(ctx context.Context, key string) (*ftpclient.Entry, error) {
	trimmed := strings.TrimSuffix(key, "/")
	name := path.Base(trimmed)
	parent := strings.TrimSuffix(trimmed, name)
	list, err := c.entries(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, e := range list {
		if e.Name == name {
			return e, nil
		}
	}
	return nil, &backend.UpstreamError{Status: 404, Code: "NoSuchFile"}
}

// transfer is one RETR data stream. Closing it before the transfer completed
// discards the control connection rather than trying to resynchronize it.
type transfer struct {
	io.Reader
	resp     *ftpclient.Response
	client   *Client
	conn     *ftpclient.ServerConn
	remain   int64 // bytes still expected; -1 for until EOF
	complete bool
}

func (t *transfer) Read(p []byte) (int, error) {
	n, err := t.Reader.Read(p)
	if t.remain > 0 {
		t.remain -= int64(n)
		if t.remain == 0 && err == nil {
			// A limited read cannot observe EOF; assume the server sent exactly
			// this range so the connection stays reusable.
			t.complete = true
		}
	}
	if err == io.EOF {
		t.complete = true
	}
	return n, err
}
func (t *transfer) Close() error {
	err := t.resp.Close()
	t.client.release(t.conn, t.complete && err == nil)
	return err
}

func (c *Client) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	sc, err := c.conn(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := sc.RetrFrom(c.remote(key), uint64(offset))
	if err != nil {
		c.release(sc, reusable(err))
		return nil, mapError(err)
	}
	var r io.Reader = resp
	if length > 0 {
		r = io.LimitReader(resp, length)
	}
	return &transfer{Reader: r, resp: resp, client: c, conn: sc, remain: length}, nil
}
