package core

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
)

// nodeCache keeps in the writer's memory the nodes that query listens read again on every write
// below them (taggedPushes), so that a write updates them in place, through Node.Set as the
// store applies it, instead of having them read from Postgres again. Only the database's writer
// goroutine uses it, and every write goes through that writer, so the cache sees each change.
//
// It holds what the writer's transaction sees: a batch's changes go in as they are applied, and
// come out again if the batch does not commit (batchState.undo). When a commit's outcome is
// unknown, or the database's data change outside the writer (PurgeProject), it is emptied.
type nodeCache struct {
	entries map[string]*cacheEntry
	budget  *cacheBudget
	clock   uint64 // orders entries by last use, for eviction
	// check makes every node the cache serves be read from the store too and compared: for
	// tests (ARUMA_RTDB_CHECK_CACHE).
	check bool
}

type cacheEntry struct {
	path Path
	node *Node
	rows int64 // the rows the node stores (storedRows), as charged to the budget
	used uint64
}

// cacheBudget is the stored rows all databases' caches may hold together (Limits.CacheRows).
type cacheBudget struct {
	limit int64
	used  atomic.Int64
}

// reserve charges n rows, false when they do not fit.
func (b *cacheBudget) reserve(n int64) bool {
	if b == nil || b.limit <= 0 {
		return false
	}
	if b.used.Add(n) > b.limit {
		b.used.Add(-n)
		return false
	}
	return true
}

func (b *cacheBudget) release(n int64) {
	if b != nil {
		b.used.Add(-n)
	}
}

func newNodeCache(budget *cacheBudget) *nodeCache {
	return &nodeCache{entries: map[string]*cacheEntry{}, budget: budget, check: os.Getenv("ARUMA_RTDB_CHECK_CACHE") != ""}
}

// get returns the value at p from a cached node at p or above it.
func (c *nodeCache) get(ctx context.Context, tx StoreTx, p Path) (*Node, bool, error) {
	for _, e := range c.entries {
		if !e.path.Contains(p) {
			continue
		}
		c.clock++
		e.used = c.clock
		n := e.node.Get(e.path.Rel(p))
		if c.check {
			stored, err := tx.Read(ctx, p)
			if err != nil {
				return nil, false, err
			}
			if !Equal(n, stored) {
				panic(fmt.Sprintf("rtdb: the node cache at %q holds %s, the store %s", p.String(), n.ExportJSON(), stored.ExportJSON()))
			}
		}
		return n, true, nil
	}
	return nil, false, nil
}

// put caches n, as read in tx, as the node at p, if the budget has room for it. A batch that
// does not commit takes it out again: it may hold that batch's writes.
func (c *nodeCache) put(p Path, n *Node, state *batchState) {
	if p.IsPriority() {
		return
	}
	if _, ok := c.entries[p.String()]; ok {
		return
	}
	rows := storedRows(n)
	if c.budget == nil || rows > c.budget.limit {
		return // could never fit: keep what is cached
	}
	for !c.budget.reserve(rows) {
		if !c.evict() {
			return
		}
	}
	c.clock++
	e := &cacheEntry{path: p, node: n, rows: rows, used: c.clock}
	key := p.String()
	c.entries[key] = e
	state.undo = append(state.undo, func() {
		if c.entries[key] == e {
			c.remove(key)
		}
	})
}

// apply writes v at p, as Write does in the store, into every cached node it touches.
func (c *nodeCache) apply(p Path, v *Node, state *batchState) {
	for _, e := range c.entries {
		var node *Node
		var delta int64
		switch {
		case e.path.Contains(p):
			rel := e.path.Rel(p)
			if !rel.IsPriority() {
				delta = storedRows(v) - storedRows(e.node.Get(rel))
			}
			node = e.node.Set(rel, v)
		case p.Contains(e.path) && !p.IsPriority():
			node = v.Get(p.Rel(e.path))
			delta = storedRows(node) - e.rows
		default:
			continue
		}
		oldNode, oldRows := e.node, e.rows
		e.node, e.rows = node, max(0, e.rows+delta)
		c.budget.used.Add(e.rows - oldRows)
		state.undo = append(state.undo, func() {
			c.budget.used.Add(oldRows - e.rows)
			e.node, e.rows = oldNode, oldRows
		})
	}
}

// prune drops the entries that no query listen reads any more.
func (c *nodeCache) prune(keep func(Path) bool) {
	for key, e := range c.entries {
		if !keep(e.path) {
			c.remove(key)
		}
	}
}

// clear drops every entry.
func (c *nodeCache) clear() {
	for key := range c.entries {
		c.remove(key)
	}
}

// evict drops the entry used longest ago; false when there is none.
func (c *nodeCache) evict() bool {
	var oldest string
	var e *cacheEntry
	for key, x := range c.entries {
		if e == nil || x.used < e.used {
			oldest, e = key, x
		}
	}
	if e == nil {
		return false
	}
	c.remove(oldest)
	return true
}

func (c *nodeCache) remove(key string) {
	if e, ok := c.entries[key]; ok {
		c.budget.release(e.rows)
		delete(c.entries, key)
	}
}

// storedRows counts the rows the store keeps for n: one per leaf, and one per object that
// carries a priority (rowsOf).
func storedRows(n *Node) int64 {
	switch {
	case n == nil:
		return 0
	case n.IsLeaf():
		return 1
	}
	var rows int64
	if n.priority != nil {
		rows = 1
	}
	for _, c := range n.children {
		rows += storedRows(c)
	}
	return rows
}
