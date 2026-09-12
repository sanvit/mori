package objectcache

import (
	"strconv"
	"strings"
	"time"
)

type CachePolicy struct {
	Cacheable  bool
	TTL        time.Duration
	BrowserTTL *time.Duration
}

func cachePolicyFor(cfg Config, requestPath string, h map[string][]string) CachePolicy {
	p := CachePolicy{Cacheable: cfg.CacheEnabled, TTL: cfg.CacheDefaultTTL}
	cc := strings.Join(h["Cache-Control"], ",")
	if cc == "" {
		cc = strings.Join(h["cache-control"], ",")
	}

	if cfg.CacheRespectOrigin {
		directives := parseCacheControl(cc)
		if _, ok := directives["no-store"]; ok {
			p.Cacheable = false
		}
		if _, ok := directives["private"]; ok {
			p.Cacheable = false
		}
		if _, ok := directives["no-cache"]; ok {
			p.TTL = 0
		} else if v, ok := directives["s-maxage"]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				p.TTL = time.Duration(n) * time.Second
			}
		} else if v, ok := directives["max-age"]; ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
				p.TTL = time.Duration(n) * time.Second
			}
		}
	}

	for _, r := range cfg.CacheRules {
		if !ruleMatches(r, requestPath) {
			continue
		}
		if r.Bypass {
			p.Cacheable = false
		}
		if r.TTL != "" {
			if d, err := parseDuration(r.TTL); err == nil {
				p.TTL = d
				// An explicit TTL overrides an origin that asked not to be cached, but
				// it must not re-enable caching this same rule just turned off.
				if !r.Bypass {
					p.Cacheable = true
				}
			}
		}
		if r.BrowserTTL != "" {
			if d, err := parseDuration(r.BrowserTTL); err == nil {
				p.BrowserTTL = &d
			}
		}
		break
	}

	if !cfg.CacheEnabled {
		p.Cacheable = false
	}
	if p.TTL < cfg.CacheMinTTL {
		p.TTL = cfg.CacheMinTTL
	}
	if cfg.CacheMaxTTL > 0 && p.TTL > cfg.CacheMaxTTL {
		p.TTL = cfg.CacheMaxTTL
	}
	return p
}

func parseCacheControl(s string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		k := strings.ToLower(strings.TrimSpace(kv[0]))
		v := ""
		if len(kv) == 2 {
			v = strings.Trim(strings.TrimSpace(kv[1]), `"`)
		}
		out[k] = v
	}
	return out
}

// ruleMatches reports whether a rule applies to a request path. A rule with
// neither a prefix nor a suffix is a catch-all: because the first matching rule
// wins, placing one last turns it into a default for everything the earlier rules
// did not claim.
func ruleMatches(r CacheRule, p string) bool {
	if r.Prefix != "" && !strings.HasPrefix(p, r.Prefix) {
		return false
	}
	if r.Suffix != "" && !strings.HasSuffix(p, r.Suffix) {
		return false
	}
	return true
}
