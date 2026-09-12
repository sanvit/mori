// Package objectcache stores object bodies as aligned segments on disk and
// serves reads from them, fetching only the missing ranges from the backend.
package objectcache

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type CacheRule struct {
	Prefix      string `json:"prefix"`
	Suffix      string `json:"suffix"`
	TTL         string `json:"ttl"`
	BrowserTTL  string `json:"browser_ttl"`
	Bypass      bool   `json:"bypass"`
	IgnoreQuery bool   `json:"ignore_query"`
}

// Config holds the cache and site options. Storage connection settings belong
// to the backend the Proxy reads through, not here.
type Config struct {
	CacheDownloadConcurrency    int
	OriginMaxConcurrentRequests int
	OriginFetchMaxSize          int64
	CacheEnabled                bool
	CacheDir                    string
	CacheBlockSize              int64
	CacheSegmentSize            int64
	CacheMaxDiskSize            int64
	CacheQueryMode              string
	CacheDefaultTTL             time.Duration
	CacheMinTTL                 time.Duration
	CacheMaxTTL                 time.Duration
	CacheRespectOrigin          bool
	CacheStaleIfError           time.Duration
	CacheNegativeTTL404         time.Duration
	CacheNegativeTTL403         time.Duration
	CacheRules                  []CacheRule

	SPAMode              bool
	SPAIndex             string
	SPAAllowDottedRoutes bool

	IndexDocument string

	HealthPath string
	AccessLog  bool

	ErrorPages map[int]string
}

