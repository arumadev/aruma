package core

import "encoding/json"

// Auth is who a session acts as. Rules see it as their auth variable (R4).
type Auth struct {
	// Admin is an admin or service credential. It bypasses rules, as in Firebase.
	Admin bool
	// Account names the admin principal for the logs: a service account email or an account
	// id. Empty for clients.
	Account string
	// Variable is the auth variable an Admin SDK asked its rules to see instead of an admin
	// (databaseAuthVariableOverride, sent as authvar), kept as sent. Such a session is not an
	// admin: R4 hands the value to the rules; until then the locked rules deny it.
	Variable json.RawMessage
}

// canRead and canWrite decide access until R4 wires package rules to the document stored in
// rtdb.databases. Every database keeps the locked rules it starts with (ADR-020), so only
// admins pass, unless the instance runs with dev_open_rules and the client is on this
// machine, which acts as the test-mode rules {".read": true, ".write": true}. A permitted
// operation admits the session against its project's connection limit.
func (s *Session) canRead(Path) error  { return s.allowed() }
func (s *Session) canWrite(Path) error { return s.allowed() }

func (s *Session) allowed() error {
	if !s.Auth().Admin && !s.devOpen() {
		return ErrPermissionDenied
	}
	return s.admit()
}
