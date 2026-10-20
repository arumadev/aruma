# Track D: articles, docs, talks informing the RTDB design (2026-10-20)

Method: web research only (WebSearch/WebFetch, `curl` of public GitHub raw files). Legend: [V] = read on the primary page this session; [S] = secondary/third-party; [U] = unverified or could not confirm; [I] = my inference.
No code was cloned. Licenses noted where a project is discussed. No AGPL/GPL code read.

---------------------------------------------------------------------------------------------------
## 1. How RTDB works inside / published limits

### What is public (primary)
| Fact | Value | Source |
|---|---|---|
| Simultaneous connections per database | 200,000 (Spark: 100) [V] | https://firebase.google.com/docs/database/usage/limits |
| Simultaneous responses from one database | ~100,000/s sustained, bursts OK [V] | same |
| Write rate per database | 1,000 writes/s; "not a hard limit", sustained above may be rate-limited [V] | same |
| Bytes written | 64 MB/min [V] | same |
| Single write | 256 MB REST, 16 MB via SDKs [V] | same |
| Max string | 10 MB; key length 768 bytes; depth 32 levels [V] | same |
| Single read response | 256 MB; path with listener/query max 75 million nodes cumulative; query max 15 min [V] | same |
| Event size to Cloud Functions | 1 MB; 1,000 functions per write [V] | same |
| Databases per project (Blaze) | up to 1,000 instances; queries cannot span instances; one app instance connects to one DB at a time; each has its own rules [V] | https://firebase.google.com/docs/database/usage/sharding |
| Cost model | "each write can take up 0.1% of your total database utilization"; batching with multi-path update reduces cost; deleting paths with thousands of leaves is "potentially expensive" [V] | https://firebase.google.com/docs/database/usage/optimize |
| Query cost | `orderByChild` can be "6-8 times slower"; `orderByValue` "very slow for large data sets"; listen low in the tree [V] | same |

