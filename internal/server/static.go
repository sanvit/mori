package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"mori/web"
)

func (a *App) static(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/" {
		p = "/index.html"
	}
	// Embedded, exact paths only: never list directories or clean a traversal into a file.
	if path.Clean(p) != p || strings.Contains(p, "\\") || strings.HasSuffix(p, "/") {
		http.NotFound(w, r)
		return
	}
	b, err := web.Files.ReadFile(strings.TrimPrefix(p, "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(p))
	typ := mime.TypeByExtension(ext)
	switch ext {
	case ".js", ".mjs":
		typ = "text/javascript; charset=utf-8"
	case ".wasm":
		typ = "application/wasm"
	case ".bcmap":
		typ = "application/octet-stream"
	}
	if typ == "" {
		typ = "application/octet-stream"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(b)))
	if strings.HasPrefix(p, "/vendor/") && p != "/vendor/manifest.json" {
		// Authenticated, versioned assets may be kept in this user's browser cache.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, path.Base(p), time.Time{}, bytes.NewReader(b))
}
