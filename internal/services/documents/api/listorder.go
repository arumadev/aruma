package api

import (
	"cmp"
	"context"
	"encoding/base64"
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// ListDocuments with an order_by other than __name__ ascending is a query on one collection
// (f1-list-documents.json order_by.*): production checks it against the indexes, then answers
// the documents that have every ordered field, in that order, ties broken by name in the
// direction of the last order.
//
// Until the query engine and the index entries arrive (F3), the collection is read whole at one
// read time and sorted here: bounded by maxOrderedScan documents.
const maxOrderedScan = 100_000

var errOrderedScan = wire.NewError(wire.CodeResourceExhausted,
	"ListDocuments with an order_by reads at most 100000 documents on this server.")

// orderedEntry is a document with the values of its ordered fields.
type orderedEntry struct {
	doc  core.Document
	vals []value.Value
}

func (s *Service) listOrdered(ctx context.Context, a admitted, lr core.ListRequest, req *pb.ListDocumentsRequest, order listOrder,
	listing []byte, mask []value.FieldPath, masked bool) (*pb.ListDocumentsResponse, error) {
	if err := s.checkIndex(ctx, a, req.GetCollectionId(), order); err != nil {
		return nil, err
	}
	fields, nameDesc := order.fields()
	var cursor *orderedEntry
	if tok := req.GetPageToken(); tok != "" {
		t, err := decodeToken(tok)
		if err != nil {
			return nil, err
		}
		if string(t.Listing) != string(listing) || len(t.Values) != len(fields) {
			return nil, errForeignToken
		}
		cursor = &orderedEntry{doc: core.Document{Path: core.Path(t.After)}}
		for _, enc := range t.Values {
			v, err := decodeValue(enc)
			if err != nil {
				return nil, errGarbageToken
			}
			cursor.vals = append(cursor.vals, v)
		}
	}

	// The whole collection at one read time.
	var entries []orderedEntry
	scan := lr
	scan.PageSize, scan.After = core.MaxPageSize, nil
	for scanned := 0; ; {
		res, err := s.o.Store.ListDocuments(ctx, a.db, scan)
		if err != nil {
			return nil, s.fail(ctx, err)
		}
		scan.ReadTime = res.ReadTime
		for _, d := range res.Documents {
			e := orderedEntry{doc: d}
			for _, f := range fields {
				v, ok := d.Fields.Lookup(f.path)
				if !ok {
					break
				}
				e.vals = append(e.vals, v)
			}
			if len(e.vals) == len(fields) {
				entries = append(entries, e)
			}
		}
		if scanned += len(res.Documents); scanned > maxOrderedScan {
			return nil, errOrderedScan
		}
		if res.Next == nil {
			break
		}
		scan.After = res.Next
	}

	compare := func(x, y orderedEntry) int {
		for i, f := range fields {
			c := value.Compare(x.vals[i], y.vals[i])
			if f.desc {
				c = -c
			}
			if c != 0 {
				return c
			}
		}
		c := slices.Compare(x.doc.Path, y.doc.Path)
		if nameDesc {
			c = -c
		}
		return c
	}
	slices.SortFunc(entries, compare)
	if cursor != nil {
		i, _ := slices.BinarySearchFunc(entries, *cursor, compare)
		for i < len(entries) && compare(entries[i], *cursor) <= 0 {
			i++
		}
		entries = entries[i:]
	}

	size := cmp.Or(lr.PageSize, core.MaxPageSize)
	out := &pb.ListDocumentsResponse{}
	for _, e := range entries[:min(size, len(entries))] {
		out.Documents = append(out.Documents, documentProto(a.db, e.doc, mask, masked))
	}
	if len(entries) > size {
		last := entries[size-1]
		t := pageToken{Listing: listing, After: last.doc.Path}
		for _, v := range last.vals {
			t.Values = append(t.Values, encodeValue(v))
		}
		out.NextPageToken = encodeToken(t)
	}
	return out, nil
}

// checkIndex is production's index rule for the listing's query, against the database's
// enforcement (enforce unless the database overrides it): a needs-index query fails with
// production's error and console link (f1-list-documents.json order_by.03).
func (s *Service) checkIndex(ctx context.Context, a admitted, collectionID string, order listOrder) error {
	mode := index.Enforce
	settings, err := s.o.Store.Database(ctx, a.db)
	if err != nil {
		return s.fail(ctx, err)
	}
	if settings.IndexEnforcement != "" {
		if m, err := index.ParseMode(settings.IndexEnforcement); err == nil {
			mode = m
		}
	}
	shape := index.QueryShape{CollectionID: collectionID, Scope: index.ScopeCollection}
	for _, c := range order {
		shape.OrderBy = append(shape.OrderBy, index.Order{Field: c.path.String(), Desc: c.desc})
	}
	d := index.Check(shape, nil, mode)
	if d.Reject() {
		return wire.IndexError(index.Suggest(a.wdb.Project, a.wdb.ID, d))
	}
	if d.Log() {
		s.log.Warn("documents: a ListDocuments order is served without the index production requires",
			"project", a.wdb.Project, "collection", collectionID, "order", order.String())
	}
	return nil
}

// fields are the order's clauses on fields, and the direction of the name order that breaks
// their ties: __name__'s own when ordered, else the last clause's.
func (o listOrder) fields() (fields listOrder, nameDesc bool) {
	for _, c := range o {
		if c.path.IsDocumentID() {
			return fields, c.desc
		}
		fields = append(fields, c)
	}
	if len(fields) > 0 {
		nameDesc = fields[len(fields)-1].desc
	}
	return fields, nameDesc
}

func encodeValue(v value.Value) string {
	b, _ := proto.Marshal(wire.ValueToProto(v))
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeValue(s string) (value.Value, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return value.Value{}, err
	}
	var p pb.Value
	if err := proto.Unmarshal(b, &p); err != nil {
		return value.Value{}, err
	}
	return wire.ValueFromProto(&p)
}
