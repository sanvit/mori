package s3

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPresignAWSOfficialVector(t *testing.T) {
	c := testConfig()
	c.Region = "us-east-1"
	c.AccessKey = "AKIAIOSFODNN7EXAMPLE"
	c.SecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	c.PresignTTL = 24 * time.Hour
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	got, err := PresignURL(*u, "GET", c, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := url.Parse(got)
	if out.Query().Get("X-Amz-Signature") != "aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404" {
		t.Fatal("AWS official signature mismatch", got)
	}
}
func TestPresignAgainstBotocoreFixtures(t *testing.T) {
	data, err := os.ReadFile("../../tests/fixtures/presign-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Method, URL, Token string
			SignedURL          string `json:"signed_url"`
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, item := range fixture.Cases {
		t.Run(item.Method+item.URL, func(t *testing.T) {
			c := testConfig()
			c.AccessKey = "TESTACCESS"
			c.SecretKey = "test-secret-key"
			c.SessionToken = item.Token
			c.PresignTTL = 15 * time.Minute
			u, _ := url.Parse(item.URL)
			got, err := PresignURL(*u, item.Method, c, time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC))
			if item.Method != http.MethodGet {
				if err == nil {
					t.Fatal("non-GET URL signed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := url.Parse(got)
			want, _ := url.Parse(item.SignedURL)
			if actual.Scheme != want.Scheme || actual.Host != want.Host || actual.EscapedPath() != want.EscapedPath() || actual.Query().Encode() != want.Query().Encode() {
				t.Fatalf("botocore mismatch\ngot  %s\nwant %s", got, item.SignedURL)
			}
		})
	}
}
func TestPresignPublicEndpointPrefixAndSafePreview(t *testing.T) {
	c := testConfig()
	c.Endpoint, _ = url.Parse("https://private.unreachable.test:9000")
	c.PresignEndpoint, _ = url.Parse("https://objects.example.test/gateway")
	c.Prefix = "public/"
	c.AccessKey = "TESTACCESS"
	c.SecretKey = "test-secret-key"
	c.SessionToken = "test-session+/="
	c.PresignTTL = 15 * time.Minute
	for _, key := range []string{"docs/한글 +&%#.txt", "evil.html", "evil.svg", "image.png", "report.pdf", "a%2Fb.txt"} {
		link, err := New(c).Presign("GET", key, false, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(link)
		if u.Host != "objects.example.test" || u.Path != "/gateway/test-bucket/public/"+key || u.Query().Get("X-Amz-Security-Token") != c.SessionToken {
			t.Fatal(u)
		}
		if strings.HasPrefix(key, "evil") && u.Query().Get("response-content-type") != "text/plain; charset=utf-8" {
			t.Fatal("active preview allowed")
		}
	}
	c.PathStyle = false
	link, err := New(c).Presign("GET", "report.pdf", false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	if u.Host != "test-bucket.objects.example.test" || u.Path != "/gateway/public/report.pdf" || u.Query().Get("response-content-type") != "application/pdf" {
		t.Fatal(u)
	}
	if _, err = New(c).Presign("PUT", "report.pdf", false, time.Now()); err == nil {
		t.Fatal("write presigned")
	}
	if _, err = New(c).Presign("GET", "../secret", false, time.Now()); err == nil {
		t.Fatal("path escape")
	}
	c.AccessKey = ""
	if _, err = New(c).Presign(http.MethodGet, "a", false, time.Now()); err == nil {
		t.Fatal("anonymous presigning")
	}
}

func TestPresignHostMatchesBrowserNormalization(t *testing.T) {
	c := testConfig()
	c.AccessKey = "TESTACCESS"
	c.SecretKey = "secret"
	c.PresignTTL = time.Minute
	for _, raw := range []string{"https://BUCKET.Example.test:443/file.txt", "http://BUCKET.Example.test:80/file.txt"} {
		u, _ := url.Parse(raw)
		got, err := PresignURL(*u, "GET", c, time.Unix(1700000000, 0))
		if err != nil {
			t.Fatal(err)
		}
		normalized, _ := url.Parse(got)
		if normalized.Host != "bucket.example.test" {
			t.Fatal(normalized.Host)
		}
		u.Host = "bucket.example.test"
		want, _ := PresignURL(*u, "GET", c, time.Unix(1700000000, 0))
		if got != want {
			t.Fatal("signature changed after browser URL normalization")
		}
	}
}
