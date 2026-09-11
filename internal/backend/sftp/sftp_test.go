package sftp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	sftplib "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"mori-s3/internal/backend"
	"mori-s3/internal/config"
)

// startServer runs an in-process SSH server exposing the SFTP subsystem.
func startServer(t *testing.T) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	hostKey, _ = ssh.NewPublicKey(pub)
	// Offer an RSA host key too, as OpenSSH does, so the client must pick the
	// algorithm that matches known_hosts.
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaSigner, _ := ssh.NewSignerFromKey(rsaKey)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if c.User() == "tester" && string(pass) == "secret" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(rsaSigner)
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						return
					}
					go func() {
						for req := range requests {
							ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
							req.Reply(ok, nil)
							if ok {
								if srv, err := sftplib.NewServer(channel, sftplib.ReadOnly()); err == nil {
									srv.Serve()
								}
								channel.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), hostKey
}

func fixture(t *testing.T) (*Client, string, ssh.PublicKey) {
	t.Helper()
	root := t.TempDir()
	for p, body := range map[string]string{"README.md": "0123456789", "docs/a.txt": "root", "docs/sub/x.txt": "x", "docs/sub/deep/한글 +&%.txt": "안녕", "docs/sub/zero.txt": ""} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(body), 0o644)
	}
	os.MkdirAll(filepath.Join(root, "docs/sub/empty"), 0o755)
	os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, "docs/link.md"))
	os.Symlink(filepath.Join(root, "docs"), filepath.Join(root, "docs/loop"))
	addr, hostKey := startServer(t)
	known := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(known, []byte(knownhosts.Normalize(addr)+" "+string(ssh.MarshalAuthorizedKey(hostKey))), 0o600)
	c, err := New(config.SFTPConfig{Addr: addr, Username: "tester", Password: "secret", KnownHosts: known, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.mu.Lock()
		if c.sftp != nil {
			c.sftp.Close()
			c.conn.Close()
		}
		c.mu.Unlock()
	})
	return c, addr, hostKey
}

func TestListStatOpenWalk(t *testing.T) {
	c, _, _ := fixture(t)
	ctx := context.Background()
	l, err := c.List(ctx, "docs/", "")
	if err != nil || len(l.Entries) != 3 || l.Entries[0].Key != "docs/sub/" || !l.Entries[0].Folder || l.Entries[1].Key != "docs/a.txt" || l.Entries[2].Key != "docs/link.md" || l.Entries[2].Size != 10 || l.Entries[1].ETag == "" {
		t.Fatalf("%+v %v", l, err)
	}
	st, err := c.Stat(ctx, "docs/sub/deep/한글 +&%.txt")
	if err != nil || st.Size != 6 || st.Directory || st.ETag == "" {
		t.Fatalf("%+v %v", st, err)
	}
	var ue *backend.UpstreamError
	if _, err = c.Stat(ctx, "missing.txt"); !errors.As(err, &ue) || ue.Status != 404 {
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
	keys := []string{}
	err = c.Walk(ctx, "docs/sub/", func(o backend.Object) error { keys = append(keys, o.Key); return nil })
	sort.Strings(keys)
	if err != nil || strings.Join(keys, ",") != "docs/sub/,docs/sub/deep/,docs/sub/deep/한글 +&%.txt,docs/sub/empty/,docs/sub/x.txt,docs/sub/zero.txt" {
		t.Fatal(keys, err)
	}
	if err = c.Walk(ctx, "docs/nope/", func(backend.Object) error { return nil }); !errors.As(err, &ue) || ue.Status != 404 {
		t.Fatal(err)
	}
}

func TestHostKeyAndPasswordAreVerified(t *testing.T) {
	_, addr, _ := fixture(t)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewPublicKey(otherPub)
	known := filepath.Join(t.TempDir(), "known_hosts")
	os.WriteFile(known, []byte(knownhosts.Normalize(addr)+" "+string(ssh.MarshalAuthorizedKey(other))), 0o600)
	c, err := New(config.SFTPConfig{Addr: addr, Username: "tester", Password: "secret", KnownHosts: known, Root: "."})
	if err != nil {
		t.Fatal(err)
	}
	var ue *backend.UpstreamError
	if _, err = c.List(context.Background(), "", ""); !errors.As(err, &ue) || ue.Status != 403 {
		t.Fatal("host key mismatch accepted", err)
	}
	c, _ = New(config.SFTPConfig{Addr: addr, Username: "tester", Password: "wrong", InsecureHostKey: true, Root: "."})
	if _, err = c.List(context.Background(), "", ""); !errors.As(err, &ue) || ue.Status != 403 {
		t.Fatal("bad password accepted", err)
	}
	if _, err = New(config.SFTPConfig{Addr: addr, Username: "tester", Password: "x", KnownHosts: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("missing known_hosts accepted")
	}
}
