package server

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"mori/internal/backend"
	"mori/internal/s3"
)

const archivePlanTTL = 2 * time.Minute
const archivePlanLimit = 128
const archivePlanEntryLimit = 20000 // Across all pending plans, not per client.

// archiveItem is an S3 object plus its path inside the ZIP.
type archiveItem struct {
	backend.Object
	Name string
}
type archivePlan struct {
	Items              []archiveItem
	Filename           string
	Size               int64
	Files, Directories int
	Expires            time.Time
}
type archiveStore struct {
	mu    sync.Mutex
	plans map[string]archivePlan
}

func newArchiveStore() *archiveStore { return &archiveStore{plans: make(map[string]archivePlan)} }
func (s *archiveStore) prune(now time.Time) {
	for token, plan := range s.plans {
		if !now.Before(plan.Expires) {
			delete(s.plans, token)
		}
	}
}
func (s *archiveStore) put(plan archivePlan, now time.Time) (string, bool, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", false, err
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	entries := len(plan.Items)
	for _, pending := range s.plans {
		entries += len(pending.Items)
	}
	if len(s.plans) >= archivePlanLimit || entries > archivePlanEntryLimit {
		return "", false, nil
	}
	plan.Expires = now.Add(archivePlanTTL)
	s.plans[token] = plan
	return token, true, nil
}
func (s *archiveStore) take(token string, now time.Time) (archivePlan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	plan, ok := s.plans[token]
	if ok {
		delete(s.plans, token)
	}
	return plan, ok
}

