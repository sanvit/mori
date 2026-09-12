package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSFTPInlineSettings(t *testing.T) {
	isolateEnv(t)
	t.Setenv("STORAGE_BACKEND", "sftp")
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("SFTP_ADDR", "sftp.example.test")
	t.Setenv("SFTP_USERNAME", "reader")
	t.Setenv("SFTP_KEY", "private-key-placeholder")
	t.Setenv("SFTP_HOST_KEY", "host-key-placeholder")
	c, err := Read()
	if err != nil || c.SFTP.Key != "private-key-placeholder" || c.SFTP.HostKey != "host-key-placeholder" || c.SFTP.InsecureHostKey {
		t.Fatal("inline configuration rejected", err)
	}
	for _, pair := range [][2]string{{"SFTP_KEY_FILE", "key-file"}, {"SFTP_KNOWN_HOSTS", "known-hosts-file"}, {"SFTP_INSECURE_HOST_KEY", "true"}} {
		t.Run(pair[0], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if _, err := Read(); err == nil {
				t.Fatal("conflicting SFTP settings accepted")
			}
		})
	}
	t.Setenv("SFTP_KEY", "")
	t.Setenv("SFTP_PASSWORD", "test-password")
	t.Setenv("SFTP_KEY_PASSPHRASE", "test-passphrase")
	if _, err := Read(); err == nil {
		t.Fatal("passphrase without a key accepted")
	}
}

func TestSFTPKeyDotEnvEscapes(t *testing.T) {
	isolateEnv(t)
	t.Setenv("SFTP_KEY", "")
	os.Unsetenv("SFTP_KEY") // restore original state through t.Setenv cleanup
	file := filepath.Join(t.TempDir(), "test.env")
	if err := os.WriteFile(file, []byte("SFTP_KEY=\"-----BEGIN OPENSSH PRIVATE KEY-----\\nTEST-ONLY\\n-----END OPENSSH PRIVATE KEY-----\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadEnv(file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(os.Getenv("SFTP_KEY"), "\nTEST-ONLY\n") {
		t.Fatal("quoted newline escapes were not decoded")
	}
}
