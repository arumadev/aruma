package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Envelope types.
const (
	TypeControl = "c"
	TypeData    = "d"
)

// Control is a control message in either direction: {"t":"c","d":{"t":<Type>,"d":<Data>}}.
type Control struct {
	Type string          `json:"t"`
	Data json.RawMessage `json:"d,omitempty"`
}

// Request is a client request: {"t":"d","d":{"r":<ID>,"a":<Action>,"b":<Body>}}. Requests are
// numbered per connection and answered by number (PersistentConnection.ts:642-658).
type Request struct {
	ID     int64           `json:"r"`
	Action string          `json:"a"`
	Body   json.RawMessage `json:"b"`
}

// Incoming is one decoded client message: exactly one of Control and Request is set.
type Incoming struct {
	Control *Control
	Request *Request
}

// ErrMalformed reports a message that is not a protocol envelope.
var ErrMalformed = errors.New("wire: malformed message")

type envelope struct {
	T string          `json:"t"`
	D json.RawMessage `json:"d"`
}

// Parse decodes a client message.
func Parse(msg []byte) (Incoming, error) {
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		return Incoming{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	switch env.T {
	case TypeControl:
		var c Control
		if err := json.Unmarshal(env.D, &c); err != nil || c.Type == "" {
			return Incoming{}, fmt.Errorf("%w: control without a type", ErrMalformed)
		}
		return Incoming{Control: &c}, nil
	case TypeData:
		var r struct {
			ID     *int64          `json:"r"`
			Action string          `json:"a"`
			Body   json.RawMessage `json:"b"`
		}
		if err := json.Unmarshal(env.D, &r); err != nil || r.ID == nil || r.Action == "" {
			return Incoming{}, fmt.Errorf("%w: request without r or a", ErrMalformed)
		}
		return Incoming{Request: &Request{ID: *r.ID, Action: r.Action, Body: r.Body}}, nil
	default:
		return Incoming{}, fmt.Errorf("%w: unknown envelope type %q", ErrMalformed, env.T)
	}
}

// Reply is the body of a response: a status and its data, which is the result for "ok" and
// a human-readable message otherwise.
type Reply struct {
	Status string `json:"s"`
	Data   any    `json:"d"`
}

// EncodeResponse answers request id.
func EncodeResponse(id int64, r Reply) []byte {
	return encode(envelope2{T: TypeData, D: struct {
		R int64 `json:"r"`
		B Reply `json:"b"`
	}{id, r}})
}

// envelope2 is envelope with a typed payload, for encoding.
type envelope2 struct {
	T string `json:"t"`
	D any    `json:"d"`
}

// encode marshals v without the HTML escaping encoding/json applies by default: < > & stay
// as they are, as in the frames Firebase sends.
func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Every value encoded here is built by this package or is already valid JSON.
		panic(fmt.Sprintf("wire: encode: %v", err))
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
