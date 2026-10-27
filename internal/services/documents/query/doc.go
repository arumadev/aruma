// Package query is the Document Database's query engine apart from storage (ADR-025 §1, §5,
// §6): a StructuredQuery read off the wire and checked as production checks it, the order
// query.proto makes of it, and the reference path, which filters and sorts candidate
// documents in Go with value's comparison and is the arbiter every index path is checked
// against.
//
// It knows no storage: the service layer reads candidates from core (a collection, a
// collection group, or every descendant of a parent) and hands them here as Docs. It knows no
// transport either: errors are *wire.Error with production's texts, which each surface renders.
//
// The files:
//
//   - query.go: Query, the checked form of a StructuredQuery, and Parse;
//   - filter.go: the filter tree, read off the wire;
//   - validate.go: production's limits and texts on filters, orders, cursors, limit and offset;
//   - disjunction.go: the DNF count production bounds to 30, and the per-disjunct rules;
//   - order.go: the normalized order (query.proto's implicit orders) and the order of results;
//   - cursor.go: start_at and end_at;
//   - match.go: whether a document matches the filter, operator by operator, as production
//     matches (null, NaN, missing fields, arrays);
//   - reference.go: the reference path's run over candidates: offset, limit, cursors, bounded
//     memory, streaming where the scan order is the query's;
//   - aggregate.go: count (with up_to), sum and avg;
//   - nearest.go: find_nearest, answered as production answers without a vector index;
//   - partition.go: PartitionQuery's checks;
//   - plan.go: the planner, which serves a query from index entries when an index serves it
//     exactly, as abstract scans that the service layer reads from core;
//   - recheck.go: every index path candidate checked again by the reference matcher;
//   - paths.go: the path counters (documents_query_path_total).
package query
