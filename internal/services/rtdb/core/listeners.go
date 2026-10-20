package core

import (
	"encoding/json"
)

// Push is a message the server sends a session without being asked: data (d), a merge (m) or
// a cancelled listen (c), as protocol pushes carry them (A-protocol-sdks.md §3).
type Push struct {
	Action string
	Path   Path
	// Data is the export-format JSON of a d push, or for an m push an object keyed by paths
	// relative to Path. nil means null.
	Data json.RawMessage
	// Tag is the client's tag of a filtered query listen, 0 for an untagged push.
	Tag int64
	// Query is the query of a cancelled listen, as the client sent it; nil for a default
	// listen.
	Query *QuerySpec
}

// Push actions.
const (
	PushData   = "d"
	PushMerge  = "m"
	PushCancel = "c"
)

// Sink receives a session's pushes, on the writer goroutine, in the database's order. It
// must not block: the transport queues them (a byte-bounded outbox).
type Sink interface {
	Push(p Push)
}

// listen is one registered listen of a session.
type listen struct {
	sess  *Session
	path  Path
	query Query
	// spec is the query as the client sent it, nil for a default listen.
	spec *QuerySpec
	// tag is set when the server filters the listen and tags its pushes: a non-default query
	// in key or priority order, or one whose index the rules declare (ADR-020 §1). An
	// untagged listen gets everything at its path, as written.
	tag int64
	// window is a tagged listen's current result: the children its query selects.
	window *Node
}

// key identifies a listen among its session's: the path and the query.
func (l *listen) key() string { return listenKey(l.path, l.query) }

func listenKey(p Path, q Query) string { return p.String() + "\x00" + q.ID() }

// listeners are a database's listens, in a trie by path, owned by its writer goroutine:
// registration, removal and fan-out all happen there, in the database's order.
type listeners struct {
	root      trie
	bySession map[*Session]map[string]*listen
}

type trie struct {
	children map[string]*trie
	listens  []*listen
}

func newListeners() *listeners {
	return &listeners{bySession: map[*Session]map[string]*listen{}}
}

// add registers l, replacing the session's listen with the same path and query. It returns
// the replaced listen, if any.
func (ls *listeners) add(l *listen) *listen {
	old := ls.remove(l.sess, l.key())
	t := &ls.root
	for _, k := range l.path {
		if t.children == nil {
			t.children = map[string]*trie{}
		}
		c := t.children[k]
		if c == nil {
			c = &trie{}
			t.children[k] = c
		}
		t = c
	}
	t.listens = append(t.listens, l)
	m := ls.bySession[l.sess]
	if m == nil {
		m = map[string]*listen{}
		ls.bySession[l.sess] = m
	}
	m[l.key()] = l
	return old
}

// remove drops a session's listen by key, returning it.
func (ls *listeners) remove(s *Session, key string) *listen {
	l := ls.bySession[s][key]
	if l == nil {
		return nil
	}
	delete(ls.bySession[s], key)
	if len(ls.bySession[s]) == 0 {
		delete(ls.bySession, s)
	}
	// Walk down, remembering the way, and prune empty branches on the way back.
	trail := []*trie{&ls.root}
	t := &ls.root
	for _, k := range l.path {
		t = t.children[k]
		trail = append(trail, t)
	}
	for i, x := range t.listens {
		if x == l {
			t.listens = append(t.listens[:i:i], t.listens[i+1:]...)
			break
		}
	}
	for i := len(l.path); i > 0; i-- {
		n := trail[i]
		if len(n.listens) > 0 || len(n.children) > 0 {
			break
		}
		delete(trail[i-1].children, l.path[i-1])
	}
	return l
}

// of returns a session's listens.
func (ls *listeners) of(s *Session) []*listen {
	out := make([]*listen, 0, len(ls.bySession[s]))
	for _, l := range ls.bySession[s] {
		out = append(out, l)
	}
	return out
}

// around calls fn for every listen at p, above it, or below it: those a write at p can
// concern. Each listen is visited once.
func (ls *listeners) around(p Path, fn func(*listen)) {
	t := &ls.root
	for _, l := range t.listens {
		fn(l)
	}
	for _, k := range p {
		if t = t.children[k]; t == nil {
			return
		}
		for _, l := range t.listens {
			fn(l)
		}
	}
	var below func(*trie)
	below = func(t *trie) {
		for _, c := range t.children {
			for _, l := range c.listens {
				fn(l)
			}
			below(c)
		}
	}
	below(t)
}
