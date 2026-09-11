package webdav

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	xwebdav "golang.org/x/net/webdav"

	"mori-s3/internal/backend"
	"mori-s3/internal/config"
)

func fixture(t *testing.T) (*Client, *httptest.Server) {
	return mountedFixture(t, "/dav")
}

// mountedFixture serves the tree under mount ("" for the server root).
func mountedFixture(t *testing.T, mount string) (*Client, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	for p, body := range map[string]string{"README.md": "0123456789", "docs/a.txt": "root", "docs/sub/x.txt": "x", "docs/sub/deep/한글 +&%.txt": "안녕", "docs/sub/zero.txt": ""} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "docs/sub/empty"), 0o755)
	h := &xwebdav.Handler{Prefix: mount, FileSystem: xwebdav.Dir(root), LockSystem: xwebdav.NewMemLS()}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "tester" || p != "secret" {
			w.WriteHeader(401)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	u, _ := url.Parse(s.URL + mount + "/")
	return New(config.WebDAVConfig{URL: u, Username: "tester", Password: "secret"}), s
}

func TestListStatAndOpen(t *testing.T) {
	c, _ := fixture(t)
	ctx := context.Background()
	l, err := c.List(ctx, "", "")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Key != "docs/" || !l.Entries[0].Folder || l.Entries[1].Key != "README.md" || l.Entries[1].Size != 10 || l.Entries[1].ETag == "" || l.Entries[1].Modified == "" {
		t.Fatalf("%+v %v", l, err)
	}
	l, err = c.List(ctx, "docs/sub/", "")
	if err != nil || len(l.Entries) != 4 || l.Entries[0].Key != "docs/sub/deep/" || l.Entries[1].Key != "docs/sub/empty/" || l.Entries[2].Key != "docs/sub/x.txt" || l.Entries[3].Key != "docs/sub/zero.txt" {
		t.Fatalf("%+v %v", l, err)
	}
	st, err := c.Stat(ctx, "docs/sub/deep/한글 +&%.txt")
	if err != nil || st.Size != 6 || st.Directory || st.ETag == "" || st.Modified.IsZero() {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err = c.Stat(ctx, "docs/sub/"); err != nil || !st.Directory {
		t.Fatal(st, err)
	}
	var ue *backend.UpstreamError
	if _, err = c.Stat(ctx, "missing.txt"); err == nil || !asUpstream(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		offset, length int64
		want           string
	}{{0, -1, "0123456789"}, {2, 3, "234"}, {7, -1, "789"}, {0, 0, ""}} {
		r, err := c.Open(ctx, "README.md", tc.offset, tc.length)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if string(b) != tc.want {
			t.Fatal(tc, string(b))
		}
	}
	if _, err = c.Open(ctx, "missing.txt", 0, -1); err == nil || !asUpstream(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
}

func TestWalkAndAuth(t *testing.T) {
	c, s := fixture(t)
	ctx := context.Background()
	keys := []string{}
	err := c.Walk(ctx, "docs/sub/", func(o backend.Object) error {
		if !o.Directory && o.ETag == "" {
			t.Error("file without version tag", o)
		}
		keys = append(keys, o.Key)
		return nil
	})
	sort.Strings(keys)
	if err != nil || strings.Join(keys, ",") != "docs/sub/,docs/sub/deep/,docs/sub/deep/한글 +&%.txt,docs/sub/empty/,docs/sub/x.txt,docs/sub/zero.txt" {
		t.Fatal(keys, err)
	}
	var ue *backend.UpstreamError
	if err = c.Walk(ctx, "docs/nope/", func(backend.Object) error { return nil }); err == nil || !asUpstream(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
	u, _ := url.Parse(s.URL + "/dav")
	wrong := New(config.WebDAVConfig{URL: u, Username: "tester", Password: "bad"})
	if _, err = wrong.List(ctx, "", ""); err == nil || !asUpstream(err, &ue) || ue.Status != 403 {
		t.Fatal(err)
	}
}

func asUpstream(err error, target **backend.UpstreamError) bool {
	u, ok := err.(*backend.UpstreamError)
	if ok {
		*target = u
	}
	return ok
}

func TestServerRootMount(t *testing.T) {
	c, _ := mountedFixture(t, "")
	l, err := c.List(context.Background(), "", "")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Key != "docs/" || l.Entries[1].Key != "README.md" {
		t.Fatalf("%+v %v", l, err)
	}
	if l, err = c.List(context.Background(), "docs/", ""); err != nil || len(l.Entries) != 2 {
		t.Fatalf("%+v %v", l, err)
	}
}
