package wire

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Client actions (A-protocol-sdks.md §2).
const (
	ActionStats          = "s"          // SDK usage counters, sent once per connection
	ActionListen         = "q"          // start listening at a path, optionally with a query
	ActionUnlisten       = "n"          // stop listening
	ActionPut            = "p"          // set a value, optionally only if the hash matches
	ActionMerge          = "m"          // multi-path update
	ActionGet            = "g"          // read once
	ActionOnDisconnPut   = "o"          // set a value when this connection closes
	ActionOnDisconnMerge = "om"         // update when this connection closes
	ActionOnDisconnClear = "oc"         // cancel the onDisconnect writes at a path
	ActionAuth           = "auth"       // sign in with a token
	ActionGAuth          = "gauth"      // sign in with a Google OAuth access token (Admin SDKs)
	ActionUnauth         = "unauth"     // sign out
	ActionAppCheck       = "appcheck"   // present an App Check token
	ActionUnAppCheck     = "unappcheck" // withdraw it
)

// Server push actions (A-protocol-sdks.md §3).
const (
	PushData          = "d"  // replace the data at a path
	PushMerge         = "m"  // merge children at a path
	PushListenRevoked = "c"  // a listen is cancelled, for example because rules no longer allow it
	PushAuthRevoked   = "ac" // the session's credential is no longer valid
)

// ListenBody is the body of q and n. Default queries carry neither Query nor Tag.
type ListenBody struct {
	Path  string `json:"p"`
	Query *Query `json:"q,omitempty"`
	Tag   *int64 `json:"t,omitempty"`
	// Hash is the hash of what the client has cached at Path, "" when nothing.
	Hash string `json:"h"`
	// CompoundHash is the per-range hash Android and iOS send for caches over 1 KB. A server
	// may ignore it and send the full data, which the SDKs always accept.
	CompoundHash *CompoundHash `json:"ch,omitempty"`
}

// CompoundHash splits a cached node into ranges, each with its hash.
type CompoundHash struct {
	Paths  []string `json:"ps"`
	Hashes []string `json:"hs"`
}

// WriteBody is the body of p, m, o and om. Data stays raw JSON for the data model to parse.
type WriteBody struct {
	Path string          `json:"p"`
	Data json.RawMessage `json:"d"`
	// Hash makes a put conditional: it applies only if the current value at Path still has
	// this hash, else the reply is datastale. Transactions use it.
	Hash *string `json:"h,omitempty"`
}

// GetBody is the body of g.
type GetBody struct {
	Path  string `json:"p"`
	Query *Query `json:"q,omitempty"`
}

// PathBody is the body of oc.
type PathBody struct {
	Path string `json:"p"`
}

// AuthBody is the body of auth and gauth.
type AuthBody struct {
	Credential string `json:"cred"`
	// AuthVar is the auth variable an Admin SDK asks its rules to see (databaseAuthVariableOverride).
	AuthVar json.RawMessage `json:"authvar,omitempty"`
	// NoAuth means the Admin SDK wants to act as an unauthenticated client.
	NoAuth bool `json:"noauth,omitempty"`
}

// AppCheckBody is the body of appcheck.
type AppCheckBody struct {
	Token string `json:"token"`
}

// DecodeBody decodes a request body into v, which must be a pointer to one of the body types.
// A body without the path an action needs is malformed.
func DecodeBody(req *Request, v any) error {
	if len(req.Body) == 0 || string(req.Body) == "null" {
		return fmt.Errorf("%w: %s without a body", ErrMalformed, req.Action)
	}
	if err := json.Unmarshal(req.Body, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMalformed, req.Action, err)
	}
	if w, ok := v.(*WriteBody); ok && len(w.Data) == 0 {
		return fmt.Errorf("%w: %s without d", ErrMalformed, req.Action)
	}
	return nil
}

// PushPath is a path as pushes carry it: without the leading slash, "" for the root
// (emulator 4.11.2; JS sends "/a/b", Android and iOS "a/b").
func PushPath(p string) string { return strings.Trim(p, "/") }
