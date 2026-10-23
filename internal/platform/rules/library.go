package rules

// The library's two dispatch tables: the functions of each type, and the global and namespaced
// functions (int(), float(), string(), path(), math.*, hashing.*, latlng.value, timestamp.*,
// duration.*, debug()). The interpreter reaches them through callMethod and callFunction. The
// lookups (get, exists, getAfter, existsAfter, firestore.*) are not here: lookup.go has them.
// Errors are an *evalError or an *abort, nothing else. S1-B replaces these stubs.

// callMethod calls recv.name(args).
func callMethod(m *meter, recv Value, name string, args []Value) (Value, error) {
	panic("S1-B: callMethod")
}

// callFunction calls the global or namespaced function name ("int", "math.abs") with args.
func callFunction(m *meter, name string, args []Value) (Value, error) {
	panic("S1-B: callFunction")
}
