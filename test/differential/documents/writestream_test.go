package documents

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
	conformance "github.com/arumadev/aruma/test/conformance/documents"
)

// writeDiffers are the Write stream cases Aruma does not answer as production, each with its
// parity exception (parity-exceptions.json). A listed case that matches fails the test.
var writeDiffers = map[string]string{}

// The recording of the Write stream (record-f4.mjs) is raw ordered frames, not requests with
// answers, so it has its own replay: each case drives bidi streams frame by frame against an
// Aruma in this process, over the real gRPC route.

type wsFrame struct {
	Dir    string          `json:"dir"`
	Kind   string          `json:"kind"`
	Stream int             `json:"stream"`
	Msg    json.RawMessage `json:"msg"`
	Status *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type wsCase struct {
	ID     string            `json:"id"`
	Setup  []json.RawMessage `json:"setup"`
	Frames []wsFrame         `json:"frames"`
	Labels struct {
		Bytes map[string]struct {
			Hex string `json:"hex"`
		} `json:"bytes"`
	} `json:"labels"`
}

var (
	tsLabel         = regexp.MustCompile(`\\u003cts t\d+\\u003e`)
	bytesLabel      = regexp.MustCompile(`^<bytes (b\d+) len=\d+>$`)
	streamIDLabel   = regexp.MustCompile(`^<stream_id (s\d+) len=(\d+)>$`)
	wsStoredVersion = regexp.MustCompile(`stored version \(\d+\)`)
)

// maskedToken is a stream token with its 8-byte stream part (field 3, fixed64, which is random
// per stream) masked: the rest, the batch counter, is the structure that must match.
func maskedToken(b []byte) string {
	if n := len(b); n >= 9 && b[n-9] == 0x19 {
		b = append(slices.Clone(b[:n-8]), make([]byte, 8)...)
	}
	return "<token " + hex.EncodeToString(b) + ">"
}

// sentTime is the time of the recording's timestamp labels in requests.
var sentTime = time.Unix(1000, 0).UTC()

type wsStream struct {
	st    *connect.BidiStreamForClient[pb.WriteRequest, pb.WriteResponse]
	ended bool
	bytes map[string][]byte // the tokens received, by the recording's label
	ids   map[string]string
}

func TestReplayWriteStreamInProcess(t *testing.T) {
	path := filepath.Join(conformance.CorpusDir, "f4-write-stream.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []wsCase `json:"cases"`
	}
	if err := json.Unmarshal(bytes.ReplaceAll(raw, []byte("{project}"), []byte(apitest.Project)), &file); err != nil {
		t.Fatal(err)
	}

	env := apitest.New(t, apitest.Options{})
	h := docgrpc.New(docgrpc.Options{Service: env.Service, Metrics: docgrpc.NewMetrics(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ts := httptest.NewUnstartedServer(h)
	var sp http.Protocols
	sp.SetHTTP1(true)
	sp.SetUnencryptedHTTP2(true)
	ts.Config.Protocols = &sp
	ts.Start()
	t.Cleanup(ts.Close)
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	client := firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, ts.URL, connect.WithGRPC())
	token := env.JWT(t)
	auth := func(h http.Header) { h.Set("Authorization", "Bearer "+token) }

	for _, c := range file.Cases {
		id := "f4-write-stream.json " + c.ID
		if len(c.Setup) > 0 {
			var ws []*pb.Write
			for _, s := range c.Setup {
				w := &pb.Write{}
				if err := protojson.Unmarshal(s, w); err != nil {
					t.Fatalf("%s: setup: %v", id, err)
				}
				ws = append(ws, w)
			}
			r := connect.NewRequest(&pb.CommitRequest{Database: "projects/" + apitest.Project + "/databases/(default)", Writes: ws})
			auth(r.Header())
			if _, err := client.Commit(t.Context(), r); err != nil {
				t.Fatalf("%s: setup: %v", id, err)
			}
		}

		streams := map[int]*wsStream{}
		global := &wsStream{bytes: map[string][]byte{}, ids: map[string]string{}}
		open := func(n int) *wsStream {
			if s, ok := streams[n]; ok {
				return s
			}
			st := client.Write(t.Context())
			auth(st.RequestHeader())
			s := &wsStream{st: st, bytes: map[string][]byte{}, ids: map[string]string{}}
			streams[n] = s
			t.Cleanup(func() { _ = st.CloseRequest(); _ = st.CloseResponse() })
			return s
		}

		// live answers by frame index, compared once the case's timestamps can be labelled.
		type answer struct {
			frame int
			msg   map[string]any
			err   error
			eof   bool
		}
		var answers []answer
		for i, f := range c.Frames {
			switch f.Dir {
			case "send":
				s := open(f.Stream)
				if s.ended {
					continue
				}
				req, err := wsRequest(f.Msg, s, global, c.Labels.Bytes)
				if err != nil {
					t.Fatalf("%s: frame %d: %v", id, i, err)
				}
				// A send the server's close beat is not an error: the answers say what happened.
				_ = s.st.Send(req)
			case "recv":
				s := open(f.Stream)
				res, err := s.st.Receive()
				a := answer{frame: i, err: err}
				if err == nil {
					a.msg = wsJSON(t, res)
					var want struct{ StreamToken, StreamId string }
					_ = json.Unmarshal(f.Msg, &want)
					if m := bytesLabel.FindStringSubmatch(want.StreamToken); m != nil {
						s.bytes[m[1]], global.bytes[m[1]] = res.GetStreamToken(), res.GetStreamToken()
					}
					if m := streamIDLabel.FindStringSubmatch(want.StreamId); m != nil {
						s.ids[m[1]], global.ids[m[1]] = res.GetStreamId(), res.GetStreamId()
					}
				}
				answers = append(answers, a)
			case "end":
				s := open(f.Stream)
				a := answer{frame: i}
				if f.Status.Code == 0 {
					_ = s.st.CloseRequest()
				}
				_, a.err = s.st.Receive()
				a.eof = errors.Is(a.err, io.EOF)
				if a.eof {
					a.err = nil
				}
				s.ended = true
				answers = append(answers, a)
			}
		}

		// Timestamps are labelled as the recording labels them: by their order within the case,
		// equal values sharing a label.
		var times []time.Time
		for _, a := range answers {
			if a.msg != nil {
				walkTimes(a.msg, func(tm time.Time) {
					if !slices.ContainsFunc(times, tm.Equal) {
						times = append(times, tm)
					}
				})
			}
		}
		if slices.ContainsFunc(c.Frames, func(f wsFrame) bool { return f.Dir == "send" && strings.Contains(string(f.Msg), "<ts t") }) {
			times = append(times, sentTime)
		}
		slices.SortFunc(times, time.Time.Compare)

		var diffs []string
		for _, a := range answers {
			f := c.Frames[a.frame]
			switch f.Dir {
			case "recv":
				if a.err != nil {
					diffs = append(diffs, fmt.Sprintf("frame %d: want a response, got %v", a.frame, a.err))
					continue
				}
				var want any
				if err := json.Unmarshal(f.Msg, &want); err != nil {
					t.Fatal(err)
				}
				got := labelLive(a.msg, times)
				for _, d := range Diff(wsWant(want, c.Labels.Bytes), got) {
					diffs = append(diffs, fmt.Sprintf("frame %d: %s", a.frame, d))
				}
			case "end":
				code, msg := 0, ""
				if ce, ok := errors.AsType[*connect.Error](a.err); ok {
					code, msg = int(ce.Code()), ce.Message()
				} else if a.err != nil {
					msg = a.err.Error()
					code = -1
				}
				msg = wsStoredVersion.ReplaceAllString(msg, "stored version (<micros>)")
				wantMsg := wsStoredVersion.ReplaceAllString(f.Status.Message, "stored version (<micros>)")
				if code != f.Status.Code || msg != wantMsg {
					diffs = append(diffs, fmt.Sprintf("frame %d: ends with %d %q, want %d %q", a.frame, code, msg, f.Status.Code, wantMsg))
				}
			}
		}

		// The batches pipelined behind a failed one are not applied.
		if strings.HasPrefix(c.ID, "write.precondition-") || c.ID == "write.invalid-argument" {
			r := connect.NewRequest(&pb.GetDocumentRequest{Name: "projects/" + apitest.Project + "/databases/(default)/documents/aruma-ref-f4c-w/ok2"})
			auth(r.Header())
			if _, err := client.GetDocument(t.Context(), r); connect.CodeOf(err) != connect.CodeNotFound {
				diffs = append(diffs, fmt.Sprintf("the batch pipelined behind the failure was applied: %v", err))
			}
		}

		if exception, ok := writeDiffers[id]; ok {
			if len(diffs) == 0 {
				t.Errorf("%s matches production, but is listed as differing (%s)", id, exception)
			}
			continue
		}
		if len(diffs) > 0 {
			t.Errorf("%s differs from production:\n  %s", id, strings.Join(diffs, "\n  "))
		}
	}
	t.Logf("replayed %d Write stream cases", len(file.Cases))
}

// wsRequest is a recorded request with its labels given live values: a stream_token or stream_id
// received on the stream (or on an earlier one) is sent back as received, and one never received
// as recorded (the garbled token's bytes) or as a string of the recorded length.
func wsRequest(msg json.RawMessage, s, global *wsStream, labels map[string]struct {
	Hex string `json:"hex"`
}) (*pb.WriteRequest, error) {
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		return nil, err
	}
	if v, ok := m["streamToken"].(string); ok {
		l := bytesLabel.FindStringSubmatch(v)
		if l == nil {
			return nil, fmt.Errorf("stream_token %q is not a label", v)
		}
		b, ok := s.bytes[l[1]]
		if !ok {
			b, ok = global.bytes[l[1]]
		}
		if !ok {
			var err error
			if b, err = hex.DecodeString(labels[l[1]].Hex); err != nil {
				return nil, err
			}
		}
		m["streamToken"] = base64.StdEncoding.EncodeToString(b)
	}
	if v, ok := m["streamId"].(string); ok {
		l := streamIDLabel.FindStringSubmatch(v)
		if l == nil {
			return nil, fmt.Errorf("stream_id %q is not a label", v)
		}
		id, ok := s.ids[l[1]]
		if !ok {
			id, ok = global.ids[l[1]]
		}
		if !ok {
			var n int
			_, _ = fmt.Sscan(l[2], &n)
			id = strings.Repeat("x", n)
		}
		m["streamId"] = id
	}
	// A timestamp label in a request is a time the recorder chose: the precondition's base
	// version, 1000 s after the epoch (the 1000000000 µs production's refusal names).
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	b = tsLabel.ReplaceAll(b, []byte(sentTime.Format(time.RFC3339)))
	req := &pb.WriteRequest{}
	return req, protojson.Unmarshal(b, req)
}

// wsJSON is a live response as the recording's JSON shows it (protojson's names and enums).
func wsJSON(t *testing.T, res *pb.WriteResponse) map[string]any {
	t.Helper()
	b, err := protojson.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	walkNull(m)
	// The token's bytes are masked, the id kept by its length: both are random per stream.
	if s, ok := m["streamToken"].(string); ok {
		tok, _ := base64.StdEncoding.DecodeString(s)
		m["streamToken"] = maskedToken(tok)
	}
	if s, ok := m["streamId"].(string); ok {
		m["streamId"] = fmt.Sprintf("<stream_id len=%d>", len(s))
	}
	return m
}

// walkNull shows a null value as the recording does, by the enum's name.
func walkNull(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if y == nil && k == "nullValue" {
				x[k] = "NULL_VALUE"
			}
			walkNull(y)
		}
	case []any:
		for _, y := range x {
			walkNull(y)
		}
	}
}

