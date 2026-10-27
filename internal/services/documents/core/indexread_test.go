package core

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// changedSince collects what rd.ChangedSince gives.
func changedSince(rd *IndexRead, seq int64, scope ChangeScope) ([]Document, error) {
	var out []Document
	err := rd.ChangedSince(context.Background(), seq, scope, func(d Document) error {
		out = append(out, d)
		return nil
	})
	return out, err
}

// ChangedSince gives the documents of a scope that the commits after a snapshot changed, once
// each, as they were at that snapshot (Path alone for one created since), and fails when the
// changelog no longer holds those commits.
func TestChangedSince(t *testing.T) {
	s, pool := newStore(t, Options{})
	ctx := t.Context()
	mustCommit(t, s, set("c/a", doc("v", value.Int(1))), set("c/b", doc("v", value.Int(2))), set("d/x", doc()),
		set("c/a/s/y", doc()))
	rd, err := s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	seq := rd.Seq()
	rd.Close(ctx)
	mustCommit(t, s, set("c/a", doc("v", value.Int(10))))
	mustCommit(t, s, set("c/a", doc("v", value.Int(20))), Write{Kind: WriteDelete, Path: ParsePath("c/b")}, set("c/n", doc()),
		set("d/x", doc("v", value.Int(1))), set("c/a/s/y", doc("v", value.Int(1))))

	rd, err = s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close(ctx)
	if rd.Seq() != seq+2 {
		t.Fatalf("seq %d, want %d", rd.Seq(), seq+2)
	}
	got, err := changedSince(rd, seq, ChangeScope{CollectionID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		path string
		v    int64 // -1: absent at seq
	}{{"c/a", 1}, {"c/b", 2}, {"c/n", -1}}
	if len(got) != len(want) {
		t.Fatalf("got %v", paths(got))
	}
	for i, w := range want {
		d := got[i]
		v, ok := d.Fields.Lookup(value.FieldPath{"v"})
		switch {
		case d.Path.String() != w.path:
			t.Errorf("%d: %s, want %s", i, d.Path, w.path)
		case w.v < 0 && d.Exists():
			t.Errorf("%s: exists, want absent", w.path)
		case w.v >= 0 && (!ok || v.IntValue() != w.v):
			t.Errorf("%s: %v, want v %d", w.path, d.Fields, w.v)
		}
	}
	got, err = changedSince(rd, seq, ChangeScope{CollectionID: "s", Group: true})
	if err != nil || len(got) != 1 || got[0].Path.String() != "c/a/s/y" || !got[0].Exists() {
		t.Errorf("the group s: %v %v", paths(got), err)
	}
	if got, err := changedSince(rd, rd.Seq(), ChangeScope{CollectionID: "c"}); err != nil || len(got) != 0 {
		t.Errorf("since the read's own commit: %v %v", paths(got), err)
	}

	// Retention pruned the first commit after seq, an old one: the changes can no longer be read.
	if _, err := pool.Exec(ctx, `UPDATE documents.changelog SET commit_time = commit_time - interval '2 hours'
		WHERE seq <= $1`, seq+1); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM documents.changelog WHERE seq <= $1`, seq+1); err != nil {
		t.Fatal(err)
	}
	rd2, err := s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	defer rd2.Close(ctx)
	if _, err := changedSince(rd2, seq, ChangeScope{CollectionID: "c"}); !errors.Is(err, ErrIndexReadTooLong) {
		t.Errorf("pruned: %v", err)
	}
	if _, err := changedSince(rd2, seq+1, ChangeScope{CollectionID: "c"}); err != nil {
		t.Errorf("after the pruned commit: %v", err)
	}
}

// A scope is a collection's documents or a group's, not those of a nested collection, of another
// parent, or of a collection whose id merely starts or ends like the scope's, with text and
// numeric ids alike.
func TestChangedSinceScope(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	mustCommit(t, s, set("x/seed", doc()))
	rd, err := s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	seq := rd.Seq()
	rd.Close(ctx)
	all := []string{
		"s/top", "s/__id7__", "c/a/s/y", "c/a/s/__id-3__", "c/a/s/y/s/z", "c/a/s/__id9__/s/w", "c/b/s/v",
		"c/a/sx/q", "c/a/xs/q", "c/s", "c/a/s/y/t/u", "c/a/s/s\x01\x00", "c/a/s/\x01\x00\x02", "s/a\x00\x01b",
	}
	var writes []Write
	for _, p := range all {
		writes = append(writes, set(p, doc()))
	}
	mustCommit(t, s, writes...)
	rd, err = s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close(ctx)
	for _, c := range []struct {
		scope ChangeScope
		want  []string
	}{
		{ChangeScope{CollectionID: "s", Group: true}, []string{"c/a/s/s\x01\x00", "c/a/s/\x01\x00\x02", "c/a/s/__id-3__",
			"c/a/s/__id9__/s/w", "c/a/s/y", "c/a/s/y/s/z", "c/b/s/v", "s/__id7__", "s/a\x00\x01b", "s/top"}},
		{ChangeScope{Parent: ParsePath("c/a"), CollectionID: "s"}, []string{"c/a/s/s\x01\x00", "c/a/s/\x01\x00\x02",
			"c/a/s/__id-3__", "c/a/s/y"}},
		{ChangeScope{CollectionID: "s"}, []string{"s/__id7__", "s/a\x00\x01b", "s/top"}},
		{ChangeScope{Parent: ParsePath("c/a/s/__id9__"), CollectionID: "s"}, []string{"c/a/s/__id9__/s/w"}},
		{ChangeScope{CollectionID: "sx", Group: true}, []string{"c/a/sx/q"}},
		{ChangeScope{CollectionID: "t", Group: true}, []string{"c/a/s/y/t/u"}},
	} {
		got, err := changedSince(rd, seq, c.scope)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, w := range c.want {
			want = append(want, ParsePath(w).String())
		}
		var names []string
		for _, d := range got {
			names = append(names, d.Path.String())
		}
		slices.Sort(names)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("%+v: %q, want %q", c.scope, names, want)
		}
	}
}

// What ChangedSince reads does not grow with what changed outside its scope: Postgres keeps
// other collections' images out, and the rows stream, so 24 MiB of large documents changed in
// another collection, and in a nested collection of the same id, cost little.
func TestChangedSinceMemory(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	big := value.String(strings.Repeat("x", 1<<20-1000))
	var writes []Write
	for i := range 12 {
		writes = append(writes, set(fmt.Sprintf("other/d%02d", i), doc("s", big)), set(fmt.Sprintf("c/n/d/d%02d", i), doc("s", big)))
	}
	mustCommit(t, s, append(writes, set("c/a", doc("v", value.Int(1))))...)
	rd, err := s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	seq := rd.Seq()
	rd.Close(ctx)
	for i := range writes {
		writes[i].Fields = doc("s", big, "n", value.Int(int64(i)))
	}
	mustCommit(t, s, append(writes, set("c/a", doc("v", value.Int(2))))...)
	rd, err = s.BeginIndexRead(ctx, testDB)
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close(ctx)
	for _, scope := range []ChangeScope{{CollectionID: "c"}, {CollectionID: "c", Group: true}} {
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		got, err := changedSince(rd, seq, scope)
		runtime.ReadMemStats(&m1)
		if err != nil || len(got) != 1 || got[0].Path.String() != "c/a" {
			t.Fatalf("%+v: %v %v", scope, paths(got), err)
		}
		if n := m1.TotalAlloc - m0.TotalAlloc; n > 4<<20 {
			t.Errorf("%+v: allocated %d bytes for one small document beside 24 MiB of others changed", scope, n)
		}
	}
	// In scope, the images stream one at a time to the caller, which stops at its first error.
	var seen int
	stop := errors.New("stop")
	if err := rd.ChangedSince(ctx, seq, ChangeScope{CollectionID: "d", Group: true}, func(Document) error {
		if seen++; seen == 3 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) || seen != 3 {
		t.Errorf("stopped at %d: %v", seen, err)
	}
}

// A continued read whose first commit since is the changelog's oldest row is answered while that
// row is younger than the configured retention, not only than the least retention: a database
// idle longer than the retention, then a commit, then a client that stalls minutes, still reads.
func TestChangedSinceRetention(t *testing.T) {
	for _, c := range []struct {
		retention time.Duration
		aborted   bool
	}{{0, false}, {time.Minute, true}} {
		s, pool := newStore(t, Options{ChangelogRetention: c.retention})
		ctx := t.Context()
		mustCommit(t, s, set("c/a", doc("v", value.Int(1))))
		rd, err := s.BeginIndexRead(ctx, testDB)
		if err != nil {
			t.Fatal(err)
		}
		seq := rd.Seq()
		rd.Close(ctx)
		// Idle past the retention: retention pruned every row up to seq.
		if _, err := pool.Exec(ctx, `DELETE FROM documents.changelog WHERE seq <= $1`, seq); err != nil {
			t.Fatal(err)
		}
		mustCommit(t, s, set("c/a", doc("v", value.Int(2))))
		// The client stalled 5 minutes since.
		if _, err := pool.Exec(ctx, `UPDATE documents.changelog SET commit_time = commit_time - interval '5 minutes'`); err != nil {
			t.Fatal(err)
		}
		rd, err = s.BeginIndexRead(ctx, testDB)
		if err != nil {
			t.Fatal(err)
		}
		got, err := changedSince(rd, seq, ChangeScope{CollectionID: "c"})
		rd.Close(ctx)
		if c.aborted {
			if !errors.Is(err, ErrIndexReadTooLong) {
				t.Errorf("retention %v: %v %v, want ErrIndexReadTooLong", c.retention, paths(got), err)
			}
		} else if err != nil || len(got) != 1 {
			t.Errorf("retention %v: %v %v", c.retention, paths(got), err)
		}
	}
}
