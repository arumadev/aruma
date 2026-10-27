package documents

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// TestReplayListenInProcess replays f4-listen.json, production's raw Listen frames, against an
// Aruma in this process over gRPC: each case's setup, then its frames in order, the client's
// requests sent as recorded and the commits it made (listenActions) made again. Every response
// passes apitest.ListenChecker. Then, per stream and per target, Aruma's frames are compared in
// order with production's: the kinds, document names and fields, causes and counts; times and
// tokens differ by run. Global snapshots are not compared per target (the checker holds them to
// the protocol), and production's repeats that depend on its timing are tolerated by rule
// (listenRepeats). A case that still differs is listed in listenDiffers with its parity exception.
func TestReplayListenInProcess(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(conformance.CorpusDir, "f4-listen.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Cases []listenCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &corpus); err != nil {
		t.Fatal(err)
	}
	env := apitest.New(t, apitest.Options{})
	h := docgrpc.New(docgrpc.Options{Service: env.Service, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	ts := httptest.NewUnstartedServer(h)
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	ts.Config.Protocols = &p
	ts.Start()
	t.Cleanup(ts.Close)
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	client := firestorepbconnect.NewFirestoreClient(&http.Client{Transport: &http.Transport{Protocols: &cp}}, ts.URL, connect.WithGRPC())

	seen := map[string]bool{}
	for _, c := range corpus.Cases {
		r := &listenReplay{t: t, c: c, client: client, token: env.JWT(t), ours: map[string][]byte{}, times: map[string]time.Time{},
			streams: map[int]*replayStream{}}
		diffs := r.run()
		for _, k := range slices.Sorted(maps.Keys(diffs)) {
			id := c.ID + " " + k
			if _, ok := listenDiffers[id]; ok {
				seen[id] = true
				continue
			}
			t.Errorf("%s differs from production:\n  %s", id, strings.Join(diffs[k], "\n  "))
		}
		for id := range listenDiffers {
			if strings.HasPrefix(id, c.ID+" ") && !seen[id] {
				if k := strings.TrimPrefix(id, c.ID+" "); len(diffs[k]) == 0 {
					t.Errorf("%s matches production, but is listed as differing (%s)", id, listenDiffers[id])
				}
				seen[id] = true
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(listenDiffers)) {
		if !seen[id] {
			t.Errorf("%s is listed as differing but was not replayed", id)
		}
	}
	t.Logf("replayed %d Listen cases", len(corpus.Cases))
}

// listenDiffers are the streams and targets ("case stream/target") whose frames Aruma does not
// reproduce, each with its parity exception.
var listenDiffers = map[string]string{
	// Resumed from T0 after an insert, a leave by a change and a delete: production relies on the
	// filter's count for the delete too; Aruma sends the delete it holds in the changelog.
	"listen.resume-token 10/1": "listen-resume-explicit-deletes",
	"listen.resume-token 11/1": "listen-resume-explicit-deletes",
}

type listenCase struct {
	ID     string                       `json:"id"`
	Setup  []map[string]json.RawMessage `json:"setup"`
	Frames []listenFrame                `json:"frames"`
	Labels struct {
		Bytes      map[string]struct{ Hex string } `json:"bytes"`
		Timestamps map[string]struct {
			Offset int64 `json:"offsetMicrosFromT1"`
		} `json:"timestamps"`
	} `json:"labels"`
}

type listenFrame struct {
	Dir        string          `json:"dir"`
	Stream     int             `json:"stream"`
	Msg        json.RawMessage `json:"msg"`
	Action     string          `json:"action"`
	CommitTime string          `json:"commitTime"`
	Status     *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type replayStream struct {
	l     *apitest.ListenClient
	ended bool
	prod  []*pb.ListenResponse
	end   *listenFrame // production's end
}

// listenReplay replays one case.
type listenReplay struct {
	t      *testing.T
	c      listenCase
	client firestorepbconnect.FirestoreClient
	token  string
	// ours maps a production token's hex to Aruma's token of the same place; times maps a
	// timestamp label to Aruma's time.
	ours    map[string][]byte
	times   map[string]time.Time
	streams map[int]*replayStream
}

var (
	bytesLabel = regexp.MustCompile(`<bytes (b\d+) len=\d+>`)
	tsLabel    = regexp.MustCompile(`<ts (t\d+)>`)
)

const recordedDB = "projects/{project}/databases/(default)"

func (r *listenReplay) run() map[string][]string {
	t := r.t
	t.Helper()
	for _, w := range r.c.Setup {
		var u struct {
			Name   string          `json:"name"`
			Fields json.RawMessage `json:"fields"`
		}
		if err := json.Unmarshal(w["update"], &u); err != nil {
			t.Fatal(err)
		}
		var doc pb.Document
		if err := protojson.Unmarshal([]byte(`{"fields":`+cmpOr(string(u.Fields), "{}")+`}`), &doc); err != nil {
			t.Fatal(err)
		}
		r.commit(&pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: r.name(u.Name), Fields: doc.Fields}}})
	}
	frames := r.c.Frames
	for i, f := range frames {
		switch f.Dir {
		case "recv":
			s := r.stream(f.Stream, false)
			s.prod = append(s.prod, r.prodResponse(f.Msg))
		case "end":
			r.stream(f.Stream, false).end = &frames[i]
			if f.Status.Code == 0 {
				r.close(f.Stream)
			}
		case "action":
			do, ok := listenActions[f.Action]
			if !ok {
				if strings.HasPrefix(f.Action, "commit") {
					t.Fatalf("%s: no replay of the action %q", r.c.ID, f.Action)
				}
				continue
			}
			at := do(r)
			if m := tsLabel.FindStringSubmatch(f.CommitTime); m != nil {
				r.times[m[1]] = at
			}
			r.settle(f.Stream, frames[i+1:])
		case "send":
			s := r.stream(f.Stream, true)
			if s.ended {
				continue
			}
			var req pb.ListenRequest
			if err := protojson.Unmarshal(unwrapValues([]byte(r.ourText(string(f.Msg)))), &req); err != nil {
				t.Fatalf("%s: request %s: %v", r.c.ID, f.Msg, err)
			}
			s.l.Send(&req)
			r.settle(f.Stream, frames[i+1:])
		}
	}
	for n := range r.streams {
		r.close(n)
	}
	return r.compare()
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// name is a recorded name in this server's project.
func (r *listenReplay) name(n string) string {
	return strings.ReplaceAll(n, "{project}", apitest.Project)
}

func (r *listenReplay) commit(writes ...*pb.Write) time.Time {
	r.t.Helper()
	res, err := r.client.Commit(r.t.Context(), authorized(&pb.CommitRequest{Database: r.name(recordedDB), Writes: writes}, r.token))
	if err != nil {
		r.t.Fatalf("%s: commit: %v", r.c.ID, err)
	}
	return res.Msg.GetCommitTime().AsTime()
}

func authorized[T any](m *T, token string) *connect.Request[T] {
	req := connect.NewRequest(m)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}

func (r *listenReplay) stream(n int, open bool) *replayStream {
	s := r.streams[n]
	if s == nil {
		s = &replayStream{}
		r.streams[n] = s
	}
	if open && s.l == nil && !s.ended {
		db := r.name(recordedDB)
		s.l = apitest.OpenListen(r.t, r.client, r.token, "Google-Cloud-Resource-Prefix", db, "X-Goog-Request-Params", "database="+url.QueryEscape(db))
	}
	return s
}

// close ends a stream the recorder closed: the client's side ends, and the stream ends OK.
func (r *listenReplay) close(n int) {
	s := r.streams[n]
	if s == nil || s.l == nil || s.ended {
		return
	}
	s.l.CloseSend()
	if err := s.l.Ended(); err != nil {
		r.t.Errorf("%s stream %d: ended with %v, want OK", r.c.ID, n, err)
	}
	s.ended = true
}

// settle reads the stream's responses until it has answered as far as production had before
// the recorder's next step: its REMOVE, its end, or a global snapshot; when production answered
// nothing, until the stream is quiet.
func (r *listenReplay) settle(n int, next []listenFrame) {
	s := r.streams[n]
	if s == nil || s.l == nil || s.ended {
		return
	}
	var removes []int32
	ends, global := false, false
	for _, f := range next {
		if f.Dir == "send" || f.Dir == "action" {
			break
		}
		if f.Stream != n {
			continue
		}
		switch {
		case f.Dir == "end":
			ends = f.Status.Code != 0
		case f.Dir == "recv":
			res := r.prodResponse(f.Msg)
			tc := res.GetTargetChange()
			if tc.GetTargetChangeType() == pb.TargetChange_REMOVE {
				removes = append(removes, tc.GetTargetIds()...)
			}
			global = global || apitest.IsGlobal(res)
		}
	}
	switch {
	case len(removes) > 0:
		for len(removes) > 0 {
			res := s.l.Next(apitest.ListenWait)
			if res == nil {
				r.t.Errorf("%s stream %d: no REMOVE for %v", r.c.ID, n, removes)
				return
			}
			if tc := res.GetTargetChange(); tc.GetTargetChangeType() == pb.TargetChange_REMOVE {
				removes = slices.DeleteFunc(removes, func(id int32) bool { return slices.Contains(tc.GetTargetIds(), id) })
			}
		}
	case ends:
		s.ended = true
		s.l.Ended()
	case global:
		s.l.UntilGlobal()
	default:
		for s.l.Next(150*time.Millisecond) != nil {
		}
	}
}

// ourText is a recorded request in this server's terms: its project, Aruma's tokens for
// production's, and times on this run's clock.
func (r *listenReplay) ourText(s string) string {
	s = r.name(s)
	r.mapTokens()
	s = bytesLabel.ReplaceAllStringFunc(s, func(m string) string {
		h := r.c.Labels.Bytes[bytesLabel.FindStringSubmatch(m)[1]].Hex
		b, ok := r.ours[h]
		if !ok {
			b, _ = hex.DecodeString(h) // never received here: production's own bytes
		}
		return base64.StdEncoding.EncodeToString(b)
	})
	return tsLabel.ReplaceAllStringFunc(s, func(m string) string {
		return r.ourTime(tsLabel.FindStringSubmatch(m)[1]).UTC().Format(time.RFC3339Nano)
	})
}

// unwrapValues writes the wrapper messages of a recorded request (limit, expectedCount), which
// proto-loader gives as {"value": n}, in protojson's form: n.
func unwrapValues(b []byte) []byte {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return b
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, y := range x {
				if m, ok := y.(map[string]any); ok && len(m) == 1 && (k == "limit" || k == "expectedCount") {
					if n, ok := m["value"]; ok {
						x[k] = n
						continue
					}
				}
				x[k] = walk(y)
			}
		case []any:
			for i, y := range x {
				x[i] = walk(y)
			}
		}
		return v
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return b
	}
	return out
}

// ourTime is a timestamp label on this run's clock: the time it was mapped to, else a mapped
// label's time moved by the recorded offset between the two.
func (r *listenReplay) ourTime(label string) time.Time {
	if at, ok := r.times[label]; ok {
		return at
	}
	for k, at := range r.times {
		d := r.c.Labels.Timestamps[label].Offset - r.c.Labels.Timestamps[k].Offset
		return at.Add(time.Duration(d) * time.Microsecond)
	}
	r.t.Fatalf("%s: no time for %s", r.c.ID, label)
	return time.Time{}
}

// prodResponse is a recorded response, its labels replaced by production's bytes and by times
// on a fixed clock: they are not compared.
func (r *listenReplay) prodResponse(msg json.RawMessage) *pb.ListenResponse {
	s := r.name(string(msg))
	s = bytesLabel.ReplaceAllStringFunc(s, func(m string) string {
		b, _ := hex.DecodeString(r.c.Labels.Bytes[bytesLabel.FindStringSubmatch(m)[1]].Hex)
		return base64.StdEncoding.EncodeToString(b)
	})
	s = tsLabel.ReplaceAllStringFunc(s, func(m string) string {
		off := r.c.Labels.Timestamps[tsLabel.FindStringSubmatch(m)[1]].Offset
		return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(off) * time.Microsecond).Format(time.RFC3339Nano)
	})
	var res pb.ListenResponse
	if err := protojson.Unmarshal([]byte(s), &res); err != nil {
		r.t.Fatalf("%s: response %s: %v", r.c.ID, msg, err)
	}
	return &res
}

