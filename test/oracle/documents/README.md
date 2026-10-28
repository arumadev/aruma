# Document Database oracle (Firestore wire protocol)

Recorded answers that judge Aruma's Document Database from outside its code: what production
Cloud Firestore, the emulator and the client SDKs actually do, pinned by hash and replayed in CI
without network. It mirrors `test/oracle/rules/` (provenance per file, recorders kept beside the
data, a parity-exceptions file); unlike the rules oracle it also has a Go checker,
`oracle_test.go`, which runs in plain `go test ./...` (and so in `make test`).

## Oracle order, and the rule that production wins

When two sources disagree, the higher one decides (plan v0.4 §1 and ADR-028):

1. **Production**: the owner's project `chatapp-8ae21`, `(default)` database (FIRESTORE_NATIVE,
   STANDARD edition, PESSIMISTIC concurrency, europe-west1), recorded under top-level collections
   whose id starts with `aruma-ref`, with everything deleted afterwards; plus the Rules Test API
   and the Firebase console in a browser for management behaviour.
2. **The client SDK code and Google's docs**: what is sent and what is expected.
3. **The emulator and hidane**, second degree: cheap breadth where they are shown to agree with
   production.

**Production wins.** Where Aruma differs from production it is a bug, unless the difference is
listed in `parity-exceptions.json` with the test that proves it. Where the emulator or hidane
differs from production, Aruma follows production and the difference is listed there too. A
recording from a lower oracle never overrides a production recording of the same behaviour.

## Files

```
README.md                this file
manifest.json            SHA-256, oracle, generator, date and versions of every corpus file
parity-exceptions.json   every known difference between oracles, what Aruma does, and its test
phases.json              the phases of v0.4 and those closed (for planned tests)
oracle_test.go           the checker (standard library only)
corpus/                  the recorded answers, one JSON file per topic
production/              the recorders (Node 24), which write corpus/ and rebuild the manifest
```

### `corpus/`

Each file is canonical JSON (object keys sorted, two-space indent, final newline) of the form
`{schema: "aruma.documents-oracle/v1", header, cases, ...}`. The header names:

| field | meaning |
|---|---|
| `oracle` | `production`, `sdk`, `docs`, `emulator` or `hidane` |
| `target` | what was called: project, database type, edition, concurrency, location, transport |
| `generator` | the script (and arguments) that wrote the file |
| `recordedAt` | start and end of the recording run (UTC) |
| `versions` | Node, firebase-tools (used for its login only), gRPC and proto versions, or the emulator jar and Java |
| `notes` | what a reader must know to use the cases |

Normalisation, so that a re-recording differs only where behaviour differs:

- `readTime`, `updateTime`, `createTime`, `commitTime`, `expireTime`, `executionDuration` and
  `nextPageToken` values become `<key: type>` placeholders; transaction ids become a placeholder
  wherever they occur (request, response or message).
- Request strings longer than 300 characters (a 1 MiB field, a 6 KiB name) are written as
  `<string N bytes, sha256 …, starts "…">`. **Messages are never shortened or edited** except for
  the transaction-id placeholder.
- The project id stays as recorded: it is inside the base64 index links, which must stay exact.

