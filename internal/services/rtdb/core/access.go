package core

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/services/rtdb/rules"
)

// Security rules govern every operation of a session that is not an admin (ADR-020 §2). The
// writer checks them, inside the transaction a command runs in, so a rule reads the data as
// the command will find it: after the writes queued before it, and before its own. Every
// check that passes admits the session against its project's connection limit, so clients
// the rules turn away take no slot.
//
// A failure to read the data while checking is a storage error like any other: the command
// fails with unavailable, never with permission_denied.

// openRules are the test-mode rules {".read": true, ".write": true}: what dev_open_rules
// gives clients on this machine instead of their database's rules. .indexOn still comes from
// the database's own rules.
var openRules = func() *rules.Ruleset {
	rs, err := rules.Parse([]byte(`{"rules": {".read": true, ".write": true}}`))
	if err != nil {
		panic(err)
	}
	return rs
}()

// access is who a command acts for, as the session was when it sent the command.
type access struct {
	sess *Session
	auth rules.Auth
	// open is a client on this machine under dev_open_rules.
	open bool
}

// access snapshots who the session acts as now.
func (s *Session) access() *access {
	s.mu.Lock()
	defer s.mu.Unlock()
	return &access{sess: s, auth: s.principal, open: s.devOpen()}
}

// ruleset returns the rules a's commands answer to; nil, stored rules that do not parse,
// denies everything.
func (d *Database) ruleset(a *access) *rules.Ruleset {
	if a.open {
		return openRules
	}
	return d.rules
}

// env is what a check reads: the auth, the batch's clock and the data through tx.
func env(a *access, tx StoreTx, now time.Time) rules.Env {
	return rules.Env{Auth: a.auth, Now: now, Data: rulesSource{tx}}
}

// readable reports whether a may read p with the query q shows to rules, without admitting
// anyone.
func (d *Database) readable(ctx context.Context, tx StoreTx, a *access, p Path, q rules.Query, now time.Time) (bool, error) {
	if a == nil || a.auth.IsAdmin() {
		return true, nil
	}
	rs := d.ruleset(a)
	if rs == nil {
		return false, nil
	}
	return rs.CanRead(ctx, env(a, tx, now), p, q)
}

// mayRead checks a read or a listen: nil, ErrPermissionDenied, ErrTooManyConnections, or a
// storage error.
func (d *Database) mayRead(ctx context.Context, tx StoreTx, a *access, p Path, q rules.Query, now time.Time) error {
	ok, err := d.readable(ctx, tx, a, p, q, now)
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrPermissionDenied
	case a == nil:
		return nil
	}
	return a.sess.admit()
}

// mayWrite checks that a may make all of changes at once, as mayRead does.
func (d *Database) mayWrite(ctx context.Context, tx StoreTx, a *access, now time.Time, changes ...rules.Change) error {
	if a == nil {
		return nil
	}
	if !a.auth.IsAdmin() {
		rs := d.ruleset(a)
		if rs == nil {
			return ErrPermissionDenied
		}
		ok, err := rs.CanWrite(ctx, env(a, tx, now), changes...)
		if err != nil {
			return err
		}
		if !ok {
			return ErrPermissionDenied
		}
	}
	return a.sess.admit()
}

// ruleChanges is a resolved change as the rules engine takes it: one change for a set, one per
// child for an update (rules.Update). A priority is no data to rules: setting one is a write of
// its node as it is, which needs the same .write and changes nothing .validate reads. In an
// update that also writes below that node, the other child stands for it.
func ruleChanges(ctx context.Context, tx StoreTx, ch Change) ([]rules.Change, error) {
	if ch.Merge == nil {
		c, err := ruleChange(ctx, tx, ch.Path, ch.Value)
		if err != nil {
			return nil, err
		}
		return []rules.Change{c}, nil
	}
	values := make(map[string]any, len(ch.Merge))
	for _, c := range ch.Merge {
		at := ch.Path.Join(c.Path)
		if at.IsPriority() && slices.ContainsFunc(ch.Merge, func(o Child) bool {
			return !o.Path.IsPriority() && at.Parent().Contains(ch.Path.Join(o.Path))
		}) {
			continue
		}
		rc, err := ruleChange(ctx, tx, at, c.Value)
		if err != nil {
			return nil, err
		}
		values[strings.Join(rc.Path[len(ch.Path):], "/")] = rc.Value
	}
	return rules.Update(ch.Path, values), nil
}

func ruleChange(ctx context.Context, tx StoreTx, p Path, v *Node) (rules.Change, error) {
	if !p.IsPriority() {
		return rules.Change{Path: p, Value: v.plain()}, nil
	}
	cur, err := tx.Read(ctx, p.Parent())
	if err != nil {
		return rules.Change{}, err
	}
	return rules.Change{Path: p.Parent(), Value: cur.plain()}, nil
}

// plain is n as encoding/json would decode its JSON, priorities left out: what the rules
// engine reads.
func (n *Node) plain() any {
	switch n.Kind() {
	case KindNull:
		return nil
	case KindBool:
		return n.b
	case KindNumber:
		return n.n // the rules engine reads every number as a double
	case KindString:
		return n.s
	case KindObject:
		m := make(map[string]any, len(n.children))
		for k, c := range n.children {
			m[k] = c.plain()
		}
		return m
	}
	return nil // an unresolved server value; callers resolve first
}

// rulesSource is the stored data as rules read it, through the writer's transaction: only
// the paths a rule touches, each without what lies below it.
type rulesSource struct{ tx StoreTx }

func (r rulesSource) Get(ctx context.Context, path []string) (rules.Node, error) {
	leaf, has, err := r.tx.Peek(ctx, Path(path))
	if err != nil {
		return rules.Node{}, err
	}
	return rules.Node{Value: leaf.plain(), HasChildren: has}, nil
}

func (r rulesSource) HasChildExcept(ctx context.Context, path []string, keys []string) (bool, error) {
	return r.tx.HasChildExcept(ctx, Path(path), keys)
}

// rules is the query as the query variable shows it to rules. A read without a query is a
// listen's, ordered by priority (rules.PlainListen). equalTo travels as a start and an end at
// the same value, and rules see it as equalTo, as on the emulator (rules' matrix.golden.json).
func (q Query) rules() rules.Query {
	var r rules.Query
	switch q.Index.kind {
	case byKey:
		r.OrderByKey = true
	case byValue:
		r.OrderByValue = true
	case byChild:
		r.OrderByChild = q.Index.child.String()
	default:
		r.OrderByPriority = true
	}
	switch {
	case q.start != nil && q.end != nil && q.start.inclusive && q.end.inclusive && Equal(q.start.value, q.end.value):
		r.EqualTo = q.start.value.plain()
	default:
		if q.start != nil {
			r.StartAt = q.start.value.plain()
		}
		if q.end != nil {
			r.EndAt = q.end.value.plain()
		}
	}
	if q.Limit > 0 {
		if q.FromRight {
			r.LimitToLast = q.Limit
		} else {
			r.LimitToFirst = q.Limit
		}
	}
	return r
}