// LoadConfig reads the cache and site options from the environment and reports
// the first invalid value, so a typo fails at startup rather than at request time.
func LoadConfig() (Config, error) {
	for _, key := range []string{"INDEX_DOCUMENT", "SPA_INDEX", "ERROR_PAGE_404"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" && !validObjectPath("/"+strings.TrimPrefix(raw, "/")) {
			return Config{}, fmt.Errorf("%s contains an invalid path", key)
		}
	}
	cfg := Config{
		CacheEnabled:         envBool("CACHE_ENABLED", true),
		CacheDir:             env("CACHE_DIR", "/cache"),
		CacheQueryMode:       strings.ToLower(env("CACHE_QUERY_MODE", "sort")),
		CacheRespectOrigin:   envBool("CACHE_RESPECT_ORIGIN", true),
		SPAMode:              envBool("SPA_MODE", false),
		SPAIndex:             normalizeWebPath(env("SPA_INDEX", "/index.html")),
		SPAAllowDottedRoutes: envBool("SPA_ALLOW_DOTTED_ROUTES", false),
		IndexDocument:        strings.Trim(envOptional("INDEX_DOCUMENT", "index.html"), "/"),
		AccessLog:            envBool("ACCESS_LOG", true),
		ErrorPages:           map[int]string{},
	}
	for _, name := range []string{"CACHE_ENABLED", "CACHE_RESPECT_ORIGIN", "SPA_MODE", "SPA_ALLOW_DOTTED_ROUTES", "ACCESS_LOG"} {
		if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
			if _, err := strconv.ParseBool(raw); err != nil {
				return cfg, fmt.Errorf("%s must be true or false", name)
			}
		}
	}
	if h := envOptional("HEALTH_PATH", "/healthz"); h != "" {
		cfg.HealthPath = normalizeWebPath(h)
	}

	for _, option := range []struct {
		name     string
		value    *int
		fallback string
	}{
		{"CACHE_DOWNLOAD_CONCURRENCY", &cfg.CacheDownloadConcurrency, "4"},
		{"ORIGIN_MAX_CONCURRENT_REQUESTS", &cfg.OriginMaxConcurrentRequests, "32"},
	} {
		n, err := strconv.Atoi(env(option.name, option.fallback))
		if err != nil || n < 1 || n > 256 {
			return cfg, fmt.Errorf("%s must be between 1 and 256", option.name)
		}
		*option.value = n
	}
	var err error
	if cfg.CacheBlockSize, err = parseBytes(env("CACHE_BLOCK_SIZE", "8MiB")); err != nil {
		return cfg, fmt.Errorf("CACHE_BLOCK_SIZE: %w", err)
	}
	if cfg.CacheBlockSize < 256*1024 {
		return cfg, fmt.Errorf("CACHE_BLOCK_SIZE must be at least 256KB")
	}
	if cfg.CacheSegmentSize, err = parseBytes(env("CACHE_SEGMENT_SIZE", "2MiB")); err != nil {
		return cfg, fmt.Errorf("CACHE_SEGMENT_SIZE: %w", err)
	}
	if cfg.CacheSegmentSize < 256*1024 {
		return cfg, fmt.Errorf("CACHE_SEGMENT_SIZE must be at least 256KB")
	}
	if cfg.CacheSegmentSize > cfg.CacheBlockSize {
		return cfg, fmt.Errorf("CACHE_SEGMENT_SIZE must be <= CACHE_BLOCK_SIZE")
	}
	if cfg.CacheBlockSize%cfg.CacheSegmentSize != 0 {
		return cfg, fmt.Errorf("CACHE_BLOCK_SIZE must be an exact multiple of CACHE_SEGMENT_SIZE")
	}
	if cfg.OriginFetchMaxSize, err = parseBytes(env("ORIGIN_FETCH_MAX_SIZE", "16MiB")); err != nil {
		return cfg, fmt.Errorf("ORIGIN_FETCH_MAX_SIZE: %w", err)
	}
	if cfg.OriginFetchMaxSize < cfg.CacheSegmentSize {
		return cfg, fmt.Errorf("ORIGIN_FETCH_MAX_SIZE must be >= CACHE_SEGMENT_SIZE")
	}
	if cfg.CacheMaxDiskSize, err = parseBytes(env("CACHE_MAX_DISK_SIZE", "100GB")); err != nil {
		return cfg, fmt.Errorf("CACHE_MAX_DISK_SIZE: %w", err)
	}
	if cfg.CacheDefaultTTL, err = parseDuration(env("CACHE_DEFAULT_TTL", "1h")); err != nil {
		return cfg, fmt.Errorf("CACHE_DEFAULT_TTL: %w", err)
	}
	if cfg.CacheMinTTL, err = parseDuration(env("CACHE_MIN_TTL", "0s")); err != nil {
		return cfg, fmt.Errorf("CACHE_MIN_TTL: %w", err)
	}
	if cfg.CacheMaxTTL, err = parseDuration(env("CACHE_MAX_TTL", "168h")); err != nil {
		return cfg, fmt.Errorf("CACHE_MAX_TTL: %w", err)
	}
	if cfg.CacheStaleIfError, err = parseDuration(env("CACHE_STALE_IF_ERROR", "24h")); err != nil {
		return cfg, fmt.Errorf("CACHE_STALE_IF_ERROR: %w", err)
	}
	if cfg.CacheNegativeTTL404, err = parseDuration(env("CACHE_NEGATIVE_TTL_404", "30s")); err != nil {
		return cfg, fmt.Errorf("CACHE_NEGATIVE_TTL_404: %w", err)
	}
	if cfg.CacheNegativeTTL404 < 0 {
		return cfg, fmt.Errorf("CACHE_NEGATIVE_TTL_404 must be >= 0")
	}
	if cfg.CacheNegativeTTL403, err = parseDuration(env("CACHE_NEGATIVE_TTL_403", "10s")); err != nil {
		return cfg, fmt.Errorf("CACHE_NEGATIVE_TTL_403: %w", err)
	}
	if cfg.CacheNegativeTTL403 < 0 {
		return cfg, fmt.Errorf("CACHE_NEGATIVE_TTL_403 must be >= 0")
	}
	if cfg.CacheDefaultTTL < 0 || cfg.CacheMinTTL < 0 || cfg.CacheMaxTTL < 0 || cfg.CacheStaleIfError < 0 {
		return cfg, fmt.Errorf("cache durations must be non-negative")
	}
	if cfg.CacheMaxTTL > 0 && cfg.CacheMinTTL > cfg.CacheMaxTTL {
		return cfg, fmt.Errorf("CACHE_MIN_TTL must be <= CACHE_MAX_TTL")
	}

	switch cfg.CacheQueryMode {
	case "sort", "include", "ignore":
	default:
		return cfg, fmt.Errorf("CACHE_QUERY_MODE must be sort, include, or ignore")
	}

	if raw := strings.TrimSpace(os.Getenv("CACHE_RULES_JSON")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg.CacheRules); err != nil {
			return cfg, fmt.Errorf("CACHE_RULES_JSON: %w", err)
		}
	}
	// A malformed duration inside a rule is otherwise ignored at request time, which
	// looks like the rule silently not applying.
	for i, r := range cfg.CacheRules {
		if r.TTL != "" {
			if d, err := parseDuration(r.TTL); err != nil || d < 0 {
				return cfg, fmt.Errorf("CACHE_RULES_JSON[%d].ttl: %w", i, err)
			}
		}
		if r.BrowserTTL != "" {
			if d, err := parseDuration(r.BrowserTTL); err != nil || d < 0 {
				return cfg, fmt.Errorf("CACHE_RULES_JSON[%d].browser_ttl: %w", i, err)
			}
		}
	}

	if p := strings.TrimSpace(os.Getenv("ERROR_PAGE_404")); p != "" {
		cfg.ErrorPages[404] = normalizeWebPath(p)
	}
	if raw := strings.TrimSpace(os.Getenv("ERROR_PAGES_JSON")); raw != "" {
		var pages map[string]string
		if err := json.Unmarshal([]byte(raw), &pages); err != nil {
			return cfg, fmt.Errorf("ERROR_PAGES_JSON: %w", err)
		}
		for k, v := range pages {
			if !validObjectPath("/"+strings.TrimPrefix(v, "/")) || v == "" || v == "/" {
				return cfg, fmt.Errorf("ERROR_PAGES_JSON: invalid path %q", v)
			}
			code, err := strconv.Atoi(k)
			if err != nil || code < 400 || code > 599 {
				return cfg, fmt.Errorf("ERROR_PAGES_JSON: invalid status %q", k)
			}
			cfg.ErrorPages[code] = normalizeWebPath(v)
		}
	}

	return cfg, nil
}

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

// envOptional returns the default when the variable is unset, but an explicitly
// empty value stays empty. That is what lets a path-keyed feature such as the
// health endpoint or the index document be switched off rather than only retuned.
func envOptional(k, d string) string {
	raw, ok := os.LookupEnv(k)
	if !ok {
		return d
	}
	return strings.TrimSpace(raw)
}

func envBool(k string, d bool) bool {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return d
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return d
	}
	return b
}
