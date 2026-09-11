package media

import (
	"strings"
	"testing"
)

func TestPreviewTypesAndSafePresentation(t *testing.T) {
	tests := []struct{ name, kind, mime string }{
		{"photo.JPG", "image", "image/jpeg"}, {"cover.avif", "image", "image/avif"}, {"icon.ico", "image", "image/x-icon"},
		{"sample.mp4", "video", "video/mp4"}, {"sample.m4v", "video", "video/mp4"}, {"sample.webm", "video", "video/webm"}, {"sample.mov", "video", "video/quicktime"},
		{"song.mp3", "audio", "audio/mpeg"}, {"song.m4a", "audio", "audio/mp4"}, {"song.opus", "audio", "audio/ogg"}, {"song.flac", "audio", "audio/flac"}, {"song.wav", "audio", "audio/wav"},
		{"guide.PDF", "pdf", "application/pdf"}, {"evil.html", "text", "text/plain; charset=utf-8"}, {"evil.svg", "text", "text/plain; charset=utf-8"},
		{"README", "text", "text/plain; charset=utf-8"}, {"file.ts", "text", "text/plain; charset=utf-8"}, {"source.py", "text", "text/plain; charset=utf-8"}, {"data.json", "text", "text/plain; charset=utf-8"},
		{"archive.zip", "unsupported", "application/octet-stream"}, {"document.docx", "unsupported", "application/octet-stream"}, {"photo.heic", "unsupported", "application/octet-stream"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreviewKind(tc.name); got != tc.kind {
				t.Fatalf("kind=%s, expected %s", got, tc.kind)
			}
			typ, disposition := Presentation(tc.name, false)
			if typ != tc.mime {
				t.Errorf("type=%s, expected %s", typ, tc.mime)
			}
			if tc.kind == "unsupported" && !strings.HasPrefix(disposition, "attachment") {
				t.Error("unsafe inline content")
			}
			_, download := Presentation(tc.name, true)
			if !strings.HasPrefix(download, "attachment") {
				t.Error("download must stay attachment")
			}
		})
	}
}
