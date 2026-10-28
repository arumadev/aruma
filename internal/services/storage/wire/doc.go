// Package wire is Cloud Storage's formats on the wire, apart from any service logic: the object
// resources of Firebase REST (/v0) and of the GCS JSON API, their errors, the header names, and
// the parsers of what clients send (paths, upload commands, Content-Range, multipart/related,
// Range, X-Goog-Hash). It imports Go's standard library alone; each surface converts core's
// values into these types itself (api/resource.go, gcs/resource.go), because the two disagree on
// tokens, contentEncoding and numbers.
//
// Sources: the client protocol research (A), the
// Admin and GCS protocol (B), the emulator's recordings (D, test/oracle/storage/emulator) and
// production against the emulator (E, test/oracle/storage/production, and Pyric's observations
// of real Storage), as ADR-022 cites them. golden_test.go holds every type and parser to the
// recordings.
//
// No null on /v0: Android reads a JSON null as the string "null" (A §1.8), so every field of a
// /v0 type is a string with omitempty, or a map of strings, and the lists a client iterates are
// present even when empty (ADR-022 §12, §13). wiretest.AssertNoNull checks every /v0 reply in
// the api tests. Marshal writes a reply as Google does: compact, without HTML escaping.
//
// The parsers bound what a hostile request costs: a header is scanned once and never split into
// a slice (a billion ranges or commands cost one pass), numbers stop at an int64, and a
// multipart body's preamble and part headers are read under a fixed budget, so only the media
// streams without one, under its caller's cap. Content-Disposition is not here: its one escaping
// is internal/platform/disposition, which core and media share.
package wire
