package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// putNames stores objects by name alone, in one statement, for the list tests.
func putNames(t *testing.T, pool *pgxpool.Pool, project int64, names []string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO storage.objects (project_number, name, generation, blob_key, size, md5, crc32c, time_created, updated)
		SELECT $1::bigint, n, 1, 'p/' || $1::bigint || '/' || gen_random_uuid(), 0, decode(repeat('00', 16), 'hex'), 0, now(), now()
		FROM unnest($2::text[]) AS n`, project, names)
	if err != nil {
		t.Fatal(err)
	}
}

// entry is one result of a list: a prefix or an object's name.
type entry struct {
	name   string
	prefix bool
}

func (e entry) String() string {
	if e.prefix {
		return e.name + "(prefix)"
	}
	return e.name
}

// modelList is what a list returns over every page, computed from the names alone: GCS's
// meaning (B §4.2, ADR-022 §13), with /v0 leaving out what has an empty segment.
func modelList(names []string, o ListOptions) []entry {
	names = slices.Clone(names)
	slices.Sort(names)
	var out []entry
	lastPrefix := ""
	for _, n := range names {
		if !strings.HasPrefix(n, o.Prefix) || (o.StartOffset != "" && n < o.StartOffset) || (o.EndOffset != "" && n >= o.EndOffset) {
			continue
		}
		if o.Delimiter != "" {
			if i := strings.Index(n[len(o.Prefix):], o.Delimiter); i >= 0 {
				p := n[:len(o.Prefix)+i+len(o.Delimiter)]
				if p != lastPrefix {
					lastPrefix = p
					if o.Surface != SurfaceFirebase || !prefixHasEmptySegment(p) {
						out = append(out, entry{p, true})
					}
				}
				continue
			}
		}
		if o.Surface == SurfaceFirebase && hasEmptySegment(n) {
			continue
		}
		out = append(out, entry{n, false})
	}
	return out
}

// listAll pages through a list as listAll does, and returns every entry in order.
func listAll(t *testing.T, s *Service, b Bucket, o ListOptions) ([]entry, int) {
	t.Helper()
	var out []entry
	pages := 0
	for {
		page, err := s.List(context.Background(), b, admin, o)
		if err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
		pages++
		if n := len(page.Prefixes) + len(page.Objects); o.MaxResults > 0 && n > o.MaxResults {
			t.Fatalf("%+v: a page of %d", o, n)
		}
		// Prefixes and objects come in one byte order: merge them as a client would see them.
		var got []entry
		for _, p := range page.Prefixes {
			got = append(got, entry{p, true})
		}
		for _, obj := range page.Objects {
			if obj.Bucket != b.Name {
				t.Fatalf("an object of bucket %q", obj.Bucket)
			}
			got = append(got, entry{obj.Name, false})
		}
		slices.SortFunc(got, func(a, b entry) int { return strings.Compare(a.name, b.name) })
		out = append(out, got...)
		if page.NextPageToken == "" {
			return out, pages
		}
		if pages > 10000 {
			t.Fatalf("%+v: does not end", o)
		}
		o.PageToken = page.NextPageToken
	}
}

func TestListAgainstTheModel(t *testing.T) {
	f := newObjectsFixture(t)
	names := []string{
		"a", "a/", "a/b", "a/b/c", "a/b/d", "a/c", "a//x", "a//y/z", "a/b//c", "/root", "/", "//x",
		"b.txt", "b/x", "b/y/z", "b/y/zz", "b0", "c", "photos/2026/01/a.png", "photos/2026/01/b.png", "photos/2026/02/c.png",
		"photos/cover.png", "\u00e9/1", "\u00e9/2", "\u00e9t\u00e9", "x\u00e9y\u00e9z", "z\U0010FFFF/1", "z\U0010FFFFa", "z\U0010FFFF\U0010FFFF",
		"naïve 'q' 1+1%;x.txt", "a b/c d",
		// CT "should not include show invalid prefixes and items".
		"listAll//foo", "listAll/bar//", "listAll/baz//qux",
	}
	putNames(t, f.pool, testProject, names)
	for _, sf := range []Surface{SurfaceFirebase, SurfaceGCS} {
		for _, prefix := range []string{"", "a/", "a", "b/", "/", "a//", "\u00e9/", "z\U0010FFFF", "photos/2026/", "nothing/", "listAll/"} {
			for _, delim := range []string{"", "/", "\u00e9", "/2"} {
				offsets := [][2]string{{"", ""}}
				if sf == SurfaceGCS {
					offsets = append(offsets, [2]string{"a/b", ""}, [2]string{"", "b"}, [2]string{"a", "b/y"}, [2]string{"photos/2026/01/b", "photos/3"})
				}
				for _, off := range offsets {
					for _, max := range []int{1, 2, 3, 0} {
						o := ListOptions{Prefix: prefix, Delimiter: delim, StartOffset: off[0], EndOffset: off[1], MaxResults: max, Surface: sf}
						want := modelList(names, o)
						got, _ := listAll(t, f.s, f.b, o)
						if !slices.Equal(got, want) {
							t.Errorf("%+v:\n got  %v\n want %v", o, got, want)
						}
					}
				}
			}
		}
	}
}

func TestList(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	tok := newDownloadToken()
	f.put(t, "p/a.png", seed{body: "a", tokens: []string{tok}, metadata: map[string]string{"k": "v"}})
	f.put(t, "p/b.png", seed{body: "bb"})
	f.put(t, "p/sub/c.png", seed{body: "c"})

	// Arrays are present even when empty, and the last page has no token.
	page, err := f.s.List(ctx, f.b, admin, ListOptions{Prefix: "none/", Delimiter: "/", Surface: SurfaceFirebase})
	if err != nil || page.Prefixes == nil || page.Objects == nil || page.NextPageToken != "" {
		t.Fatalf("an empty list: %+v, %v", page, err)
	}
	// Objects carry their whole metadata, tokens included, for GCS's items.
	page, err = f.s.List(ctx, f.b, admin, ListOptions{Prefix: "p/", Delimiter: "/", Surface: SurfaceGCS})
	if err != nil || !slices.Equal(page.Prefixes, []string{"p/sub/"}) || len(page.Objects) != 2 || page.NextPageToken != "" {
		t.Fatalf("p/: %+v, %v", page, err)
	}
	if a := page.Objects[0]; a.Name != "p/a.png" || a.Size != 1 || !slices.Equal(a.DownloadTokens, []string{tok}) || a.Metadata["k"] != "v" {
		t.Errorf("an item: %+v", a)
	}
	// The rules decide a list on the prefix, before anything is read.
	f.allow(func(op ruleOp) error {
		if op.Method != rules.MethodList || !op.List || op.Name != "p/" || op.Resource != nil || op.Incoming != nil {
			t.Errorf("the rules saw %+v", op)
		}
		return ErrPermissionDenied
	})
	if _, err := f.s.List(ctx, f.b, alice, ListOptions{Prefix: "p/", Delimiter: "/", Surface: SurfaceFirebase}); err != ErrPermissionDenied {
		t.Errorf("refused: %v", err)
	}
	f.allow(func(ruleOp) error { return nil })
	if page, err := f.s.List(ctx, f.b, alice, ListOptions{Prefix: "p/", Delimiter: "/", Surface: SurfaceFirebase}); err != nil || len(page.Objects) != 2 {
		t.Errorf("allowed: %+v, %v", page, err)
	}

	// maxResults: 0 is the limit, more is cut to it, negative is 400.
	f.s.limits.ListMaxResults = 2
	page, err = f.s.List(ctx, f.b, admin, ListOptions{Surface: SurfaceGCS, MaxResults: 5})
	if err != nil || len(page.Objects) != 2 || page.NextPageToken == "" {
		t.Errorf("over the limit: %+v, %v", page, err)
	}
	page, err = f.s.List(ctx, f.b, admin, ListOptions{Surface: SurfaceGCS})
	if err != nil || len(page.Objects) != 2 {
		t.Errorf("0: %+v, %v", page, err)
	}
	var ie *InvalidError
	if _, err := f.s.List(ctx, f.b, admin, ListOptions{Surface: SurfaceGCS, MaxResults: -1}); !errors.As(err, &ie) {
		t.Errorf("negative: %v", err)
	}
	for _, o := range []ListOptions{{Prefix: "a\x00"}, {Delimiter: "\xff"}, {StartOffset: "\xc3"}, {EndOffset: "\x00"}} {
		if _, err := f.s.List(ctx, f.b, admin, o); !errors.As(err, &ie) {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

// A page token is bound to its list: another bucket, project, prefix, delimiter, offset or
// surface, a forged or truncated token, all are 400, never a list from the start.
func TestListPageTokens(t *testing.T) {
	ctx := context.Background()
	f := newObjectsFixture(t)
	putNames(t, f.pool, testProject, []string{"d/1", "d/2", "d/3", "d/x/1", "d/y/1"})
	o := ListOptions{Prefix: "d/", Delimiter: "/", MaxResults: 2, Surface: SurfaceFirebase}
	page, err := f.s.List(ctx, f.b, admin, o)
	if err != nil || page.NextPageToken == "" {
		t.Fatalf("%+v, %v", page, err)
	}
	tok := page.NextPageToken
	if strings.ContainsAny(tok, "+/=") {
		t.Errorf("the token %q is not URL-safe", tok)
	}
	next := o
	next.PageToken = tok
	if _, err := f.s.List(ctx, f.b, admin, next); err != nil {
		t.Fatalf("the token on its own list: %v", err)
	}
	// The page size may change between pages.
	next.MaxResults = 1
	if _, err := f.s.List(ctx, f.b, admin, next); err != nil {
		t.Errorf("another page size: %v", err)
	}
	other := []struct {
		name string
		b    Bucket
		o    ListOptions
	}{
		{"another project", Bucket{testProject + 1, f.b.Name}, next},
		{"another bucket", Bucket{testProject, "other.aruma.test"}, next},
		{"another prefix", f.b, ListOptions{Prefix: "d/x/", Delimiter: "/", Surface: SurfaceFirebase, PageToken: tok}},
		{"no delimiter", f.b, ListOptions{Prefix: "d/", Surface: SurfaceFirebase, PageToken: tok}},
		{"GCS", f.b, ListOptions{Prefix: "d/", Delimiter: "/", Surface: SurfaceGCS, PageToken: tok}},
		{"a start offset", f.b, ListOptions{Prefix: "d/", Delimiter: "/", Surface: SurfaceFirebase, StartOffset: "d/2", PageToken: tok}},
		{"an end offset", f.b, ListOptions{Prefix: "d/", Delimiter: "/", Surface: SurfaceFirebase, EndOffset: "d/z", PageToken: tok}},
	}
	for _, c := range other {
		if _, err := f.s.List(ctx, c.b, admin, c.o); err != ErrInvalidPageToken {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	raw := []byte(tok)
	for i, bad := range []string{"", "x", "null", tok[:len(tok)-1], tok + "A", tok[1:], strings.Repeat("A", 2000),
		string(append(slices.Clone(raw[:4]), append([]byte{raw[4] ^ 1}, raw[5:]...)...))} {
		if bad == "" {
			continue
		}
		o := next
		o.PageToken = bad
		if _, err := f.s.List(ctx, f.b, admin, o); err != ErrInvalidPageToken {
			t.Errorf("forged token %d: %v", i, err)
		}
	}
	// A token from another key (another server's master key) fails too.
	f.s.keys = otherKeys(t)
	if _, err := f.s.List(ctx, f.b, admin, next); err != ErrInvalidPageToken {
		t.Errorf("another key: %v", err)
	}
}

// countingDB counts the list's seeks.
type countingDB struct {
	queries.DBTX
	seeks atomic.Int64
}

func (c *countingDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "name: ListObjects") {
		c.seeks.Add(1)
	}
	return c.DBTX.Query(ctx, sql, args...)
}

// On a million names, a page costs at most maxResults+1 seeks of the index, whatever the
// layout: a thousand folders of a thousand files, listed at the root, inside a folder, and
// without a delimiter (ADR-022 §13).
func TestListSeeksOnAMillionNames(t *testing.T) {
	if testing.Short() {
		t.Skip("a million rows")
	}
	ctx := context.Background()
	f := newObjectsFixture(t)
	start := time.Now()
	// The blob keys' constraints are not what the walk reads: this throwaway database goes
	// without them, so the million rows go in faster.
	for _, sql := range []string{
		"ALTER TABLE storage.objects DROP CONSTRAINT objects_blob_key_key, DROP CONSTRAINT objects_blob_key_project, DROP CONSTRAINT objects_blob_key_check",
		`INSERT INTO storage.objects (project_number, name, generation, blob_key, size, md5, crc32c, time_created, updated)
		SELECT $1::bigint, format('f%s/%s', lpad((i / 1000)::text, 4, '0'), lpad((i % 1000)::text, 4, '0')), 1,
			'p/0/x', 0, '\x00000000000000000000000000000000'::bytea, 0, now(), now()
		FROM generate_series(0, 999999) AS i`,
	} {
		args := []any{testProject}
		if strings.HasPrefix(sql, "ALTER") {
			args = nil
		}
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.pool.Exec(ctx, "ANALYZE storage.objects"); err != nil {
		t.Fatal(err)
	}
	t.Logf("a million rows in %v", time.Since(start))

	// The seek is an index range scan, not a scan of the bucket.
	var plan []string
	rows, err := f.pool.Query(ctx, `EXPLAIN SELECT * FROM storage.objects WHERE project_number = $1 AND name >= $2 AND name < $3
		ORDER BY name LIMIT 1001`, testProject, "f0500/", "f05000")
	if err == nil {
		plan, err = pgx.CollectRows(rows, pgx.RowTo[string])
	}
	if err != nil || !strings.Contains(strings.Join(plan, "\n"), "Index Scan using objects_pkey") {
		t.Fatalf("the plan:\n%s\n%v", strings.Join(plan, "\n"), err)
	}

	db := &countingDB{DBTX: f.pool}
	f.s.reads = queries.New(db)
	for _, c := range []struct {
		o         ListOptions
		prefixes  int
		objects   int
		maxSeeks  int64
		nextToken bool
	}{
		{ListOptions{Delimiter: "/", MaxResults: 1000, Surface: SurfaceFirebase}, 1000, 0, 1001, false},
		{ListOptions{Delimiter: "/", MaxResults: 10, Surface: SurfaceFirebase}, 10, 0, 11, true},
		{ListOptions{Prefix: "f0500/", Delimiter: "/", MaxResults: 1000, Surface: SurfaceFirebase}, 0, 1000, 6, false},
		{ListOptions{Prefix: "f0500/", Delimiter: "/", MaxResults: 100, Surface: SurfaceFirebase}, 0, 100, 3, true},
		{ListOptions{MaxResults: 1000, Surface: SurfaceGCS}, 0, 1000, 1, true},
		{ListOptions{Delimiter: "/", MaxResults: 5, StartOffset: "f0998/05", Surface: SurfaceGCS}, 2, 0, 3, false},
	} {
		db.seeks.Store(0)
		begin := time.Now()
		page, err := f.s.List(ctx, f.b, admin, c.o)
		if err != nil {
			t.Fatal(err)
		}
		seeks := db.seeks.Load()
		if len(page.Prefixes) != c.prefixes || len(page.Objects) != c.objects || (page.NextPageToken != "") != c.nextToken {
			t.Errorf("%+v: %d prefixes, %d objects, token %v", c.o, len(page.Prefixes), len(page.Objects), page.NextPageToken != "")
		}
		if seeks > c.maxSeeks || seeks > int64(c.o.MaxResults)+1 {
			t.Errorf("%+v: %d seeks, want at most %d", c.o, seeks, c.maxSeeks)
		}
		t.Logf("%+v: %d seeks in %v", c.o, seeks, time.Since(begin))
	}
	// The next page of the folders continues after the last one.
	page, err := f.s.List(ctx, f.b, admin, ListOptions{Delimiter: "/", MaxResults: 10, Surface: SurfaceFirebase})
	if err != nil {
		t.Fatal(err)
	}
	db.seeks.Store(0)
	page, err = f.s.List(ctx, f.b, admin, ListOptions{Delimiter: "/", MaxResults: 10, Surface: SurfaceFirebase, PageToken: page.NextPageToken})
	if err != nil || len(page.Prefixes) != 10 || page.Prefixes[0] != "f0010/" || db.seeks.Load() > 11 {
		t.Errorf("the second page: %v, %v, %d seeks", page.Prefixes, err, db.seeks.Load())
	}
}
