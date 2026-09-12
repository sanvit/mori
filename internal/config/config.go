// Package config reads mori's environment configuration and validates object keys.
package config

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"mori/internal/objectcache"
)

type Config struct {
	ServeMode, CacheMode, HealthPath                                          string
	ObjectCache                                                               objectcache.Config
	Listen, Title, Bucket, Region, Prefix, AccessKey, SecretKey, SessionToken string
	Endpoint                                                                  *url.URL
	// BasePath (STORAGE_BASE_PATH) is the folder published as the browser
	// root, relative to the backend root. It is "" or ends with "/". For S3 it
	// is already folded into Prefix.
	BasePath                        string
	PathStyle, Public, CacheEnabled bool
	Username, Password              string
	ListingTTL                      time.Duration
	ShutdownTimeout                 time.Duration
	ListingMax                      int
	DownloadMode, PreviewMode       string
	PresignTTL                      time.Duration
	PresignEndpoint                 *url.URL
	ZipMaxFiles, ZipConcurrency     int
	ZipMaxBytes                     int64

	// HTML capabilities are opt-in and shared by previews and new tabs.
	HTMLPreviewEnabled           bool
	HTMLPreviewScripts           bool
	HTMLPreviewExternalResources bool

	// ZipDisabled turns off multi-file ZIP downloads (BROWSER_ZIP_ENABLED=false).
	// The zero value keeps ZIP enabled, matching the default.
	ZipDisabled bool
	// Backend selects the storage: s3 (default), webdav, ftp, or sftp. The S3
	// fields above and presigned delivery apply to
	// s3 only. The internal object cache supports every backend.
	Backend string
	WebDAV  WebDAVConfig
	FTP     FTPConfig
	SFTP    SFTPConfig
}

// WebDAVConfig targets one collection URL with optional Basic credentials.
type WebDAVConfig struct {
	URL                *url.URL
	Username, Password string
}

// FTPConfig targets host:port with a root directory. TLS enables explicit FTPS.
type FTPConfig struct {
	Addr, Username, Password, Root string
	TLS                            bool
}

// SFTPConfig targets host:port with password and/or private-key auth. The host
// key is pinned by HostKey or verified against KnownHosts, unless
// InsecureHostKey is set explicitly. Key contains inline OpenSSH/PEM text.
type SFTPConfig struct {
	Addr, Username, Password, KeyFile, KnownHosts, Root string
	Key, KeyPassphrase, HostKey                         string
	InsecureHostKey                                     bool
}

// LoadEnv intentionally does not expand shell expressions or overwrite process variables.
func LoadEnv(path string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 256*1024)
	for line := 1; scan.Scan(); line++ {
		s := strings.TrimSpace(scan.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		s = strings.TrimPrefix(s, "export ")
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" || strings.ContainsAny(k, " \t\r\n") {
			return fmt.Errorf(".env line %d: expected KEY=value", line)
		}
		if strings.HasPrefix(v, "\"") {
			x, e := strconv.Unquote(v)
			if e != nil {
				return fmt.Errorf(".env line %d: invalid quoted value", line)
			}
			v = x
		} else if strings.HasPrefix(v, "'") {
			if len(v) < 2 || !strings.HasSuffix(v, "'") {
				return fmt.Errorf(".env line %d: unterminated quote", line)
			}
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if _, exists := os.LookupEnv(k); !exists {
			if e := os.Setenv(k, v); e != nil {
				return fmt.Errorf(".env line %d: invalid key", line)
			}
		}
	}
	return scan.Err()
}

