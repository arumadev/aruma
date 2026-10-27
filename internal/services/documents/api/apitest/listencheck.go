package apitest

import (
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// ListenChecker is a Listen stream's client side for tests: told of every request sent and every
// response received, it rebuilds each target's documents the way the JS SDK applies the frames
// (watch_change.ts) and fails the test on any frame that breaks the protocol's MUST rules
// (ADR-026 §1; CL MUST 1-10):
//
//   - ADD answers an add_target, and REMOVE without a cause a remove_target (or a once target
//     after its snapshot); no other target change is unasked: no ADD or REMOVE without its
//     request, no per-target NO_CHANGE;
//   - no data (documents, filter, CURRENT, RESET) for a target before its ADD, nor after its
//     REMOVE;
//   - a global snapshot (NO_CHANGE without target ids) has a read time, never earlier than the
//     last one, and comes only when every target of the stream is CURRENT;
//   - a CURRENT's read time never goes back for its target;
//   - a document's versions never go back on the stream, across all its targets: a
//     document_change's update_time is never older than one already sent for the document, nor
//     than a document_delete's read_time, and a document_delete's read_time never older than
//     either (ADR-026 §2: commit order);
//   - a single-document target's ExistenceFilter counts 0 or 1;
//   - with Oracle set, at every global snapshot each target's documents are its result at the
//     snapshot's read time: whole commits, nothing missed or left over.
//
// A filter whose count differs from what the target holds marks it Mismatched: the client then
// queries again (remove, then add without a token), as the SDKs do.
type ListenChecker struct {
	t testing.TB
	// Oracle, when set, is a target's result at a read time, by document name, each with its
	// update time.
	Oracle func(id int32, readTime time.Time) (map[string]time.Time, error)

	targets  map[int32]*ListenTarget
	pending  map[int32]*ListenTarget // added, not answered yet
	assigned []*ListenTarget         // added with target_id 0, not answered yet
	removing map[int32]bool
	global   time.Time
	// versions is, by document name, the newest version the stream sent of it.
	versions map[string]time.Time
	// Held are the documents a client holds of targets it resumes on this stream, by target id:
	// an add_target with a resume token or read_time starts from them.
	Held map[int32]map[string]*pb.Document
	// Globals counts the global snapshots received.
	Globals int
}

// ListenTarget is one target as the client holds it.
type ListenTarget struct {
	ID int32
	// Docs are its documents by name.
	Docs map[string]*pb.Document
	// Current: CURRENT came since its ADD or its last RESET.
	Current bool
	// Mismatched: a filter disagreed with Docs; the client must query again.
	Mismatched bool
	// Token is the last resume token the target was given.
	Token  []byte
	Target *pb.Target
	single bool
	added  bool
	read   time.Time
}

// NewListenChecker returns a checker for one stream.
func NewListenChecker(t testing.TB) *ListenChecker {
	return &ListenChecker{t: t, targets: map[int32]*ListenTarget{}, pending: map[int32]*ListenTarget{}, removing: map[int32]bool{},
		versions: map[string]time.Time{}}
}

// Target is the target id holds, or nil.
func (c *ListenChecker) Target(id int32) *ListenTarget { return c.targets[id] }

// Targets are the targets the stream holds, by id.
func (c *ListenChecker) Targets() []*ListenTarget {
	return slices.Collect(func(yield func(*ListenTarget) bool) {
		for _, id := range slices.Sorted(maps.Keys(c.targets)) {
			if !yield(c.targets[id]) {
				return
			}
		}
	})
}

// Settled reports whether no add or remove waits for its answer.
func (c *ListenChecker) Settled() bool {
	return len(c.pending) == 0 && len(c.assigned) == 0 && len(c.removing) == 0
}

func (c *ListenChecker) fail(format string, args ...any) {
	c.t.Helper()
	c.t.Errorf("listen protocol: "+format, args...)
}

// Sent records a request the client sent.
func (c *ListenChecker) Sent(req *pb.ListenRequest) {
	switch tc := req.GetTargetChange().(type) {
	case *pb.ListenRequest_AddTarget:
		t := tc.AddTarget
		lt := &ListenTarget{ID: t.GetTargetId(), Docs: map[string]*pb.Document{}, Target: t,
			single: len(t.GetDocuments().GetDocuments()) == 1}
		if t.GetResumeType() != nil {
			maps.Copy(lt.Docs, c.Held[lt.ID])
		}
		if lt.ID == 0 {
			c.assigned = append(c.assigned, lt)
		} else {
			c.pending[lt.ID] = lt
		}
	case *pb.ListenRequest_RemoveTarget:
		c.removing[tc.RemoveTarget] = true
	}
}

// Received checks and applies one response; global reports a global snapshot.
func (c *ListenChecker) Received(res *pb.ListenResponse) (global bool) {
	c.t.Helper()
	switch r := res.GetResponseType().(type) {
	case *pb.ListenResponse_TargetChange:
		return c.targetChange(r.TargetChange)
	case *pb.ListenResponse_DocumentChange:
		dc := r.DocumentChange
		name := dc.GetDocument().GetName()
		c.version(name, dc.GetDocument().GetUpdateTime().AsTime(), "document_change")
		for _, id := range dc.GetTargetIds() {
			if t := c.data(id, "document_change"); t != nil {
				if t.single && t.Target.GetDocuments().GetDocuments()[0] != name {
					c.fail("target %d: document_change of %s, not its document", id, name)
				}
				t.Docs[name] = dc.GetDocument()
			}
		}
		for _, id := range dc.GetRemovedTargetIds() {
			if t := c.data(id, "document_change (removed)"); t != nil {
				delete(t.Docs, name)
			}
		}
	case *pb.ListenResponse_DocumentDelete:
		c.version(r.DocumentDelete.GetDocument(), r.DocumentDelete.GetReadTime().AsTime(), "document_delete")
		for _, id := range r.DocumentDelete.GetRemovedTargetIds() {
			if t := c.data(id, "document_delete"); t != nil {
				delete(t.Docs, r.DocumentDelete.GetDocument())
			}
		}
	case *pb.ListenResponse_DocumentRemove:
		for _, id := range r.DocumentRemove.GetRemovedTargetIds() {
			if t := c.data(id, "document_remove"); t != nil {
				delete(t.Docs, r.DocumentRemove.GetDocument())
			}
		}
	case *pb.ListenResponse_Filter:
		f := r.Filter
		if t := c.data(f.GetTargetId(), "filter"); t != nil {
			if t.single && f.GetCount() > 1 {
				c.fail("target %d: a single-document target's filter counts %d", t.ID, f.GetCount())
			}
			if len(f.GetUnchangedNames().GetBits().GetBitmap()) > 0 {
				c.fail("target %d: a bloom filter, which this server does not send", t.ID)
			}
			if int(f.GetCount()) != len(t.Docs) {
				t.Mismatched = true
			}
		}
	default:
		c.fail("an empty response")
	}
	return false
}

// version records a document's version sent at, failing when it is older than one sent before.
// A document_remove is not one: the document may come back into a window unchanged, older.
func (c *ListenChecker) version(name string, at time.Time, what string) {
	c.t.Helper()
	if last := c.versions[name]; at.Before(last) {
		c.fail("%s of %s at %v after a version at %v: out of commit order", what, name,
			at.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano))
		return
	}
	c.versions[name] = at
}

