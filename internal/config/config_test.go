package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateEnv unsets every mori setting for the duration of a test, so results
// do not depend on the environment running the tests. Docker builds matter
// here: platforms such as Coolify pass deployment settings as build
// arguments, which appear as environment variables during `go test`.
func isolateEnv(t *testing.T) {
	t.Helper()
	prefixes := []string{"STORAGE_", "BROWSER_", "S3_", "WEBDAV_", "FTP_", "SFTP_", "CACHE_", "SPA_", "ORIGIN_", "ERROR_"}
	exact := map[string]bool{"SERVE_MODE": true, "AUTH_MODE": true, "INDEX_DOCUMENT": true, "LISTEN_ADDR": true, "ACCESS_LOG": true, "HEALTH_PATH": true, "SHUTDOWN_TIMEOUT": true}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		match := exact[k]
		for _, prefix := range prefixes {
			match = match || strings.HasPrefix(k, prefix)
		}
		if match {
			t.Setenv(k, "") // registers restoration of the original value
			os.Unsetenv(k)
		}
	}
}

func TestValidateKeys(t *testing.T) {
	for _, key := range []string{"../secret", "a/../b", "a/./b", "/root", "a\\b", "a//b", "a\x00b", strings.Repeat("x", 1025)} {
		if ValidateKey(key, true) == nil {
			t.Errorf("accepted %q", key)
		}
	}
	for _, key := range []string{"한글 +&%#?.txt", "folder/name.txt", "folder/", "a%2Fb"} {
		if e := ValidateKey(key, false); e != nil {
			t.Errorf("rejected %q", key)
		}
	}
	if ValidateKey("", false) == nil || ValidateKey("", true) != nil {
		t.Error("empty key policy")
	}
}
func TestConfigFailsClosed(t *testing.T) {
	isolateEnv(t)
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("BROWSER_USERNAME", "")
	t.Setenv("BROWSER_PASSWORD", "")
	t.Setenv("BROWSER_PUBLIC", "false")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	if _, e := Read(); e == nil {
		t.Fatal("unprotected real bucket permitted")
	}
	t.Setenv("BROWSER_USERNAME", "admin")
	t.Setenv("BROWSER_PASSWORD", "test-strong-password")
	if _, e := Read(); e != nil {
		t.Fatal(e)
	}
}
func TestConfigPartialCredentialsAndBadURLs(t *testing.T) {
	isolateEnv(t)
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("S3_ACCESS_KEY_ID", "key")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	if _, e := Read(); e == nil {
		t.Fatal("partial creds accepted")
	}
	for _, u := range []string{"ftp://host", "https://user:pass@host", "https://host?a=b", "https://host#fragment", "http:///path"} {
		if _, e := endpoint(u, "TEST"); e == nil {
			t.Errorf("accepted %s", u)
		}
	}
}
func TestEnvParserPreservesProcessAndLiteralSecrets(t *testing.T) {
	isolateEnv(t)
	t.Setenv("MORI_TEST_PROCESS", "process")
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	os.WriteFile(p, []byte("MORI_TEST_PROCESS=file\nMORI_TEST_QUOTED='a$# value'\nMORI_TEST_COMMENT=ok # comment\n"), 0600)
	defer os.Unsetenv("MORI_TEST_QUOTED")
	defer os.Unsetenv("MORI_TEST_COMMENT")
	if e := LoadEnv(p); e != nil {
		t.Fatal(e)
	}
	if os.Getenv("MORI_TEST_PROCESS") != "process" || os.Getenv("MORI_TEST_QUOTED") != "a$# value" || os.Getenv("MORI_TEST_COMMENT") != "ok" {
		t.Fatal("dotenv parsing")
	}
}

func TestConfigRequiresBucketWithoutFallback(t *testing.T) {
	isolateEnv(t)
	t.Setenv("S3_BUCKET", "")
	t.Setenv("BROWSER_USERNAME", "")
	t.Setenv("BROWSER_PASSWORD", "")
	t.Setenv("BROWSER_PUBLIC", "true")
	if _, e := Read(); e == nil || !strings.Contains(e.Error(), "S3_BUCKET") {
		t.Fatalf("missing bucket did not stop startup: %v", e)
	}
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	t.Setenv("S3_SESSION_TOKEN", "")
	t.Setenv("S3_ENDPOINT", "https://s3.ap-northeast-2.amazonaws.com")
	c, e := Read()
	if e != nil || c.Endpoint == nil || c.Bucket != "test-bucket" {
		t.Fatalf("valid config rejected: %+v %v", c, e)
	}
}

