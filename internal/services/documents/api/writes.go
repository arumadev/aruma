package api

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/maphash"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/status"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The F2 write methods outside transactions: Commit and BatchWrite (f1-write-results.json).

// Production's answers to writes it refuses before reading anything (f2-transforms.json,
// f2-commit.json, f2-batch-write.json).
var (
	errNoOperation       = wire.InvalidArgument("empty write operation")
	errNoFieldTransforms = wire.InvalidArgument("The list of FieldTransform must not be empty.")
	errNoTransformType   = wire.InvalidArgument("Operation type must be specified for field transformation.")
	errServerValue       = wire.InvalidArgument("ServerValue type must be specified for set_to_server_value transformation.")
	errNotNumber         = wire.InvalidArgument("Input must be a number.")
	errNestedArray       = wire.InvalidArgument("Nested arrays are not allowed")
	// errTransformName is a transform of __name__, which production names by its Datastore name.
	errTransformName = wire.InvalidArgument("the name __key__ is reserved")
	errSameDocument  = wire.InvalidArgument("the same document cannot be written more than once in a single request")
	// errInsertTwice and errWriteThenVerify are a Commit's two writes of one document that
	// production refuses (f2-commit.json same.create_create, same.update_then_verify_exists).
	errInsertTwice     = wire.InvalidArgument("Cannot insert then insert an entity in the same request.")
	errWriteThenVerify = wire.InvalidArgument("the same document cannot be written and then verified in a single request")
	// errVerifyPrecondition is a verify without a current_document (f1-write-results.json
	// rest.verify_no_precondition).
	errVerifyPrecondition = wire.InvalidArgument("a verify must contain a precondition")
)

// The bounds of a request's writes, checked before any of them is read, so that the work a
// request makes before its rules and its size checks is bounded by its count as well as by its
// bytes (message_bytes). Production's are not recorded beyond its acceptance of 501 writes
// (f2-commit.json, f2-batch-write.json limit.501_writes); these are far above every recorded
// case (parity request-work-bounds).
const (
	// MaxRequestWrites bounds a Commit's or a BatchWrite's writes.
	MaxRequestWrites = 10_000
	// MaxRequestTransforms bounds the field transforms of all of a request's writes.
	MaxRequestTransforms = 50_000
)

var (
	errTooManyWrites     = wire.InvalidArgument("A request may hold at most 10000 writes.")
	errTooManyTransforms = wire.InvalidArgument("A request may hold at most 50000 field transforms.")
)

// errBSONNumber is an Enterprise BSON int32 or Decimal128 that a numeric or array transform
// meets in its operand, which Standard refuses (f2-transforms.json bson.*).
func errBSONNumber(k value.BSONKind) error {
	return wire.InvalidArgument("Value type " + bsonTypeName[k] + " is not allowed")
}

var bsonTypeName = map[value.BSONKind]string{value.BSONInt32: "INT", value.BSONDecimal128: "DECIMAL128"}

// operationName is how production names a write's operation in its refusals.
func operationName(w *pb.Write) string {
	switch w.GetOperation().(type) {
	case *pb.Write_Delete:
		return "delete"
	case *pb.Write_Verify:
		return "verify"
	}
	return "transform"
}

// Commit applies writes atomically, in order (f1-write-results.json), in the request's
// transaction when it names one, which the commit ends whatever its outcome (f0-errors.json
// txn_unknown.commit, txn_reused.*; f0-contention.json).
func (s *Service) Commit(ctx context.Context, c Call, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	res, err := s.commitWrites(ctx, a, req.GetWrites(), req.GetTransaction())
	if err != nil {
		return nil, err
	}
	if len(req.GetWrites()) == 0 && len(req.GetTransaction()) == 0 {
		// Production answers a commit of no writes with an empty message, no commit_time
		// (f1-write-results.json rest.empty); in a transaction, with its commit_time
		// (f2-transactions.json read.commit_read_write_empty, ro.commit_empty).
		return &pb.CommitResponse{}, nil
	}
	out := &pb.CommitResponse{CommitTime: timestamp(res.CommitTime), WriteResults: make([]*pb.WriteResult, len(res.Results))}
	for i, r := range res.Results {
		out.WriteResults[i] = writeResultProto(r)
	}
	return out, nil
}

