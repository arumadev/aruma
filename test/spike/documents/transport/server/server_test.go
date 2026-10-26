package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// These tests measure what tells the two stacks apart without a real client: each runs once per
// stack, on an h2c listener with Aruma's server settings, through grpc-go's client.

var stacks = []string{"connect", "grpcgo"}

const database = "projects/spike-project/databases/(default)"

// start serves one stack on a fresh listener; tune may change the server before it starts.
func start(t *testing.T, stack string, later time.Duration, keepDeadlines bool, tune func(*http.Server)) (*spike, *http.Server, pb.FirestoreClient) {
	t.Helper()
	dir := t.TempDir()
	rec := &recorder{log: slog.New(slog.DiscardHandler)}
	s := newSpike(slog.New(slog.DiscardHandler), rec, later, keepDeadlines)
	cert, err := makeCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := s.server(listener{name: "h2c-" + stack, addr: ln.Addr().String(), stack: stack}, cert)
	if tune != nil {
		tune(srv)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	conn, err := grpc.NewClient("passthrough:///"+ln.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return s, srv, pb.NewFirestoreClient(conn)
}

func docTarget(id int32) *pb.ListenRequest {
	return &pb.ListenRequest{Database: database, TargetChange: &pb.ListenRequest_AddTarget{AddTarget: &pb.Target{
		TargetId:   id,
		TargetType: &pb.Target_Documents{Documents: &pb.Target_DocumentsTarget{Documents: []string{database + "/documents/spike/doc"}}},
	}}}
}

func TestCallsAndStreams(t *testing.T) {
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			_, _, c := start(t, stack, 300*time.Millisecond, false, nil)
			ctx := t.Context()

			res, err := c.Commit(ctx, &pb.CommitRequest{Database: database, Writes: []*pb.Write{
				{Operation: &pb.Write_Update{Update: &pb.Document{Name: database + "/documents/spike/a"}}},
				{Operation: &pb.Write_Verify{Verify: database + "/documents/spike/b"}},
			}})
			if err != nil || len(res.GetWriteResults()) != 2 || res.GetCommitTime() == nil {
				t.Fatalf("Commit: %v %v", res, err)
			}

			ls, err := c.Listen(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := ls.Send(docTarget(2)); err != nil {
				t.Fatal(err)
			}
			var got []string
			for len(got) < 5 {
				m, err := ls.Recv()
				if err != nil {
					t.Fatalf("Listen after %v: %v", got, err)
				}
				if tc := m.GetTargetChange(); tc != nil {
					got = append(got, tc.GetTargetChangeType().String())
				} else {
					got = append(got, "DOC "+m.GetDocumentChange().GetDocument().GetName())
				}
			}
			want := "ADD CURRENT NO_CHANGE DOC " + database + "/documents/spike/doc NO_CHANGE"
			if strings.Join(got, " ") != want {
				t.Fatalf("Listen: %q, want %q", got, want)
			}

			ws, err := c.Write(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := ws.Send(&pb.WriteRequest{Database: database}); err != nil {
				t.Fatal(err)
			}
			hs, err := ws.Recv()
			if err != nil || len(hs.GetStreamToken()) == 0 || len(hs.GetWriteResults()) != 0 || hs.GetStreamId() == "" {
				t.Fatalf("Write handshake: %v %v", hs, err)
			}
			if err := ws.Send(&pb.WriteRequest{StreamToken: hs.GetStreamToken(), Writes: []*pb.Write{{Operation: &pb.Write_Delete{Delete: database + "/documents/spike/a"}}}}); err != nil {
				t.Fatal(err)
			}
			if r, err := ws.Recv(); err != nil || len(r.GetWriteResults()) != 1 || r.GetCommitTime() == nil {
				t.Fatalf("Write: %v %v", r, err)
			}
		})
	}
}

// TestStatusDetails: the index error's link travels as a google.rpc.Help detail in
// grpc-status-details-bin, the same bytes from both stacks.
func TestStatusDetails(t *testing.T) {
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			_, _, c := start(t, stack, time.Hour, false, nil)
			_, err := c.Commit(t.Context(), &pb.CommitRequest{Database: database, Writes: []*pb.Write{
				{Operation: &pb.Write_Update{Update: &pb.Document{Name: database + indexErrorDoc}}}}})
			st := status.Convert(err)
			if st.Code() != codes.FailedPrecondition || st.Message() != indexMessage {
				t.Fatalf("status %v %q", st.Code(), st.Message())
			}
			details := st.Details()
			if len(details) != 1 {
				t.Fatalf("details %v", details)
			}
			if h, ok := details[0].(*errdetails.Help); !ok || h.GetLinks()[0].GetUrl() != indexLink {
				t.Fatalf("detail %v", details[0])
			}
		})
	}
}

