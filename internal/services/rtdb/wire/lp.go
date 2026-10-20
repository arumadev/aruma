package wire

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Long polling (/.lp) carries the same messages as the WebSocket in another envelope, for
// browsers that cannot use a WebSocket (BrowserPollConnection.ts). The client loads each
// request as a script in a hidden iframe; the server answers with JavaScript that calls the
// callbacks the SDK installed on the parent window:
//
//   - pLPCommand('start', id, pw): the session the client polls with, sent once;
//   - pRTLPCB(n, [messages]): the n-th batch of messages, numbered from 0 per session, which
//     the client processes in order whatever order the responses arrive in;
//   - pLPCommand('close'[, n]): stop polling, after batch n when it is given.
//
// The client sends messages in the URLs of its requests: the JSON in web-safe base64, cut
// into numbered segments, seg<i>, ts<i> and d<i> for the i-th segment of a request.
// Golden lp_*.json record the emulator's bodies byte for byte.

// LP query parameters.
const (
	LPStart      = "start"   // start=t opens a session
	LPCallback   = "cb"      // the id of the client's callbacks, on start
	LPID         = "id"      // the session, on every later request
	LPPassword   = "pw"      // its password
	LPSerial     = "ser"     // the request's serial number
	LPDisconnect = "disconn" // disconn=t ends the session (sent when the page unloads)
	// LPDisconnectFrame, dframe=t, asks for the page the client keeps in a hidden iframe once
	// the session started, which sends disconn when the page unloads.
	LPDisconnectFrame = "dframe"
)

// LPContentType is the content type of every script answer.
const LPContentType = "application/javascript; charset=utf-8"

// lpPreamble defines the two functions an answer calls, forwarding to the callbacks the SDK
// registered on the parent window under its callback id. It precedes the start answer, as
// on the emulator; later answers run in the same iframe and find the functions there.
func lpPreamble(cb string) string {
	return "\nfunction pLPCommand(c, a1, a2, a3, a4) {\n" +
		`parent.window["pLPCommand` + cb + `"] && parent.window["pLPCommand` + cb + `"](c, a1, a2, a3, a4);` + "\n}\n" +
		"function pRTLPCB(pN, data) {\n" +
		`parent.window["pRTLPCB` + cb + `"] && parent.window["pRTLPCB` + cb + `"](pN, data);` + "\n}\n" +
		"         "
}

// ValidLPCallback reports whether cb can be the callback id of a start request. It goes into
// JavaScript source, so only the digits the SDK sends (LUIDGenerator) are accepted.
func ValidLPCallback(cb string) bool {
	if cb == "" || len(cb) > 20 {
		return false
	}
	for i := 0; i < len(cb); i++ {
		if cb[i] < '0' || cb[i] > '9' {
			return false
		}
	}
	return true
}

// EncodeLPStart answers a start request: the session id and password, and batch 0, which
// holds hello. cb is the client's callback id, already checked with ValidLPCallback; without
// one the answer calls the functions directly, as the emulator's does.
func EncodeLPStart(cb, id, pw string, hello []byte) []byte {
	var b bytes.Buffer
	if cb != "" {
		b.WriteString(lpPreamble(cb))
	}
	b.WriteString("pLPCommand('start','" + id + "','" + pw + "');\n")
	b.Write(EncodeLPBatch(0, [][]byte{hello}))
	return b.Bytes()
}

// EncodeLPBatch is pRTLPCB(n,[msgs...]);. The messages are JSON, which is a JavaScript
// expression once U+2028 and U+2029 are escaped (older engines end a line on them).
func EncodeLPBatch(n int64, msgs [][]byte) []byte {
	var b bytes.Buffer
	b.WriteString("pRTLPCB(" + strconv.FormatInt(n, 10) + ",[")
	for i, m := range msgs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(escapeLineSeparators(m))
	}
	b.WriteString("]);\n")
	return b.Bytes()
}

func escapeLineSeparators(m []byte) []byte {
	if !bytes.Contains(m, []byte(" ")) && !bytes.Contains(m, []byte(" ")) {
		return m
	}
	m = bytes.ReplaceAll(m, []byte(" "), []byte(` `))
	return bytes.ReplaceAll(m, []byte(" "), []byte(` `))
}

// EncodeLPClose tells the client to stop polling once it has processed batch after, or at
// once when after is negative. The client then reconnects, or carries on over the WebSocket
// it upgraded to.
func EncodeLPClose(after int64) []byte {
	if after < 0 {
		return []byte("pLPCommand('close');\n")
	}
	return []byte("pLPCommand('close'," + strconv.FormatInt(after, 10) + ");\n")
}

