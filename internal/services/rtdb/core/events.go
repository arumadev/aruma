package core

import (
	"context"
	"encoding/json"
	"slices"
)

// delivery is one push for one session, sent once the batch that made it has committed.
type delivery struct {
	sess *Session
	push Push
}

// batchState collects what a batch will send, and how to undo the listener state it changed
// should its transaction not commit.
type batchState struct {
	deliveries []delivery
	undo       []func()
	// sent dedupes identical pushes to one session for one change: a session listening at
	// a path and below it needs a write's push once.
	sent map[sentKey]bool
}

type sentKey struct {
	sess *Session
	push string
}

func (b *batchState) deliver(s *Session, p Push) {
	// The query tells apart the c pushes of two listens at one path.
	query := ""
	if p.Query != nil {
		query = p.Query.canonical()
	}
	desc, _ := json.Marshal([]any{p.Action, p.Path.String(), p.Tag, string(p.Data), query})
	key := sentKey{s, string(desc)}
	if b.sent[key] {
		return
	}
	if b.sent == nil {
		b.sent = map[sentKey]bool{}
	}
	b.sent[key] = true
	b.deliveries = append(b.deliveries, delivery{s, p})
}

// rollback undoes the listener changes of a batch that did not commit, newest first.
func (b *batchState) rollback() {
	for i := len(b.undo) - 1; i >= 0; i-- {
		b.undo[i]()
	}
	b.undo, b.deliveries = nil, nil
}

// fanout turns one applied change into pushes for the listens it concerns, reading through
// tx the state the change left (B-emulator-oracle.md §1.4, §1.7, §2):
//
//   - An untagged listen gets the write as written: a set below or at its path as a d push
//     at the written path; a set above it as a d push at its own path with its part of the
//     value; a merge at or below its path as one m push with the merged children.
//   - A tagged listen gets its window's changes: each child that entered or changed, at the
//     child's path with the whole child; a child that left because it changed (moved out of
//     range, or was deleted) with its new value, null when deleted; nothing for a child the
//     limit pushed out, which the client drops itself. Several in one write go as one m
//     push. A write at or above its path replaces the whole window with a d push.
func (d *Database) fanout(ctx context.Context, tx StoreTx, ch Change, b *batchState) error {
	b.sent = nil
	targets := []Path{ch.Path}
	if ch.Merge != nil {
		targets = targets[:0]
		for _, c := range ch.Merge {
			targets = append(targets, ch.Path.Join(c.Path))
		}
	}
	seen := map[*listen]bool{}
	var concerned []*listen
	for _, t := range targets {
		d.listeners.around(t, func(l *listen) {
			if !seen[l] {
				seen[l] = true
				concerned = append(concerned, l)
			}
		})
	}
	for _, l := range concerned {
		if l.tag == 0 {
			untaggedPushes(l, ch, b)
			continue
		}
		if err := taggedPushes(ctx, tx, l, targets, b); err != nil {
			return err
		}
	}
	return nil
}

func untaggedPushes(l *listen, ch Change, b *batchState) {
	setAt := func(at Path, v *Node) {
		switch {
		case l.path.Contains(at):
			b.deliver(l.sess, Push{Action: PushData, Path: at, Data: v.ExportJSON()})
		case at.Contains(l.path):
			b.deliver(l.sess, Push{Action: PushData, Path: l.path, Data: v.Get(l.path[len(at):]).ExportJSON()})
		}
	}
	if ch.Merge == nil {
		setAt(ch.Path, ch.Value)
		return
	}
	if l.path.Contains(ch.Path) {
		b.deliver(l.sess, Push{Action: PushMerge, Path: ch.Path, Data: mergeJSON(ch.Merge)})
		return
	}
	for _, c := range ch.Merge {
		setAt(ch.Path.Join(c.Path), c.Value)
	}
}

// mergeJSON encodes merged children as the m push carries them: keyed by relative paths.
func mergeJSON(children []Child) json.RawMessage {
	out := []byte{'{'}
	for i, c := range children {
		if i > 0 {
			out = append(out, ',')
		}
		out = appendJSONString(out, c.Path.String())
		out = append(out, ':')
		out = c.Value.appendJSON(out, true)
	}
	return append(out, '}')
}

func taggedPushes(ctx context.Context, tx StoreTx, l *listen, targets []Path, b *batchState) error {
	node, err := tx.Read(ctx, l.path)
	if err != nil {
		return err
	}
	old, neu := l.window, l.query.Filter(node)
	if Equal(old, neu) {
		return nil
	}
	l.window = neu
	b.undo = append(b.undo, func() { l.window = old })
	if slices.ContainsFunc(targets, func(t Path) bool { return t.Contains(l.path) }) {
		b.deliver(l.sess, Push{Action: PushData, Path: l.path, Data: neu.ExportJSON(), Tag: l.tag})
		return nil
	}
	var changed []Child
	for _, k := range unionKeys(old, neu) {
		o, n := old.Child(k), neu.Child(k)
		switch {
		case Equal(o, n):
		case n != nil:
			changed = append(changed, Child{Path: Path{k}, Value: n})
		default:
			// Left the window: say so only if the child itself changed. One the limit pushed
			// out is unchanged, and the client drops it on its own.
			if cur := node.Child(k); !Equal(cur, o) {
				changed = append(changed, Child{Path: Path{k}, Value: cur})
			}
		}
	}
	switch len(changed) {
	case 0:
	case 1:
		b.deliver(l.sess, Push{Action: PushData, Path: l.path.Join(changed[0].Path), Data: changed[0].Value.ExportJSON(), Tag: l.tag})
	default:
		b.deliver(l.sess, Push{Action: PushMerge, Path: l.path, Data: mergeJSON(changed), Tag: l.tag})
	}
	return nil
}

// unionKeys returns the keys of two objects together, in key order.
func unionKeys(a, b *Node) []string {
	keys := append(a.Keys(), b.Keys()...)
	slices.SortFunc(keys, CompareKeys)
	return slices.Compact(keys)
}
