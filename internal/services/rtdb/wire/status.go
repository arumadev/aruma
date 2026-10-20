package wire

// Reply statuses. The SDKs map these to their error codes (Android DatabaseError.java:104-122,
// JS util.ts:473-491); anything else is an unknown error to them. Codes the clients make up
// themselves (disconnected, maxretries, overriddenbyset, write_canceled, network_error) never
// come from a server.
const (
	StatusOK               = "ok"
	StatusPermissionDenied = "permission_denied"
	StatusDataStale        = "datastale"
	StatusNoIndex          = "no_index"
	StatusTooBig           = "too_big"
	StatusInvalidToken     = "invalid_token"
	StatusExpiredToken     = "expired_token"
	StatusUnavailable      = "unavailable"
	// StatusInvalidParameters answers a request the server cannot accept as written, such as
	// an invalid path (emulator 4.11.2: "Invalid token in path").
	StatusInvalidParameters = "invalid_parameters"
)

// OK is the reply to a request that succeeded with no data; the emulator sends "" as its data.
func OK() Reply { return Reply{Status: StatusOK, Data: ""} }

// OKWith is a successful reply carrying data.
func OKWith(d any) Reply { return Reply{Status: StatusOK, Data: d} }

// Fail is an error reply: a status and a message for people.
func Fail(status, msg string) Reply { return Reply{Status: status, Data: msg} }
