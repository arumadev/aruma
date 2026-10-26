package core

// Principal is who calls, as the transports classified the caller at authentication (ADR-025
// §7.1, ADR-029). It chooses a read-write transaction's model, and a transaction id is bound to
// it: another principal's use of the id is a forged id.
type Principal struct {
	// Admin is a service account, the project's owner or the dev owner (Bearer owner): its
	// read-write transactions are pessimistic unless it asks for OPTIMISTIC. Anyone else is an
	// end user (a Firebase ID token, or no credential), whose read-write transactions must ask
	// for OPTIMISTIC, so that no end user holds locks on documents (ADR-025 §7.2).
	Admin bool
	// ID names the principal stably across its requests: the service account's email, the
	// account id, the user's uid; "" for one without identity.
	ID string
}
