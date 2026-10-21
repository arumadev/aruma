package core

import (
	"bytes"
	"encoding/json"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kind is what a node holds.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindObject
	// kindServerValue is a placeholder ({".sv": ...}) that the writer replaces before storing.
	kindServerValue
)

// Node is a value in a database: a leaf (boolean, number or string) or an object of non-null
// children, either with an optional priority (a number or a string), as in the SDKs'
// LeafNode and ChildrenNode. A nil *Node is null, which is also an empty object: setting {}
// deletes. Nodes are immutable once built.
type Node struct {
	kind     Kind
	b        bool
	n        float64
	s        string
	sv       *ServerValue
	children map[string]*Node
	priority *Node // nil, or a number, string or server value leaf
}

// Bool returns a boolean leaf.
func Bool(b bool) *Node { return &Node{kind: KindBool, b: b} }

// Number returns a number leaf. Numbers are IEEE-754 doubles, as in JavaScript; -0 becomes 0,
// which is what JSON.stringify sends for it.
func Number(f float64) *Node {
	if f == 0 {
		f = 0
	}
	return &Node{kind: KindNumber, n: f}
}

// String returns a string leaf.
func String(s string) *Node { return &Node{kind: KindString, s: s} }

// Object returns an object of the non-null children, or null when none is left.
func Object(children map[string]*Node) *Node {
	out := make(map[string]*Node, len(children))
	for k, c := range children {
		if c != nil {
			out[k] = c
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &Node{kind: KindObject, children: out}
}

// Kind returns what n holds; KindNull for nil.
func (n *Node) Kind() Kind {
	if n == nil {
		return KindNull
	}
	return n.kind
}

// IsNull reports whether n is null.
func (n *Node) IsNull() bool { return n == nil }

// IsLeaf reports whether n is a boolean, number or string (or a server value placeholder).
func (n *Node) IsLeaf() bool { return n != nil && n.kind != KindObject }

// BoolValue, NumberValue and StringValue return a leaf's value; the zero value otherwise.
func (n *Node) BoolValue() bool      { return n != nil && n.kind == KindBool && n.b }
func (n *Node) NumberValue() float64 { return n.numberOr(0) }
func (n *Node) StringValue() string {
	if n == nil || n.kind != KindString {
		return ""
	}
	return n.s
}

func (n *Node) numberOr(f float64) float64 {
	if n == nil || n.kind != KindNumber {
		return f
	}
	return n.n
}

// Priority returns the priority, nil when none.
func (n *Node) Priority() *Node {
	if n == nil {
		return nil
	}
	return n.priority
}

// WithPriority returns n with priority p (nil removes it). Null has no priority: setting one
// on it leaves null, as EMPTY_NODE.updatePriority does.
func (n *Node) WithPriority(p *Node) *Node {
	if n == nil {
		return nil
	}
	c := *n
	c.priority = p
	return &c
}

// Len returns the number of children.
func (n *Node) Len() int {
	if n == nil || n.kind != KindObject {
		return 0
	}
	return len(n.children)
}

// Child returns the child named key, nil when there is none. The .priority pseudo-child is
// the priority, as getImmediateChild does in the SDKs.
func (n *Node) Child(key string) *Node {
	if n == nil {
		return nil
	}
	if key == PriorityKey {
		return n.priority
	}
	return n.children[key]
}

// Get returns the node at p below n.
func (n *Node) Get(p Path) *Node {
	for _, k := range p {
		if n = n.Child(k); n == nil {
			return nil
		}
	}
	return n
}

// Keys returns the names of the children in key order.
func (n *Node) Keys() []string {
	if n.Len() == 0 {
		return nil
	}
	return slices.SortedFunc(maps.Keys(n.children), CompareKeys)
}

// Set returns n with the node at p replaced by v, following the SDKs' updateChild: a leaf
// that gains a child becomes an object and keeps its priority; an object that loses its
// last child becomes null; writing p/.priority sets the priority of p.
func (n *Node) Set(p Path, v *Node) *Node {
	if len(p) == 0 {
		return v
	}
	key := p[0]
	if key == PriorityKey {
		// Only a priority path's last key may be .priority.
		return n.WithPriority(v)
	}
	if n.IsLeaf() {
		if v == nil {
			return n
		}
		return (&Node{kind: KindObject, children: map[string]*Node{}, priority: n.priority}).Set(p, v)
	}
	child := n.Child(key).Set(p[1:], v)
	var children map[string]*Node
	var prio *Node
	if n != nil {
		children = maps.Clone(n.children)
		prio = n.priority
	}
	if children == nil {
		children = map[string]*Node{}
	}
	if child == nil {
		delete(children, key)
	} else {
		children[key] = child
	}
	if len(children) == 0 {
		return nil
	}
	return &Node{kind: KindObject, children: children, priority: prio}
}

// Equal reports whether a and b hold the same value and priorities.
func Equal(a, b *Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.kind != b.kind || !Equal(a.priority, b.priority) {
		return false
	}
	switch a.kind {
	case KindBool:
		return a.b == b.b
	case KindNumber:
		return a.n == b.n
	case KindString:
		return a.s == b.s
	case KindObject:
		if len(a.children) != len(b.children) {
			return false
		}
		for k, c := range a.children {
			if !Equal(c, b.children[k]) {
				return false
			}
		}
		return true
	case kindServerValue:
		return *a.sv == *b.sv
	}
	return false
}

// leaves calls fn for every leaf below n with its path relative to n, and for every object
// that carries a priority, with leaf false. This is how nodes become storage rows.
func (n *Node) leaves(at Path, fn func(p Path, n *Node, leaf bool) error) error {
	if n == nil {
		return nil
	}
	if n.IsLeaf() {
		return fn(at, n, true)
	}
	if n.priority != nil {
		if err := fn(at, n, false); err != nil {
			return err
		}
	}
	for k, c := range n.children {
		if err := c.leaves(at.Child(k), fn); err != nil {
			return err
		}
	}
	return nil
}

// ExportJSON encodes n in the SDKs' export format: priorities as ".priority", a leaf with a
// priority as {".value":v,".priority":p}, objects always as objects. The WebSocket protocol
// carries this format (val(true) in the SDKs); null encodes as null.
func (n *Node) ExportJSON() []byte { return n.appendJSON(nil, jsonExport) }

// JSON encodes n as REST answers without format=export: no priorities, and an object whose
// keys are all array indexes, dense enough, becomes an array (ChildrenNode.val: every key
// matches ^(0|[1-9]\d*)$ and the largest is below twice the count).
func (n *Node) JSON() []byte { return n.appendJSON(nil, jsonPlain) }

// EchoJSON encodes n as REST echoes a value it wrote: priorities as in the export format, and
// an object without a priority whose keys make an array as an array (emulator 4.11.2, golden
// rest.golden.json "data shapes").
func (n *Node) EchoJSON() []byte { return n.appendJSON(nil, jsonEcho) }

// The encodings of a node.
type jsonMode uint8

const (
	jsonPlain jsonMode = iota
	jsonExport
	jsonEcho
)

func (n *Node) appendJSON(b []byte, mode jsonMode) []byte {
	export := mode != jsonPlain
	if n == nil {
		return append(b, "null"...)
	}
	if n.kind != KindObject {
		if export && n.priority != nil {
			b = append(b, `{".value":`...)
			b = n.appendLeaf(b)
			b = append(b, `,".priority":`...)
			b = n.priority.appendLeaf(b)
			return append(b, '}')
		}
		return n.appendLeaf(b)
	}
	keys := n.Keys()
	if !export || mode == jsonEcho && n.priority == nil {
		if size, ok := arrayLen(keys); ok {
			b = append(b, '[')
			i := 0
			for idx := range size {
				if idx > 0 {
					b = append(b, ',')
				}
				if i < len(keys) && keys[i] == strconv.Itoa(idx) {
					b = n.children[keys[i]].appendJSON(b, mode)
					i++
				} else {
					b = append(b, "null"...)
				}
			}
			return append(b, ']')
		}
	}
	b = append(b, '{')
	first := true
	if export && n.priority != nil {
		b = append(b, `".priority":`...)
		b = n.priority.appendLeaf(b)
		first = false
	}
	for _, k := range keys {
		if !first {
			b = append(b, ',')
		}
		first = false
		b = appendJSONString(b, k)
		b = append(b, ':')
		b = n.children[k].appendJSON(b, mode)
	}
	return append(b, '}')
}

// arrayLen reports whether keys, in key order, make an array, and its length.
func arrayLen(keys []string) (int, bool) {
	maxKey := 0
	for _, k := range keys {
		if !arrayIndex(k) {
			return 0, false
		}
		v, err := strconv.Atoi(k)
		if err != nil {
			return 0, false
		}
		maxKey = max(maxKey, v)
	}
	if maxKey >= 2*len(keys) {
		return 0, false
	}
	return maxKey + 1, true
}

// arrayIndex matches ^(0|[1-9]\d*)$ (ChildrenNode.ts:194).
func arrayIndex(k string) bool {
	if k == "0" {
		return true
	}
	if k == "" || k[0] < '1' || k[0] > '9' || len(k) > 10 {
		return false
	}
	for i := 1; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return false
		}
	}
	return true
}

