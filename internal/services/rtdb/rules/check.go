package rules

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Env is what a check needs besides the request: who asks, the clock, and the stored data.
type Env struct {
	Auth Auth
	// Now is the server's clock, the now variable. The zero time means time.Now.
	Now time.Time
	// Data is the stored data, as it is before the write.
	Data Source
}

// Query is the query a read asks for, as the query variable shows it to rules.
type Query struct {
	OrderByKey, OrderByPriority, OrderByValue bool
	// OrderByChild is the child path a query orders by, or "" when it orders otherwise.
	OrderByChild string
	// StartAt, EndAt and EqualTo are the bounds' values (string, float64, bool) or nil.
	StartAt, EndAt, EqualTo any
	// LimitToFirst and LimitToLast are 0 when the query sets no limit.
	LimitToFirst, LimitToLast int
}

// The query of a read with no query parameters depends on the transport (emulator 4.11.2): a
// listen over the protocol is ordered by priority, a REST GET by key.
var (
	PlainListen = Query{OrderByPriority: true}
	PlainREST   = Query{OrderByKey: true}
)

func (q *Query) field(name string) any {
	switch name {
	case "orderByKey":
		return q.OrderByKey
	case "orderByPriority":
		return q.OrderByPriority
	case "orderByValue":
		return q.OrderByValue
	case "orderByChild":
		if q.OrderByChild == "" {
			return nil
		}
		return q.OrderByChild
	case "startAt":
		return q.StartAt
	case "endAt":
		return q.EndAt
	case "equalTo":
		return q.EqualTo
	case "limitToFirst":
		if q.LimitToFirst == 0 {
			return nil
		}
		return float64(q.LimitToFirst)
	case "limitToLast":
		if q.LimitToLast == 0 {
			return nil
		}
		return float64(q.LimitToLast)
	}
	return nil
}

// Change is one location a write replaces and the value it puts there; a nil value deletes.
// A set is one Change; an update is one Change per child it names (see Update).
type Change struct {
	Path  []string
	Value any
}

// Update expands an update at path into its changes: each key, which may be a deeper path
// such as "a/b", replaces the value at path/key. Core rejects overlapping keys before asking.
func Update(path []string, values map[string]any) []Change {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Change, 0, len(keys))
	for _, k := range keys {
		p := append([]string(nil), path...)
		for _, seg := range strings.Split(k, "/") {
			if seg != "" {
				p = append(p, seg)
			}
		}
		out = append(out, Change{Path: p, Value: values[k]})
	}
	return out
}

// CanRead reports whether env.Auth may read path with query q. The first .read on the way from
// the root to path that grants decides: a grant above cannot be taken back below, and a grant
// below path does not allow reading path. The error is a failure to read env.Data, never a
// denial.
func (rs *Ruleset) CanRead(ctx context.Context, env Env, path []string, q Query) (bool, error) {
	return rs.canRead(ctx, env, path, q, nil)
}

// tracer observes each rule a check evaluates, for tests: the rule's place in the document and
// the data path it ran at.
type tracer func(rule string, at []string)

func (rs *Ruleset) canRead(ctx context.Context, env Env, path []string, q Query, trace tracer) (bool, error) {
	if env.Auth.admin {
		return true, nil
	}
	w := &world{ctx: ctx, src: env.Data, cache: map[string]Node{}}
	ev := newEvaluator(w, env, trace)
	ev.query = &q
	n := rs.root
	for depth := 0; n != nil; depth++ {
		if n.read != nil {
			ev.path = path[:depth]
			ok := ev.run(n.read)
			if w.err != nil {
				return false, w.err
			}
			if ok {
				return true, nil
			}
		}
		if depth == len(path) {
			break
		}
		n = n.descend(path[depth], ev.vars)
	}
	return false, nil
}