| file | oracle | what it holds |
|---|---|---|
| `indexes-105.json` | production | 105 query shapes on a project with no composite index: verdict (`ok` or `FAILED_PRECONDITION`), HTTP status, exact message, its template and arguments, and the decoded `create_composite` / `create_index` / `create_exemption` link; each shape run on a populated and on a never-written collection |
| `indexes-105.emulator.json` | emulator | the same shapes on `cloud-firestore-emulator` v1.22.0 |
| `f0-missing-database.json` | production | REST, gRPC and Admin API calls against `databases/aruma-ref-nonexistent` |
| `f0-find-nearest.json` | production | `findNearest` on Standard with no vector index |
| `f0-explain.json` | production | `explainOptions` with `analyze` false and true, for queries and aggregations |
| `f0-errors.json` | production | value, document, id and name size limits, subcollection depth, 30 disjunctions, `in`/`not-in` sizes, unknown and reused transactions |
| `f0-descendants.json` | production | the kindless all-descendants queries of `firebase firestore:delete` and Node `recursiveDelete`, and `PartitionQuery` |
| `f0-list-show-missing.json` | production | `ListDocuments` with `showMissing` on a parent that only has a subcollection |
| `f0-cleanup.json` | production | the deletion of everything the F0 run wrote, and its verification |
| `f0-contention.json` | production | lock contention between pessimistic read-write transactions: six scenarios, three runs each (`record-contention.mjs`) |
| `values-order.json` | production | one document per edge value of every type: what was stored, `orderBy` both ways, and each value's `startAt`/`endAt` class (`record-values.mjs`) |
| `values-equality.json` | production | `==` and `in` on every value of `values-order.json`; `!=`, `not-in`, the unary filters, `array-contains(-any)` and the four range operators |
| `values-size.json` | production | each type's storage size, read from production's "exceeds the maximum allowed size" message, and the per-value cap |
| `values-validate.json` | production | what a write may not hold: depth, reserved and long names, vectors, geopoints, timestamps, references |
| `values-mask.json` | production | read masks and update masks on a plain document |
| `values-cleanup.json` | production | the deletion of everything `record-values.mjs` wrote, verified |
| `values-bson.json` | production | the maps the SDKs send for Enterprise BSON types (MinKey, MaxKey, int32, Decimal128, BSON timestamp, binary, ObjectId, regex) on Standard: malformed shapes and their messages, the stored (canonical) form, order and ties against every neighbouring type, `==`, range, `!=`, `in`, unary and `array-contains` filters, operands production refuses, each shape's storage size and index budget (`record-bson.mjs`) |
| `values-mask-vector.json` | production | read masks and update masks that reach into a vector or a BSON shape |
| `values-bson-cleanup.json` | production | the deletion of everything `record-bson.mjs` wrote, verified |
| `f1-get.json` | production | `GetDocument`: masks, missing documents, `readTime` (the setup commit, before it, an hour and more ago, the future, nanoseconds), transactions (`record-f1.mjs`) |
| `f1-batch-get.json` | production | `BatchGetDocuments`: response order, found and missing, masks, duplicates, `transaction`, `newTransaction`, `readTime`, bad names |
| `f1-list-documents.json` | production | `ListDocuments`: paging and page tokens, accepted and refused `orderBy` forms, `showMissing`, masks, the empty collection id, `readTime` |
| `f1-list-collection-ids.json` | production | `ListCollectionIds`: paging and page tokens, missing parents |
| `f1-create.json` | production | `CreateDocument`: automatic ids (length and alphabet over 25), existing and bad ids |
| `f1-update.json` | production | `UpdateDocument`: update masks, `currentDocument` (`exists`, `updateTime`, the version numbers of its message), upsert |
| `f1-delete.json` | production | `DeleteDocument`: missing documents and failed preconditions |
| `f1-write-results.json` | production | write results: time precision on both transports, their relations, when `transformResults` appears |
| `f1-resource-names.json` | production | names of another project or database inside a request, a `google-cloud-resource-prefix` that disagrees with the body, invalid API keys |
| `f1-wire-errors.json` | production | the REST front end's refusals: unknown fields and query parameters, wrong JSON kinds, syntax errors, non-object roots, bad enums, malformed and out-of-range timestamps, a Value with no type, database and project ids in the path and in gRPC's `database` field, Admin names outside `projects/`, oneof conflicts, and which carry `google.rpc.BadRequest` details (`errorDetailTypes`) |
| `f1-cleanup.json` | production | the deletion of everything `record-f1.mjs` wrote, verified |
| `f2-transforms.json` | production | field transforms (`record-f2.mjs`): `REQUEST_TIME` against `commitTime`, `increment` (integer and double mixes, int64 saturation, NaN, -0.0, missing and non-numeric fields), `maximum`/`minimum`, `appendMissingElements`/`removeAllFromArray` (equality of NaN, -0.0, int against double, maps), BSON shapes and vectors as operands and targets, several transforms on one path, overlap with the update mask, the legacy `transform` write, transforms on a missing document, `transformResults`, the size limit reached by a transform |
| `f2-commit.json` | production | `Commit`: the no-op write (its `updateTime`, `commitTime`), the empty commit, update masks on a missing document, `exists`/`updateTime` preconditions and `verify`, two writes to one document, 501 writes, a missing database |
| `f2-batch-write.json` | production | `BatchWrite`: per-write status, mixed success and failure, one document twice, labels, invalid writes, 500 and 501 writes, a missing database |
| `f2-transactions.json` | production | `BeginTransaction` (read-write, read-only, `readTime`, `retryTransaction`), `Rollback` (twice, unknown, forged, empty), `Commit` with unknown, forged, rolled-back and read-only transactions, reads and `newTransaction` on every read RPC, and `lifetime`: transactions idle or busy for 70, 271 and 300 s |
| `f2-locks.json` | production | which writes wait on a read-write transaction's query (the scanned index range, phantoms, sibling collections, `limit`, collection groups), a wounded transaction call by call, and an owner's `OPTIMISTIC` transaction |
| `f2-non-utf8.json` | production | ids, field names and strings that are not UTF-8: REST paths, query parameters and raw JSON bodies, and hand-encoded gRPC |
| `f2-cleanup.json` | production | the deletion of everything `record-f2.mjs` wrote, verified, and the strays found before the run |
| `f3-run-query.json` | production | `RunQuery` on one fixture of every value type (`record-f3.mjs`): order of mixed types, a missing ordered field, the implicit order of an inequality, cursors (partial, `__name__`, more values than the explicit order), offset and `skippedResults`, `limit` 0, projections, every operator with null and NaN, `array-contains(-any)`, OR and nested AND/OR, empty results, collection groups, kindless queries with `__name__` ranges, `transaction`/`newTransaction`/`readTime`, the gRPC stream's elements, and the missing-index error over gRPC (code 9) |
| `f3-query-limits.json` | production | the refusals of a query: `in`/`not-in`/`array-contains-any` sizes, 30 disjunctions, one `!=`/`not-in`/`IS_NOT_*` per query, `not-in` with `IN`/`OR`, one `array-contains` per disjunction, 10 inequality fields, 100 filters + orders + ancestor, cursors, `limit`/`offset`, field paths, `__name__` filters of the wrong type, filter and `from` structure, some over gRPC |
| `f3-aggregations.json` | production | `count` (`upTo`), `sum` (int64 overflow to double, mixed types, NaN, the infinities, the empty set), `avg`, up to five aggregations, aliases (absent, duplicate, reserved, 1500 bytes), operators, the missing-index error over gRPC |
| `f3-partition-query.json` | production | `PartitionQuery` over a collection group of 3000 documents (counts, paging, `readTime`), and every refused shape |
| `f3-admin.json` | production | read-only Admin API: the database, composite indexes, field overrides with firebase-tools' filter, `__default__/fields/*`, one field, missing indexes and operations, page sizes and filters refused |
| `f3-index-entries.json` | production | per-document index entry limits reached by writes: 40,000 entries (fields, array elements, map subfields), the entry size against the document name, and the transaction size limit a document with long-named entries meets first (also on its delete) |
| `f3-cleanup.json` | production | the deletion of everything `record-f3.mjs` wrote, verified |

