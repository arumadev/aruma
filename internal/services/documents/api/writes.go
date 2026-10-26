package api

import (
	"context"
	"errors"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/status"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The F2 write methods outside transactions: Commit and BatchWrite (f1-write-results.json).

// Provisional texts: production's answers to these requests are not recorded. The SDKs never
// send them.
var (
	errNoOperation        = wire.InvalidArgument("A write must have an operation.")
	errTransformsNoUpdate = wire.InvalidArgument("update_transforms can only be set on an update.")
	errMaskNoUpdate       = wire.InvalidArgument("update_mask can only be set on an update.")
	errNoFieldTransforms  = wire.InvalidArgument("A transform must contain at least one field transform.")
	errNoTransformType    = wire.InvalidArgument("A field transform must have a transform type.")
	errServerValue        = wire.InvalidArgument("Unknown server value.")
	errNotNumber          = wire.InvalidArgument("Increment, maximum and minimum must be given an integer or a double.")
	errTransformName      = wire.InvalidArgument("Cannot transform the document's name.")
	errSameDocument       = wire.InvalidArgument("A batch write cannot contain more than one write to the same document.")
)

// errVerifyPrecondition is production's answer to a verify without a current_document
// (f1-write-results.json rest.verify_no_precondition).
var errVerifyPrecondition = wire.InvalidArgument("a verify must contain a precondition")

// Commit applies writes atomically, in order (f1-write-results.json), in the request's
// transaction when it names one, which the commit ends whatever its outcome (f0-errors.json
// txn_unknown.commit, txn_reused.*; f0-contention.json).
func (s *Service) Commit(ctx context.Context, c Call, req *pb.CommitRequest) (*pb.CommitResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, err
	}
	writes, err := writesOf(req.GetWrites())
	if err != nil {
		return nil, err
	}
	var res core.CommitResult
	if len(req.GetTransaction()) > 0 {
		res, err = s.o.Store.CommitTransaction(ctx, a.db, *a.transaction(req.GetTransaction()), writes)
	} else {
		res, err = s.o.Store.Commit(ctx, a.db, writes)
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if len(writes) == 0 {
		// Production answers a commit of no writes with an empty message, no commit_time
		// (f1-write-results.json rest.empty).
		return &pb.CommitResponse{}, nil
	}
	out := &pb.CommitResponse{CommitTime: timestamp(res.CommitTime), WriteResults: make([]*pb.WriteResult, len(res.Results))}
	for i, r := range res.Results {
		out.WriteResults[i] = writeResultProto(r)
	}
	return out, nil
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
	results := make([]core.CommitResult, len(writes))
	errs := make([]error, len(writes))
	var wg sync.WaitGroup
	next := make(chan int)
	for range min(batchWriteParallel, len(writes)) {
		wg.Go(func() {
			for i := range next {
				results[i], errs[i] = s.o.Store.Commit(ctx, a.db, writes[i:i+1])
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

// writesOf reads a request's writes, each checked as production checks it before the commit.
func writesOf(ws []*pb.Write) ([]core.Write, error) {
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
			return core.Write{}, errTransformsNoUpdate
		case w.GetUpdateMask() != nil:
			return core.Write{}, errMaskNoUpdate
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
	return out, nil
}

// number reads the operand of increment, maximum and minimum: an integer or a double.
func number(dst *value.Value, v *pb.Value) error {
	x, err := wire.ValueFromProto(v)
	if err != nil {
		return err
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
	if err := value.CheckBSON(x); err != nil {
		return wire.InvalidArgument(err.Error())
	}
	*dst = stored(x)
	return nil
}
