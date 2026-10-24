package rules

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The tests of the contract commit (S1-0): the parts every task builds on. Each task's own
// behaviour is tested in its own files.

func TestValueKinds(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 5, time.FixedZone("x", 3600))
	for _, c := range []struct {
		v    Value
		kind Kind
		name string
	}{
		{Null(), KindNull, "null"},
		{Bool(true), KindBool, "bool"},
		{Int(-3), KindInt, "int"},
		{Float(1.5), KindFloat, "float"},
		{String("نص 🙂"), KindString, "string"},
		{Bytes([]byte{0, 1}), KindBytes, "bytes"},
		{List(nil), KindList, "list"},
		{Map(nil), KindMap, "map"},
		{Timestamp(now), KindTimestamp, "timestamp"},
		{Duration(-1500 * time.Millisecond), KindDuration, "duration"},
		{LatLng(24.7, 46.7), KindLatLng, "latlng"},
		{PathOf([]string{"databases", "(default)", "documents"}), KindPath, "path"},
		{Value{}, kindInvalid, "invalid"},
	} {
		if c.v.Kind() != c.kind || c.kind.String() != c.name {
			t.Errorf("%#v: kind %v (%q), want %v (%q)", c.v, c.v.Kind(), c.v.Kind().String(), c.kind, c.name)
		}
	}
	// The kinds reserved for F5 exist and have names, but no constructor makes them.
	for k, name := range map[Kind]string{KindSet: "set", KindMapDiff: "map_diff", KindConstraint: "constraint", KindUnknown: "unknown"} {
		if k.String() != name {
			t.Errorf("Kind %d named %q, want %q", k, k.String(), name)
		}
	}
	if Kind(200).String() != "invalid" {
		t.Error("an unknown kind is not named invalid")
	}
}

func TestValueReaders(t *testing.T) {
	if b, ok := Bool(true).AsBool(); !b || !ok {
		t.Error("AsBool")
	}
	if i, ok := Int(math.MinInt64).AsInt(); i != math.MinInt64 || !ok {
		t.Error("AsInt")
	}
	if _, ok := Int(1).AsFloat(); ok {
		t.Error("an int reads as a float")
	}
	if _, ok := Float(1).AsInt(); ok {
		t.Error("a float reads as an int")
	}
	if s, ok := String("a").AsString(); s != "a" || !ok {
		t.Error("AsString")
	}
	if _, ok := Bytes([]byte("a")).AsString(); ok {
		t.Error("bytes read as a string")
	}
	raw := []byte{1, 2}
	bv := Bytes(raw)
	raw[0] = 9
	if b, ok := bv.AsBytes(); !ok || b[0] != 1 {
		t.Error("Bytes does not copy its argument")
	}
	if l, ok := List([]Value{Int(1)}).AsList(); !ok || len(l) != 1 {
		t.Error("AsList")
	}
	if m, ok := Map(map[string]Value{"a": Null()}).AsMap(); !ok || m["a"].Kind() != KindNull {
		t.Error("AsMap")
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("x", 3600))
	if ts, ok := Timestamp(at).AsTimestamp(); !ok || !ts.Equal(at) || ts.Location() != time.UTC {
		t.Error("AsTimestamp: not the same instant in UTC")
	}
	for _, d := range []time.Duration{0, -1500 * time.Millisecond, math.MaxInt64, math.MinInt64 + 1} {
		if got, ok := Duration(d).AsDuration(); !ok || got != d {
			t.Errorf("Duration(%v).AsDuration() = %v, %v", d, got, ok)
		}
	}
	if lat, lng, ok := LatLng(1, 2).AsLatLng(); !ok || lat != 1 || lng != 2 {
		t.Error("AsLatLng")
	}
	segs := []string{"a", "b"}
	p := PathOf(segs)
	segs[0] = "z"
	if got, ok := p.AsPath(); !ok || strings.Join(got, "/") != "a/b" {
		t.Error("PathOf does not copy its argument")
	}
	if _, ok := (Value{}).AsBool(); ok {
		t.Error("the zero Value reads as a bool")
	}
	// == on two lists would panic at run time; it must not compile.
	if reflect.TypeFor[Value]().Comparable() {
		t.Error("Value is comparable with ==")
	}
}

func TestSegments(t *testing.T) {
	segs := Literals("b", "bucket", "o", "")
	for i, want := range []string{"b", "bucket", "o", ""} {
		if n, ok := segs[i].Name(); !ok || n != want {
			t.Errorf("segment %d = %q, %v; want literal %q", i, n, ok, want)
		}
	}
	if (Segment{}) != Literal("") {
		t.Error("the zero Segment is not the empty literal")
	}
	for _, s := range []Segment{UnknownSegment, UnknownRun} {
		if _, ok := s.Name(); ok {
			t.Errorf("%v reads as a literal", s)
		}
	}
	if UnknownSegment == UnknownRun || UnknownSegment == Literal("<unknown>") {
		t.Error("the unknown segments are not distinct")
	}
}

func TestMethodsAndOutcomes(t *testing.T) {
	for m, want := range map[Method]string{MethodGet: "get", MethodList: "list", MethodCreate: "create",
		MethodUpdate: "update", MethodDelete: "delete", 0: "invalid"} {
		if m.String() != want {
			t.Errorf("Method %d = %q, want %q", m, m.String(), want)
		}
	}
	for o, want := range map[Outcome]string{OutcomeFalse: "false", OutcomeTrue: "true", OutcomeError: "error"} {
		if o.String() != want {
			t.Errorf("Outcome %d = %q, want %q", o, o.String(), want)
		}
	}
}

