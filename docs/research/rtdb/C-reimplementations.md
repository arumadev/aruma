# Track C: existing RTDB reimplementations and rules evaluators

Date: 2026-10-20. Clones (all `--depth 1`) are in `/Volumes/Work/aurma-project/.cache/research/src/{flakbase,firebase-server,lark,targaryen,bolt,dynDB-x,Alternative-to-firebase-rtdb,firebase-realtime-db-emulator}`. Nothing was built or run.
Notation: **[V]** verified in code/docs (repo-relative path, line, commit), **[I]** my inference, **[U]** unverified.
Web search via Firecrawl was unavailable (account out of credits), so discovery of other projects used `gh search repos` and my own knowledge; the "other projects" section says which is which.
Lark (AGPL-3.0) was read for ideas only. Everything about it below is described in my own words; no code was copied.

## 0. Inventory

| Project | Lang | License | Last commit (clone HEAD) | Size | Stars | Status |
|---|---|---|---|---|---|---|
| flakbase `IguteChung/flakbase` @47ead96 | Go 1.12 | Apache-2.0 (`LICENSE`, no NOTICE file) | 2019-06-10 | 3,343 Go lines incl. 1,100 test lines | 4 | abandoned, 1 open issue ("Esprima AST Parser") |
| firebase-server `urish/firebase-server` @850f50e | TypeScript | MIT (`package.json:26`; no LICENSE file, file headers say MIT) | 2020-09-11 | index.ts 475 + lib 244 + tests ~1,000 | 670 | archived, "Project Deprecated!" in README |
| Lark `lark-sh/lark` @c909918 (v0.3.0) | Rust server + Go edge | AGPL-3.0 (`LICENSE`); CLA required for contributions | 2026-10-11 | about 102k lines (Rust server+blob, Go edge) | 14 | active; created 2026-05-25, first public release 0.2.0 on 2026-08-11 (`CHANGELOG.md`) |
| targaryen `goldibex/targaryen` @e4151e7 (v3.1.0) | JavaScript | ISC (permissive; `LICENSE`) | 2018-05-13 (pushed 2019-01) | lib 4,400 lines | 240 | unmaintained, 8 open issues |
| Bolt `FirebaseExtended/bolt` @a0a3003 | TypeScript | Apache-2.0 | 2020-08-07 | src ~3,560 lines | 897 | unmaintained |
| dynDB-x `lanicon/dynDB-x` @8955dcd | C# (.NET Core preview) | AGPL-3.0 | 2016-12-18 | ~820 C# lines | 0 | experiment |
| Alternative-to-firebase-rtdb @9228d46 | Node.js + Postgres | MIT | 2021-08-14 | 318-line server | 0 | demo |
| firebase-realtime-db-emulator "FireMongo" @af7b0e8 | Python (FastAPI) + MongoDB | GPL-3.0 | 2026-08-03 | app/ ~1,350 lines | 0 | small, active |

Sources for stars/dates: `gh api repos/<owner>/<repo>`.

## 1. flakbase (Go, Apache-2.0): main candidate base

### 1.1 Verdict first
**[V]** It is a 3.3k-line proof of concept, not a base to build RTDB on. It does not evaluate security rules at all, ignores transaction hashes, auth, onDisconnect, server values and priorities, and has concurrency bugs in the message path (details below). **[I]** Its value to Aruma is as a compact map of the Firebase wire-field names and as a list of what is easy to get wrong. I would not copy-adapt it.

### 1.2 Architecture
- **Process model [V]**: one Go process, `net/http` + gorilla/websocket, port 9527 (`pkg/net/config.go`). A single handler serves both REST (`*.json`) and WebSocket on `/`; it switches on the `Upgrade` header (`pkg/net/handler.go:26-43`). No namespace/project concept: one database per process. No `.lp` long-poll, no SSE streaming REST.
- **Connections [V]**: per connection, one reader loop and one writer goroutine draining an unbuffered `ListenChannel`; writes to the socket are guarded by a mutex (`handler.go:55-80`). Every inbound request is handled in its own `go func()` (`handler.go:90`), so two writes from one client can be applied out of order. **[I]** This breaks the Firebase guarantee that a client's operations apply in send order.
- **Write to listener [V]**: `HandleSet`/`HandleUpdate` write to the DB, then `callbackRef` finds registered listeners whose path is an ancestor or descendant of the changed path, and for each listener re-reads the whole listened subtree (with its query) and pushes the complete value (`pkg/store/impl.go:14-153`, `pkg/store/listener.go:48-60`). So no deltas: every change re-sends the full snapshot at the listened path. `listeners.find` and the loop in `callbackRef` read the listener map without taking the mutex (`listener.go:48`), a data race with `register`/`unregister`. A slow client blocks the writer because the channel is unbuffered and unbounded work is done inline.
- **Storage [V]**: two back ends behind a `db.DB` interface (`pkg/db/db.go`): in-memory nested `map[string]interface{}` guarded by one RWMutex (`pkg/db/memory/client.go`), and MongoDB (`pkg/db/mongodb/client.go`, 470 lines). Mongo model: a "collection table" document per path with a hash, and one Mongo collection per path that has a `.indexOn` rule; data under such a path is stored as documents, updates to descendants use `$set`/`$unset` on dotted fields (`client.go:137-185`). So `.indexOn` in the rules is the only thing that decides the physical layout. Persistence = Mongo; scaling = whatever Mongo does, but listeners are in-process only, so one node. **[I]** No cross-node fan-out.
- `HandleUpdate` sets each key in a loop with a TODO "set entries in transaction" (`impl.go:46`): multi-path updates are not atomic. **[V]**

