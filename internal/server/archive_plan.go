package server

import (
	"context"
	"fmt"
	"path"
	"strings"

	"mori/internal/backend"
	"mori/internal/config"
)

type archiveError struct {
	Status        int
	Code, Message string
}

func (e *archiveError) Error() string                 { return e.Code }
func zipError(status int, code, message string) error { return &archiveError{status, code, message} }

// Build a bounded metadata-only plan. Explicit file keys use a fresh Stat;
// recursive files use fresh listing metadata (not client sizes or list cache).
// GET still enforces If-Match/ETag/length, so changed files cannot silently pass.
func (a *App) buildArchivePlan(ctx context.Context, prefix string, keys []string) (archivePlan, error) {
	plan := archivePlan{Filename: "files.zip"}
	if config.ValidateKey(prefix, true) != nil || (prefix != "" && !strings.HasSuffix(prefix, "/")) || len(a.cfg.Prefix+prefix) > 1024 {
		return plan, zipError(400, "invalid_prefix", "올바르지 않은 폴더 경로입니다.")
	}
	if len(keys) == 0 || len(keys) > a.cfg.ZipMaxFiles {
		return plan, zipError(400, "zip_file_limit", fmt.Sprintf("파일 또는 폴더를 1개 이상, 최대 %d개까지 선택해 주세요.", a.cfg.ZipMaxFiles))
	}
	if prefix != "" {
		plan.Filename = path.Base(strings.TrimSuffix(prefix, "/")) + ".zip"
	}
	// Validate ALL selections before sending any network request. Selected roots
	// must be immediate children; arbitrary descendants arrive only from S3 LIST.
	selections := []string{}
	selected := map[string]bool{}
	for _, key := range keys {
		name := strings.TrimSuffix(strings.TrimPrefix(key, prefix), "/")
		if config.ValidateKey(key, false) != nil || len(a.cfg.Prefix+key) > 1024 || !strings.HasPrefix(key, prefix) || name == "" || strings.ContainsAny(name, "/\\:") {
			return plan, zipError(400, "invalid_archive_key", "현재 폴더의 파일 또는 폴더를 선택해 주세요. 잘못된 경로나 콜론은 ZIP 이름에 허용하지 않습니다.")
		}
		if !selected[key] {
			selected[key] = true
			selections = append(selections, key)
		}
	}
	seen := map[string]int{}
	add := func(item archiveItem) error {
		item.Name = strings.TrimPrefix(item.Key, prefix)
		if config.ValidateKey(item.Key, false) != nil || len(a.cfg.Prefix+item.Key) > 1024 || !strings.HasPrefix(item.Key, prefix) || item.Name == "" || strings.ContainsAny(item.Name, "\\:") {
			return zipError(400, "unsafe_archive_path", "하위 파일에 ZIP으로 안전하게 저장할 수 없는 경로가 있습니다.")
		}
		if idx, ok := seen[item.Key]; ok {
			prev := plan.Items[idx]
			if prev.Size != item.Size || prev.ETag != item.ETag {
				return &backend.UpstreamError{Status: 412, Code: "ObjectChanged"}
			}
			return nil
		}
		if item.Directory {
			// Preserve zero-byte S3 folder markers, including empty folders.
			// Never silently discard a nonempty object whose key ends in '/'.
			if item.Size != 0 {
				return zipError(400, "nonempty_folder_marker", "내용이 있는 슬래시 끝 객체는 폴더로 ZIP에 넣을 수 없습니다.")
			}
			if plan.Directories >= a.cfg.ZipMaxFiles {
				return zipError(413, "zip_folder_limit", "ZIP에 포함할 폴더 항목 수가 서버 제한을 넘었습니다.")
			}
			plan.Directories++
		} else {
			if item.Size < 0 || item.ETag == "" || strings.HasPrefix(item.ETag, "W/") {
				return zipError(502, "zip_metadata_missing", "저장소가 ZIP에 필요한 파일 크기 또는 버전 정보를 반환하지 않았습니다.")
			}
			if plan.Files >= a.cfg.ZipMaxFiles {
				return zipError(413, "zip_file_limit", fmt.Sprintf("하위 파일을 포함한 합계가 ZIP 파일 수 제한 %d개를 넘었습니다.", a.cfg.ZipMaxFiles))
			}
			if item.Size > a.cfg.ZipMaxBytes-plan.Size {
				return zipError(413, "zip_size_limit", "하위 파일을 포함한 합계가 서버의 ZIP 용량 제한을 넘었습니다.")
			}
			plan.Size += item.Size
			plan.Files++
		}
		seen[item.Key] = len(plan.Items)
		plan.Items = append(plan.Items, item)
		return nil
	}
	for _, key := range selections {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if strings.HasSuffix(key, "/") {
			found := false
			err := a.store.Walk(ctx, key, func(obj backend.Object) error {
				found = true
				return add(archiveItem{Object: obj})
			})
			if err != nil {
				return plan, err
			}
			if !found {
				return plan, zipError(404, "zip_folder_missing", "선택한 폴더를 찾을 수 없습니다. 목록을 새로고침해 주세요.")
			}
		} else {
			st, err := a.store.Stat(ctx, key)
			if err != nil {
				return plan, err
			}
			item := archiveItem{Object: st}
			if err := add(item); err != nil {
				return plan, err
			}
		}
	}
	// S3 permits both "a" and "a/b", but a ZIP extractor cannot represent
	// the same path as both a file and directory. Fail rather than overwrite.
	files := map[string]bool{}
	for _, item := range plan.Items {
		if !item.Directory {
			files[item.Name] = true
		}
	}
	for _, item := range plan.Items {
		name := strings.TrimSuffix(item.Name, "/")
		if item.Directory && files[name] {
			return plan, zipError(409, "zip_path_conflict", "같은 경로에 파일과 폴더가 있어 ZIP으로 묶을 수 없습니다.")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if files[parent] {
				return plan, zipError(409, "zip_path_conflict", "같은 경로에 파일과 폴더가 있어 ZIP으로 묶을 수 없습니다.")
			}
		}
	}
	return plan, ctx.Err()
}
