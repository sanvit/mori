package config

import (
	"fmt"
	"strings"

	"mori/internal/objectcache"
)

func readServeMode(c *Config) error {
	c.ServeMode = strings.ToLower(Env("SERVE_MODE", ""))
	spa, err := envBool("SPA_MODE", false)
	if err != nil {
		return err
	}
	if c.ServeMode == "" {
		c.ServeMode = "browser"
		if spa {
			c.ServeMode = "spa"
		}
	}
	if c.ServeMode != "browser" && c.ServeMode != "spa" && c.ServeMode != "direct" {
		return fmt.Errorf("SERVE_MODE must be browser, spa, or direct")
	}
	if raw := Env("SPA_MODE", ""); raw != "" && spa != (c.ServeMode == "spa") {
		return fmt.Errorf("SPA_MODE conflicts with SERVE_MODE")
	}
	if listen := Env("LISTEN_ADDR", ""); listen != "" {
		if legacy := Env("BROWSER_LISTEN_ADDR", ""); legacy != "" && legacy != listen {
			return fmt.Errorf("LISTEN_ADDR conflicts with BROWSER_LISTEN_ADDR")
		}
		c.Listen = listen
	}
	return nil
}

func readObjectOptions(c *Config) error {
	cache, err := objectcache.LoadConfig()
	if err != nil {
		return err
	}
	c.CacheMode = strings.ToLower(Env("CACHE_MODE", ""))
	if c.CacheMode == "" {
		c.CacheMode = "internal"
	}
	if c.CacheMode != "internal" && c.CacheMode != "off" {
		return fmt.Errorf("CACHE_MODE must be internal or off")
	}
	if !c.CacheEnabled {
		c.CacheMode = "off"
	}
	cache.CacheEnabled = c.CacheMode == "internal"
	cache.CacheDir = Env("CACHE_DIR", "./cache")
	cache.SPAMode = c.ServeMode == "spa"
	index := "index.html"
	if c.ServeMode == "browser" {
		index = ""
	}
	cache.IndexDocument = Env("INDEX_DOCUMENT", index)
	cache.HealthPath = "" // The authenticated application's router owns health.
	c.HealthPath = Env("HEALTH_PATH", "/_mori/healthz")
	for _, p := range []string{cache.IndexDocument, strings.TrimPrefix(cache.SPAIndex, "/")} {
		if err := ValidateKey(p, true); err != nil {
			return fmt.Errorf("invalid index path: %w", err)
		}
	}
	for _, p := range cache.ErrorPages {
		if err := ValidateKey(strings.TrimPrefix(p, "/"), false); err != nil {
			return fmt.Errorf("invalid error page path: %w", err)
		}
	}
	if c.HealthPath != "" {
		if !strings.HasPrefix(c.HealthPath, "/") || c.HealthPath == "/" || strings.HasSuffix(c.HealthPath, "/") || strings.HasPrefix(c.HealthPath, "/_mori/api") || strings.HasPrefix(c.HealthPath, "/_mori/assets") || strings.HasPrefix(c.HealthPath, "/_mori/vendor") || ValidateKey(strings.TrimPrefix(c.HealthPath, "/"), false) != nil {
			return fmt.Errorf("HEALTH_PATH must be a non-conflicting absolute file path or empty")
		}
	}
	c.ObjectCache = cache
	return nil
}
