package server

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mori-s3/internal/config"
	"mori-s3/internal/media"
)

// Resolve a GetObject source without performing HEAD or reading the file body.
// A same-origin JSON endpoint avoids auth/cross-origin redirect trouble in PDF.js.
// Signed URLs stay in memory in the UI; they are not put into location/history/storage.
func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(w, http.StatusMethodNotAllowed, "method_not_allowed", "미리보기 주소는 GET으로 요청합니다.")
		return
	}
	key := r.URL.Query().Get("key")
	if config.ValidateKey(key, false) != nil || strings.HasSuffix(key, "/") || len(a.cfg.Prefix+key) > 1024 {
		fail(w, 400, "invalid_key", "올바르지 않은 파일 경로입니다.")
		return
	}
	kind := media.PreviewKind(key)
	result := map[string]any{"kind": kind, "mode": a.cfg.PreviewMode, "name": path.Base(key), "textLimit": 1 << 20}
	if kind == "unsupported" {
		jsonOut(w, result)
		return
	}
	source := "/api/object?" + url.Values{"key": {key}}.Encode()
	if a.cfg.PreviewMode == "presigned" && a.s3 != nil {
		now := time.Now()
		var err error
		source, err = a.s3.Presign(http.MethodGet, key, false, now)
		if err != nil {
			a.upstreamFail(w, err)
			return
		}
		result["expiresAt"] = now.Add(a.cfg.PresignTTL).UTC().Format(time.RFC3339)
	}
	result["url"] = source
	result["contentType"], _ = media.Presentation(key, false)
	jsonOut(w, result)
}

// Allow only the configured public S3 authority, never arbitrary third-party hosts.
// Inline styles are needed for Media Chrome shadow styles and PDF canvas/font layout;
// inline scripts and JS eval remain blocked. wasm-unsafe-eval is WASM-only.
func (a *App) contentSecurityPolicy() string {
	remote := ""
	if a.cfg.PreviewMode == "presigned" && a.s3 != nil {
		endpoint := a.cfg.Endpoint
		if a.cfg.PresignEndpoint != nil {
			endpoint = a.cfg.PresignEndpoint
		}
		if endpoint != nil {
			host := endpoint.Host
			if !a.cfg.PathStyle {
				host = a.cfg.Bucket + "." + host
			}
			remote = " " + endpoint.Scheme + "://" + host
		}
	}
	return "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:" + remote + "; media-src 'self' blob:" + remote + "; " +
		"connect-src 'self'" + remote + "; font-src 'self' data: blob:; worker-src 'self' blob:; " +
		"object-src 'none'; frame-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}
