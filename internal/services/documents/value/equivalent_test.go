package value

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// Equivalent is Compare's equality without the index budget (the array transforms' equality).
func TestEquivalent(t *testing.T) {
	long := strings.Repeat("x", 2000)
	nan := Double(math.NaN())
	dec, _ := Decimal128("1.0")
	// An int32 and a decimal spend less and more of the index budget than an integer, so Key
	// cuts these pairs at different points (F2 re-review N2); their whole keys agree.
	near := func(n Value, l int) Value {
		return Map(Field{"a", n}, Field{"b", String(strings.Repeat("x", l))})
	}
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
		{near(Int32(1), 1488), near(Int(1), 1488), true},
		{near(dec, 1480), near(Int(1), 1480), true},
		{near(Double(1), 1480), near(Int(2), 1480), false},
		{Map(Field{"a", nan}, Field{"b", Double(math.Copysign(0, -1))}), Map(Field{"a", nan}, Field{"b", Int(0)}), true},
	} {
		if got := Equivalent(c.a, c.b); got != c.want {
			t.Errorf("Equivalent(%v, %v) = %v", c.a, c.b, got)
		}
		if got := bytes.Equal(AppendWholeKey(nil, c.a), AppendWholeKey(nil, c.b)); got != c.want {
			t.Errorf("whole keys of %v and %v equal: %v", c.a, c.b, got)
		}
	}
}
