package core

import (
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// Transforms apply under the document's lock: concurrent increments lose none. REQUEST_TIME is
// the commit's own commit_time at whole milliseconds, in the stored document and the result.
func TestTransformsCommit(t *testing.T) {
	notes := &recordingNotifier{}
	s, pool := newStore(t, Options{Notifier: notes})
	ctx := t.Context()
	p := ParsePath("c/counter")
	stamp := Transform{Path: value.FieldPath{"ts"}, Kind: TransformServerTime}
	inc := Transform{Path: value.FieldPath{"n"}, Kind: TransformIncrement, Operand: value.Int(1)}
	first := mustCommit(t, s, Write{Kind: WriteUpdate, Path: p, Fields: doc("a", value.Int(1)), Transforms: []Transform{stamp, inc}})
	ts := RequestTime(first.CommitTime)
	if first.Seq != 1 || len(first.Results[0].TransformResults) != 2 || !value.Identical(first.Results[0].TransformResults[0], ts) ||
		!value.Identical(first.Results[0].TransformResults[1], value.Int(1)) || !first.Results[0].UpdateTime.Equal(first.CommitTime) {
		t.Fatalf("first: %+v", first)
	}
	got, _, err := s.Get(ctx, testDB, p, ReadOptions{})
	if err != nil || !value.Identical(got.Fields, doc("a", value.Int(1), "n", value.Int(1), "ts", ts)) || !got.UpdateTime.Equal(first.CommitTime) {
		t.Fatalf("stored: %+v %v", got, err)
	}

	const workers, each = 8, 10
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for range each {
				ws := []Write{{Kind: WriteUpdate, Path: p, Masked: true, Fields: value.Map(), Transforms: []Transform{inc}}}
				if w%2 == 0 { // half the commits are built after their stamp
					ws[0].Transforms = []Transform{inc, stamp}
				}
				res, err := s.Commit(ctx, testDB, ws)
				if err != nil {
					t.Error(err)
					return
				}
				if w%2 == 0 && !value.Identical(res.Results[0].TransformResults[1], RequestTime(res.CommitTime)) {
					t.Errorf("server time %v at %v", res.Results[0].TransformResults[1], res.CommitTime)
				}
			}
		})
	}
	wg.Wait()
	got, _, _ = s.Get(ctx, testDB, p, ReadOptions{})
	if n, _ := got.Fields.Get("n"); !value.Identical(n, value.Int(1+workers*each)) {
		t.Fatalf("counter %v", n)
	}
	var seqs, head int64
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT seq) FROM documents.changelog WHERE project_number = $1`, testDB.Project).Scan(&seqs); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT seq FROM documents.sequencer WHERE project_number = $1`, testDB.Project).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if seqs != 1+workers*each || head != seqs || notes.count() != int(seqs) {
		t.Fatalf("%d changelog seqs, head %d, %d notified", seqs, head, notes.count())
	}
}

// A commit that changes nothing takes no seq, but its commit_time is the clock's, as
// production's advances: later than the last commit, and a read at it answers.
func TestUnchangedCommitTimeAdvances(t *testing.T) {
	s, _ := newStore(t, Options{})
	ctx := t.Context()
	p := ParsePath("c/same")
	first := mustCommit(t, s, set(p.String(), doc("a", value.Int(1))))
	time.Sleep(2 * time.Millisecond)
	same := mustCommit(t, s, set(p.String(), doc("a", value.Int(1))))
	if same.Seq != 0 || !same.Results[0].UpdateTime.Equal(first.CommitTime) || !same.CommitTime.After(first.CommitTime) {
		t.Fatalf("unchanged: %+v after %v", same, first.CommitTime)
	}
	time.Sleep(2 * time.Millisecond)
	verify := mustCommit(t, s, Write{Kind: WriteVerify, Path: p, Precondition: Precondition{Exists: new(true)}})
	if verify.Seq != 0 || !verify.CommitTime.After(same.CommitTime) || !verify.Results[0].UpdateTime.Equal(first.CommitTime) {
		t.Fatalf("verify: %+v", verify)
	}
	if _, rt, err := s.Get(ctx, testDB, p, ReadOptions{ReadTime: verify.CommitTime}); err != nil || !rt.Equal(verify.CommitTime) {
		t.Fatalf("read at the commit_time: %v %v", rt, err)
	}
	// A REQUEST_TIME on a document the commit then deletes, which never existed: no change,
	// and the transform's result is the unchanged commit's time.
	gone := ParsePath("c/gone")
	res := mustCommit(t, s, Write{Kind: WriteUpdate, Path: gone, Fields: value.Map(), Transforms: []Transform{{Path: value.FieldPath{"ts"}, Kind: TransformServerTime}}},
		Write{Kind: WriteDelete, Path: gone})
	if res.Seq != 0 || !value.Identical(res.Results[0].TransformResults[0], RequestTime(res.CommitTime)) {
		t.Fatalf("deleted after a server time: %+v", res)
	}
}
