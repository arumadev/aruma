package main

import (
	"cmp"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The two vendored trees, relative to the repository root (third_party/README.md).
const (
	baseDir = "third_party/googleapis"
	jsDir   = "third_party/firebase-js-sdk"
)

// Check compares every proto under jsDir with the googleapis copy at the same path and returns
// each breach of the merge rule.
func Check(root string) ([]string, error) {
	var problems []string
	js := filepath.Join(root, jsDir)
	err := filepath.WalkDir(js, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		rel, _ := filepath.Rel(js, path)
		jsFile, err := parseFile(path)
		if err != nil {
			return err
		}
		basePath := filepath.Join(root, baseDir, rel)
		if _, err := os.Stat(basePath); err != nil {
			problems = append(problems, fmt.Sprintf("%s: only in firebase-js-sdk; vendor the googleapis file first", rel))
			return nil
		}
		baseFile, err := parseFile(basePath)
		if err != nil {
			return err
		}
		problems = append(problems, compare(filepath.ToSlash(rel), baseFile, jsFile)...)
		return nil
	})
	return problems, err
}

// compare returns the breaches of the merge rule between one file's two copies.
func compare(rel string, base, js protoFile) []string {
	problems := slices.Clone(base.problems)
	for _, name := range slices.Sorted(maps.Keys(js.scopes)) {
		jsScope := js.scopes[name]
		bs, ok := base.scopes[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: %s %s is only in firebase-js-sdk", rel, jsScope.kind, name))
			continue
		}
		members := slices.SortedFunc(maps.Values(jsScope.byName), func(a, b member) int { return cmp.Compare(a.num, b.num) })
		for _, m := range members {
			where := fmt.Sprintf("%s: %s.%s = %d", rel, name, m.name, m.num)
			if b, ok := bs.byName[m.name]; ok {
				if b.num != m.num {
					problems = append(problems, fmt.Sprintf("%s has number %d in googleapis", where, b.num))
				}
				if b.typ != m.typ {
					problems = append(problems, fmt.Sprintf("%s is %q in firebase-js-sdk and %q in googleapis", where, m.typ, b.typ))
				}
				// repeated, optional and a oneof change the wire form or the presence of a field
				// whose type and number agree.
				if b.label != m.label {
					problems = append(problems, fmt.Sprintf("%s is %s in firebase-js-sdk and %s in googleapis", where, labelName(m.label), labelName(b.label)))
				}
				if b.oneof != m.oneof {
					problems = append(problems, fmt.Sprintf("%s is %s in firebase-js-sdk and %s in googleapis", where, oneofName(m.oneof), oneofName(b.oneof)))
				}
				continue
			}
			if b, ok := bs.byNum[m.num]; ok {
				problems = append(problems, fmt.Sprintf("%s collides with googleapis %s.%s = %d", where, name, b.name, b.num))
				continue
			}
			if bs.reserves(m.num) {
				problems = append(problems, fmt.Sprintf("%s collides with a number googleapis reserves", where))
				continue
			}
			problems = append(problems, fmt.Sprintf("%s is only in firebase-js-sdk; add it to the googleapis copy with that number", where))
		}
	}
	return problems
}

// protoFile is what the check needs of a .proto file: its messages and enums by qualified name
// (Outer.Inner, without the package).
type protoFile struct {
	scopes   map[string]*scope
	problems []string // duplicate numbers within one message
}

type scope struct {
	kind     string // "message" or "enum"
	byName   map[string]member
	byNum    map[int]member
	reserved [][2]int // inclusive ranges
}

// member is a field (typ is its type, "map<k,v>" for a map) or an enum value (typ is empty).
// label is a field's "repeated", "optional" or "required", empty for a singular field; oneof
// names the oneof that holds it.
type member struct {
	name, typ    string
	label, oneof string
	num          int
}

func labelName(l string) string { return cmp.Or(l, "singular") }

func oneofName(o string) string {
	if o == "" {
		return "outside any oneof"
	}
	return "in oneof " + o
}

func (s *scope) reserves(n int) bool {
	return slices.ContainsFunc(s.reserved, func(r [2]int) bool { return n >= r[0] && n <= r[1] })
}

