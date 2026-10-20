package core

import (
	"strings"
)

// Path is a location in a database: its keys from the root, which is the empty Path. Paths
// are values; methods never modify their receiver.
type Path []string

// Protocol limits the SDKs enforce before sending (Path.ts:23,26). The server enforces them
// again on everything it receives.
const (
	// MaxDepth is the most keys a location may have, its path and the keys inside the data
	// written there counted together.
	MaxDepth = 32
	// MaxPathBytes caps a location's path: the UTF-8 bytes of its keys, separators not
	// counted, as the emulator 4.11.2 counts them (wire/testdata/golden/limits.json). The SDK
	// counts the separators and a leading slash too (ValidationPath in Path.ts:264-330), so
	// everything it sends passes.
	MaxPathBytes = 768
)

// PriorityKey is the pseudo-child that addresses a node's priority: a write to a/.priority
// sets a's priority (setPriority in the SDKs).
const PriorityKey = ".priority"

// ParsePath parses a path as the SDKs send it: "/a/b" from the JS SDK, "a/b" from Android
// and iOS, "/" or "" for the root. Empty segments are dropped, as the SDKs' Path does.
func ParsePath(s string) (Path, error) {
	if s == "" || s == "/" {
		return Path{}, nil
	}
	var p Path
	for seg := range strings.SplitSeq(s, "/") {
		if seg == "" {
			continue
		}
		if !ValidKey(seg) {
			return nil, ErrInvalidPath
		}
		p = append(p, seg)
	}
	if err := p.checkSize(); err != nil {
		return nil, err
	}
	return p, nil
}

// ParseWritePath parses the path of a write, which may end in .priority to set a priority.
func ParseWritePath(s string) (Path, error) {
	trimmed := strings.TrimRight(s, "/")
	if base, ok := strings.CutSuffix(trimmed, "/"+PriorityKey); ok || trimmed == PriorityKey {
		p, err := ParsePath(base)
		if err != nil {
			return nil, err
		}
		p = p.Child(PriorityKey)
		if err := p.checkSize(); err != nil {
			return nil, err
		}
		return p, nil
	}
	return ParsePath(s)
}

// ValidKey reports whether k may name a child: not empty, and none of . # $ [ ] / or the
// ASCII control characters (validation.ts:45). Its length is bounded by MaxPathBytes.
func ValidKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		switch c := k[i]; {
		case c < 0x20 || c == 0x7f:
			return false
		case c == '.' || c == '#' || c == '$' || c == '[' || c == ']' || c == '/':
			return false
		}
	}
	return true
}

func (p Path) checkSize() error {
	if len(p) > MaxDepth {
		return ErrPathTooDeep
	}
	if p.byteLen() > MaxPathBytes {
		return ErrPathTooLong
	}
	return nil
}

// byteLen is the path's length as MaxPathBytes counts it.
func (p Path) byteLen() int {
	n := 0
	for _, k := range p {
		n += len(k)
	}
	return n
}

// String returns the path as stored and pushed: keys joined by "/", "" for the root.
func (p Path) String() string { return strings.Join(p, "/") }

// IsRoot reports whether p is the root.
func (p Path) IsRoot() bool { return len(p) == 0 }

// Child returns p extended by key.
func (p Path) Child(key string) Path {
	out := make(Path, len(p), len(p)+1)
	copy(out, p)
	return append(out, key)
}

// Join returns p extended by q.
func (p Path) Join(q Path) Path {
	out := make(Path, 0, len(p)+len(q))
	return append(append(out, p...), q...)
}

// Parent returns p without its last key; the root is its own parent.
func (p Path) Parent() Path {
	if len(p) == 0 {
		return p
	}
	return p[: len(p)-1 : len(p)-1]
}

// Last returns the last key, "" for the root.
func (p Path) Last() string {
	if len(p) == 0 {
		return ""
	}
	return p[len(p)-1]
}

// Contains reports whether q is p or lies below it.
func (p Path) Contains(q Path) bool {
	if len(q) < len(p) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// Rel returns q relative to p, which must contain it.
func (p Path) Rel(q Path) Path { return q[len(p):] }

// IsPriority reports whether p addresses a priority.
func (p Path) IsPriority() bool { return p.Last() == PriorityKey }
