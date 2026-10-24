package media

import (
	"net/http"
	"time"
)

// The write deadline of a download (ADR-022 §17, F §3.5). The server's WriteTimeout (60 s,
// server.go) would cut every download longer than a minute; lifting it altogether, as the
// WebSocket does, would let a client that never reads hold a connection and its file open for
// ever. So it moves a window at a time: each window of paceWindow must carry
// MinBytesPerSecond × paceWindow bytes to the client, or the write blocked in it fails at the
// window's end. This is min_bytes_per_second's meaning for uploads too ("below it over 30
// seconds, an upload request is cut", core.Limits).

// paceWindow is a window's length; a variable for the tests.
var paceWindow = 30 * time.Second

// pacer moves the write deadline of one reply.
type pacer struct {
	rc    *http.ResponseController
	quota int64 // the bytes one window must carry; 0 when pacing is off
	sent  int64 // in the current window
}

// newPacer starts the first window, or returns a pacer that does nothing when minRate is not
// positive (the server's own deadline then stands) or w has no deadline to move.
func newPacer(w http.ResponseWriter, minRate int64) *pacer {
	p := &pacer{rc: http.NewResponseController(w)}
	if minRate <= 0 {
		return p
	}
	p.quota = max(minRate*int64(paceWindow/time.Second), minRate)
	if !p.extend() {
		p.quota = 0
	}
	return p
}

// wrote records n bytes the client took, and opens a new window once this one carried its
// quota.
func (p *pacer) wrote(n int) {
	if p.quota == 0 {
		return
	}
	p.sent += int64(n)
	if p.sent >= p.quota {
		p.sent = 0
		p.extend()
	}
}

// extend sets the deadline at a window from now, and reports whether w supports it.
func (p *pacer) extend() bool {
	return p.rc.SetWriteDeadline(time.Now().Add(paceWindow)) == nil
}