func parseFile(path string) (protoFile, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return protoFile{}, err
	}
	f, err := parse(string(src))
	if err != nil {
		return protoFile{}, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// parse reads the subset of proto3 that the Firestore protos use: messages, enums and oneofs
// nested to any depth, fields with options, maps, and reserved ranges. Services, extensions and
// options are skipped.
func parse(src string) (protoFile, error) {
	p := &parser{toks: tokenize(src)}
	f := protoFile{scopes: map[string]*scope{}}
	type frame struct {
		name  string // qualified; a oneof's own name, whose fields belong to the message around it
		scope *scope // nil for a oneof
	}
	var stack []frame
	current := func() (string, *scope) {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].scope != nil {
				return stack[i].name, stack[i].scope
			}
		}
		return "", nil
	}
	add := func(m member) {
		name, s := current()
		if s == nil {
			return
		}
		if top := stack[len(stack)-1]; top.scope == nil {
			m.oneof = top.name
		}
		if prev, dup := s.byNum[m.num]; dup && s.kind == "message" {
			f.problems = append(f.problems, fmt.Sprintf("%s: %s and %s share number %d", name, prev.name, m.name, m.num))
		}
		s.byName[m.name] = m
		s.byNum[m.num] = m
	}
	for !p.done() {
		switch t := p.next(); t {
		case "syntax", "package", "import", "option":
			p.skipStatement()
		case "message", "enum":
			name := p.next()
			if p.next() != "{" {
				return f, fmt.Errorf("%s %s: expected {", t, name)
			}
			if outer, _ := current(); outer != "" {
				name = outer + "." + name
			}
			s := &scope{kind: t, byName: map[string]member{}, byNum: map[int]member{}}
			f.scopes[name] = s
			stack = append(stack, frame{name, s})
		case "oneof":
			name := p.next()
			if p.next() != "{" {
				return f, fmt.Errorf("oneof %s: expected {", name)
			}
			stack = append(stack, frame{name: name})
		case "service", "extend":
			p.skipBlock()
		case "reserved":
			_, s := current()
			for _, r := range p.reservedRanges() {
				if s != nil {
					s.reserved = append(s.reserved, r)
				}
			}
		case "}":
			if len(stack) == 0 {
				return f, fmt.Errorf("unbalanced }")
			}
			stack = stack[:len(stack)-1]
		case ";":
		default:
			_, s := current()
			if s == nil {
				return f, fmt.Errorf("unexpected %q at top level", t)
			}
			m, err := p.member(t, s.kind == "enum")
			if err != nil {
				return f, err
			}
			add(m)
		}
	}
	if len(stack) != 0 {
		return f, fmt.Errorf("unclosed %s", stack[len(stack)-1].name)
	}
	return f, nil
}

type parser struct {
	toks []string
	pos  int
}

func (p *parser) done() bool { return p.pos >= len(p.toks) }

func (p *parser) next() string {
	if p.done() {
		return ""
	}
	t := p.toks[p.pos]
	p.pos++
	return t
}

// skipStatement skips to the end of a statement, over an option's { } value.
func (p *parser) skipStatement() {
	depth := 0
	for !p.done() {
		switch p.next() {
		case "{":
			depth++
		case "}":
			depth--
		case ";":
			if depth == 0 {
				return
			}
		}
	}
}

// skipBlock skips a declaration up to and including its balanced { }.
func (p *parser) skipBlock() {
	for !p.done() && p.next() != "{" {
	}
	for depth := 1; depth > 0 && !p.done(); {
		switch p.next() {
		case "{":
			depth++
		case "}":
			depth--
		}
	}
}

// reservedRanges reads "reserved 2, 15, 9 to 11;" (names in quotes are skipped).
func (p *parser) reservedRanges() [][2]int {
	var out [][2]int
	for !p.done() {
		t := p.next()
		if t == ";" {
			return out
		}
		n, err := strconv.Atoi(t)
		if err != nil {
			continue
		}
		hi := n
		if p.pos+1 < len(p.toks) && p.toks[p.pos] == "to" {
			p.pos++
			if t := p.next(); t == "max" {
				hi = 536870911
			} else if v, err := strconv.Atoi(t); err == nil {
				hi = v
			}
		}
		out = append(out, [2]int{n, hi})
	}
	return out
}

// member reads a field ("repeated Type name = 3 [opts];", "map<K, V> name = 4;") or an enum value
// ("NAME = 2 [opts];"), first being its first token.
func (p *parser) member(first string, enum bool) (member, error) {
	var m member
	if enum {
		m.name = first
	} else {
		typ := first
		if typ == "repeated" || typ == "optional" || typ == "required" {
			m.label, typ = typ, p.next()
		}
		if typ == "map" {
			var b strings.Builder
			b.WriteString("map")
			for t := p.next(); ; t = p.next() {
				b.WriteString(t)
				if t == ">" || t == "" {
					break
				}
			}
			typ = b.String()
		}
		m.typ = typ
		m.name = p.next()
	}
	if p.next() != "=" {
		return m, fmt.Errorf("%s: expected =", m.name)
	}
	n, err := strconv.Atoi(p.next())
	if err != nil {
		return m, fmt.Errorf("%s: number: %w", m.name, err)
	}
	m.num = n
	p.skipStatement()
	return m, nil
}

// tokenize splits proto source into identifiers (dots included), numbers, quoted strings and
// single punctuation characters, dropping comments.
func tokenize(src string) []string {
	var toks []string
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.HasPrefix(src[i:], "//"):
			if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = len(src)
			}
		case strings.HasPrefix(src[i:], "/*"):
			if j := strings.Index(src[i+2:], "*/"); j >= 0 {
				i += j + 4
			} else {
				i = len(src)
			}
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			toks = append(toks, src[i:min(j+1, len(src))])
			i = j + 1
		case isWord(c) || c == '-' && i+1 < len(src) && isWord(src[i+1]):
			j := i + 1
			for j < len(src) && (isWord(src[j]) || src[j] == '.') {
				j++
			}
			toks = append(toks, src[i:j])
			i = j
		default:
			toks = append(toks, string(c))
			i++
		}
	}
	return toks
}

func isWord(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
