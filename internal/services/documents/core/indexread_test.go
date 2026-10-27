package core

import (
	"errors"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

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
	got, err := rd.ChangedSince(ctx, seq, ChangeScope{CollectionID: "c"})
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
	got, err = rd.ChangedSince(ctx, seq, ChangeScope{CollectionID: "s", Group: true})
	if err != nil || len(got) != 1 || got[0].Path.String() != "c/a/s/y" || !got[0].Exists() {
		t.Errorf("the group s: %v %v", paths(got), err)
	}
	if got, err := rd.ChangedSince(ctx, rd.Seq(), ChangeScope{CollectionID: "c"}); err != nil || len(got) != 0 {
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
	if _, err := rd2.ChangedSince(ctx, seq, ChangeScope{CollectionID: "c"}); !errors.Is(err, ErrIndexReadTooLong) {
		t.Errorf("pruned: %v", err)
	}
	if _, err := rd2.ChangedSince(ctx, seq+1, ChangeScope{CollectionID: "c"}); err != nil {
		t.Errorf("after the pruned commit: %v", err)
	}
}