// commitWrites is Commit's work after its admission, which the Write stream's batches share:
// the writes read and checked, then applied atomically, in the transaction when there is one.
func (s *Service) commitWrites(ctx context.Context, a admitted, ws []*pb.Write, txn []byte) (core.CommitResult, error) {
	writes, err := writesOf(ws)
	if err != nil {
		return core.CommitResult{}, err
	}
	if err := sameDocumentWrites(writes); err != nil {
		return core.CommitResult{}, err
	}
	ctx, err = s.judged(ctx, a)
	if err != nil {
		return core.CommitResult{}, err
	}
	var res core.CommitResult
	if len(txn) > 0 {
		res, err = s.o.Store.CommitTransaction(ctx, a.db, *a.transaction(txn), writes)
	} else {
		res, err = s.o.Store.Commit(ctx, a.db, writes)
	}
	if err != nil {
		return core.CommitResult{}, s.fail(ctx, err)
	}
	s.o.Usage.Wrote(a.db.Project, writes)
	return res, nil
}

// sameDocumentWrites checks a Commit's writes of one document: production applies them in order
// (f2-commit.json same.*) but for two that insert it (exists=false) and for a verify after a
// write of it.
func sameDocumentWrites(writes []core.Write) error {
	inserted, written := map[string]bool{}, map[string]bool{}
	for _, w := range writes {
		k := w.Path.String()
		switch {
		case w.Kind == core.WriteVerify:
			if written[k] {
				return errWriteThenVerify
			}
		case w.Precondition.Exists != nil && !*w.Precondition.Exists:
			if inserted[k] {
				return errInsertTwice
			}
			inserted[k], written[k] = true, true
		default:
			written[k] = true
		}
	}
	return nil
}

// batchWriteParallel is how many of a BatchWrite's writes are committed at once; the commit
// gate of the database bounds them too.
const batchWriteParallel = 4

// BatchWrite applies each write on its own, not atomically and in no order, and answers each
// one's status and result: a write that failed has its error and an empty result. The request
// as a whole is refused before any write when it does not read (names, values, transforms), or
// names a document twice, and when its database does not exist.
func (s *Service) BatchWrite(ctx context.Context, c Call, req *pb.BatchWriteRequest) (*pb.BatchWriteResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	// Production refuses BatchWrite to end users, even every write allowed (rules.go).
	if err := a.refuseHeld(); err != nil {
		return nil, err
	}
	writes, err := writesOf(req.GetWrites())
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(writes))
	for _, w := range writes {
		k := w.Path.String()
		if seen[k] {
			return nil, errSameDocument
		}
		seen[k] = true
	}
	ctx, err = s.judged(ctx, a)
	if err != nil {
		return nil, err
	}
	results := make([]core.CommitResult, len(writes))
	errs := make([]error, len(writes))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(batchWriteParallel, len(writes)) {
		wg.Go(func() {
			for i := range next {
				if results[i], errs[i] = s.o.Store.Commit(ctx, a.db, writes[i:i+1]); errs[i] == nil {
					s.o.Usage.Wrote(a.db.Project, writes[i:i+1])
				}
			}
		})
	}
	for i := range writes {
		next <- i
	}
	close(next)
	wg.Wait()
	out := &pb.BatchWriteResponse{WriteResults: make([]*pb.WriteResult, len(writes)), Status: make([]*status.Status, len(writes))}
	for i, err := range errs {
		if err == nil {
			out.WriteResults[i], out.Status[i] = writeResultProto(results[i].Results[0]), &status.Status{}
			continue
		}
		if _, missing := errors.AsType[*core.DatabaseNotFoundError](err); missing || ctx.Err() != nil {
			return nil, s.fail(ctx, err)
		}
		out.WriteResults[i], out.Status[i] = &pb.WriteResult{}, wire.From(s.fail(ctx, err)).Status()
	}
	return out, nil
}

// writeResultProto is a write's result on the wire.
func writeResultProto(r core.WriteResult) *pb.WriteResult {
	out := &pb.WriteResult{UpdateTime: timestamp(r.UpdateTime)}
	for _, v := range r.TransformResults {
		out.TransformResults = append(out.TransformResults, wire.ValueToProto(v))
	}
	return out
}

