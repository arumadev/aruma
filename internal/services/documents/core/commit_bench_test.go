package core

import (
	"fmt"
	"testing"

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
