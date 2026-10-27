package webchannel

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// The wire format of WebChannel VER=8, as closure-library's goog.labs.net.webChannel reads and
// writes it (WebChannelBase, ChannelRequest, WireV8).
//
// The server answers each request with chunks: the chunk's length in UTF-16 code units, which is
// how ChannelRequest.getNextChunk_ counts a JavaScript string, a newline, then the chunk. A chunk
// on the backchannel, or the handshake's, is a JSON array of arrays [id, payload]; a forward
// channel's is [backchannelPresent, lastArrayId, outstandingBytes] (handlePostResponse_).

// chunk appends one chunk of text to dst.
func chunk(dst, text []byte) []byte {
	dst = strconv.AppendInt(dst, int64(utf16Len(text)), 10)
	dst = append(dst, '\n')
	return append(dst, text...)
}

// utf16Len is the length of b decoded as UTF-8, in UTF-16 code units: a rune past the BMP is two.
// An invalid byte decodes as one U+FFFD, as a browser decodes it.
func utf16Len(b []byte) int {
	n := 0
	for _, r := range string(b) {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	return n
}

// array is one array of the backchannel: its id and its payload, a JSON array.
type array struct {
	id      int
	payload []byte
}

// arrays is the JSON of a backchannel chunk: [[id,payload],…].
func arrays(as []array) []byte {
	var b []byte
	b = append(b, '[')
	for i, a := range as {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '[')
		b = strconv.AppendInt(b, int64(a.id), 10)
		b = append(b, ',')
		b = append(b, a.payload...)
		b = append(b, ']')
	}
	return append(b, ']')
}

// The payloads the server sends besides messages: the handshake's, which carries the session id,
// no host prefix, the channel version and the server's (WebChannelBase.onInput_), and the
// keep-alive.
var noop = []byte(`["noop"]`)

// message is a message's payload, as the client's MESSAGE event takes it: data[0] is the message.
// Production pretty-prints it, two spaces to a level, and ends the array on a line of its own
// (f6-webchannel-listen.json).
func message(msg []byte) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	if json.Indent(&b, msg, "", "  ") != nil {
		b.Truncate(1)
		b.Write(msg)
	}
	b.WriteString("\n]")
	return b.Bytes()
}

// closing is the payload of a stream's status, and the close that follows it, as production ends a
// stream (f6-webchannel-write.json): {"__sm__":{"status":[[{"error":{…}}]]}}, compact; the
// transport takes __sm__'s value as the message's data, whose [0][0].error the SDK reads as the
// stream's status (webchannel_connection.ts).
func closing(restBody []byte) [][]byte {
	sm := make([]byte, 0, len(restBody)+24)
	sm = append(sm, `{"__sm__":{"status":[[`...)
	sm = append(sm, restBody...)
	sm = append(sm, "]]}}"...)
	return [][]byte{sm, []byte(`["close"]`)}
}

// forward is a forward channel request's body: application/x-www-form-urlencoded, as
// WireV8.encodeMessageQueue writes it, count=N&ofs=O&req0___data__=…&req1___data__=…, each map
// one message in proto3 JSON (sendRawJson). The handshake's body starts with headers=, the
// client's headers encoded as "Name:value\r\n" lines (encodeInitMessageHeaders).
type forward struct {
	headers http.Header
	maps    []fmap
}

// fmap is one map of the forward channel: its id, ofs + its index, and its message.
type fmap struct {
	id   int
	data []byte
}

var errBadData = errors.New("bad data")

// parseForward reads a forward channel body of at most maxMaps maps.
func parseForward(body []byte, maxMaps int) (forward, error) {
	var f forward
	q, err := url.ParseQuery(string(body))
	if err != nil {
		return f, errBadData
	}
	if hs, ok := q["headers"]; ok {
		if len(hs) != 1 {
			return f, errBadData
		}
		f.headers = parseHeaders(hs[0])
	}
	if !q.Has("count") {
		if len(body) == 0 || f.headers != nil && len(q) == 1 {
			return f, nil // a handshake with no message yet
		}
		return f, errBadData
	}
	count, err := strconv.Atoi(q.Get("count"))
	if err != nil || count < 0 || count > maxMaps {
		return f, errBadData
	}
	ofs := 0
	if count > 0 {
		if ofs, err = strconv.Atoi(q.Get("ofs")); err != nil || ofs < 0 || ofs > maxMapID {
			return f, errBadData
		}
	}
	// A map's key is reqN, its id ofs + N. The keys need not run from 0: the maps of a retried
	// request are sent again with the new ones, whose ids need not follow them, and ofs is then
	// some id at most 100 below the lowest (WireV8.encodeMessageQueue).
	for key, data := range q {
		rest, ok := strings.CutPrefix(key, "req")
		if !ok {
			continue
		}
		n, field, _ := strings.Cut(rest, "_")
		i, err := strconv.Atoi(n)
		if err != nil || i < 0 || i > maxMapKey || field != "__data__" || len(data) != 1 {
			// A map that is not raw JSON: the SDK always sends raw JSON (sendRawJson), and a map
			// the client failed to encode comes as type=_badmap.
			return f, errBadData
		}
		f.maps = append(f.maps, fmap{id: ofs + i, data: []byte(data[0])})
	}
	if len(f.maps) != count {
		return f, errBadData
	}
	slices.SortFunc(f.maps, func(a, b fmap) int { return cmp.Compare(a.id, b.id) })
	return f, nil
}

// maxMapKey and maxMapID bound a map's key and id, far past what a client sends, so that ofs + N
// cannot overflow.
const (
	maxMapKey = 1 << 20
	maxMapID  = 1 << 40
)

// parseHeaders reads the "Name:value\r\n" lines of a headers= field
// (goog.net.rpc.HttpCors.generateHttpHeadersOverwriteParam).
func parseHeaders(s string) http.Header {
	h := http.Header{}
	for line := range strings.SplitSeq(s, "\r\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || name == "" {
			continue
		}
		h.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	return h
}
