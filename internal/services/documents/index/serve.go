package index

// What the planner may serve a query from (ADR-025 §1, §3): the READY indexes alone, never a
// CREATING one (R21), whose entries are not all written yet.

// SingleFieldReady reports whether the single-field index of scope and mode on field path of
// collection group group exists and is READY: its governing configuration has it.
func (c *Catalog) SingleFieldReady(group, path string, scope Scope, mode FieldMode) bool {
	return c.fieldOverrides().singleField(group, path, scope, mode) == enabled
}

// ReadyComposites returns the READY composite indexes of collection group group at scope,
// normalized (their fields end with __name__).
func (c *Catalog) ReadyComposites(group string, scope Scope) []Index {
	var out []Index
	for _, ix := range c.indexes(group) {
		if ix.Scope == scope && ix.State == StateReady {
			out = append(out, ix)
		}
	}
	return out
}
