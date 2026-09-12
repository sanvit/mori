// Package sftp reads files over SSH. One multiplexed SSH connection is shared
// by all requests and re-established after a connection loss.
package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	sftplib "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"mori/internal/backend"
	"mori/internal/config"
	"mori/internal/media"
)

const maxDirectories = 10000

type Client struct {
	cfg  config.SFTPConfig
	ssh  *ssh.ClientConfig
	mu   sync.Mutex
	conn *ssh.Client
	sftp *sftplib.Client
}

// New validates credentials and host-key settings up front; the connection
// itself is opened lazily on first use.
func New(c config.SFTPConfig) (*Client, error) {
	cfg := &ssh.ClientConfig{User: c.Username, Timeout: 15 * time.Second}
	if c.Key != "" && c.KeyFile != "" {
		return nil, fmt.Errorf("set only one of SFTP_KEY and SFTP_KEY_FILE")
	}
	if c.HostKey != "" && c.KnownHosts != "" || c.InsecureHostKey && (c.HostKey != "" || c.KnownHosts != "") {
		return nil, fmt.Errorf("choose one SFTP host verification setting")
	}
	if c.Key != "" || c.KeyFile != "" {
		// Accept real newlines or literal backslash-n for single-line ENV values.
		pem := []byte(strings.ReplaceAll(c.Key, `\n`, "\n"))
		if c.KeyFile != "" {
			var err error
			pem, err = os.ReadFile(c.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("SFTP_KEY_FILE: unable to read private key file")
			}
		}
		var signer ssh.Signer
		var err error
		if c.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(c.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			return nil, fmt.Errorf("invalid SFTP private key or passphrase; check SFTP_KEY/SFTP_KEY_FILE and SFTP_KEY_PASSPHRASE")
		}
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		cfg.Auth = append(cfg.Auth, ssh.Password(c.Password))
	}
	if c.InsecureHostKey {
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey() // #nosec G106 -- explicit operator opt-in
	} else if c.HostKey != "" {
		value := strings.TrimSpace(c.HostKey)
		key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
		if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("SFTP_HOST_KEY must contain one OpenSSH public key: key-type base64-key [comment]")
		}
		if _, certificate := key.(*ssh.Certificate); certificate {
			return nil, fmt.Errorf("SFTP_HOST_KEY requires a plain host public key, not a certificate")
		}
		cfg.HostKeyCallback = ssh.FixedHostKey(key)
		cfg.HostKeyAlgorithms = hostKeyAlgorithms(key)
	} else {
		callback, err := knownhosts.New(c.KnownHosts)
		if err != nil {
			return nil, fmt.Errorf("SFTP_KNOWN_HOSTS: %w", err)
		}
		cfg.HostKeyCallback = callback
		// Ask only for key types we can verify. Otherwise a server offering an
		// RSA key first fails against a known_hosts that lists only ed25519.
		if cfg.HostKeyAlgorithms, err = knownAlgorithms(c.KnownHosts, c.Addr); err != nil {
			return nil, fmt.Errorf("SFTP_KNOWN_HOSTS: %w", err)
		}
	}
	return &Client{cfg: c, ssh: cfg}, nil
}

func (c *Client) remote(key string) string {
	p := path.Join(c.cfg.Root, strings.TrimSuffix(key, "/"))
	if p == "" {
		return "."
	}
	return p
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	if c.sftp != nil {
		err = c.sftp.Close()
		c.sftp = nil
	}
	if c.conn != nil {
		err = errors.Join(err, c.conn.Close())
		c.conn = nil
	}
	return err
}
func (c *Client) client(ctx context.Context) (*sftplib.Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sftp != nil {
		return c.sftp, nil
	}
	nc, err := (&net.Dialer{Timeout: c.ssh.Timeout}).DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, c.cfg.Addr, c.ssh)
	if err != nil {
		nc.Close()
		return nil, &backend.UpstreamError{Status: 403, Code: "ssh: " + err.Error()}
	}
	conn := ssh.NewClient(cc, chans, reqs)
	client, err := sftplib.NewClient(conn, sftplib.UseConcurrentReads(true))
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.conn, c.sftp = conn, client
	return client, nil
}

// fail maps an operation error and drops the session when the transport died.
func (c *Client) fail(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return &backend.UpstreamError{Status: 404, Code: "NoSuchFile"}
	}
	if errors.Is(err, fs.ErrPermission) {
		return &backend.UpstreamError{Status: 403, Code: "PermissionDenied"}
	}
	var status *sftplib.StatusError
	if !errors.As(err, &status) {
		c.mu.Lock()
		if c.sftp != nil {
			c.sftp.Close()
			c.conn.Close()
			c.sftp, c.conn = nil, nil
		}
		c.mu.Unlock()
	}
	return err
}