### 1.3 Protocol coverage (files: `pkg/data/query.go`, `pkg/data/message.go`, `pkg/net/websocket.go`)
| Item | Status |
|---|---|
| Handshake `{"t":"c","d":{"t":"h","d":{ts,v:"5",h:<host>,s:""}}}` | sent (`message.go:25-40`) |
| Chunked frames (client sends a count, then N frames) | read supported (`websocket.go:35-80`); server never splits its own outgoing messages |
| Ping from client `{"t":"c","d":{"t":"p"}}` | answered with a `{"t":"c","d":{"t":"o"}}`-style message (`handler.go:104`, `message.go:44-55`) |
| `l`/`q` listen, `n` unlisten | yes, with query tag `t` echoed in the data push (`message.go:84-94`) |
| `p` put, `m` merge | yes |
| `h` hash on put (transaction check), `datastale` | **not parsed**; `Request` has no hash field |
| `g` get | **no** (only `l`/`q`) |
| `auth`, `gauth`, `appcheck` | **no**; any other action returns "unknown r.D.A" and the request fails |
| onDisconnect (`o`,`om`,`oc`) | no |
| server values `.sv` | no |
| stats `s` | not handled |
| Permission denied / error replies | none; failure only logs, an `ok` is still sent (`handler.go:105-124`) |
| REST | GET/PUT/PATCH/DELETE on `*.json`, query params `orderBy`, `limitToFirst/Last`, `startAt`, `endAt`, `equalTo`, `shallow` (`pkg/net/restful.go`); CORS `*`; no POST/push, no `auth=` param, no `print=silent`, no streaming |

### 1.4 Queries
**[V]** `queryOnData` in `pkg/db/memory/client.go:90-185`: iterates the map, computes the order value (`.key`, `.value`, or nested child path), filters `startAt`/`endAt`/`startKey`/`endKey`, sorts only if a limit is set, slices for limit. Comparison is done on `fmt.Sprint(value)` strings (marked TODO), so numbers sort lexicographically ("10" < "9"), booleans/null/mixed types do not follow Firebase's ordering (null < false < true < numbers < strings < objects), and map iteration order makes unlimited results unordered. `shallow` is supported. The results are sent as a full tagged snapshot. No indexes in memory mode; Mongo mode creates indexes from `.indexOn` (`client.go:456`). `noIndex` warning (`w:["no_index"]`) exists in the ok reply path (`message.go:60-75`).

### 1.5 Rules
**[V]** `pkg/rules/rules.go` (91 lines) only loads the rules JSON and walks it by path (`Child`, `$variable` keys, `.indexOn` list). `pkg/rules/esprima/` is an AST model for single-expression JavaScript, populated by shelling out to a Node `esparse` binary writing temp files into `/tmp` (`esprima/parser.go:18-45`). **Nothing evaluates it**: no other package imports `esprima` (grep of `pkg/`), and the repo's one open issue is "Esprima AST Parser". `.read/.write/.validate`, `auth`, `data`, `newData`, `root`, `now` are all absent. Test coverage for rules: `rules_test.go` (path walking only) and one big parser test. **Not suitable as a reference for a Go rules engine** beyond showing that the AST can be shaped as Binary/Logical/Call/Member/Array/Identifier/Literal nodes (`esprima/expression.go`). **[I]** A hand-written Go parser is clearly better than depending on Node.

### 1.6 Tests
**[V]** Table-style Go tests on the in-memory and Mongo handlers (`pkg/store/handler_test.go`, 628 lines, a testify suite run against both back ends), `listener_test.go`, `query_test.go`, REST tests. No official-SDK tests, no recorded frames, no `firebase` mention in any `_test.go`.

