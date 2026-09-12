package server

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"mori/internal/s3"
)

func TestDeliveryModesIndependentAndNoClientOverride(t *testing.T) {
	for _, downloadMode := range []string{"proxy", "presigned"} {
		for _, previewMode := range []string{"proxy", "presigned"} {
			t.Run(downloadMode+"-"+previewMode, func(t *testing.T) {
				a := fixtureApp(t)
				a.cfg.AccessKey = "TESTACCESS"
				a.cfg.SecretKey = "NEVER-EXPOSE-SECRET"
				a.cfg.DownloadMode = downloadMode
				a.cfg.PreviewMode = previewMode
				a.s3 = s3.New(a.cfg)
				a.store = a.s3
				for _, download := range []bool{false, true} {
					mode := previewMode
					target := "/_mori/api/object?key=README.md&mode=presigned"
					if download {
						mode = downloadMode
						target += "&download=1"
					}
					w := call(a, "GET", target, nil)
					if w.Header().Get("X-Delivery-Mode") != mode {
						t.Fatal("wrong mode", w.Header())
					}
					if mode == "proxy" {
						if w.Code != 200 || w.Body.Len() != 40 {
							t.Fatal("proxy did not stream", w.Code)
						}
						continue
					}
					if w.Code != 307 || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("invalid redirect", w.Code, w.Header())
					}
					u, err := url.Parse(w.Header().Get("Location"))
					if err != nil {
						t.Fatal(err)
					}
					d := u.Query().Get("response-content-disposition")
					if download != strings.HasPrefix(d, "attachment;") {
						t.Fatal(d)
					}
					if u.Query().Get("response-content-type") != "text/plain; charset=utf-8" || strings.Contains(u.String(), a.cfg.SecretKey) {
						t.Fatal("unsafe override or leaked secret")
					}
				}
			})
		}
	}
}
func TestPresignOnlyGetObjectNotHeadOrListing(t *testing.T) {
	a := fixtureApp(t)
	a.cfg.AccessKey, a.cfg.SecretKey = "TESTACCESS", "test-secret"
	a.cfg.DownloadMode, a.cfg.PreviewMode = "presigned", "presigned"
	a.s3 = s3.New(a.cfg)
	a.store = a.s3
	for _, method := range []string{"HEAD", "PUT", "POST", "DELETE", "OPTIONS"} {
		if _, err := a.s3.Presign(method, "README.md", false, time.Now()); err == nil {
			t.Fatal("non-GET object presigned", method)
		}
	}
	for _, suffix := range []string{"", "&download=1"} {
		w := call(a, "HEAD", "/_mori/api/object?key=README.md"+suffix, nil)
		if w.Code != 200 || w.Header().Get("Location") != "" || w.Header().Get("X-Delivery-Mode") != "proxy" || w.Header().Get("Content-Length") != "40" || w.Body.Len() != 0 {
			t.Fatal("HEAD was not server-side", w.Code, w.Header(), w.Body.String())
		}
		if w := call(a, "GET", "/_mori/api/object?key=README.md"+suffix, nil); w.Code != 307 {
			t.Fatal(w.Code)
		}
	}
	w := call(a, "GET", "/_mori/api/list", nil)
	if w.Code != 200 || w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "README.md") || strings.Contains(w.Body.String(), "X-Amz-") {
		t.Fatal("listing was presigned", w.Code, w.Body.String())
	}
}