// writesOf reads a request's writes, each checked as production checks it before the commit,
// once their count is within the request's bounds.
func writesOf(ws []*pb.Write) ([]core.Write, error) {
	if len(ws) > MaxRequestWrites {
		return nil, errTooManyWrites
	}
	transforms := 0
	for _, w := range ws {
		transforms += len(w.GetUpdateTransforms()) + len(w.GetTransform().GetFieldTransforms())
	}
	if transforms > MaxRequestTransforms {
		return nil, errTooManyTransforms
	}
	out := make([]core.Write, len(ws))
	for i, w := range ws {
		var err error
		if out[i], err = writeOf(w); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeOf reads one write: an update (with its mask and transforms), a delete, a verify, or a
// legacy transform, which is an update of no field with its transforms (Write.transform).
func writeOf(w *pb.Write) (core.Write, error) {
	var out core.Write
	var name string
	switch op := w.GetOperation().(type) {
	case *pb.Write_Update:
		out.Kind, name = core.WriteUpdate, op.Update.GetName()
	case *pb.Write_Delete:
		out.Kind, name = core.WriteDelete, op.Delete
	case *pb.Write_Verify:
		out.Kind, name = core.WriteVerify, op.Verify
	case *pb.Write_Transform:
		out.Kind, name, out.Masked, out.Fields = core.WriteUpdate, op.Transform.GetDocument(), true, value.Map()
	default:
		return core.Write{}, errNoOperation
	}
	n, err := wire.ParseDocumentName(name)
	if err != nil {
		return core.Write{}, err
	}
	out.Path = pathOf(n)
	if w.GetUpdate() == nil {
		switch {
		case len(w.GetUpdateTransforms()) > 0:
			return core.Write{}, wire.InvalidArgument("a " + operationName(w) + " must not specify a update transform.")
		case w.GetUpdateMask() != nil:
			return core.Write{}, wire.InvalidArgument("a " + operationName(w) + " must not specify a document mask.")
		}
	}
	switch op := w.GetOperation().(type) {
	case *pb.Write_Update:
		if out.Fields, out.Mask, out.Masked, err = updateOf(op.Update, w.GetUpdateMask()); err != nil {
			return core.Write{}, err
		}
		if out.Transforms, err = transformsOf(w.GetUpdateTransforms()); err != nil {
			return core.Write{}, err
		}
	case *pb.Write_Transform:
		if len(op.Transform.GetFieldTransforms()) == 0 {
			return core.Write{}, errNoFieldTransforms
		}
		if out.Transforms, err = transformsOf(op.Transform.GetFieldTransforms()); err != nil {
			return core.Write{}, err
		}
	}
	if out.Precondition, err = precondition(w.GetCurrentDocument()); err != nil {
		return core.Write{}, err
	}
	if out.Kind == core.WriteVerify && w.GetCurrentDocument().GetConditionType() == nil {
		return core.Write{}, errVerifyPrecondition
	}
	return out, nil
}

// updateOf reads an update's document and update mask, as UpdateDocument and Commit check
// them: without a mask, the document as a whole (fieldsOf); with one, in production's order
// (value.Validate's doc), the BSON shapes of the update, then the mask's paths, leaving the
// document the mask makes to core.
func updateOf(d *pb.Document, updateMask *pb.DocumentMask) (fields value.Value, mask []value.FieldPath, masked bool, err error) {
	if updateMask == nil {
		fields, err = fieldsOf(d)
		return fields, nil, false, err
	}
	if fields, err = wire.FieldsFromProto(d.GetFields()); err != nil {
		return value.Value{}, nil, false, err
	}
	if err := value.CheckBSON(fields); err != nil {
		return value.Value{}, nil, false, wire.InvalidArgument(err.Error())
	}
	if mask, _, err = maskPaths(updateMask); err != nil {
		return value.Value{}, nil, false, err
	}
	return stored(fields), mask, true, nil
}

// transformsOf reads a write's field transforms. Each field path parses and names a field a
// mask may name; the numeric transforms take an integer or a double, the array ones an array
// whose values are stored as a write's are. The document they make is checked in core.
func transformsOf(fts []*pb.DocumentTransform_FieldTransform) ([]core.Transform, error) {
	if len(fts) == 0 {
		return nil, nil
	}
	out := make([]core.Transform, len(fts))
	for i, ft := range fts {
		p, err := value.ParseFieldPath(ft.GetFieldPath())
		if err != nil {
			return nil, wire.InvalidArgument(err.Error())
		}
		if p.IsDocumentID() {
			return nil, errTransformName
		}
		if err := value.ValidateMaskPath(p); err != nil {
			return nil, wire.InvalidArgument(err.Error())
		}
		t := core.Transform{Path: p}
		switch tt := ft.GetTransformType().(type) {
		case *pb.DocumentTransform_FieldTransform_SetToServerValue:
			if tt.SetToServerValue != pb.DocumentTransform_FieldTransform_REQUEST_TIME {
				return nil, errServerValue
			}
			t.Kind = core.TransformServerTime
		case *pb.DocumentTransform_FieldTransform_Increment:
			t.Kind, err = core.TransformIncrement, number(&t.Operand, tt.Increment)
		case *pb.DocumentTransform_FieldTransform_Maximum:
			t.Kind, err = core.TransformMaximum, number(&t.Operand, tt.Maximum)
		case *pb.DocumentTransform_FieldTransform_Minimum:
			t.Kind, err = core.TransformMinimum, number(&t.Operand, tt.Minimum)
		case *pb.DocumentTransform_FieldTransform_AppendMissingElements:
			t.Kind, err = core.TransformAppendMissing, array(&t.Operand, tt.AppendMissingElements)
		case *pb.DocumentTransform_FieldTransform_RemoveAllFromArray:
			t.Kind, err = core.TransformRemoveAll, array(&t.Operand, tt.RemoveAllFromArray)
		default:
			return nil, errNoTransformType
		}
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	// Several transforms of one field apply in order; a field and one inside it may not both be
	// transformed (f2-transforms.json multi.*).
	if parent, ok := nestedTransform(out); ok {
		return nil, wire.InvalidArgument("Cannot transform property " + parent.String() + " and its nested property at the same time.")
	}
	return out, nil
}

// nestedTransform finds the first transform, in order, whose field holds or is inside the field
// of one before it, and returns the outer field of it and of the first such one before it: the
// pair production names. Each field is looked up by the hash of each of its prefixes, so the
// check costs the paths' segments, not the square of their number (F2 review M1); a hash is
// only a candidate, which IsPrefixOf confirms.
func nestedTransform(ts []core.Transform) (value.FieldPath, bool) {
	seed := maphash.MakeSeed()
	whole := map[uint64][]int{}  // by the hash of a transform's path: the transforms with it
	inside := map[uint64][]int{} // by the hash of a proper prefix: the transforms below it
	var h maphash.Hash
	for i, t := range ts {
		if len(t.Path) == 0 {
			continue
		}
		h.SetSeed(seed)
		prefixes := make([]uint64, len(t.Path)) // prefixes[k]: the hash of t.Path[:k+1]
		for k, seg := range t.Path {
			var n [binary.MaxVarintLen64]byte
			h.Write(n[:binary.PutUvarint(n[:], uint64(len(seg)))])
			h.WriteString(seg)
			prefixes[k] = h.Sum64()
		}
		first, parent := -1, value.FieldPath(nil)
		// One before it that holds it.
		for k := range len(t.Path) - 1 {
			for _, j := range whole[prefixes[k]] {
				if first >= 0 && j > first {
					break
				}
				if len(ts[j].Path) == k+1 && ts[j].Path.IsPrefixOf(t.Path) {
					first, parent = j, ts[j].Path
					break
				}
			}
		}
		// One before it inside it.
		for _, j := range inside[prefixes[len(t.Path)-1]] {
			if first >= 0 && j > first {
				break
			}
			if t.Path.IsPrefixOf(ts[j].Path) {
				first, parent = j, t.Path
				break
			}
		}
		if first >= 0 {
			return parent, true
		}
		whole[prefixes[len(prefixes)-1]] = append(whole[prefixes[len(prefixes)-1]], i)
		for _, p := range prefixes[:len(prefixes)-1] {
			inside[p] = append(inside[p], i)
		}
	}
	return nil, false
}

// number reads the operand of increment, maximum and minimum: an integer or a double.
func number(dst *value.Value, v *pb.Value) error {
	x, err := wire.ValueFromProto(v)
	if err != nil {
		return err
	}
	if k := x.BSONKind(); bsonTypeName[k] != "" {
		return errBSONNumber(k)
	}
	if !x.IsNumber() {
		return errNotNumber
	}
	*dst = x
	return nil
}

// array reads the operand of the array transforms; a missing array is empty.
func array(dst *value.Value, a *pb.ArrayValue) error {
	x, err := wire.ValueFromProto(&pb.Value{ValueType: &pb.Value_ArrayValue{ArrayValue: a}})
	if err != nil {
		return err
	}
	for _, e := range x.Elements() {
		if e.Kind() == value.KindArray {
			return errNestedArray
		}
	}
	if k := bsonNumberIn(x); k != value.NotBSON {
		return errBSONNumber(k)
	}
	if err := value.CheckBSON(x); err != nil {
		return wire.InvalidArgument(err.Error())
	}
	*dst = stored(x)
	return nil
}

// bsonNumberIn returns the first BSON int32 or Decimal128 in v, NotBSON when it holds none.
func bsonNumberIn(v value.Value) value.BSONKind {
	if k := v.BSONKind(); k != value.NotBSON {
		if bsonTypeName[k] != "" {
			return k
		}
		return value.NotBSON
	}
	var els []value.Value
	switch v.Kind() {
	case value.KindArray:
		els = v.Elements()
	case value.KindMap:
		for _, f := range v.Fields() {
			els = append(els, f.Value)
		}
	}
	for _, e := range els {
		if k := bsonNumberIn(e); k != value.NotBSON {
			return k
		}
	}
	return value.NotBSON
}
