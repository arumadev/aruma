package core

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
)

// randomLeaf returns a leaf of every kind storage meets, with the strings and numbers whose
// encoding has edge cases: escapes, U+0000 (stored as base64), non-ASCII, integers past 2^53,
// doubles that look like integers, and doubles in scientific notation.
func randomLeaf(rng *rand.Rand) *Node {
	switch rng.IntN(14) {
	case 0:
		return Bool(rng.IntN(2) == 0)
	case 1:
		return Int(rng.Int64N(2000) - 1000)
	case 2:
		return Int(math.MaxInt64 - rng.Int64N(10))
	case 3:
		return Int(math.MinInt64 + rng.Int64N(10))
	case 4:
		return Number(rng.Float64() * 1000)
	case 5:
		return Number(float64(rng.IntN(100))) // a double that reads like an integer: 3.0
	case 6:
		return Number([]float64{1e300, -1.5e-7, 1e21, 5e-324, math.MaxFloat64, 1e7, 1e-3}[rng.IntN(7)])
	case 7:
		return String("")
	case 8:
		return String(`quote " backslash \ slash / tab	newline
end`)
	case 9:
		return String("nul \x00 inside")
	case 10:
		return String("日本語 😀 \u2028 \u00e9 \x7f \x01")
	case 11:
		return String(fmt.Sprintf("item %d", rng.IntN(1000)))
	case 12:
		return String("<script>&amp;</script>")
	}
	return String(strings.Repeat("x", rng.IntN(100)))
}

func randomPriority(rng *rand.Rand) *Node {
	switch rng.IntN(4) {
	case 0:
		return Number(float64(rng.IntN(10)))
	case 1:
		return String(fmt.Sprintf("p%d", rng.IntN(10)))
	}
	return nil
}

func randomTree(rng *rand.Rand, depth int) *Node {
	if depth == 0 || rng.IntN(3) == 0 {
		return randomLeaf(rng).WithPriority(randomPriority(rng))
	}
	children := map[string]*Node{}
	for i := range 1 + rng.IntN(5) {
		key := fmt.Sprintf("k%d", i)
		if rng.IntN(4) == 0 {
			key = fmt.Sprint(i)
		}
		children[key] = randomTree(rng, depth-1)
	}
	return Object(children).WithPriority(randomPriority(rng))
}

// treeOf rebuilds exactly what rowsOf stored, at the root and below any path.
func TestTreeOfRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 9))
	for round := range 2000 {
		base := Path{}
		if rng.IntN(2) == 0 {
			base = Path{"a", "b"}[:1+rng.IntN(2)]
		}
		want := randomTree(rng, 4)
		got, err := treeOf(base, rowsOf(base, want))
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if !Equal(got, want) {
			t.Fatalf("round %d at %q:\n got  %s\n want %s", round, base.String(), got.ExportJSON(), want.ExportJSON())
		}
	}
}

// leafOf reads back every stored leaf and priority as the leaf it was.
func TestLeafOfRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(4, 2))
	for range 5000 {
		want := randomLeaf(rng)
		got, err := leafOf(storedLeaf(want))
		if err != nil {
			t.Fatalf("%s: %v", storedLeaf(want), err)
		}
		if !Equal(got, want) || got.isInt != want.isInt {
			t.Fatalf("%s: got %s (int %v), want %s (int %v)", storedLeaf(want), got.ExportJSON(), got.isInt, want.ExportJSON(), want.isInt)
		}
	}
}

// The rows of a node like the query bench's: 100k children, each a score and a name.
func benchRows(children int) (Path, []row) {
	rng := rand.New(rand.NewPCG(1, 2))
	m := make(map[string]*Node, children)
	for i := range children {
		m[fmt.Sprintf("c%07d", i)] = Object(map[string]*Node{
			"score": Number(rng.Float64() * 1000),
			"name":  String(fmt.Sprintf("item %d", i)),
		})
	}
	base := Path{"q", fmt.Sprintf("n%d", children)}
	return base, rowsOf(base, Object(m))
}

func BenchmarkTreeOf(b *testing.B) {
	base, rows := benchRows(100_000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := treeOf(base, rows); err != nil {
			b.Fatal(err)
		}
	}
}