// targetEvent is a frame as it concerns one target.
type targetEvent struct {
	// seg counts the global snapshots before it: the documents of one commit are compared in
	// any order, as a commit is applied whole.
	seg   int
	text  string
	kind  string // ADD, REMOVE, CURRENT, RESET, change, gone (delete or remove), filter
	name  string // a document's
	token []byte // a CURRENT's
}

// perTarget splits a stream's frames by target, in order; global snapshots and per-target
// NO_CHANGE (production's heartbeats) are left out.
func perTarget(rs []*pb.ListenResponse) map[int32][]targetEvent {
	out := map[int32][]targetEvent{}
	seg := 0
	add := func(id int32, e targetEvent) {
		e.seg = seg
		out[id] = append(out[id], e)
	}
	for _, res := range rs {
		switch x := res.GetResponseType().(type) {
		case *pb.ListenResponse_TargetChange:
			tc := x.TargetChange
			kind := tc.GetTargetChangeType().String()
			if kind == "NO_CHANGE" {
				seg++
				continue
			}
			text := kind
			if c := tc.GetCause(); c != nil {
				text += fmt.Sprintf(" %d %q", c.GetCode(), c.GetMessage())
			}
			for _, id := range tc.GetTargetIds() {
				add(id, targetEvent{text: text, kind: kind, token: tc.GetResumeToken()})
			}
		case *pb.ListenResponse_DocumentChange:
			d := x.DocumentChange.GetDocument()
			fields, _ := protojson.Marshal(&pb.Document{Fields: d.GetFields()})
			var v any
			_ = json.Unmarshal(fields, &v)
			canon, _ := json.Marshal(v)
			for _, id := range x.DocumentChange.GetTargetIds() {
				add(id, targetEvent{text: "change " + d.GetName() + " " + string(canon), kind: "change", name: d.GetName()})
			}
			for _, id := range x.DocumentChange.GetRemovedTargetIds() {
				add(id, targetEvent{text: "leave " + d.GetName() + " " + string(canon), kind: "gone", name: d.GetName()})
			}
		case *pb.ListenResponse_DocumentDelete:
			for _, id := range x.DocumentDelete.GetRemovedTargetIds() {
				add(id, targetEvent{text: "delete " + x.DocumentDelete.GetDocument(), kind: "gone", name: x.DocumentDelete.GetDocument()})
			}
		case *pb.ListenResponse_DocumentRemove:
			for _, id := range x.DocumentRemove.GetRemovedTargetIds() {
				add(id, targetEvent{text: "remove " + x.DocumentRemove.GetDocument(), kind: "gone", name: x.DocumentRemove.GetDocument()})
			}
		case *pb.ListenResponse_Filter:
			add(x.Filter.GetTargetId(), targetEvent{text: fmt.Sprintf("filter %d", x.Filter.GetCount()), kind: "filter"})
		}
	}
	return out
}

