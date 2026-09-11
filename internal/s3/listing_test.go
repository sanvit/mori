package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
)

// Test-only S3 contract model, not a real bucket or a runtime sample mode.
func TestManyDescendantsCountAsOneFolder(t *testing.T) {
	keys := []string{"public/docs/a-first.txt", "public/docs/z-last.txt"}
	for i := 0; i < 10000; i++ {
		keys = append(keys, fmt.Sprintf("public/docs/sub/file-%05d.txt", i))
	}
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if q.Get("list-type") != "2" || q.Get("delimiter") != "/" || q.Get("prefix") != "public/docs/" || q.Get("max-keys") != "1000" {
			t.Error("not a one-level metadata listing", q)
		}
		groups := map[string]bool{}
		for _, key := range keys {
			rest := strings.TrimPrefix(key, "public/docs/")
			if i := strings.Index(rest, "/"); i >= 0 {
				groups["public/docs/"+rest[:i+1]] = true
			} else {
				groups[key] = false
			}
		}
		ordered := []string{}
		for key := range groups {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		io.WriteString(w, "<ListBucketResult>")
		for _, key := range ordered[:min(1000, len(ordered))] {
			if groups[key] {
				fmt.Fprintf(w, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", key)
			} else {
				fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>1</Size></Contents>", key)
			}
		}
		io.WriteString(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")
	}))
	defer s.Close()
	c := testConfig()
	c.Prefix = "public/"
	c.Endpoint, _ = url.Parse(s.URL)
	l, err := New(c).List(context.Background(), "docs/", "")
	if err != nil || len(l.Entries) != 3 || l.Cursor != "" || calls.Load() != 1 {
		t.Fatal(l, err, calls.Load())
	}
	for _, e := range l.Entries {
		if !e.Folder && strings.Contains(e.Name, "/") {
			t.Fatal("recursive file leaked", e)
		}
	}
}
func TestCurrentLevel1001ItemsContinues(t *testing.T) {
	for _, folders := range []bool{false, true} {
		t.Run(fmt.Sprint(folders), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				q := r.URL.Query()
				if q.Get("delimiter") != "/" || q.Get("prefix") != "docs/" {
					t.Error(q)
				}
				start, end := 0, 1000
				if q.Get("continuation-token") == "last" {
					start, end = 1000, 1001
				}
				io.WriteString(w, "<ListBucketResult>")
				for i := start; i < end; i++ {
					if folders {
						fmt.Fprintf(w, "<CommonPrefixes><Prefix>docs/f-%04d/</Prefix></CommonPrefixes>", i)
					} else {
						fmt.Fprintf(w, "<Contents><Key>docs/f-%04d.txt</Key><Size>1</Size></Contents>", i)
					}
				}
				if end == 1000 {
					io.WriteString(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>last</NextContinuationToken>")
				}
				io.WriteString(w, "</ListBucketResult>")
			}))
			defer s.Close()
			c := testConfig()
			c.Endpoint, _ = url.Parse(s.URL)
			o := New(c)
			first, err := o.List(context.Background(), "docs/", "")
			if err != nil || len(first.Entries) != 1000 || first.Cursor == "" || calls.Load() != 1 {
				t.Fatal(len(first.Entries), err, calls.Load())
			}
			second, err := o.List(context.Background(), "docs/", first.Cursor)
			if err != nil || len(second.Entries) != 1 || second.Cursor != "" || calls.Load() != 2 {
				t.Fatal(second, err, calls.Load())
			}
		})
	}
}