func TestNewEnvValidation(t *testing.T) {
	isolateEnv(t)
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	t.Setenv("S3_SESSION_TOKEN", "")
	c, err := Read()
	if err != nil || c.DownloadMode != "proxy" || c.PreviewMode != "proxy" || c.PresignTTL != 15*time.Minute || c.ZipMaxFiles != 200 || c.ZipMaxBytes != 20<<30 || c.ZipConcurrency != 2 {
		t.Fatal(c, err)
	}
	if c.ZipDisabled {
		t.Fatal("ZIP must be enabled by default")
	}
	t.Setenv("BROWSER_ZIP_ENABLED", "false")
	if c, err = Read(); err != nil || !c.ZipDisabled {
		t.Fatal("BROWSER_ZIP_ENABLED=false ignored", err)
	}
	t.Setenv("BROWSER_ZIP_ENABLED", "maybe")
	if _, err = Read(); err == nil {
		t.Fatal("invalid BROWSER_ZIP_ENABLED accepted")
	}
	t.Setenv("BROWSER_ZIP_ENABLED", "")
	t.Setenv("BROWSER_DOWNLOAD_MODE", "presigned")
	if _, err = Read(); err == nil {
		t.Fatal("anonymous presigned config accepted")
	}
	t.Setenv("S3_ACCESS_KEY_ID", "TESTACCESS")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret-key")
	if _, err = Read(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ k, v string }{{"BROWSER_DOWNLOAD_MODE", "direct"}, {"BROWSER_PREVIEW_MODE", "invalid"}, {"BROWSER_PRESIGN_TTL", "169h"}, {"BROWSER_PRESIGN_TTL", "0s"}, {"BROWSER_PRESIGN_TTL", "1.5s"}, {"BROWSER_ZIP_MAX_FILES", "0"}, {"BROWSER_ZIP_MAX_FILES", "10001"}, {"BROWSER_ZIP_CONCURRENCY", "17"}, {"BROWSER_ZIP_MAX_SIZE", "0"}, {"BROWSER_ZIP_MAX_SIZE", "999999999999999999999GB"}, {"BROWSER_PRESIGN_ENDPOINT", "https://host?token=x"}} {
		t.Run(test.k+test.v, func(t *testing.T) {
			t.Setenv(test.k, test.v)
			if _, err := Read(); err == nil {
				t.Fatal("bad env accepted")
			}
		})
	}
}

