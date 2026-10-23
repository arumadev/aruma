package rules

// The operators of the language (ADR-021 §2.3). Each charges m before it builds a value, and
// returns only an *evalError, which && and || may absorb, or an *abort, which ends the
// evaluation. Any kind an operator does not know, the reserved Constraint and Unknown included,
// is an *evalError. S1-B replaces these stubs.

// binary applies x op y for every binary operator but && and ||, which the interpreter
// evaluates itself: + - * / % < <= > >= == != and in.
func binary(m *meter, op token, x, y Value) (Value, error) {
	panic("S1-B: binary")
}

// unary applies op x, op being tokNot or tokMinus.
func unary(m *meter, op token, x Value) (Value, error) {
	panic("S1-B: unary")
}

// selectField returns x.name: a map's entry.
func selectField(m *meter, x Value, name string) (Value, error) {
	panic("S1-B: selectField")
}

// selectIndex returns x[i]: a list's element, a map's entry, a string's character or a path's
// segment, string indexes counting UTF-16 code units.
func selectIndex(m *meter, x, i Value) (Value, error) {
	panic("S1-B: selectIndex")
}

// selectSlice returns x[lo:hi] of a list or a string; a nil bound was left out.
func selectSlice(m *meter, x Value, lo, hi *Value) (Value, error) {
	panic("S1-B: selectSlice")
}

// typeIs evaluates x is typ, typ being the type's name as written. An unknown name is an
// *evalError.
func typeIs(x Value, typ string) (Value, error) {
	panic("S1-B: typeIs")
}
