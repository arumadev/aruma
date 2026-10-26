package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// fake is the Firestore both RPC stacks serve: just enough of Commit, BatchGetDocuments, Listen
// and Write for the official clients to complete a call, hold a stream and finish the Write
// handshake. It stores nothing.
type fake struct {
	// later is how long after a target is added Listen sends one more document and snapshot,
	// which the clients must receive on the stream they still hold.
	later time.Duration
	// drain ends every open stream with errDraining when it is done: the server is shutting
	// down, and http.Server.Shutdown waits for streams to end on their own.
	drain   context.Context
	streams atomic.Int64
	flooded atomic.Int64
}

// errDraining ends a stream on shutdown; each stack sends it as UNAVAILABLE, which the clients
// retry with backoff, against another instance or this one restarted.
var errDraining = errors.New("the server is shutting down")

// bidi is the part of a bidirectional stream the fake uses; each stack adapts its own.
type bidi[Req, Res any] interface {
	Context() context.Context
	Recv() (*Req, error)
	Send(*Res) error
}

func (f *fake) commit(req *pb.CommitRequest) *pb.CommitResponse {
	now := timestamppb.Now()
	return &pb.CommitResponse{WriteResults: writeResults(req.GetWrites(), now), CommitTime: now}
}

// batchGet answers every document as missing.
func (f *fake) batchGet(req *pb.BatchGetDocumentsRequest, send func(*pb.BatchGetDocumentsResponse) error) error {
	for _, name := range req.GetDocuments() {
		res := &pb.BatchGetDocumentsResponse{Result: &pb.BatchGetDocumentsResponse_Missing{Missing: name}, ReadTime: timestamppb.Now()}
		if err := send(res); err != nil {
			return err
		}
	}
	return nil
}

// receive reads a stream's requests on their own goroutine, so a handler can also wait for the
// drain. The channel closes at the end of the stream; its error, if any, is in *err.
func receive[Req, Res any](s bidi[Req, Res], err *error) <-chan *Req {
	ch := make(chan *Req)
	go func() {
		defer close(ch)
		for {
			req, e := s.Recv()
			if e != nil {
				if !errors.Is(e, io.EOF) {
					*err = e
				}
				return
			}
			select {
			case ch <- req:
			case <-s.Context().Done():
				return
			}
		}
	}()
	return ch
}

// listen answers each added target with ADD, CURRENT (with a resume token) and a global
// NO_CHANGE carrying read_time, then keeps the stream open; `later` after the target was added
// it sends a document for the target and another NO_CHANGE. A removed target gets REMOVE.
func (f *fake) listen(s bidi[pb.ListenRequest, pb.ListenResponse]) error {
	ctx, cancel := context.WithCancelCause(s.Context())
	defer cancel(nil)
	var mu sync.Mutex // Send is not safe for concurrent use
	send := func(res *pb.ListenResponse) error {
		mu.Lock()
		defer mu.Unlock()
		return s.Send(res)
	}
	var recvErr error
	reqs := receive(s, &recvErr)
	for {
		var req *pb.ListenRequest
		select {
		case <-f.drain.Done():
			return errDraining
		case <-ctx.Done():
			return context.Cause(ctx)
		case r, ok := <-reqs:
			if !ok {
				return recvErr
			}
			req = r
		}
		switch t := req.GetTargetChange().(type) {
		case *pb.ListenRequest_AddTarget:
			id := t.AddTarget.GetTargetId()
			now := timestamppb.Now()
			current := targetChange(pb.TargetChange_CURRENT, nil, id)
			current.GetTargetChange().ResumeToken = fmt.Appendf(nil, "spike-%d-%d", id, now.AsTime().UnixMicro())
			for _, res := range []*pb.ListenResponse{targetChange(pb.TargetChange_ADD, nil, id), current, targetChange(pb.TargetChange_NO_CHANGE, now)} {
				if err := send(res); err != nil {
					return err
				}
			}
			if id == floodTarget {
				if err := f.flood(send); err != nil {
					return err
				}
			}
			name := laterName(t.AddTarget)
			timer := time.AfterFunc(f.later, func() {
				now := timestamppb.Now()
				doc := &pb.Document{
					Name:       name,
					Fields:     map[string]*pb.Value{"later": {ValueType: &pb.Value_BooleanValue{BooleanValue: true}}},
					CreateTime: now,
					UpdateTime: now,
				}
				change := &pb.ListenResponse{ResponseType: &pb.ListenResponse_DocumentChange{
					DocumentChange: &pb.DocumentChange{Document: doc, TargetIds: []int32{id}}}}
				for _, res := range []*pb.ListenResponse{change, targetChange(pb.TargetChange_NO_CHANGE, timestamppb.Now())} {
					if err := send(res); err != nil {
						cancel(err)
						return
					}
				}
			})
			defer timer.Stop()
		case *pb.ListenRequest_RemoveTarget:
			if err := send(targetChange(pb.TargetChange_REMOVE, nil, t.RemoveTarget)); err != nil {
				return err
			}
		}
	}
}

