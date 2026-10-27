package grpc_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// lateNotifier tells the hub of commits late while armed: the hub's position lags the store's
// head, as it does for any commit whose Committed has not run yet.
type lateNotifier struct {
	documents.Notifier
	armed atomic.Bool
	wg    sync.WaitGroup
}

func (n *lateNotifier) Committed(db documents.DB, seq int64, at time.Time, changes []documents.Change) {
	if n.armed.Load() {
		n.wg.Go(func() {
			time.Sleep(300 * time.Millisecond)
			n.Notifier.Committed(db, seq, at, changes)
		})
		return
	}
	n.Notifier.Committed(db, seq, at, changes)
}

// A target added or resumed while another target's events are still to come is answered at the
// store's head only after them: one document's versions never go back on the stream (ADR-026 §2),
// which a client keeping the last version it got per document relies on.
func TestListenTargetAfterPendingEvents(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "add", true: "resume"}[resumed], func(t *testing.T) {
			late := &lateNotifier{}
			x := newHarness(t, apitest.Options{Hub: listen.Options{GapTimeout: 5 * time.Second},
				Notifier: func(n documents.Notifier) documents.Notifier { late.Notifier = n; return late }}, docgrpc.Limits{})
			tok := x.env.JWT(t)
			c0 := x.commit(t, tok, update("oo/x", rankDoc(5)))
			l := apitest.OpenListen(t, x.client, tok)
			l.Add(queryTarget(1, byRank("oo", 5, 0, 0)))
			l.UntilGlobal()

			late.armed.Store(true)
			x.commit(t, tok, update("oo/x", rankDoc(1))) // leaves target 1
			x.commit(t, tok, update("oo/x", rankDoc(0)))
			late.armed.Store(false)
			t2 := docTarget(2, "oo/x")
			if resumed {
				t2.ResumeType = &pb.Target_ReadTime{ReadTime: timestamppb.New(c0)}
			}
			l.Add(t2)

			var last time.Time
			for {
				r := l.Next(3 * time.Second)
				if r == nil {
					t.Fatal("no global snapshot")
				}
				if d := r.GetDocumentChange().GetDocument(); d != nil {
					ut := d.GetUpdateTime().AsTime()
					if ut.Before(last) {
						t.Fatalf("oo/x at %v after %v (rank %d)", ut, last, d.GetFields()["rank"].GetIntegerValue())
					}
					last = ut
				}
				if apitest.IsGlobal(r) {
					break
				}
			}
			late.wg.Wait()
		})
	}
}
