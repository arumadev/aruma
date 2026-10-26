package value

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The production recordings of test/oracle/documents/corpus, written by
// production/record-values.mjs, judge this package: these tests replay them offline.

const corpusDir = "../../../../test/oracle/documents/corpus"

func readCorpus(t *testing.T, name string, into any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatal(err)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(into); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// compactRE matches lib.mjs's compact form of a long request string.
var compactRE = regexp.MustCompile(`^<string (\d+) bytes, sha256 ([0-9a-f]{64}), starts "(.*)">$`)

// expand rebuilds a string the recorder compacted: every long string it sends repeats its
// start, which the SHA-256 confirms.
func expand(t *testing.T, s string) string {
	m := compactRE.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	n, _ := strconv.Atoi(m[1])
	var start string
	if err := json.Unmarshal([]byte(`"`+m[3]+`"`), &start); err != nil || start == "" {
		t.Fatalf("compacted string %q: %v", s, err)
	}
	// One character repeated, or the base64 of one repeated byte, a 4-character period.
	for _, period := range []int{1, 4} {
		if period > len(start) {
			break
		}
		full := strings.Repeat(start[:period], n/period+1)[:n]
		if sum := sha256.Sum256([]byte(full)); hex.EncodeToString(sum[:]) == m[2] {
			return full
		}
	}
	// The base64 of one repeated byte, padded.
	if b, err := base64.StdEncoding.DecodeString(start[:4]); err == nil {
		for k := range 3 {
			full := base64.StdEncoding.EncodeToString(bytes.Repeat(b[:1], n/4*3-k))
			if sum := sha256.Sum256([]byte(full)); hex.EncodeToString(sum[:]) == m[2] {
				return full
			}
		}
	}
	t.Fatalf("compacted string %q does not repeat its start", s[:60])
	return ""
}

// fromJSON converts a proto3 JSON value as recorded (doubles may be "NaN", "Infinity",
// "-Infinity" or "-0") to a Value.
func fromJSON(t *testing.T, x any) Value {
	t.Helper()
	m, ok := x.(map[string]any)
	if !ok || len(m) != 1 {
		t.Fatalf("not a value: %v", x)
	}
	for k, y := range m {
		switch k {
		case "nullValue":
			return Null()
		case "booleanValue":
			return Bool(y.(bool))
		case "integerValue":
			i, err := strconv.ParseInt(y.(string), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return Int(i)
		case "doubleValue":
			return Double(jsonDouble(t, y))
		case "timestampValue":
			ts, err := time.Parse(time.RFC3339Nano, y.(string))
			if err != nil {
				t.Fatal(err)
			}
			return Timestamp(ts.Unix(), int32(ts.Nanosecond()))
		case "stringValue":
			return String(expand(t, y.(string)))
		case "bytesValue":
			s := y.(string)
			b, err := base64.StdEncoding.DecodeString(expand(t, s))
			if err != nil {
				t.Fatal(err)
			}
			return Bytes(b)
		case "referenceValue":
			return Reference(y.(string))
		case "geoPointValue":
			g := y.(map[string]any)
			var lat, lng float64
			if v, ok := g["latitude"]; ok {
				lat = jsonDouble(t, v)
			}
			if v, ok := g["longitude"]; ok {
				lng = jsonDouble(t, v)
			}
			return GeoPoint(lat, lng)
		case "arrayValue":
			var els []Value
			if vs, ok := y.(map[string]any)["values"]; ok {
				for _, e := range vs.([]any) {
					els = append(els, fromJSON(t, e))
				}
			}
			return Array(els...)
		case "mapValue":
			return fieldsJSON(t, y.(map[string]any)["fields"])
		}
		t.Fatalf("unknown value key %q", k)
	}
	panic("unreachable")
}

func fieldsJSON(t *testing.T, x any) Value {
	t.Helper()
	fs, _ := x.(map[string]any)
	out := make([]Field, 0, len(fs))
	for k, y := range fs {
		out = append(out, Field{k, fromJSON(t, y)})
	}
	return Map(out...)
}

func jsonDouble(t *testing.T, x any) float64 {
	t.Helper()
	switch y := x.(type) {
	case json.Number:
		f, err := strconv.ParseFloat(string(y), 64)
		if err != nil {
			t.Fatal(err)
		}
		return f
	case string:
		switch y {
		case "NaN":
			return math.NaN()
		case "Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		case "-0":
			return math.Copysign(0, -1)
		}
	}
	t.Fatalf("not a double: %v", x)
	return 0
}

type orderCorpus struct {
	Values []struct {
		ID     string `json:"id"`
		Sent   any    `json:"sent"`
		Stored any    `json:"stored"`
		Write  struct {
			HTTP  int `json:"http"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"write"`
	} `json:"values"`
	OrderAsc      struct{ IDs []string }     `json:"orderAsc"`
	OrderDesc     struct{ IDs []string }     `json:"orderDesc"`
	CursorClasses map[string]json.RawMessage `json:"cursorClasses"`
}

// valueCorpora are the recordings of one document per value in one collection, field v: the
// edge values of every type, and the BSON shapes with their neighbours. Each holds the order,
// cursor classes, equality results and filters of its own documents.
var valueCorpora = []struct{ order, equality string }{
	{"values-order.json", "values-equality.json"},
	{"values-bson.json", "values-bson.json"},
}

// storedValues returns the values production stored, by document id.
func storedValues(t *testing.T, file string) (map[string]Value, *orderCorpus) {
	var c orderCorpus
	readCorpus(t, file, &c)
	vs := map[string]Value{}
	for _, x := range c.Values {
		if x.Write.HTTP == 200 {
			vs[x.ID] = fromJSON(t, x.Stored)
		}
	}
	if len(vs) < 100 {
		t.Fatalf("%s: only %d stored values", file, len(vs))
	}
	return vs, &c
}

// TestCompareReproducesProductionOrder sorts the recorded values with Compare, ties broken by
// document id as production breaks them by __name__, and expects production's orderBy v
// results, ascending and descending.
func TestCompareReproducesProductionOrder(t *testing.T) {
	for _, files := range valueCorpora {
		vs, c := storedValues(t, files.order)
		ids := slices.Sorted(maps.Keys(vs))
		asc := slices.Clone(ids)
		slices.SortFunc(asc, func(a, b string) int { return cmp.Or(Compare(vs[a], vs[b]), strings.Compare(a, b)) })
		if !slices.Equal(asc, c.OrderAsc.IDs) {
			t.Errorf("%s: ascending order differs from production:\n%s", files.order, diffOrder(asc, c.OrderAsc.IDs))
		}
		desc := slices.Clone(ids)
		slices.SortFunc(desc, func(a, b string) int { return cmp.Or(Compare(vs[b], vs[a]), strings.Compare(b, a)) })
		if !slices.Equal(desc, c.OrderDesc.IDs) {
			t.Errorf("%s: descending order differs from production:\n%s", files.order, diffOrder(desc, c.OrderDesc.IDs))
		}
	}
}

// setDiff describes how two sorted id lists differ.
func setDiff(got, want []string) string {
	var extra, missing []string
	for _, g := range got {
		if !slices.Contains(want, g) {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			missing = append(missing, w)
		}
	}
	return "extra " + strings.Join(extra, " ") + "; missing " + strings.Join(missing, " ")
}

func diffOrder(got, want []string) string {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			lo, hi := max(i-3, 0), min(i+4, len(got), len(want))
			return "at " + strconv.Itoa(i) + ": got " + strings.Join(got[lo:hi], " ") + "\n       want " + strings.Join(want[lo:hi], " ")
		}
	}
	return "lengths " + strconv.Itoa(len(got)) + " and " + strconv.Itoa(len(want))
}

// TestCompareTiesAsProductionCursors checks every startAt(v)/endAt(v) class: the documents
// that tie with v under Compare, and no others. A cursor production refuses as too large is a
// value over the limit that the index does not cut.
func TestCompareTiesAsProductionCursors(t *testing.T) {
	for _, files := range valueCorpora {
		vs, c := storedValues(t, files.order)
		if len(c.CursorClasses) != len(vs) {
			t.Errorf("%s: %d cursor classes for %d values", files.order, len(c.CursorClasses), len(vs))
		}
		for id, raw := range c.CursorClasses {
			v, ok := vs[id]
			if !ok {
				t.Errorf("%s: cursor class of %s, which production did not store", files.order, id)
				continue
			}
			var class []string
			if json.Unmarshal(raw, &class) != nil {
				var e struct{ Error struct{ Message string } }
				if err := json.Unmarshal(raw, &e); err != nil || e.Error.Message != "the cursor value is too large" {
					t.Errorf("%s: unexpected cursor result %s", id, raw)
				}
				if IndexedWhole(v) || Truncated(v) {
					t.Errorf("%s: production refuses the cursor; IndexSize %d, Truncated %v", id, IndexSize(v), Truncated(v))
				}
				continue
			}
			if !IndexedWhole(v) && !Truncated(v) {
				t.Errorf("%s: production accepts the cursor, but IndexSize is %d and the value is not cut", id, IndexSize(v))
			}
			var got []string
			for j, w := range vs {
				if Compare(w, v) == 0 {
					got = append(got, j)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, class) {
				t.Errorf("%s: %s ties: %s", files.order, id, setDiff(got, class))
			}
		}
	}
}

type equalityCorpus struct {
	Equal   map[string]json.RawMessage `json:"equal"`
	In      map[string]json.RawMessage `json:"in"`
	Filters []struct {
		Label   string          `json:"label"`
		Request any             `json:"request"`
		IDs     []string        `json:"ids"`
		Error   json.RawMessage `json:"error"`
	} `json:"filters"`
}

// rangeOps are the range operators by their proto names.
var rangeOps = map[string]RangeOp{"LESS_THAN": Less, "LESS_THAN_OR_EQUAL": LessOrEqual, "GREATER_THAN": Greater,
	"GREATER_THAN_OR_EQUAL": GreaterOrEqual}

// TestEqualReproducesProductionFilters replays where v == x and v in [x] for every recorded
// value, and every other recorded filter: ranges (InRange), array-contains, array-contains-any,
// in, != and not-in, the unary filters, and the operands production refuses (ValidateOperand).
// No recorded filter is skipped, and values-equality.json's 60 accepted range filters must all
// run.
func TestEqualReproducesProductionFilters(t *testing.T) {
	for _, files := range valueCorpora {
		vs, _ := storedValues(t, files.order)
		var c equalityCorpus
		readCorpus(t, files.equality, &c)
		ranges := replayFilters(t, files.equality, vs, c)
		if files.equality == "values-equality.json" && ranges != 60 {
			t.Errorf("%s: %d accepted range filters checked, recorded 60", files.equality, ranges)
		}
	}
}

// replayFilters replays one corpus's filters and returns how many accepted range filters it
// checked.
func replayFilters(t *testing.T, file string, vs map[string]Value, c equalityCorpus) (ranges int) {
	matching := func(pred func(Value) bool) []string {
		var out []string
		for j, w := range vs {
			if pred(w) {
				out = append(out, j)
			}
		}
		slices.Sort(out)
		return out
	}
	for name, results := range map[string]map[string]json.RawMessage{"==": c.Equal, "in": c.In} {
		for id, raw := range results {
			x, ok := vs[id]
			if !ok {
				t.Errorf("%s: v %s %s, which production did not store", file, name, id)
				continue
			}
			var r struct {
				IDs   []string
				Error *struct{ Message string }
			}
			if err := json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			if r.Error != nil {
				if r.Error.Message != "value for v is too large to be used in a query" || IndexedWhole(x) {
					t.Errorf("v %s %s: production refused %q; IndexSize %d", name, id, r.Error.Message, IndexSize(x))
				}
				continue
			}
			if !IndexedWhole(x) {
				t.Errorf("v %s %s: production accepts an operand of IndexSize %d, Truncated %v", name, id, IndexSize(x), Truncated(x))
			}
			if got := matching(func(w Value) bool { return Equal(w, x) }); !slices.Equal(got, r.IDs) {
				t.Errorf("%s: v %s %s: Equal: %s", file, name, id, setDiff(got, r.IDs))
			}
		}
	}
	contains := func(w, x Value) bool {
		if x.IsNull() || x.IsNaN() {
			// Production's array-contains null (or NaN) matches the arrays that hold an empty
			// array, and nothing else: [null] and [NaN] do not match (tr-empty-arrays-*). The
			// query layer must do the same; Equal(null, []) stays false.
			return slices.ContainsFunc(w.Elements(), func(e Value) bool { return e.kind == KindArray && len(e.list) == 0 })
		}
		return slices.ContainsFunc(w.Elements(), func(e Value) bool { return Equal(e, x) })
	}
	for _, f := range c.Filters {
		req := f.Request.(map[string]any)
		var pred func(Value) bool
		if u, ok := req["unaryFilter"].(map[string]any); ok {
			switch u["op"] {
			case "IS_NAN":
				pred = Value.IsNaN
			case "IS_NOT_NAN":
				pred = func(w Value) bool { return !w.IsNull() && !w.IsNaN() }
			case "IS_NULL":
				pred = Value.IsNull
			case "IS_NOT_NULL":
				pred = func(w Value) bool { return !w.IsNull() }
			default:
				t.Errorf("%s: %s: unknown unary operator", file, f.Label)
				continue
			}
		} else {
			ff := req["fieldFilter"].(map[string]any)
			op := ff["op"].(string)
			x := fromJSON(t, ff["value"])
			if len(f.Error) > 0 && string(f.Error) != "null" {
				var e struct{ Message string }
				if err := json.Unmarshal(f.Error, &e); err != nil {
					t.Fatal(err)
				}
				if err := ValidateOperand(x); err == nil || err.Error() != e.Message {
					t.Errorf("%s: %s: ValidateOperand = %v, production %q", file, f.Label, err, e.Message)
				}
				continue
			}
			if err := ValidateOperand(x); err != nil {
				t.Errorf("%s: %s: production accepts, ValidateOperand: %v", file, f.Label, err)
			}
			switch op {
			case "EQUAL":
				pred = func(w Value) bool { return Equal(w, x) }
			case "ARRAY_CONTAINS":
				pred = func(w Value) bool { return contains(w, x) }
			case "ARRAY_CONTAINS_ANY":
				pred = func(w Value) bool {
					return slices.ContainsFunc(x.Elements(), func(y Value) bool { return contains(w, y) })
				}
			case "IN":
				pred = func(w Value) bool {
					return slices.ContainsFunc(x.Elements(), func(y Value) bool { return Equal(w, y) })
				}
			case "NOT_IN", "NOT_EQUAL":
				// != and not-in leave out null, and an operand holding null matches nothing.
				ops := []Value{x}
				if op == "NOT_IN" {
					ops = x.Elements()
				}
				if slices.ContainsFunc(ops, Value.IsNull) {
					pred = func(Value) bool { return false }
					break
				}
				pred = func(w Value) bool {
					return !w.IsNull() && !slices.ContainsFunc(ops, func(y Value) bool { return Equal(w, y) })
				}
			default:
				rop, ok := rangeOps[op]
				if !ok {
					t.Errorf("%s: %s: unknown operator", file, f.Label)
					continue
				}
				ranges++
				pred = func(w Value) bool { return InRange(rop, w, x) }
				checkBounds(t, f.Label, rop, x, vs)
			}
		}
		want := slices.Clone(f.IDs)
		slices.Sort(want)
		if got := matching(pred); !slices.Equal(got, want) {
			t.Errorf("%s: %s: %s", file, f.Label, setDiff(got, want))
		}
	}
	return ranges
}

// checkBounds checks that ClassBounds brackets a range filter's matches: every value InRange
// accepts has a key inside [lo, hi) on the operand's side, as a store would scan it.
func checkBounds(t *testing.T, label string, op RangeOp, x Value, vs map[string]Value) {
	t.Helper()
	lo, hi, ok := ClassBounds(x)
	kx := Key(x)
	for id, w := range vs {
		if !InRange(op, w, x) {
			continue
		}
		kw := Key(w)
		switch {
		case !ok:
			t.Errorf("%s: %s matches, but ClassBounds says nothing can", label, id)
		case bytes.Compare(kw, lo) < 0 || bytes.Compare(kw, hi) >= 0:
			t.Errorf("%s: %s matches, but its key lies outside the class bounds", label, id)
		case (op == Greater || op == GreaterOrEqual) && bytes.Compare(kw, kx) < 0,
			(op == Less || op == LessOrEqual) && bytes.Compare(kw, kx) > 0:
			t.Errorf("%s: %s matches, but its key lies on the wrong side of the operand's", label, id)
		}
	}
}

type sizeCorpus struct {
	Cases []struct {
		ID                  string `json:"id"`
		FieldName           string `json:"fieldName"`
		Value               any    `json:"value"`
		FillerBytes         []int  `json:"fillerBytes"`
		ProductionValueSize *int   `json:"productionValueSize"`
		ProductionDocSize   *int   `json:"productionDocumentSize"`
	} `json:"cases"`
	Cap []struct {
		ID       string `json:"id"`
		Document string `json:"document"`
		Value    any    `json:"value"`
		Write    struct {
			HTTP  int `json:"http"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"write"`
	} `json:"cap"`
}

// TestSizeMatchesProduction checks Size and DocumentSize against the sizes production put in
// its "exceeds the maximum allowed size" messages, for a value of every type.
func TestSizeMatchesProduction(t *testing.T) {
	var c sizeCorpus
	readCorpus(t, "values-size.json", &c)
	sizeRE := regexp.MustCompile(`its size \(([\d,]+) bytes\)`)
	for _, x := range c.Cases {
		if x.ProductionValueSize == nil {
			t.Errorf("%s: production gave no size", x.ID)
			continue
		}
		v := fromJSON(t, x.Value)
		if got := Size(v); got != *x.ProductionValueSize {
			t.Errorf("%s: Size(%v) = %d, production %d", x.ID, v, got, *x.ProductionValueSize)
		}
		doc := Map(Field{"s1", String(strings.Repeat("x", x.FillerBytes[0]))},
			Field{"s2", String(strings.Repeat("y", x.FillerBytes[1]))}, Field{x.FieldName, v})
		name := "projects/p/databases/(default)/documents/aruma-ref-values-size/" + strings.TrimPrefix(x.ID, "size.")
		if got := DocumentSize(name, doc); got != *x.ProductionDocSize {
			t.Errorf("%s: DocumentSize = %d, production %d", x.ID, got, *x.ProductionDocSize)
		}
	}
	for _, x := range c.Cap {
		doc := Map(Field{"p", fromJSON(t, x.Value)})
		size := DocumentSize(x.Document, doc)
		err := Validate(doc)
		switch {
		case x.Write.HTTP == 200:
			if err != nil || size > MaxDocumentSize {
				t.Errorf("%s: production accepts; Validate %v, DocumentSize %d", x.ID, err, size)
			}
		case sizeRE.MatchString(x.Write.Error.Message):
			want, _ := strconv.Atoi(strings.ReplaceAll(sizeRE.FindStringSubmatch(x.Write.Error.Message)[1], ",", ""))
			if size != want {
				t.Errorf("%s: DocumentSize %d, production %d", x.ID, size, want)
			}
		default:
			if err == nil || err.Error() != x.Write.Error.Message {
				t.Errorf("%s: Validate = %v, production %q", x.ID, err, x.Write.Error.Message)
			}
		}
	}
}

// TestDocumentSizeMatchesF0 checks DocumentSize against the 1 MiB refusals of the F0 run.
func TestDocumentSizeMatchesF0(t *testing.T) {
	var c struct {
		Cases []struct {
			ID      string `json:"id"`
			Request struct {
				Body struct {
					Writes []struct {
						Update struct {
							Name   string `json:"name"`
							Fields any    `json:"fields"`
						} `json:"update"`
					} `json:"writes"`
				} `json:"body"`
			} `json:"request"`
			Error *struct{ Message string } `json:"error"`
		} `json:"cases"`
	}
	readCorpus(t, "f0-errors.json", &c)
	sizeRE := regexp.MustCompile(`its size \(([\d,]+) bytes\)`)
	n := 0
	for _, x := range c.Cases {
		if !strings.HasPrefix(x.ID, "rest.doc_size.") || x.Error == nil {
			continue
		}
		w := x.Request.Body.Writes[0].Update
		want, _ := strconv.Atoi(strings.ReplaceAll(sizeRE.FindStringSubmatch(x.Error.Message)[1], ",", ""))
		if got := DocumentSize(w.Name, fieldsJSON(t, w.Fields)); got != want {
			t.Errorf("%s: DocumentSize %d, production %d", x.ID, got, want)
		}
		n++
	}
	if n == 0 {
		t.Fatal("no doc_size refusal in f0-errors.json")
	}
}

type validateCase struct {
	ID     string `json:"id"`
	Fields any    `json:"fields"`
	Write  struct {
		HTTP  int `json:"http"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"write"`
}

// wireLevel lists the refusals production makes while parsing the request, before any value
// exists: timestamps, integers, bytes and reference names (__id05__ is not a numeric id but an
// invalid one) belong to the wire package.
func wireLevel(id string) bool {
	for _, p := range []string{"time.year_", "int.", "ref.", "bytes.", "order.r-c-id-"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// knownGaps are the recorded writes Validate is known to answer differently, each with why. The
// test asserts that each still differs, so an entry fails the day it is fixed and must go.
var knownGaps = map[string]string{
	"bson.bad-min-max": "production names the first reserved key in request order ({__min__, __max__} as __min__); a Value keeps byte order",
}

// TestValidateReproducesProduction replays every recorded write: Validate accepts what
// production stored and refuses the rest with production's message.
func TestValidateReproducesProduction(t *testing.T) {
	var c struct{ Cases []validateCase }
	readCorpus(t, "values-validate.json", &c)
	cases := c.Cases
	// The value runs' writes count too: a NaN vector, a stray __type__, the BSON shapes.
	for _, file := range []string{"values-order.json", "values-bson.json"} {
		var o struct {
			Values []struct {
				validateCase
				Sent any `json:"sent"`
			} `json:"values"`
			Documents []validateCase `json:"documents"`
		}
		readCorpus(t, file, &o)
		prefix := strings.TrimSuffix(strings.TrimPrefix(file, "values-"), ".json") + "."
		for _, v := range o.Values {
			vc := v.validateCase
			vc.ID = prefix + v.ID
			vc.Fields = map[string]any{"v": v.Sent}
			cases = append(cases, vc)
		}
		for _, d := range o.Documents {
			d.ID = prefix + d.ID
			cases = append(cases, d)
		}
	}
	gaps := 0
	for _, x := range cases {
		if wireLevel(x.ID) {
			continue
		}
		err := Validate(fieldsJSON(t, x.Fields))
		agrees := x.Write.HTTP == 200 && err == nil || x.Write.HTTP != 200 && err != nil && err.Error() == x.Write.Error.Message
		if why, ok := knownGaps[x.ID]; ok {
			gaps++
			if agrees {
				t.Errorf("%s: known gap no longer reproduces (%s): remove it from knownGaps", x.ID, why)
			}
			continue
		}
		switch {
		case agrees:
		case x.Write.HTTP == 200:
			t.Errorf("%s: production accepts, Validate: %v", x.ID, err)
		default:
			t.Errorf("%s: Validate = %v, production %q", x.ID, err, x.Write.Error.Message)
		}
	}
	if gaps != len(knownGaps) {
		t.Errorf("%d of %d known gaps found among the recorded writes", gaps, len(knownGaps))
	}
}

// TestMaskReproducesProduction replays the read masks and update masks, of a plain document
// and of one holding a vector and BSON shapes. A mask is parsed (ParseFieldPath) and checked
// (ValidateMaskPath); an update is checked in Validate's order: CheckBSON on its fields, the
// mask, then Validate on the document ApplyMask makes.
func TestMaskReproducesProduction(t *testing.T) {
	for _, file := range []string{"values-mask.json", "values-mask-vector.json"} {
		replayMasks(t, file)
	}
}

func replayMasks(t *testing.T, file string) {
	var c struct {
		Document any `json:"document"`
		Reads    []struct {
			Paths  []string `json:"maskFieldPaths"`
			HTTP   int      `json:"http"`
			Error  *struct{ Message string }
			Fields any `json:"fields"`
		} `json:"reads"`
		Updates []struct {
			Description string   `json:"description"`
			Fields      any      `json:"fields"`
			Mask        []string `json:"mask"`
			Write       struct {
				HTTP  int `json:"http"`
				Error *struct{ Message string }
			} `json:"write"`
			After any `json:"after"`
		} `json:"updates"`
	}
	readCorpus(t, file, &c)
	doc := fieldsJSON(t, c.Document)
	parse := func(paths []string) ([]FieldPath, error) {
		var out []FieldPath
		for _, s := range paths {
			p, err := ParseFieldPath(s)
			if err == nil {
				err = ValidateMaskPath(p)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
		return out, nil
	}
	for _, r := range c.Reads {
		paths, err := parse(r.Paths)
		if r.HTTP != 200 {
			if err == nil || err.Error() != r.Error.Message {
				t.Errorf("%s: mask %q: %v, production %q", file, r.Paths, err, r.Error.Message)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: mask %q: production accepts, %v", file, r.Paths, err)
			continue
		}
		if got, want := Project(doc, paths), fieldsJSON(t, r.Fields); !Identical(got, want) {
			t.Errorf("%s: mask %q: Project = %v, production %v", file, r.Paths, got, want)
		}
	}
	for _, u := range c.Updates {
		update := fieldsJSON(t, u.Fields)
		err := CheckBSON(update)
		var paths []FieldPath
		if err == nil {
			paths, err = parse(u.Mask)
		}
		var got Value
		if err == nil {
			got = ApplyMask(doc, update, paths)
			err = Validate(got)
		}
		want := fieldsJSON(t, u.After)
		if u.Write.HTTP != 200 {
			if err == nil || err.Error() != u.Write.Error.Message {
				t.Errorf("%s: %s: %v, production %q", file, u.Description, err, u.Write.Error.Message)
			}
			if !Identical(doc, want) {
				t.Errorf("%s: %s: a refused update changed the document", file, u.Description)
			}
			continue
		}
		switch {
		case err != nil:
			t.Errorf("%s: %s: production accepts, %v", file, u.Description, err)
		case !Identical(got, want):
			t.Errorf("%s: %s: ApplyMask = %v, production %v", file, u.Description, got, want)
		}
	}
}

// TestBSONStoredAsProduction: CanonicalBSON turns each value sent into the value production
// stored (decimal strings in canonical form, extra keys dropped).
func TestBSONStoredAsProduction(t *testing.T) {
	var c orderCorpus
	readCorpus(t, "values-bson.json", &c)
	n := 0
	for _, x := range c.Values {
		if x.Write.HTTP != 200 {
			continue
		}
		sent, stored := fromJSON(t, x.Sent), fromJSON(t, x.Stored)
		if got := CanonicalBSON(sent); !Identical(got, stored) {
			t.Errorf("%s: CanonicalBSON(%v) = %v, production stored %v", x.ID, sent, got, stored)
		}
		if !Identical(CanonicalBSON(stored), stored) {
			t.Errorf("%s: the stored %v is not canonical", x.ID, stored)
		}
		n++
	}
	if n < 100 {
		t.Errorf("only %d stored values", n)
	}
}

// TestBSONSizeAndBudget checks Size against the sizes production gave for each BSON shape, and
// the index budget each spends: [x×k, shape, false] and [x×k, shape, true] sort apart at
// k = 1498 - size and tie at 1499 - size.
func TestBSONSizeAndBudget(t *testing.T) {
	var c struct {
		Size []struct {
			ID                  string `json:"id"`
			Value               any    `json:"value"`
			FillerBytes         []int  `json:"fillerBytes"`
			ProductionValueSize *int   `json:"productionValueSize"`
			ProductionDocSize   *int   `json:"productionDocumentSize"`
		} `json:"size"`
		Truncation []struct {
			ID          string   `json:"id"`
			PrefixBytes int      `json:"prefixBytes"`
			Shape       any      `json:"shape"`
			ClassOfA    []string `json:"classOfA"`
		} `json:"truncation"`
	}
	readCorpus(t, "values-bson.json", &c)
	for _, x := range c.Size {
		if x.ProductionValueSize == nil {
			t.Errorf("%s: production gave no size", x.ID)
			continue
		}
		v := fromJSON(t, x.Value)
		if got := Size(v); got != *x.ProductionValueSize {
			t.Errorf("%s: Size(%v) = %d, production %d", x.ID, v, got, *x.ProductionValueSize)
		}
		doc := Map(Field{"s1", String(strings.Repeat("x", x.FillerBytes[0]))},
			Field{"s2", String(strings.Repeat("y", x.FillerBytes[1]))}, Field{"t", v})
		name := "projects/p/databases/(default)/documents/aruma-ref-bson-size/" + strings.TrimPrefix(x.ID, "size.")
		if got := DocumentSize(name, doc); got != *x.ProductionDocSize {
			t.Errorf("%s: DocumentSize = %d, production %d", x.ID, got, *x.ProductionDocSize)
		}
	}
	if len(c.Truncation) < 16 {
		t.Errorf("only %d budget pairs", len(c.Truncation))
	}
	for _, x := range c.Truncation {
		shape := fromJSON(t, x.Shape)
		prefix := String(strings.Repeat("x", x.PrefixBytes))
		a, b := Array(prefix, shape, Bool(false)), Array(prefix, shape, Bool(true))
		tie := slices.Contains(x.ClassOfA, x.ID+"-b")
		if got := Compare(a, b) == 0; got != tie {
			t.Errorf("%s: Compare ties %v, production %v", x.ID, got, tie)
		}
		if got := bytes.Equal(Key(a), Key(b)); got != tie {
			t.Errorf("%s: keys equal %v, production ties %v", x.ID, got, tie)
		}
	}
}
