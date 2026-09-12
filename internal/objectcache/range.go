package objectcache

import (
	"fmt"
	"strconv"
	"strings"
)

type ByteRange struct {
	Start int64
	End   int64
}

func parseSingleRange(s string, size int64) (ByteRange, bool, error) {
	if s == "" {
		return ByteRange{}, false, nil
	}
	if !strings.HasPrefix(strings.ToLower(s), "bytes=") {
		return ByteRange{}, false, fmt.Errorf("unsupported range unit")
	}
	spec := strings.TrimSpace(s[len("bytes="):])
	if strings.Contains(spec, ",") {
		return ByteRange{}, false, fmt.Errorf("multiple")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return ByteRange{}, false, fmt.Errorf("invalid range")
	}
	if size <= 0 {
		return ByteRange{}, false, fmt.Errorf("invalid size")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return ByteRange{}, false, fmt.Errorf("invalid suffix range")
		}
		if suffix > size {
			suffix = size
		}
		return ByteRange{Start: size - suffix, End: size - 1}, true, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 || start >= size {
		return ByteRange{}, false, fmt.Errorf("invalid start")
	}
	end := size - 1
	if parts[1] != "" {
		end, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return ByteRange{}, false, fmt.Errorf("invalid end")
		}
		if end >= size {
			end = size - 1
		}
	}
	return ByteRange{Start: start, End: end}, true, nil
}