func TestTokenText(t *testing.T) {
	for tok := tokIllegal; tok <= tokDecrement; tok++ {
		if tok != tokIllegal && (tok.String() == "" || tok.String() == "ILLEGAL") {
			t.Errorf("token %d has no text", tok)
		}
	}
	if tokDecrement+1 != token(len(tokenText)) {
		t.Error("tokenText does not cover every token")
	}
}

func TestIssues(t *testing.T) {
	warning := Issue{File: "storage.rules", Severity: SeverityWarning, Description: "Missing 'rules_version' statement."}
	unused := Issue{File: "storage.rules", Position: &Position{Line: 3, Column: 12}, Severity: SeverityWarning, Description: "Unused function: f."}
	if Issues([]Issue{warning, unused}).Err() != nil {
		t.Error("warnings alone fail compilation")
	}
	if got := warning.String(); got != "[W] Missing 'rules_version' statement." {
		t.Errorf("a whole-file issue prints %q", got)
	}
	bad := Issue{File: "storage.rules", Position: &Position{Line: 5, Column: 10}, Severity: SeverityError, Description: "Unexpected '}'."}
	if got := bad.String(); got != "[E] 5:10 - Unexpected '}'." {
		t.Errorf("an error prints %q, not as firebase-tools does", got)
	}
	err := Issues([]Issue{warning, bad}).Err()
	if err == nil || !strings.Contains(err.Error(), "storage.rules") || !strings.Contains(err.Error(), "[E] 5:10") ||
		strings.Contains(err.Error(), "[W]") {
		t.Errorf("Err() = %v", err)
	}
	if SeverityError.String() != "ERROR" || SeverityWarning.String() != "WARNING" {
		t.Error("severities are not named as the Rules API names them")
	}
}

// The checker resolves calls by these names before S1-B's library exists.
func TestLibraryNames(t *testing.T) {
	for _, name := range []string{"int", "float", "string", "path", "debug", "math.abs", "hashing.sha256", "latlng.value", "timestamp.date", "duration.value"} {
		if !isLibraryFunction(name) {
			t.Errorf("%s is not a library function", name)
		}
	}
	// The lookups are lookup.go's, not the library's.
	for _, name := range []string{"get", "exists", "getAfter", "firestore.get", "math", "size", ""} {
		if isLibraryFunction(name) {
			t.Errorf("%s is a library function", name)
		}
	}
	for _, ns := range []string{"math", "hashing", "latlng", "timestamp", "duration"} {
		if !isLibraryNamespace(ns) {
			t.Errorf("%s is not a library namespace", ns)
		}
	}
	if isLibraryNamespace("firestore") || isLibraryNamespace("request") {
		t.Error("firestore or request is a library namespace")
	}
}

func TestRulesetWeight(t *testing.T) {
	lim := DefaultLimits()
	rs := &Ruleset{src: strings.Repeat("x", 1000), lim: lim, regexes: newRegexCache(&lim)}
	if got, want := rs.Weight(), int64(1000)+lim.RegexCacheBytes; got != want {
		t.Errorf("Weight() = %d, want the text plus the pattern cache's cap, %d", got, want)
	}
}

func TestNoDocuments(t *testing.T) {
	var d Documents = NoDocuments{}
	_, found, err := d.Get(context.Background(), 1, []string{"databases", "(default)", "documents", "c", "d"}, false)
	if found || !errors.Is(err, ErrNoDocuments) {
		t.Errorf("NoDocuments.Get = %v, %v; want ErrNoDocuments", found, err)
	}
}

// The lookup limits come from the ruleset's service and limits, never from the budget, which
// holds only the request's count and cache.
func TestLookupLimits(t *testing.T) {
	lim := DefaultLimits()
	if op, req := lookupLimits(Firestore, &lim); op != 10 || req != 20 {
		t.Errorf("Firestore: %d per operation, %d per request; want 10 and 20", op, req)
	}
	if op, req := lookupLimits(Storage, &lim); op != 2 || req != 2 {
		t.Errorf("Storage: %d per operation, %d per request; want 2 and 2", op, req)
	}
	if op, req := lookupLimits("cloud.unknown", &lim); op != 0 || req != 0 {
		t.Error("an unknown service allows lookups")
	}
	if b := NewBudget(); b.used != 0 || b.cache == nil {
		t.Error("a new budget is not empty")
	}
}

func TestAbortUnwraps(t *testing.T) {
	var err error = &abort{msg: "evaluation memory exhausted", err: ErrOverloaded}
	if !errors.Is(err, ErrOverloaded) {
		t.Error("an overload abort does not unwrap to ErrOverloaded")
	}
	err = &abort{msg: "limit"}
	if errors.Is(err, ErrOverloaded) || err.Error() != "limit" {
		t.Errorf("a limit abort = %v", err)
	}
	if e := errorf("Type error. Received: [%s] Expected: [%s].", KindInt, KindBool); e.Error() != "Type error. Received: [int] Expected: [bool]." {
		t.Errorf("errorf = %q", e.Error())
	}
}
