package core

import (
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// scanAll pages through a scan to its end, PageSize at a time.
func scanAll(t *testing.T, s *PGStore, r ScanRequest) []string {
	t.Helper()
	var got []string
	for {
		res, err := s.Scan(t.Context(), testDB, r)
		if err != nil {
			t.Fatalf("scan %+v: %v", r, err)
		}
		got = append(got, paths(res.Documents)...)
		if res.Next == nil {
			return got
		}
		r.From, r.FromInclusive = res.Next, false
	}
}

// Scan's scopes: a collection, a collection group database-wide or under a document, every
// collection directly under a document (kindless) or anywhere below it, paged, at a read_time,
// and past names longer than the btree's bound.
func TestScan(t *testing.T) {
	s, _ := newStore(t, Options{})
	var writes []Write
	for _, p := range []string{"tree/a", "tree/a/other/z", "tree/a/sub/x", "tree/a/sub/x/deep/y", "tree/b", "tree/m/kids/k1",
		"tree2/q", "tree/a/sub/x/sub/w", "sub/top"} {
		writes = append(writes, set(p, doc("v", value.Int(1))))
	}
	r1 := mustCommit(t, s, writes...)
	mustCommit(t, s, set("tree/c", doc()), Write{Kind: WriteDelete, Path: ParsePath("tree/b")})

	for _, c := range []struct {
		name string
		r    ScanRequest
		want []string
	}{
		{"collection", ScanRequest{CollectionID: "tree"}, []string{"tree/a", "tree/c"}},
		{"group", ScanRequest{CollectionID: "sub", AllDescendants: true}, []string{"sub/top", "tree/a/sub/x", "tree/a/sub/x/sub/w"}},
		{"group under a document", ScanRequest{Parent: ParsePath("tree/a"), CollectionID: "sub", AllDescendants: true},
			[]string{"tree/a/sub/x", "tree/a/sub/x/sub/w"}},
		{"kindless children", ScanRequest{Parent: ParsePath("tree/a")}, []string{"tree/a/other/z", "tree/a/sub/x"}},
		{"kindless descendants", ScanRequest{Parent: ParsePath("tree/a"), AllDescendants: true},
			[]string{"tree/a/other/z", "tree/a/sub/x", "tree/a/sub/x/deep/y", "tree/a/sub/x/sub/w"}},
		{"kindless descendants of the root", ScanRequest{AllDescendants: true},
			[]string{"sub/top", "tree/a", "tree/a/other/z", "tree/a/sub/x", "tree/a/sub/x/deep/y", "tree/a/sub/x/sub/w", "tree/c",
				"tree/m/kids/k1", "tree2/q"}},
		{"paged", ScanRequest{AllDescendants: true, PageSize: 2, From: ParsePath("tree/a/sub/x"), FromInclusive: true},
			[]string{"tree/a/sub/x", "tree/a/sub/x/deep/y", "tree/a/sub/x/sub/w", "tree/c", "tree/m/kids/k1", "tree2/q"}},
		{"at a read_time", ScanRequest{CollectionID: "tree", ReadOptions: ReadOptions{ReadTime: r1.CommitTime}}, []string{"tree/a", "tree/b"}},
		{"group at a read_time", ScanRequest{CollectionID: "tree", AllDescendants: true, ReadOptions: ReadOptions{ReadTime: r1.CommitTime}},
			[]string{"tree/a", "tree/b"}},
	} {
		if got := scanAll(t, s, c.r); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}

	// Names past the btree's bound sort and page by their whole name.
	long := strings.Repeat("x", 1499)
	var want []string
	writes = nil
	for _, suffix := range []string{"e", "a", "d", "b", "c"} {
		p := "l/" + long + "p/s/" + long + suffix
		writes = append(writes, set(p, doc()))
		want = append(want, p)
	}
	mustCommit(t, s, writes...)
	slices.Sort(want)
	for _, r := range []ScanRequest{
		{CollectionID: "s", AllDescendants: true, PageSize: 2},
		{Parent: ParsePath("l/" + long + "p"), AllDescendants: true, PageSize: 2},
		{Parent: ParsePath("l/" + long + "p"), CollectionID: "s", PageSize: 2},
	} {
		if got := scanAll(t, s, r); !slices.Equal(got, want) {
			t.Errorf("long names %+v: %d documents, want %d in order", r, len(got), len(want))
		}
	}
}
