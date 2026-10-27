package api

import (
	"context"
	"crypto/rand"
	"encoding/binary"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The Write stream's logic (ADR-026 §6): a handshake, then each request is one atomic batch,
// executed as Commit executes it. The transport (package grpc) owns the stream's lifetime, its
// deadlines and the order of its requests. Every refusal below is production's, status and text,
// as recorded in corpus/f4-write-stream.json.
var (
	// errHandshakeWrites: a first request with writes (write.no-handshake).
	errHandshakeWrites = wire.InvalidArgument("the first request in a stream cannot contain writes")
	// errNoToken: a later request without a stream_token, which includes a second handshake
	// (write.no-handshake, write.token-reuse).
	errNoToken = wire.InvalidArgument("missing stream token")
	// errBadToken: a stream_token that is not a protobuf message (write.token-reuse).
	errBadToken = wire.InvalidArgument("invalid stream token")
	// errResume: a first request that names a stream to resume (write.resume-stream).
	errResume = wire.NewError(wire.CodeAborted, "resuming a stream not supported")
	// errEmptyBatch: an empty writes list after the handshake, which production answers by
	// cancelling the stream (write.empty-first, write.handshake-and-batches).
	errEmptyBatch = wire.NewError(wire.CodeCanceled, "The operation was cancelled.")
)

// WriteStream is one open Write stream, admitted at its handshake. It is not safe for concurrent
// use: the transport calls it one request at a time, in order.
type WriteStream struct {
	s    *Service
	a    admitted
	id   string
	part [8]byte // the stream's part of every token
	last uint64  // the sequence of the last token sent
}

// Admission is who the stream was admitted for.
func (w *WriteStream) Admission() Admission { return w.a.public() }

// streamIDAlphabet is the alphabet of the stream id, a one character string as production's is.
const streamIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// OpenWriteStream admits the caller to the database a stream's first request names, as Commit
// does, and answers the handshake: the stream's id and a first token, no write results.
func (s *Service) OpenWriteStream(ctx context.Context, c Call, req *pb.WriteRequest) (*WriteStream, *pb.WriteResponse, error) {
	a, err := s.admit(ctx, c, req, databaseOf(req.GetDatabase()))
	if err != nil {
		return nil, nil, err
	}
	if len(req.GetWrites()) > 0 {
		return nil, nil, errHandshakeWrites
	}
	if req.GetStreamId() != "" || len(req.GetStreamToken()) > 0 {
		return nil, nil, errResume
	}
	w := &WriteStream{s: s, a: a}
	var id [1]byte
	_, _ = rand.Read(id[:])
	w.id = string(streamIDAlphabet[int(id[0])%len(streamIDAlphabet)])
	_, _ = rand.Read(w.part[:])
	return w, &pb.WriteResponse{StreamId: w.id, StreamToken: w.token()}, nil
}

// token is the next stream_token, a protobuf as production's: the batch counter (field 2, absent
// in the handshake's) and the stream's 8 bytes (field 3, fixed64). Clients send back the last they
// saw, and the server only parses it.
func (w *WriteStream) token() []byte {
	var b []byte
	if w.last > 0 {
		b = protowire.AppendTag(b, 2, protowire.VarintType)
		b = protowire.AppendVarint(b, w.last)
	}
	b = protowire.AppendTag(b, 3, protowire.Fixed64Type)
	b = binary.LittleEndian.AppendUint64(b, binary.LittleEndian.Uint64(w.part[:]))
	w.last++
	return b
}

// validToken reports whether b parses as a protobuf message.
func validToken(b []byte) bool {
	for len(b) > 0 {
		_, _, n := protowire.ConsumeField(b)
		if n < 0 {
			return false
		}
		b = b[n:]
	}
	return true
}

// Write executes one request's writes as one batch, exactly as Commit does, and answers with its
// commit_time and one result per write. An error ends the stream with its status.
func (w *WriteStream) Write(ctx context.Context, req *pb.WriteRequest) (*pb.WriteResponse, error) {
	switch tok := req.GetStreamToken(); {
	case len(tok) == 0:
		return nil, errNoToken
	case !validToken(tok):
		return nil, errBadToken
	}
	if len(req.GetWrites()) == 0 {
		return nil, errEmptyBatch
	}
	// The names of the writes must be of the stream's database, as a Commit's are of its own.
	creq := &pb.CommitRequest{Database: w.a.wdb.Name(), Writes: req.GetWrites()}
	if _, err := wire.Bind(creq, w.a.wdb); err != nil {
		return nil, err
	}
	res, err := w.s.commitWrites(ctx, w.a, creq.GetWrites(), nil)
	if err != nil {
		return nil, err
	}
	out := &pb.WriteResponse{StreamToken: w.token(), CommitTime: timestamppb.New(res.CommitTime),
		WriteResults: make([]*pb.WriteResult, len(res.Results))}
	for i, r := range res.Results {
		// A delete's result is empty: no document is left to date.
		out.WriteResults[i] = writeResultProto(r)
	}
	return out, nil
}