// wsWant is a recording's response with its labels masked as wsJSON masks the live one.
func wsWant(v any, labels map[string]struct {
	Hex string `json:"hex"`
}) any {
	m := v.(map[string]any)
	if s, ok := m["streamToken"].(string); ok {
		if l := bytesLabel.FindStringSubmatch(s); l != nil {
			b, _ := hex.DecodeString(labels[l[1]].Hex)
			m["streamToken"] = maskedToken(b)
		}
	}
	if s, ok := m["streamId"].(string); ok {
		if l := streamIDLabel.FindStringSubmatch(s); l != nil {
			m["streamId"] = "<stream_id len=" + l[2] + ">"
		}
	}
	// Timestamp labels are compared by their number: <ts tN>.
	return m
}

// walkTimes calls f with every timestamp of a live response: the commit and update times and the
// timestamp values of transform results.
func walkTimes(v any, f func(time.Time)) {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if s, ok := y.(string); ok && (k == "commitTime" || k == "updateTime" || k == "timestampValue") {
				if tm, err := time.Parse(time.RFC3339Nano, s); err == nil {
					f(tm)
				}
				continue
			}
			walkTimes(y, f)
		}
	case []any:
		for _, y := range x {
			walkTimes(y, f)
		}
	}
}

// labelLive replaces the timestamps of a live response by their labels, <ts tN> by their rank.
func labelLive(v any, times []time.Time) any {
	switch x := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(x))
		for k, y := range x {
			if s, ok := y.(string); ok && (k == "commitTime" || k == "updateTime" || k == "timestampValue") {
				if tm, err := time.Parse(time.RFC3339Nano, s); err == nil {
					o[k] = fmt.Sprintf("<ts t%d>", slices.IndexFunc(times, tm.Equal)+1)
					continue
				}
			}
			o[k] = labelLive(y, times)
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, y := range x {
			o[i] = labelLive(y, times)
		}
		return o
	}
	return v
}
