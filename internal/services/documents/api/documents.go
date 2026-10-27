package api

import (
	"cmp"
	"context"
	"errors"
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
	var txn *core.TransactionRef
	if t, ok := req.GetConsistencySelector().(*pb.GetDocumentRequest_Transaction); ok {
		txn = a.transaction(t.Transaction)
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return nil, err
	}
	d, readTime, err := s.o.Store.Get(ctx, a.db, pathOf(n), ro)
	if ce, ok := errors.AsType[*core.Error](err); ok && ce.Code == core.NotFound {
		// The rules decide first: a missing document is NOT_FOUND only to whom may read it.
		if err := s.allowReads(ctx, a, []core.Document{{Path: pathOf(n)}}, ro.ReadTime, readBudget(1)); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if err := s.allowReads(ctx, a, []core.Document{d}, readTime, readBudget(1)); err != nil {
		return nil, err
	}
	return documentProto(a.db, d, mask, masked), nil
}

// BatchGetDocuments reads documents and sends a response for each, through send: one that does
// not exist is missing, and each is answered once, in production's order (f1-batch-get.json
// order_run1-3, grpc.order, duplicates): those found in name order, then those missing, last
// name first. The missing ones' order rests on one recording of two names, which reads as
// descending names and as the request's order reversed alike; the clients match answers to
// names, so neither changes what they return.
//
// The documents are read batchGetChunk at a time, in name order, at the first read's time, and
// each chunk's found documents are sent before the next is read: a request's memory is bounded
// by a chunk of documents, not by every document it names (F1 review M3). In a transaction each
// chunk reads as the transaction does. With new_transaction the read runs in a transaction begun
// for it, whose id is the first response, alone (f1-batch-get.json new_transaction.*,
// new_transaction.empty_list).
func (s *Service) BatchGetDocuments(ctx context.Context, c Call, req *pb.BatchGetDocumentsRequest, send func(*pb.BatchGetDocumentsResponse) error) (err error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return err
	}
	paths := make([]core.Path, 0, len(req.GetDocuments()))
	for _, name := range req.GetDocuments() {
		n, err := wire.ParseDocumentName(name)
		if err != nil {
			return err
		}
		paths = append(paths, pathOf(n))
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return err
	}
	var txn *core.TransactionRef
	if t, ok := req.GetConsistencySelector().(*pb.BatchGetDocumentsRequest_Transaction); ok {
		txn = a.transaction(t.Transaction)
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return err
	}
	if nt, ok := req.GetConsistencySelector().(*pb.BatchGetDocumentsRequest_NewTransaction); ok {
		// err is the method's own: the deferred calls below see what it returns.
		var id []byte
		var end func(error)
		if id, end, err = s.newTransaction(ctx, a, nt.NewTransaction); err != nil {
			return err
		}
		defer func() { end(err) }()
		ro.Transaction = a.transaction(id)
		first := &pb.BatchGetDocumentsResponse{Transaction: id}
		read := send
		send = func(r *pb.BatchGetDocumentsResponse) error {
			if first != nil {
				f := first
				first = nil
				if err := read(f); err != nil {
					return err
				}
			}
			return read(r)
		}
		defer func() {
			if err == nil && first != nil {
				err = read(first)
			}
		}()
	}
	slices.SortFunc(paths, comparePaths)
	paths = slices.CompactFunc(paths, func(x, y core.Path) bool { return comparePaths(x, y) == 0 })
	var missing []core.Path
	var rt *timestamppb.Timestamp
	budget := readBudget(len(paths))
	for chunk := range slices.Chunk(paths, batchGetChunk) {
		docs, readTime, err := s.o.Store.BatchGet(ctx, a.db, chunk, ro)
		if err != nil {
			return s.fail(ctx, err)
		}
		if ro.Transaction == nil {
			ro.ReadTime = readTime
		}
		rt = timestamppb.New(readTime)
		// Each chunk is judged whole before any of it is sent: a denial ends the request.
		if err := s.allowReads(ctx, a, docs, readTime, budget); err != nil {
			return err
		}
		for _, d := range docs {
			if !d.Exists() {
				missing = append(missing, d.Path)
				continue
			}
			if err := send(&pb.BatchGetDocumentsResponse{ReadTime: rt,
				Result: &pb.BatchGetDocumentsResponse_Found{Found: documentProto(a.db, d, mask, masked)}}); err != nil {
				return err
			}
		}
	}
	if len(paths) == 0 {
		// Nothing to read but the database: it must exist, as production's answer says.
		if _, _, err := s.o.Store.BatchGet(ctx, a.db, nil, ro); err != nil {
			return s.fail(ctx, err)
		}
	}
	for _, p := range slices.Backward(missing) {
		if err := send(&pb.BatchGetDocumentsResponse{ReadTime: rt,
			Result: &pb.BatchGetDocumentsResponse_Missing{Missing: a.db.DocumentName(p)}}); err != nil {
			return err
		}
	}
	return nil
}