// EncodeLPDisconnectFrame is the dframe page. On unload it fetches disconnURL synchronously,
// so the server learns at once that the page is gone and runs the onDisconnect writes.
func EncodeLPDisconnectFrame(disconnURL string) []byte {
	return []byte("<html><body><script>\n" +
		"function EnvSendPing(destURL) {\n" +
		"try{\n" +
		"var xhr=new XMLHttpRequest();\n" +
		"xhr.open(\"GET\", destURL, false);\n" +
		"xhr.send(null);\n" +
		"} catch (e) { }\n" +
		"}\n" +
		"function EnvDisconnect() {\n" +
		"EnvSendPing(\"" + disconnURL + "\");\n" +
		"}\n" +
		"if(window.addEventListener)\n" +
		"window.addEventListener('unload',EnvDisconnect,false);\n" +
		"else if(window.attachEvent)\n" +
		"window.attachEvent('onunload',EnvDisconnect);\n" +
		"</script></body></html>")
}

// LPSegment is one piece of a client message: segment Num of the session's sequence, one of
// Total that make up its message, holding Data, a piece of the message's web-safe base64.
type LPSegment struct {
	Num   int64
	Total int64
	Data  string
}

// ErrBadSegment reports segment parameters the SDK would not send.
var ErrBadSegment = errors.New("wire: invalid long-poll segment")

// ParseLPSegments reads the segments of one request, seg0/ts0/d0, seg1/ts1/d1, ..., up to
// the first index that is missing.
func ParseLPSegments(q url.Values) ([]LPSegment, error) {
	var segs []LPSegment
	for i := 0; ; i++ {
		n := strconv.Itoa(i)
		num, ok := q["seg"+n]
		if !ok {
			return segs, nil
		}
		s, err1 := strconv.ParseInt(num[0], 10, 64)
		t, err2 := strconv.ParseInt(q.Get("ts"+n), 10, 64)
		if err1 != nil || err2 != nil || s < 0 || t < 1 {
			return nil, ErrBadSegment
		}
		segs = append(segs, LPSegment{Num: s, Total: t, Data: q.Get("d" + n)})
	}
}

// LPReceiver puts the client's segments back together into messages, in the client's order.
// A client keeps up to two requests outstanding, so segments can arrive out of order; the
// receiver holds them until the gap fills. The zero value is not usable; set Max.
type LPReceiver struct {
	// Max caps one message, in bytes once decoded. It may change between messages.
	Max int

	next int64 // the first segment of the next message
	segs map[int64]LPSegment
	held int // base64 bytes held
}

// lpWindow is how far ahead of the next expected segment a segment may be. The SDK has at
// most two requests outstanding, each with at most 1870 characters of segments.
const lpWindow = 4096

// Add takes a segment. A segment already taken is ignored; one too far ahead, or more held
// than a message may be, is ErrBadSegment and ends the session.
func (r *LPReceiver) Add(s LPSegment) error {
	if s.Num < r.next {
		return nil
	}
	if s.Num >= r.next+lpWindow || s.Total > lpWindow {
		return ErrBadSegment
	}
	if _, dup := r.segs[s.Num]; dup {
		return nil
	}
	if r.held+len(s.Data) > 2*r.Max+MaxFrameChars {
		return ErrBadSegment
	}
	if r.segs == nil {
		r.segs = map[int64]LPSegment{}
	}
	r.segs[s.Num] = s
	r.held += len(s.Data)
	return nil
}

// Next returns the next complete message, or nil when it has not all arrived. A message
// that is not base64 is ErrMalformed and one over Max is ErrMessageTooBig; either is skipped,
// so the caller answers it as it would on a WebSocket and calls Next again.
func (r *LPReceiver) Next() ([]byte, error) {
	first, ok := r.segs[r.next]
	if !ok {
		return nil, nil
	}
	var b strings.Builder
	for i := int64(0); i < first.Total; i++ {
		s, ok := r.segs[r.next+i]
		if !ok {
			return nil, nil
		}
		b.WriteString(s.Data)
	}
	for i := int64(0); i < first.Total; i++ {
		r.held -= len(r.segs[r.next+i].Data)
		delete(r.segs, r.next+i)
	}
	r.next += first.Total
	enc := b.String()
	if base64.URLEncoding.DecodedLen(len(enc)) > r.Max+3 {
		return nil, ErrMessageTooBig
	}
	msg, err := base64.URLEncoding.DecodeString(strings.ReplaceAll(enc, ".", "="))
	if err != nil {
		return nil, ErrMalformed
	}
	if len(msg) > r.Max {
		return nil, ErrMessageTooBig
	}
	return msg, nil
}