F1 files hold a `setup` section (the commits written before the cases) and mark with
`replay: true` the cases whose answer depends only on that setup: `test/differential/documents`
replays exactly those against Aruma, in the normal test run against an Aruma in process
(`TestReplayCorpusInProcess`, the F3 cases by the planner and by the reference path), and
against a running server with `ARUMA_DOCUMENTS_URL` (`TestReplayCorpusAgainstAruma`). A case
that still differs is listed there with its parity exception. `TestF3CorpusReplay`
(`internal/services/documents/grpc`) sends the F3 cases over gRPC.

The analysis of the F0 recordings is in `.cache/briefs/research/f0-production-recordings.md`; the
composite-index rule derived from `indexes-105.json` is in
`.cache/briefs/research/firestore-composite-index-evidence.md`.

### `manifest.json`

Generated, never edited by hand: every recorder calls `rebuildManifest` (`production/lib.mjs`)
after it writes, which hashes every file in `corpus/` and copies its header's oracle, generator,
date and versions. After a deliberate hand edit of a corpus file run
`node production/rebuild-manifest.mjs` and give the reason in the commit.

`writeCorpus` also puts the SHA-256 of the recorder script into `header.versions["generator
sha256"]`, and the checker compares it with the script in the tree. A recorder edited after
its recording therefore fails `TestCorpusHeaders` until it is run again, so a changed recorder
and its changed corpus show up together in a diff. `lib.mjs` itself is not hashed. The hashes
of the files recorded before this check were added by hand, from the scripts that wrote them.

### `parity-exceptions.json`

