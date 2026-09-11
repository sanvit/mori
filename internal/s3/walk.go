package s3

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mori-s3/internal/backend"
)

// Walk visits every descendant object of prefix. It is ONLY used for a
// selected ZIP folder. Unlike List it deliberately omits delimiter so S3
// returns every descendant object key. Iterate pages, not directory depth;
// object bodies are never read here.
func (o *Origin) Walk(ctx context.Context, prefix string, visit func(backend.Object) error) error {
	fullPrefix := o.cfg.Prefix + prefix
	cursor := ""
	seenCursors := map[string]bool{}
	maxPages := 2*o.cfg.ZipMaxFiles + 1 // Also bounds pathological empty pages.
	for page := 0; page < maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{"list-type": {"2"}, "prefix": {fullPrefix}, "max-keys": {"1000"}, "encoding-type": {"url"}}
		if cursor != "" {
			q.Set("continuation-token", cursor)
		}
		resp, err := o.Request(ctx, http.MethodGet, "", q, nil, false)
		if err != nil {
			return err
		}
		var x struct {
			XMLName  xml.Name `xml:"ListBucketResult"`
			Contents []struct {
				Key, LastModified, ETag string
				Size                    *int64
			}
			CommonPrefixes        []struct{ Prefix string }
			NextContinuationToken string
			IsTruncated           bool
			EncodingType          string
		}
		err = func() error {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return ReadError(resp)
			}
			return xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&x)
		}()
		if err != nil {
			return err
		}
		if len(x.CommonPrefixes) != 0 || len(x.Contents) > 1000 {
			return fmt.Errorf("invalid recursive S3 listing")
		}
		for _, obj := range x.Contents {
			key := obj.Key
			if x.EncodingType == "url" {
				key, err = url.PathUnescape(key)
				if err != nil {
					return err
				}
			}
			if !strings.HasPrefix(key, fullPrefix) || obj.Size == nil {
				return fmt.Errorf("S3 recursive listing key escaped prefix or has no size")
			}
			item := backend.Object{Key: strings.TrimPrefix(key, o.cfg.Prefix), Size: *obj.Size,
				ETag: obj.ETag, Directory: strings.HasSuffix(key, "/")}
			item.Modified, _ = time.Parse(time.RFC3339, obj.LastModified)
			if err := visit(item); err != nil {
				return err
			}
		}
		if !x.IsTruncated {
			return nil
		}
		next := x.NextContinuationToken
		if next == "" || len(next) > 8192 || seenCursors[next] {
			return fmt.Errorf("upstream recursive pagination token is missing or repeated")
		}
		seenCursors[next] = true
		cursor = next
	}
	return fmt.Errorf("upstream recursive pagination exceeded safety limit")
}
