package webchannel

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// backchannel serves a backchannel GET (WebChannelBase.startBackChannel_): every array after the
// client's AID, then what the stream sends, as chunks. Streamed (CI=0), it is held open, with a
// noop every Timing.Noop, until Timing.Backchannel has passed or it carried a quarter of
// SessionBytes, so that the next one acknowledges what it carried. Buffered (CI=1, long polling),
// it answers with what there is as soon as there is anything, or a noop once the poll is held
// for Timing.LongPoll or the client's TO.
func (h *Handler) backchannel(w http.ResponseWriter, r *http.Request, q url.Values) {
	s := h.lookup(q)
	if s == nil {
		unknownSID(w)
		return
	}
	buffered := q.Get("CI") == "1"
	hold := h.timing.Backchannel
	keepalive := h.timing.Noop
	if buffered {
		hold = h.timing.LongPoll
		if to, err := strconv.Atoi(q.Get("TO")); err == nil && to > 0 {
			hold = min(hold, time.Duration(to)*time.Millisecond)
		}
		keepalive = hold
	}

	bc := &backchannel{}
	s.mu.Lock()
	bc.first = !s.backed
	s.backed = true
	if s.back != nil {
		s.back.replaced = true
	}
	s.back = bc
	bc.next = s.nextID
	if len(s.out) > 0 {
		bc.next = s.out[0].id
	}
	s.wakeLocked()
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.back == bc {
			s.back = nil
			s.touchLocked()
		}
		s.mu.Unlock()
	}()

	rc := http.NewResponseController(w)
	// The server's read timeout would end the request's context while it is held.
	_ = rc.SetReadDeadline(time.Time{})
	hd := w.Header()
	hd.Set("Content-Type", contentType)
	hd.Set("Cache-Control", "private, no-store")
	hd.Set("X-Accel-Buffering", "no")
	if bc.first {
		// As production's first backchannel of a session varies (f6-webchannel-*.json).
		hd["Vary"] = append([]string{"Referer"}, hd["Vary"]...)
	}
	w.WriteHeader(http.StatusOK)
	write := func(text []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(h.limits.WriteStall))
		if _, err := w.Write(chunk(nil, text)); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	// A backchannel with nothing pending begins with a noop, as production's do but the
	// session's first streamed one (f6-webchannel-listen.json, f6-webchannel-longpoll.json).
	if buffered || !bc.first {
		if id, ok := s.noopID(bc); ok && !write(arrays([]array{{id: id, payload: noop}})) {
			return
		}
	}
	end := time.NewTimer(hold)
	defer end.Stop()
	tick := time.NewTimer(keepalive)
	defer tick.Stop()
	var carried int64
	for {
		s.mu.Lock()
		var send []array
		for _, a := range s.out {
			if a.id >= bc.next {
				send = append(send, a)
			}
		}
		if len(send) > 0 {
			bc.next = send[len(send)-1].id + 1
		}
		done := bc.replaced || s.removed || !s.ended.IsZero() && len(send) == 0
		ch := s.changed
		s.mu.Unlock()
		if len(send) > 0 {
			if !write(arrays(send)) {
				return
			}
			for _, a := range send {
				carried += int64(len(a.payload))
			}
			if buffered || carried >= h.limits.SessionBytes/4 {
				return
			}
			tick.Reset(keepalive)
			continue
		}
		if done {
			return
		}
		select {
		case <-ch:
		case <-tick.C:
			if id, ok := s.noopID(bc); ok {
				if !write(arrays([]array{{id: id, payload: noop}})) || buffered {
					return
				}
			}
			tick.Reset(keepalive)
		case <-end.C:
			// A long poll held to its end answers a noop, as its keep-alive would.
			if buffered {
				if id, ok := s.noopID(bc); ok {
					write(arrays([]array{{id: id, payload: noop}}))
				}
			}
			return
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// noopID numbers a noop for bc: an array like any, but not kept for a later backchannel. There is
// none while bc has arrays to write: a noop numbered after them and written before them would
// let the client acknowledge them unread.
func (s *session) noopID(bc *backchannel) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bc.next < s.nextID {
		return 0, false
	}
	id := s.nextID
	s.nextID++
	bc.next = s.nextID
	return id, true
}

// Receive reads the stream's next request.
func (c listenConn) Receive() (*pb.ListenRequest, error) {
	b, err := c.s.receive()
	if err != nil {
		return nil, err
	}
	req := new(pb.ListenRequest)
	if err := wire.UnmarshalREST(b, req); err != nil {
		return nil, err
	}
	return req, nil
}

// Send queues a response for the backchannel.
func (c listenConn) Send(r *pb.ListenResponse) error {
	var b []byte
	var err error
	if dd := r.GetDocumentDelete(); dd != nil {
		b, err = documentDelete(dd)
	} else {
		b, err = wire.MarshalREST(r)
	}
	if err != nil {
		return err
	}
	return c.s.send(message(b))
}

// documentDelete is a DocumentDelete response in production's order of its fields, their
// numbers' (f6-webchannel-errors.json): document, readTime, removedTargetIds, where protojson
// writes them as the proto declares them, removedTargetIds before readTime.
func documentDelete(dd *pb.DocumentDelete) ([]byte, error) {
	var readTime json.RawMessage
	if dd.ReadTime != nil {
		b, err := wire.MarshalREST(dd.ReadTime)
		if err != nil {
			return nil, err
		}
		readTime = b
	}
	type body struct {
		Document         string          `json:"document,omitempty"`
		ReadTime         json.RawMessage `json:"readTime,omitempty"`
		RemovedTargetIDs []int32         `json:"removedTargetIds,omitempty"`
	}
	return json.Marshal(struct {
		DocumentDelete body `json:"documentDelete"`
	}{body{dd.GetDocument(), readTime, dd.GetRemovedTargetIds()}})
}

// Receive reads the stream's next request.
func (c writeConn) Receive() (*pb.WriteRequest, error) {
	b, err := c.s.receive()
	if err != nil {
		return nil, err
	}
	req := new(pb.WriteRequest)
	if err := wire.UnmarshalREST(b, req); err != nil {
		return nil, err
	}
	return req, nil
}

// Send queues a response for the backchannel. The handshake's stream id is "0", as production
// answers every Write session (f6-webchannel-write.json, f6-webchannel-errors.json); no client
// reads it back, a stream being never resumed.
func (c writeConn) Send(r *pb.WriteResponse) error {
	if r.GetStreamId() != "" {
		r.StreamId = "0"
	}
	b, err := wire.MarshalREST(r)
	if err != nil {
		return err
	}
	return c.s.send(message(b))
}