// CanWrite reports whether env.Auth may make all of changes at once. For each change, a .write
// on the way from the root to the change's path must grant, and every .validate must pass on
// that way and on every node the change writes, except where the new data is null. newData is
// the stored data with all the changes applied; root and data are the stored data. No changes
// at all, an empty update, is allowed.
//
// onDisconnect writes are checked when they are registered, like other writes, with two
// differences the emulator 4.11.2 shows: an onDisconnect update is checked one child at a time,
// each as its own CanWrite against the stored data, and cancelling checks no rule at all.
func (rs *Ruleset) CanWrite(ctx context.Context, env Env, changes ...Change) (bool, error) {
	return rs.canWrite(ctx, env, nil, changes)
}

func (rs *Ruleset) canWrite(ctx context.Context, env Env, trace tracer, changes []Change) (bool, error) {
	if env.Auth.admin {
		return true, nil
	}
	w := &world{ctx: ctx, src: env.Data, cache: map[string]Node{}}
	for _, c := range changes {
		v, err := normalize(c.Value, 0)
		if err != nil {
			return false, err
		}
		w.changes = append(w.changes, change{path: c.Path, value: v})
	}
	ev := newEvaluator(w, env, trace)
	ev.query = &Query{} // a write has no query: every field is false or null (emulator 4.11.2)
	validated := map[validation]bool{}
	for _, c := range w.changes {
		ok := rs.checkChange(ev, c, validated)
		if w.err != nil {
			return false, w.err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// validation names one .validate at one path, so that ancestors shared by several changes
// are validated once.
type validation struct {
	r    *rule
	path string
}

func (rs *Ruleset) checkChange(ev *evaluator, c change, validated map[validation]bool) bool {
	ev.vars = map[string]string{}
	granted := false
	n := rs.root
	depth := 0
	for ; n != nil; depth++ {
		path := c.path[:depth]
		ev.path = path
		if !granted && n.write != nil && ev.run(n.write) {
			granted = true
		}
		if n.validate != nil && ev.w.fresh(path).exists() && !ev.validate(n.validate, validated) {
			return false
		}
		if depth == len(c.path) {
			break
		}
		n = n.descend(c.path[depth], ev.vars)
	}
	if !granted || n == nil || depth != len(c.path) {
		return granted
	}
	if obj, ok := c.value.(map[string]any); ok {
		return ev.validateTree(n, c.path, obj, validated)
	}
	return true
}

// validateTree runs the .validate rules of every node a change writes below its path.
func (ev *evaluator) validateTree(n *node, path []string, obj map[string]any, validated map[validation]bool) bool {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	outer := ev.vars
	for _, k := range keys {
		child, ok := n.children[k]
		vars := outer
		if !ok && n.wild != nil {
			child = n.wild
			vars = make(map[string]string, len(outer)+1)
			for name, v := range outer {
				vars[name] = v
			}
			vars[n.wildName] = k
		}
		if child == nil {
			continue
		}
		p := append(path[:len(path):len(path)], k)
		ev.vars, ev.path = vars, p
		if child.validate != nil && !ev.validate(child.validate, validated) {
			return false
		}
		if sub, ok := obj[k].(map[string]any); ok && !ev.validateTree(child, p, sub, validated) {
			return false
		}
	}
	ev.vars = outer
	return true
}

func (ev *evaluator) validate(r *rule, validated map[validation]bool) bool {
	key := validation{r: r, path: pathKey(ev.path)}
	if ok, seen := validated[key]; seen {
		return ok
	}
	ok := ev.run(r)
	validated[key] = ok
	return ok
}

// descend returns the rules for child key k and binds a wildcard's variable in vars. An inner
// wildcard with the same name as an outer one shadows it (emulator 4.11.2).
func (n *node) descend(k string, vars map[string]string) *node {
	if c, ok := n.children[k]; ok {
		return c
	}
	if n.wild != nil {
		vars[n.wildName] = k
	}
	return n.wild
}

func newEvaluator(w *world, env Env, trace tracer) *evaluator {
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	ev := &evaluator{w: w, now: float64(now.UnixMilli()), vars: map[string]string{}, trace: trace}
	if env.Auth.variable != nil {
		ev.auth = env.Auth.variable
	}
	return ev
}