func TestBackendSelection(t *testing.T) {
	isolateEnv(t)
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("S3_BUCKET", "")
	t.Setenv("STORAGE_BACKEND", "webdav")
	if _, err := Read(); err == nil {
		t.Fatal("webdav without URL accepted")
	}
	t.Setenv("WEBDAV_URL", "https://dav.example.test/files/")
	c, err := Read()
	if err != nil || c.Backend != "webdav" || c.WebDAV.URL.Path != "/files/" || c.Prefix != "" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("WEBDAV_USERNAME", "u")
	if _, err = Read(); err == nil {
		t.Fatal("partial WebDAV credentials accepted")
	}
	t.Setenv("WEBDAV_PASSWORD", "p")
	t.Setenv("BROWSER_PREVIEW_MODE", "presigned")
	if _, err = Read(); err == nil {
		t.Fatal("presigned mode accepted without S3")
	}
	t.Setenv("BROWSER_PREVIEW_MODE", "proxy")

	t.Setenv("STORAGE_BACKEND", "ftp")
	if _, err = Read(); err == nil {
		t.Fatal("ftp without address accepted")
	}
	t.Setenv("FTP_ADDR", "files.example.test")
	c, err = Read()
	if err != nil || c.FTP.Addr != "files.example.test:21" || c.FTP.Username != "anonymous" || c.FTP.Password != "anonymous@" || c.FTP.Root != "/" {
		t.Fatalf("%+v %v", c.FTP, err)
	}
	t.Setenv("FTP_ADDR", "[::1]:2121")
	t.Setenv("FTP_PATH", "/pub/../etc")
	if _, err = Read(); err == nil {
		t.Fatal("dot segments in FTP_PATH accepted")
	}
	t.Setenv("FTP_PATH", "/pub/data/")
	if c, err = Read(); err != nil || c.FTP.Addr != "[::1]:2121" || c.FTP.Root != "/pub/data" {
		t.Fatalf("%+v %v", c.FTP, err)
	}

	t.Setenv("STORAGE_BACKEND", "sftp")
	t.Setenv("SFTP_ADDR", "host.example.test")
	t.Setenv("SFTP_USERNAME", "u")
	if _, err = Read(); err == nil {
		t.Fatal("sftp without credentials accepted")
	}
	t.Setenv("SFTP_PASSWORD", "p")
	if _, err = Read(); err == nil {
		t.Fatal("sftp without host key policy accepted")
	}
	t.Setenv("SFTP_INSECURE_HOST_KEY", "true")
	if c, err = Read(); err != nil || c.SFTP.Addr != "host.example.test:22" || c.SFTP.Root != "." || !c.SFTP.InsecureHostKey {
		t.Fatalf("%+v %v", c.SFTP, err)
	}
	t.Setenv("STORAGE_BACKEND", "gopher")
	if _, err = Read(); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestStorageBasePath(t *testing.T) {
	isolateEnv(t)
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	t.Setenv("S3_SESSION_TOKEN", "")
	t.Setenv("S3_PREFIX", "public")
	t.Setenv("STORAGE_BASE_PATH", "/team/한글 docs/")
	c, err := Read()
	if err != nil || c.BasePath != "team/한글 docs/" || c.Prefix != "public/team/한글 docs/" {
		t.Fatalf("%q %q %v", c.BasePath, c.Prefix, err)
	}
	t.Setenv("S3_PREFIX", "")
	t.Setenv("STORAGE_BASE_PATH", "team")
	if c, err = Read(); err != nil || c.Prefix != "team/" {
		t.Fatalf("%q %v", c.Prefix, err)
	}
	for _, bad := range []string{"../etc", "a/../b", "a/./b", "a//b", "a\\b", "a\x1bb", strings.Repeat("x", 600)} {
		t.Setenv("STORAGE_BASE_PATH", bad)
		if _, err = Read(); err == nil {
			t.Errorf("accepted STORAGE_BASE_PATH %q", bad)
		}
	}
	for _, empty := range []string{"", "/", " // "} {
		t.Setenv("STORAGE_BASE_PATH", empty)
		if c, err = Read(); err != nil || c.BasePath != "" || c.Prefix != "" {
			t.Fatalf("%q: %q %q %v", empty, c.BasePath, c.Prefix, err)
		}
	}

	t.Setenv("S3_BUCKET", "")
	t.Setenv("STORAGE_BASE_PATH", "shared/reports/")
	t.Setenv("STORAGE_BACKEND", "webdav")
	t.Setenv("WEBDAV_URL", "https://dav.example.test/remote.php/dav/")
	if c, err = Read(); err != nil || c.WebDAV.URL.Path != "/remote.php/dav/shared/reports/" || c.Prefix != "" {
		t.Fatalf("%+v %v", c.WebDAV.URL, err)
	}
	t.Setenv("WEBDAV_URL", "https://dav.example.test")
	if c, err = Read(); err != nil || c.WebDAV.URL.Path != "/shared/reports/" {
		t.Fatalf("%+v %v", c.WebDAV.URL, err)
	}

	t.Setenv("STORAGE_BACKEND", "ftp")
	t.Setenv("FTP_ADDR", "ftp.example.test")
	for root, want := range map[string]string{"/": "/shared/reports", "/pub": "/pub/shared/reports", "files": "files/shared/reports"} {
		t.Setenv("FTP_PATH", root)
		if c, err = Read(); err != nil || c.FTP.Root != want {
			t.Fatalf("FTP_PATH=%s: %q %v", root, c.FTP.Root, err)
		}
	}

	t.Setenv("STORAGE_BACKEND", "sftp")
	t.Setenv("SFTP_ADDR", "sftp.example.test")
	t.Setenv("SFTP_USERNAME", "u")
	t.Setenv("SFTP_PASSWORD", "p")
	t.Setenv("SFTP_INSECURE_HOST_KEY", "true")
	for root, want := range map[string]string{".": "shared/reports", "/srv": "/srv/shared/reports"} {
		t.Setenv("SFTP_PATH", root)
		if c, err = Read(); err != nil || c.SFTP.Root != want {
			t.Fatalf("SFTP_PATH=%s: %q %v", root, c.SFTP.Root, err)
		}
	}
}
