// Package listen is the Document Database's in-process Listen hub (ADR-026 §2, ADR-024 §4–§5):
// the store tells it of every commit (core.Notifier), and it gives each subscriber the commits
// that change its targets, per database in seq order, whole. It knows no transport and no
// stream protocol: the Listen stream (ADD, CURRENT, NO_CHANGE, resume tokens, ExistenceFilter,
// limit windows, rules) is built on it.
//
// # Order
//
// Per database, commits are released strictly in seq order. One told of ahead of its turn is
// held; a gap that outlasts GapTimeout (10 ms), or HoldLimit commits held, is filled from the
// changelog, read in one statement bounded by the sequencer's seq, so all it returns is
// committed. A commit whose outcome is unknown (Notifier.Unknown, with its seq, or 0 when the
// stamp's reply was lost) is settled from the changelog without RESET: read at once, then every
// 20 ms for 10 s, then every second until released. Only when the changelog no longer holds a
// commit the hub missed (retention) do the database's subscribers resync. The hub keeps state
// only for databases with subscribers; the first subscriber's read of the last commit starts it.
//
// # Using it
//
// A stream subscribes to its database, adds a target, then takes its result:
//
//	sub, err := hub.Subscribe(ctx, db)         // one per stream and database; sub.Close() at the end
//	t, err := sub.Add(listen.Spec{ID: id, Parent: parent, Query: q}) // or Documents: paths
//	head, err := t.Snapshot(ctx, func(d core.Document) error { ... }) // the result at head.Seq
//	for {
//		select {
//		case <-sub.Ready():
//			b := sub.Take() // b.Events, b.Through, b.Resync
//		case <-sub.Done(): // the hub closed
//		}
//	}
//
// Subscribe then snapshot (ADR-024 §5): Add registers the target at the hub's position H, and
// Snapshot reads the database's last commit S ≥ H and the result at S's commit_time (a
// commit is in it exactly when its seq is at most S). Events up to S are dropped for that target,
// later ones delivered, so none is lost or doubled. Snapshot then asks Ready to signal once
// the hub has released S: Batch.Through tells when every target has reached a commit, for the
// stream's global snapshot (ADR-026 §1.2); Await asks for any other.
//
// Each Event is one commit, with every change it makes to the subscriber's targets, judged on
// the document before and after it with the target's scope and query.Admits (filter, order
// fields, cursors; ADR-026 §2's table): Enter, Modify, LeaveExists, LeaveDeleted. A query's
// offset and limit are not judged here: the stream keeps a limit window, refilled with
// Target.ReadAt or ReadQuery at an event's commit. Target.Changes gives a resumed target's
// changes since a commit up to its snapshot, or reports that they are no longer held (the
// count path, or RESET).
//
// A subscriber queues at most LagEvents (stream_lag_events) events untaken; past it the queue
// is dropped and the next Take reports Resync, as it does when the hub lost commits to
// retention: the stream takes every target's Snapshot again before taking events (RESET,
// ADR-026 §1.4). Nothing is dropped silently, and nothing grows without bound.
//
// The files: reader.go, the store's side (core.PGStore implements Reader); hub.go, the order
// and the filler per database; subscriber.go, subscribers, targets and delivery; target.go,
// the targets' index, their judgement and their reads.
package listen