// listenRepeats drops from production's frames for one target what its timing alone made it
// send, by three rules:
//
//   - a filter after the target is CURRENT (production's unasked count, with the heartbeat
//     global snapshots; parity exception listen-heartbeats);
//   - RESET, documents, CURRENT that send again exactly what the target holds (a limit or
//     offset target's re-query: listen.limit-target);
//   - a document_delete, after CURRENT, of a document the target does not hold (the missing
//     document repeated with the heartbeats: refused-targets stream 30).
func listenRepeats(es []targetEvent) []targetEvent {
	var out []targetEvent
	held := map[string]string{}
	current := false
	for i := 0; i < len(es); i++ {
		e := es[i]
		switch {
		case e.kind == "filter" && current:
			continue
		case e.kind == "RESET" && current:
			j := i + 1
			again := map[string]string{}
			for j < len(es) && es[j].kind == "change" {
				again[es[j].name] = es[j].text
				j++
			}
			if j < len(es) && es[j].kind == "CURRENT" && maps.Equal(again, held) {
				i = j
				continue
			}
		case e.kind == "gone" && current && strings.HasPrefix(e.text, "delete ") && held[e.name] == "":
			continue
		}
		switch e.kind {
		case "change":
			held[e.name] = e.text
		case "gone":
			delete(held, e.name)
		case "RESET":
			clear(held)
			current = false
		case "CURRENT":
			current = true
		}
		out = append(out, e)
	}
	return out
}

