package wire

// Control message types.
const (
	ControlHello    = "h" // server: the handshake, first frame on every connection
	ControlShutdown = "s" // server: going away, do not reconnect
	ControlReset    = "r" // server: reconnect to another host
	ControlError    = "e" // server: an error not tied to a request
	ControlPing     = "p" // client: are you there?
	ControlPong     = "o" // server: the answer to a ping
	// The upgrade from long polling to a WebSocket (Connection.ts:240-304, 353-360).
	ControlSwitchAck       = "a" // both: on the new WebSocket, it is ready / the client moves to it
	ControlEndTransmission = "n" // both: on the long poll, nothing more comes this way
)

// ProtocolVersion is the only version the SDKs speak.
const ProtocolVersion = "5"

// Hello is the handshake's payload (Connection.ts:335-404, FConnection.m:226-262).
type Hello struct {
	// Timestamp is the server time in milliseconds since the epoch; clients derive
	// .info/serverTimeOffset from it. iOS closes the connection unless it is a number.
	Timestamp int64 `json:"ts"`
	// Version is always "5".
	Version string `json:"v"`
	// Host is where the client connects next time. The SDKs reconnect to it, so it must be
	// exactly the host and port the client used.
	Host string `json:"h"`
	// Session is the session id; the client sends it back as ls= when it reconnects.
	Session string `json:"s"`
}

// EncodeHello encodes the handshake frame.
func EncodeHello(h Hello) []byte {
	if h.Version == "" {
		h.Version = ProtocolVersion
	}
	return encodeControl(ControlHello, h)
}

// EncodeShutdown tells the client the database is gone for good. The SDKs report the reason
// and never reconnect on their own (PersistentConnection.ts:876-882, Android
// PersistentConnectionImpl.java:539-554), so it is for a database or project that has really
// been disabled or deleted, never for a restart.
func EncodeShutdown(reason string) []byte { return encodeControl(ControlShutdown, reason) }

// EncodeReset sends the client to another host.
func EncodeReset(host string) []byte { return encodeControl(ControlReset, host) }

// EncodeError reports an error the client cannot match to a request. The JS SDK only logs it;
// Android and iOS ignore it.
func EncodeError(msg string) []byte { return encodeControl(ControlError, msg) }

// EncodeSwitchAck is the server's first frame on a WebSocket that joins a long-poll session,
// with null data as the emulator's (golden/lp_upgrade.json).
func EncodeSwitchAck() []byte { return encodeControl(ControlSwitchAck, nil) }

// EncodeEndTransmission is the server's last message on a long poll whose session moved to a
// WebSocket: the client reads the WebSocket from then on.
func EncodeEndTransmission() []byte { return encodeControl(ControlEndTransmission, nil) }

// EncodePong answers a ping, with null data as the emulator does (golden/handshake.json).
func EncodePong() []byte { return encodeControl(ControlPong, nil) }

func encodeControl(t string, d any) []byte {
	return encode(envelope2{T: TypeControl, D: struct {
		T string `json:"t"`
		D any    `json:"d"`
	}{t, d}})
}
