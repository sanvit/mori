package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	t.Setenv("BROWSER_PROXY_URL", "")
	c, e := Read()
	if e != nil || c.Endpoint == nil || c.Bucket != "test-bucket" {
		t.Fatalf("valid config rejected: %+v %v", c, e)
	}
}

func TestNewEnvValidation(t *testing.T) {
	t.Setenv("S3_BUCKET", "test-bucket")
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("S3_ACCESS_KEY_ID", "")
	t.Setenv("S3_SECRET_ACCESS_KEY", "")
	t.Setenv("S3_SESSION_TOKEN", "")
	c, err := Read()
	if err != nil || c.DownloadMode != "proxy" || c.PreviewMode != "proxy" || c.PresignTTL != 15*time.Minute || c.ZipMaxFiles != 200 || c.ZipMaxBytes != 20<<30 || c.ZipConcurrency != 2 {
		t.Fatal(c, err)
	}
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
	t.Setenv("BROWSER_PROXY_URL", "http://cache:8080")
	if _, err = Read(); err == nil {
		t.Fatal("cache proxy accepted without S3")
	}
	t.Setenv("BROWSER_PROXY_URL", "")

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
