package wire

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// units is the length of s as the JS SDK measures it.
func units(s []byte) int { return len(utf16.Encode([]rune(string(s)))) }

func TestSplit(t *testing.T) {
	cases := []struct {
		name   string
		msg    string
		frames int // pieces, not counting the count frame
	}{
		{"empty", "", 1},
		{"short", `{"t":"d"}`, 1},
		{"exactly one frame", strings.Repeat("a", MaxFrameChars), 1},
		{"one over", strings.Repeat("a", MaxFrameChars+1), 2},
		{"three frames", strings.Repeat("a", 3*MaxFrameChars), 3},
		// 16384 Arabic letters are 32768 bytes but one frame of 16384 code units.
		{"arabic fits", strings.Repeat("م", MaxFrameChars), 1},
		{"arabic over", strings.Repeat("م", MaxFrameChars+1), 2},
		// An emoji is two code units: the pair must not straddle a frame boundary.
		{"emoji at the boundary", strings.Repeat("a", MaxFrameChars-1) + "😀" + "b", 2},
		{"emojis", strings.Repeat("😀", MaxFrameChars), 2},
		{"mixed", strings.Repeat("aم😀", 20000), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frames := Split([]byte(c.msg))
			pieces := frames
			if len(frames) > 1 {
				n, ok := frameCount(frames[0])
				if !ok || n != len(frames)-1 {
					t.Fatalf("count frame %q for %d pieces", frames[0], len(frames)-1)
				}
				pieces = frames[1:]
			}
			if c.frames != 0 && len(pieces) != c.frames {
				t.Errorf("%d pieces, want %d", len(pieces), c.frames)
			}
			var joined []byte
			for i, p := range pieces {
				if !utf8.Valid(p) {
					t.Errorf("piece %d is not valid UTF-8 on its own", i)
				}
				if u := units(p); u > MaxFrameChars || (len(pieces) > 1 && u == 0) {
					t.Errorf("piece %d has %d code units", i, u)
				}
				joined = append(joined, p...)
			}
			if string(joined) != c.msg {
				t.Error("pieces do not join to the message")
			}
			// The assembler reads back what Split wrote.
			a := Assembler{Max: 1 << 30}
			var got []byte
			for _, f := range frames {
				msg, err := a.Feed(f)
				if err != nil {
					t.Fatal(err)
				}
				if msg != nil {
					got = msg
				}
			}
			if c.msg != "" && string(got) != c.msg {
				t.Error("round trip through the assembler lost data")
			}
		})
	}
}

func TestSplitFillsFrames(t *testing.T) {
	// Every piece but the last is full, or one unit short where a pair would not fit.
	frames := Split([]byte(strings.Repeat("😀a", 30000)))
	for i, p := range frames[1 : len(frames)-1] {
		if u := units(p); u < MaxFrameChars-1 {
			t.Errorf("piece %d holds %d units, want a full frame", i, u)
		}
	}
}

func TestAssembler(t *testing.T) {
	a := Assembler{Max: 10}
	feed := func(f string) ([]byte, error) { return a.Feed([]byte(f)) }

	// Keepalives are ignored.
	for _, f := range []string{"0", "", "000"} {
		if msg, err := feed(f); msg != nil || err != nil {
			t.Errorf("keepalive %q: %q %v", f, msg, err)
		}
	}
	// A single frame is a message, even when short.
	if msg, _ := feed("{}"); string(msg) != "{}" {
		t.Errorf("short message: %q", msg)
	}
	// A frame of more than 6 characters is never a count.
	if msg, _ := feed("1234567"); string(msg) != "1234567" {
		t.Errorf("long digits: %q", msg)
	}
	// Count, then pieces.
	if msg, _ := feed("3"); msg != nil || !a.Pending() {
		t.Fatal("count frame should open a message")
	}
	feed(`{"a"`)
	feed(`:`)
	msg, err := feed(`1}`)
	if err != nil || string(msg) != `{"a":1}` || a.Pending() {
		t.Fatalf("reassembled %q %v", msg, err)
	}
	// Inside a message, "0" is data.
	feed("2")
	feed("0")
	if msg, _ := feed("0"); string(msg) != "00" {
		t.Errorf("zeros inside a message: %q", msg)
	}

	// Over the limit: the frames are consumed, nothing is kept, and the next message works.
	feed("3")
	feed("123456")
	feed("123456")
	if _, err := feed("1"); !errors.Is(err, ErrMessageTooBig) {
		t.Fatalf("over the limit: %v", err)
	}
	if msg, err := feed("ok"); string(msg) != "ok" || err != nil {
		t.Fatalf("after a dropped message: %q %v", msg, err)
	}
	if _, err := feed(strings.Repeat("x", 11)); !errors.Is(err, ErrMessageTooBig) {
		t.Fatalf("single frame over the limit: %v", err)
	}
}

type budget struct{ left, peak, held int }

func (b *budget) Take(n int) bool {
	if n > b.left {
		return false
	}
	b.left -= n
	b.held += n
	b.peak = max(b.peak, b.held)
	return true
}

func (b *budget) Give(n int) { b.left += n; b.held -= n }

func TestAssemblerBudget(t *testing.T) {
	b := &budget{left: 10}
	a := Assembler{Max: 100, Budget: b}
	feed := func(f string) ([]byte, error) { return a.Feed([]byte(f)) }

	feed("2")
	feed("12345")
	if msg, err := feed("678"); string(msg) != "12345678" || err != nil || b.held != 0 {
		t.Fatalf("within the budget: %q %v, %d held", msg, err, b.held)
	}
	// Past the shared budget the message is dropped like an oversized one, and what it held
	// is given back.
	feed("3")
	feed("123456")
	feed("123456")
	if _, err := feed("x"); !errors.Is(err, ErrMessageTooBig) || b.held != 0 {
		t.Fatalf("over the budget: %v, %d held", err, b.held)
	}
	// A connection that ends mid-message returns its bytes.
	feed("2")
	feed("1234")
	if b.held != 4 {
		t.Fatalf("held %d", b.held)
	}
	a.Reset()
	if b.held != 0 || a.Pending() {
		t.Fatalf("after reset: %d held", b.held)
	}
	if msg, _ := feed("ok"); string(msg) != "ok" {
		t.Fatalf("after reset: %q", msg)
	}
}

func TestSplitSharesMemory(t *testing.T) {
	msg := bytes.Repeat([]byte("a"), 2*MaxFrameChars)
	frames := Split(msg)
	if &frames[1][0] != &msg[0] {
		t.Error("pieces should be sub-slices of the message")
	}
}
