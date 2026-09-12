package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	"mori/internal/backend"
	"mori/internal/config"
)

type driver struct {
	fs     afero.Fs
	legacy bool // no MLST/MLSD, like vsftpd
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{ListenAddr: "127.0.0.1:0", DisableActiveMode: true, DisableMLST: d.legacy, DisableMLSD: d.legacy}, nil
}
func (d *driver) ClientConnected(ftpserver.ClientContext) (string, error) { return "test", nil }
func (d *driver) ClientDisconnected(ftpserver.ClientContext)              {}
func (d *driver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user == "tester" && pass == "secret" {
		return d.fs, nil
	}
	return nil, errors.New("denied")
}
func (d *driver) GetTLSConfig() (*tls.Config, error) { return nil, errors.New("no tls") }

func fixture(t *testing.T) (*Client, string) {
	return fixtureWith(t, false)
}

func fixtureWith(t *testing.T, legacy bool) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	for p, body := range map[string]string{"README.md": "0123456789", "docs/a.txt": "root", "docs/sub/x.txt": "x", "docs/sub/deep/한글 +&%.txt": "안녕", "docs/sub/zero.txt": ""} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "docs/sub/empty"), 0o755)
	server := ftpserver.NewFtpServer(&driver{fs: afero.NewBasePathFs(afero.NewOsFs(), root), legacy: legacy})
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	t.Cleanup(func() { server.Stop() })
	c := New(config.FTPConfig{Addr: server.Addr(), Username: "tester", Password: "secret", Root: "/"})
	t.Cleanup(func() {
		for {
			select {
			case sc := <-c.idle:
				sc.Quit()
			default:
				return
			}
		}
	})
	return c, server.Addr()
}

func TestListStatOpenWalk(t *testing.T) {
	c, addr := fixture(t)
	ctx := context.Background()
	l, err := c.List(ctx, "", "")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Key != "docs/" || !l.Entries[0].Folder || l.Entries[1].Key != "README.md" || l.Entries[1].Size != 10 || l.Entries[1].ETag == "" {
		t.Fatalf("%+v %v", l, err)
	}
	l, err = c.List(ctx, "docs/sub/", "")
	if err != nil || len(l.Entries) != 4 || l.Entries[0].Key != "docs/sub/deep/" || l.Entries[1].Key != "docs/sub/empty/" || l.Entries[2].Key != "docs/sub/x.txt" || l.Entries[3].Key != "docs/sub/zero.txt" {
		t.Fatalf("%+v %v", l, err)
	}
	st, err := c.Stat(ctx, "docs/sub/deep/한글 +&%.txt")
	if err != nil || st.Size != 6 || st.Directory || st.ETag == "" {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = c.Stat(ctx, "docs/sub/"); err != nil || !st.Directory {
		t.Fatal(st, err)
	}
	var ue *backend.UpstreamError
	if _, err = c.Stat(ctx, "missing.txt"); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset, length int64
		want           string
	}{{0, -1, "0123456789"}, {2, 3, "234"}, {7, -1, "789"}, {0, 0, ""}, {0, 10, "0123456789"}} {
		r, err := c.Open(ctx, "README.md", tc.offset, tc.length)
		if err != nil {
			t.Fatal(tc, err)
		}
		b, _ := io.ReadAll(r)
		if err = r.Close(); err != nil && tc.length < 0 {
			t.Fatal(tc, err)
		}
		if string(b) != tc.want {
			t.Fatal(tc, string(b))
		}
	}
	if _, err = c.Open(ctx, "missing.txt", 0, -1); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
	keys := []string{}
	err = c.Walk(ctx, "docs/sub/", func(o backend.Object) error { keys = append(keys, o.Key); return nil })
	sort.Strings(keys)
	if err != nil || strings.Join(keys, ",") != "docs/sub/,docs/sub/deep/,docs/sub/deep/한글 +&%.txt,docs/sub/empty/,docs/sub/x.txt,docs/sub/zero.txt" {
		t.Fatal(keys, err)
	}
	if err = c.Walk(ctx, "docs/nope/", func(backend.Object) error { return nil }); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
	if len(c.idle) == 0 {
		t.Fatal("completed transfers should return connections to the pool")
	}
	wrong := New(config.FTPConfig{Addr: addr, Username: "tester", Password: "bad", Root: "/"})
	if _, err = wrong.List(ctx, "", ""); !errors.As(err, &ue) || ue.Status != 403 {
		t.Fatal("bad password accepted", err)
	}
}