// A JSON POST prepares only small metadata, returning a one-use download URL.
// Its GET is a native browser download: no fetch/Blob of the complete archive.
func (a *App) archive(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.Header.Get("Range") != "" {
		w.Header().Set("Accept-Ranges", "none")
		fail(w, 416, "zip_not_resumable", "ZIP은 이어받기를 지원하지 않습니다. 파일을 다시 선택해 다운로드하세요.")
		return
	}
	select {
	case a.zipSlots <- struct{}{}:
		defer func() { <-a.zipSlots }()
	default:
		w.Header().Set("Retry-After", "5")
		fail(w, 429, "zip_busy", "진행 중인 ZIP 요청이 많습니다. 잠시 후 다시 시도해 주세요.")
		return
	}
	if r.Method == http.MethodPost {
		a.prepareArchive(w, r)
		return
	}
	a.streamArchive(w, r)
}
func sameOriginArchiveRequest(r *http.Request) bool {
	if r.Header.Get("X-Mori-Request") != "1" {
		return false
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return false
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Host, r.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}
func (a *App) prepareArchive(w http.ResponseWriter, r *http.Request) {
	if !sameOriginArchiveRequest(r) {
		fail(w, 403, "invalid_archive_request", "같은 사이트의 파일 목록에서 ZIP 다운로드를 요청해 주세요.")
		return
	}
	// Bound slow JSON request bodies as well as their size. The stream itself
	// has no fixed write deadline; downloads can be large.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer rc.SetReadDeadline(time.Time{})
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input struct {
		Prefix string   `json:"prefix"`
		Keys   []string `json:"keys"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid_archive_request", "선택 파일 목록이 올바르지 않거나 너무 큽니다.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	plan, err := a.buildArchivePlan(ctx, input.Prefix, input.Keys)
	if err != nil {
		var ae *archiveError
		if errors.As(err, &ae) {
			fail(w, ae.Status, ae.Code, ae.Message)
		} else {
			a.upstreamFail(w, err)
		}
		return
	}
	token, ok, err := a.archives.put(plan, time.Now())
	if err != nil {
		fail(w, 500, "zip_prepare_failed", "ZIP 다운로드를 준비하지 못했습니다.")
		return
	}
	if !ok {
		w.Header().Set("Retry-After", "5")
		fail(w, 429, "zip_queue_full", "대기 중인 ZIP 요청이 많습니다. 잠시 후 다시 시도해 주세요.")
		return
	}
	jsonOut(w, map[string]any{"url": "/api/archive?token=" + token, "filename": plan.Filename, "files": plan.Files, "folders": plan.Directories, "size": plan.Size, "expiresIn": int(archivePlanTTL / time.Second)})
}

func (a *App) streamArchive(w http.ResponseWriter, r *http.Request) {
	plan, ok := a.archives.take(r.URL.Query().Get("token"), time.Now())
	if !ok {
		fail(w, 410, "zip_expired", "ZIP 링크가 만료되었거나 이미 사용되었습니다. 파일을 다시 선택해 다운로드하세요.")
		return
	}
	var zw *zip.Writer
	startZIP := func() {
		if zw != nil {
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": plan.Filename}))
		w.Header().Set("Accept-Ranges", "none")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("X-Delivery-Mode", "proxy")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		zw = zip.NewWriter(w)
	}
	// Never finalize a ZIP after a failure: closing it would make a partial
	// archive look successful. Before headers, return JSON; afterwards abort.
	failStream := func(err error) {
		if zw != nil {
			panic(http.ErrAbortHandler)
		}
		a.upstreamFail(w, err)
	}
	for _, item := range plan.Items {
		if r.Context().Err() != nil {
			failStream(r.Context().Err())
			return
		}
		if item.Directory {
			startZIP()
			header := &zip.FileHeader{Name: item.Name, Method: zip.Store}
			header.SetMode(os.ModeDir | 0755)
			header.Modified = item.Modified
			if _, err := zw.CreateHeader(header); err != nil {
				failStream(err)
				return
			}
			continue
		}
		body, err := a.openArchiveItem(r.Context(), item)
		if err != nil {
			failStream(err)
			return
		}
		startZIP()
		err = func() error {
			defer body.Close()
			header := &zip.FileHeader{Name: item.Name, Method: zip.Store, UncompressedSize64: uint64(item.Size)}
			header.SetMode(0644)
			if !item.Modified.IsZero() {
				header.Modified = item.Modified
			}
			entry, err := zw.CreateHeader(header)
			if err != nil {
				return err
			}
			if err = zw.Flush(); err != nil {
				return err
			}
			// Send headers before waiting for file bytes. Network intermediaries
			// must also have response buffering disabled for end-to-end streaming.
			if err = http.NewResponseController(w).Flush(); err != nil && err != http.ErrNotSupported {
				return err
			}
			if _, err = io.CopyN(entry, body, item.Size); err != nil {
				return err
			}
			var extra [1]byte
			n, err := io.ReadFull(body, extra[:])
			if n != 0 || err != io.EOF {
				return fmt.Errorf("object length changed during ZIP")
			}
			return nil
		}()
		if err != nil {
			failStream(err)
			return
		}
	}
	if zw == nil {
		fail(w, 400, "empty_archive", "선택한 파일이 없습니다.")
		return
	}
	if err := zw.Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
}

// openArchiveItem returns the body of one planned file, failing with 412 if
// the object no longer matches the plan. S3 enforces this atomically with
// If-Match; other backends re-stat before opening and rely on the length
// check while copying.
func (a *App) openArchiveItem(ctx context.Context, item archiveItem) (io.ReadCloser, error) {
	if a.s3 != nil {
		resp, err := a.s3.Object(ctx, http.MethodGet, item.Key, http.Header{"If-Match": {item.ETag}})
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			err = s3.ReadError(resp)
			resp.Body.Close()
			return nil, err
		}
		if resp.Header.Get("ETag") != item.ETag || (resp.ContentLength >= 0 && resp.ContentLength != item.Size) {
			resp.Body.Close()
			return nil, &backend.UpstreamError{Status: 412, Code: "ObjectChanged"}
		}
		return resp.Body, nil
	}
	st, err := a.store.Stat(ctx, item.Key)
	if err != nil {
		return nil, err
	}
	if st.ETag != item.ETag || st.Size != item.Size {
		return nil, &backend.UpstreamError{Status: 412, Code: "ObjectChanged"}
	}
	return a.store.Open(ctx, item.Key, 0, item.Size)
}
