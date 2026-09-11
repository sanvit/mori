// Package media decides how an object key is typed and presented to browsers.
// Every rule is extension-based: user HTML/SVG is never rendered as markup.
package media

import (
	"mime"
	"path"
	"strings"
)

// FileType maps an object key to a MIME type. Common media types are listed
// explicitly so results stay deterministic in minimal Docker images without
// /etc/mime.types.
func FileType(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".bmp":
		return "image/bmp"
	case ".ico":
		return "image/x-icon"
	case ".pdf":
		return "application/pdf"
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".ogv":
		return "video/ogg"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".wav":
		return "audio/wav"
	case ".ogg", ".oga", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	}
	t := mime.TypeByExtension(strings.ToLower(path.Ext(key)))
	if t == "" {
		return "application/octet-stream"
	}
	return strings.Split(t, ";")[0]
}

// PreviewKind returns image, video, audio, pdf, text, or unsupported.
func PreviewKind(key string) string {
	switch strings.ToLower(path.Ext(key)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".ico":
		return "image"
	case ".mp4", ".m4v", ".webm", ".ogv", ".mov":
		return "video"
	case ".mp3", ".m4a", ".aac", ".wav", ".ogg", ".oga", ".opus", ".flac":
		return "audio"
	case ".pdf":
		return "pdf"
	case ".txt", ".md", ".markdown", ".rst", ".log", ".csv", ".tsv", ".json", ".jsonl", ".ndjson", ".yaml", ".yml", ".toml", ".ini", ".conf", ".cfg", ".env", ".sh", ".bash", ".zsh", ".bat", ".ps1", ".go", ".py", ".js", ".mjs", ".cjs", ".jsx", ".ts", ".tsx", ".java", ".c", ".h", ".cc", ".cpp", ".cs", ".rs", ".rb", ".php", ".html", ".htm", ".css", ".scss", ".xml", ".svg", ".sql", ".vue", ".svelte", ".gitignore", ".vtt", ".srt":
		return "text"
	}
	switch strings.ToLower(path.Base(key)) {
	case "readme", "license", "dockerfile", "makefile", ".gitignore", ".env":
		return "text"
	}
	return "unsupported"
}

// Presentation returns the Content-Type and Content-Disposition for an object.
// The same safe rules apply to proxy responses and signed S3 URLs.
func Presentation(key string, download bool) (contentType, disposition string) {
	contentType, disposition = FileType(key), "inline"
	if PreviewKind(key) == "text" || strings.HasPrefix(contentType, "text/") || contentType == "application/json" || contentType == "image/svg+xml" {
		contentType = "text/plain; charset=utf-8"
	} else if !safeMediaType(contentType) {
		contentType, disposition = "application/octet-stream", "attachment"
	}
	if download {
		disposition = "attachment"
	}
	return contentType, mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(key)})
}
func safeMediaType(t string) bool {
	switch t {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "application/pdf", "video/mp4", "video/webm", "audio/mpeg", "audio/ogg", "audio/wav", "audio/x-wav", "audio/mp4", "video/ogg", "video/quicktime", "audio/aac", "audio/flac", "image/x-icon", "image/vnd.microsoft.icon":
		return true
	}
	return false
}