// Env returns the process environment variable k, or d when it is unset.
func Env(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}
func envBool(k string, d bool) (bool, error) {
	v, ok := os.LookupEnv(k)
	if !ok || v == "" {
		return d, nil
	}
	b, e := strconv.ParseBool(v)
	if e != nil {
		return false, fmt.Errorf("%s must be true or false", k)
	}
	return b, nil
}
func endpoint(v, name string) (*url.URL, error) {
	u, e := url.Parse(v)
	if e != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%s must be an http(s) URL without credentials, query, or fragment", name)
	}
	for _, ch := range u.Host {
		if ch > 127 {
			return nil, fmt.Errorf("%s hostname must be ASCII (use punycode for an IDN)", name)
		}
	}
	return u, nil
}

// Read builds a Config from the process environment and validates it.
func Read() (Config, error) {
	c := Config{Listen: Env("BROWSER_LISTEN_ADDR", ":8080"), Title: Env("BROWSER_TITLE", "Files"), Bucket: Env("S3_BUCKET", ""), Region: Env("S3_REGION", "ap-northeast-2"), AccessKey: Env("S3_ACCESS_KEY_ID", ""), SecretKey: Env("S3_SECRET_ACCESS_KEY", ""), SessionToken: Env("S3_SESSION_TOKEN", ""), Username: Env("BROWSER_USERNAME", ""), Password: Env("BROWSER_PASSWORD", ""), ListingMax: 512}
	var err error
	if err = readServeMode(&c); err != nil {
		return c, err
	}
	if c.Public, err = envBool("BROWSER_PUBLIC", false); err != nil {
		return c, err
	}
	switch Env("AUTH_MODE", "") {
	case "":
	case "public":
		c.Public = true
		c.Username = ""
		c.Password = ""
	case "basic":
		c.Public = false
	default:
		return c, fmt.Errorf("AUTH_MODE must be basic or public")
	}
	if c.HTMLPreviewEnabled, err = envBool("BROWSER_HTML_PREVIEW_ENABLED", false); err != nil {
		return c, err
	}
	if c.HTMLPreviewScripts, err = envBool("BROWSER_HTML_PREVIEW_SCRIPTS", false); err != nil {
		return c, err
	}
	if c.HTMLPreviewExternalResources, err = envBool("BROWSER_HTML_PREVIEW_EXTERNAL_RESOURCES", false); err != nil {
		return c, err
	}
	if c.PathStyle, err = envBool("S3_FORCE_PATH_STYLE", true); err != nil {
		return c, err
	}
	if c.CacheEnabled, err = envBool("CACHE_ENABLED", true); err != nil {
		return c, err
	}
	if c.ShutdownTimeout, err = time.ParseDuration(Env("SHUTDOWN_TIMEOUT", "30s")); err != nil || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 24*time.Hour {
		return c, fmt.Errorf("SHUTDOWN_TIMEOUT must be a positive duration up to 24h")
	}
	if c.ListingTTL, err = time.ParseDuration(Env("BROWSER_LIST_TTL", "30s")); err != nil || c.ListingTTL < 0 || c.ListingTTL > 24*time.Hour {
		return c, fmt.Errorf("BROWSER_LIST_TTL must be a duration between 0s and 24h")
	}
	c.Backend = strings.ToLower(strings.TrimSpace(Env("STORAGE_BACKEND", "s3")))
	if (c.Username == "") != (c.Password == "") {
		return c, fmt.Errorf("BROWSER_USERNAME and BROWSER_PASSWORD must both be set")
	}
	if !c.Public && c.Username == "" {
		return c, fmt.Errorf("set BROWSER_USERNAME/BROWSER_PASSWORD, or explicitly set BROWSER_PUBLIC=true to publish the configured storage")
	}
	if c.DownloadMode, err = deliveryMode("BROWSER_DOWNLOAD_MODE"); err != nil {
		return c, err
	}
	if c.PreviewMode, err = deliveryMode("BROWSER_PREVIEW_MODE"); err != nil {
		return c, err
	}
	if c.PresignTTL, err = time.ParseDuration(Env("BROWSER_PRESIGN_TTL", "15m")); err != nil || c.PresignTTL < time.Second || c.PresignTTL > 7*24*time.Hour || c.PresignTTL%time.Second != 0 {
		return c, fmt.Errorf("BROWSER_PRESIGN_TTL must be whole seconds between 1s and 168h")
	}
	switch c.Backend {
	case "s3":
		err = readS3(&c)
	case "webdav":
		err = readWebDAV(&c)
	case "ftp":
		err = readFTP(&c)
	case "sftp":
		err = readSFTP(&c)
	default:
		err = fmt.Errorf("STORAGE_BACKEND must be s3, webdav, ftp, or sftp")
	}
	if err != nil {
		return c, err
	}
	if c.BasePath, err = basePath(Env("STORAGE_BASE_PATH", "")); err != nil {
		return c, err
	}
	if c.BasePath != "" {
		dir := strings.TrimSuffix(c.BasePath, "/")
		switch c.Backend {
		case "s3":
			c.Prefix += c.BasePath
			if len(c.Prefix) > 512 {
				return c, fmt.Errorf("S3_PREFIX and STORAGE_BASE_PATH together must be at most 512 bytes")
			}
		case "webdav":
			u := *c.WebDAV.URL
			u.Path = strings.TrimSuffix(u.Path, "/") + "/" + c.BasePath
			u.RawPath = ""
			c.WebDAV.URL = &u
		case "ftp":
			c.FTP.Root = path.Join(c.FTP.Root, dir)
		case "sftp":
			c.SFTP.Root = path.Join(c.SFTP.Root, dir)
		}
	}
	if c.Backend != "s3" {
		if c.DownloadMode == "presigned" || c.PreviewMode == "presigned" {
			return c, fmt.Errorf("presigned delivery modes require STORAGE_BACKEND=s3")
		}
	}
	zipEnabled, err := envBool("BROWSER_ZIP_ENABLED", true)
	if err != nil {
		return c, err
	}
	c.ZipDisabled = !zipEnabled
	if c.ZipMaxFiles, err = boundedInt("BROWSER_ZIP_MAX_FILES", "200", 1, 10000); err != nil {
		return c, err
	}
	if c.ZipConcurrency, err = boundedInt("BROWSER_ZIP_CONCURRENCY", "2", 1, 16); err != nil {
		return c, err
	}
	if c.ZipMaxBytes, err = byteSize(Env("BROWSER_ZIP_MAX_SIZE", "20GiB")); err != nil || c.ZipMaxBytes < 1 || c.ZipMaxBytes > 1<<50 {
		return c, fmt.Errorf("BROWSER_ZIP_MAX_SIZE must be a positive integer size up to 1PiB, e.g. 20GiB")
	}
	if err = readObjectOptions(&c); err != nil {
		return c, err
	}
	return c, nil
}

