// Package rules is the Realtime Database security rules language: it loads a rules document,
// checks every expression when it is loaded, and decides whether a read or a write is allowed.
//
// The language follows the official reference (firebase.google.com/docs/reference/security/
// database) and, where the reference is silent, what the official emulator 4.11.2 does; tests
// replay verdicts captured from the emulator (testdata/emulator) and targaryen's 186 expression
// cases (testdata/targaryen). The package imports nothing from the rest of the service: it reads
// data through the Source interface in snapshot.go, which core backs with Postgres.
//
// What Aruma does not support is refused when the rules are loaded, never denied silently at run
// time: getPriority(), and regular expressions Go's regexp
// cannot run with Firebase's meaning (see regex.go). auth.provider, refused until R4, is
// supported since the verified ID tokens give it its value (auth.go).
package rules

import (
	"fmt"
	"strings"
)

// MaxSize bounds a rules document. Firebase documents no limit for Realtime Database rules; in a
// sample of 2255 public rules files the median was 1.2 KB and the largest 150 KB
// so 1 MiB refuses nothing real while keeping a
// hostile upload cheap to reject.
const MaxSize = 1 << 20

// maxErrors caps how many expression errors one load reports.
const maxErrors = 20

// Error is one reason a rules document cannot be loaded. Line and Column are 1-based and count
// characters in the document, like the "1:26:" prefix Firebase puts on its messages. Path names
// the rule, such as "rules/users/$uid/.write", and is empty for an error in the document's shape.
type Error struct {
	Line, Column int
	Path         string
	Msg          string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return fmt.Sprintf("%d:%d: %s", e.Line, e.Column, e.Msg)
	}
	return fmt.Sprintf("%d:%d: %s (%s)", e.Line, e.Column, e.Msg, e.Path)
}

// Errors is every error found in one document, in document order. Firebase reports all the
// expression errors of a document at once, one per line.
type Errors []*Error

func (es Errors) Error() string {
	lines := make([]string, len(es))
	for i, e := range es {
		lines[i] = e.Error()
	}
	return strings.Join(lines, "\n")
}

// Ruleset is a loaded rules document. It is immutable and safe for concurrent use.
type Ruleset struct {
	root *node
}

// node is the rules at one level of the tree.
type node struct {
	read, write, validate *rule
	indexOn               []string
	children              map[string]*node
	wildName              string // "$uid", the variable a wildcard child binds
	wild                  *node
}

// rule is one .read, .write or .validate.
type rule struct {
	path string // "rules/users/$uid/.write"
	expr expr
}

type ruleKind uint8

const (
	kindRead ruleKind = iota
	kindWrite
	kindValidate
)

// Parse loads a rules document. A document Firebase would refuse is an error, and so is one
// that uses what Aruma does not support. The error is an *Error for a malformed document and
// Errors for problems in the expressions.
func Parse(src []byte) (*Ruleset, error) {
	if len(src) > MaxSize {
		return nil, &Error{Line: 1, Column: 1, Msg: fmt.Sprintf("Rules are larger than %d bytes.", MaxSize)}
	}
	doc, err := readDocument(src)
	if err != nil {
		return nil, err
	}
	b := &builder{src: src}
	if doc.kind != jsonObject {
		return nil, b.at(doc.off, "", "Expected '{'.")
	}
	var rulesVal *jsonValue
	for _, m := range doc.members {
		if m.key != "rules" || rulesVal != nil {
			return nil, b.at(m.keyOff, "", "Expected '}'.")
		}
		rulesVal = m.val
	}
	if rulesVal == nil {
		return nil, b.at(doc.off+1, "", "Expected 'rules' property.")
	}
	root, err := b.node(rulesVal, "rules", nil)
	if err != nil {
		return nil, err
	}
	if len(b.errs) > 0 {
		return nil, b.errs
	}
	return &Ruleset{root: root}, nil
}

type builder struct {
	src  []byte
	errs Errors
}

func (b *builder) at(off int, path, msg string) *Error {
	line, col := position(b.src, off)
	return &Error{Line: line, Column: col, Path: path, Msg: msg}
}

