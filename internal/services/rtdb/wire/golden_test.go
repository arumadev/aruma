package wire

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The golden files are frames the official emulator 4.11.2 exchanged with a raw client on
// 2026-10-20 (testdata/golden/README.md). These tests hold the package to them: every frame
// the client sent decodes, and every frame the emulator sent is one this package can encode,
// field for field. The acceptance tests replay the same scenarios against Aruma itself.

type goldenFrame struct {
	Conn   string `json:"conn"`
	Dir    string `json:"dir"` // send, recv, close, http
	Text   string `json:"text"`
	Len    int    `json:"len"`
	SHA256 string `json:"sha256"`
	Code   int    `json:"code"`
}

type golden struct {
	Scenario string        `json:"scenario"`
	Source   string        `json:"source"`
	Emulator string        `json:"emulator"`
	Frames   []goldenFrame `json:"frames"`
}

func loadGolden(t *testing.T) []golden {
	t.Helper()
	files, err := filepath.Glob("testdata/golden/*.json")
	if err != nil || len(files) < 10 {
		t.Fatalf("golden files: %v %d", err, len(files))
	}
	var out []golden
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var g golden
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if g.Source != "emulator" || g.Emulator != "firebase-database-emulator-v4.11.2" {
			t.Errorf("%s: provenance %q %q", f, g.Source, g.Emulator)
		}
		out = append(out, g)
	}
	return out
}

func TestGoldenChecksums(t *testing.T) {
	f, err := os.Open("testdata/golden/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	listed := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		sum, name, ok := strings.Cut(sc.Text(), "  ")
		if !ok {
			t.Fatalf("bad line %q", sc.Text())
		}
		b, err := os.ReadFile(filepath.Join("testdata/golden", name))
		if err != nil {
			t.Fatal(err)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s changed since it was captured; recapture it instead of editing it", name)
		}
		listed[name] = true
	}
	all, _ := filepath.Glob("testdata/golden/*.json")
	for _, f := range all {
		if !listed[filepath.Base(f)] {
			t.Errorf("%s is not in SHA256SUMS", f)
		}
	}
}

func TestGoldenClientFrames(t *testing.T) {
	for _, g := range loadGolden(t) {
		for i, f := range g.Frames {
			if f.Dir != "send" || f.Text == "" {
				continue
			}
			if _, ok := frameCount([]byte(f.Text)); ok && len(f.Text) <= maxCountChars {
				continue // a count frame
			}
			in, err := Parse([]byte(f.Text))
			if err != nil {
				// The emulator answered these with its generic error frame too.
				if g.Scenario != "unknown_action" {
					t.Errorf("%s: %s: %v", g.Scenario, f.Text, err)
				}
				continue
			}
			if in.Request == nil {
				continue
			}
			var body any
			switch in.Request.Action {
			case ActionPut, ActionMerge, ActionOnDisconnPut, ActionOnDisconnMerge:
				body = &WriteBody{}
			case ActionListen, ActionUnlisten:
				body = &ListenBody{}
			case ActionGet:
				body = &GetBody{}
			case ActionOnDisconnClear:
				body = &PathBody{}
			default:
				continue
			}
			err = DecodeBody(in.Request, body)
			// A body this package cannot decode is one the emulator could not either; the
			// emulator also refuses some that decode, on their data, which core checks.
			noBody := in.Request.Body == nil
			if noBody != (err != nil) && !(err != nil && answeredWithError(g.Frames[i+1:], f.Conn)) {
				t.Errorf("%s: %s: DecodeBody = %v", g.Scenario, f.Text, err)
			}
		}
	}
}

// answeredWithError reports whether the emulator's next frame to conn, after a request, was
// its generic error frame: a body it could not decode either (golden listen_errors.json).
func answeredWithError(rest []goldenFrame, conn string) bool {
	for _, f := range rest {
		if f.Conn == conn && f.Dir == "recv" {
			return strings.Contains(f.Text, `"t":"e"`)
		}
	}
	return false
}

func TestGoldenServerFrames(t *testing.T) {
	seen := map[string]int{}
	for _, g := range loadGolden(t) {
		for i, f := range g.Frames {
			if f.Dir != "recv" || f.Text == "" {
				continue
			}
			if _, ok := frameCount([]byte(f.Text)); ok {
				continue
			}
			kind, ours := reencode(t, f.Text)
			seen[kind]++
			if !sameJSON(t, []byte(f.Text), ours) {
				t.Errorf("%s frame %d (%s):\n emulator %s\n ours     %s", g.Scenario, i, kind, f.Text, ours)
			}
		}
	}
	for _, kind := range []string{"hello", "pong", "error", "response", "push d", "push m", "push c"} {
		if seen[kind] == 0 {
			t.Errorf("no golden %s frame was checked", kind)
		}
	}
}

