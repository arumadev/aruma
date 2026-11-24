package main

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// readAll reads a collection's documents by id, with the read time of the result.
func (r *run) readAll(ctx context.Context, coll string) (map[string]*pb.Document, int, time.Time, error) {
	var lastErr error
	for range 5 {
		docs, rt, err := r.grpc.Query(ctx, &pb.RunQueryRequest{Parent: r.db + "/documents",
			QueryType: &pb.RunQueryRequest_StructuredQuery{StructuredQuery: &pb.StructuredQuery{
				From: []*pb.StructuredQuery_CollectionSelector{{CollectionId: coll}}}}})
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		out := map[string]*pb.Document{}
		for _, d := range docs {
			out[d.GetName()[strings.LastIndexByte(d.GetName(), '/')+1:]] = d
		}
		return out, len(docs), rt, nil
	}
	return nil, 0, time.Time{}, lastErr
}

func (r *run) check(ctx context.Context, clients []*client, rep *report) {
	colls := map[string]map[string]*pb.Document{}
	for _, coll := range []string{"ops", "tx", "stx", "bw", "totals", "ctr"} {
		docs, n, _, err := r.readAll(ctx, coll)
		if err != nil {
			r.Violation("read %s back: %v", coll, err)
			return
		}
		colls[coll] = docs
		if coll != "totals" && coll != "ctr" {
			rep.Docs += n
		}
	}
	// Every document must be one a client wrote, and be there exactly when the client's record
	// says so.
	known := map[string]*marker{}
	for _, c := range clients {
		for _, m := range c.markers {
			known[m.coll+"/"+m.id] = m
		}
	}
	for _, coll := range []string{"ops", "tx", "stx", "bw"} {
		for id := range colls[coll] {
			m := known[coll+"/"+id]
			switch {
			case m == nil:
				r.Violation("%s/%s exists, and no client wrote it", coll, id)
			case m.created == refused:
				r.Violation("%s/%s exists, and its write was refused", coll, id)
			case m.created == notApplied:
				r.Violation("%s/%s exists, and was not there when read back after its reply was lost", coll, id)
			case m.deleted.present():
				r.Violation("%s/%s exists, and its delete was %s", coll, id, outcomeName(m.deleted))
			}
		}
	}
	for key, m := range known {
		if m.live() {
			if _, ok := colls[m.coll][m.id]; !ok {
				r.Violation("%s is missing: its create was %s", key, outcomeName(m.created))
			}
		}
	}
	// Counters: totals/all is the sum of every ops document's k, totals/c<i> of client i's.
	sums := map[string]int64{}
	for _, d := range colls["ops"] {
		k := intOf(d, "k")
		sums["all"] += k
		sums[fmt.Sprintf("c%d", intOf(d, "c"))] += k
	}
	for _, id := range append([]string{"all"}, clientIDs(len(clients))...) {
		if got := intOf(colls["totals"][id], "sum"); got != sums[id] {
			r.Violation("totals/%s is %d, the ops documents add up to %d: an increment was lost or applied twice", id, got, sums[id])
		}
	}
	// Transactions: the markers of a counter hold 1..n exactly.
	checkSeries := func(what string, ctr *pb.Document, field string, vals []int64) {
		n := intOf(ctr, field)
		slices.Sort(vals)
		ok := int64(len(vals)) == n
		for i, v := range vals {
			if v != int64(i+1) {
				ok = false
			}
		}
		if !ok {
			r.Violation("%s: the counter is %d, its markers hold %s", what, n, series(vals))
		}
	}
	perClient := map[int64][]int64{}
	for _, d := range colls["tx"] {
		perClient[intOf(d, "c")] = append(perClient[intOf(d, "c")], intOf(d, "t"))
	}
	for i := range clients {
		checkSeries(fmt.Sprintf("ctr/t%d", i), colls["ctr"][fmt.Sprintf("t%d", i)], "t", perClient[int64(i)])
	}
	var shared []int64
	for _, d := range colls["stx"] {
		shared = append(shared, intOf(d, "v"))
	}
	checkSeries("ctr/shared", colls["ctr"]["shared"], "v", shared)

	r.checkStamps(clients, rep)
	r.checkSeq(ctx, rep)
}

