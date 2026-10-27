package core

import (
	"fmt"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// benchDoc is a document of six fields, a map and an array: 16 automatic index entries.
func benchDoc(i int) value.Value {
	return doc("a", value.Int(int64(i)), "b", value.String(fmt.Sprint(i)), "c", value.Bool(i%2 == 0),
		"t", value.Array(value.Int(int64(i%7)), value.Int(int64(i%5))),
		"m", value.Map(value.Field{Name: "x", Value: value.Int(int64(i))}, value.Field{Name: "y", Value: value.String("y")}))
}

// BenchmarkCommitIndexed commits one update of a document whose automatic index entries change,
// in a database of 50 documents and of 20,000 (320,000 entries): the commit path with its index
// statements, which must probe the primary key rather than scan the table.
func BenchmarkCommitIndexed(b *testing.B) {
	for _, preload := range []int{50, 20_000} {
		b.Run(fmt.Sprint(preload), func(b *testing.B) {
			s := NewPGStore(storetest.NewPool(b), Options{})
			ctx := b.Context()
			for j := 0; j < preload; j += 500 {
				var ws []Write
				for i := j; i < min(j+500, preload); i++ {
					ws = append(ws, set(fmt.Sprintf("c/%d", i), benchDoc(i)))
				}
				if _, err := s.Commit(ctx, testDB, ws); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := s.pool.Exec(ctx, "ANALYZE documents.index_entries"); err != nil {
				b.Fatal(err)
			}
			i := 0
			for b.Loop() {
				i++
				if _, err := s.Commit(ctx, testDB, []Write{set(fmt.Sprintf("c/%d", i%50), benchDoc(preload+i))}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// wideDoc is a document of n fields of the four common kinds, as an ordinary document holds
// them: about 2n automatic index entries.
func wideDoc(n, v int) value.Value {
	fs := make([]value.Field, n)
	for i := range fs {
		var x value.Value
		switch i % 4 {
		case 0:
			x = value.Int(int64(i + v))
		case 1:
			x = value.Double(float64(i+v) + 0.5)
		case 2:
			x = value.String("some value text")
		default:
			x = value.Timestamp(1700000000+int64(v), 0)
		}
		fs[i] = value.Field{Name: fmt.Sprintf("field_%03d", i), Value: x}
	}
	return value.Map(fs...)
}

// BenchmarkCommitWide commits the largest batch the SDKs send, 500 documents of 200 fields
// (about 200,000 index entries), as one create, one rewrite of most fields and one delete,
// reporting each commit's time and the time from its stamp to its return, which bounds the
// time it held the sequencer row (the commit_time is the stamp's clock).
func BenchmarkCommitWide(b *testing.B) {
	const docs, fields = 500, 200
	s := NewPGStore(storetest.NewPool(b), Options{})
	ctx := b.Context()
	var held time.Duration
	commit := func(ws []Write) time.Duration {
		start := time.Now()
		res, err := s.Commit(ctx, testDB, ws)
		if err != nil {
			b.Fatal(err)
		}
		held = max(held, time.Since(res.CommitTime))
		return time.Since(start)
	}
	writes := func(v int, del bool) []Write {
		ws := make([]Write, docs)
		for i := range ws {
			p := fmt.Sprintf("users/AbCdEfGhIjKlM%05d", i)
			if del {
				ws[i] = Write{Kind: WriteDelete, Path: ParsePath(p)}
			} else {
				ws[i] = set(p, wideDoc(fields, v))
			}
		}
		return ws
	}
	var create, rewrite, del time.Duration
	v := 0
	for b.Loop() {
		v += 2
		create += commit(writes(v, false))
		rewrite += commit(writes(v+1, false))
		del += commit(writes(0, true))
	}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 / float64(b.N) }
	b.ReportMetric(ms(create), "create-ms")
	b.ReportMetric(ms(rewrite), "rewrite-ms")
	b.ReportMetric(ms(del), "delete-ms")
	b.ReportMetric(float64(held.Microseconds())/1000, "max-held-ms")
}
