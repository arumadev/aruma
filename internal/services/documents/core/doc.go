// Package core is the Document Database apart from any transport (ADR-024, ADR-025): the
// storage of documents and their past versions on Postgres, the one commit path and the
// sequencer that orders commits, reads at the latest or at a read_time, listings with
// show_missing and collection ids, the databases of a project, and the single-instance guard.
// The surfaces above it (gRPC, REST, WebChannel, the Admin API, the console) call Store and
// convert core's values to the wire; core knows no HTTP, no gRPC and no wire format but the one it
// stores (google.firestore.v1.Document, binary protobuf).
//
// The files:
//
//   - store.go: Store, the contract the transports and the later phases use, and its types.
//   - store_pg.go: PGStore, Store on Postgres: the reads and listings.
//   - writes.go: a commit's writes folded per document, with preconditions and update masks.
//   - sequencer.go: the commit path (ADR-024 §2.1): the per-database gate, the two pipelined
//     batches and the stamp, retries, and the outcomes the hub will be told of.
//   - database.go: the databases of a project; '(default)' is created on first use.
//   - instance.go: the session advisory lock that keeps the Document Database on one process.
//   - names.go: paths and their order-preserving name keys, cut for the btree (ADR-024 §1.3, §1.4).
//   - docpb.go: the stored document encoding.
//   - errors.go: the errors, with production's messages where they were recorded.
//
// Not yet here, by phase: transforms, transactions and the lock manager, index entries and
// queries (F2); the hub and Listen (F3); usage counters, retention and purge.
package core
