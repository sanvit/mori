package media

import (
	"mime"
	"strings"
	"testing"
)

func TestDispositionUnicodeAndFallback(t *testing.T) {
	for _, tc := range []struct{ raw, key, kind, name string }{
		{"", "docs/한글 +%#😀.txt", "inline", "한글 +%#😀.txt"},
		{"attachment", "docs/한글.pdf", "attachment", "한글.pdf"},
		{`attachment; filename="원본 안내.pdf"`, "fallback.pdf", "attachment", "원본 안내.pdf"},
		{`inline; filename*=utf-8''%ED%95%9C%EA%B8%80.txt`, "fallback.txt", "inline", "한글.txt"},
		{"attachment; filename=\"bad\r\nX-Evil: 1\"", "한글.txt", "attachment", "한글.txt"},
		{`attachment; filename="../../secret.txt"`, "safe.txt", "attachment", "secret.txt"},
	} {
		t.Run(tc.key+tc.raw, func(t *testing.T) {
			got := Disposition(tc.raw, tc.key)
			kind, params, err := mime.ParseMediaType(got)
			if err != nil || kind != tc.kind || params["filename"] != tc.name {
				t.Fatal(got, err)
			}
			for _, r := range got {
				if r > 127 || r < 32 {
					t.Fatal("raw Unicode/control in header", got)
				}
			}
			if strings.Contains(tc.name, "한글") && !strings.Contains(got, "filename*=utf-8''") {
				t.Fatal(got)
			}
		})
	}
}
