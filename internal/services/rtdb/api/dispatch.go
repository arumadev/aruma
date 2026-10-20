package api

import (
	"context"
	"encoding/json"

	"github.com/osama300/aurma-project/internal/services/rtdb/core"
	"github.com/osama300/aurma-project/internal/services/rtdb/wire"
)

// errorFrame is the emulator's answer to a message it cannot act on: an unknown action, a
// frame that is not JSON, a request without a body (golden/unknown_action.json). The request
// gets no reply; the connection stays open. No SDK sends such messages.
const errorFrame = "Error on incoming message"

// handle acts on one client message. The transport calls it for each message in order.
func (s *session) handle(ctx context.Context, msg []byte) {
	in, err := wire.Parse(msg)
	if err != nil {
		s.send(wire.EncodeError(errorFrame))
		return
	}
	if c := in.Control; c != nil {
		if c.Type == wire.ControlPing {
			s.send(wire.EncodePong())
		}
		return
	}
	s.dispatch(ctx, in.Request)
}

func (s *session) dispatch(ctx context.Context, req *wire.Request) {
	switch req.Action {
	case wire.ActionStats:
		// Usage counters are acknowledged and not kept.
		var b struct {
			C map[string]json.RawMessage `json:"c"`
		}
		if s.decode(req, &b) {
			s.reply(req, wire.OK())
		}

	case wire.ActionAppCheck, wire.ActionUnAppCheck:
		// App Check is accepted and ignored (rtdb-plan.md): the SDKs only log a failure, and
		// Aruma has no App Check yet.
		s.reply(req, wire.OK())

	case wire.ActionPut:
		var b wire.WriteBody
		if s.decode(req, &b) {
			sl := s.reserve(req.ID)
			s.core.Set(b.Path, b.Data, b.Hash, func(n *core.Node, err error) {
				switch {
				case err != nil:
					s.fill(sl, result(req.ID, err))
				case b.Hash != nil:
					// A conditional put is acked with the data written (emulator 4.11.2).
					s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith(json.RawMessage(n.ExportJSON()))))
				default:
					s.fill(sl, wire.EncodeResponse(req.ID, wire.OK()))
				}
			})
		}

	case wire.ActionMerge:
		var b wire.WriteBody
		if s.decode(req, &b) {
			sl := s.reserve(req.ID)
			s.core.Update(b.Path, b.Data, func(err error) { s.fill(sl, result(req.ID, err)) })
		}

	case wire.ActionGet:
		var b wire.GetBody
		if !s.decode(req, &b) {
			return
		}
		if err := b.Query.Validate(); err != nil || !b.Query.IsDefault() {
			// Queries are served with the listeners (R3).
			s.reply(req, wire.Fail(wire.StatusUnavailable, "Queries are not supported by this server yet."))
			return
		}
		sl := s.reserve(req.ID)
		s.core.Get(b.Path, func(n *core.Node, err error) {
			if err != nil {
				s.fill(sl, result(req.ID, err))
				return
			}
			s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith(json.RawMessage(n.ExportJSON()))))
		})

	case wire.ActionListen, wire.ActionUnlisten:
		// Listening comes in R3. Until then a listen fails cleanly: the SDKs remove it and
		// report the error, where an "ok" would leave them waiting for data that never comes.
		var b wire.ListenBody
		if s.decode(req, &b) {
			s.reply(req, wire.Fail(wire.StatusUnavailable, "Listening is not supported by this server yet."))
		}

	case wire.ActionOnDisconnPut, wire.ActionOnDisconnMerge:
		var b wire.WriteBody
		if s.decode(req, &b) {
			var err error
			if req.Action == wire.ActionOnDisconnPut {
				err = s.core.OnDisconnectSet(b.Path, b.Data)
			} else {
				err = s.core.OnDisconnectUpdate(b.Path, b.Data)
			}
			s.reply(req, replyOf(err))
		}

	case wire.ActionOnDisconnClear:
		var b wire.PathBody
		if s.decode(req, &b) {
			s.reply(req, replyOf(s.core.OnDisconnectCancel(b.Path)))
		}

	case wire.ActionAuth, wire.ActionGAuth:
		var b wire.AuthBody
		if s.decode(req, &b) {
			s.reply(req, s.authenticate(ctx, b))
		}

	case wire.ActionUnauth:
		s.signOut()
		s.reply(req, wire.OKWith("unauthenticated")) // the emulator's reply

	default:
		s.send(wire.EncodeError(errorFrame))
	}
}

// decode reads a request's body, answering a malformed one as the emulator does.
func (s *session) decode(req *wire.Request, v any) bool {
	if err := wire.DecodeBody(req, v); err != nil {
		s.send(wire.EncodeError(errorFrame))
		return false
	}
	return true
}

// reply answers a request on the spot, after the replies still pending before it.
func (s *session) reply(req *wire.Request, r wire.Reply) {
	s.fill(s.reserve(req.ID), wire.EncodeResponse(req.ID, r))
}

func replyOf(err error) wire.Reply {
	if err != nil {
		e := core.AsError(err)
		return wire.Fail(e.Status, e.Message)
	}
	return wire.OK()
}

// result encodes the reply to request id: ok, or err's status and message.
func result(id int64, err error) []byte { return wire.EncodeResponse(id, replyOf(err)) }