func clientIDs(n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("c%d", i))
	}
	return out
}

func outcomeName(o outcome) string {
	return [...]string{"never settled", "acknowledged", "refused", "applied after a lost reply", "not applied after a lost reply"}[o]
}

// series prints a sorted list of values compactly, with its gaps and repeats.
func series(vals []int64) string {
	var bad []string
	for i := 1; i < len(vals); i++ {
		if vals[i] == vals[i-1] {
			bad = append(bad, fmt.Sprintf("%d twice", vals[i]))
		} else if vals[i] != vals[i-1]+1 {
			bad = append(bad, fmt.Sprintf("gap %d..%d", vals[i-1], vals[i]))
		}
	}
	if len(vals) > 0 && vals[0] != 1 {
		bad = append(bad, fmt.Sprintf("starts at %d", vals[0]))
	}
	return fmt.Sprintf("%d values %v", len(vals), bad)
}

// checkStamps: a commit acknowledged before another was sent has the earlier commit_time.
func (r *run) checkStamps(clients []*client, rep *report) {
	var all []stamp
	for _, c := range clients {
		all = append(all, c.stamps...)
	}
	rep.Commits = len(all)
	byAck := slices.Clone(all)
	sort.Slice(byAck, func(i, j int) bool { return byAck[i].acked.Before(byAck[j].acked) })
	maxCT := make([]time.Time, len(byAck))
	for i, s := range byAck {
		maxCT[i] = s.commit
		if i > 0 && maxCT[i-1].After(s.commit) {
			maxCT[i] = maxCT[i-1]
		}
	}
	bad := 0
	for _, s := range all {
		// The commits acknowledged before s was sent.
		n := sort.Search(len(byAck), func(i int) bool { return !byAck[i].acked.Before(s.sent) })
		if n > 0 && !maxCT[n-1].Before(s.commit) {
			bad++
			if bad <= 5 {
				r.Violation("commit via %s sent at %v has commit_time %v, not after %v of a commit acknowledged before it was sent",
					s.via, s.sent.Format(time.StampMicro), s.commit, maxCT[n-1])
			}
		}
	}
}

// checkSeq reads the changelog and the sequencer row: seqs 1..n without a gap, the row at n, and
// commit_time rising strictly with seq.
func (r *run) checkSeq(ctx context.Context, rep *report) {
	conn, err := r.Srv.DB(ctx)
	if err != nil {
		r.Violation("connect to the database: %v", err)
		return
	}
	defer conn.Close(ctx)
	var seq int64
	if err := conn.QueryRow(ctx, `SELECT seq, last_ts FROM documents.sequencer WHERE database_id = '(default)'`).Scan(&seq, &r.lastTS); err != nil {
		r.Violation("read the sequencer: %v", err)
		return
	}
	rep.Seq = seq
	rows, err := conn.Query(ctx, `SELECT seq, min(commit_time), max(commit_time) FROM documents.changelog GROUP BY seq ORDER BY seq`)
	if err != nil {
		r.Violation("read the changelog: %v", err)
		return
	}
	defer rows.Close()
	var prevSeq int64
	var prevT time.Time
	bad := 0
	for rows.Next() {
		var s int64
		var lo, hi time.Time
		if err := rows.Scan(&s, &lo, &hi); err != nil {
			r.Violation("scan the changelog: %v", err)
			return
		}
		switch {
		case s != prevSeq+1:
			bad++
			r.Violation("changelog: seq %d follows %d", s, prevSeq)
		case !lo.Equal(hi):
			bad++
			r.Violation("changelog: seq %d has two commit_times %v and %v", s, lo, hi)
		case !lo.After(prevT):
			bad++
			r.Violation("changelog: seq %d at %v, not after seq %d at %v", s, lo, prevSeq, prevT)
		}
		if bad > 5 {
			return
		}
		prevSeq, prevT = s, lo
	}
	if prevSeq != seq {
		r.Violation("the sequencer is at %d, the changelog ends at %d", seq, prevSeq)
	}
}
