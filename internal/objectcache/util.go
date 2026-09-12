package objectcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

func hashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func normalizeWebPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	c := path.Clean(p)
	if c == "." {
		return "/"
	}
	return c
}

func parseBytes(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mul    int64
	}{
		{"TIB", 1 << 40}, {"TB", 1000 * 1000 * 1000 * 1000},
		{"GIB", 1 << 30}, {"GB", 1000 * 1000 * 1000},
		{"MIB", 1 << 20}, {"MB", 1000 * 1000},
		{"KIB", 1 << 10}, {"KB", 1000},
		{"B", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n := strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			f, err := strconv.ParseFloat(n, 64)
			if err != nil || f < 0 || math.IsNaN(f) || math.IsInf(f, 0) || f*float64(u.mul) >= math.Exp2(63) {
				return 0, fmt.Errorf("invalid byte size %q", s)
			}
			return int64(f * float64(u.mul)), nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid byte size %q", s)
	}
	return n, nil
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if strings.HasSuffix(s, "d") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || math.Abs(f*float64(24*time.Hour)) >= math.Exp2(63) {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(f * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(s)
}

func canonicalQuery(raw, mode string) string {
	switch mode {
	case "ignore":
		return ""
	case "include":
		return raw
	case "sort":
		vals, err := url.ParseQuery(raw)
		if err != nil {
			return raw
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
			sort.Strings(vals[k])
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			for _, v := range vals[k] {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		return strings.Join(parts, "&")
	default:
		return raw
	}
}

func cacheKeyFor(p, rawQuery, mode string) string {
	q := canonicalQuery(rawQuery, mode)
	if q == "" {
		return p
	}
	return p + "?" + q
}

// originKey turns a request path into the backend object key. The backend
// already resolves its own configured root, so no prefix is added here.
func originKey(requestPath string) string {
	return strings.TrimPrefix(normalizeWebPath(requestPath), "/")
}
