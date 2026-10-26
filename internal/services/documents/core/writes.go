package core

import (
	"bytes"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// lockedDoc is a document as a commit's first batch read it under its row lock.
type lockedDoc struct {
	seq        int64
	fields     value.Value
	createTime time.Time
	updateTime time.Time
}

// docChange is one document's part of a commit: its writes folded in order, from the document
// as locked (before) to the document as the commit leaves it.
type docChange struct {
	path   Path
	keys   docKeys
	writes int // how many of the commit's writes name it
	// mask is the update mask of a masked write (changelog.update_mask when it is the only one).
	masked bool
	mask   []value.FieldPath

	before *lockedDoc // nil: it did not exist
	exists bool
	fields value.Value
	// serverTime: a write set a field of it to the commit's time (TransformServerTime).
	serverTime bool
}

// changed reports whether the commit changes the document: a write that leaves it as it was
// changes nothing, keeps its update_time and is not in the changelog (WriteResult.update_time in
// write.proto; f1-write-results.json, rest.update_unchanged). A document a write gave the
// commit's time changes: its fields are known only once the commit is stamped, and the time is
// later than any it held, but for a field set within the same millisecond.
func (d *docChange) changed() bool {
	if d.before == nil || !d.exists {
		return (d.before != nil) != d.exists
	}
	return d.serverTime || !value.Identical(d.before.fields, d.fields)
}

// plan is a commit's writes grouped by document, in the order their rows are locked. F3 adds the
// index entries here; the commit path does not change shape.
type plan struct {
	writes []Write
	docs   []*docChange // by stored name key
	of     []*docChange // per write
	// serverTime: a write has a REQUEST_TIME transform, so the documents are built only once
	// the commit is stamped (sequencer.attempt).
	serverTime bool
	// transformed holds each write's transform results, as the last apply made them.
	transformed [][]value.Value
}

func newPlan(writes []Write) *plan {
	p := &plan{writes: writes, of: make([]*docChange, len(writes)), transformed: make([][]value.Value, len(writes))}
	byKey := map[string]*docChange{}
	for i, w := range writes {
		k := nameKey(w.Path)
		d := byKey[string(k)]
		if d == nil {
			d = &docChange{path: w.Path, keys: keysOf(w.Path)}
			byKey[string(k)] = d
			p.docs = append(p.docs, d)
		}
		d.writes++
		if w.Kind == WriteUpdate && w.Masked {
			// A transformed field changes as a masked one does.
			d.masked, d.mask = true, slices.Clip(w.Mask)
			for _, t := range w.Transforms {
				d.mask = append(d.mask, t.Path)
			}
		}
		for _, t := range w.Transforms {
			if t.Kind == TransformServerTime {
				p.serverTime = true
			}
		}
		p.of[i] = d
	}
	slices.SortFunc(p.docs, func(a, b *docChange) int { return bytes.Compare(a.keys.stored, b.keys.stored) })
	return p
}

// storedKeys returns the documents' name_key values, in lock order.
func (p *plan) storedKeys() [][]byte {
	ks := make([][]byte, len(p.docs))
	for i, d := range p.docs {
		ks[i] = d.keys.stored
	}
	return ks
}

// apply folds the writes over the documents as locked (by stored key; absent if not there): each
// write's precondition is checked against the document as the writes before it left it, and the
// first that fails fails the commit with production's error. A write's transforms apply after
// its fields, in order, REQUEST_TIME as RequestTime(commitTime). It may run again with another
// commitTime: it starts from the documents as locked each time, and only REQUEST_TIME's values
// differ, which change neither a document's validity nor its size.
func (p *plan) apply(db DB, locked map[string]*lockedDoc, commitTime time.Time) error {
	for _, d := range p.docs {
		d.before = locked[string(d.keys.stored)]
		d.exists = d.before != nil
		d.serverTime = false
		d.fields = value.Map()
		if d.exists {
			d.fields = d.before.fields
		}
	}
	now := RequestTime(commitTime)
	for i, w := range p.writes {
		// The transports refuse such names; this keeps one from ever being stored, since a name
		// key's segments are only read back right when they are UTF-8 (0xFF 0x01 would end one).
		if j := slices.IndexFunc(w.Path, func(id string) bool { return !utf8.ValidString(id) }); j >= 0 {
			return &Error{InvalidArgument, `Resource id "` + strings.ToValidUTF8(w.Path[j], "\uFFFD") + `" is invalid because it is not UTF-8.`}
		}
		d := p.of[i]
		if err := d.check(db, w.Precondition); err != nil {
			return err
		}
		switch w.Kind {
		case WriteUpdate:
			after := w.Fields
			if w.Masked {
				after = value.ApplyMask(d.fields, w.Fields, w.Mask)
			}
			p.transformed[i] = nil
			if len(w.Transforms) > 0 {
				p.transformed[i] = make([]value.Value, len(w.Transforms))
			}
			for j, t := range w.Transforms {
				after, p.transformed[i][j] = t.apply(after, now)
				d.serverTime = d.serverTime || t.Kind == TransformServerTime
			}
			if w.Masked || len(w.Transforms) > 0 {
				if err := value.Validate(after); err != nil {
					return invalidDocument(err)
				}
			}
			if size := value.DocumentSize(w.Path.String(), after); size > value.MaxDocumentSize {
				return documentTooLarge(db.DocumentName(w.Path), size)
			}
			d.exists, d.fields = true, after
		case WriteDelete:
			d.exists, d.fields, d.serverTime = false, value.Map(), false
		}
	}
	return nil
}

// check evaluates a precondition. update_time is matched against the stored document's: a
// document an earlier write of the same commit created has none yet.
func (d *docChange) check(db DB, c Precondition) error {
	if c.Exists != nil {
		switch {
		case *c.Exists && !d.exists:
			return noDocumentToUpdate(db.DocumentName(d.path))
		case !*c.Exists && d.exists:
			return alreadyExists(db.DocumentName(d.path))
		}
	}
	if !c.UpdateTime.IsZero() {
		var stored time.Time
		if d.exists && d.before != nil {
			stored = d.before.updateTime
		}
		if !stored.Equal(c.UpdateTime) {
			return staleVersion(stored, c.UpdateTime)
		}
	}
	return nil
}

// results returns each write's result once the commit took commitTime.
func (p *plan) results(commitTime time.Time) []WriteResult {
	out := make([]WriteResult, len(p.writes))
	for i, w := range p.writes {
		out[i].TransformResults = p.transformed[i]
		d := p.of[i]
		if w.Kind == WriteDelete || !d.exists {
			continue
		}
		switch {
		case d.changed():
			out[i].UpdateTime = commitTime
		case d.before != nil:
			out[i].UpdateTime = d.before.updateTime
		}
	}
	return out
}

// Change is one document's change in a commit, as the hub hears of it: Before and After are the
// document as it was and as the commit left it (Exists false for absent).
type Change struct {
	Path   Path
	Before Document
	After  Document
}

// changes returns the changes the commit makes, in lock order.
func (p *plan) changes(commitTime time.Time) []Change {
	var out []Change
	for _, d := range p.docs {
		if !d.changed() {
			continue
		}
		c := Change{Path: d.path, Before: Document{Path: d.path}, After: Document{Path: d.path}}
		if d.before != nil {
			c.Before.Fields, c.Before.CreateTime, c.Before.UpdateTime = d.before.fields, d.before.createTime, d.before.updateTime
		}
		if d.exists {
			c.After.Fields, c.After.CreateTime, c.After.UpdateTime = d.fields, commitTime, commitTime
			if d.before != nil {
				c.After.CreateTime = d.before.createTime
			}
		}
		out = append(out, c)
	}
	return out
}

// stampArgs fills the stamp statement's arrays from the changed documents.
func (p *plan) stampArgs(a *stampInput) error {
	for _, d := range p.docs {
		if !d.changed() {
			continue
		}
		var doc []byte
		if d.exists {
			var err error
			if doc, err = encodeDocument(d.fields, time.Time{}, time.Time{}); err != nil {
				return err
			}
		}
		size := int32(value.DocumentSize(d.path.String(), d.fields))
		switch {
		case d.before != nil && d.exists:
			a.updateKeys = append(a.updateKeys, d.keys.stored)
			a.updateSeqs = append(a.updateSeqs, d.before.seq)
			a.updateDocs = append(a.updateDocs, doc)
			a.updateSizes = append(a.updateSizes, size)
			a.replaced = append(a.replaced, d.keys.stored)
		case d.exists:
			a.insertKeys = append(a.insertKeys, d.keys.stored)
			a.insertNames = append(a.insertNames, d.keys.name)
			a.insertParentHashes = append(a.insertParentHashes, d.keys.parentHash)
			a.insertCgHashes = append(a.insertCgHashes, d.keys.cgHash)
			a.insertParents = append(a.insertParents, d.keys.parent)
			a.insertCollectionIDs = append(a.insertCollectionIDs, d.path.CollectionID())
			a.insertDocs = append(a.insertDocs, doc)
			a.insertSizes = append(a.insertSizes, size)
		default:
			a.deleteKeys = append(a.deleteKeys, d.keys.stored)
			a.deleteSeqs = append(a.deleteSeqs, d.before.seq)
			a.replaced = append(a.replaced, d.keys.stored)
		}
		before, after, mask, err := d.logImages()
		if err != nil {
			return err
		}
		a.logKeys = append(a.logKeys, d.keys.stored)
		a.logNames = append(a.logNames, d.keys.name)
		a.logBefores = append(a.logBefores, before)
		a.logAfters = append(a.logAfters, after)
		a.logMasks = append(a.logMasks, mask)
	}
	return nil
}

// logImages encodes the changelog's images (migration documents/00001, changelog): before with
// its times; after with its create_time when it existed before, its update_time being the
// commit's; nil for an absent document. The mask is the one masked write's, if it was alone.
func (d *docChange) logImages() (before, after, mask []byte, err error) {
	if d.before != nil {
		if before, err = encodeDocument(d.before.fields, d.before.createTime, d.before.updateTime); err != nil {
			return nil, nil, nil, err
		}
	}
	if d.exists {
		var created time.Time
		if d.before != nil {
			created = d.before.createTime
		}
		if after, err = encodeDocument(d.fields, created, time.Time{}); err != nil {
			return nil, nil, nil, err
		}
	}
	if d.writes == 1 && d.masked {
		m := &firestorepb.DocumentMask{FieldPaths: make([]string, len(d.mask))}
		for i, f := range d.mask {
			m.FieldPaths[i] = f.String()
		}
		if mask, err = proto.Marshal(m); err != nil {
			return nil, nil, nil, err
		}
	}
	return before, after, mask, nil
}