// batchGetChunk is how many documents BatchGetDocuments reads at a time: at most 100 MiB of
// documents of 1 MiB.
const batchGetChunk = 100

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
	var txn *core.TransactionRef
	if t, ok := req.GetConsistencySelector().(*pb.ListDocumentsRequest_Transaction); ok {
		txn = a.transaction(t.Transaction)
	}
	ro, err := readOptions(req.GetReadTime(), txn)
	if err != nil {
		return nil, err
	}
	// A list without filters (q1.list-documents). The page size as the limit is unrecorded.
	lq := listQuery{parent: pathOf(parent), collection: req.GetCollectionId(), orders: len(order)}
	if ps := req.GetPageSize(); ps > 0 {
		lq.limit = new(int64(ps))
	}
	if err := s.allowList(ctx, a, lq, ro.ReadTime); err != nil {
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
		return s.listOrdered(ctx, a, parent, lr, req, order, listing, mask, masked)
	}
	if tok := req.GetPageToken(); tok != "" {
		t, err := decodeToken(tok)
		if err != nil {
			return nil, err
		}
		if string(t.Listing) != string(listing) || t.Values != nil {
			return nil, errForeignToken
		}
		if lr.After, err = tokenAfter(t.After, parent, req.GetCollectionId()); err != nil {
			return nil, err
		}
	}
	// The page is read listChunk documents at a time, at the first read's time, and ends at its
	// size or once it holds maxPageBytes of documents: a short page, which every client follows
	// by its token (F1 review M3).
	want := cmp.Or(lr.PageSize, core.MaxPageSize)
	out := &pb.ListDocumentsResponse{}
	var next core.Path
	for size := 0; ; {
		chunk := lr
		chunk.PageSize = min(want-len(out.Documents), listChunk)
		res, err := s.o.Store.ListDocuments(ctx, a.db, chunk)
		if err != nil {
			return nil, s.fail(ctx, err)
		}
		if lr.Transaction == nil {
			// In a transaction every chunk reads as the transaction does.
			lr.ReadTime = res.ReadTime
		}
		next = res.Next
		for i, d := range res.Documents {
			out.Documents = append(out.Documents, documentProto(a.db, d, mask, masked))
			if size += docSize(d); size >= maxPageBytes && (i < len(res.Documents)-1 || res.Next != nil) {
				next = d.Path
				break
			}
		}
		if next == nil || len(out.Documents) >= want || size >= maxPageBytes {
			break
		}
		lr.After = next
	}
	if next != nil {
		out.NextPageToken = encodeToken(pageToken{Listing: listing, After: next})
	}
	return out, nil
}

// A ListDocuments page is read listChunk documents at a time and holds at most about
// maxPageBytes of documents, by value.DocumentSize. Production's bound on a page's bytes is not
// recorded: these are this server's, so a page of 1000 documents of 1 MiB is neither held nor
// sent at once.
const (
	listChunk    = 100
	maxPageBytes = 16 << 20
)

// docSize is a document's size as production measures it, the bound of a page's bytes.
func docSize(d core.Document) int {
	if !d.Exists() {
		return value.NameSize(d.Path.String())
	}
	return value.DocumentSize(d.Path.String(), d.Fields)
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
	ctx, err = s.judged(ctx, a)
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
	if w.Fields, w.Mask, w.Masked, err = updateOf(req.GetDocument(), req.GetUpdateMask()); err != nil {
		return nil, err
	}
	if w.Precondition, err = precondition(req.GetCurrentDocument()); err != nil {
		return nil, err
	}
	mask, masked, err := maskPaths(req.GetMask())
	if err != nil {
		return nil, err
	}
	jctx, err := s.judged(ctx, a)
	if err != nil {
		return nil, err
	}
	res, err := s.o.Store.Commit(jctx, a.db, []core.Write{w})
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
	ctx, err = s.judged(ctx, a)
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
	if err := a.requireBypass(); err != nil {
		return nil, err
	}
	parent, err := wire.ParseParentName(req.GetParent())
	if err != nil {
		return nil, err
	}
	if req.GetPageSize() < 0 {
		return nil, wire.InvalidArgument("page_size must be greater than or equal to zero.")
	}
	// ListCollectionIdsRequest has no transaction: a read_time or the latest.
	ro, err := readOptions(req.GetReadTime(), nil)
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