// ValidateKey rejects dot segments rather than normalizing them: object keys must
// never escape the configured prefix through a normalizing HTTP reverse proxy.
func ValidateKey(key string, allowEmpty bool) error {
	if (!allowEmpty && key == "") || len(key) > 1024 || strings.HasPrefix(key, "/") || strings.Contains(key, "\\") || strings.Contains(key, "//") {
		return fmt.Errorf("invalid object key")
	}
	for _, r := range key {
		if r < 32 || r == 127 {
			return fmt.Errorf("control character in object key")
		}
	}
	for _, s := range strings.Split(key, "/") {
		if s == "." || s == ".." {
			return fmt.Errorf("dot segments are not supported")
		}
	}
	return nil
}

func readS3(c *Config) error {
	var err error
	c.Prefix = strings.Trim(Env("S3_PREFIX", ""), "/")
	if c.Prefix != "" {
		c.Prefix += "/"
	}
	if err = ValidateKey(c.Prefix, true); err != nil {
		return fmt.Errorf("invalid S3_PREFIX: %w", err)
	}
	if c.Bucket == "" || strings.ContainsAny(c.Bucket, "/\\?# \t\r\n") {
		return fmt.Errorf("a valid S3_BUCKET is required")
	}
	if c.Region == "" {
		return fmt.Errorf("S3_REGION is required")
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		return fmt.Errorf("S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must both be set (or both empty for anonymous access)")
	}
	if c.SessionToken != "" && c.AccessKey == "" {
		return fmt.Errorf("S3_SESSION_TOKEN requires access and secret keys")
	}
	if c.Endpoint, err = endpoint(Env("S3_ENDPOINT", "https://s3."+c.Region+".amazonaws.com"), "S3_ENDPOINT"); err != nil {
		return err
	}
	if p := Env("BROWSER_PRESIGN_ENDPOINT", ""); p != "" {
		if c.PresignEndpoint, err = endpoint(p, "BROWSER_PRESIGN_ENDPOINT"); err != nil {
			return err
		}
	}
	if (c.DownloadMode == "presigned" || c.PreviewMode == "presigned") && (c.AccessKey == "" || c.SecretKey == "") {
		return fmt.Errorf("presigned mode requires S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY")
	}
	return nil
}
func readWebDAV(c *Config) error {
	var err error
	if c.WebDAV.URL, err = endpoint(Env("WEBDAV_URL", ""), "WEBDAV_URL"); err != nil {
		return err
	}
	c.WebDAV.Username, c.WebDAV.Password = Env("WEBDAV_USERNAME", ""), Env("WEBDAV_PASSWORD", "")
	if (c.WebDAV.Username == "") != (c.WebDAV.Password == "") {
		return fmt.Errorf("WEBDAV_USERNAME and WEBDAV_PASSWORD must both be set")
	}
	return nil
}
func readFTP(c *Config) error {
	var err error
	if c.FTP.Addr, err = hostPort(Env("FTP_ADDR", ""), "21", "FTP_ADDR"); err != nil {
		return err
	}
	c.FTP.Username, c.FTP.Password = Env("FTP_USERNAME", "anonymous"), Env("FTP_PASSWORD", "")
	if c.FTP.Username == "anonymous" && c.FTP.Password == "" {
		c.FTP.Password = "anonymous@"
	}
	if c.FTP.TLS, err = envBool("FTP_TLS", false); err != nil {
		return err
	}
	if c.FTP.Root, err = rootPath(Env("FTP_PATH", "/"), "FTP_PATH"); err != nil {
		return err
	}
	return nil
}
func readSFTP(c *Config) error {
	var err error
	if c.SFTP.Addr, err = hostPort(Env("SFTP_ADDR", ""), "22", "SFTP_ADDR"); err != nil {
		return err
	}
	c.SFTP.Username, c.SFTP.Password, c.SFTP.KeyFile = Env("SFTP_USERNAME", ""), Env("SFTP_PASSWORD", ""), Env("SFTP_KEY_FILE", "")
	c.SFTP.Key, c.SFTP.KeyPassphrase, c.SFTP.HostKey = Env("SFTP_KEY", ""), Env("SFTP_KEY_PASSPHRASE", ""), Env("SFTP_HOST_KEY", "")
	if c.SFTP.Key != "" && c.SFTP.KeyFile != "" {
		return fmt.Errorf("set only one of SFTP_KEY and SFTP_KEY_FILE")
	}
	if c.SFTP.KeyPassphrase != "" && c.SFTP.Key == "" && c.SFTP.KeyFile == "" {
		return fmt.Errorf("SFTP_KEY_PASSPHRASE requires SFTP_KEY or SFTP_KEY_FILE")
	}
	if c.SFTP.Username == "" || (c.SFTP.Password == "" && c.SFTP.KeyFile == "" && c.SFTP.Key == "") {
		return fmt.Errorf("SFTP_USERNAME and SFTP_PASSWORD, SFTP_KEY, or SFTP_KEY_FILE are required")
	}
	c.SFTP.KnownHosts = Env("SFTP_KNOWN_HOSTS", "")
	if c.SFTP.HostKey != "" && c.SFTP.KnownHosts != "" {
		return fmt.Errorf("set only one of SFTP_HOST_KEY and SFTP_KNOWN_HOSTS")
	}
	if c.SFTP.InsecureHostKey, err = envBool("SFTP_INSECURE_HOST_KEY", false); err != nil {
		return err
	}
	if c.SFTP.InsecureHostKey && (c.SFTP.HostKey != "" || c.SFTP.KnownHosts != "") {
		return fmt.Errorf("SFTP_INSECURE_HOST_KEY conflicts with SFTP_HOST_KEY or SFTP_KNOWN_HOSTS")
	}
	if c.SFTP.KnownHosts == "" && c.SFTP.HostKey == "" && !c.SFTP.InsecureHostKey {
		return fmt.Errorf("set SFTP_HOST_KEY or SFTP_KNOWN_HOSTS, or explicitly set SFTP_INSECURE_HOST_KEY=true to skip host key verification")
	}
	if c.SFTP.Root, err = rootPath(Env("SFTP_PATH", "."), "SFTP_PATH"); err != nil {
		return err
	}
	return nil
}