// data is the target a data frame names, which must have been added and not removed.
func (c *ListenChecker) data(id int32, what string) *ListenTarget {
	c.t.Helper()
	if t := c.targets[id]; t != nil {
		return t // a target removed and added again holds its data until REMOVE
	}
	if _, ok := c.pending[id]; ok {
		c.fail("%s for target %d before its ADD", what, id)
	} else {
		c.fail("%s for target %d, which the stream does not hold", what, id)
	}
	return nil
}

func (c *ListenChecker) targetChange(tc *pb.TargetChange) bool {
	c.t.Helper()
	ids := tc.GetTargetIds()
	switch tc.GetTargetChangeType() {
	case pb.TargetChange_NO_CHANGE:
		if len(ids) > 0 {
			c.fail("an unasked NO_CHANGE for targets %v", ids)
			return false
		}
		return c.globalSnapshot(tc)
	case pb.TargetChange_ADD:
		for _, id := range ids {
			t := c.pending[id]
			delete(c.pending, id)
			if t == nil && len(c.assigned) > 0 {
				t, c.assigned = c.assigned[0], c.assigned[1:]
				t.ID = id
			}
			if t == nil {
				c.fail("ADD for target %d, which no add_target asked", id)
				continue
			}
			if old := c.targets[id]; old != nil && !c.removing[id] {
				c.fail("ADD for target %d, which the stream holds", id)
			}
			t.added = true
			c.targets[id] = t
		}
	case pb.TargetChange_REMOVE:
		for _, id := range ids {
			if tc.GetCause() != nil {
				if _, ok := c.pending[id]; ok {
					delete(c.pending, id)
					continue
				}
				if len(c.assigned) > 0 && c.targets[id] == nil {
					c.assigned = c.assigned[1:]
					continue
				}
				if c.targets[id] == nil {
					c.fail("REMOVE (%v) for target %d, which the stream does not hold", tc.GetCause().GetMessage(), id)
				}
				delete(c.targets, id)
				continue
			}
			t := c.targets[id]
			switch {
			case c.removing[id]:
				delete(c.removing, id)
			case t != nil && t.Target.GetOnce() && t.Current:
			default:
				c.fail("REMOVE for target %d, which no remove_target asked", id)
			}
			delete(c.targets, id)
		}
	case pb.TargetChange_CURRENT:
		for _, id := range ids {
			if t := c.data(id, "CURRENT"); t != nil {
				rt := tc.GetReadTime().AsTime()
				if tc.GetReadTime() == nil || rt.Before(t.read) {
					c.fail("target %d: CURRENT at %v, before its last at %v", id, rt, t.read)
				}
				t.Current, t.read, t.Token = true, rt, tc.GetResumeToken()
			}
		}
	case pb.TargetChange_RESET:
		for _, id := range ids {
			if t := c.data(id, "RESET"); t != nil {
				t.Current = false
				clear(t.Docs)
			}
		}
	}
	return false
}

