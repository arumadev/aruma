package api

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// nestedPair is the check nestedTransform replaces: every pair, in order.
func nestedPair(ts []core.Transform) (value.FieldPath, bool) {
	for i, t := range ts {
		for _, u := range ts[:i] {
			parent := t.Path
			if u.Path.IsPrefixOf(parent) {
				parent = u.Path
			}
			if len(t.Path) != len(u.Path) && parent.IsPrefixOf(t.Path) && parent.IsPrefixOf(u.Path) {
				return parent, true
			}
		}
	}
	return nil, false
}

// nestedTransform names the pair the pairwise check names, on random paths over few segments.
func TestNestedTransformPairwise(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	segs := []string{"a", "b", "c"}
	for range 20000 {
		ts := make([]core.Transform, 1+rng.IntN(6))
		for i := range ts {
			p := make(value.FieldPath, 1+rng.IntN(3))
			for k := range p {
				p[k] = segs[rng.IntN(len(segs))]
			}
			ts[i].Path = p
		}
		want, wantOK := nestedPair(ts)
		got, ok := nestedTransform(ts)
		if ok != wantOK || got.String() != want.String() {
			t.Fatalf("%v: got %v %v, want %v %v", ts, got, ok, want, wantOK)
		}
	}
}

// The request bounds: a request over them is refused before any write is read; one at them is
// read. The checks of a request about as large as message_bytes allows take well under a second.
func TestRequestWorkBounds(t *testing.T) {
	update := func(n int) *pb.Write {
		w := &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: "projects/p/databases/(default)/documents/c/d"}}}
		for i := range n {
			w.UpdateTransforms = append(w.UpdateTransforms, &pb.DocumentTransform_FieldTransform{
				FieldPath:     "f" + strconv.Itoa(i),
				TransformType: &pb.DocumentTransform_FieldTransform_Increment{Increment: &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: 1}}},
			})
		}
		return w
	}
	ws := make([]*pb.Write, MaxRequestWrites+1)
	for i := range ws {
		ws[i] = update(0)
	}
	if _, err := writesOf(ws); err != errTooManyWrites {
		t.Fatalf("over the writes: %v", err)
	}
	if _, err := writesOf(ws[:MaxRequestWrites]); err != nil {
		t.Fatalf("at the writes: %v", err)
	}
	if _, err := writesOf([]*pb.Write{update(MaxRequestTransforms + 1)}); err != errTooManyTransforms {
		t.Fatalf("over the transforms: %v", err)
	}
	start := time.Now()
	if _, err := writesOf([]*pb.Write{update(MaxRequestTransforms)}); err != nil {
		t.Fatalf("at the transforms: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("%d transforms took %v to read", MaxRequestTransforms, d)
	}
}
