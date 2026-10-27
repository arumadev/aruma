package api

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// The Listen stream's wire forms: its resume tokens, its messages and its errors, with
// production's texts (f4-listen.json, listen.refused-targets unless named).

// Errors that end the stream, with its status.
var (
	errListenNoChange = wire.InvalidArgument("Unknown target_change: 0") // stream 26
	errListenDatabase = wire.InvalidArgument("Mismatch between database name in the request and http header " +
		"('google-cloud-resource-prefix' or 'x-goog-request-params') or query param 'database'.") // stream 27
	errListenManualID = wire.InvalidArgument("Manually assigned target IDs are not allowed after a server assigned ID.") // listen.remove-target
	// errListenClosed ends the streams of a hub that closed: the server is shutting down.
	errListenClosed = wire.NewError(wire.CodeUnavailable, "The server is shutting down. Retry the request.")
	// errListenUnserved is Listen on a service built without a hub.
	errListenUnserved = wire.Unimplemented("Listen")
)

func errListenTargetID(id int32) error {
	return wire.InvalidArgument(fmt.Sprintf("Invalid target ID: %d", id))
} // stream 23

// errListenNoTarget is the remove of a target the stream does not have (listen.remove-target).
func errListenNoTarget(id int32) error {
	return wire.NewError(wire.CodeNotFound, fmt.Sprintf("Target id not found: %d", id))
}

// errListenDuplicate is an add of a target id the stream has: not recorded (the recorder's stream
// ended on another error first), so the text is ours.
func errListenDuplicate(id int32) error {
	return wire.InvalidArgument(fmt.Sprintf("Target id %d is already in use on this stream.", id))
}

// Errors of one target: REMOVE with the error as its cause, and the stream stays open.
var (
	errTargetDatabase    = wire.InvalidArgument("Cannot add a target for a different database.")
	errTargetType        = wire.InvalidArgument("Unsupported target type: 0")
	errTargetNoDocuments = wire.InvalidArgument("No documents provided.")
	errTargetAllKinds    = wire.InvalidArgument("All-collection queries are not supported by real-time queries.")
	errTargetGroupUnder  = wire.InvalidArgument("'all_descendants' in CollectionSelector with document parent is not supported by real-time queries.")
	errTargetSelect      = wire.InvalidArgument("'select' clauses are not supported for real-time queries.")
	errTargetIdentifier  = wire.InvalidArgument("Invalid identifier") // stream 28: RunQuery's "Invalid empty property path string."
	errTargetReadFuture  = wire.InvalidArgument(core.ErrReadTimeFuture.Message)
	// errBadResumeToken: a token that does not parse (listen.resume-bad-token).
	errBadResumeToken = wire.InvalidArgument("bad resume token")
)

// msgEmptyPath is query.Parse's text for an empty field path, which Listen words as
// errTargetIdentifier.
const msgEmptyPath = "Invalid empty property path string."

func errTargetPathPiece(id string) error {
	return wire.InvalidArgument("Unexpected document path piece: /" + id) // stream 19
}

// A resume token is production's form (f4-listen.json labels.bytes): a message whose field 1 is a
// message whose field 1 is the read time in microseconds since the epoch, 0a 09 08 <varint>. It
// carries a time only, no target, project or query, so a token of any target resumes any other
// (listen.resume-bad-token streams 3, 5, 6). A RESET carries the 2-byte 0a 00.
var resetToken = []byte{0x0a, 0x00}

func resumeToken(t time.Time) []byte {
	inner := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), uint64(t.UnixMicro()))
	return protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), inner)
}

// maxTokenMicros is the last microsecond a Timestamp holds (9999-12-31T23:59:59.999999Z).
const maxTokenMicros = 253402300799999999

// parseResumeToken reads a token's time; ok is false for bytes that are not a token. A RESET's
// token, which has no time, is the zero time: a fresh add (a guess: production's answer to it is
// not recorded).
func parseResumeToken(b []byte) (t time.Time, ok bool) {
	num, typ, n := protowire.ConsumeTag(b)
	if n < 0 || num != 1 || typ != protowire.BytesType {
		return time.Time{}, false
	}
	inner, m := protowire.ConsumeBytes(b[n:])
	if m < 0 || n+m != len(b) {
		return time.Time{}, false
	}
	if len(inner) == 0 {
		return time.Time{}, true
	}
	num, typ, n = protowire.ConsumeTag(inner)
	if n < 0 || num != 1 || typ != protowire.VarintType {
		return time.Time{}, false
	}
	v, m := protowire.ConsumeVarint(inner[n:])
	if m < 0 || n+m != len(inner) || v == 0 || v > maxTokenMicros {
		return time.Time{}, false
	}
	return time.UnixMicro(int64(v)).UTC(), true
}

// headerDatabase is the database the stream's headers name: google-cloud-resource-prefix, else
// x-goog-request-params' database; "" when neither does.
func (c Call) headerDatabase() string {
	if c.ResourcePrefix != "" {
		return c.ResourcePrefix
	}
	for p := range strings.SplitSeq(c.RequestParams, "&") {
		if v, ok := strings.CutPrefix(p, "database="); ok {
			if d, err := url.QueryUnescape(v); err == nil {
				return d
			}
		}
	}
	return ""
}

func targetChange(kind pb.TargetChange_TargetChangeType, ids ...int32) *pb.ListenResponse {
	return &pb.ListenResponse{ResponseType: &pb.ListenResponse_TargetChange{TargetChange: &pb.TargetChange{
		TargetChangeType: kind, TargetIds: ids}}}
}

// snapshotChange is a CURRENT or a global NO_CHANGE at rt: production gives both the read time
// and its token (listen.doc-target).
func snapshotChange(kind pb.TargetChange_TargetChangeType, rt time.Time, ids ...int32) *pb.ListenResponse {
	r := targetChange(kind, ids...)
	r.GetTargetChange().ReadTime = timestamppb.New(rt)
	r.GetTargetChange().ResumeToken = resumeToken(rt)
	return r
}

// removeTarget is REMOVE for one target; with err, its cause: the code and message alone, as
// recorded, no details.
func removeTarget(id int32, err *wire.Error) *pb.ListenResponse {
	r := targetChange(pb.TargetChange_REMOVE, id)
	if err != nil {
		r.GetTargetChange().Cause = &status.Status{Code: int32(err.Code), Message: err.Message}
	}
	return r
}
