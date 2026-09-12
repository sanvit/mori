package backend

import (
	"strings"
	"testing"
	"time"
)

func TestVirtualETagMetadataAndScope(t *testing.T) {
	stamp := time.Date(2026, 9, 12, 1, 2, 3, 456, time.UTC)
	tag := VersionTag("ftp:host:user:/root", "folder/한글.txt", 42, stamp)
	if !SyntheticETag(tag) || StrongETag(tag) {
		t.Fatal(tag)
	}
	if tag != VersionTag("ftp:host:user:/root", "folder/한글.txt", 42, stamp.In(time.FixedZone("other", 9*3600))) {
		t.Fatal("timezone changed token")
	}
	for _, got := range []string{VersionTag("sftp:host:user:/root", "folder/한글.txt", 42, stamp), VersionTag("ftp:host:user:/root", "else/한글.txt", 42, stamp), VersionTag("ftp:host:user:/root", "folder/한글.txt", 43, stamp), VersionTag("ftp:host:user:/root", "folder/한글.txt", 42, stamp.Add(time.Nanosecond))} {
		if got == tag {
			t.Fatal("metadata collision", got)
		}
	}
	if strings.Contains(tag, "folder") || strings.Contains(tag, "host") {
		t.Fatal("metadata exposed")
	}
	if VersionTag("a\nb", "c", 1, stamp) == VersionTag("a", "b\nc", 1, stamp) {
		t.Fatal("ambiguous field boundaries")
	}
	if VersionTag("s", "k", 1, time.Time{}) == VersionTag("s", "k", 1, time.Unix(0, 0)) {
		t.Fatal("missing timestamp indistinguishable from epoch")
	}
}

func TestETagSyntax(t *testing.T) {
	for _, tag := range []string{`"a"`, `W/"a"`, `""`} {
		if !ValidETag(tag) {
			t.Fatal(tag)
		}
	}
	for _, tag := range []string{"", "unquoted", `w/"a"`, `"a b"`, "\"a\nb\"", `"a"b"`} {
		if ValidETag(tag) {
			t.Fatal(tag)
		}
	}
}