// emptyListFs imitates vsftpd: LIST of a missing path is an empty success and
// LIST of a file returns that file.
type emptyListFs struct{ afero.Fs }

func (f emptyListFs) Open(name string) (afero.File, error) {
	file, err := f.Fs.Open(name)
	if err != nil && os.IsNotExist(err) {
		return f.Fs.Open("/docs/sub/empty")
	}
	return file, err
}

func TestMissingFolderWithLenientListing(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "docs/sub/empty"), 0o755)
	os.WriteFile(filepath.Join(root, "README.md"), []byte("x"), 0o644)
	server := ftpserver.NewFtpServer(&driver{fs: emptyListFs{afero.NewBasePathFs(afero.NewOsFs(), root)}})
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	defer server.Stop()
	c := New(config.FTPConfig{Addr: server.Addr(), Username: "tester", Password: "secret", Root: "/"})
	var ue *backend.UpstreamError
	if _, err := c.List(context.Background(), "nope/", ""); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal("missing folder listed as empty", err)
	}
	if err := c.Walk(context.Background(), "docs/nope/", func(backend.Object) error { return nil }); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal("missing folder walked as empty", err)
	}
	if l, err := c.List(context.Background(), "docs/sub/empty/", ""); err != nil || len(l.Entries) != 0 {
		t.Fatal("real empty folder rejected", l, err)
	}
}

// The ZIP planner compares Stat tags with Walk tags, so both must come from the
// same metadata source even when the server lacks MLST.
func TestStatMatchesListingWithoutMLST(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		c, _ := fixtureWith(t, legacy)
		ctx := context.Background()
		tags := map[string]string{}
		if err := c.Walk(ctx, "docs/", func(o backend.Object) error { tags[o.Key] = o.ETag; return nil }); err != nil {
			t.Fatal(legacy, err)
		}
		for _, key := range []string{"docs/a.txt", "docs/sub/x.txt", "docs/sub/deep/한글 +&%.txt"} {
			st, err := c.Stat(ctx, key)
			if err != nil || st.ETag == "" || st.ETag != tags[key] {
				t.Fatalf("legacy=%v %s stat=%q walk=%q err=%v", legacy, key, st.ETag, tags[key], err)
			}
		}
		var ue *backend.UpstreamError
		if _, err := c.Stat(ctx, "docs/missing.txt"); !errors.As(err, &ue) || ue.Status != 404 {
			t.Fatal(legacy, err)
		}
		if st, err := c.Stat(ctx, "docs/sub/"); err != nil || !st.Directory {
			t.Fatal(legacy, st, err)
		}
	}
}

func TestMissingRootWithLenientListing(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "pub/team"), 0o755)
	os.MkdirAll(filepath.Join(root, "docs/sub/empty"), 0o755)
	server := ftpserver.NewFtpServer(&driver{fs: emptyListFs{afero.NewBasePathFs(afero.NewOsFs(), root)}})
	if err := server.Listen(); err != nil {
		t.Fatal(err)
	}
	go server.Serve()
	defer server.Stop()
	var ue *backend.UpstreamError
	for _, missing := range []string{"/pub/teem", "pub/teem"} {
		c := New(config.FTPConfig{Addr: server.Addr(), Username: "tester", Password: "secret", Root: missing})
		if _, err := c.List(context.Background(), "", ""); !errors.As(err, &ue) || ue.Status != 404 {
			t.Fatal("mistyped root listed as empty", missing, err)
		}
	}
	c := New(config.FTPConfig{Addr: server.Addr(), Username: "tester", Password: "secret", Root: "/pub/team"})
	if l, err := c.List(context.Background(), "", ""); err != nil || len(l.Entries) != 0 {
		t.Fatal("existing empty root rejected", l, err)
	}
}