// TestStreamOutlivesServerTimeouts: net/http applies the server's ReadTimeout and WriteTimeout
// to each HTTP/2 stream. With the deadlines left on, a Listen stream dies when they pass; the
// gRPC route lifts them.
func TestStreamOutlivesServerTimeouts(t *testing.T) {
	short := func(srv *http.Server) { srv.ReadTimeout, srv.WriteTimeout = time.Second, time.Second }
	for _, stack := range stacks {
		for _, keep := range []bool{false, true} {
			name := stack + "/lifted"
			if keep {
				name = stack + "/kept"
			}
			t.Run(name, func(t *testing.T) {
				_, _, c := start(t, stack, 2500*time.Millisecond, keep, short)
				ls, err := c.Listen(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := ls.Send(docTarget(2)); err != nil {
					t.Fatal(err)
				}
				var n int
				for ; n < 5; n++ {
					if _, err = ls.Recv(); err != nil {
						break
					}
				}
				t.Logf("received %d of 5 messages, the later one 2.5 s in; then %v", n, err)
				if !keep && n != 5 {
					t.Fatalf("the stream did not outlive the 1 s timeouts: %v", err)
				}
				if keep && n == 5 {
					t.Fatal("the stream outlived the 1 s timeouts though they were kept")
				}
			})
		}
	}
}

// TestShutdownDrainsStreams: Shutdown waits for open streams; the drain ends them first, with
// UNAVAILABLE.
func TestShutdownDrainsStreams(t *testing.T) {
	for _, stack := range stacks {
		for _, drain := range []bool{true, false} {
			name := stack + "/drain"
			if !drain {
				name = stack + "/shutdown-only"
			}
			t.Run(name, func(t *testing.T) {
				s, srv, c := start(t, stack, time.Hour, false, nil)
				ls, err := c.Listen(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				ls.Send(docTarget(2))
				for range 3 {
					if _, err := ls.Recv(); err != nil {
						t.Fatal(err)
					}
				}
				begin := time.Now()
				var shutErr error
				if drain {
					shutErr = s.shutdown([]*http.Server{srv}, 2*time.Second)
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					shutErr = srv.Shutdown(ctx)
					cancel()
					srv.Close() // what is left after Shutdown gives up: the stream is cut
				}
				took := time.Since(begin)
				_, recvErr := ls.Recv()
				t.Logf("shutdown took %v (%v); the client's stream ended with %v", took.Round(10*time.Millisecond), shutErr, status.Code(recvErr))
				if drain && (shutErr != nil || status.Code(recvErr) != codes.Unavailable || took > time.Second) {
					t.Fatalf("drained shutdown: %v in %v, stream %v", shutErr, took, recvErr)
				}
				if !drain && !errors.Is(shutErr, context.DeadlineExceeded) {
					t.Fatalf("Shutdown without the drain returned %v: it did not wait for the stream", shutErr)
				}
			})
		}
	}
}

// TestBackpressure: a client that reads nothing. Send must block once HTTP/2 flow control is
// exhausted, so the server holds a bounded amount per stream.
func TestBackpressure(t *testing.T) {
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			s, _, c := start(t, stack, time.Hour, false, nil)
			ls, err := c.Listen(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ls.Send(docTarget(floodTarget))
			time.Sleep(1500 * time.Millisecond)
			sent := s.fake.flooded.Load()
			t.Logf("1 MiB messages Send returned for while the client read nothing: %d", sent)
			if sent > 16 {
				t.Fatalf("%d MiB went out to a client that reads nothing", sent)
			}
		})
	}
}

// noFlush is a ResponseWriter without Flush, like httpx.StatusRecorder.
type noFlush struct{ http.ResponseWriter }

// TestNeedsFlusher: both stacks assert http.Flusher on the ResponseWriter itself (no Unwrap), so
// the gRPC route cannot pass through a wrapper that hides it: grpc-go refuses every call, connect
// every streaming one.
func TestNeedsFlusher(t *testing.T) {
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			_, srv, c := start(t, stack, time.Hour, false, nil)
			inner := srv.Handler
			srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { inner.ServeHTTP(noFlush{w}, r) })
			_, unaryErr := c.Commit(t.Context(), &pb.CommitRequest{Database: database})
			var streamErr error
			if ls, err := c.Listen(t.Context()); err != nil {
				streamErr = err
			} else {
				ls.Send(docTarget(2))
				_, streamErr = ls.Recv()
			}
			t.Logf("through a writer without Flush: unary %v; stream %v", status.Code(unaryErr), streamErr)
			if streamErr == nil || (stack == "grpcgo") != (unaryErr != nil) {
				t.Fatalf("unary %v, stream %v", unaryErr, streamErr)
			}
		})
	}
}

// TestHTTP1: a unary gRPC call over HTTP/1.1, as a proxy that speaks HTTP/1.1 to its upstream
// would send it. grpc-go's ServeHTTP refuses anything but HTTP/2; connect answers.
func TestHTTP1(t *testing.T) {
	for _, stack := range stacks {
		t.Run(stack, func(t *testing.T) {
			_, srv, _ := start(t, stack, time.Hour, false, nil)
			body, _ := proto.Marshal(&pb.CommitRequest{Database: database})
			frame := make([]byte, 5, 5+len(body))
			binary.BigEndian.PutUint32(frame[1:], uint32(len(body)))
			req, _ := http.NewRequest("POST", "http://"+srv.Addr+"/google.firestore.v1.Firestore/Commit", bytes.NewReader(append(frame, body...)))
			req.Header.Set("Content-Type", "application/grpc")
			req.Header.Set("TE", "trailers")
			client := &http.Client{Transport: &http.Transport{}} // HTTP/1.1 only over plain TCP
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
			grpcStatus := res.Trailer.Get("Grpc-Status") + res.Header.Get("Grpc-Status")
			t.Logf("%s over HTTP/1.1: HTTP %d, grpc-status %q", stack, res.StatusCode, grpcStatus)
			ok := res.StatusCode == 200 && grpcStatus == "0"
			if ok != (stack == "connect") {
				t.Fatalf("HTTP %d grpc-status %q", res.StatusCode, grpcStatus)
			}
		})
	}
}
