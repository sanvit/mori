package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"mori/internal/backend"
	"mori/internal/config"
	"mori/internal/media"
	"mori/web"
)

// The listing is rendered here and nowhere else. The browser UI attaches
// behaviour to these rows and asks for the next page as HTML rather than
// building rows of its own, so a client without JavaScript -- a terminal, a
// crawler, an agent -- sees the same complete listing a person does.
var listingTemplate = template.Must(template.ParseFS(web.Files, "index.html", "rows.html"))

type crumb struct {
	Name, Href string
	Current    bool
}

type listingRow struct {
	Key, Name, Href, DownloadHref, Icon string
	LinkTitle, MobileMeta               string
	DateText, SizeText, ModifiedTitle   string
	Modified                            string
	Size                                int64
	Folder, Preview                     bool
}

type listingPage struct {
	Title, DocumentTitle, Prefix string
	Crumbs                       []crumb
	Parent, RefreshHref          string
	Rows                         []listingRow
	NextHref, Status, Error      string
	EmptyMessage                 string
	DownloadTitle                string
	Sort                         string
	Direction                    int
	ZipEnabled                   bool
	Columns                      int
	Config                       string
}

func (p listingPage) IsSort(field string, direction int) bool {
	return p.Sort == field && p.Direction == direction
}

// Sorted and SortMark keep the header's assistive-technology state and its
// arrow in step with the order the rows were actually rendered in.
func (p listingPage) Sorted(field string) string {
	if p.Sort != field {
		return "none"
	}
	if p.Direction == 1 {
		return "ascending"
	}
	return "descending"
}

func (p listingPage) SortMark(field string) string {
	if p.Sort != field {
		return ""
	}
	if p.Direction == 1 {
		return "↑"
	}
	return "↓"
}

// SortHref makes each column header a working link, so ordering does not
// require JavaScript. Clicking the active column reverses it.
func (p listingPage) SortHref(field string) string {
	direction := 1
	if p.Sort == field && p.Direction == 1 {
		direction = -1
	}
	q := url.Values{"sort": {field}}
	if direction == -1 {
		q.Set("dir", "desc")
	}
	return "?" + q.Encode()
}

func folderHref(prefix string) string {
	if prefix == "" {
		return "/"
	}
	segments := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
	for i, segment := range segments {
		segments[i] = encodeSegment(segment)
	}
	return "/" + strings.Join(segments, "/") + "/"
}

func humanSize(size int64) string {
	if size < 0 {
		return "—"
	}
	if size < 1024 {
		return strconv.FormatInt(size, 10) + " B"
	}
	power := int(math.Min(4, math.Floor(math.Log(float64(size))/math.Log(1024))))
	unit := []string{"B", "KiB", "MiB", "GiB", "TiB"}[power]
	return strconv.FormatFloat(float64(size)/math.Pow(1024, float64(power)), 'f', 1, 64) + " " + unit
}

// The server has no way to know the reader's time zone, so it prints UTC and
// the browser rewrites the cell from data-modified once it loads.
func humanTime(value string) (string, string) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "—", ""
	}
	return t.UTC().Format("2006-01-02 15:04"), t.UTC().Format(time.RFC1123)
}

func iconFor(entry backend.Entry) string {
	if entry.Folder {
		return "folder"
	}
	switch media.PreviewKind(entry.Name) {
	case "video", "audio", "pdf":
		return media.PreviewKind(entry.Name)
	case "image":
		return "image"
	}
	switch strings.ToLower(strings.TrimPrefix(pathExt(entry.Name), ".")) {
	case "zip", "gz", "7z", "rar", "tar", "xz":
		return "archive"
	case "json", "js", "ts", "css", "html", "go", "py", "yaml", "yml", "xml", "sh":
		return "code"
	}
	return "file"
}

func pathExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