// floodTarget is a target id for the backpressure test: Listen sends it 1 MiB documents until
// Send fails, counting each Send that returned in fake.flooded. With a client that reads
// nothing, the count shows how much a stack buffers before Send blocks.
const floodTarget = 999

func (f *fake) flood(send func(*pb.ListenResponse) error) error {
	big := &pb.Value{ValueType: &pb.Value_StringValue{StringValue: strings.Repeat("x", 1<<20)}}
	for i := 0; ; i++ {
		doc := &pb.Document{
			Name:   fmt.Sprintf("projects/spike-project/databases/(default)/documents/flood/%d", i),
			Fields: map[string]*pb.Value{"big": big}, CreateTime: timestamppb.Now(), UpdateTime: timestamppb.Now(),
		}
		res := &pb.ListenResponse{ResponseType: &pb.ListenResponse_DocumentChange{
			DocumentChange: &pb.DocumentChange{Document: doc, TargetIds: []int32{floodTarget}}}}
		if err := send(res); err != nil {
			return err
		}
		f.flooded.Add(1)
	}
}

// laterName is the document Listen sends later for a target: the target's own document, or a
// document "later" in the queried collection.
func laterName(t *pb.Target) string {
	switch tt := t.GetTargetType().(type) {
	case *pb.Target_Documents:
		if docs := tt.Documents.GetDocuments(); len(docs) > 0 {
			return docs[0]
		}
	case *pb.Target_Query:
		q := tt.Query
		if from := q.GetStructuredQuery().GetFrom(); len(from) > 0 {
			return strings.TrimSuffix(q.GetParent(), "/") + "/" + from[0].GetCollectionId() + "/later"
		}
	}
	return "projects/spike-project/databases/(default)/documents/spike/later"
}

func targetChange(kind pb.TargetChange_TargetChangeType, readTime *timestamppb.Timestamp, ids ...int32) *pb.ListenResponse {
	return &pb.ListenResponse{ResponseType: &pb.ListenResponse_TargetChange{
		TargetChange: &pb.TargetChange{TargetChangeType: kind, TargetIds: ids, ReadTime: readTime}}}
}

// write runs the Write stream: the handshake (a request with no stream token and no writes) gets
// a stream id and token and no results, and each later request one result per write with a
// commit time, in order.
func (f *fake) write(s bidi[pb.WriteRequest, pb.WriteResponse]) error {
	id := f.streams.Add(1)
	var recvErr error
	reqs := receive(s, &recvErr)
	for n := 0; ; n++ {
		var req *pb.WriteRequest
		select {
		case <-f.drain.Done():
			return errDraining
		case r, ok := <-reqs:
			if !ok {
				return recvErr
			}
			req = r
		}
		token := fmt.Appendf(nil, "spike-%d-%d", id, n)
		res := &pb.WriteResponse{StreamToken: token}
		if n == 0 {
			res.StreamId = fmt.Sprintf("spike-%d", id)
		} else {
			res.CommitTime = timestamppb.Now()
			res.WriteResults = writeResults(req.GetWrites(), res.CommitTime)
		}
		if err := s.Send(res); err != nil {
			return err
		}
	}
}

// writeResults is one result per write, with one value per field transform: the request time,
// or the transform's own operand.
func writeResults(writes []*pb.Write, now *timestamppb.Timestamp) []*pb.WriteResult {
	out := make([]*pb.WriteResult, len(writes))
	for i, w := range writes {
		r := &pb.WriteResult{UpdateTime: now}
		transforms := w.GetUpdateTransforms()
		if t := w.GetTransform(); t != nil {
			transforms = append(transforms, t.GetFieldTransforms()...)
		}
		for _, t := range transforms {
			v := &pb.Value{ValueType: &pb.Value_TimestampValue{TimestampValue: now}}
			switch tt := t.GetTransformType().(type) {
			case *pb.DocumentTransform_FieldTransform_Increment:
				v = tt.Increment
			case *pb.DocumentTransform_FieldTransform_Maximum:
				v = tt.Maximum
			case *pb.DocumentTransform_FieldTransform_Minimum:
				v = tt.Minimum
			case *pb.DocumentTransform_FieldTransform_AppendMissingElements, *pb.DocumentTransform_FieldTransform_RemoveAllFromArray:
				v = &pb.Value{ValueType: &pb.Value_NullValue{}}
			}
			r.TransformResults = append(r.TransformResults, v)
		}
		out[i] = r
	}
	return out
}
