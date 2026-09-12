package config

import "testing"

func TestUnifiedSettings(t *testing.T) {
	isolateEnv(t)
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("BROWSER_PUBLIC", "true")
	c, err := Read()
	if err != nil || c.ServeMode != "browser" || c.CacheMode != "internal" || c.HealthPath != "/_mori/healthz" || c.ObjectCache.IndexDocument != "" {
		t.Fatal(c.ServeMode, c.CacheMode, c.HealthPath, err)
	}
	t.Setenv("SERVE_MODE", "spa")
	c, err = Read()
	if err != nil || !c.ObjectCache.SPAMode || c.ObjectCache.IndexDocument != "index.html" {
		t.Fatal(err, c.ObjectCache)
	}
	t.Setenv("INDEX_DOCUMENT", "")
	t.Setenv("HEALTH_PATH", "")
	t.Setenv("CACHE_ENABLED", "false")
	c, err = Read()
	if err != nil || c.CacheMode != "off" || c.ObjectCache.CacheEnabled || c.HealthPath != "" || c.ObjectCache.IndexDocument != "" {
		t.Fatal(err, c.CacheMode, c.HealthPath)
	}
}

func TestUnifiedRejectsInvalidSettings(t *testing.T) {
	for _, pair := range [][2]string{{"SERVE_MODE", "oops"}, {"CACHE_MODE", "external"}, {"CACHE_MODE", "oops"}, {"SPA_MODE", "oops"}, {"CACHE_RESPECT_ORIGIN", "oops"}, {"ACCESS_LOG", "oops"}, {"CACHE_BLOCK_SIZE", "NaNMiB"}, {"CACHE_MAX_DISK_SIZE", "999999999999TiB"}, {"CACHE_DEFAULT_TTL", "NaNd"}, {"CACHE_STALE_IF_ERROR", "-1s"}, {"CACHE_RULES_JSON", `[{"ttl":"-1s"}]`}, {"SPA_INDEX", "/a/../index.html"}, {"INDEX_DOCUMENT", "a//b"}, {"ERROR_PAGE_404", "/../secret"}, {"ERROR_PAGES_JSON", `{"404":"/a/../secret"}`}, {"HEALTH_PATH", "/_mori/api/object"}, {"HEALTH_PATH", "/"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			isolateEnv(t)
			t.Setenv("S3_BUCKET", "bucket")
			t.Setenv("BROWSER_PUBLIC", "true")
			t.Setenv(pair[0], pair[1])
			if _, err := Read(); err == nil {
				t.Fatal("accepted", pair)
			}
		})
	}
}

func TestUnifiedConflictsAndLegacyAliases(t *testing.T) {
	isolateEnv(t)
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("BROWSER_PUBLIC", "true")
	t.Setenv("SPA_MODE", "true")
	c, err := Read()
	if err != nil || c.ServeMode != "spa" {
		t.Fatal(err, c.ServeMode)
	}
	t.Setenv("SERVE_MODE", "browser")
	if _, err := Read(); err == nil {
		t.Fatal("conflicting SPA mode accepted")
	}
	t.Setenv("SPA_MODE", "false")
	t.Setenv("CACHE_MODE", "off")
	c, err = Read()
	if err != nil || c.CacheMode != "off" {
		t.Fatal(err, c.CacheMode)
	}
	t.Setenv("LISTEN_ADDR", ":9000")
	t.Setenv("BROWSER_LISTEN_ADDR", ":8000")
	if _, err := Read(); err == nil {
		t.Fatal("conflicting listeners accepted")
	}
}