One entry per known difference: `id`, `summary`, what `production`, the `emulator` and `aruma`
do, `follows` (always `production`), the `evidence` corpus files, and `test`, written
`path/to/x_test.go:TestName`, or `planned: {test, phase}`, the test a later phase of
`phases.json` writes, or both (`test` for what is proven today, `planned` for the rest). A
planned test must not exist yet (once written it moves to `test`), and its phase must still be
open: closing a phase in `phases.json` fails CI for every entry still planned in it. The Firestore section of `docs/reference/compatibility.md`
summarizes it.

### How CI checks it

`oracle_test.go`, in `go test ./...`:

- `TestManifestPinsCorpus`: every manifest entry's file exists and has that SHA-256 and header;
  **every corpus file has an entry** (a new file without one fails).
- `TestCorpusHeaders`: every file names its oracle, generator, date and versions; a generator
  under `test/` exists and **hashes to the header's `generator sha256`**; case ids are unique.
- `TestParityExceptionsNameTests`: every exception is complete, has one of the three kinds,
  follows production, cites pinned corpus files (or, with none, says its production behaviour is
  `Not recorded`), and **names a test function that exists**: a top-level
  `func TestX(t *testing.T)` found by parsing the file, so one inside a comment does not count,
  that never calls `Skip`, `Skipf` or `SkipNow` (as the rules oracle binds each exclusion to its
  cases), or a planned test of an open phase (`phases.json`). `TestTestExistsRefusesFakes` and
  `TestPlannedTestsChecked` hold the checker to that.
- `TestIndexCorpus`: each refusal's template reproduces its message byte for byte, each link's
  payload decodes (protobuf, `google.firestore.admin.v1.Index`) to the recorded index, and the
  empty collection got the populated collection's verdict.
- `TestIndexEnforcementEmulatorDiffers`: proves the one parity exception.

The value and index corpora are replayed against Aruma's code by the packages they judge:
`internal/services/documents/value/corpus_test.go` replays every `values-*.json` (order, ties,
equality and range filters, sizes, validation, masks, the BSON shapes), and
`internal/services/documents/index/corpus_test.go` replays `indexes-105.json`.

## Recording safety