func (n *Node) appendLeaf(b []byte) []byte {
	switch n.kind {
	case KindBool:
		return strconv.AppendBool(b, n.b)
	case KindNumber:
		return appendNumber(b, n.n)
	case KindString:
		return appendJSONString(b, n.s)
	case kindServerValue:
		return n.sv.appendJSON(b)
	}
	return append(b, "null"...)
}

// ParseData parses data written at path at, in the export format the SDKs send, as their
// nodeFromJSON reads it after validateFirebaseData accepts it (nodeFromJSON.ts,
// validation.ts): ".priority" sets a priority, ".value" holds a leaf that has one, {".sv":...}
// is a server value, arrays are objects keyed by index, and null children vanish. Every key
// counts towards MaxDepth and MaxPathBytes together with at, metadata keys included, as in
// the SDK. A string longer than maxString bytes is ErrTooBig.
func ParseData(data []byte, at Path, maxString int) (*Node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	p := parser{dec: dec, data: data, maxString: maxString}
	v, err := p.value(len(at), at.byteLen())
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrInvalidData
	}
	return v.node()
}

// jv is parsed JSON before the export format is interpreted.
type jv struct {
	kind Kind
	b    bool
	n    float64
	s    string
	keys []string // object keys in order, without duplicates
	vals map[string]*jv
}