### 1.7 Lessons / pitfalls
From reading the code (it has no real tracker history, 1 issue, 0 PRs): (a) per-message goroutines reorder writes; (b) full-snapshot re-reads per change are O(listeners x subtree); (c) string comparison ordering; (d) sending `ok` for failed operations hides errors; (e) unlocked listener map; (f) writes and reads each open/close a DB client (`Connect`/`Close` per call).

### 1.8 License
Apache-2.0: reuse permitted with the license text and a note of changes in modified files (NOTICE only if the original had one; it has none). **[I]** The licence is no obstacle, but the code is too thin to be worth importing.

## 2. firebase-server (TypeScript, MIT)

### 2.1 Architecture [V]
A test double, not a database. `new FirebaseServer(port, name, data)` starts a `ws` WebSocket server (`index.ts` ~475 lines). **Trick**: the data store is a real Firebase JS SDK client instance on an in-memory app (`this.baseRef`, `index.ts:~95`), and listeners are plain SDK `ref.on('value')` handlers whose output is forwarded to the socket (`handleListen`, `index.ts:~216`). Writes go to the SDK tree after a rules check. Rules are checked by **targaryen**: `this.targaryen = targaryen.database(rules, data)` (`index.ts:99`), then `.as(authData()).read/write/update(...)` (`index.ts:215-237`); the result's `newDatabase` replaces the checking copy. So there are two trees (SDK tree and targaryen tree) kept in step by hand. Single process, no persistence, no scaling. Every message handler is synchronous-ish per connection; on `fbRef.set` it waits for `once('value')` to ack.

