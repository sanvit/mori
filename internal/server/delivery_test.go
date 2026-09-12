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

// A browser-mode file path follows the same delivery choice as the object API,
// so a presigned download is not silently proxied just because the visitor used
// the plain URL. HEAD, HTML previews and the site modes stay server-side.
func TestFilePathFollowsDeliveryMode(t *testing.T) {
	open := func(t *testing.T, serve string, previewMode string) *App {
		t.Helper()
		c := fixtureApp(t).cfg
		c.ServeMode, c.CacheMode = serve, "off"
		c.AccessKey, c.SecretKey = "TESTACCESS", "test-secret"
		c.DownloadMode, c.PreviewMode = "presigned", previewMode
		a, err := Open(c, s3.New(c))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(a.Shutdown)
		return a
	}

	a := open(t, "browser", "proxy")
	w := call(a, "GET", "/README.md?download=1", nil)
	if w.Code != 307 || w.Body.Len() != 0 || w.Header().Get("X-Delivery-Mode") != "presigned" {
		t.Fatal("download was not presigned", w.Code, w.Header(), w.Body.String())
	}
	if link := w.Header().Get("Location"); !strings.Contains(link, "X-Amz-Signature=") || !strings.Contains(link, "README.md") {
		t.Fatal("redirect is not a signed object URL", link)
	}
	// PreviewMode stays proxy, so an inline read of the same file does not.
	if w := call(a, "GET", "/README.md", nil); w.Code != 200 || w.Header().Get("Location") != "" || w.Header().Get("X-Delivery-Mode") != "proxy" {
		t.Fatal("inline read was presigned under PreviewMode=proxy", w.Code, w.Header())
	}
	if w := call(a, "HEAD", "/README.md?download=1", nil); w.Code != 200 || w.Header().Get("Location") != "" {
		t.Fatal("HEAD was presigned", w.Code, w.Header())
	}

	// An HTML preview needs mori's sandbox headers, so it is never handed off.
	html := open(t, "browser", "presigned")
	html.cfg.HTMLPreviewEnabled = true
	if w := call(html, "GET", "/evil.html", nil); w.Header().Get("X-Delivery-Mode") != "proxy" || w.Code == 307 {
		t.Fatal("HTML preview was presigned", w.Code, w.Header().Get("X-Delivery-Mode"))
	}

	// spa and direct publish the storage itself; their paths are always served
	// through mori so index, SPA and error pages keep working.
	for _, mode := range []string{"spa", "direct"} {
		site := open(t, mode, "presigned")
		if w := call(site, "GET", "/README.md?download=1", nil); w.Code == 307 {
			t.Fatal("site path was presigned in mode", mode, w.Header().Get("Location"))
		}
	}
}