func sortEntries(entries []backend.Entry, field string, direction int) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Folder != b.Folder {
			return a.Folder
		}
		cmp := 0
		switch field {
		case "size":
			cmp = compareInt64(a.Size, b.Size)
		case "modified":
			cmp = strings.Compare(a.Modified, b.Modified)
		}
		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		return cmp*direction < 0
	})
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// serveListing renders one folder page. prefix is "" or ends with "/".
func (a *App) serveListing(w http.ResponseWriter, r *http.Request, prefix string) {
	query := r.URL.Query()
	cursor := query.Get("cursor")
	if config.ValidateKey(prefix, true) != nil || len(a.cfg.Prefix+prefix) > 1024 || len(cursor) > 8192 {
		fail(w, 400, "invalid_prefix", "올바르지 않은 폴더 경로입니다.")
		return
	}
	field, direction := "name", 1
	switch query.Get("sort") {
	case "size":
		field = "size"
	case "modified":
		field = "modified"
	}
	if query.Get("dir") == "desc" {
		direction = -1
	}

	page := listingPage{
		Title: a.cfg.Title, Prefix: prefix, Sort: field, Direction: direction,
		ZipEnabled: !a.cfg.ZipDisabled, Columns: 5, DownloadTitle: "서버 경유 다운로드",
	}
	if !page.ZipEnabled {
		page.Columns = 4
	}
	if a.cfg.DownloadMode == "presigned" {
		page.DownloadTitle = "S3 직접 다운로드"
	}
	page.DocumentTitle = a.cfg.Title
	if prefix != "" {
		page.DocumentTitle = "/" + prefix + " · " + a.cfg.Title
	}
	path := ""
	parts := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
	if prefix != "" {
		for i, name := range parts {
			path += name + "/"
			page.Crumbs = append(page.Crumbs, crumb{Name: name, Href: folderHref(path), Current: i == len(parts)-1})
		}
		page.Parent = folderHref(strings.Join(parts[:len(parts)-1], "/"))
	}
	refresh := url.Values{"refresh": {"1"}}
	if field != "name" || direction != 1 {
		refresh.Set("sort", field)
		if direction == -1 {
			refresh.Set("dir", "desc")
		}
	}
	page.RefreshHref = "?" + refresh.Encode()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	listing, _, err := a.cache.Get(ctx, prefix+"\x00"+cursor, query.Get("refresh") == "1", func(ctx context.Context) (backend.Listing, error) {
		return a.store.List(ctx, prefix, cursor)
	})
	if err != nil {
		status, _, message := a.upstreamError(err)
		page.Error = message
		page.EmptyMessage = "목록을 불러오지 못했습니다."
		a.renderListing(w, r, page, status)
		return
	}

	entries := listing.Entries
	sortEntries(entries, field, direction)
	folders := 0
	for _, entry := range entries {
		if entry.Folder {
			folders++
		}
		row := listingRow{
			Key: entry.Key, Name: entry.Name, Folder: entry.Folder, Size: entry.Size,
			Modified: entry.Modified, Icon: iconFor(entry), LinkTitle: entry.Name,
		}
		if entry.Folder {
			row.Href = folderHref(entry.Key)
			row.SizeText = "—"
			row.MobileMeta = "폴더"
		} else {
			row.Href = objectPath(entry.Key, false)
			row.DownloadHref = objectPath(entry.Key, true)
			row.SizeText = humanSize(entry.Size)
			row.DateText, row.ModifiedTitle = humanTime(entry.Modified)
			row.Preview = media.PreviewKind(entry.Name) != "unsupported"
			if row.Preview {
				row.LinkTitle = entry.Name + " 미리보기"
			}
			meta := []string{row.SizeText}
			if day, _ := humanTime(entry.Modified); day != "—" {
				meta = append(meta, day[:10])
			}
			row.MobileMeta = strings.Join(meta, " · ")
		}
		if entry.Folder {
			row.DateText, row.ModifiedTitle = humanTime(entry.Modified)
		}
		page.Rows = append(page.Rows, row)
	}
	page.Status = fmt.Sprintf("%d개 폴더 · %d개 파일", folders, len(entries)-folders)
	page.EmptyMessage = "이 폴더는 비어 있습니다."
	if listing.Cursor != "" {
		next := url.Values{"cursor": {listing.Cursor}}
		if field != "name" || direction != 1 {
			next.Set("sort", field)
			if direction == -1 {
				next.Set("dir", "desc")
			}
		}
		page.NextHref = "?" + next.Encode()
		page.Status += " · 다음 페이지 있음"
		page.EmptyMessage = "다음 페이지에 항목이 더 있습니다."
	}
	a.renderListing(w, r, page, http.StatusOK)
}

func (a *App) renderListing(w http.ResponseWriter, r *http.Request, page listingPage, status int) {
	settings, _ := json.Marshal(map[string]any{
		"title": a.cfg.Title, "prefix": page.Prefix, "zipEnabled": page.ZipEnabled,
		"zipMaxFiles": a.cfg.ZipMaxFiles, "zipMaxBytes": a.cfg.ZipMaxBytes,
		"downloadMode": a.cfg.DownloadMode, "previewMode": a.cfg.PreviewMode,
	})
	page.Config = string(settings)
	var body bytes.Buffer
	if err := listingTemplate.Execute(&body, page); err != nil {
		fail(w, 500, "render_failed", "목록을 표시하지 못했습니다.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}