// basePath normalizes STORAGE_BASE_PATH to "" or "a/b/". Leading and trailing
// slashes are optional; dot segments and other unsafe keys are rejected, so the
// published root can never be widened with "..".
func basePath(v string) (string, error) {
	v = strings.Trim(strings.TrimSpace(v), "/")
	if v == "" {
		return "", nil
	}
	v += "/"
	if len(v) > 512 {
		return "", fmt.Errorf("STORAGE_BASE_PATH must be at most 512 bytes")
	}
	if err := ValidateKey(v, false); err != nil {
		return "", fmt.Errorf("invalid STORAGE_BASE_PATH: %w", err)
	}
	return v, nil
}

// hostPort validates host[:port], adding the default port when missing.
func hostPort(v, defaultPort, name string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || strings.ContainsAny(v, "/?# \t\r\n") {
		return "", fmt.Errorf("%s must be host or host:port", name)
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		host, port = strings.Trim(v, "[]"), defaultPort
	}
	if host == "" || port == "" {
		return "", fmt.Errorf("%s must be host or host:port", name)
	}
	return net.JoinHostPort(host, port), nil
}

// rootPath accepts an absolute or home-relative remote directory without dot segments.
func rootPath(v, name string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "." {
		return ".", nil
	}
	if strings.Contains(v, "\\") || strings.Contains(v, "//") || strings.ContainsAny(v, "\x00\r\n") {
		return "", fmt.Errorf("%s must be a plain remote directory path", name)
	}
	for _, seg := range strings.Split(strings.Trim(v, "/"), "/") {
		if seg == "." || seg == ".." {
			return "", fmt.Errorf("%s must not contain dot segments", name)
		}
	}
	if trimmed := strings.TrimSuffix(v, "/"); trimmed != "" {
		return trimmed, nil
	}
	return "/", nil
}

func deliveryMode(name string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(Env(name, "proxy")))
	if mode != "proxy" && mode != "presigned" {
		return "", fmt.Errorf("%s must be proxy or presigned", name)
	}
	return mode, nil
}
func boundedInt(name, fallback string, low, high int) (int, error) {
	v, err := strconv.Atoi(Env(name, fallback))
	if err != nil || v < low || v > high {
		return 0, fmt.Errorf("%s must be between %d and %d", name, low, high)
	}
	return v, nil
}
func byteSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	units := map[string]int64{"": 1, "B": 1, "KB": 1000, "MB": 1000000, "GB": 1000000000, "TB": 1000000000000, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40, "PIB": 1 << 50}
	unit, ok := units[strings.ToUpper(strings.TrimSpace(s[i:]))]
	if err != nil || !ok || n > (1<<63-1)/unit {
		return 0, fmt.Errorf("invalid byte size")
	}
	return n * unit, nil
}
