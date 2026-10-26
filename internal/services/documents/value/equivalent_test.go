package value

import (
	"math"
	"strings"
	"testing"
)

// Equivalent is Compare's equality without the index budget (the array transforms' equality).
func TestEquivalent(t *testing.T) {
	long := strings.Repeat("x", 2000)
	nan := Double(math.NaN())
	for _, c := range []struct {
		a, b Value
		want bool
	}{
		{Int(3), Double(3), true},
		{Int(0), Double(math.Copysign(0, -1)), true},
		{nan, nan, true},
		{Null(), Null(), true},
		{Null(), nan, false},
		{Int32(1), Int(1), true},
		{Array(Int(1), Map(Field{"k", Double(2)})), Array(Double(1), Map(Field{"k", Int(2)})), true},
		{Array(Int(1)), Array(Int(1), Int(1)), false},
		{String(long + "a"), String(long + "b"), false},
		{String(long), String(long), true},
		{Vector(0), Vector(math.Copysign(0, -1)), false},
		{Timestamp(1, 1000), Timestamp(1, 1000), true},
		{String("1"), Int(1), false},
	} {
		if got := Equivalent(c.a, c.b); got != c.want {
			t.Errorf("Equivalent(%v, %v) = %v", c.a, c.b, got)
		}
	}
}
