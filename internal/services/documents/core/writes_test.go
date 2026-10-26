package core

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

var testDB = DB{Project: 482915736204, ProjectID: "chatapp-8ae21", ID: DefaultDatabase}

func TestDocumentEncodingRoundTrip(t *testing.T) {
	fields := value.Map(
		value.Field{Name: "n", Value: value.Null()},
		value.Field{Name: "b", Value: value.Bool(true)},
		value.Field{Name: "i", Value: value.Int(math.MinInt64)},
		value.Field{Name: "z", Value: value.Double(math.Copysign(0, -1))},
		value.Field{Name: "nan", Value: value.Double(math.NaN())},
		value.Field{Name: "t", Value: value.Timestamp(-62135596800, 999999000)},
		value.Field{Name: "s", Value: value.String("é\x00")},
		value.Field{Name: "y", Value: value.Bytes([]byte{0, 0xff})},
		value.Field{Name: "r", Value: value.Reference("projects/p/databases/(default)/documents/c/d")},
		value.Field{Name: "g", Value: value.GeoPoint(-90, 180)},
		value.Field{Name: "a", Value: value.Array(value.Int(1), value.Array())},
		value.Field{Name: "v", Value: value.Vector(1, 2)},
		value.Field{Name: "m", Value: value.Map(value.Field{Name: "__int__", Value: value.Int(5)})},
		value.Field{Name: "e", Value: value.Map()},
	)
	created, updated := time.UnixMicro(1793038687926996).UTC(), time.UnixMicro(1793038697978566).UTC()
	b, err := encodeDocument(fields, created, updated)
	if err != nil {
		t.Fatal(err)
	}
	got, c, u, err := decodeDocument(b)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Identical(got, fields) || !c.Equal(created) || !u.Equal(updated) {
		t.Fatalf("round trip: %v %v %v", got, c, u)
	}
	empty, _ := encodeDocument(value.Map(), time.Time{}, time.Time{})
	if got, c, _, err := decodeDocument(empty); err != nil || got.Kind() != value.KindMap || len(got.Fields()) != 0 || !c.IsZero() {
		t.Fatalf("empty document: %v %v %v", got, c, err)
	}
}

func fieldsOf(kv ...any) value.Value {
	var fs []value.Field
	for i := 0; i < len(kv); i += 2 {
		fs = append(fs, value.Field{Name: kv[i].(string), Value: kv[i+1].(value.Value)})
	}
	return value.Map(fs...)
}

