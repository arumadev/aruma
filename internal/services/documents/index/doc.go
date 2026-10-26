// Package index is the Document Database's index catalogue and production's composite-index
// rule (ADR-025 §2 to §4): which single-field indexes exist automatically, how field overrides
// change them, which composite indexes a database declares, and whether a query is served by
// them, needs an index, or falls where the evidence is not settled.
//
// It is pure logic. It knows no values, no protobufs and no storage: a query arrives as a
// QueryShape (shape.go), which the query package builds from a StructuredQuery, and the
// catalogue arrives as the declared Index and FieldOverride lists (spec.go, overrides.go). Field
// paths are canonical strings throughout ("a", "m.x", "`odd name`.y", "__name__").
//
// The rule is production's, recorded on 105 query shapes (test/oracle/documents/corpus/
// indexes-105.json; the evidence brief's R1 to R24):
//
//   - enforce.go decides. R1 to R13 are verified and implemented exactly. Where the evidence
//     is docs-only or uncertain (R18 to R22, and shapes the probe never sent), Check never
//     rejects: it answers Uncertain, which the caller serves and logs as
//     index_enforcement_uncertain.
//   - suggest.go writes production's FAILED_PRECONDITION error byte for byte: one of the five
//     message templates and the Firebase console link with the hand-encoded
//     google.firestore.admin.v1.Index payload.
//   - automatic.go and overrides.go are the single-field indexes: ascending, descending and
//     array-contains per field at collection scope by default, and the overrides that enable
//     collection-group scope or exempt a field.
package index
