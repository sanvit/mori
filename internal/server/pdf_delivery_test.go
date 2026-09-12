package server

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPDFInlineDeliveryHeaders(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Origin metadata must not turn a new-tab preview into an attachment.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=wrong.bin")
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes 0-4/9")
			w.WriteHeader(206)
		}
		w.Write([]byte("%PDF-"))
	}))
	defer origin.Close()
	for _, mode := range []string{"proxy", "presigned"} {
		c := testConfig()
		c.Endpoint, _ = url.Parse(origin.URL)
		c.PreviewMode, c.DownloadMode = mode, mode
		c.AccessKey, c.SecretKey = "test", "test"
		a := New(c)
		for _, name := range []string{"guide.pdf", "한글 안내.PDF"} {
			for _, method := range []string{"GET", "HEAD"} {
				for _, download := range []bool{false, true} {
					for _, partial := range []bool{false, true} {
						q := url.Values{"key": {name}}
						if download {
							q.Set("download", "1")
						}
						headers := http.Header{}
						if partial {
							headers.Set("Range", "bytes=0-4")
						}
						w := call(a, method, "/_mori/api/object?"+q.Encode(), headers)
						typ, disposition := w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition")
						if mode == "presigned" && method == "GET" {
							if w.Code != 307 {
								t.Fatal(w.Code)
							}
							location, err := url.Parse(w.Header().Get("Location"))
							if err != nil {
								t.Fatal(err)
							}
							typ, disposition = location.Query().Get("response-content-type"), location.Query().Get("response-content-disposition")
						} else {
							wantStatus := 200
							if partial {
								wantStatus = 206
							}
							if w.Code != wantStatus {
								t.Fatal(w.Code, w.Body.String())
							}
							if !download && strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
								t.Fatal("native PDF must not be sandboxed", w.Header())
							}
						}
						kind, params, err := mime.ParseMediaType(disposition)
						want := "inline"
						if download {
							want = "attachment"
						}
						if err != nil || typ != "application/pdf" || kind != want || params["filename"] != name {
							t.Fatal(typ, disposition, err)
						}
					}
				}
			}
		}
	}
}

func TestNativePDFPolicyKeepsOtherTypesIsolated(t *testing.T) {
	a := New(testConfig())
	for _, key := range []string{"x.pdf", "x.jpg", "x.mp4", "x.mp3", "x.html", "x.svg", "x.txt", "x.zip"} {
		w := httptest.NewRecorder()
		a.objectPresentation(w, key, false)
		native := key == "x.pdf"
		if strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") == native {
			t.Fatal(key, w.Header())
		}
	}
}
