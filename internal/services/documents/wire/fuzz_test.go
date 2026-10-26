package wire

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// FuzzParseName: the parsers never panic, accept only names they format back byte for byte, and
// agree with each other on a name's kind; every refusal is INVALID_ARGUMENT.
func FuzzParseName(f *testing.F) {
	for _, s := range []string{root, root + "/c", root + "/c/d", root + "/c//d", root + "/c/__id-1__", root + "/c/__x__",
		"projects/p/databases/Bad DB/documents/c/d", "c/a", "projects/p/databases/(default)", root + "/é/😀",
		root + "/c/d/" + strings.Repeat("x", 1501), "projects/p/databases/abcd/documents/./d"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, errAny := ParseName(s)
		d, errDoc := ParseDocumentName(s)
		p, errPar := ParseParentName(s)
		for _, err := range []error{errAny, errDoc, errPar} {
			if err != nil && From(err).Code != CodeInvalidArgument {
				t.Fatalf("%q: %v", s, err)
			}
		}
		if errAny == nil && n.String() != s {
			t.Fatalf("ParseName(%q) formats as %q", s, n.String())
		}
		if errDoc == nil && (errAny != nil || !d.IsDocument() || d.String() != s) {
			t.Fatalf("ParseDocumentName(%q) = %v, ParseName %v", s, d, errAny)
		}
		if errPar == nil && (errAny != nil || n.IsCollection() || p.String() != s) {
			t.Fatalf("ParseParentName(%q) = %v, ParseName %v", s, p, errAny)
		}
		if errAny == nil {
			if (errDoc == nil) != n.IsDocument() || (errPar == nil) != !n.IsCollection() {
				t.Fatalf("%q: kinds disagree: document %v parent %v, name %v", s, errDoc, errPar, n)
			}
		}
		if db, err := ParseDatabaseName(s); err == nil && db.Name() != s {
			t.Fatalf("ParseDatabaseName(%q) formats as %q", s, db.Name())
		}
	})
}

// valueGen builds a value from fuzz bytes: every kind, any double bits, nested arrays and maps,
// and valid references and timestamps (the wire refuses others, which FuzzParseName covers).
type valueGen struct{ b []byte }

func (g *valueGen) byte() byte {
	if len(g.b) == 0 {
		return 0
	}
	c := g.b[0]
	g.b = g.b[1:]
	return c
}

func (g *valueGen) uint64() uint64 {
	var buf [8]byte
	for i := range buf {
		buf[i] = g.byte()
	}
	return binary.LittleEndian.Uint64(buf[:])
}