// mapTokens maps the token of each CURRENT production sent so far to Aruma's of the same place,
// for the requests that resume with them.
func (r *listenReplay) mapTokens() {
	for _, s := range r.streams {
		if s.l == nil {
			continue
		}
		prod, ours := perTarget(s.prod), perTarget(s.l.Frames)
		for id, es := range prod {
			var pc, oc [][]byte
			for _, e := range listenRepeats(es) {
				if e.kind == "CURRENT" {
					pc = append(pc, e.token)
				}
			}
			for _, e := range ours[id] {
				if e.kind == "CURRENT" {
					oc = append(oc, e.token)
				}
			}
			for i := range min(len(pc), len(oc)) {
				r.ours[hex.EncodeToString(pc[i])] = oc[i]
			}
		}
	}
}

// compare compares every stream's targets; it returns the differences by "stream/target".
func (r *listenReplay) compare() map[string][]string {
	out := map[string][]string{}
	for _, n := range slices.Sorted(maps.Keys(r.streams)) {
		s := r.streams[n]
		if s.l == nil {
			continue
		}
		prod, ours := perTarget(s.prod), perTarget(s.l.Frames)
		for _, id := range slices.Sorted(maps.Keys(mapsUnion(prod, ours))) {
			if d := eventDiff(listenRepeats(prod[id]), ours[id]); d != nil {
				out[fmt.Sprintf("%d/%d", n, id)] = d
			}
		}
		if e := s.end; e != nil && e.Status.Code != 0 {
			ce, _ := errors.AsType[*connect.Error](s.l.End)
			switch {
			case ce == nil:
				out[fmt.Sprintf("%d/end", n)] = []string{fmt.Sprintf("ended %v, want %d %q", s.l.End, e.Status.Code, e.Status.Message)}
			case int(ce.Code()) != e.Status.Code || ce.Message() != e.Status.Message:
				out[fmt.Sprintf("%d/end", n)] = []string{fmt.Sprintf("ended %d %q, want %d %q", ce.Code(), ce.Message(), e.Status.Code, e.Status.Message)}
			}
		}
	}
	return out
}