// entries lists a directory, folders first. Symbolic links to files are
// followed; links to directories are skipped to avoid cycles.
func (c *Client) entries(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	list, err := client.ReadDir(c.remote(dir))
	if err != nil {
		return nil, c.fail(err)
	}
	out := list[:0]
	for _, fi := range list {
		name := fi.Name()
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
			continue
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			resolved, err := client.Stat(c.remote(dir + name))
			if err != nil || resolved.IsDir() {
				continue
			}
			fi = renamed{resolved, name}
		}
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, fi)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir() != out[j].IsDir() {
			return out[i].IsDir()
		}
		return out[i].Name() < out[j].Name()
	})
	return out, nil
}

type renamed struct {
	fs.FileInfo
	name string
}

func (r renamed) Name() string { return r.name }

func (c *Client) List(ctx context.Context, prefix, cursor string) (backend.Listing, error) {
	l := backend.Listing{Prefix: prefix, Entries: []backend.Entry{}}
	if cursor != "" {
		return l, nil
	}
	list, err := c.entries(ctx, prefix)
	if err != nil {
		return l, err
	}
	for _, fi := range list {
		if fi.IsDir() {
			l.Entries = append(l.Entries, backend.Entry{Key: prefix + fi.Name() + "/", Name: fi.Name(), Folder: true, Type: "folder"})
			continue
		}
		l.Entries = append(l.Entries, backend.Entry{Key: prefix + fi.Name(), Name: fi.Name(), Size: fi.Size(), Modified: fi.ModTime().UTC().Format(time.RFC3339), ETag: c.versionTag(prefix+fi.Name(), fi.Size(), fi.ModTime()), Type: media.FileType(fi.Name())})
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
			return fmt.Errorf("sftp walk exceeded %d directories", maxDirectories)
		}
		dir := queue[0]
		queue = queue[1:]
		list, err := c.entries(ctx, dir)
		if err != nil {
			return err
		}
		for _, fi := range list {
			if fi.IsDir() {
				key := dir + fi.Name() + "/"
				if err := visit(backend.Object{Key: key, Directory: true}); err != nil {
					return err
				}
				queue = append(queue, key)
				continue
			}
			if err := visit(backend.Object{Key: dir + fi.Name(), ETag: c.versionTag(dir+fi.Name(), fi.Size(), fi.ModTime()), Size: fi.Size(), Modified: fi.ModTime()}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Client) Stat(ctx context.Context, key string) (backend.Object, error) {
	client, err := c.client(ctx)
	if err != nil {
		return backend.Object{}, err
	}
	fi, err := client.Stat(c.remote(key))
	if err != nil {
		return backend.Object{}, c.fail(err)
	}
	obj := backend.Object{Key: key, Size: fi.Size(), Modified: fi.ModTime(), Directory: fi.IsDir()}
	if !obj.Directory {
		obj.ETag = c.versionTag(key, fi.Size(), fi.ModTime())
	}
	return obj, nil
}

func (c *Client) Open(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if length == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	client, err := c.client(ctx)
	if err != nil {
		return nil, err
	}
	f, err := client.Open(c.remote(key))
	if err != nil {
		return nil, c.fail(err)
	}
	if offset == 0 && length < 0 {
		return f, nil // io.Copy uses File.WriteTo, which pipelines reads.
	}
	if length < 0 {
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, c.fail(err)
		}
		length = fi.Size() - offset
	}
	return backend.Reader{Reader: io.NewSectionReader(f, offset, length), Closer: f}, nil
}

// knownAlgorithms lists host key algorithms for addr's plain-text entries in a
// known_hosts file. Hashed entries cannot be matched here, so nil (the library
// default) is returned when nothing matches.
func knownAlgorithms(file, addr string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	want := knownhosts.Normalize(addr)
	seen := map[string]bool{}
	var algorithms []string
	add := func(names ...string) {
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				algorithms = append(algorithms, n)
			}
		}
	}
	for len(data) > 0 {
		marker, hosts, key, _, rest, err := ssh.ParseKnownHosts(data)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		data = rest
		if marker != "" {
			continue
		}
		for _, h := range hosts {
			if knownhosts.Normalize(h) != want {
				continue
			}
			add(hostKeyAlgorithms(key)...)
		}
	}
	return algorithms, nil
}

func hostKeyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{key.Type()}
}

func (c *Client) versionTag(key string, size int64, modified time.Time) string {
	return backend.VersionTag(fmt.Sprintf("sftp:%q:%q:%q", c.cfg.Addr, c.cfg.Username, c.cfg.Root), key, size, modified)
}
