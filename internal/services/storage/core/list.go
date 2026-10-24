package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// List returns one page of a bucket's objects and prefixes after the rules allow a list, walking
// the index with one seek per prefix, so a page costs at most MaxResults+1 index lookups
// (ADR-022 §13).
//
// The walk reads the names in [from, before) in byte order. A name with the delimiter after the
// prefix gives a prefix, and the next seek starts past every name under it (pastPrefix), so a
// bucket of a million names under a hundred folders costs a hundred and one seeks. Every seek
// but the last returns an entry (or, on /v0, skips one), which bounds them by MaxResults+1;
// the extra row a seek asks for tells whether a next page exists, without another one. Without
// a delimiter a seek reads the rest of the page; with one, it reads listBatch rows at first and
// twice as many after each seek that found no prefix, so a folder's names past its first few
// are not read for nothing. Prefixes and objects share the page's MaxResults in one order, as
// GCS pages them. On /v0, names and prefixes with an empty segment are left out, and the next
// seek still starts past them.
func (s *Service) List(ctx context.Context, b Bucket, p Principal, o ListOptions) (ListPage, error) {
	for _, f := range []struct{ name, v string }{
		{"prefix", o.Prefix}, {"delimiter", o.Delimiter}, {"startOffset", o.StartOffset}, {"endOffset", o.EndOffset},
	} {
		if err := checkListText(f.name, f.v); err != nil {
			return ListPage{}, err
		}
	}
	size := o.MaxResults
	switch {
	case size < 0:
		return ListPage{}, invalid("maxResults must not be negative.")
	case size == 0 || size > s.limits.ListMaxResults:
		size = s.limits.ListMaxResults
	}
	var cur *pageCursor
	if o.PageToken != "" {
		c, err := s.decodePageToken(b, o)
		if err != nil {
			return ListPage{}, err
		}
		cur = &c
	}
	if err := s.authorize(ctx, b, p, ruleOp{Method: rules.MethodList, Name: o.Prefix, List: true, Surface: o.Surface}); err != nil {
		return ListPage{}, err
	}

	page := ListPage{Prefixes: []string{}, Objects: []Object{}}
	firebase := o.Surface == SurfaceFirebase
	if firebase && (strings.HasPrefix(o.Prefix, "/") || strings.Contains(o.Prefix, "//")) {
		return page, nil // every name under the prefix has an empty segment
	}
	from, before := max(o.Prefix, o.StartOffset), maxName
	if cur != nil {
		next, ok := cur.next()
		if !ok {
			return page, nil
		}
		from = max(from, next)
	}
	if o.Prefix != "" {
		if up, ok := pastPrefix(o.Prefix); ok {
			before = min(before, up)
		}
	}
	if o.EndOffset != "" {
		before = min(before, o.EndOffset)
	}

	n, batch := 0, listBatch
	var last pageCursor
	for from < before {
		limit := size - n + 1
		if o.Delimiter != "" {
			limit = min(limit, batch)
		}
		rows, err := s.reads.ListObjects(ctx, queries.ListObjectsParams{
			ProjectNumber: b.Project, FromName: from, BeforeName: before, MaxRows: int32(limit),
		})
		if err != nil {
			return ListPage{}, fmt.Errorf("%w: list: %w", ErrUnavailable, err)
		}
		jumped := false
		for _, r := range rows {
			if n == size {
				page.NextPageToken = s.encodePageToken(b, o, last)
				return page, nil
			}
			if o.Delimiter != "" {
				if i := strings.Index(r.Name[len(o.Prefix):], o.Delimiter); i >= 0 {
					pre := r.Name[:len(o.Prefix)+i+len(o.Delimiter)]
					last = pageCursor{last: pre, prefix: true}
					if !firebase || !prefixHasEmptySegment(pre) {
						page.Prefixes = append(page.Prefixes, pre)
						n++
					}
					next, ok := pastPrefix(pre)
					if !ok {
						return page, nil
					}
					from, jumped = next, true
					break
				}
			}
			last = pageCursor{last: r.Name}
			from = after(r.Name)
			if firebase && hasEmptySegment(r.Name) {
				continue
			}
			obj, err := s.objectFromRow(b, r)
			if err != nil {
				return ListPage{}, err
			}
			page.Objects = append(page.Objects, obj)
			n++
		}
		switch {
		case jumped:
			batch = listBatch
		case len(rows) < limit:
			return page, nil // the range holds no more names
		default:
			batch *= 2
		}
	}
	return page, nil
}

// listBatch is the rows a seek reads at first when the list has a delimiter.
const listBatch = 16