- Writes only under top-level collections starting with `aruma-ref` (`assertScoped` in
  `production/lib.mjs` refuses anything else before a request leaves the machine); the F0 run
  used `aruma-ref-f0`, `-f0-tree`, `-f0-tree2`, `-f0-vec` and `-f0-explain`, the index probe
  `aruma-ref-idx` and its subcollections `aruma-ref-idxcg`, the value runs `aruma-ref-values*`
  and `aruma-ref-bson*` (each swept by its own `__name__` range, since other recorders may run
  at the same time), the contention run `aruma-ref-txn`, the F1 run `aruma-ref-f1-*` (swept
  over `aruma-ref-f1` .. `aruma-ref-f2`; its cleanup also deletes, and lists as `strays`, any
  document in that range the run did not track, such as one left by an interrupted run), and the
  F2 run `aruma-ref-f2-*` (swept over `aruma-ref-f2` .. `aruma-ref-f3` before the run and after it;
  its lock probe uses subcollections named `aruma-ref-f2-lock-items` so that its collection-group
  query reads only its own documents), and the F3 run `aruma-ref-f3-*` (swept over `aruma-ref-f3` ..
  `aruma-ref-f4`, paged; documents too big to delete by one commit are emptied 1000 fields at a
  time first; a query answer that holds a document outside `aruma-ref*` stops the recorder, so
  the project's own data never reaches a corpus file).
- Everything written is deleted by commit at the end, and the deletion is verified: one kindless
  all-descendants query over every collection id from `aruma-ref` up to `aruma-reg`, plus
  `showMissing` lists of each collection used (`f0-cleanup.json`, `cleanup` in
  `indexes-105.json`).
- No index, field override, rule, TTL policy, database or project setting is created or changed,
  no Firebase Auth user is created, and nothing touches billing.
- The access token comes from the firebase CLI login through firebase-tools' own `lib/auth.js`
  (as `test/oracle/rules/tools/rulesapi.mjs`). It is never printed, logged or written; it travels
  only in the `Authorization` header or gRPC metadata. The recorded calls run as the project
  owner, so security rules do not apply to them.

## Re-recording

Needs Node 24, a logged-in firebase CLI (`firebase login`) with access to the project, and, for
the gRPC half, the protos of `@google-cloud/firestore` (for example `npm pack
@google-cloud/firestore@9.3.1` unpacked in a scratch directory). gRPC uses the `@grpc/grpc-js`
and `@grpc/proto-loader` that ship inside firebase-tools; nothing is installed. On the owner's
machine Node's connections to Google need IPv4 first:

```sh
export NODE_OPTIONS="--no-network-family-autoselection --dns-result-order=ipv4first"
export FIREBASE_TOOLS=/opt/homebrew/lib/node_modules/firebase-tools   # the default
export FIRESTORE_PROTOS=<unpacked @google-cloud/firestore>/build/protos  # optional: gRPC cases
cd test/oracle/documents/production

node record-f0.mjs                       # all F0 topics (about a minute), then cleanup
node record-f0.mjs --only errors,explain # some topics; the cleanup always runs
node record-indexes.mjs                  # the 105 shapes on production (about 2 minutes)
node record-indexes.mjs --emulator 127.0.0.1:8086   # the same on a running emulator
node record-indexes.mjs --import <composite-index-probe.json>  # convert a recorded probe
node record-values.mjs                   # the value model, then cleanup
node record-bson.mjs                     # BSON shapes and vector masks (about 2 minutes), then cleanup
node record-contention.mjs               # lock contention between transactions
node record-f1.mjs                       # the F1 unary RPCs and wire errors (about 9 minutes), then cleanup
node record-f2.mjs                       # writes, transforms, transactions, locks (about 10 minutes), then cleanup
node record-f2.mjs --only=locks,commit   # a probe of some topics: cleanup verified, no corpus file
                                         # (F2_PROBE_OUT=<file outside the repo> keeps its results)
node record-f3.mjs                       # queries, aggregations, partitions, Admin GETs, index entry limits (about 9 minutes), then cleanup
node record-f3.mjs --only=limits,admin   # a probe (F3_PROBE_OUT=<file outside the repo> keeps its results)
node rebuild-manifest.mjs                # only after a deliberate hand edit
(cd .. && go test .)                   # the checker
```

A recorder rewrites its files and the manifest. Review the diff: a change in a message, a status
or a verdict is a change in production behaviour (or in what was sent), and the commit must say
which. The plan asks for a monthly re-recording, and one at each phase.

## Not recorded yet

These are modelled on the nearest recorded behaviour, and the code's comments say so where they apply:

- **Field paths and query filters into vectors and BSON shapes**: `orderBy v.value` or `where
  b.__int__ == 1`. Masks were recorded (`values-mask-vector.json`): production refuses a reserved
  segment, and a vector or BSON shape is one value to a mask.
- **How a regex or binary value longer than the 1500-byte index budget is cut**: modelled as a
  string and as bytes; each shape's budget was recorded only below the cut
  (`values-bson.json`, `truncation`).
- **Which of two reserved keys in one map is named**: production names the first in request
  order (`{__min__, __max__}` as `__min__`, recorded), which a value's sorted map cannot keep;
  it is the one known gap of `value/corpus_test.go`.
- **The number of paths an update mask may hold**: `ApplyMask` is linear in them, but the wire
  package should cap them at production's limit, which has not been probed.

- **Index limits that need an index or override to be created**: the number of composite
  indexes (200 without billing, 1000 with), of field overrides, more than 100 fields in one
  index, a second array field in one index, a vector index of more than 2048 dimensions, an
  index identical to an existing one, a query on an index still `CREATING` (R21), merges of
  declared composites (R18) and exemptions (R20). The owner forbids creating indexes or
  changing field configuration on the production project, so these wait for a test project.
- **The 8 MiB total of a document's index entries** (IX §1.5): every attempt met `Transaction
  too big. Decrease transaction size.` first (`f3-index-entries.json`, `total`).

## Pending owner action

- **A pessimistic transaction requested by an end user.** ADR-025 needs the exact status and
  message production returns when an end user (a Firebase Auth ID token, not an owner or service
  account) sends `BeginTransaction` with `readWrite` (google.firestore.v1 `common.proto` as
  shipped in `@google-cloud/firestore` 9.3.1, lines 61-62: "Firestore does not allow 3rd party
  auth requests to create read-write. transactions."; the plan quotes a newer googleapis wording
  about optimistic transactions, so both the plain and the optimistic request should be
  recorded). Recording it needs a test user, which this oracle may not create. The owner creates
  one (email/password, in `chatapp-8ae21`) and passes its ID token to a recorder; until then the
  case is not recorded.
