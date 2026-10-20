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
		sl := s.reserve(req.ID)
		s.core.Get(b.Path, querySpec(b.Query), func(n *core.Node, err error) {
			if err != nil {
				s.fill(sl, result(req.ID, err))
				return
			}
			s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith(json.RawMessage(n.ExportJSON()))))
		})

	case wire.ActionListen:
		var b wire.ListenBody
		if !s.decode(req, &b) {
			return
		}
		var tag int64
		if b.Tag != nil {
			tag = *b.Tag
		}
		// The data come before the reply: the writer pushes them before calling back.
		sl := s.reserve(req.ID)
		s.core.Listen(b.Path, querySpec(b.Query), tag, b.Hash, func(warnings []string, err error) {
			switch {
			case err != nil:
				s.fill(sl, result(req.ID, err))
			case len(warnings) > 0:
				s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith(map[string]any{"w": warnings})))
			default:
				// Always an object: Android reads the reply's data as a map.
				s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith(map[string]any{})))
			}
		})

	case wire.ActionUnlisten:
		var b wire.ListenBody
		if s.decode(req, &b) {
			sl := s.reserve(req.ID)
			var tag int64
			if b.Tag != nil {
				tag = *b.Tag
			}
			s.core.Unlisten(b.Path, querySpec(b.Query), tag, func(err error) { s.fill(sl, result(req.ID, err)) })
		}

	case wire.ActionOnDisconnPut, wire.ActionOnDisconnMerge:
		var b wire.WriteBody
		if s.decode(req, &b) {
			// The rules check it against the data, on the writer.
			sl := s.reserve(req.ID)
			done := func(err error) { s.fill(sl, result(req.ID, err)) }
			if req.Action == wire.ActionOnDisconnPut {
				s.core.OnDisconnectSet(b.Path, b.Data, done)
			} else {
				s.core.OnDisconnectUpdate(b.Path, b.Data, done)
			}
		}

	case wire.ActionOnDisconnClear:
		var b wire.PathBody
		if s.decode(req, &b) {
			s.reply(req, replyOf(s.core.OnDisconnectCancel(b.Path)))
		}

	case wire.ActionAuth, wire.ActionGAuth:
		var b wire.AuthBody
		if s.decode(req, &b) {
			// Whoever the session now is, the listens it may no longer read are cancelled
			// with c before the reply, as the emulator does when a client signs in as
			// someone else (golden auth_change.json).
			sl := s.reserve(req.ID)
			r := s.authenticate(ctx, b)
			s.core.RevokeListens(func() { s.fill(sl, wire.EncodeResponse(req.ID, r)) })
		}

	case wire.ActionUnauth:
		// Listens the session may no longer read are cancelled with c before the reply, as
		// the emulator does (B-emulator-oracle.md §1.8).
		s.signOut()
		sl := s.reserve(req.ID)
		s.core.RevokeListens(func() {
			s.fill(sl, wire.EncodeResponse(req.ID, wire.OKWith("unauthenticated"))) // the emulator's reply
		})

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

// querySpec turns the protocol's query object into core's; nil (no q) stays nil.
func querySpec(q *wire.Query) *core.QuerySpec {
	if q == nil {
		return nil
	}
	var limit int64
	if q.Limit != nil {
		limit = *q.Limit
	}
	return &core.QuerySpec{
		Index:      q.Index,
		StartValue: q.StartValue, StartName: q.StartName, StartInclusive: q.StartInclusive,
		EndValue: q.EndValue, EndName: q.EndName, EndInclusive: q.EndInclusive,
		Limit: limit, FromRight: q.ViewFrom == wire.ViewFromRight,
	}
}

// wireQuery turns a core query back into the protocol's object, for the c push that cancels
// a listen; nil for a default listen (the emulator sends "q":null).
func wireQuery(s *core.QuerySpec) *wire.Query {
	if s == nil {
		return nil
	}
	q := &wire.Query{
		Index:      s.Index,
		StartValue: s.StartValue, StartName: s.StartName, StartInclusive: s.StartInclusive,
		EndValue: s.EndValue, EndName: s.EndName, EndInclusive: s.EndInclusive,
	}
	if s.Limit > 0 {
		q.Limit = &s.Limit
		q.ViewFrom = wire.ViewFromLeft
		if s.FromRight {
			q.ViewFrom = wire.ViewFromRight
		}
	}
	return q
}

// Push implements core.Sink: it queues a push from the database's writer.
func (s *session) Push(p core.Push) {
	var tag *int64
	if p.Tag != 0 {
		tag = &p.Tag
	}
	switch p.Action {
	case core.PushData:
		s.out.push(wire.EncodeDataPush(p.Path.String(), p.Data, tag))
	case core.PushMerge:
		s.out.push(wire.EncodeMergePush(p.Path.String(), p.Data, tag))
	case core.PushCancel:
		s.out.push(wire.EncodeListenRevoked(p.Path.String(), wireQuery(p.Query)))
	case core.PushAuthRevoked:
		s.out.push(wire.EncodeAuthRevoked(p.Status, p.Reason))
	}
}
