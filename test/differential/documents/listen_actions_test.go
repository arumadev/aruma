package documents

import (
	"maps"
	"slices"
	"time"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// listenActions are the commits record-f4.mjs made beside its Listen streams, by the text of
// their action frame: each made again here, returning its commit time (for the frame's label).
var listenActions = map[string]func(r *listenReplay) time.Time{
	"commit: update aruma-ref-f4c-doc/d1 {n: 2} (mask n)": merge("aruma-ref-f4c-doc/d1", "n", 2),
	"commit: delete aruma-ref-f4c-doc/d1":                 remove("aruma-ref-f4c-doc/d1"),
	"commit: create aruma-ref-f4c-doc/missing {n: 1}":     merge("aruma-ref-f4c-doc/missing", "n", 1),
	"commit: delete aruma-ref-f4c-doc/missing":            remove("aruma-ref-f4c-doc/missing"),

	"commit: create q/d {rank: 5} (enters)":                              merge("aruma-ref-f4c-q/d", "rank", 5),
	"commit: update q/b {rank: 9} (stays, changes)":                      merge("aruma-ref-f4c-q/b", "rank", 9),
	"commit: update q/c {rank: 0} (leaves by field change)":              merge("aruma-ref-f4c-q/c", "rank", 0),
	"commit: delete q/d":                                                 remove("aruma-ref-f4c-q/d"),
	"commit: update q/a {rank: 1, note: x} (outside the query, changes)": merge("aruma-ref-f4c-q/a", "rank", 1, "note", "x"),
	"commit: create q/e and update q/b in one commit (one batch)": func(r *listenReplay) time.Time {
		return r.commit(mergeWrite(r, "aruma-ref-f4c-q/e", "rank", 4), mergeWrite(r, "aruma-ref-f4c-q/b", "rank", 10))
	},

	"commit: delete lim/r1 (window edge member; next is r3)":               remove("aruma-ref-f4c-lim/r1"),
	"commit: create lim/r0 {rank: 0} (enters at the front, pushes r3 out)": merge("aruma-ref-f4c-lim/r0", "rank", 0),
	"commit: update lim/r4 {note: x} (outside the window)":                 merge("aruma-ref-f4c-lim/r4", "note", "x"),
	"commit: update lim/r2 {rank: 50} (member moves behind the window)":    merge("aruma-ref-f4c-lim/r2", "rank", 50),

	"commit: update doc/d5 {n: 2} (no target is listening)": merge("aruma-ref-f4c-doc/d5", "n", 2),

	"commit: update multi/m1 {rank: 5} (in both targets)":  merge("aruma-ref-f4c-multi/m1", "rank", 5),
	"commit: update multi/m2 {rank: 6} (in target 2 only)": merge("aruma-ref-f4c-multi/m2", "rank", 6),
	"commit: delete multi/m1 (both)":                       remove("aruma-ref-f4c-multi/m1"),

	"commit while disconnected: create res/x4 {rank: 4}":                    merge("aruma-ref-f4c-res/x4", "rank", 4),
	"commit while disconnected: update res/x2 {rank: 0} (leaves the query)": merge("aruma-ref-f4c-res/x2", "rank", 0),
	"commit while disconnected: delete res/x3":                              remove("aruma-ref-f4c-res/x3"),

	// The setup wrote y1; y2 follows it (2.5 s later in production; a few ms here, after the
	// read_time of y1 + 1 ms the streams resume from). The frame's label is y1's commit time.
	"y1 was created at the first commit time, y2 about 2.5 s later": func(r *listenReplay) time.Time {
		y1 := r.updateTime("aruma-ref-f4c-rt/y1")
		time.Sleep(5 * time.Millisecond)
		r.commit(mergeWrite(r, "aruma-ref-f4c-rt/y2", "rank", 2))
		return y1
	},
}

// merge is an update of the fields given as name, value pairs, under their mask.
func merge(path string, kv ...any) func(r *listenReplay) time.Time {
	return func(r *listenReplay) time.Time { return r.commit(mergeWrite(r, path, kv...)) }
}

func mergeWrite(r *listenReplay, path string, kv ...any) *pb.Write {
	fields := map[string]*pb.Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case int:
			fields[kv[i].(string)] = &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: int64(v)}}
		case string:
			fields[kv[i].(string)] = &pb.Value{ValueType: &pb.Value_StringValue{StringValue: v}}
		}
	}
	return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: r.docName(path), Fields: fields}},
		UpdateMask: &pb.DocumentMask{FieldPaths: slices.Sorted(maps.Keys(fields))}}
}

func remove(path string) func(r *listenReplay) time.Time {
	return func(r *listenReplay) time.Time {
		return r.commit(&pb.Write{Operation: &pb.Write_Delete{Delete: r.docName(path)}})
	}
}

func (r *listenReplay) docName(path string) string { return r.name(recordedDB) + "/documents/" + path }

// updateTime is a document's update time.
func (r *listenReplay) updateTime(path string) time.Time {
	r.t.Helper()
	res, err := r.client.GetDocument(r.t.Context(), authorized(&pb.GetDocumentRequest{Name: r.docName(path)}, r.token))
	if err != nil {
		r.t.Fatalf("%s: get %s: %v", r.c.ID, path, err)
	}
	return res.Msg.GetUpdateTime().AsTime()
}
