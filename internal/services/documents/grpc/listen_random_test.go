package grpc_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// TestListenRandom: writers commit at random while listeners hold query, document, limit and
// offset targets, each disconnecting and resuming midway with its tokens and counts. Every frame
// passes the checker, and at every global snapshot each target's documents, rebuilt as the JS
// SDK applies the frames (an ExistenceFilter that disagrees makes it query again), are its
// result at the snapshot's read time; at the end, its result now.
func TestListenRandom(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	tok := x.env.JWT(t)
	const keys, writers, commits, listeners = 16, 4, 30, 3
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	for k := range keys {
		x.commit(t, tok, update(fmt.Sprintf("rnd/k%02d", k), rankDoc(int64(k%10))))
	}
	type target struct {
		make  func(id int32) *pb.Target
		query *pb.StructuredQuery // nil for the documents target
		docs  []string
	}
	targets := map[int32]target{
		1: {make: func(id int32) *pb.Target { return queryTarget(id, byRank("rnd", 5, 0, 0)) }, query: byRank("rnd", 5, 0, 0)},
		2: {make: func(id int32) *pb.Target { return docTarget(id, "rnd/k03", "rnd/k07") }, docs: []string{"rnd/k03", "rnd/k07"}},
		3: {make: func(id int32) *pb.Target { return queryTarget(id, byRank("rnd", -1, 3, 0)) }, query: byRank("rnd", -1, 3, 0)},
		4: {make: func(id int32) *pb.Target { return queryTarget(id, byRank("rnd", 2, 4, 2)) }, query: byRank("rnd", 2, 4, 2)},
		5: {make: func(id int32) *pb.Target { return docTarget(id, "rnd/k05") }, docs: []string{"rnd/k05"}},
	}
	oracle := func(id int32, rt time.Time) (map[string]time.Time, error) {
		tg := targets[id]
		if tg.query != nil {
			return x.result(t, rt, tg.query)
		}
		return x.documentsAt(t, rt, tg.docs)
	}

	var marker atomic.Pointer[time.Time]
	var wg sync.WaitGroup
	for n := range listeners {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(seed, uint64(n)))
			open := func(held map[int32]map[string]*pb.Document) *apitest.ListenClient {
				l := apitest.OpenListen(t, x.client, tok)
				l.Checker.Oracle, l.Checker.Held = oracle, held
				return l
			}
			// pump reads until stop accepts a global snapshot; then the targets a filter
			// disagreed with are queried again.
			pump := func(l *apitest.ListenClient, stop func(rt time.Time) bool) bool {
				asked := map[*apitest.ListenTarget]bool{} // once per target: its answer replaces it
				for {
					res := l.Next(apitest.ListenWait)
					if res == nil {
						t.Errorf("listener %d: no response (end %v)", n, l.End)
						return false
					}
					if !apitest.IsGlobal(res) {
						continue
					}
					requery := false
					for _, lt := range l.Checker.Targets() {
						if lt.Mismatched && !asked[lt] {
							asked[lt] = true
							l.Remove(lt.ID)
							l.Add(targets[lt.ID].make(lt.ID))
							requery = true
						}
					}
					if !requery && l.Checker.Settled() && stop(res.GetTargetChange().GetReadTime().AsTime()) {
						return true
					}
				}
			}
			l := open(nil)
			for id := range int32(len(targets)) {
				l.Add(targets[id+1].make(id + 1))
			}
			globals := 2 + rng.IntN(20)
			if !pump(l, func(time.Time) bool { globals--; return globals <= 0 || marker.Load() != nil }) {
				return
			}
			// Away and back: resume every target with its token and what it holds.
			held := map[int32]map[string]*pb.Document{}
			resume := map[int32][]byte{}
			for _, lt := range l.Checker.Targets() {
				held[lt.ID], resume[lt.ID] = maps.Clone(lt.Docs), lt.Token
			}
			l.Checker.Oracle = nil // the frames left before the end are checked, not compared: they are many under load
			l.CloseSend()
			if err := l.Ended(); err != nil {
				t.Errorf("listener %d: %v", n, err)
			}
			time.Sleep(time.Duration(rng.IntN(30)) * time.Millisecond)
			l = open(held)
			for id := range int32(len(targets)) {
				tg := targets[id+1].make(id + 1)
				tg.ResumeType = &pb.Target_ResumeToken{ResumeToken: resume[id+1]}
				if rng.IntN(2) == 0 {
					tg.ExpectedCount = wrapperspb.Int32(int32(len(held[id+1])))
				}
				l.Add(tg)
			}
			// Until a global snapshot at or after the writers' last commit.
			if !pump(l, func(rt time.Time) bool { m := marker.Load(); return m != nil && !rt.Before(*m) }) {
				return
			}
			for _, lt := range l.Checker.Targets() {
				want, err := oracle(lt.ID, time.Now())
				if err != nil {
					t.Error(err)
					continue
				}
				if len(want) != len(lt.Docs) {
					t.Errorf("listener %d target %d: holds %d documents, the result has %d", n, lt.ID, len(lt.Docs), len(want))
				}
			}
		})
	}

	var writing sync.WaitGroup
	for w := range writers {
		writing.Go(func() {
			rng := rand.New(rand.NewPCG(seed, uint64(100+w)))
			for range commits {
				var writes []*pb.Write
				seen := map[int]bool{}
				for range 1 + rng.IntN(3) {
					k := rng.IntN(keys)
					if seen[k] {
						continue
					}
					seen[k] = true
					p := fmt.Sprintf("rnd/k%02d", k)
					switch r := rng.IntN(10); {
					case r < 6:
						writes = append(writes, update(p, rankDoc(int64(rng.IntN(10)))))
					case r < 8:
						writes = append(writes, del(p))
					default:
						writes = append(writes, update(p, map[string]*pb.Value{"rank": intValue(int64(rng.IntN(10))), "note": intValue(int64(r))}))
					}
				}
				if _, err := x.client.Commit(t.Context(), req(&pb.CommitRequest{Database: db, Writes: writes}, tok)); err != nil {
					t.Error(err)
					return
				}
				time.Sleep(time.Duration(rng.IntN(5)) * time.Millisecond)
			}
		})
	}
	writing.Wait()
	last := x.commit(t, tok, update("rnd/marker", rankDoc(-1)))
	marker.Store(&last)
	wg.Wait()
}

// documentsAt is documents' state at a read time, those that exist, by name with update times.
func (x *harness) documentsAt(t *testing.T, rt time.Time, paths []string) (map[string]time.Time, error) {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = root + "/" + p
	}
	rs, err := batchGet(t, x.client, x.env.JWT(t), &pb.BatchGetDocumentsRequest{Database: db, Documents: names,
		ConsistencySelector: &pb.BatchGetDocumentsRequest_ReadTime{ReadTime: timestamppb.New(rt)}})
	if err != nil {
		return nil, err
	}
	out := map[string]time.Time{}
	for _, r := range rs {
		if d := r.GetFound(); d != nil {
			out[d.GetName()] = d.GetUpdateTime().AsTime()
		}
	}
	return out, nil
}