// reencode decodes a frame the emulator sent and encodes the same content with this package.
func reencode(t *testing.T, text string) (string, []byte) {
	t.Helper()
	var env struct {
		T string          `json:"t"`
		D json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	if env.T == TypeControl {
		var c struct {
			T string          `json:"t"`
			D json.RawMessage `json:"d"`
		}
		json.Unmarshal(env.D, &c)
		switch c.T {
		case ControlHello:
			var h Hello
			if err := json.Unmarshal(c.D, &h); err != nil || h.Timestamp == 0 || len(h.Session) != 32 {
				t.Errorf("hello %s: %+v %v", c.D, h, err)
			}
			return "hello", EncodeHello(h)
		case ControlPong:
			return "pong", EncodePong()
		case ControlSwitchAck:
			return "switch ack", EncodeSwitchAck()
		case ControlEndTransmission:
			return "end transmission", EncodeEndTransmission()
		case ControlReset:
			var host string
			json.Unmarshal(c.D, &host)
			return "reset", EncodeReset(host)
		case ControlError:
			var msg string
			json.Unmarshal(c.D, &msg)
			return "error", EncodeError(msg)
		}
		t.Fatalf("unknown control %s", text)
	}
	var d struct {
		R *int64          `json:"r"`
		A string          `json:"a"`
		B json.RawMessage `json:"b"`
	}
	json.Unmarshal(env.D, &d)
	if d.R != nil {
		var r struct {
			S string          `json:"s"`
			D json.RawMessage `json:"d"`
		}
		json.Unmarshal(d.B, &r)
		return "response", EncodeResponse(*d.R, Reply{Status: r.S, Data: r.D})
	}
	var p struct {
		P string          `json:"p"`
		D json.RawMessage `json:"d"`
		T *int64          `json:"t"`
	}
	json.Unmarshal(d.B, &p)
	switch d.A {
	case PushData:
		return "push d", EncodeDataPush(p.P, p.D, p.T)
	case PushMerge:
		return "push m", EncodeMergePush(p.P, p.D, p.T)
	case PushListenRevoked:
		var c struct {
			Q []Query `json:"q"`
		}
		json.Unmarshal(d.B, &c)
		if len(c.Q) > 1 {
			t.Errorf("revoke with %d queries: %s", len(c.Q), text)
		}
		var q *Query
		if len(c.Q) == 1 {
			q = &c.Q[0]
		}
		return "push c", EncodeListenRevoked(p.P, q)
	}
	t.Fatalf("unknown push %s", text)
	return "", nil
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(x, y)
}

func TestGoldenMultiFrame(t *testing.T) {
	// The emulator split a 60 KB message into a count frame and 16384-character pieces
	// (golden/limits.json); Split does the same with a message of that length.
	checked := 0
	for _, g := range loadGolden(t) {
		for i := 0; i < len(g.Frames); i++ {
			f := g.Frames[i]
			n, err := strconv.Atoi(f.Text)
			if err != nil || n < 2 || f.Dir == "close" {
				continue
			}
			var lens []int
			total := 0
			for _, piece := range g.Frames[i+1 : i+1+n] {
				if piece.Conn != f.Conn || piece.Dir != f.Dir || piece.Len == 0 {
					t.Fatalf("%s: frames after the count are not its pieces", g.Scenario)
				}
				lens = append(lens, piece.Len)
				total += piece.Len
			}
			frames := Split(bytes.Repeat([]byte("x"), total))
			if string(frames[0]) != f.Text {
				t.Errorf("%s: count %s, ours %s", g.Scenario, f.Text, frames[0])
			}
			for j, p := range frames[1:] {
				if len(p) != lens[j] {
					t.Errorf("%s: piece %d of %d chars, ours %d", g.Scenario, j, lens[j], len(p))
				}
			}
			// And the assembler takes the client's pieces back.
			a := Assembler{Max: 16 << 20}
			var msg []byte
			for _, p := range frames {
				if msg, err = a.Feed(p); err != nil {
					t.Fatal(err)
				}
			}
			if len(msg) != total {
				t.Errorf("%s: reassembled %d of %d", g.Scenario, len(msg), total)
			}
			checked++
		}
	}
	if checked < 2 {
		t.Errorf("checked %d multi-frame messages, want the sent and the received one", checked)
	}
}
