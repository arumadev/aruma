package wire

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestParseRange(t *testing.T) {
	const size = 64
	for _, c := range []struct {
		header string
		size   int64
		want   Range
		ok     bool
		err    bool
	}{
		{header: "", size: size},
		{header: "bytes=2-5", size: size, want: Range{2, 4}, ok: true}, // Pyric rangeGet: bytes 2-5/64
		{header: "bytes=0-0", size: size, want: Range{0, 1}, ok: true},
		{header: "bytes=10-", size: size, want: Range{10, 54}, ok: true},
		{header: "bytes=63-", size: size, want: Range{63, 1}, ok: true},
		{header: "bytes=-4", size: size, want: Range{60, 4}, ok: true},
		{header: "bytes=-64", size: size, want: Range{0, 64}, ok: true},
		{header: "bytes=-1000", size: size, want: Range{0, 64}, ok: true}, // a suffix past the start is the whole
		{header: "bytes=0-1000", size: size, want: Range{0, 64}, ok: true},
		{header: "bytes=0-99999999999999999999999", size: size, want: Range{0, 64}, ok: true},
		{header: "BYTES=1-2", size: size, want: Range{1, 2}, ok: true},
		{header: "bytes= 1-2 ", size: size, want: Range{1, 2}, ok: true},
		{header: "bytes=007-008", size: size, want: Range{7, 2}, ok: true},
		// Unsatisfiable: 416 with bytes */64.
		{header: "bytes=64-", size: size, err: true},
		{header: "bytes=64-70", size: size, err: true},
		{header: "bytes=99999999999999999999-", size: size, err: true},
		{header: "bytes=-0", size: size, err: true},
		// An empty object is a 200 whatever the range (E §4).
		{header: "bytes=0-1023", size: 0},
		{header: "bytes=5-", size: 0},
		{header: "bytes=-0", size: 0},
		// Several ranges: the whole object.
		{header: "bytes=0-1,4-5", size: size},
		{header: "bytes=64-,0-1", size: size},
		// Forms that are ignored.
		{header: "bytes=5-4", size: size},
		{header: "bytes=-", size: size},
		{header: "bytes=", size: size},
		{header: "bytes", size: size},
		{header: "items=0-1", size: size},
		{header: "bytes=a-b", size: size},
		{header: "bytes=+1-2", size: size},
		{header: "bytes=1 - 2", size: size},
		{header: "bytes=1-2-3", size: size},
		{header: "bytes=-1-2", size: size},
		{header: "bytes=0x1-2", size: size},
	} {
		got, ok, err := ParseRange(c.header, c.size)
		if c.err {
			if !errors.Is(err, ErrUnsatisfiable) || ok {
				t.Errorf("%q/%d: %+v %v %v, want ErrUnsatisfiable", c.header, c.size, got, ok, err)
			}
			continue
		}
		if err != nil || ok != c.ok || got != c.want {
			t.Errorf("%q/%d: %+v %v %v, want %+v %v", c.header, c.size, got, ok, err, c.want, c.ok)
		}
	}
}

func TestContentRangeHeaders(t *testing.T) {
	if got := (Range{2, 4}).ContentRange(64); got != "bytes 2-5/64" { // Pyric rangeGet
		t.Errorf("ContentRange = %q", got)
	}
	if got := UnsatisfiedContentRange(64); got != "bytes */64" {
		t.Errorf("UnsatisfiedContentRange = %q", got)
	}
}

// A header of millions of ranges costs one scan and no allocation: the comma alone decides.
func TestParseRangeManyParts(t *testing.T) {
	h := "bytes=" + strings.Repeat("0-0,", 2_500_000) + "0-0" // 10 MB, far over any server's header cap
	allocs := testing.AllocsPerRun(5, func() {
		if _, ok, err := ParseRange(h, 100); ok || err != nil {
			t.Fatal(ok, err)
		}
	})
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
	long := "bytes=" + strings.Repeat("9", 1_000_000) + "-"
	if _, ok, err := ParseRange(long, 100); ok || !errors.Is(err, ErrUnsatisfiable) {
		t.Errorf("a huge start: %v %v", ok, err)
	}
}

// Every Range a client sent and the emulator served with 206 parses to the range it served
// (sdk_read.json, admin_node.json, admin_go.json).
func TestGoldenRanges(t *testing.T) {
	n := 0
	for _, e := range exchanges(t) {
		rh := header(e.Request.Headers).Get("Range")
		cr := header(e.Response.Headers).Get("Content-Range")
		if rh == "" || e.Response.Status != http.StatusPartialContent || cr == "" {
			continue
		}
		_, total, _ := strings.Cut(cr, "/")
		size, err := strconv.ParseInt(total, 10, 64)
		if err != nil {
			t.Fatalf("%v: %q", e, cr)
		}
		r, ok, err := ParseRange(rh, size)
		if err != nil || !ok || r.ContentRange(size) != cr {
			t.Errorf("%v: %q on %d: %+v %v %v, the emulator served %q", e, rh, size, r, ok, err, cr)
		}
		if b, whole := e.Response.Body.bytes(); whole && int64(len(b)) != r.Length {
			t.Errorf("%v: %d bytes served, range of %d", e, len(b), r.Length)
		}
		n++
	}
	if n < 4 {
		t.Errorf("only %d ranges", n)
	}
}
