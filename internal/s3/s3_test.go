package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"mori/internal/config"
)

func testConfig() config.Config {
	u, _ := url.Parse("http://127.0.0.1:1")
	return config.Config{Endpoint: u, Title: "Test", Bucket: "test-bucket", Region: "ap-northeast-2", PathStyle: true, ListingTTL: time.Minute, ListingMax: 4, CacheEnabled: true}
}
func TestSigV4AgainstBotocore(t *testing.T) {
	data, e := os.ReadFile("../../tests/fixtures/sigv4-fixtures.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct{ URL, Token, Authorization string }
	if e = json.Unmarshal(data, &cases); e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		t.Run(tc.URL, func(t *testing.T) {
			c := testConfig()
			c.AccessKey = "TESTACCESS"
			c.SecretKey = "test-secret-key"
			c.SessionToken = tc.Token
			r, _ := http.NewRequest("GET", tc.URL, nil)
			signRequest(r, c, time.Date(2026, 9, 9, 6, 0, 0, 0, time.UTC))
			if r.Header.Get("Authorization") != tc.Authorization {
				t.Fatalf("signature mismatch\n got %s\nwant %s", r.Header.Get("Authorization"), tc.Authorization)
			}
		})
	}
}
func TestURIAndQueryEscaping(t *testing.T) {
	if got := uriEscape("a +&%#/@한"); got != "a%20%2B%26%25%23%2F%40%ED%95%9C" {
		t.Fatal(got)
	}
	q := url.Values{"a": {"z", " "}, "a+": {"/"}, "한": {"x"}}
	if got := awsQuery(q); got != "%ED%95%9C=x&a=%20&a=z&a%2B=%2F" {
		t.Fatal(got)
	}
	if escapedPath("a//한 +.txt") != "a//%ED%95%9C%20%2B.txt" {
		t.Fatal("path normalization occurred")
	}
}
func TestListObjectsPaginationAndPrefix(t *testing.T) {
	c := testConfig()
	c.Prefix = "public/"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-bucket/" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("prefix") != "public/한글/" || q.Get("continuation-token") != "a+b/=" || q.Get("delimiter") != "/" || q.Get("encoding-type") != "url" {
			t.Errorf("query %v", q)
		}
		w.Header().Set("Content-Type", "application/xml")
		io.WriteString(w, `<ListBucketResult><EncodingType>url</EncodingType><CommonPrefixes><Prefix>public%2F%ED%95%9C%EA%B8%80%2Fsub%2F</Prefix></CommonPrefixes><Contents><Key>public%2F%ED%95%9C%EA%B8%80%2Fa%20%2B%26%25%23.txt</Key><Size>12</Size><ETag>&quot;abc&quot;</ETag><LastModified>2026-09-09T00:00:00Z</LastModified></Contents><Contents><Key>public%2F%ED%95%9C%EA%B8%80%2F</Key><Size>0</Size></Contents><Contents><Key>private%2Fsecret.txt</Key><Size>1</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>opaque+/=</NextContinuationToken></ListBucketResult>`)
	}))
	defer server.Close()
	c.Endpoint, _ = url.Parse(server.URL)
	l, e := New(c).List(context.Background(), "한글/", "a+b/=")
	if e != nil {
		t.Fatal(e)
	}
	if len(l.Entries) != 2 || l.Entries[0].Key != "한글/sub/" || l.Entries[1].Key != "한글/a +&%#.txt" || l.Cursor != "opaque+/=" || l.Entries[1].ETag != `"abc"` {
		t.Fatalf("%+v", l)
	}
}
func TestListDoesNotCacheErrorsOrLoopTokens(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{403, `<Error><Code>AccessDenied</Code></Error>`}, {200, `<ListBucketResult><IsTruncated>true</IsTruncated></ListBucketResult>`}, {200, `<not-xml`}} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); io.WriteString(w, tc.body) }))
			defer s.Close()
			c := testConfig()
			c.Endpoint, _ = url.Parse(s.URL)
			if _, e := New(c).List(context.Background(), "", ""); e == nil {
				t.Fatal("expected error")
			}
		})
	}
}
func TestOriginSeparatesCredentialsAndPreservesPrefix(t *testing.T) {
	var seen bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = true
		if r.URL.Path != "/base/test-bucket/public/a +&%#.txt" || r.URL.RawQuery != "" {
			t.Errorf("unexpected origin URL %s", r.URL.String())
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || r.Header.Get("Cookie") != "" {
			t.Error("credential leak")
		}
		if r.Header.Get("Range") != "bytes=2-5" || r.Header.Get("If-Match") != `"v1"` {
			t.Error("range/condition lost")
		}
		w.Header().Set("X-Cache", "HIT")
		w.WriteHeader(206)
		io.WriteString(w, "2345")
	}))
	defer s.Close()
	c := testConfig()
	c.Prefix = "public/"
	c.AccessKey = "TEST"
	c.SecretKey = "SECRET"
	c.Endpoint, _ = url.Parse(s.URL + "/base")
	r, e := New(c).Object(context.Background(), "GET", "a +&%#.txt", http.Header{"Range": {"bytes=2-5"}, "If-Match": {`"v1"`}, "Authorization": {"Basic secret"}, "Cookie": {"session=secret"}})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	if !seen || r.Header.Get("X-Cache") != "HIT" {
		t.Fatal("origin request failed")
	}
}
func TestDirectOriginSignsAndPreservesKey(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-bucket/public/한글 +%&.txt" {
			t.Error(r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=TEST/") {
			t.Error("not signed")
		}
		io.WriteString(w, "ok")
	}))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	c.Prefix = "public/"
	c.AccessKey = "TEST"
	c.SecretKey = "SECRET"
	r, e := New(c).Object(context.Background(), "GET", "한글 +%&.txt", nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
}
func TestOriginDoesNotFollowRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer s.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(s.URL)
	r, e := New(c).Object(context.Background(), "GET", "test", nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 307 || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}