### 2.2 Protocol coverage [V] (`index.ts:405-440` dispatch)
- Handshake `h` with `ts`, `v:"5"`, host name; chunked outgoing messages (`maxFrameLength` default 16384, `index.ts:143`; sends the frame count first, then slices, `~160`); inbound frame accumulation by retry-parsing JSON.
- `l`/`q` (listen; the SDK tree emits whole-value pushes, no query tags), `m` merge, `p` put with optional `h` hash -> `datastale` reply plus resend when the hash differs (`index.ts:293-327`), `auth`/`gauth` with HS256 tokens or an admin secret (`lib/token-validator.ts`, 116 lines), `.sv` timestamp replacement (`replaceServerTimestamp`, `index.ts:205`), priority writes (`.priority` path), `setTime`/`setAuthSecret` helpers for tests.
- Not implemented: `n` unlisten (not in the dispatch), `g`, onDisconnect (issue #90 asked; no code), `s` stats, queries with ordering/limits (the server just pushes the full node and the client SDK does the query client-side; **[I]** this only works because the client re-filters), appcheck, long-polling. REST server (`lib/http-server.ts`, 81 lines): GET/PUT/PATCH/POST/DELETE on `.json`, 1 MB body cap, no auth, no query params.
- `lib/firebase-hash.ts` (47 lines): the Firebase node hash used for the `h` field: SHA-1 over a canonical string (`":key:<childhash>"` for sorted keys, `typeof:value` for leaves, numbers as IEEE-754 big-endian hex, `priority:` prefix), base64. **[I]** Good starting point for a hash implementation, but it is known only to the test cases; it must be verified against the SDK's own `NodeHash` code (other track) before use: for example it treats falsy `.value`/`.priority` as absent (`if (value['.priority'])`), which looks wrong for priority 0 or "".

### 2.3 Queries / Rules / Tests
- Queries: none server-side. **[V]**
- Rules: delegated entirely to targaryen (see section 4); `setRules()` swaps the rules (`index.ts:414`). A BUG comment admits listeners are not re-checked after rules or auth change (`index.ts:~218`, "tryRead() here, and if it throws, cancel the listener").
- Tests: mocha specs drive the **official firebase JS SDK** against the server (`test/server.spec.ts`, 676 lines), plus hash, token-validator and HTTP specs. That is the pattern worth copying (real SDK as the conformance client).

### 2.4 Issues that show hard parts (`gh issue list`, all closed on archive)
#38 frame size of messages above the limit must be split (chunking); #44/#52 process never exits (connection cleanup); #48 nested security rules not processed; #56 "redirect requests to real database"; #84 clients falling back to port 80 when the connection drops (host/`ns`/`s-` handling); #87 "Send failed: not opened" (writing to a closed socket); #88 unexpected auth format from Android (`gauth` vs `auth`); #90 onDisconnect; #104 wss; #131 `.info/connected` is client-side; #142 `{}` returned instead of `null` for empty paths; #162 multiple clients. **[I]** Most reported pain was SDK-version drift (#50, #57, #64, #65 "Upgrade Required"): the wire protocol is stable at `v=5` but SDK expectations change.

### 2.5 License
MIT: copy with notice permitted. **[I]** Take ideas and maybe the hash function after verification; the architecture (real SDK as the store) does not transfer to a Go server.

## 3. Lark (Rust + Go, AGPL-3.0): ideas only

### 3.1 Architecture (own-words summary)
- **Shape [V]** (`README.md`, `CONTRIBUTING.md` "Technical underpinnings"): two binaries. `lark-edge` (Go, `edge/`) terminates TLS and WebSocket/WebTransport/long-poll/SSE/REST, validates JWTs, and multiplexes many clients onto a few TCP connections to `lark-server` (Rust, Glommio, thread-per-core with `io_uring`). Each database is owned by exactly one core and processed as a single-threaded actor draining an inbox, so writes within a database are serialised without locks. The gateway hashes the database id to pick the core.
- **Write path [V]** (`docs/WRITE_LIFECYCLE.md`): arrival at the edge, route to the database's inbox, batch dispatch (up to 128 messages or about 10 ms per loop), de-duplication, value processing (server values), rules check, tree mutation plus WAL append, then a "view manager" finds affected subscriptions and builds events, and a single broadcast frame listing recipient client ids is sent to the edge, which fans out. Event order is therefore the mutation order inside the actor.
- **Storage [V]** (`CONTRIBUTING.md` "Storage"): per database a JSONL write-ahead log (flushed every 2 s by default or fsync-before-ack in strict mode), plus a compacted binary blob file loaded lazily; unloaded subtrees are placeholders; idle loaded subtrees are evicted after about 30 s; background incremental compaction. Immutable shared-structure tree values (copy-on-write) make snapshots cheap. Persistence-less "ephemeral" databases also exist.
- **Scaling [V]** (`TESTING.md` intro): the tested configuration is one server scaling vertically with one or more edges; running several server nodes is "possible" but not covered by their tests.
- **Isolation [V]**: database id is `project/database`; data dir per `{project}/{database}`.

### 3.2 Protocol coverage
- Lark has its **own** native wire protocol (short-key JSON: join, auth, set, update, delete, subscribe, once, onDisconnect, transaction; `docs/LARK_WIRE_PROTOCOL.md`) and a **Firebase adapter** that translates Firebase frames into it (`server/src/transport/firebase_adapter.rs`, 2,357 lines). **[I]** This translate-to-internal-model design is the same decision Aruma will face.
- Firebase actions handled by the adapter [V] (`firebase_adapter.rs:526-548`): `q` listen (with tag; the history `h` hash is documented as ignored, `:132`), `n` unlisten, `g` get, `p` put (with `h` hash -> compare-and-swap, `:671`), `m` merge, `o`/`om`/`oc` onDisconnect put/merge/cancel; `s` stats ignored. `auth`/`gauth` are resolved by the edge (`edge/proxy/client.go:855`, `:1291`), which verifies Firebase ID tokens, custom tokens, legacy HS256 tokens and its own tokens (`CONTRIBUTING.md` "Auth tokens"), then pushes the resulting auth state to the backend, which re-validates open subscriptions on change. App Check: no hits for appcheck in `edge/` or `server/src` **[V by grep]**. Long-poll `/.lp` exists (`edge/proxy/larkdb.go:12,246`, `longpoll.go` 969 lines), SSE and REST exist. Host form is `https://{database}--{project}.larkdb.net` (`larkdb.go:12`), i.e. database and project in the host, not `?ns=`.
- Framing [V]: outgoing messages over 16 KB are split with a leading count; split only at UTF-8 character boundaries (fixed in 0.3.0 because browsers close the socket with code 1007 on a frame that is not valid UTF-8); inbound frame size limit counted in UTF-16 units, so allow up to 3 bytes per unit; total reassembly cap 16 MB, max 1,024 frames (`firebase_adapter.rs:25-50`).
- Transactions: compare-and-swap on the hash of the current value, accepting both Firebase SHA-1/base64 hashes and its own SHA-256 form; an empty hash with a "provided" flag means "only if absent" which is what the SDK sends when it has no cached value (`LARK_WIRE_PROTOCOL.md`, "Compare-and-Swap"). A failed CAS does not poison later writes.
- Tainted writes: a client sends the ids of its still-pending earlier writes with each write; if any of those was rejected, the new write is dropped silently, because the client SDK has already rolled it back (`LARK_WIRE_PROTOCOL.md`, "Local-First Writes"). **[I]** The Firebase SDK itself has a related behaviour (it re-sends and expects in-order acceptance); I have not established what the SDK sends on the wire for this, so treat this as a Lark extension, not a Firebase requirement.
- Reconnect de-dup: server remembers processed request ids per connection (bounded, 500) and acks a repeat without re-applying; a returning client can quote its previous connection id.
- Volatile paths (rule-flagged `.volatile`): unacked, coalesced, batched at 20 Hz (UDP-like transports) or 4-7 Hz (WebSocket). A Lark extension. **[I]** Not needed for RTDB compatibility.

### 3.3 Queries [V]
`server/src/db/query.rs` (1,017 lines), `server/src/db/subscription/{query_view,incremental_sort}.rs`: queries become "views" that keep a sorted window incrementally updated as writes arrive; mixed-type sort key ordering null < bool < number < string < object (`CONTRIBUTING.md` "Data model"); array coercion rule (a node reads back as an array only if all keys are canonical non-negative integers and `maxKey < 2 * numKeys`). Tagged results: events carry the query `tag` and are expressed as put/patch with a path relative to the listened path (`LARK_WIRE_PROTOCOL.md` "Event Types"; the adapter converts them to Firebase `d` pushes).

### 3.4 Rules [V]
Own Rust rules engine in `server/src/rules/` (lexer, Pratt-style expression parser, evaluator, lazy snapshots; about 7k lines incl. unit tests). Supports `.read`, `.write`, `.validate`, `.indexOn` (`parser.rs:283`), `$wildcard` capture, variables `auth`, `data`, `newData`, `root`, `now`, `query`, and a Lark-specific `lark`; snapshot methods `val/exists/child/parent/hasChild/hasChildren/isString/isNumber/getPriority`; string methods `contains/beginsWith/endsWith/replace/matches/toLowerCase/toUpperCase/length`, with the regex compiled at rule-parse time (`expr/value.rs:290-440`, `:402`). Rules authorize a write by evaluating the cascade and splitting `.validate` into "only for children actually written"; changelog 0.3.0 says validate is skipped on nodes that the write leaves null. `newData` is never materialised eagerly: it is an overlay of the write on the current tree, and only the paths a rule touches are loaded. Integration tests: `server/tests/integration_rules.rs` (43 tests). Edge also has a tiny Go rules reader that only extracts `.volatile` paths (`edge/rules/rules.go`, 190 lines).

### 3.5 Tests (the model to copy) [V] (`TESTING.md`)
1. **Run Google's own SDK test suite unmodified** (`@firebase/database-compat` mocha suite from a pinned firebase-js-sdk commit) against the running stack: `test/run-firebase-sdk.sh`. Pin by SHA, make the namespace/port configurable, support running one file or one test by name.
2. 22 integration suites in `server/tests/` that boot a real in-process server and speak the wire protocol through one shared harness (arrays, priorities, numbers, queries, transactions, onDisconnect, subscriptions, tainted writes, persistence, eviction, ...). Nothing is mocked below the client.
3. A **chaos-monkey** durability tool (`tools/chaos-monkey`): writes through the real wire, SIGKILLs the server at random, restarts, and checks every acknowledged write against a seeded ground-truth model, including deletes and raw on-disk state; seeds make failures replayable. The operation mix is biased toward cases that broke before (multi-path updates at root, delete then write beneath).
4. Go edge tests with the race detector in CI; JWT tests for each token type.

### 3.6 Lessons/pitfalls (from `CHANGELOG.md` 0.3.0 "Fixed", the only history available; the project has no issues, 14 stars)
- Large responses: splitting at byte offsets broke multi-byte characters and made a database impossible to load (client reconnected and re-requested forever). Test with non-ASCII data at the frame boundary.
- Per-connection outbox bounded by message count dropped clients mid-sync, reconnect loop; fix was a bound in bytes (default 256 MB, warn at 64 MB). Write deadline must scale with payload size.
- Root set/delete never reached persistent storage; delete-then-write under the same node and nulls inside multi-path updates were mishandled in compaction. **[I]** Root writes and null-in-update are the classic edge cases for any RTDB store.
- Paths with missing leading or trailing slash leaked data; two concurrent `auth` messages crashed the edge (closed channel twice); dead backend connections silently dropped messages.
- `.validate` at nodes a write nulls out (set/update/remove must all treat it the same).
- Silent drops are hard to diagnose: they moved every defensive limit to WARN-level logs with reason.

### 3.7 License implications
AGPL-3.0 plus a CLA: no code or translated code may enter Aruma. Network-use clause means even a "small helper" would force Aruma's source open. Reading the public docs for design ideas is fine; do not paste structures or constants from `firebase_adapter.rs`. The frame-size numbers (16 KB, UTF-16 units) are facts about the Firebase SDK, to be re-derived from the SDK source (other track).

## 4. targaryen (JavaScript, ISC)

### 4.1 Architecture and parser [V]
Library for testing rules, not a server. `lib/parser/index.js`: each rule string is parsed with **esprima 1.2** into an AST, then converted into typed node objects (`lib/parser/statement/*.js`: array, binary, call, conditional, expression, identifier, literal, logical, member, unary). It does static type inference per node ("Expression must evaluate to a boolean", `index.js:~30`) so invalid rules fail at load like the Firebase console. Regular-expression literals get their own parser (`lib/parser/regexp/`, 285 lines) to validate Firebase's regex dialect. Scope: wildchildren (`$x`) typed as strings; `auth` any; `root`, `data`, `newData` snapshots; `now` number; `query` (`scope.js`). Snapshot methods and string methods with stricter types are in `lib/parser/types.js` and `string-methods.js` (`contains`, `beginsWith`, `endsWith`, `replace`, `matches`). **[V]** Errors at runtime (for example calling a method on null) make the rule evaluate to false and are reported in `info`.

### 4.2 Evaluation semantics [V] (`lib/database/ruleset.js`, 417 lines)
- Read: walk from the root down the requested path; the first `.read` that evaluates true grants everything below (cascade).
- Write/update: `tryWrite` walks down the path, evaluating `.write` until one grants; and `.validate` at every level where `newData` exists; then it walks the *new data* subtree and evaluates `.validate` for every existing child that has a rule node (`ruleset.js:161-250`). `update` is evaluated as the merge of all paths with the same machinery. `.write` granted above does not skip `.validate`.
- `now` is passed explicitly (`newData.timestamp`), making rule tests deterministic.
- Result objects carry an evaluation trace per sub-expression for debugging.

### 4.3 Tests, notably Firebase parity [V]
- `test/spec/lib/parser/fixtures.json` holds **186 rule-expression cases** (rule text, auth user, data, `isValid`, `failAtRuntime`, `evaluateTo`) that were recorded by deploying each rule to a real Firebase project and evaluating it (`lib/parser/specs.js` regenerates them and reports where targaryen diverges; issue #63 "live testing for Firebase parity"). Plus `test/integration/{rules,tests}.json` (950-line rules, 1,756-line expectation file) and per-class specs.
- **[I]** These fixtures are the single most useful artefact in this track: language-neutral JSON that pins how Firebase treats type errors, null, `==` vs `===`, string methods, regex, and snapshots. The fixtures are ISC-licensed (copy with the copyright notice); a Go port can load them directly as table tests. Regenerating them requires contact with a real Firebase project, which this research must not do.

### 4.4 Issues that reveal hard semantics [V] (titles from `gh issue list`)
#154 empty string as child key behaves differently; #152 trailing backslash in regex; #134 patch allowed in targaryen but denied by Firebase; #133 `==` must deep-compare; #125 `.length` of empty string; #122 array-like collections; #121 `newData.parent().parent()` not reaching the root; #113 null values passed up the hierarchy; #108 `newData` must contain all merged data; #85/#84 comparison and equality type rules; #73 whether `.write` on a child lets you write; #61 evaluation error must deny; #60 missing `auth` properties are null; #9 "canWrite passes if any child can be written" (a bug); #45/#138 `.validate` testing missing at first; #124 update result missing new value; #130 Firestore not supported. **[I]** Cascading and merged-`newData` semantics were the main bug source.

### 4.5 Suitability for a Go engine
**[I]** As a *semantic reference*: high. As a code base: no (JS, esprima 1.2). Features: `.read/.write/.validate`, `$vars`, `auth/root/data/newData/now/query`, all string and snapshot methods. Missing or stale: `.indexOn` ignored, rules after 2018 (for example newer string methods) may be missing **[U]**, no priority handling in rules beyond `getPriority` **[U]**.

### 4.6 License
ISC, equivalent to MIT: reuse with copyright and permission notice.

## 5. Bolt compiler (TypeScript, Apache-2.0)
**[V]** `FirebaseExtended/bolt` (`src/rules-parser.pegjs`, 610 lines): a PEG grammar for the Bolt language and for JavaScript-like expressions with the Firebase operator set; `src/rules-generator.ts` (1,048 lines) emits the rules JSON; `src/simulator.ts` (368 lines) evaluates rules against data for testing (via `firebase-rest.ts` it can also deploy to a real project). **[I]** Useful for the *expression grammar* (precedence table, literals, regex literal, member/call forms); Apache-2.0 so a grammar can be re-expressed in Go with attribution. Not needed otherwise (Bolt is a different input language that compiles to the JSON rules).

## 6. Smaller projects

| Project | What it is | Verdict |
|---|---|---|
| dynDB-x (C#, AGPL) | REST-only, Firebase REST-compatible store on LiteDB/key-value DB. Stores JSON "flattened" into path->leaf key-value rows so a subtree read is a prefix scan; reuses the Firebase push-id algorithm. No WebSocket, no rules, no auth. | Idea: flattened path/leaf rows (**[I]** that is the same trade-off as one-row-per-node in Postgres). AGPL: ideas only. |
| Alternative-to-firebase-rtdb (Node + Postgres, MIT) | Demo comparing Firebase and Postgres for a task-board app: a `ws` server that, after a SQL change, tells connected browsers to refetch. Own protocol, not Firebase-compatible, relational tables. | Nothing to reuse; confirms "Postgres + WebSocket" is easy for a demo and tells nothing about RTDB semantics. |
| FireMongo / firebase-realtime-db-emulator (Python FastAPI + MongoDB, GPL-3.0) | REST-only (GET/POST/PUT/PATCH/DELETE and an index endpoint), query engine in `app/domain/query_engine.py` (118 lines) with `orderBy` + filters, OpenAPI docs, two storage models (nested documents, then flattened). No WebSocket, no rules evaluation. | GPL: ideas only; nothing beyond what flakbase has. |

## 7. Other projects worth knowing (searched with `gh search repos`, Firecrawl unavailable)
- **No other serious RTDB-wire-compatible server was found** on GitHub for Go, Rust, Java, Python. Queries run: "firebase realtime database server/compatible/clone/mock/rtdb emulator/self-hosted" and per-language variants. Hits were client demos, REST clients, or the projects above. **[I]** The field is: firebase-server (tests only), flakbase (PoC), Lark (the only production-grade one, AGPL).
- **Firebase's own emulator** runs a closed Java JAR; out of scope here (no decompiling). **[V]** `firebase-tools` downloads it (other tracks cover observation from outside).
- `lark-sh/client` (Lark's own JS client) returned 404 for `gh api`; presumably renamed or private **[U]**.
- `brianneisler/firetree` (Apache-2.0, 8 stars, 2023): AST parser/generator for **Firestore/Storage** rules, not RTDB; different language (CEL-like). **[V]** per repo description. Not applicable.
- `bijoutrouvaille/fireward` (MIT, 235 stars, Haskell): Firestore rules language, not RTDB. **[V]** description only.
- `wcandillon/firebase-bolt-compiler`, `fny/firebase-bolt-transpiler`: Bolt-to-TypeScript generators; not evaluators.
- `@firebase/rules-unit-testing` in firebase-js-sdk: needs the emulator; no evaluator of its own **[U]**, not checked in this track.
- I know of no Go package that parses and evaluates RTDB rules expressions **[U]**; `gh search` for "firebase security rules parser" and "firebase database rules evaluator" produced none.

## 8. Comparison table

| Capability | flakbase | firebase-server | Lark | targaryen | Bolt |
|---|---|---|---|---|---|
| Role | PoC server | test double | production server | rules tester | rules compiler/simulator |
| WebSocket Firebase wire v5 | partial | partial | full via adapter | n/a | n/a |
| Long-poll `.lp` | no | no | yes | n/a | n/a |
| REST | partial | partial | yes (+SSE) | n/a | deploy only |
| listen/unlisten/tags | listen+tag, unlisten | listen only | yes | n/a | n/a |
| put/merge | yes (merge not atomic) | yes | yes (atomic, actor) | n/a | n/a |
| hash CAS / `datastale` | no | yes | yes (hash accepted) | n/a | n/a |
| onDisconnect | no | no | yes | n/a | n/a |
| server values `.sv` | no | timestamp only | timestamp + increment | n/a | n/a |
| auth/gauth | no | yes (HS256/secret) | yes (edge JWT, 4 token kinds) | `auth` object only | n/a |
| App Check | no | no | no (grep) | n/a | n/a |
| Query orderBy/limit/range | yes, string-compared | no (client side) | yes, incremental views, correct type order | n/a | n/a |
| Indexes (`.indexOn`) | Mongo indexes | no | parsed | ignored | n/a |
| Rules evaluation | no | via targaryen | own engine | yes (reference) | simulator |
| Delta events | no (full snapshot) | no | yes (put/patch) | n/a | n/a |
| Persistence | memory/Mongo | none | WAL + blob | n/a | n/a |
| Multi-node | no | no | not tested | n/a | n/a |
| Official-SDK tests | no | yes (JS SDK) | yes (SDK's own suite) | Firebase-live fixtures | simulator tests |
| License | Apache-2.0 | MIT | AGPL-3.0 | ISC | Apache-2.0 |

## 9. What Aruma should take from each (inference)

- **flakbase**: only as a wire-field cheat sheet (`pkg/data/query.go` keys `a/r/b/p/d/t/q`, query fields `sp sn ep en i l vf`, chunked-read count-prefix, ping/pong shape). Do not copy code. Copy-adapt vs own: **write our own**; the reusable fraction is perhaps 300 lines of struct definitions, and its design choices (goroutine per message, full snapshots, string compare) are the ones to avoid. If any struct is copied, keep the Apache-2.0 header and add a NOTICE entry, but I would rather re-derive field names from the SDK sources (other tracks).
- **firebase-server**: (a) its test approach (the official JS SDK as the conformance client against a throwaway server); (b) the hash function as a first draft after checking it against the SDK source; (c) the `datastale` flow: on hash mismatch send the current data then the `datastale` status; (d) a list of the client actions in a minimal server: `l/q`, `m`, `p` (+`h`), `auth/gauth`; (e) lack of unlisten/onDisconnect there tells us which pieces are easy to forget.
- **Lark** (ideas only): (a) the Firebase adapter as a *translation layer* to an internal write/subscribe model, with auth resolved before the data layer; (b) single-writer-per-database ordering (an actor/queue per project or database) as the answer to "concurrent writes without lost events"; (c) events as put/patch deltas from one ordered mutation stream; (d) incremental query views; (e) bounded outbox by bytes, write deadlines proportional to payload, WARN logs on every limit hit; (f) UTF-8-safe frame splitting and UTF-16 inbound limits; (g) processed-request-id window for reconnect de-duplication; (h) test stack: run the unmodified SDK suite pinned by commit + a seeded kill-and-recover durability tool + a shared real-wire harness. **Do not** adopt its volatile paths, thread-per-core or blob format; they solve problems Aruma does not have in v0.2.
- **targaryen**: (a) load `test/spec/lib/parser/fixtures.json` (186 Firebase-verified expression cases, ISC) as the golden table test for the Go rules evaluator, keeping the copyright notice; (b) the cascade semantics and the `tryWrite`/`validate` walk, then its issue list as a test-case checklist; (c) static type inference at rule-load time so bad rules are rejected at deploy rather than at request time; (d) deterministic `now` injection and per-expression trace for a "rules debugger" in the dashboard.
- **Bolt**: expression grammar and precedence as the source for a hand-written Go recursive-descent parser (Apache-2.0, attribute); the simulator as a second oracle.
- **Small projects**: dynDB-x's flattened path/leaf representation is a data point for the storage-model decision (path-keyed rows in Postgres). Nothing else.
- **Rules engine in Go [I]**: write ourselves. Reasons: no usable Go code exists (flakbase has none that evaluates); Go's `regexp` is RE2, which is likely closer to Firebase's regex dialect than JS regex (**[U]**, unverified, check with the fixtures); a small Pratt/recursive-descent parser plus typed evaluator is a few thousand lines, and targaryen's fixtures plus Lark-style integration suites give the test oracle.

## 10. Source index
- flakbase: `pkg/net/handler.go`, `pkg/net/websocket.go`, `pkg/net/restful.go`, `pkg/data/query.go`, `pkg/data/message.go`, `pkg/store/{impl,handler,listener}.go`, `pkg/db/memory/client.go`, `pkg/db/mongodb/client.go`, `pkg/rules/**` (all @47ead96).
- firebase-server: `index.ts`, `lib/firebase-hash.ts`, `lib/http-server.ts`, `test/server.spec.ts` (@850f50e); issues via `gh issue list -R urish/firebase-server`.
- Lark @c909918: `README.md`, `TESTING.md`, `CONTRIBUTING.md`, `CHANGELOG.md`, `docs/LARK_WIRE_PROTOCOL.md`, `docs/WRITE_LIFECYCLE.md`, `server/src/transport/firebase_adapter.rs`, `server/src/rules/**`, `edge/proxy/{client,larkdb,longpoll}.go`, `test/run-firebase-sdk.sh`.
- targaryen @e4151e7: `lib/parser/**`, `lib/database/ruleset.js`, `test/spec/lib/parser/fixtures.json`, `CHANGELOG.md`; issues via `gh issue list -R goldibex/targaryen`.
- Bolt @a0a3003: `src/rules-parser.pegjs`, `src/rules-generator.ts`, `src/simulator.ts`.
- Stars/dates/licenses: `gh api repos/...` on 2026-10-20.