func (c *ListenChecker) globalSnapshot(tc *pb.TargetChange) bool {
	c.t.Helper()
	if tc.GetReadTime() == nil {
		c.fail("a global snapshot without a read time")
		return false
	}
	rt := tc.GetReadTime().AsTime()
	if rt.Before(c.global) {
		c.fail("a global snapshot at %v, before the last at %v", rt, c.global)
	}
	c.global = rt
	c.Globals++
	for _, t := range c.Targets() {
		if !t.Current {
			c.fail("a global snapshot at %v before target %d is CURRENT", rt, t.ID)
			continue
		}
		t.Token = tc.GetResumeToken()
		if c.Oracle == nil || t.Mismatched {
			continue
		}
		want, err := c.Oracle(t.ID, rt)
		if err != nil {
			c.t.Errorf("listen oracle: target %d at %v: %v", t.ID, rt, err)
			continue
		}
		if diff := viewDiff(t.Docs, want); diff != "" {
			c.fail("target %d at the global snapshot %v: %s", t.ID, rt, diff)
		}
	}
	return true
}

// viewDiff tells how the documents a target holds differ from its result.
func viewDiff(got map[string]*pb.Document, want map[string]time.Time) string {
	var out []string
	for _, n := range slices.Sorted(maps.Keys(want)) {
		d, ok := got[n]
		switch {
		case !ok:
			out = append(out, "missing "+n)
		case !d.GetUpdateTime().AsTime().Equal(want[n]):
			out = append(out, fmt.Sprintf("%s at %v, want %v", n, d.GetUpdateTime().AsTime(), want[n]))
		}
	}
	for _, n := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[n]; !ok {
			out = append(out, "extra "+n)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return fmt.Sprint(out)
}