func TestStatAndOpenImplementBackendContract(t *testing.T) {
	body := "0123456789"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/test-bucket/public/a.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		if r.URL.Query().Get("ignore-range") != "" || r.Header.Get("X-Test-Ignore") != "" {
			r.Header.Del("Range")
		}
		http.ServeContent(w, r, "a.txt", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC), strings.NewReader(body))
	}))
	defer s.Close()
	c := testConfig()
	c.Prefix = "public/"
	c.Endpoint, _ = url.Parse(s.URL)
	o := New(c)
	st, err := o.Stat(context.Background(), "a.txt")
	if err != nil || st.Size != 10 || st.ETag != `"v1"` || st.Modified.IsZero() || st.Key != "a.txt" {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err = o.Stat(context.Background(), "missing"); err == nil {
		t.Fatal("missing object stat succeeded")
	}
	for _, tc := range []struct {
		offset, length int64
		want           string
	}{{0, -1, body}, {2, 3, "234"}, {7, -1, "789"}, {0, 0, ""}} {
		r, err := o.Open(context.Background(), "a.txt", tc.offset, tc.length)
		if err != nil {
			t.Fatal(tc, err)
		}
		b, _ := io.ReadAll(r)
		r.Close()
		if string(b) != tc.want {
			t.Fatal(tc, string(b))
		}
	}
	if _, err = o.Open(context.Background(), "a.txt", 50, -1); err == nil {
		t.Fatal("unsatisfiable range opened")
	}
}

// STORAGE_BASE_PATH is folded into S3_PREFIX for every origin request.
func TestBasePathOriginKeys(t *testing.T) {
	var direct []string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") == "2" {
			direct = append(direct, "list:"+r.URL.Query().Get("prefix"))
			io.WriteString(w, `<ListBucketResult><CommonPrefixes><Prefix>public/team/sub/</Prefix></CommonPrefixes><Contents><Key>public/team/a.txt</Key><Size>1</Size></Contents></ListBucketResult>`)
			return
		}
		direct = append(direct, r.Method+":"+r.URL.Path)
		io.WriteString(w, "x")
	}))
	defer origin.Close()
	c := testConfig()
	c.Endpoint, _ = url.Parse(origin.URL)
	c.Prefix = "public/team/"
	o := New(c)
	l, err := o.List(context.Background(), "", "")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Key != "sub/" || l.Entries[1].Key != "a.txt" {
		t.Fatalf("%+v %v", l, err)
	}
	if _, err = o.Stat(context.Background(), "a.txt"); err != nil {
		t.Fatal(err)
	}
	r, err := o.Object(context.Background(), "GET", "a.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if strings.Join(direct, ",") != "list:public/team/,HEAD:/test-bucket/public/team/a.txt,GET:/test-bucket/public/team/a.txt" {
		t.Fatal(direct)
	}
	c.AccessKey, c.SecretKey, c.PresignTTL = "TESTACCESS", "secret", time.Minute
	link, err := New(c).Presign("GET", "a.txt", true, time.Now())
	if u, _ := url.Parse(link); err != nil || u.Path != "/test-bucket/public/team/a.txt" {
		t.Fatal(link, err)
	}
}
