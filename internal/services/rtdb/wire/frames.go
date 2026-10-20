package wire

import (
	"errors"
	"strconv"
	"unicode/utf8"
)

// MaxFrameChars is the largest frame, in UTF-16 code units, that the SDKs send and that the
// server sends back: 16384 (JS WebSocketConnection.ts:44, Android WebsocketConnection.java:39,
// iOS FConstants.m:107). A longer message travels as a frame holding the decimal number of
// frames, followed by that many pieces (WebSocketConnection.ts:292-355).
const MaxFrameChars = 16384

// maxCountChars is the longest frame a receiver reads as a frame count; anything longer is a
// message (WebSocketConnection.ts extractFrameCount_).
const maxCountChars = 6

// Split returns the frames that carry msg: msg alone when it fits in one frame, else the count
// frame and the pieces. Pieces are measured in UTF-16 code units, as the JS SDK measures its
// strings, and end on character boundaries: a WebSocket text frame must be valid UTF-8 on its
// own, and a browser closes the connection when it is not. The frames share msg's memory.
func Split(msg []byte) [][]byte {
	if len(msg) <= MaxFrameChars {
		// A byte is never fewer than one code unit, so a short message is one frame.
		return [][]byte{msg}
	}
	var pieces [][]byte
	start, units := 0, 0
	for i := 0; i < len(msg); {
		r, size := utf8.DecodeRune(msg[i:])
		n := 1
		if r > 0xFFFF {
			n = 2 // a surrogate pair in UTF-16
		}
		if units+n > MaxFrameChars {
			pieces = append(pieces, msg[start:i])
			start, units = i, 0
		}
		units += n
		i += size
	}
	pieces = append(pieces, msg[start:])
	if len(pieces) == 1 {
		return pieces
	}
	return append([][]byte{[]byte(strconv.Itoa(len(pieces)))}, pieces...)
}

// ErrMessageTooBig reports a message whose frames add up to more than the assembler's limit.
// The assembler has skipped the rest of its frames, so the connection can continue.
var ErrMessageTooBig = errors.New("wire: message exceeds the size limit")

// ErrBadFrameCount reports a count frame the protocol does not allow.
var ErrBadFrameCount = errors.New("wire: invalid frame count")

// Budget accounts for the bytes held by partly reassembled messages across connections.
type Budget interface {
	// Take reserves n bytes, or reports that they are not available.
	Take(n int) bool
	// Give returns n bytes.
	Give(n int)
}

// Assembler rebuilds the client's messages from its text frames. The zero value is not
// usable; set Max.
type Assembler struct {
	// Max caps a reassembled message, in bytes. It may change between messages.
	Max int
	// Budget, when set, is charged for the pieces of multi-frame messages while they are
	// held; a message the budget cannot hold is dropped as too big.
	Budget Budget

	want    int // frames still expected for the current message; 0 when none is open
	buf     []byte
	discard bool // the current message is dropped: count its frames, keep nothing
}

// Reset drops a message in progress and returns its bytes to the budget, for a connection
// that ends.
func (a *Assembler) Reset() {
	a.release()
	a.want, a.discard = 0, false
}

func (a *Assembler) release() {
	if a.Budget != nil && len(a.buf) > 0 {
		a.Budget.Give(len(a.buf))
	}
	a.buf = nil
}

// Feed takes one text frame. It returns a complete message, or nil when the frame was a
// keepalive or one piece of a longer message. A frame count of 0, the "0" frame clients send
// after 45 seconds of silence (WebSocketConnection.ts:425-435), is a keepalive.
func (a *Assembler) Feed(frame []byte) ([]byte, error) {
	if a.want == 0 {
		if len(frame) <= maxCountChars {
			if n, ok := frameCount(frame); ok {
				if n == 0 {
					return nil, nil
				}
				a.want, a.buf, a.discard = n, a.buf[:0], false
				return nil, nil
			}
		}
		if len(frame) > a.Max {
			return nil, ErrMessageTooBig
		}
		return frame, nil
	}
	a.want--
	if !a.discard {
		if len(a.buf)+len(frame) > a.Max || a.Budget != nil && !a.Budget.Take(len(frame)) {
			a.release()
			a.discard = true
		} else {
			a.buf = append(a.buf, frame...)
		}
	}
	if a.want > 0 {
		return nil, nil
	}
	if a.discard {
		a.discard = false
		return nil, ErrMessageTooBig
	}
	msg := a.buf
	if a.Budget != nil {
		a.Budget.Give(len(msg))
	}
	a.buf = nil
	return msg, nil
}

// Pending reports whether a multi-frame message is open.
func (a *Assembler) Pending() bool { return a.want > 0 }

// frameCount parses a count frame: decimal digits only, as the SDKs write it. An empty frame
// counts as 0, as Number("") does in the JS SDK.
func frameCount(frame []byte) (int, bool) {
	n := 0
	for _, c := range frame {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
