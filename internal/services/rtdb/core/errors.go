package core

import (
	"errors"
	"net/http"
)

// Error is a Realtime Database error: the status the WebSocket protocol replies with, the
// message beside it, and the HTTP status REST answers with. Messages follow the emulator
// 4.11.2 where it has one (B-emulator-oracle.md); Firebase publishes none.
type Error struct {
	Status  string // protocol status: permission_denied, datastale, ...
	Message string
	HTTP    int
}

func (e *Error) Error() string { return e.Status + ": " + e.Message }

// Protocol statuses, as in package wire, which core does not import.
const (
	statusPermissionDenied  = "permission_denied"
	statusDataStale         = "datastale"
	statusInvalidParameters = "invalid_parameters"
	statusTooBig            = "too_big"
	statusUnavailable       = "unavailable"
	statusInvalidToken      = "invalid_token"
	statusExpiredToken      = "expired_token"
)

var (
	ErrPermissionDenied = &Error{statusPermissionDenied, "Permission denied", http.StatusUnauthorized}
	// ErrDataStale answers a conditional write whose hash no longer matches.
	ErrDataStale = &Error{statusDataStale, "Transaction ETag does not match", http.StatusPreconditionFailed}

	ErrInvalidPath = &Error{statusInvalidParameters, "Invalid token in path", http.StatusBadRequest}
	ErrPathTooDeep = &Error{statusInvalidParameters,
		"Path specified exceeds the maximum depth that can be written (32).", http.StatusBadRequest}
	ErrPathTooLong = &Error{statusInvalidParameters,
		"Path specified exceeds the maximum length that can be written (768 bytes).", http.StatusBadRequest}
	ErrInvalidKey = &Error{statusInvalidParameters,
		"Invalid data; Key value can't be empty or contain $ # [ ] / or .", http.StatusBadRequest}
	ErrInvalidData = &Error{statusInvalidParameters,
		"Invalid data; couldn't parse JSON object, array, or value.", http.StatusBadRequest}
	ErrInvalidPriority = &Error{statusInvalidParameters,
		"Invalid data; a priority must be a number, a string or null.", http.StatusBadRequest}
	ErrInvalidServerValue = &Error{statusInvalidParameters,
		"Invalid data; unknown server value.", http.StatusBadRequest}
	ErrOverlappingPaths = &Error{statusInvalidParameters,
		"Invalid data; an update may not write a path and one of its descendants.", http.StatusBadRequest}
	// ErrTooBig is a write or string over the size limits.
	ErrTooBig = &Error{statusTooBig, "Data to write exceeds the maximum size that can be modified with a single request.", http.StatusRequestEntityTooLarge}
	// ErrReadTooBig is a read of more rows than Limits.ReadRows, in Firebase's words for a
	// read too large to answer, with the status the SDKs know for it. REST answers 400.
	ErrReadTooBig = &Error{statusTooBig, "Data requested exceeds the maximum size that can be accessed with a single request.", http.StatusBadRequest}

	// ErrWriteRate is a database over its bytes-per-minute write limit.
	ErrWriteRate = &Error{statusUnavailable, "Write rate limit exceeded; retry later.", http.StatusTooManyRequests}
	// ErrUnavailable is a failure of the server itself, such as its database.
	ErrUnavailable = &Error{statusUnavailable, "The service is currently unavailable.", http.StatusServiceUnavailable}
	// ErrUnsupported is a request this version does not serve yet.
	ErrUnsupported = &Error{statusUnavailable, "Not supported by this server yet.", http.StatusNotImplemented}

	// ErrInvalidQuery is a query object the SDKs would not send. The emulator 4.11.2 names
	// two cases (golden listen_errors.json): a negative limit, ErrInvalidLimit, and an index
	// that is not a valid path, ErrInvalidPath.
	ErrInvalidQuery = &Error{statusInvalidParameters, "Invalid query.", http.StatusBadRequest}
	ErrInvalidLimit = &Error{statusInvalidParameters, "Invalid query limit", http.StatusBadRequest}

	// ErrNoDatabase is a namespace that names no database: not <project>-default-rtdb, or a
	// project that does not exist.
	ErrNoDatabase = &Error{statusInvalidParameters, "Firebase error. Please ensure that you have the URL of your Firebase Realtime Database instance configured correctly.", http.StatusNotFound}
)

// statusNoIndex answers a read whose order needs an index the rules do not declare.
const statusNoIndex = "no_index"

// NoIndexError is the emulator's answer to a get, or a REST read, ordered by a child or by
// value without the .indexOn that covers it (B-emulator-oracle.md, queries addendum). A listen
// is served anyway, unfiltered, with the warning instead.
func NoIndexError(key string, p Path) *Error {
	return &Error{statusNoIndex,
		`Index not defined, add ".indexOn": "` + key + `", for path "/` + p.String() + `", to the rules`,
		http.StatusBadRequest}
}

// AsError returns err as an *Error, mapping anything unexpected to ErrUnavailable.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrUnavailable
}
