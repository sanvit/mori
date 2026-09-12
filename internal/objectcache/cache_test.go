package objectcache

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiskEvictsWholeBlockWithPartialSegments(t *testing.T) {
	// 8-byte block / 2-byte segment. The disk can hold 4 bytes total.
	cache, err := NewDiskCache(t.TempDir(), 4, 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	key, version := "object", "v1"

	// Two segments in block 0 consume the whole 4-byte budget.
	if err := cache.StoreSegmentStreaming(key, version, 0, 2, bytes.NewBufferString("ab"), nil); err != nil {
		t.Fatal(err)
	}
	if err := cache.StoreSegmentStreaming(key, version, 1, 2, bytes.NewBufferString("cd"), nil); err != nil {
		t.Fatal(err)
	}
	_, first := cache.segmentLocation(key, version, 0)
	_, second := cache.segmentLocation(key, version, 1)
	if _, err := os.Stat(first); err != nil {
		t.Fatal("expected first segment in partial block 0 to be cached")
	}
	if _, err := os.Stat(second); err != nil {
		t.Fatal("expected partial block 0 to be cached")
	}

	// Segment 4 belongs to block 1. Adding it exceeds the disk budget, so the
	// least-recently-used management block (block 0) must be removed as a unit.
	if err := cache.StoreSegmentStreaming(key, version, 4, 2, bytes.NewBufferString("ij"), nil); err != nil {
		t.Fatal(err)
	}
	if cache.HasSegment(key, version, 0) || cache.HasSegment(key, version, 1) {
		t.Fatal("expected all cached segments from evicted block 0 to be removed")
	}
	if !cache.HasSegment(key, version, 4) {
		t.Fatal("expected segment in block 1 to remain cached")
	}
}

func TestProbationaryScanDoesNotEvictProtectedBlocks(t *testing.T) {
	// Five one-segment blocks fit. Four repeatedly used blocks occupy the 80%
	// protected tier and a sequential scan churns only the probation slot.
	cache, err := NewDiskCache(t.TempDir(), 20, 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	for idx := int64(0); idx < 4; idx++ {
		if err := cache.StoreSegmentStreaming("hot", "v1", idx, 4, bytes.NewBufferString("hot!"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for idx := int64(0); idx < 4; idx++ {
		if !cache.HasSegment("hot", "v1", idx) {
			t.Fatalf("hot segment %d missing before scan", idx)
		}
	}

	for idx := int64(0); idx < 8; idx++ {
		if err := cache.StoreSegmentStreaming("scan", "v1", idx, 4, bytes.NewBufferString("scan"), nil); err != nil {
			t.Fatal(err)
		}
	}
	for idx := int64(0); idx < 4; idx++ {
		if !cache.HasSegment("hot", "v1", idx) {
			t.Fatalf("sequential scan evicted protected block %d", idx)
		}
	}
	if !cache.HasSegment("scan", "v1", 7) {
		t.Fatal("newest probationary scan block should remain")
	}
}

func TestTruncatedStreamDoesNotCommitPartialTailSegment(t *testing.T) {
	cache, err := NewDiskCache(t.TempDir(), 1<<20, 8, 2)
	if err != nil {
		t.Fatal(err)
	}

	// The object is 10 bytes, but the stream dies after 5 (client disconnect or a
	// truncated origin response). Segments 0 and 1 are whole and may be kept; the
	// pending third segment holds a single byte and must be thrown away.
	w := cache.NewSegmentStreamWriter("key", "v1", 10)
	if _, err := io.Copy(w, strings.NewReader("abcde")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil {
		t.Fatal("expected Close to report the incomplete stream")
	}
	if !cache.HasSegment("key", "v1", 0) || !cache.HasSegment("key", "v1", 1) {
		t.Fatal("complete segments should still be cached")
	}
	if cache.HasSegment("key", "v1", 2) {
		t.Fatal("partial tail segment must not be committed as a complete segment")
	}
}

func TestCompleteStreamCommitsShortFinalSegment(t *testing.T) {
	cache, err := NewDiskCache(t.TempDir(), 1<<20, 8, 4)
	if err != nil {
		t.Fatal(err)
	}

	// A 6-byte object with 4-byte segments legitimately ends in a 2-byte segment.
	w := cache.NewSegmentStreamWriter("key", "v1", 6)
	if _, err := io.Copy(w, strings.NewReader("abcdef")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := cache.OpenSegment("key", "v1", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil || string(b) != "ef" {
		t.Fatalf("final segment = %q err=%v, want %q", b, err, "ef")
	}
}

func TestLateMetadataWriteCannotReplaceNewerLookup(t *testing.T) {
	cache, err := NewDiskCache(t.TempDir(), 1<<20, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	newer := &ObjectMeta{CacheKey: "key", Version: "v2", ETag: `"v2"`, CachedAt: base.Add(time.Second), ExpiresAt: base.Add(time.Hour)}
	if err := cache.SaveMeta(newer); err != nil {
		t.Fatal(err)
	}
	lateNegative := &ObjectMeta{CacheKey: "key", StatusCode: 404, CachedAt: base, ExpiresAt: base.Add(time.Minute)}
	if err := cache.SaveMeta(lateNegative); err != nil {
		t.Fatal(err)
	}
	if err := cache.ExpireMetaIfVersion("key", "v1"); err != nil {
		t.Fatal(err)
	}
	got, err := cache.LoadMeta("key")
	if err != nil || got == nil {
		t.Fatalf("load meta: meta=%v err=%v", got, err)
	}
	if got.Version != "v2" || got.isNegative() || !got.ExpiresAt.Equal(newer.ExpiresAt) {
		t.Fatalf("new metadata was overwritten: %+v", got)
	}
}

func TestRestartRemovesExpiredNegativeMetadata(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewDiskCache(dir, 1<<20, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	m := &ObjectMeta{CacheKey: "missing", StatusCode: 404, CachedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(-time.Second)}
	if err := cache.SaveMeta(m); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDiskCache(dir, 1<<20, 8, 4); err != nil {
		t.Fatal(err)
	}
	if got, err := cache.LoadMeta(m.CacheKey); err != nil || got != nil {
		t.Fatalf("expired negative metadata survived restart: meta=%v err=%v", got, err)
	}
}
