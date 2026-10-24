// Package wire is Cloud Storage's formats on the wire, apart from any service logic: the object
// resources of Firebase REST (/v0) and of the GCS JSON API, their errors, the header names, and
// the parsers of what clients send (paths, upload commands, Content-Range, multipart/related,
// Range). It imports Go's standard library alone; each surface converts core's values into
// these types itself (api/resource.go, gcs/resource.go), because the two disagree on tokens,
// contentEncoding and numbers.
//
// Sources: the client protocol research (docs/research/storage/A-client-protocol.md, A), the
// Admin and GCS protocol (B), the emulator's recordings (D) and production against the emulator
// (E), as ADR-022 cites them.
//
// No null on /v0: Android reads a JSON null as the string "null" (A §1.8), so every field of a
// /v0 type is a string with omitempty, or a map of strings, and the lists a client iterates are
// present even when empty (ADR-022 §12, §13). wiretest.AssertNoNull checks every /v0 reply in
// the api tests.
package wire
