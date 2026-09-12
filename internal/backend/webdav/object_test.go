package webdav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"mori/internal/backend"
	"mori/internal/config"
)

func TestOptionalWebDAVETagConsistency(t *testing.T) {
	for _, tag := range []string{"", `W/"weak"`, `"strong"`, "invalid"} {
		for _, headStatus := range []int{200, 405} {
			t.Run(fmt.Sprint(tag, headStatus), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == "HEAD" {
						w.Header().Set("Cache-Control", "max-age=60")
						if tag != "" {
							w.Header().Set("ETag", tag)
						}
						w.WriteHeader(headStatus)
						return
					}
					if r.Method != "PROPFIND" {
						t.Error(r.Method)
						w.WriteHeader(405)
						return
					}
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/한글.txt</d:href><d:propstat><d:prop><d:getcontentlength>4</d:getcontentlength><d:getlastmodified>Sat, 12 Sep 2026 00:00:00 GMT</d:getlastmodified><d:getetag>%s</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`, tag)
				}))
				defer srv.Close()
				u, _ := url.Parse(srv.URL + "/dav/")
				c := New(config.WebDAVConfig{URL: u})
				st, err := c.Stat(context.Background(), "한글.txt")
				if err != nil {
					t.Fatal(err)
				}
				if backend.ValidETag(tag) {
					if st.ETag != tag {
						t.Fatal("native tag replaced", st.ETag)
					}
				} else if !backend.SyntheticETag(st.ETag) {
					t.Fatal(st.ETag)
				}
				list, err := c.List(context.Background(), "", "")
				if err != nil || len(list.Entries) != 1 || list.Entries[0].ETag != st.ETag {
					t.Fatal(list, err)
				}
				head, err := c.Object(context.Background(), "HEAD", "한글.txt", http.Header{})
				if err != nil {
					t.Fatal(err)
				}
				head.Body.Close()
				if head.Header.Get("ETag") != st.ETag || head.Header.Get("Cache-Control") != "max-age=60" {
					t.Fatal(head.Header)
				}
				fresh, err := c.Object(context.Background(), "HEAD", "한글.txt", http.Header{"If-None-Match": {st.ETag}})
				if err != nil {
					t.Fatal(err)
				}
				fresh.Body.Close()
				if fresh.StatusCode != 304 {
					t.Fatal(fresh.StatusCode)
				}
				native, err := c.NativeObjectValidator(context.Background(), "한글.txt", st.ETag)
				if err != nil || native != backend.StrongETag(st.ETag) {
					t.Fatal(native, err)
				}
			})
		}
	}
}

func TestMissingPROPFINDTimestampCannotReuseHEADTimestamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.Header().Set("Last-Modified", "Sat, 12 Sep 2026 00:00:00 GMT")
			w.Header().Set("Content-Type", "application/custom")
			w.Header().Set("Cache-Control", "public, max-age=60")
			return
		}
		w.WriteHeader(207)
		fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/dav/a</d:href><d:propstat><d:prop><d:getcontentlength>4</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL + "/dav/")
	c := New(config.WebDAVConfig{URL: u})
	st, err := c.Stat(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Object(context.Background(), "HEAD", "a", http.Header{"If-None-Match": {st.ETag}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Last-Modified") != "" || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/custom" {
		t.Fatal(resp.StatusCode, resp.Header)
	}
}
