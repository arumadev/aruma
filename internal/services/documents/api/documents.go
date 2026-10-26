package api

import (
	"context"
	"slices"

	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The F1 methods. Each answers its response message, or a *wire.Error with production's text
// (f1-*.json); the recordings each check cites are under test/oracle/documents/corpus.

// GetDocument reads one document (f1-get.json).
func (s *Service) GetDocument(ctx context.Context, c Call, req *pb.GetDocumentRequest) (*pb.Document, error) {
	a, err := s.admit(ctx, c, req, documentOf(req.GetName()))
	if err != nil {
		return nil, err
	}
	n, err := wire.ParseDocumentName(req.GetName())
	if err != nil {
		return nil, err
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	_, txn := req.GetConsistencySelector().(*pb.GetDocumentRequest_Transaction)
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return nil, err
	}
	d, _, err := s.o.Store.Get(ctx, a.db, pathOf(n), ro)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return documentProto(a.db, d, mask, masked), nil
}

// BatchGetDocuments reads documents; one that does not exist is missing, and each is answered
// once, in production's order (answerOrder; f1-batch-get.json). The transports send the
// responses as a stream (gRPC) or an array (REST).
func (s *Service) BatchGetDocuments(ctx context.Context, c Call, req *pb.BatchGetDocumentsRequest) ([]*pb.BatchGetDocumentsResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(c, req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	paths := make([]core.Path, len(req.GetDocuments()))
	for i, name := range req.GetDocuments() {
		n, err := wire.ParseDocumentName(name)
		if err != nil {
			return nil, err
		}
		paths[i] = pathOf(n)
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	var txn bool
	switch req.GetConsistencySelector().(type) {
	case *pb.BatchGetDocumentsRequest_Transaction, *pb.BatchGetDocumentsRequest_NewTransaction:
		txn = true
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return nil, err
	}
	docs, readTime, err := s.o.Store.BatchGet(ctx, a.db, paths, ro)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	rt := timestamppb.New(readTime)
	out := make([]*pb.BatchGetDocumentsResponse, 0, len(docs))
	for _, d := range answerOrder(docs) {
		r := &pb.BatchGetDocumentsResponse{ReadTime: rt}
		if d.Exists() {
			r.Result = &pb.BatchGetDocumentsResponse_Found{Found: documentProto(a.db, d, mask, masked)}
		} else {
			r.Result = &pb.BatchGetDocumentsResponse_Missing{Missing: a.db.DocumentName(d.Path)}
		}
		out = append(out, r)
	}
	return out, nil
}

// answerOrder is the order production answers a batch get in, on REST and gRPC alike
// (f1-batch-get.json order_run1-3, grpc.order, duplicates): each document once, those found in
// name order, then those missing, last name first. The missing ones' order rests on one
// recording of two names, which reads as descending names and as the request's order reversed
// alike; the clients match answers to names, so neither changes what they return.
func answerOrder(docs []core.Document) []core.Document {
	seen := make(map[string]bool, len(docs))
	var found, missing []core.Document
	for _, d := range docs {
		key := d.Path.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		if d.Exists() {
			found = append(found, d)
		} else {
			missing = append(missing, d)
		}
	}
	byName := func(a, b core.Document) int { return slices.Compare(a.Path, b.Path) }
	slices.SortFunc(found, byName)
	slices.SortFunc(missing, func(a, b core.Document) int { return byName(b, a) })
	return append(found, missing...)
}

// ListDocuments lists a collection's documents in name order, or with an empty collection id
// the documents of every collection directly under the parent (f1-list-documents.json).
func (s *Service) ListDocuments(ctx context.Context, c Call, req *pb.ListDocumentsRequest) (*pb.ListDocumentsResponse, error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return nil, err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return nil, err
	}
	if req.GetCollectionId() != "" {
		if err := wire.CheckID(req.GetCollectionId(), false); err != nil {
			return nil, err
		}
	}
	if req.GetPageSize() < 0 {
		return nil, wire.InvalidArgument("Page size must be nonnegative.")
	}
	order, err := parseOrderBy(req.GetOrderBy())
	if err != nil {
		return nil, err
	}
	switch {
	case req.GetShowMissing() && req.GetCollectionId() == "":
		return nil, wire.InvalidArgument("collection id must be set when show_missing is true")
	case req.GetShowMissing() && len(order) > 0:
		return nil, wire.InvalidArgument("cannot specify an order when show_missing is true")
	case req.GetCollectionId() == "" && !order.byName():
		return nil, wire.InvalidArgument("kind is required for all orders except __key__ ascending")
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	_, txn := req.GetConsistencySelector().(*pb.ListDocumentsRequest_Transaction)
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return nil, err
	}
	listing := listingDigest(parent.String(), req.GetCollectionId(), order.String(), req.GetShowMissing())
	lr := core.ListRequest{
		Parent:       pathOf(parent),
		CollectionID: req.GetCollectionId(),
		PageSize:     min(int(req.GetPageSize()), core.MaxPageSize),
		ShowMissing:  req.GetShowMissing(),
		ReadOptions:  ro,
	}
	if !order.byName() {
		return s.listOrdered(ctx, a, lr, req, order, listing, mask, masked)
	}
	if tok := req.GetPageToken(); tok != "" {
		t, err := decodeToken(tok)
		if err != nil {
			return nil, err
		}
		if string(t.Listing) != string(listing) || t.Values != nil {
			return nil, errForeignToken
		}
		lr.After = core.Path(t.After)
	}
	res, err := s.o.Store.ListDocuments(ctx, a.db, lr)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := &pb.ListDocumentsResponse{}
	for _, d := range res.Documents {
		out.Documents = append(out.Documents, documentProto(a.db, d, mask, masked))
	}
	if res.Next != nil {
		out.NextPageToken = encodeToken(pageToken{Listing: listing, After: res.Next})
	}
	return out, nil
}

// CreateDocument creates a document under a collection, with the id given or one of its own,
// and fails if it exists (f1-create.json).
func (s *Service) CreateDocument(ctx context.Context, c Call, req *pb.CreateDocumentRequest) (*pb.Document, error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return nil, err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return nil, err
	}
	if req.GetCollectionId() == "" {
		// Provisional text: REST always names the collection in its path.
		return nil, wire.InvalidArgument("collection_id must be set")
	}
	if err := wire.CheckID(req.GetCollectionId(), false); err != nil {
		return nil, err
	}
	if req.GetDocument().GetName() != "" {
		return nil, wire.InvalidArgument("document.name must not be set")
	}
	id := req.GetDocumentId()
	if id == "" {
		id = autoID()
	} else if err := wire.CheckID(id, true); err != nil {
		return nil, err
	}
	// The whole name within production's limits (key path, name size).
	n, err := wire.ParseDocumentName(parent.Child(req.GetCollectionId()).Child(id).String())
	if err != nil {
		return nil, err
	}
	fields, err := fieldsOf(req.GetDocument())
	if err != nil {
		return nil, err
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	res, err := s.o.Store.Commit(ctx, a.db, []core.Write{{
		Kind: core.WriteUpdate, Path: pathOf(n), Fields: fields,
		Precondition: core.Precondition{Exists: new(false)},
	}})
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	t := res.Results[0].UpdateTime
	return documentProto(a.db, core.Document{Path: pathOf(n), Fields: fields, CreateTime: t, UpdateTime: t}, mask, masked), nil
}

// UpdateDocument sets a document's fields, all of them or those of the update mask, creating it
// unless a precondition says otherwise, and answers the document as the update left it
// (f1-update.json).
func (s *Service) UpdateDocument(ctx context.Context, c Call, req *pb.UpdateDocumentRequest) (*pb.Document, error) {
	a, err := s.admit(ctx, c, req, documentOf(req.GetDocument().GetName()))
	if err != nil {
		return nil, err
	}
	n, err := wire.ParseDocumentName(req.GetDocument().GetName())
	if err != nil {
		return nil, err
	}
	w := core.Write{Kind: core.WriteUpdate, Path: pathOf(n)}
	if req.GetUpdateMask() != nil {
		// Checked in production's order (value.Validate's doc): the BSON shapes of the update,
		// the mask's paths, then, in core, the document the mask makes.
		fields, err := wire.FieldsFromProto(req.GetDocument().GetFields())
		if err != nil {
			return nil, err
		}
		if err := value.CheckBSON(fields); err != nil {
			return nil, wire.InvalidArgument(err.Error())
		}
		if w.Mask, _, err = maskPaths(req.GetUpdateMask()); err != nil {
			return nil, err
		}
		w.Fields, w.Masked = stored(fields), true
	} else if w.Fields, err = fieldsOf(req.GetDocument()); err != nil {
		return nil, err
	}
	if w.Precondition, err = precondition(req.GetCurrentDocument()); err != nil {
		return nil, err
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	res, err := s.o.Store.Commit(ctx, a.db, []core.Write{w})
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	// The document as this commit left it: at its commit time, or the latest when the commit
	// changed nothing and so took no place in the order.
	var ro core.ReadOptions
	if res.Seq > 0 {
		ro.ReadTime = res.CommitTime
	}
	d, _, err := s.o.Store.Get(ctx, a.db, w.Path, ro)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return documentProto(a.db, d, mask, masked), nil
}

// DeleteDocument deletes a document; deleting one that does not exist is no error unless a
// precondition says so (f1-delete.json).
func (s *Service) DeleteDocument(ctx context.Context, c Call, req *pb.DeleteDocumentRequest) (*emptypb.Empty, error) {
	a, err := s.admit(ctx, c, req, documentOf(req.GetName()))
	if err != nil {
		return nil, err
	}
	n, err := wire.ParseDocumentName(req.GetName())
	if err != nil {
		return nil, err
	}
	pre, err := precondition(req.GetCurrentDocument())
	if err != nil {
		return nil, err
	}
	if _, err := s.o.Store.Commit(ctx, a.db, []core.Write{{Kind: core.WriteDelete, Path: pathOf(n), Precondition: pre}}); err != nil {
		return nil, s.fail(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

// ListCollectionIds lists the ids of the collections directly under a document or the root,
// whether or not the document exists (f1-list-collection-ids.json).
func (s *Service) ListCollectionIds(ctx context.Context, c Call, req *pb.ListCollectionIdsRequest) (*pb.ListCollectionIdsResponse, error) {
	a, err := s.admit(ctx, c, req, parentOf(req.GetParent()))
	if err != nil {
		return nil, err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return nil, err
	}
	if req.GetPageSize() < 0 {
		return nil, wire.InvalidArgument("page_size must be greater than or equal to zero.")
	}
	ro, err := readOptions(req.GetReadTime(), false)
	if err != nil {
		return nil, err
	}
	lr := core.ListCollectionIDsRequest{
		Parent:      pathOf(parent),
		PageSize:    min(int(req.GetPageSize()), core.MaxPageSize),
		ReadOptions: ro,
	}
	if tok := req.GetPageToken(); tok != "" {
		t, err := decodeToken(tok)
		if err != nil {
			return nil, err
		}
		if len(t.Listing) != 0 || len(t.After) != 1 {
			return nil, errGarbageToken
		}
		lr.After = t.After[0]
	}
	res, err := s.o.Store.ListCollectionIDs(ctx, a.db, lr)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := &pb.ListCollectionIdsResponse{CollectionIds: res.IDs}
	if res.More && len(res.IDs) > 0 {
		out.NextPageToken = encodeToken(pageToken{After: []string{res.IDs[len(res.IDs)-1]}})
	}
	return out, nil
}