// node builds one level of the tree. vars are the wildcard names bound above it.
func (b *builder) node(v *jsonValue, path string, vars []string) (*node, error) {
	if v.kind != jsonObject {
		return nil, b.at(v.off, path, "Expected '{'.")
	}
	n := &node{}
	seen := map[string]bool{}
	for _, m := range v.members {
		childPath := path + "/" + m.key
		// Firebase reports a problem with a child key once it has read the child, at its end.
		end := m.val.off
		if m.val.kind == jsonObject {
			end = m.val.end
		}
		if seen[m.key] {
			if strings.HasPrefix(m.key, ".") {
				return nil, b.at(m.keyOff, path, fmt.Sprintf("Cannot have multiple '%s' rules.", m.key))
			}
			return nil, b.at(end, path, fmt.Sprintf("'%s' occurs multiple times.", m.key))
		}
		seen[m.key] = true
		switch {
		case m.key == ".read" || m.key == ".write" || m.key == ".validate":
			kind := map[string]ruleKind{".read": kindRead, ".write": kindWrite, ".validate": kindValidate}[m.key]
			r, err := b.rule(m.val, childPath, kind, vars)
			if err != nil {
				return nil, err
			}
			switch kind {
			case kindRead:
				n.read = r
			case kindWrite:
				n.write = r
			default:
				n.validate = r
			}
		case m.key == ".indexOn":
			idx, err := b.indexOn(m.val, childPath)
			if err != nil {
				return nil, err
			}
			n.indexOn = idx
		case strings.HasPrefix(m.key, "."):
			return nil, b.at(m.val.off, childPath, "Expected '{'.")
		case strings.HasPrefix(m.key, "$"):
			if !validKey(m.key[1:]) {
				return nil, b.at(end, childPath, `String can't contain ".", "#", "$", "/", "[", or "]"`)
			}
			if n.wild != nil {
				return nil, b.at(end, childPath, fmt.Sprintf("Cannot have multiple default rules ('%s' and '%s').", n.wildName, m.key))
			}
			child, err := b.node(m.val, childPath, append(vars[:len(vars):len(vars)], m.key))
			if err != nil {
				return nil, err
			}
			n.wildName, n.wild = m.key, child
		default:
			if m.key == "" {
				return nil, b.at(m.keyOff, childPath, "Unexpected empty string.")
			}
			if !validKey(m.key) {
				return nil, b.at(end, childPath, `String can't contain ".", "#", "$", "/", "[", or "]"`)
			}
			child, err := b.node(m.val, childPath, vars)
			if err != nil {
				return nil, err
			}
			if n.children == nil {
				n.children = map[string]*node{}
			}
			n.children[m.key] = child
		}
	}
	return n, nil
}

// rule compiles one rule value: true, false, or an expression string.
func (b *builder) rule(v *jsonValue, path string, kind ruleKind, vars []string) (*rule, error) {
	switch v.kind {
	case jsonBool:
		return &rule{path: path, expr: &literal{val: v.boolean}}, nil
	case jsonString:
	default:
		return nil, b.at(v.off, path, "Invalid rule expression.  Expected 'true', 'false', or an expression string.")
	}
	e, perr := parseExpr(v.str)
	if perr == nil {
		perr = check(e, kind, vars)
	}
	if perr != nil {
		if len(b.errs) < maxErrors {
			off := v.offs[min(perr.pos, len(v.offs)-1)]
			b.errs = append(b.errs, b.at(off, path, perr.msg))
		}
		return &rule{path: path}, nil
	}
	return &rule{path: path, expr: e}, nil
}

func (b *builder) indexOn(v *jsonValue, path string) ([]string, error) {
	switch v.kind {
	case jsonString:
		return []string{v.str}, nil
	case jsonArray:
		if len(v.elems) == 0 {
			return nil, b.at(v.off+1, path, "Cannot have an empty .indexOn declaration")
		}
		keys := make([]string, len(v.elems))
		for i, el := range v.elems {
			if el.kind != jsonString {
				return nil, b.at(el.off, path, "Invalid indexOn expression. Expected ']'")
			}
			keys[i] = el.str
		}
		return keys, nil
	default:
		return nil, b.at(v.off, path, "Invalid indexOn expression. Must be either a string or an array of strings")
	}
}

// validKey reports whether a rules key may name a child: not empty, none of . # $ / [ ], and no
// control characters, the same rule as for data keys.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for _, c := range k {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(".#$/[]", c) {
			return false
		}
	}
	return true
}

// IndexOn returns the keys the rules at path declare with .indexOn, or nil when they declare
// none. A key is a child path such as "score" or "meta/created", or ".value". The lookup follows
// wildcards but does not inherit: an index applies only at the level that declares it.
func (rs *Ruleset) IndexOn(path []string) []string {
	n := rs.root
	for _, k := range path {
		if n = n.child(k); n == nil {
			return nil
		}
	}
	return n.indexOn
}

// child returns the rules for a child key, preferring a named child over the wildcard.
func (n *node) child(k string) *node {
	if c, ok := n.children[k]; ok {
		return c
	}
	return n.wild
}
