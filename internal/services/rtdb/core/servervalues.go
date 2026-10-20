package core

// ServerValue is a value the server fills in when it applies a write: {".sv":"timestamp"} or
// {".sv":{"increment":n}} (ServerValues.ts). The SDKs show an estimate until the server's
// value arrives.
type ServerValue struct {
	Timestamp bool
	Increment float64
}

// parseServerValue reads the content of ".sv". Anything but the two known forms is an error;
// the emulator answers it with an error frame and drops the write.
func parseServerValue(v *jv) (*ServerValue, error) {
	switch v.kind {
	case KindString:
		if v.s == "timestamp" {
			return &ServerValue{Timestamp: true}, nil
		}
	case KindObject:
		if inc, ok := v.vals["increment"]; ok && len(v.keys) == 1 && inc.kind == KindNumber {
			return &ServerValue{Increment: inc.n}, nil
		}
	}
	return nil, ErrInvalidServerValue
}

func (sv *ServerValue) appendJSON(b []byte) []byte {
	if sv.Timestamp {
		return append(b, `{".sv":"timestamp"}`...)
	}
	b = append(b, `{".sv":{"increment":`...)
	return append(appendNumber(b, sv.Increment), "}}"...)
}

// String is the server value as JSON.
func (sv *ServerValue) String() string { return string(sv.appendJSON(nil)) }

// HasServerValues reports whether n holds a placeholder anywhere, priorities included.
func (n *Node) HasServerValues() bool {
	if n == nil {
		return false
	}
	if n.kind == kindServerValue || n.priority.Kind() == kindServerValue {
		return true
	}
	for _, c := range n.children {
		if c.HasServerValues() {
			return true
		}
	}
	return false
}

// increments lists the paths, relative to n, of the increments n holds. A priority increment
// is listed as its node's path followed by .priority.
func (n *Node) increments(at Path, out []Path) []Path {
	if n == nil {
		return out
	}
	if n.priority.Kind() == kindServerValue && !n.priority.sv.Timestamp {
		out = append(out, at.Child(PriorityKey))
	}
	if n.kind == kindServerValue && !n.sv.Timestamp {
		return append(out, at)
	}
	for k, c := range n.children {
		out = c.increments(at.Child(k), out)
	}
	return out
}

// ResolveServerValues replaces the placeholders in n, which is written at the location whose
// current value below n's root is read through existing. now is the server time in
// milliseconds. An increment adds to the number already there, or starts from 0 when there
// is none or it is not a number (resolveComplexDeferredValue). Paths given to existing are
// relative to n's root.
func ResolveServerValues(n *Node, now int64, existing map[string]*Node) *Node {
	return n.resolve(Path{}, now, existing)
}

// IncrementPaths returns the locations, relative to n, whose current values ResolveServerValues
// needs. Each is a leaf location or ends in .priority.
func (n *Node) IncrementPaths() []Path { return n.increments(Path{}, nil) }

func (n *Node) resolve(at Path, now int64, existing map[string]*Node) *Node {
	if n == nil || !n.HasServerValues() {
		return n
	}
	out := *n
	if n.priority.Kind() == kindServerValue {
		out.priority = n.priority.sv.resolve(at.Child(PriorityKey), now, existing)
	}
	if n.kind == kindServerValue {
		v := n.sv.resolve(at, now, existing)
		v.priority = out.priority
		return v
	}
	if n.kind == KindObject {
		out.children = make(map[string]*Node, len(n.children))
		for k, c := range n.children {
			out.children[k] = c.resolve(at.Child(k), now, existing)
		}
	}
	return &out
}

func (sv *ServerValue) resolve(at Path, now int64, existing map[string]*Node) *Node {
	if sv.Timestamp {
		return Number(float64(now))
	}
	if cur := existing[at.String()]; cur.Kind() == KindNumber {
		return Number(cur.n + sv.Increment)
	}
	return Number(sv.Increment)
}
