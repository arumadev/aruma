package wire

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	goldenStart = regexp.MustCompile(`pLPCommand\('start','([^']*)','([^']*)'\);\npRTLPCB\(0,\[(.*)\]\);\n$`)
	goldenBatch = regexp.MustCompile(`^pRTLPCB\((\d+),(.*)\);$`)
	goldenClose = regexp.MustCompile(`^pLPCommand\('close'(?:,(\d+))?\);$`)
	goldenPing  = regexp.MustCompile(`EnvSendPing\("([^"]+)"\)`)
)

// TestGoldenLPAnswers encodes every script and page the emulator answered over /.lp
// (golden lp_*.json) and gets the same bytes.
func TestGoldenLPAnswers(t *testing.T) {
	checked := map[string]int{}
	for _, g := range loadGolden(t) {
		if !strings.HasPrefix(g.Scenario, "lp_") {
			continue
		}
		var req string
		for _, f := range g.Frames {
			switch {
			case f.Dir == "http_req":
				req = f.Text
				continue
			case f.Dir != "http_resp" || f.Text == "" || f.Text == "<!-- -->":
				continue
			}
			u, err := url.Parse(strings.TrimPrefix(req, "GET "))
			if err != nil {
				t.Fatal(err)
			}
			q := u.Query()
			var ours string
			switch {
			case q.Get(LPStart) == "t":
				m := goldenStart.FindStringSubmatch(f.Text)
				if m == nil {
					t.Fatalf("%s: start answer %q", g.Scenario, f.Text)
				}
				ours = string(EncodeLPStart(q.Get(LPCallback), m[1], m[2], []byte(m[3])))
				checked["start"]++
			case q.Get(LPDisconnectFrame) == "t":
				ours = string(EncodeLPDisconnectFrame(goldenPing.FindStringSubmatch(f.Text)[1]))
				checked["dframe"]++
			default:
				for _, line := range strings.SplitAfter(f.Text, "\n") {
					if line == "" {
						continue
					}
					trimmed := strings.TrimSuffix(line, "\n")
					if m := goldenBatch.FindStringSubmatch(trimmed); m != nil {
						var raw []json.RawMessage
						if err := json.Unmarshal([]byte(m[2]), &raw); err != nil {
							t.Fatalf("%s: %q: %v", g.Scenario, line, err)
						}
						n, _ := strconv.ParseInt(m[1], 10, 64)
						var msgs [][]byte
						for _, r := range raw {
							msgs = append(msgs, r)
						}
						ours += string(EncodeLPBatch(n, msgs))
						checked["batch"]++
					} else if m := goldenClose.FindStringSubmatch(trimmed); m != nil {
						after := int64(-1)
						if m[1] != "" {
							after, _ = strconv.ParseInt(m[1], 10, 64)
						}
						ours += string(EncodeLPClose(after))
						checked["close"]++
					} else {
						t.Errorf("%s: unknown line %q", g.Scenario, line)
					}
				}
			}
			if ours != f.Text {
				t.Errorf("%s: answer to %s\n emulator %q\n ours     %q", g.Scenario, req, f.Text, ours)
			}
		}
	}
	for _, k := range []string{"start", "dframe", "batch", "close"} {
		if checked[k] == 0 {
			t.Errorf("no golden %s answer was checked", k)
		}
	}
}