type parser struct {
	dec       *json.Decoder
	data      []byte
	maxString int
}

// KeyError is ErrInvalidKey at a place in the data: the line and column, from 1, of the key's
// opening quote, which REST names as the emulator does ("couldn't parse key beginning at
// 1:2"). It unwraps to ErrInvalidKey.
type KeyError struct{ Line, Column int }

func (e *KeyError) Error() string { return ErrInvalidKey.Error() }
func (e *KeyError) Unwrap() error { return ErrInvalidKey }

// keyError locates the key the decoder reads next, which starts after off.
func (p *parser) keyError(off int64) error {
	i := int(off)
	for i < len(p.data) && strings.IndexByte(" \t\r\n,", p.data[i]) >= 0 {
		i++
	}
	line, col := 1, 1
	for _, r := range string(p.data[:min(i, len(p.data))]) {
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return &KeyError{Line: line, Column: col}
}

// value reads one JSON value whose location has depth keys and pathBytes bytes.
func (p *parser) value(depth, pathBytes int) (*jv, error) {
	tok, err := p.dec.Token()
	if err != nil {
		return nil, ErrInvalidData
	}
	switch t := tok.(type) {
	case nil:
		return &jv{kind: KindNull}, nil
	case bool:
		return &jv{kind: KindBool, b: t}, nil
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil || math.IsInf(f, 0) {
			return nil, ErrInvalidData
		}
		return &jv{kind: KindNumber, n: f}, nil
	case string:
		if len(t) > p.maxString {
			return nil, ErrTooBig
		}
		return &jv{kind: KindString, s: t}, nil
	case json.Delim:
		obj := &jv{kind: KindObject, vals: map[string]*jv{}}
		for i := 0; p.dec.More(); i++ {
			key := strconv.Itoa(i)
			if t == '{' {
				off := p.dec.InputOffset()
				k, err := p.dec.Token()
				if err != nil {
					return nil, ErrInvalidData
				}
				key = k.(string)
				if key != ".value" && key != ".priority" && key != ".sv" && !ValidKey(key) {
					return nil, p.keyError(off)
				}
			}
			childBytes := pathBytes + len(key)
			if depth+1 > MaxDepth {
				return nil, ErrPathTooDeep
			}
			if childBytes > MaxPathBytes {
				return nil, ErrPathTooLong
			}
			child, err := p.value(depth+1, childBytes)
			if err != nil {
				return nil, err
			}
			if _, dup := obj.vals[key]; !dup {
				obj.keys = append(obj.keys, key)
			}
			obj.vals[key] = child
		}
		if _, err := p.dec.Token(); err != nil { // the closing delimiter
			return nil, ErrInvalidData
		}
		return obj, nil
	}
	return nil, ErrInvalidData
}

// node interprets the export format.
func (v *jv) node() (*Node, error) {
	switch v.kind {
	case KindNull:
		return nil, nil
	case KindBool:
		return Bool(v.b), nil
	case KindNumber:
		return Number(v.n), nil
	case KindString:
		return String(v.s), nil
	}
	_, hasValue := v.vals[".value"]
	_, hasSV := v.vals[".sv"]
	actual := false
	for _, k := range v.keys {
		switch k {
		case ".value", ".priority", ".sv":
		default:
			if !ValidKey(k) {
				return nil, ErrInvalidKey
			}
			actual = true
		}
	}
	if (hasValue || hasSV) && actual || hasValue && hasSV {
		return nil, ErrInvalidData
	}
	prio, err := v.priority()
	if err != nil {
		return nil, err
	}
	var n *Node
	switch {
	case hasSV:
		sv, err := parseServerValue(v.vals[".sv"])
		if err != nil {
			return nil, err
		}
		n = &Node{kind: kindServerValue, sv: sv}
	case hasValue:
		if n, err = v.vals[".value"].node(); err != nil {
			return nil, err
		}
	default:
		children := make(map[string]*Node, len(v.keys))
		for _, k := range v.keys {
			if k[0] == '.' {
				continue
			}
			c, err := v.vals[k].node()
			if err != nil {
				return nil, err
			}
			children[k] = c
		}
		n = Object(children)
	}
	return n.WithPriority(prio), nil
}

// priority reads ".priority": null, a number, a string or a server value (validatePriority
// in validation.ts).
func (v *jv) priority() (*Node, error) {
	p, ok := v.vals[".priority"]
	if !ok {
		return nil, nil
	}
	switch p.kind {
	case KindNull:
		return nil, nil
	case KindNumber, KindString:
		return p.node()
	case KindObject:
		if sv, ok := p.vals[".sv"]; ok && len(p.keys) == 1 {
			s, err := parseServerValue(sv)
			if err != nil {
				return nil, err
			}
			return &Node{kind: kindServerValue, sv: s}, nil
		}
	}
	return nil, ErrInvalidPriority
}

// appendNumber formats f as JavaScript's Number.prototype.toString does, so numbers read back
// exactly as an SDK wrote them: integers without a fraction, exponents from 1e21 and below
// 1e-6 (the same rule encoding/json follows).
func appendNumber(b []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	b = strconv.AppendFloat(b, f, format, -1, 64)
	if format == 'e' {
		// 1e-07 becomes 1e-7.
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return b
}

// appendJSONString quotes s as JSON.stringify does: only ", \ and control characters are
// escaped.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			if c < utf8.RuneSelf {
				i++
				continue
			}
			_, size := utf8.DecodeRuneInString(s[i:])
			i += size
			continue
		}
		b = append(b, s[start:i]...)
		switch c {
		case '"', '\\':
			b = append(b, '\\', c)
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		default:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
		}
		i++
		start = i
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