func mapsUnion[V any](a, b map[int32]V) map[int32]bool {
	out := map[int32]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// eventDiff compares one target's events. Aruma counts every resumed query unless the client's
// own count proves it right, where production's count is sometimes absent (listen.resume-token
// streams 3, 9): Aruma's filters before CURRENT may be extra when production sent none.
func eventDiff(prod, ours []targetEvent) []string {
	text := func(es []targetEvent) []string {
		out := make([]string, len(es))
		for i, e := range es {
			out[i] = e.text
		}
		// The documents of one commit, in order of name.
		doc := func(e targetEvent) bool { return e.kind == "change" || e.kind == "gone" }
		for i := 0; i < len(es); {
			j := i
			for j < len(es) && doc(es[j]) && es[j].seg == es[i].seg {
				j++
			}
			if j > i {
				slices.SortFunc(out[i:j], func(a, b string) int {
					return strings.Compare(a[strings.IndexByte(a, ' '):], b[strings.IndexByte(b, ' '):])
				})
				i = j
				continue
			}
			i++
		}
		return out
	}
	want, got := text(prod), text(ours)
	if slices.Equal(got, want) {
		return nil
	}
	if !slices.ContainsFunc(prod, func(e targetEvent) bool { return e.kind == "filter" }) {
		var kept []targetEvent
		current := false
		for _, e := range ours {
			switch {
			case e.kind == "filter" && !current:
				continue
			case e.kind == "CURRENT":
				current = true
			case e.kind == "RESET":
				current = false
			}
			kept = append(kept, e)
		}
		if slices.Equal(text(kept), want) {
			return nil
		}
	}
	return []string{"got  " + strings.Join(got, " | "), "want " + strings.Join(want, " | ")}
}
