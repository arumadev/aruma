package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The Write stream's logic (ADR-026 §6): a handshake, then each request is one atomic batch,
// executed as Commit executes it. The transport (package grpc) owns the stream's lifetime, its
// deadlines and the order of its requests.

// errHandshakeWrites is a first request that carries writes, which the handshake must not: they
// would be dropped silently otherwise. Production's answer is not recorded.
var errHandshakeWrites = wire.InvalidArgument("The first request of a write stream must not contain writes.")

// WriteStream is one open Write stream, admitted at its handshake. It is not safe for concurrent
// use: the transport calls it one request at a time, in order.
type WriteStream struct {
	s    *Service
	a    admitted
	id   string
	last uint64 // the sequence of the last token sent
}

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
	var id [16]byte
	_, _ = rand.Read(id[:])
	w := &WriteStream{s: s, a: a, id: base64.RawURLEncoding.EncodeToString(id[:])}
	return w, &pb.WriteResponse{StreamId: w.id, StreamToken: w.token()}, nil
}

// token is the next stream_token: opaque to clients, which send back the last they saw, and
// increasing within the stream. Its format is ours until production's is recorded.
func (w *WriteStream) token() []byte {
	w.last++
	return binary.BigEndian.AppendUint64(nil, w.last)
}

// Write executes one request's writes as one batch, exactly as Commit does, and answers with its
// commit_time and one result per write. An error ends the stream with its status.
func (w *WriteStream) Write(ctx context.Context, req *pb.WriteRequest) (*pb.WriteResponse, error) {
	// The names of the writes must be of the stream's database, as a Commit's are of its own.
	creq := &pb.CommitRequest{Database: w.a.wdb.Name(), Writes: req.GetWrites()}
	if _, err := wire.Bind(creq, w.a.wdb); err != nil {
		return nil, err
	}
	res, err := w.s.commitWrites(ctx, w.a, creq.GetWrites(), nil)
	if err != nil {
		return nil, err
	}
	commit := timestamppb.New(res.CommitTime)
	out := &pb.WriteResponse{StreamId: w.id, StreamToken: w.token(), CommitTime: commit,
		WriteResults: make([]*pb.WriteResult, len(res.Results))}
	for i, r := range res.Results {
		out.WriteResults[i] = writeResultProto(r)
		if out.WriteResults[i].UpdateTime == nil {
			// A delete leaves no document to date: its time is the commit's.
			out.WriteResults[i].UpdateTime = commit
		}
	}
	return out, nil
}
