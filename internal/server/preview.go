package server

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"mori/internal/config"
	"mori/internal/media"
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
	renderHTML := a.renderHTML(key, false)
	result := map[string]any{"renderHTML": renderHTML, "htmlScripts": renderHTML && a.cfg.HTMLPreviewScripts, "kind": kind, "mode": a.cfg.PreviewMode, "name": path.Base(key), "textLimit": 1 << 20}
	if kind == "unsupported" {
		jsonOut(w, result)
		return
	}
	source := "/api/object?" + url.Values{"key": {key}}.Encode()
	if a.cfg.PreviewMode == "presigned" && a.s3 != nil && !renderHTML {
		now := time.Now()
		var err error
		source, err = a.s3.Presign(http.MethodGet, key, false, now)
		if err != nil {
			a.upstreamFail(w, err)
			return
		}
		result["expiresAt"] = now.Add(a.cfg.PresignTTL).UTC().Format(time.RFC3339)
	}
	if renderHTML {
		result["mode"] = "proxy"
	}
	result["url"] = source
	result["contentType"], _ = media.Presentation(key, false)
	if renderHTML {
		result["contentType"] = "text/html; charset=utf-8"
	}
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
	frameSource := "'none'"
	if a.cfg.HTMLPreviewEnabled {
		frameSource = "'self'"
	}
	return "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob:" + remote + "; media-src 'self' blob:" + remote + "; " +
		"connect-src 'self'" + remote + "; font-src 'self' data: blob:; worker-src 'self' blob:; " +
		"object-src 'none'; frame-src " + frameSource + "; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}

func (a *App) renderHTML(key string, download bool) bool {
	return a.cfg.HTMLPreviewEnabled && !download && (strings.EqualFold(path.Ext(key), ".html") || strings.EqualFold(path.Ext(key), ".htm"))
}

func (a *App) objectPresentation(w http.ResponseWriter, key string, download bool) {
	contentType, disposition := media.Presentation(key, download)
	policy := "default-src 'none'; sandbox"
	// WebKit's native PDF viewer can fail under CSP sandbox. Presentation
	// explicitly allows application/pdf; HTML/SVG/text remain sandboxed.
	if !download && contentType == "application/pdf" {
		policy = "default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	}
	if a.renderHTML(key, download) {
		contentType = "text/html; charset=utf-8"
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		resources, scripts, sandbox := "", "'none'", "sandbox"
		if a.cfg.HTMLPreviewExternalResources {
			resources = " https: http:"
		}
		if a.cfg.HTMLPreviewScripts {
			scripts = "'unsafe-inline' 'unsafe-eval'" + resources
			sandbox += " allow-scripts"
		}
		policy = "default-src 'none'; script-src " + scripts + "; style-src 'unsafe-inline'" + resources + "; img-src data: blob:" + resources + "; font-src data:" + resources + "; media-src data: blob:" + resources + "; connect-src "
		if resources == "" {
			policy += "'none'"
		} else {
			policy += strings.TrimSpace(resources)
		}
		policy += "; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; " + sandbox
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Security-Policy", policy)
}
