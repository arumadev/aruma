package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// listFolderDecisions is the most folders one page of a list without the "/" delimiter asks the
// rules about, besides the listed one; a page that needs more ends early with a next page token.
const listFolderDecisions = 100

// List returns one page of a bucket's objects and prefixes after the rules allow a list, walking
// the index with one seek per prefix, so a page costs at most MaxResults+1 index lookups
// (ADR-022 §13).
//
// The walk reads the names in [from, before) in byte order. A name with the delimiter after the
// prefix gives a prefix, and the next seek starts past every name under it (pastPrefix), so a
// bucket of a million names under a hundred folders costs a hundred and one seeks. On /v0,
// names and prefixes with an empty segment are left out, but count against the page as if
// returned: a page examines at most MaxResults entries, and may come back short, even empty,
// with a next page token. Every seek but the last examines an entry, which bounds them by
// MaxResults+1; the extra row a seek asks for tells whether a next page exists, without another
// one. Without a delimiter a seek reads the rest of the page; with one, it reads listBatch rows
// at first and twice as many after each seek that found no prefix, so a folder's names past its
// first few are not read for nothing. Prefixes and objects share the page's MaxResults in one
// order, as GCS pages them. Objects carry no tokens on /v0, whose items are a name and a bucket.
//
// On /v0, when the delimiter is not "/", a caller the rules govern sees a name in a deeper
// folder only if the rules allow a list of that folder too: the first rules decision covers the
// children of the prefix's folder only.
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
	ctx, err := s.prepareRules(ctx, b, p)
	if err != nil {
		return ListPage{}, err
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

	// The rules decided on the children of the prefix's folder alone (rulePaths). With "/" as
	// the delimiter, as the SDKs list, deeper names fold into their folder's prefix. Without it,
	// or with another delimiter, they come back whole, from folders whose own rules were never
	// asked. So for a caller the rules govern, a name in a deeper folder is shown only when a
	// list of that folder is allowed too: each such folder is decided once a page, and a name
	// the rules refuse is left out and counted against the page as examined. Rules that allow
	// every depth (match /{all=**}) list as before.
	governed := firebase && p.Kind != PrincipalAdmin && o.Delimiter != "/"
	listed := o.Prefix[:strings.LastIndexByte(o.Prefix, '/')+1]
	folders := map[string]bool{listed: true}

	n, batch := 0, listBatch // n: the entries examined, returned or left out
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
			if governed {
				dir := r.Name[:strings.LastIndexByte(r.Name, '/')+1]
				allowed, decided := folders[dir]
				if !decided {
					if len(folders) > listFolderDecisions {
						// Enough rules decisions for one page: the next one goes on from here.
						page.NextPageToken = s.encodePageToken(b, o, last)
						return page, nil
					}
					switch err := s.authorize(ctx, b, p, ruleOp{Method: rules.MethodList, Name: dir, List: true, Surface: o.Surface}); {
					case err == nil:
						allowed = true
					case !errors.Is(err, ErrPermissionDenied):
						return ListPage{}, err
					}
					folders[dir] = allowed
				}
				if !allowed {
					last = pageCursor{last: r.Name}
					from = after(r.Name)
					n++
					continue
				}
			}
			if o.Delimiter != "" {
				if i := strings.Index(r.Name[len(o.Prefix):], o.Delimiter); i >= 0 {
					pre := r.Name[:len(o.Prefix)+i+len(o.Delimiter)]
					last = pageCursor{last: pre, prefix: true}
					if !firebase || !prefixHasEmptySegment(pre) {
						page.Prefixes = append(page.Prefixes, pre)
					}
					n++
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
			n++
			if firebase && hasEmptySegment(r.Name) {
				continue
			}
			page.Objects = append(page.Objects, s.listObject(b, r, o.Surface))
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

// listObject returns a listed object. /v0 items show a name and a bucket alone, so their tokens
// stay sealed; GCS items show them, opened here. One row that does not convert never fails the
// page: what does not open is logged and left out of that item.
func (s *Service) listObject(b Bucket, r queries.StorageObject, sf Surface) Object {
	o, err := rowObject(b, r)
	if err != nil {
		s.log.Error("storage: a listed object's metadata does not read", "project", b.Project, "name", r.Name, "err", err)
	}
	if sf == SurfaceFirebase {
		return o
	}
	tokens, err := s.openTokens(b.Project, r.Name, r.DownloadTokens)
	if err != nil {
		s.log.Error("storage: a listed object's download tokens do not open", "project", b.Project, "name", r.Name, "err", err)
		return o
	}
	o.DownloadTokens = tokens
	return o
}