// TestGoldenLPSegments decodes the segments of every request the SDK-style client sent
// (golden lp_*.json) back into the messages it sent, in order.
func TestGoldenLPSegments(t *testing.T) {
	decoded := 0
	for _, g := range loadGolden(t) {
		if !strings.HasPrefix(g.Scenario, "lp_") {
			continue
		}
		rcv := map[string]*LPReceiver{}
		want := map[string][]goldenFrame{}
		for _, f := range g.Frames {
			switch f.Dir {
			case "send":
				if rcv[f.Conn] != nil { // a long-poll client
					want[f.Conn] = append(want[f.Conn], f)
				}
			case "http_req":
				u, _ := url.Parse(strings.TrimPrefix(f.Text, "GET "))
				q := u.Query()
				if q.Get(LPStart) == "t" {
					rcv[f.Conn] = &LPReceiver{Max: 1 << 20}
				}
				segs, err := ParseLPSegments(q)
				if err != nil {
					t.Fatalf("%s: %s: %v", g.Scenario, f.Text, err)
				}
				r := rcv[f.Conn]
				for _, s := range segs {
					if err := r.Add(s); err != nil {
						t.Fatal(err)
					}
				}
				for {
					msg, err := r.Next()
					if err != nil {
						t.Fatalf("%s: %v", g.Scenario, err)
					}
					if msg == nil {
						break
					}
					if len(want[f.Conn]) == 0 {
						t.Fatalf("%s: %s: decoded %s, sent nothing", g.Scenario, f.Conn, msg)
					}
					w := want[f.Conn][0]
					want[f.Conn] = want[f.Conn][1:]
					sum := sha256.Sum256(msg)
					// A long message is stored by its sha256 (its len counts UTF-16 units).
					if w.Text != string(msg) && w.SHA256 != hex.EncodeToString(sum[:]) {
						t.Errorf("%s: decoded %.80s, sent %.80s", g.Scenario, msg, w.Text)
					}
					decoded++
				}
			}
		}
	}
	if decoded < 10 {
		t.Errorf("decoded only %d messages", decoded)
	}
}

func TestLPReceiver(t *testing.T) {
	enc := func(s string) string {
		return strings.ReplaceAll(base64.URLEncoding.EncodeToString([]byte(s)), "=", ".")
	}
	r := &LPReceiver{Max: 100}
	msg := enc(`{"t":"c","d":{"t":"p","d":{}}}`)
	// Out of order: the second piece first.
	r.Add(LPSegment{Num: 1, Total: 2, Data: msg[10:]})
	if m, err := r.Next(); m != nil || err != nil {
		t.Fatalf("early: %s %v", m, err)
	}
	r.Add(LPSegment{Num: 0, Total: 2, Data: msg[:10]})
	r.Add(LPSegment{Num: 0, Total: 2, Data: "ignored: taken already"})
	if m, err := r.Next(); string(m) != `{"t":"c","d":{"t":"p","d":{}}}` || err != nil {
		t.Fatalf("whole: %s %v", m, err)
	}
	// A segment already used is ignored; one far ahead ends the session.
	if err := r.Add(LPSegment{Num: 0, Total: 1, Data: "x"}); err != nil {
		t.Error(err)
	}
	if err := r.Add(LPSegment{Num: 1 << 20, Total: 1, Data: "x"}); !errors.Is(err, ErrBadSegment) {
		t.Errorf("far ahead: %v", err)
	}
	// Too big, and not base64: skipped with an error, and the next message still comes.
	r.Add(LPSegment{Num: 2, Total: 1, Data: enc(strings.Repeat("x", 101))})
	r.Add(LPSegment{Num: 3, Total: 1, Data: "!!!"})
	r.Add(LPSegment{Num: 4, Total: 1, Data: enc("{}")})
	for _, want := range []error{ErrMessageTooBig, ErrMalformed, nil} {
		if _, err := r.Next(); !errors.Is(err, want) {
			t.Errorf("got %v, want %v", err, want)
		}
	}

	if _, err := ParseLPSegments(url.Values{"seg0": {"x"}, "ts0": {"1"}}); !errors.Is(err, ErrBadSegment) {
		t.Errorf("bad seg: %v", err)
	}
	if segs, err := ParseLPSegments(url.Values{"seg0": {"5"}, "ts0": {"2"}, "d0": {"ab"}, "seg2": {"7"}}); err != nil || len(segs) != 1 || segs[0] != (LPSegment{5, 2, "ab"}) {
		t.Errorf("segments: %v %v", segs, err)
	}
}

func TestLPEncoding(t *testing.T) {
	if got := string(EncodeLPBatch(3, [][]byte{[]byte(`"a` + " " + `b"`), []byte(`1`)})); got != `pRTLPCB(3,["a b",1]);`+"\n" {
		t.Errorf("batch: %q", got)
	}
	if got := string(EncodeLPStart("", "1", "pw", []byte(`{}`))); got != "pLPCommand('start','1','pw');\npRTLPCB(0,[{}]);\n" {
		t.Errorf("start without cb: %q", got)
	}
	for cb, ok := range map[string]bool{"1": true, "12345678901234567890": true, "": false, "1a": false, `1"]`: false, "123456789012345678901": false} {
		if ValidLPCallback(cb) != ok {
			t.Errorf("ValidLPCallback(%q) != %v", cb, ok)
		}
	}
}