### What is NOT public
- "Single-threaded", "one machine per database", the internal storage engine, how the server indexes `.indexOn`, or why exactly queries are limited: **no primary source found** [U]. Searches of Firebase docs/blog (including the 2017 profiler post https://firebase.blog/posts/2017/03/profiling-your-realtime-database, which calls the DB "a black box") do not state it. A 2018 slide deck (https://www.baqend.com/files/Future-of-Real-Time-Databases-SCDM2018-slides.pdf) [S] quotes then-current Firebase docs as "~100,000 concurrent connections and 1,000 writes/second in a single database" (the connection figure later became 200k).
- The sharding page itself says the 200k / 1,000 w/s figures are *per instance* and the fix is "multiple databases" [V]. That is the only architecture-relevant statement: the unit of scale is one database instance, and a single instance has a write ceiling in the low thousands per second. [I] consistent with a single serialising writer per database, but Google does not say so.
- Why queries are limited [I, from the documented behaviour]: a query is one `orderBy*` on one child/key/value/priority plus range/limit; sort needs an index declared in rules (`.indexOn`); there are no joins and no cross-instance queries; reading a node returns its whole subtree. This is the shape you get from an in-memory ordered tree plus per-path indexes; it is not stated as the reason by Google.
- Ably (competitor) repeats 200k connections and "sharding is tedious, last resort" without Firebase attribution [S] https://ably.com/topic/scaling-firebase-realtime-database.

Implication for Aruma [I]: per-database 1,000 writes/s and ~200k conns are *Firebase's* published ceilings; matching them is not required for v0.2. A single-process Go server with one ordered writer per database can plausibly exceed 1,000 w/s, but no number should be promised without measurement.

---------------------------------------------------------------------------------------------------
## 2. Ordering and no lost events for concurrent writes

### The gotcha: sequence value != commit order
- Oskar Dudycz, "Ordering in Postgres outbox" [V]: sequences are assigned before commit, so a transaction that took a lower number can commit after a higher one; a poller that advances "last seen position" can skip it. Rolled-back transactions leave gaps and a poller cannot tell a rollback gap from an in-flight one. https://event-driven.io/en/ordering_in_postgres_outbox/
- Kevin Grittner on pgsql-general (2016) [V]: under SERIALIZABLE "that order is not necessarily commit order"; to number in commit order take `pg_advisory_xact_lock` as the last step before commit and assign the number under the lock; "some actual serialization of this small bit at the end of the transaction". https://www.postgresql.org/message-id/CACjxUsMKA6k-mDOdkos3k0i-KE4HFRwkd=PXPArYy4UabTd-LA@mail.gmail.com
- Same gotcha in another event store (Marten uses gap detection with tombstones, per Dudycz) [S].

### Options
| Option | How | Gapless | Commit-ordered | Cost / caveat | Source |
|---|---|---|---|---|---|
| A. One writer goroutine/actor per database (in-process), assign sequence in memory, persist then publish | all writes for a database pass through one queue; seq assigned in apply order; publish after commit | yes if persisted in the same tx and counter restored from DB on start | yes (by construction) | caps per-database write throughput to one thread plus one fsync pipeline (batch/group-commit to raise); only valid while ONE process owns the database (multi-node needs a lease/leader per database) | [I]; Firebase's own per-instance 1,000 w/s ceiling is consistent with this design (not confirmed) |
| B. Postgres sequence + poller | `events.changes.seq` from a sequence; poll `seq > last` | no (rollback gaps) | NO: late commits are skipped | silently loses events - this is exactly the lost-event hazard | Dudycz [V] |
| C. Singleton counter row (`UPDATE seq SET id=id+1 RETURNING id`) | number only advances on commit | yes | yes (row lock held to commit) | serialises ALL writers for the whole transaction duration; Dudycz found the cost too high for an outbox | Dudycz [V] |
| D. `pg_advisory_xact_lock(key)` as last statement before COMMIT, assign seq under it | per Grittner | no gaps from other writers' lateness; rollback gaps remain only if assign happens before rollback (assign last => none) | yes | tiny serialised window; every writer path must take the lock, a writer that skips it breaks the guarantee (stated by a project using this pattern [S], https://www.nuget.org/packages/EventStoreCore.Postgres/1.0.2-beta.2) | Grittner [V] |
| E. xid8 + snapshot watermark on the reader | store `pg_current_xact_id()` (xid8) per row; reader only takes rows with `transaction_id < pg_snapshot_xmin(pg_current_snapshot())`, order by (xid8, seq) | n/a (reader-side fence) | yes up to the xmin horizon | one long-running transaction anywhere in the database stalls delivery for everyone; ordering is by transaction START not commit; +8 bytes/row (Dudycz's caveats) | Dudycz [V] |
| F. Logical decoding (pgoutput/wal2json) with LSN | consume the WAL; changes arrive per transaction in commit order | n/a | yes | needs a replication slot: slots retain WAL while unused and "In extreme cases this could cause the database to shut down" (txid wraparound) [V]; slot position persisted only at checkpoint so re-delivery after crash is possible - consumer must dedupe by LSN [V]. Hasura rejected it for these reasons (see 3) | https://www.postgresql.org/docs/current/logicaldecoding-explanation.html |

Notes:
- The Postgres docs page on logical decoding says each slot streams changes "in the order they were made on the origin server" [V] (the page does not itself say "commit order"; pgoutput's per-transaction streaming of committed transactions is well known [U here]).
- `track_commit_timestamp` / `pg_xact_commit_timestamp` is discussed on pgsql-general (https://www.postgresql.org/message-id/bae60b38-4ab5-ba59-9c97-2df2ded8f956%40anayrat.info) [S, not read in detail]; timestamps are not unique/ordered enough to be a cursor [I].

[I] RTDB-specific: RTDB semantics need the *server acknowledgement order* and the *listener event order* to equal one total order per database. The cheapest correct design for a single binary that owns the data is A (writer-per-database) with the sequence persisted in the same Postgres transaction (so a restart resumes from `max(seq)`), and event fan-out *after* commit from memory. B alone loses events; keep `events.changes` only if the seq is assigned under D's lock or by A.

---------------------------------------------------------------------------------------------------
## 3. Fan-out to listeners on one node

### 3a. In-process
- When one binary owns writes (A above), the cheapest path is an in-process bus: after commit, the writer goroutine looks up affected listeners and enqueues events. No official source; this is the architecture of Firebase-style servers [I]. Per-listener filtering vs a path-tree index: a trie keyed by path segments with listener lists at nodes lets a write at `/a/b/c` find (i) listeners on ancestors (`/`, `/a`, `/a/b`, `/a/b/c`) by walking the path and (ii) listeners on descendants by walking the subtree under the written node [I]. Cost is O(depth + descendants-with-listeners), not O(all listeners). Supabase's per-subscriber check is the counter-example: "if you have 100 users subscribed ... single insert ... trigger 100 reads" [V] https://supabase.com/docs/guides/realtime/benchmarks (docs pages quoted via search) and the Realtime architecture doc, which filters per subscriber in `realtime.list_changes` [V] https://raw.githubusercontent.com/supabase/realtime/main/ARCHITECTURE.md.

### 3b. Postgres LISTEN/NOTIFY
| Fact | Source |
|---|---|
| Payload must be < 8000 bytes (default config); suggest sending a key and reading the row [V] | https://www.postgresql.org/docs/current/sql-notify.html |
| Queue is 8 GB; if full, NOTIFY transactions "fail at commit"; a session that LISTENs then sits in a long transaction blocks cleanup, warnings at half full [V] | same |
| Delivered only on commit; same-transaction order preserved; cross-transaction "in the order in which the transactions committed"; identical (channel,payload) in one tx is folded to one [V] | same |
| Commit-time global lock: a transaction with a pending NOTIFY takes an AccessExclusiveLock on "database 0" during COMMIT so queue entries land in commit order; one commit at a time. Recall.ai had three outages 19-22 Mar 2025 with tens of thousands of concurrent bot-update writers; moved to app-layer notification [V] | https://recall.ai/blog/postgres-listen-notify-does-not-scale |
| Counterpoint: DBOS reports 60K writes/s with LISTEN/NOTIFY after batching notifications [S] | https://dbos.dev/blog/postgres-listen-notify-scalability |
| Core change: Recall.ai's update cites commit 282b1cde9d... as removing the bottleneck. Its commit message (read via GitHub API, 2026-01-15, Joel Jacobson) is "Optimize LISTEN/NOTIFY via shared channel map and direct advancement": avoids WAKING uninterested listeners, "integer multiples" NOTIFY throughput gain when many listeners but few interested; "no improvement for the simplest case where every listener reads every message". It does not mention the commit-time lock. So Recall's attribution of the lock fix to it is **doubtful [U]**. It is in master after PG 18 (Jan 2026), i.e. not in Aruma's Postgres 18 [I]. | https://api.github.com/repos/postgres/postgres/commits/282b1cde9dedf456ecf02eb27caf086023a7bb71 |
| A proposed GUC `publish_out_of_order_notifications` (skip the global lock, accept disorder) is on pgsql-hackers [S, not verified whether committed]. | search result via postgresql.org message-id CAK80=jiWjMAfjwJymPoG3=FNE=hHygn5dXaoax1BWQT1rJrCCw@mail.gmail.com |

[I] LISTEN/NOTIFY is acceptable only as a wake-up hint (empty or small payload, one channel) for the multi-node case; do not put payloads in it and do not call NOTIFY from every write transaction at high rates on PG 18.

### 3c. Polling a change table
- Hasura live queries poll on an interval, multiplexing many subscribers into one SQL statement. Their stated reasons against triggers+NOTIFY (events can be dropped if consumer restarts) and logical replication (slots expensive, often unavailable on managed DBs, WAL bloat under heavy writes, hard to map events to complex queries) [V] https://github.com/hasura/graphql-engine/blob/master/architecture/live-queries.md . Numbers: 5,000 live queries at 60% CPU (1 vCPU/2 GB); 20,000 at 90% (4 vCPU/8 GB); 1 M live queries with Postgres at ~28% load, ~850 connections [V]. Hasura is Apache-2.0 for the engine [U: license not re-checked].
- Polling a change table with a monotonically safe cursor (options D/E above) is gap-free and needs no slot; cost is latency (poll interval) and idle load. [I]

### 3d. Logical replication (wal2json/pgoutput)
- Supabase Realtime (Apache-2.0 [V]) reads the WAL through a replication slot (ReplicationPoller calls `realtime.list_changes`); "Using Postgres Changes means Realtime streams two replication slots"; processing is single-threaded to preserve order, so more compute helps little; Supabase's own advice at scale is to use a "public" table without RLS or to re-stream via Broadcast [V/S] https://supabase.com/docs/guides/realtime/benchmarks , https://www.supabase.com/docs/guides/realtime/reports . No official Postgres Changes throughput number is published; third-party 500 writes/s load test saw growing delay [S, anecdotal].
- ElectricSQL consumes Postgres logical replication and serves "shapes" over HTTP with offsets (cache-friendly), not per-client sockets [U: architecture page fetch returned redirect then 404; not read]. 
- Both need `wal_level=logical`, a slot and a role - a real operational burden for a self-hosted single binary [I]; the cost shows when Postgres is shared with other apps.

### 3e. Multi-node later [I, from the above]
1. A database must still have one total order. Either (a) sticky routing: one node is leader for a database (lease in Postgres via advisory lock or a leases table), other nodes forward writes; or (b) all nodes write through Postgres with option D/E and read the change table by cursor, using NOTIFY only as a wake-up.
2. Supabase's model: per-tenant singleton "Connect" process owns the tenant DB pool, placed by consistent hashing/least CPU; every node otherwise identical [V].
3. Keep `events.changes` rows with (database_id, seq) so a new node can replay from a cursor; make fan-out consume the same table the single node writes.

---------------------------------------------------------------------------------------------------
## 4. Long-lived WebSockets in Go

### Libraries
| Library | License / status (GitHub API 2026-10-20) | Notes | Source |
|---|---|---|---|
| coder/websocket (ex nhooyr) | ISC; pushed 2026-06-15; maintained by Coder since 2024 [V] | context-aware API; all methods concurrent-safe except Reader/Read; default read limit 32768 bytes/message (`SetReadLimit`, -1 disables); origin check: same host allowed, others via `OriginPatterns`; `CompressionMode` default disabled ("Do not enable compression without benchmarking"); **on any error including context expiry the connection is closed**; `Close` has 5 s write + 5 s read timeouts; `Ping` waits for pong and must run while a reader is active; docs say TCP keepalive "should suffice for most use cases"; `CloseRead` for write-only connections. The README/comparison (maintainer-authored, not neutral) claims an extra goroutine for context cancellation costing ~2 KB/conn [S, from search snippet; not in the current README text I fetched: [U]] | https://pkg.go.dev/github.com/coder/websocket , https://github.com/coder/websocket |
| gorilla/websocket | BSD-2-Clause; last push 2025-03-19; not archived; README: "API is stable" [V] | one concurrent reader and one concurrent writer; app must read to process control frames; `SetWriteDeadline`; prepared messages; configurable buffer sizes; permessage-deflate only no-context-takeover (per coder README comparison [S]); `CheckOrigin` default: same-origin check [U, doc page 500]. | https://github.com/gorilla/websocket |
| gobwas/ws | MIT; pushed 2026-02-12 [V] | low-level, zero-copy upgrade (`BenchmarkUpgradeHTTP` 8576 B/op, 9 allocs vs `UpgradeTCP` 0 B/op) used with own netpoll [V]; API is much more manual | Kamardin article below |

### Memory per connection (numbers)
| Design | Per connection | Source |
|---|---|---|
| Goroutine-per-conn, `net/http`, two goroutines + 4 KB bufio each way + HTTP buffers | ~24 KB (72 GB for 3 M conns, author's own accounting excluding app structs) [V] | Kamardin, "A Million WebSockets and Go" https://www.freecodecamp.org/news/million-websockets-and-go-cc58418460bb/ |
| Same, as presented by Eran Yanay (GopherCon IL) | ~20 KB (20 GB / 1 M) [V] | https://speakerdeck.com/eranyanay/going-infinite-handling-1m-websockets-connections-in-go |
| epoll event loop | -30% [V] | same |
| epoll + gobwas/ws buffer tricks | ~600 MB / 1 M = ~600 B/conn (-97%) [V]; excludes application state | same |
| Phoenix (Elixir) | 2 M conns on a 128 GB / 40-core box, ~38 GB used = ~19 KB/conn [S, 2015, derived] | https://phoenixframework.org/blog/the-road-to-2-million-websocket-connections |
- [I] For Aruma v0.2 goroutine-per-connection (2 goroutines: reader + writer with bounded channel) costs on the order of 10-25 KB/conn plus the listener index; 10k connections is ~250 MB worst case, which is fine. An epoll design is a 100k+ connection optimisation, not needed (Firebase's own cap per database is 200k).
- Kernel: raise `RLIMIT_NOFILE`; conntrack table size for large counts (Yanay) [V].
- Kamardin also bounds worker goroutines with a pool and `ScheduleTimeout` on accept so overload sheds rather than exhausts resources, and warns about reconnect storms ("self-DDoS") [V].

### net/http interaction (verified in Go source, master)
- `conn.hijackLocked` calls `rwc.SetDeadline(time.Time{})` - i.e. current Go clears the server's Read/WriteTimeout deadlines on hijack - BUT the exported `Hijacker` doc still says: "The returned net.Conn may have read or write deadlines already set, depending on the configuration of the Server. It is the caller's responsibility to set or clear those deadlines as needed." [V] https://github.com/golang/go/blob/master/src/net/http/server.go (func hijackLocked; Hijacker doc). Issue #8296 documents the historical problem [S/V title]. 
- [I] Practical rule: Server `ReadTimeout`/`WriteTimeout` 60 s remain valid for plain HTTP routes; after upgrade the WebSocket library owns deadlines. Still call `SetDeadline(time.Time{})` defensively if using a library that hijacks, and use per-write deadlines. `ErrHijacked`/`StateHijacked` mean the server no longer tracks the connection, so `Server.Shutdown` will NOT close websockets - Aruma must track and close them itself [V for StateHijacked: https://pkg.go.dev/net/http].
- No h2c: browsers/SDKs use HTTP/1.1 Upgrade for `wss://`, so no h2 WebSocket (RFC 8441) is needed [I].

### Slow consumers
- Sources: Kamardin (resource limits, no write deadlines in the article) [V]; NATS closes a connection when a write blocks beyond `write_deadline` (https://docs.nats.io/reference/config/write_deadline) [S]; Centrifugo batches writes with `client_write_delay` to cut syscalls (https://centrifugal.dev/docs/pro/client_message_batching) [S]. The Centrifugo option name `client_queue_max_size` could not be confirmed [U].
- Options: (1) bounded per-connection channel; when full, close the connection with a policy error (client reconnects and re-syncs; RTDB SDKs re-establish listeners and receive a full snapshot - SDK behaviour [I, known from SDKs, not re-verified here]); (2) drop-oldest is unsafe for RTDB because events are deltas on a tree [I]; (3) coalesce: keep latest value per path [I, complex]. Recommendation: (1) with a write deadline of ~10 s and queue of e.g. 256-1024 frames [I numbers, to be tuned].
- coder/websocket: pass a context with timeout to `Write`; expiry closes the connection [V], which is exactly the desired slow-consumer behaviour. gorilla: `SetWriteDeadline` before each write; write errors are permanent [U: doc page failed to load, well-known].

### Heartbeats and proxies
| Proxy | Idle timeout | Source |
|---|---|---|
| nginx | `proxy_read_timeout` default 60 s; "connection will be closed if the proxied server does not transmit any data within 60 seconds"; fix = raise it or send ping frames [V] | https://nginx.org/en/docs/http/websocket.html |
| Cloudflare | closes when "no data moves in either direction for a period of time"; the commonly quoted 100 s is NOT stated on the page [U]; docs say implement ping/keepalive; WAF only inspects the 101 upgrade; releases may drop sockets [V] | https://developers.cloudflare.com/network/websockets/ |
| Caddy | not researched [U] | |
- RTDB protocol already has its own keepalive: client sends a "0" frame periodically and the server answers [I from SDK knowledge, other tracks cover this]. Server-side protocol-level pings at ~25-30 s (below 60 s) are the safe choice behind nginx defaults [I].

### Compression
- permessage-deflate keeps a compressor context per connection when context takeover is on; memory is controlled by window bits (9-15) and memLevel; one IETF hybi benchmark: JSON with no_context_takeover ratio 0.84 vs 0.30 with takeover [S] https://mailarchive.ietf.org/arch/msg/hybi/F9t4uPufVEy8KBLuL36cZjCmM_Y/ . The "~300 KB/conn" folk figure could not be sourced [U]. OWASP: disable unless needed (CRIME/BREACH-style leaks) [V]. coder defaults to disabled [V]. Recommendation [I]: off in v0.2; the RTDB SDKs do not require it.

---------------------------------------------------------------------------------------------------
## 5. Security of WebSocket endpoints

OWASP WebSocket Security Cheat Sheet [V] https://cheatsheetseries.owasp.org/cheatsheets/WebSocket_Security_Cheat_Sheet.html :
- Validate `Origin` on every handshake against an explicit allowlist; no wildcards/substring match.
- Avoid tokens in query strings (log leaks); prefer token in first message over WSS; before auth accept only the auth message, close on failure/timeout, limit unauthenticated connections.
- Re-validate periodically (30 min cited) and close on expiry/logout; rotate tokens in long-lived connections.
- Message size "typically 64KB or less" (example 64*1024); ~100 messages/min as a starting rate limit; per-user, else per-IP, connection limits.
- Disable permessage-deflate unless needed; idle timeouts + ping/pong; proxy read timeouts.
- Authorize per action, not per connection; treat all messages as untrusted JSON.
- CSWSH: attacker page opens a socket using the victim's cookies; the 2023 Gitpod case is cited there [S via search].

Mapping to RTDB [I]:
| Concern | Position |
|---|---|
| Origin | Browsers send `Origin`; native iOS/Android/Admin SDK clients send none (or non-browser values). RTDB auth is via tokens in messages, not cookies, so CSWSH has no ambient credential to hijack. Policy: if `Origin` present and not in the project's allowed list -> reject; if absent -> allow. coder/websocket rejects mismatched cross-origin by default and allows same host [V]; empty Origin is allowed by default in both libraries [U: gorilla doc not loaded; coder: only "cross-origin not allowed by default" is stated]. |
| Auth over socket | RTDB sends `auth`/`gauth`/`appcheck` actions on the same socket (brief). Unauthenticated sockets: allowed in RTDB (public rules) - so the OWASP "only auth message first" cannot be enforced; instead enforce rule evaluation per read/write and cap unauthenticated resources [I]. |
| Token expiry / revocation | Firebase ID tokens last 1 h; revoking refresh tokens does not invalidate issued ID tokens until expiry (Firebase docs, https://firebase.google.com/docs/auth/admin/manage-sessions) [S via search result text; V title]. Whether real RTDB drops an open socket on expiry is not documented [U]. Aruma option: track `exp` per connection; at expiry (or on user-disabled / session revoked event from the Auth service) send RTDB `ac` (auth revoked) data message or close; closing is the simple, safe action because SDKs reconnect and re-send fresh token [I]. |
| Limits | per-IP and per-project connection caps, max frame/message bytes (coder default 32768 per message - RTDB SDK writes up to 16 MB per Firebase limits, so the read limit must be raised for authenticated connections and probably frames are split by the SDK; verify against SDK protocol notes from other tracks [U]), per-connection message rate, handshake timeout, max listeners per connection. |
| JSON DoS | Go `encoding/json` caps nesting at 10,000 (`maxNestingDepth`, scanner.go [V]); RTDB's limit is depth 32 and key 768 bytes [V]. Enforce 32 during decode of writes (reject early), and cap total message bytes before decoding; reject duplicate/control-char keys per RTDB key rules [I]. A 16 MB message of `[[[[...` costs memory in a token-streaming decoder only up to depth; use `json.Decoder` Token API or a custom bounded parser [I]. |
| Frame fragments | Per-connection reassembly buffer needs the same cap as the message limit [I]. |

---------------------------------------------------------------------------------------------------
## 6. Storage model for a JSON tree in Postgres

Facts:
- `ltree`: labels `A-Za-z0-9_-` in C locale, max label 1000 chars, path max 65,535 labels; indexes: btree, hash, GiST (`@>`, `<@`, siglen up to 2024 bytes) [V] https://www.postgresql.org/docs/current/ltree.html . RTDB keys can contain many other characters (anything except `. $ # [ ] / ` and control chars, up to 768 bytes) [U: key rules from Firebase docs, other tracks] so ltree needs an escaping/hashing scheme -> poor fit [I].
- jsonb: "any update acquires a row-level lock on the whole row. Consider limiting JSON documents to a manageable size"; documents should be "atomic datum"; GIN `jsonb_ops`/`jsonb_path_ops`; rejects `\u0000`; keeps last duplicate key; **does not preserve key order (shorter keys first)** [V] https://www.postgresql.org/docs/current/datatype-json.html . RTDB returns children ordered by key (lexicographic with numeric-key rules) so ordering is done by the app anyway [I].
- TOAST: values over ~2 kB are compressed/moved out of line, max 1 GB per value, chunks ~2000 bytes; unchanged TOASTed fields are not rewritten on UPDATE, but the docs don't say a changed value is rewritten in full - it is, since jsonb is replaced as a whole value [V for threshold/chunking; I for rewrite] https://www.postgresql.org/docs/current/storage-toast.html . Updating one leaf in a big jsonb thus rewrites the whole document and creates WAL for it.

| Model | Write one leaf | Read subtree | Delete subtree | Listener index | Fit |
|---|---|---|---|---|---|
| 1. Row per node/leaf, `path text COLLATE "C"` + btree prefix range (`path >= 'a/b/' AND path < 'a/b0'`) | O(1) row upsert | one range scan; cost ~ number of leaves | range DELETE; Firebase also warns that deleting thousands of leaves is expensive [V] | in memory | Good for deep/wide trees, wasted overhead per row (~tuple header 23+ bytes + index) ; needs reconstruct JSON on read; multi-path update = many rows in one tx (fine) |
| 2. ltree row per node | same as 1 | `<@` with GiST/btree | same | same | Key charset problem; no big win over 1 with COLLATE "C" [I] |
| 3. jsonb document per top-level child (or per N-th level) | rewrites the child doc (up to MBs) | one row fetch | single row delete | in memory | Fast for typical app shapes (users/{uid}/...) but pathological for one hot huge child; write amplification; whole-row lock = writes to the same child serialise (matches RTDB's one-order model anyway) |
| 4. whole database as one jsonb | rewrites everything | one fetch | one update | in memory | Only for tiny DBs; 1 GB TOAST cap; reject beyond |
| 5. In-memory tree + WAL/snapshot (event-sourced): append each accepted write to `events.changes` (path, value, seq), periodically snapshot the tree to Postgres/blob, recover = latest snapshot + replay tail | O(1) append | memory | memory + one log record | in memory | Fastest, matches how RTDB is believed to behave [I]; memory-bound (RTDB's 75 M-node-per-listened-path figure shows Google also bounds this [V]); needs snapshot/compaction and one process per database (conflicts with horizontal scale, which RTDB itself sidesteps by sharding into instances) |

Who did what (not deeply researched; from prior knowledge flagged [U]): Supabase Realtime itself has no RTDB tree; Lark and flakbase are covered by other tracks (Lark is AGPL: ideas only). 

[I] Reasoned comparison: model 1 (row per leaf, `COLLATE "C"` path) gives correct semantics with plain Postgres and bounded write cost, with an in-memory index for listeners; model 5 is the fastest and what a real RTDB resembles but requires snapshotting and a memory budget. A hybrid is natural: persist as model 1 (source of truth), keep a bounded in-memory cache of hot subtrees. Model 3 is attractive only if reads dominate and top-level children stay small (<~100 KB).

---------------------------------------------------------------------------------------------------
## 7. Offline-first / sync lessons

- Firebase client queues writes while offline and (with persistence) stores them on disk; local listeners fire immediately for local writes; **transactions are queued while offline but not persisted across app restart** [V/S] https://firebase.google.com/docs/database/android/offline-capabilities , https://firebase.flutter.dev/docs/database/offline-capabilities/ . So the server must expect replays of old writes after reconnect and deliver acks in order [I].
- Replicache: runs each mutation twice (optimistic locally, authoritative on server); the server's result replaces the speculative one; pending mutations are rebased onto new server state; server processes the queue in order; pokes are contentless hints, then pull [V] https://doc.replicache.dev/concepts/how-it-works . Lesson [I]: RTDB's equivalent is client-side event prediction followed by server `d` acks; the server's job is only to preserve per-connection request order and global order, and respond with the authoritative value on conflict.
- Figma multiplayer: last-writer-wins at property granularity, server is central relay, no CRDT/OT [V] https://www.figma.com/blog/multiplayer-editing-in-figma/ . RTDB is LWW at path granularity; matching it needs no merge logic.
- Convex: serializable isolation with optimistic concurrency control; queries re-run and subscriptions updated when dependencies change [S via docs summary]. Relevant contrast: RTDB transactions are client-side CAS (client reads value+hash, sends put with hash; server rejects with `datastale` if the hash differs; client retries) [I from SDK knowledge; other tracks should confirm the hash format - not verified here].
- Pitfalls [I]: (a) acking a write before its event is visible to other listeners breaks "listener sees own write then others'"; (b) re-delivered writes after reconnect can overwrite newer data (LWW), same as Firebase; (c) `serverTimestamp` placeholders must be resolved once, under the ordered writer; (d) transaction data-stale loops need server-side hash of the current value, so hashing cost scales with subtree size - cache hashes per node [I].

---------------------------------------------------------------------------------------------------
## 8. Multi-tenancy isolation for realtime servers

| System | What it does | Source |
|---|---|---|
| Supabase Realtime | tenant = host-derived `external_id`; per-tenant JWT secret/JWKS; per-tenant DB connection pool owned by one `Tenants.Connect` process; `max_concurrent_users` (counted after first successful channel join) and `max_channels_per_client` default 100; per-tenant singletons placed by region + CPU load [V] | https://raw.githubusercontent.com/supabase/realtime/main/ARCHITECTURE.md |
| Supabase plan limits | concurrent clients 200 / 500 / 10,000 (Free/Pro/Team); channels per client 100; joins per second 100 / 500 / 2,500; messages per second 100 / 500 / 2,500; broadcast payload 256 KB (free) / 3,000 KB; postgres-changes payload 1,024 KB [V] | https://supabase.com/docs/guides/realtime/limits |
| Firebase | per-database caps (above); Spark plan 100 connections; sharding gives isolation per instance with its own rules [V] | limits + sharding pages |
| Hasura | per-instance live-query counts scale with CPU; polling interval and batch size tunable [V] | live-queries.md |
- Noisy-neighbour levers [I]: per-project (a) max connections, (b) max listeners per connection and per project, (c) write bytes/min (Firebase uses 64 MB/min [V]) and writes/s, (d) read response cap, (e) event-queue memory cap, (f) fair scheduling: one writer goroutine per database so a hot project cannot block another's ordering, (g) per-project cap on in-memory tree bytes if model 5 is used.
- Isolation pitfalls [I]: shared global queues (e.g. one NOTIFY channel, one replication slot -> single thread, as Supabase documents for Postgres Changes [V]) become cross-tenant bottlenecks; prefer per-project work queues.

---------------------------------------------------------------------------------------------------
## Open items / unverified
- No primary source for RTDB's internal threading model or storage; treat "single-threaded per database" as unverified [U].
- Cloudflare 100 s WebSocket idle figure not stated on the cited page [U]; Caddy not researched.
- Gorilla `CheckOrigin` default and write-deadline doc page could not be fetched (HTTP 500) [U].
- Electric architecture page not read (redirect, then 404) [U].
- Whether PG core removed the NOTIFY commit-time lock, and in which version: not confirmed; the cited commit is a wake-up optimisation merged Jan 2026 (after PG 18) [U].
- Wire-level RTDB details (frame splitting, `0` keepalive, hash CAS, `ac` message) come from SDK knowledge, not verified here; defer to the protocol tracks.

---------------------------------------------------------------------------------------------------
## Recommendations for a single-node Aruma v0.2 that can grow (inference)

1. **One writer goroutine (actor) per database; sequence assigned in apply order and persisted in the same Postgres transaction as the data; publish to listeners only after COMMIT.** Gives a total order with no lost events and avoids sequence-vs-commit-order bugs (Dudycz; Grittner; Firebase's per-instance ceiling is consistent with it). Batch several queued writes into one transaction (group commit) to raise throughput. Restart: resume counter from `max(seq)` per database.
2. **Do not poll `events.changes` by a plain sequence cursor from concurrent writers.** If the table is also read by other consumers (multi-node later), assign seq under `pg_advisory_xact_lock(db_id)` as the last statement before commit (Grittner) or fence readers with `xid8 < pg_snapshot_xmin(pg_current_snapshot())` (Dudycz), accepting the long-transaction stall.
3. **Fan-out entirely in process in v0.2: a path-trie of listeners per database, bounded outbound queue per connection.** Avoid LISTEN/NOTIFY, logical replication and Hasura-style polling on a single node: NOTIFY has an 8000-byte limit and a commit-time global lock on PG 18 (Postgres docs; Recall.ai), logical replication needs slots/WAL retention risk (Postgres docs; Hasura), and neither is needed when one binary owns the writes.
4. **Design the change log so multi-node can be added without redesign:** keep `events.changes(database_id, seq, path, ...)`, a per-database leader lease (advisory lock) and, later, NOTIFY as a payload-free wake-up hint only (Postgres docs; Supabase per-tenant singleton model).
5. **WebSocket library: coder/websocket** (ISC, actively maintained, context-based writes whose expiry closes the connection, same-host origin default, compression off by default) with goroutine-per-connection (reader + writer, ~10-25 KB/conn per Kamardin/Yanay); gorilla is the fallback (stable, last push 2025-03). Skip epoll/gobwas until >100k connections (Kamardin; Yanay).
6. **Slow consumers: bounded send queue + per-write timeout; on overflow close the connection** (clients resync on reconnect) rather than dropping deltas. Starting values to be tuned: queue 256-1024 frames, write timeout ~10 s (NATS write_deadline; coder context semantics).
7. **Heartbeats under 60 s** (protocol keepalive handling plus server-side pings every ~25-30 s) to survive nginx's 60 s `proxy_read_timeout` and similar proxy idle timeouts; document the nginx snippet (nginx docs; Cloudflare docs).
8. **net/http integration:** keep Read/WriteTimeout 60 s for REST; the upgrade path clears server deadlines on hijack in current Go, but call `SetDeadline(time.Time{})` defensively and own per-message deadlines; track all hijacked connections yourself because `Server.Shutdown` ignores them (Go source `hijackLocked`; Hijacker doc; `StateHijacked`).
9. **Security defaults:** handshake Origin check = reject mismatched browser Origin, allow absent Origin (native SDKs); no cookies accepted for socket auth so CSWSH has nothing to ride; tokens only in socket messages; message-size cap before JSON decode and a depth cap of 32 (Firebase limit; Go's own cap is 10,000); per-IP and per-project connection and listener caps; compression disabled (OWASP; coder docs; Go scanner.go).
10. **Token expiry and revocation:** track each connection's token `exp` and project/user; on expiry or on a user-disabled/session-revoked signal from the Auth service, send the auth-revoked message or close the socket (OWASP periodic re-validation; Firebase docs on ID-token lifetime and revocation).
11. **Storage for v0.2: row per node/leaf with `path text COLLATE "C"` and prefix-range queries, in-memory listener index, optional in-memory cache of hot subtrees.** ltree is a poor fit for RTDB's key charset (ltree docs), whole-document jsonb rewrites the document per leaf update and takes a whole-row lock (jsonb docs; TOAST docs). Keep an escape hatch to model 5 (in-memory tree + snapshot) if benchmarks show reads of big subtrees dominate.
12. **Limits to ship in v0.2:** per project: connections, listeners, write bytes/min and writes/s, max read response; global: max message bytes; reuse Firebase's published numbers as defaults where they are lower than what the server can sustain (depth 32, key 768 B, string 10 MB, 16 MB per write, 1,000 w/s, 64 MB/min) so SDK-visible behaviour matches (Firebase limits page; Supabase limits table for how others scope them).
13. **Do not promise numbers before a load test:** no public source gives RTDB internals or Supabase Postgres-Changes throughput; both vendors tell users to measure (Supabase docs; Firebase `database:profile`).
