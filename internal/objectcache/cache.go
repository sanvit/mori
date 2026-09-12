package objectcache

import (
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type ObjectMeta struct {
	CacheKey           string    `json:"cache_key"`
	OriginKey          string    `json:"origin_key"`
	ETag               string    `json:"etag"`
	LastModified       time.Time `json:"last_modified"`
	Size               int64     `json:"size"`
	ContentType        string    `json:"content_type"`
	ContentEncoding    string    `json:"content_encoding,omitempty"`
	ContentDisposition string    `json:"content_disposition,omitempty"`
	CacheControl       string    `json:"cache_control"`
	ExpiresHeader      string    `json:"expires_header"`
	CachedAt           time.Time `json:"cached_at"`
	ExpiresAt          time.Time `json:"expires_at"`
	Version            string    `json:"version"`
	Cacheable          bool      `json:"cacheable"`
	BrowserTTL         int64     `json:"browser_ttl_seconds,omitempty"`
	BrowserTTLSet      bool      `json:"browser_ttl_set,omitempty"`
	// StatusCode is non-zero only for a cached negative origin lookup. Negative
	// entries contain no response body and never make segment data reachable.
	StatusCode int `json:"status_code,omitempty"`
}

func (m *ObjectMeta) isNegative() bool {
	return m != nil && (m.StatusCode == http.StatusNotFound || m.StatusCode == http.StatusForbidden)
}

type lruEntry struct {
	Size       int64
	LastAccess time.Time
	Protected  bool
}

// Keep most of the cache available to blocks that have demonstrated reuse while
// reserving a probation area where one-hit streams can churn without displacing
// the hot set. The tier is intentionally in-memory only, like LastAccess today.
const protectedCacheNumerator, protectedCacheDenominator int64 = 4, 5

const maxNegativeMetadataEntries = 4096

type negativeMetaItem struct {
	cacheKey string
	stamp    int64
}

type negativeMetaHeap []negativeMetaItem

func (h negativeMetaHeap) Len() int           { return len(h) }
func (h negativeMetaHeap) Less(i, j int) bool { return h[i].stamp < h[j].stamp }
func (h negativeMetaHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *negativeMetaHeap) Push(x any)        { *h = append(*h, x.(negativeMetaItem)) }
func (h *negativeMetaHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// DiskCache groups cached data into blocks for eviction, while storing/fetching
// individual segments inside each block. A block is only a management unit; it
// does not need to be complete to be useful.
type DiskCache struct {
	openedAt         time.Time
	restartMeta      sync.Map
	root             string
	maxBytes         int64
	blockSize        int64
	segmentSize      int64
	segmentsPerBlock int64

	mu             sync.Mutex
	metaMu         sync.Mutex
	total          int64
	protectedBytes int64
	entries        map[string]lruEntry // block directory -> aggregate segment size/access
	negativeMeta   map[string]int64
	negativeHeap   negativeMetaHeap
}

func NewDiskCache(root string, maxBytes, blockSize, segmentSize int64) (*DiskCache, error) {
	if blockSize <= 0 || segmentSize <= 0 || segmentSize > blockSize || blockSize%segmentSize != 0 {
		return nil, fmt.Errorf("invalid block/segment sizes: block=%d segment=%d", blockSize, segmentSize)
	}
	c := &DiskCache{
		openedAt: time.Now(),
		root:     root, maxBytes: maxBytes, blockSize: blockSize, segmentSize: segmentSize,
		segmentsPerBlock: blockSize / segmentSize, entries: map[string]lruEntry{}, negativeMeta: map[string]int64{},
	}
	for _, d := range []string{c.metaDir(), c.blocksDir(), c.tmpDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	if err := c.scan(); err != nil {
		return nil, err
	}
	if err := c.scanNegativeMeta(); err != nil {
		return nil, err
	}
	if err := filepath.WalkDir(c.metaDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".json" {
			c.restartMeta.Store(p, true)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	c.evictIfNeeded()
	return c, nil
}

func (c *DiskCache) metaDir() string   { return filepath.Join(c.root, "meta") }
func (c *DiskCache) blocksDir() string { return filepath.Join(c.root, "blocks") }
func (c *DiskCache) tmpDir() string    { return filepath.Join(c.root, "tmp") }

// scan rebuilds disk accounting after restart. New .seg files are aggregated by
// their parent block directory so eviction removes the whole 8 MiB management
// block. Legacy .blk files from older versions remain accounted for and are
// evicted as individual files.
func (c *DiskCache) scan() error {
	return filepath.WalkDir(c.blocksDir(), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entryPath := p
		if filepath.Ext(p) == ".seg" {
			entryPath = filepath.Dir(p)
		}
		c.total += info.Size()
		e := c.entries[entryPath]
		e.Size += info.Size()
		if info.ModTime().After(e.LastAccess) {
			e.LastAccess = info.ModTime()
		}
		c.entries[entryPath] = e
		return nil
	})
}

func (c *DiskCache) metaPath(cacheKey string) string {
	h := hashString(cacheKey)
	return filepath.Join(c.metaDir(), h[:2], h+".json")
}

func (c *DiskCache) LoadMeta(cacheKey string) (*ObjectMeta, error) {
	return c.loadMeta(cacheKey)
}

func (c *DiskCache) loadMeta(cacheKey string) (*ObjectMeta, error) {
	b, err := os.ReadFile(c.metaPath(cacheKey))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m ObjectMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	// On restart revalidate metadata before reusing persisted bodies. A fresh
	// policy or changed access rights must not inherit the previous process TTL.
	if _, pending := c.restartMeta.Load(c.metaPath(cacheKey)); pending && m.ExpiresAt.After(c.openedAt) {
		m.ExpiresAt = c.openedAt
	}
	return &m, nil
}

func (c *DiskCache) SaveMeta(m *ObjectMeta) error {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	current, err := c.loadMeta(m.CacheKey)
	if err != nil {
		return err
	}
	// Refreshes are stamped before their origin lookup. A slower, older lookup
	// must not overwrite a result from a request that began later.
	if current != nil && current.CachedAt.After(m.CachedAt) {
		return nil
	}
	if err := c.saveMeta(m); err != nil {
		return err
	}
	c.restartMeta.Delete(c.metaPath(m.CacheKey))
	if m.isNegative() {
		c.trackNegativeMetaLocked(m.CacheKey, m.CachedAt.UnixNano())
	} else {
		delete(c.negativeMeta, m.CacheKey)
	}
	return nil
}

func (c *DiskCache) saveMeta(m *ObjectMeta) error {
	p := c.metaPath(m.CacheKey)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".meta-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, p)
}

// ExpireMetaIfVersion invalidates only the version whose body fetch detected a
// precondition or ETag mismatch. A late stale run cannot overwrite metadata
// another request has already refreshed to a new version.
func (c *DiskCache) ExpireMetaIfVersion(cacheKey, version string) error {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	m, err := c.loadMeta(cacheKey)
	if err != nil || m == nil || m.isNegative() || m.Version != version {
		return err
	}
	m.ExpiresAt = time.Time{}
	return c.saveMeta(m)
}

func (c *DiskCache) trackNegativeMetaLocked(cacheKey string, stamp int64) {
	c.negativeMeta[cacheKey] = stamp
	heap.Push(&c.negativeHeap, negativeMetaItem{cacheKey: cacheKey, stamp: stamp})
	for len(c.negativeMeta) > maxNegativeMetadataEntries {
		item := heap.Pop(&c.negativeHeap).(negativeMetaItem)
		if c.negativeMeta[item.cacheKey] != item.stamp {
			continue
		}
		delete(c.negativeMeta, item.cacheKey)
		_ = os.Remove(c.metaPath(item.cacheKey))
	}
	// Repeated refreshes of a small key set leave stale heap nodes. Rebuild at a
	// fixed multiple so the tracker itself also has a hard memory bound.
	if len(c.negativeHeap) > maxNegativeMetadataEntries*4 {
		c.negativeHeap = c.negativeHeap[:0]
		for key, currentStamp := range c.negativeMeta {
			c.negativeHeap = append(c.negativeHeap, negativeMetaItem{cacheKey: key, stamp: currentStamp})
		}
		heap.Init(&c.negativeHeap)
	}
}

func (c *DiskCache) scanNegativeMeta() error {
	now := time.Now()
	return filepath.WalkDir(c.metaDir(), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m ObjectMeta
		if json.Unmarshal(b, &m) != nil || !m.isNegative() {
			return nil
		}
		if !now.Before(m.ExpiresAt) {
			_ = os.Remove(path)
			return nil
		}
		c.trackNegativeMetaLocked(m.CacheKey, m.CachedAt.UnixNano())
		return nil
	})
}

func (c *DiskCache) blockDir(cacheKey, version string, blockIdx int64) string {
	h := hashString(cacheKey)
	return filepath.Join(c.blocksDir(), h[:2], h, version, fmt.Sprintf("%012d.block", blockIdx))
}

func (c *DiskCache) segmentLocation(cacheKey, version string, segIdx int64) (blockDir, segmentPath string) {
	blockIdx := segIdx / c.segmentsPerBlock
	localIdx := segIdx % c.segmentsPerBlock
	blockDir = c.blockDir(cacheKey, version, blockIdx)
	segmentPath = filepath.Join(blockDir, fmt.Sprintf("%04d.seg", localIdx))
	return blockDir, segmentPath
}

func (c *DiskCache) HasSegment(cacheKey, version string, segIdx int64) bool {
	blockDir, p := c.segmentLocation(cacheKey, version, segIdx)
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() {
		return false
	}
	c.touchBlock(blockDir)
	return true
}

func (c *DiskCache) OpenSegment(cacheKey, version string, segIdx int64) (*os.File, error) {
	blockDir, p := c.segmentLocation(cacheKey, version, segIdx)
	f, err := os.Open(p)
	if err != nil {
		// AllSegmentsPresent trusts the in-memory accounting rather than stat-ing
		// every segment, so a file that disappeared underneath us would otherwise
		// keep being reported as cached and fail every later request the same way.
		// Drop the block and let the next request refetch it.
		c.forgetBlock(blockDir)
		return nil, err
	}
	c.touchBlock(blockDir)
	return f, nil
}

// forgetBlock discards a block from both the accounting and the disk, so the two
// agree again after external interference.
func (c *DiskCache) forgetBlock(blockDir string) {
	c.mu.Lock()
	if e, ok := c.entries[blockDir]; ok {
		c.total -= e.Size
		if e.Protected {
			c.protectedBytes -= e.Size
		}
		delete(c.entries, blockDir)
	}
	_ = os.RemoveAll(blockDir)
	c.mu.Unlock()
}

// AllSegmentsPresent reports whether every segment of the object is cached. It
// answers from the in-memory block accounting instead of stat-ing each segment,
// because this runs on every full-object request and a 1 GiB object is 512
// segments at the default size. A block's aggregate size can only equal the size
// its segments should occupy when all of them are there: each committed segment
// has its exact expected length, so no subset of them adds up to the total.
func (c *DiskCache) AllSegmentsPresent(m *ObjectMeta) bool {
	if m.Size <= 0 {
		return m.Size == 0
	}
	blocks := (m.Size + c.blockSize - 1) / c.blockSize
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()
	for b := int64(0); b < blocks; b++ {
		want := c.blockSize
		if remaining := m.Size - b*c.blockSize; remaining < want {
			want = remaining
		}
		dir := c.blockDir(m.CacheKey, m.Version, b)
		e, ok := c.entries[dir]
		if !ok || e.Size != want {
			return false
		}
		e.LastAccess = now
		if !e.Protected {
			e.Protected = true
			c.protectedBytes += e.Size
		}
		c.entries[dir] = e
	}
	c.rebalanceProtectedLocked()
	return true
}

// Stats reports current disk usage against the configured budget, and how many
// blocks are being tracked.
func (c *DiskCache) Stats() (used, max, blocks int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total, c.maxBytes, int64(len(c.entries))
}

// StoreSegmentStreaming persists one aligned segment without buffering it in
// memory. onChunk receives each chunk after it has been written to the temporary
// cache file, allowing Range MISS traffic to be forwarded at the same time.
func (c *DiskCache) StoreSegmentStreaming(cacheKey, version string, segIdx, expectedSize int64, r io.Reader, onChunk func(offset int64, chunk []byte) error) error {
	blockDir, final := c.segmentLocation(cacheKey, version, segIdx)
	if err := os.MkdirAll(blockDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.tmpDir(), ".segment-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	buf := make([]byte, 128*1024)
	var total int64
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			if expectedSize >= 0 && total+int64(n) > expectedSize {
				tmp.Close()
				return fmt.Errorf("origin segment exceeds expected size %d", expectedSize)
			}
			chunk := buf[:n]
			if _, err := tmp.Write(chunk); err != nil {
				tmp.Close()
				return err
			}
			if onChunk != nil {
				if err := onChunk(total, chunk); err != nil {
					tmp.Close()
					return err
				}
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmp.Close()
			return readErr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if expectedSize >= 0 && total != expectedSize {
		return fmt.Errorf("segment size mismatch: got %d bytes, expected %d", total, expectedSize)
	}
	return c.commitSegment(tmpName, blockDir, final, total)
}

func (c *DiskCache) commitSegment(tmp, blockDir, final string, size int64) error {
	if err := os.MkdirAll(blockDir, 0o755); err != nil {
		return err
	}

	// Serialize the atomic rename/accounting with block eviction. Because one
	// eviction unit contains only a handful of segment files (4 by default),
	// keeping this critical section around filesystem operations stays bounded
	// and prevents an evict/commit race from leaving stale disk accounting.
	c.mu.Lock()
	var oldSize int64
	if st, err := os.Stat(final); err == nil {
		oldSize = st.Size()
	}
	if err := os.Rename(tmp, final); err != nil {
		c.mu.Unlock()
		return err
	}
	e := c.entries[blockDir]
	e.Size += size - oldSize
	e.LastAccess = time.Now()
	c.total += size - oldSize
	if e.Protected {
		c.protectedBytes += size - oldSize
	}
	c.entries[blockDir] = e
	c.rebalanceProtectedLocked()
	c.mu.Unlock()

	c.evictIfNeeded()
	return nil
}

// DiscardSegment removes a committed segment after a protocol-level validation
// failure affecting its origin fetch run. It updates block accounting under the
// same lock used by commit and eviction.
func (c *DiskCache) DiscardSegment(cacheKey, version string, segIdx int64) {
	blockDir, final := c.segmentLocation(cacheKey, version, segIdx)
	c.mu.Lock()
	st, err := os.Stat(final)
	if err == nil {
		if err := os.Remove(final); err == nil {
			e := c.entries[blockDir]
			e.Size -= st.Size()
			c.total -= st.Size()
			if e.Protected {
				c.protectedBytes -= st.Size()
			}
			if e.Size <= 0 {
				delete(c.entries, blockDir)
				_ = os.Remove(blockDir)
			} else {
				c.entries[blockDir] = e
			}
		}
	}
	c.mu.Unlock()
}

func (c *DiskCache) touchBlock(blockDir string) {
	c.mu.Lock()
	if e, ok := c.entries[blockDir]; ok {
		e.LastAccess = time.Now()
		if !e.Protected {
			e.Protected = true
			c.protectedBytes += e.Size
		}
		c.entries[blockDir] = e
		c.rebalanceProtectedLocked()
	}
	c.mu.Unlock()
}

// rebalanceProtectedLocked caps the protected tier at 80% of the disk budget.
// Promotion is driven only by a cache read; newly committed blocks remain on
// probation. Demoting the oldest protected blocks leaves room for probationary
// entries to survive long enough to demonstrate a second access.
func (c *DiskCache) rebalanceProtectedLocked() {
	if c.maxBytes <= 0 {
		return
	}
	limit := c.maxBytes / protectedCacheDenominator * protectedCacheNumerator
	for c.protectedBytes > limit {
		var oldestPath string
		var oldest time.Time
		for path, e := range c.entries {
			if !e.Protected {
				continue
			}
			if oldestPath == "" || e.LastAccess.Before(oldest) {
				oldestPath, oldest = path, e.LastAccess
			}
		}
		if oldestPath == "" {
			return
		}
		e := c.entries[oldestPath]
		e.Protected = false
		c.entries[oldestPath] = e
		c.protectedBytes -= e.Size
	}
}

func (c *DiskCache) evictIfNeeded() {
	if c.maxBytes <= 0 {
		return
	}
	for {
		c.mu.Lock()
		if c.total <= c.maxBytes || len(c.entries) == 0 {
			c.mu.Unlock()
			return
		}
		type kv struct {
			Path string
			E    lruEntry
		}
		all := make([]kv, 0, len(c.entries))
		for p, e := range c.entries {
			all = append(all, kv{p, e})
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].E.Protected != all[j].E.Protected {
				return !all[i].E.Protected
			}
			return all[i].E.LastAccess.Before(all[j].E.LastAccess)
		})
		victim := all[0]
		// Remove while holding the same mutex used by commitSegment so a segment
		// cannot be atomically committed into a block that is being evicted.
		_ = os.RemoveAll(victim.Path)
		delete(c.entries, victim.Path)
		c.total -= victim.E.Size
		if victim.E.Protected {
			c.protectedBytes -= victim.E.Size
		}
		c.mu.Unlock()
	}
}

// SegmentStreamWriter splits a full-object origin stream into fixed-size segment
// files. It never buffers a whole segment in RAM; data is written incrementally to
// a temporary file and atomically committed at segment boundaries.
type SegmentStreamWriter struct {
	cache        *DiskCache
	cacheKey     string
	version      string
	expectedSize int64
	written      int64
	index        int64
	inSegment    int64
	file         *os.File
	tmpName      string
	disabled     bool
}

// expectedSize is the full object size. It is what tells Close whether a trailing
// partial segment is the object's legitimate short tail or the result of a stream
// that died early.
func (c *DiskCache) NewSegmentStreamWriter(cacheKey, version string, expectedSize int64) *SegmentStreamWriter {
	return &SegmentStreamWriter{cache: c, cacheKey: cacheKey, version: version, expectedSize: expectedSize}
}

func (w *SegmentStreamWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.disabled {
		return original, nil
	}
	w.written += int64(original)
	for len(p) > 0 {
		if w.file == nil {
			f, err := os.CreateTemp(w.cache.tmpDir(), ".stream-segment-*")
			if err != nil {
				w.disable()
				return original, nil
			}
			w.file = f
			w.tmpName = f.Name()
		}
		room := w.cache.segmentSize - w.inSegment
		n := int64(len(p))
		if n > room {
			n = room
		}
		if _, err := w.file.Write(p[:n]); err != nil {
			w.disable()
			return original, nil
		}
		w.inSegment += n
		p = p[n:]
		if w.inSegment == w.cache.segmentSize {
			if err := w.commitCurrent(); err != nil {
				w.disable()
				return original, nil
			}
		}
	}
	return original, nil
}

func (w *SegmentStreamWriter) Close() error {
	if w.disabled || w.file == nil {
		return nil
	}
	// Only the object's final segment may be shorter than segmentSize. If the
	// stream ended early -- client disconnect, truncated origin response -- the
	// pending segment is incomplete, and committing it would make HasSegment
	// report it as cached while it serves fewer bytes than the Range promises.
	if w.written != w.expectedSize {
		w.disable()
		return fmt.Errorf("incomplete object stream: got %d bytes, expected %d", w.written, w.expectedSize)
	}
	return w.commitCurrent()
}

func (w *SegmentStreamWriter) commitCurrent() error {
	if w.file == nil {
		return nil
	}
	size := w.inSegment
	if err := w.file.Close(); err != nil {
		return err
	}
	blockDir, final := w.cache.segmentLocation(w.cacheKey, w.version, w.index)
	tmp := w.tmpName
	w.file = nil
	w.tmpName = ""
	w.inSegment = 0
	w.index++
	return w.cache.commitSegment(tmp, blockDir, final, size)
}

func (w *SegmentStreamWriter) disable() {
	w.disabled = true
	if w.file != nil {
		_ = w.file.Close()
		_ = os.Remove(w.tmpName)
		w.file = nil
	}
}