func (g *valueGen) text() string {
	n := int(g.byte() % 12)
	var b []byte
	for range n {
		b = append(b, g.byte())
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

func (g *valueGen) id() string {
	alphabet := []rune("abcXYZ019-_.é😀")
	n := 1 + int(g.byte()%6)
	var b strings.Builder
	for range n {
		b.WriteRune(alphabet[int(g.byte())%len(alphabet)])
	}
	return b.String()
}

func (g *valueGen) value(depth int) value.Value {
	switch k := g.byte() % 12; {
	case k == 0:
		return value.Null()
	case k == 1:
		return value.Bool(g.byte()&1 == 1)
	case k == 2:
		return value.Int(int64(g.uint64()))
	case k == 3:
		return value.Double(math.Float64frombits(g.uint64()))
	case k == 4:
		const span = maxTimestampSeconds - minTimestampSeconds + 1
		return value.Timestamp(minTimestampSeconds+int64(g.uint64()%span), int32(g.uint64()%1_000_000_000))
	case k == 5:
		return value.String(g.text())
	case k == 6:
		return value.Bytes([]byte(g.text()))
	case k == 7:
		path := g.id() + "/" + g.id()
		if g.byte()&1 == 1 {
			path += "/" + g.id() + "/" + g.id()
		}
		name := root + "/" + path
		if _, err := ParseReference(name); err != nil {
			return value.String(name)
		}
		return value.Reference(name)
	case k == 8:
		return value.GeoPoint(math.Float64frombits(g.uint64()), math.Float64frombits(g.uint64()))
	case k == 9 && depth < 4:
		n := int(g.byte() % 4)
		els := make([]value.Value, n)
		for i := range els {
			els[i] = g.value(depth + 1)
		}
		return value.Array(els...)
	case k == 10 && depth < 4:
		n := int(g.byte() % 4)
		fs := make([]value.Field, n)
		for i := range fs {
			fs[i] = value.Field{Name: g.text(), Value: g.value(depth + 1)}
		}
		return value.Map(fs...)
	case k == 11:
		return value.Vector(math.Float64frombits(g.uint64()))
	}
	return value.Int(int64(g.byte()))
}

// FuzzValueRoundTrip: a value survives both wires unchanged, bit for bit: to the proto and
// through its binary form (gRPC), and through MarshalREST and UnmarshalREST (REST).
func FuzzValueRoundTrip(f *testing.F) {
	for _, seed := range [][]byte{
		{0}, {1, 1}, {2, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, {3, 0, 0, 0, 0, 0, 0, 0xf8, 0x7f},
		{3, 0, 0, 0, 0, 0, 0, 0, 0x80}, {4, 1, 2, 3, 4, 5, 6, 7, 8, 9}, {5, 3, 'a', 0xc3, 0xa9}, {7, 2, 1, 2, 3},
		{10, 3, 1, 'a', 2, 1, 1, 'b', 9, 1, 0, 0, 'c', 11}, {9, 2, 9, 1, 0, 6, 0},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		g := &valueGen{b: data}
		v := g.value(0)
		pv := ValueToProto(v)
		bin, err := proto.Marshal(pv)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		var back firestorepb.Value
		if err := proto.Unmarshal(bin, &back); err != nil {
			t.Fatal(err)
		}
		if got, err := ValueFromProto(&back); err != nil || !value.Identical(got, v) {
			t.Fatalf("gRPC: %v became %v (%v)", v, got, err)
		}
		js, err := MarshalREST(pv)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		var jback firestorepb.Value
		if err := UnmarshalREST(js, &jback); err != nil {
			t.Fatalf("REST %s: %v", js, err)
		}
		if got, err := ValueFromProto(&jback); err != nil || !value.Identical(got, v) {
			t.Fatalf("REST: %v became %v via %s (%v)", v, got, js, err)
		}
	})
}

// FuzzUnmarshalREST: the reader never panics, refuses only with INVALID_ARGUMENT, and where it and
// protojson both accept a body they agree.
func FuzzUnmarshalREST(f *testing.F) {
	for _, s := range []string{
		`{"writes":[{"update":{"name":"` + root + `/c/a","fields":{"a":{"integerValue":"1"},"b":{"doubleValue":"NaN"}}}}]}`,
		`{"writes":[{"delete":"x","currentDocument":{"updateTime":"2026-01-01T00:00:00.5+01:00"}}],"transaction":"dHhu"}`,
		`{"structuredQuery":{"from":[{"collectionId":"c"}],"limit":3,"orderBy":[{"field":{"fieldPath":"a"},"direction":"DESCENDING"}]}}`,
		`{"a":1}`, `[]`, `{"writes":null}`, `{"writes":[{"update":{"fields":{"m":{"mapValue":{"fields":{"x":{"bytesValue":"-_8"}}}}}}}]}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		for _, newMsg := range []func() proto.Message{
			func() proto.Message { return &firestorepb.CommitRequest{} },
			func() proto.Message { return &firestorepb.RunQueryRequest{} },
		} {
			got := newMsg()
			err := UnmarshalREST(body, got)
			if err != nil {
				if e := From(err); e.Code != CodeInvalidArgument {
					t.Fatalf("%q: %v", body, err)
				}
				continue
			}
			want := newMsg()
			if protojson.Unmarshal(body, want) == nil && !proto.Equal(got, want) {
				t.Fatalf("%q:\n got  %v\n want %v", body, got, want)
			}
		}
	})
}