func mustPath(t *testing.T, s string) value.FieldPath {
	t.Helper()
	p, err := value.ParseFieldPath(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The preconditions and their messages, production's (f1-create.json, f1-update.json,
// f1-delete.json), checked against the documents as locked.
func TestPlanPreconditions(t *testing.T) {
	p := ParsePath("aruma-ref-f1-write/u1")
	name := testDB.DocumentName(p)
	stored := time.UnixMicro(1793038697978566)
	existing := map[string]*lockedDoc{string(storedKey(nameKey(p))): {seq: 7, fields: fieldsOf("a", value.Int(1)), createTime: stored, updateTime: stored}}
	required := time.UnixMicro(1793038687926996)
	cases := []struct {
		write  Write
		locked map[string]*lockedDoc
		code   Code
		msg    string
	}{
		{Write{Kind: WriteUpdate, Path: p, Fields: value.Map(), Precondition: Precondition{Exists: new(false)}}, existing,
			AlreadyExists, "Document already exists: " + name},
		{Write{Kind: WriteUpdate, Path: p, Fields: value.Map(), Precondition: Precondition{Exists: new(true)}}, nil,
			NotFound, "No document to update: " + name},
		{Write{Kind: WriteDelete, Path: p, Precondition: Precondition{Exists: new(true)}}, nil,
			NotFound, "No document to update: " + name},
		{Write{Kind: WriteUpdate, Path: p, Fields: value.Map(), Precondition: Precondition{UpdateTime: required}}, existing,
			FailedPrecondition, "the stored version (1793038697978566) does not match the required base version (1793038687926996)"},
		{Write{Kind: WriteDelete, Path: p, Precondition: Precondition{UpdateTime: required}}, nil,
			FailedPrecondition, "the stored version (0) does not match the required base version (1793038687926996)"},
	}
	for _, c := range cases {
		err := newPlan([]Write{c.write}).apply(testDB, c.locked, time.Time{})
		e, ok := err.(*Error)
		if !ok || e.Code != c.code || e.Message != c.msg {
			t.Errorf("%+v: got %v, want %d %q", c.write.Precondition, err, c.code, c.msg)
		}
	}
	ok := Write{Kind: WriteUpdate, Path: p, Fields: value.Map(), Precondition: Precondition{Exists: new(true), UpdateTime: stored}}
	if err := newPlan([]Write{ok}).apply(testDB, existing, time.Time{}); err != nil {
		t.Errorf("matching preconditions: %v", err)
	}
}

func TestPlanMasksAndResults(t *testing.T) {
	p := ParsePath("c/d")
	prev := time.UnixMicro(1_000_000)
	locked := map[string]*lockedDoc{string(storedKey(nameKey(p))): {seq: 3,
		fields: fieldsOf("a", value.Int(1), "m", fieldsOf("x", value.Int(1), "y", value.Int(2))), createTime: prev, updateTime: prev}}

	// A mask changes its paths alone, deletes those the update lacks, and ignores the rest.
	w := Write{Kind: WriteUpdate, Path: p, Masked: true, Mask: []value.FieldPath{mustPath(t, "m.x"), mustPath(t, "a")},
		Fields: fieldsOf("m", fieldsOf("x", value.Int(9)), "ignored", value.Int(0))}
	pl := newPlan([]Write{w})
	if err := pl.apply(testDB, locked, time.Time{}); err != nil {
		t.Fatal(err)
	}
	want := fieldsOf("m", fieldsOf("x", value.Int(9), "y", value.Int(2)))
	if d := pl.docs[0]; !value.Identical(d.fields, want) || !d.changed() {
		t.Fatalf("masked update: %v", d.fields)
	}
	commit := time.UnixMicro(2_000_000)
	if r := pl.results(commit); !r[0].UpdateTime.Equal(commit) {
		t.Errorf("changed document's update_time %v", r[0].UpdateTime)
	}

	// The same fields again change nothing: the previous update_time, no changelog row.
	same := newPlan([]Write{{Kind: WriteUpdate, Path: p, Fields: locked[string(storedKey(nameKey(p)))].fields},
		{Kind: WriteVerify, Path: p, Precondition: Precondition{Exists: new(true)}}})
	if err := same.apply(testDB, locked, time.Time{}); err != nil {
		t.Fatal(err)
	}
	var in stampInput
	if err := same.stampArgs(&in); err != nil || len(in.logKeys) != 0 {
		t.Fatalf("unchanged document logged: %v %d", err, len(in.logKeys))
	}
	if r := same.results(commit); !r[0].UpdateTime.Equal(prev) || !r[1].UpdateTime.Equal(prev) {
		t.Errorf("unchanged results %v", r)
	}

	// A delete of a missing document is no change and has no update_time.
	del := newPlan([]Write{{Kind: WriteDelete, Path: ParsePath("c/missing")}})
	if err := del.apply(testDB, nil, time.Time{}); err != nil || del.docs[0].changed() || !del.results(commit)[0].UpdateTime.IsZero() {
		t.Errorf("delete of a missing document: %v", err)
	}

	// Two writes to one document fold in order; the second's precondition sees the first.
	fold := newPlan([]Write{{Kind: WriteDelete, Path: p}, {Kind: WriteUpdate, Path: p, Fields: value.Map(), Precondition: Precondition{Exists: new(false)}}})
	if err := fold.apply(testDB, locked, time.Time{}); err != nil || len(fold.docs) != 1 || !fold.docs[0].exists {
		t.Errorf("folded writes: %v", err)
	}
}

// Every write of a document has its update_time as the commit leaves it: a delete then an update
// both have it, an update then a delete neither (f2-commit.json same.delete_update,
// same.update_delete).
func TestPlanResultsOfOneDocument(t *testing.T) {
	p := ParsePath("aruma-ref-f2-commit/s4")
	commit := time.UnixMicro(2_000_000)
	delUpdate := newPlan([]Write{{Kind: WriteDelete, Path: p}, {Kind: WriteUpdate, Path: p, Fields: fieldsOf("a", value.Int(1))}})
	if err := delUpdate.apply(testDB, nil, commit); err != nil {
		t.Fatal(err)
	}
	if r := delUpdate.results(commit); !r[0].UpdateTime.Equal(commit) || !r[1].UpdateTime.Equal(commit) {
		t.Errorf("delete then update: %v", r)
	}
	updateDel := newPlan([]Write{{Kind: WriteUpdate, Path: p, Fields: fieldsOf("a", value.Int(1))}, {Kind: WriteDelete, Path: p}})
	if err := updateDel.apply(testDB, nil, commit); err != nil {
		t.Fatal(err)
	}
	if r := updateDel.results(commit); !r[0].UpdateTime.IsZero() || !r[1].UpdateTime.IsZero() {
		t.Errorf("update then delete: %v", r)
	}
}

// Of two failed preconditions, production names the missing document's, in either order
// (f2-commit.json pre.two_failures, pre.two_failures_reversed).
func TestPlanTwoFailedPreconditions(t *testing.T) {
	missing, n1 := ParsePath("aruma-ref-f2-commit/missing6"), ParsePath("aruma-ref-f2-commit/n1")
	stored := time.UnixMicro(1793038697978566)
	locked := map[string]*lockedDoc{string(storedKey(nameKey(n1))): {seq: 1, fields: fieldsOf("a", value.Int(1)), createTime: stored, updateTime: stored}}
	notFound := Write{Kind: WriteUpdate, Path: missing, Fields: value.Map(), Precondition: Precondition{Exists: new(true)}}
	exists := Write{Kind: WriteUpdate, Path: n1, Fields: value.Map(), Precondition: Precondition{Exists: new(false)}}
	want := "No document to update: " + testDB.DocumentName(missing)
	for _, ws := range [][]Write{{notFound, exists}, {exists, notFound}} {
		err := newPlan(ws).apply(testDB, locked, time.Time{})
		if e, ok := errors.AsType[*Error](err); !ok || e.Code != NotFound || e.Message != want {
			t.Errorf("got %v, want %q", err, want)
		}
	}
}

func TestPlanDocumentTooLarge(t *testing.T) {
	p := ParsePath("aruma-ref-f0/big")
	// One byte over the limit, as production measures it.
	base := value.DocumentSize(p.String(), fieldsOf("s", value.String("")))
	big := fieldsOf("s", value.String(strings.Repeat("x", value.MaxDocumentSize-base+1)))
	err := newPlan([]Write{{Kind: WriteUpdate, Path: p, Fields: big}}).apply(testDB, nil, time.Time{})
	want := "Document 'projects/chatapp-8ae21/databases/(default)/documents/aruma-ref-f0/big' cannot be written because its size (1,048,577 bytes) exceeds the maximum allowed size of 1,048,576 bytes."
	if e, ok := err.(*Error); !ok || e.Code != InvalidArgument || e.Message != want {
		t.Fatalf("got %v", err)
	}
}

func TestDatabaseNotFoundTexts(t *testing.T) {
	e := &DatabaseNotFoundError{ProjectID: "chatapp-8ae21", DatabaseID: "aruma-ref-nonexistent"}
	if got := e.RESTMessage(); got != "The database aruma-ref-nonexistent does not exist for project chatapp-8ae21 Please visit https://console.cloud.google.com/datastore/setup?project=chatapp-8ae21 to add a Cloud Datastore or Cloud Firestore database. " {
		t.Errorf("REST: %q", got)
	}
	if got := e.AdminMessage(); got != "Project 'chatapp-8ae21' or database 'aruma-ref-nonexistent' does not exist." {
		t.Errorf("Admin: %q", got)
	}
	if e.GRPCMessage() != "" || e.Code() != NotFound {
		t.Error("gRPC")
	}
}

// A commit of many writes of one large document stops at MaxCommitWork (F2 review M1): its
// checks cost writes × the document's size, which the budget bounds.
func TestCommitWorkBound(t *testing.T) {
	p := ParsePath("c/d")
	fs := make([]value.Field, 1000)
	for i := range fs {
		fs[i] = value.Field{Name: "f" + strconv.Itoa(i), Value: value.String(strings.Repeat("x", 1000))}
	}
	locked := map[string]*lockedDoc{string(storedKey(nameKey(p))): {fields: value.Map(fs...)}}
	size := value.DocumentSize(p.String(), value.Map(fs...))
	writes := func(n int) []Write {
		ws := make([]Write, n)
		for i := range ws {
			ws[i] = Write{Kind: WriteUpdate, Path: p, Masked: true, Mask: []value.FieldPath{{"g"}}, Fields: doc("g", value.Int(int64(i)))}
		}
		return ws
	}
	start := time.Now()
	if err := newPlan(writes(MaxCommitWork/size+10)).apply(testDB, locked, time.Now()); !errors.Is(err, errCommitWork) {
		t.Fatalf("over the budget: %v", err)
	}
	t.Logf("%d writes of %d bytes: %v", MaxCommitWork/size+10, size, time.Since(start))
	if err := newPlan(writes(MaxCommitWork/(size+20))).apply(testDB, locked, time.Now()); err != nil {
		t.Fatalf("within the budget: %v", err)
	}
}
