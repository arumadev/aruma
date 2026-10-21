# Performance baseline: Realtime Database

Results for R7: the durability check and the load baseline of the Realtime Database. Measured on 2026-10-21 with [`tools/rtdb-chaos`](../../tools/rtdb-chaos) and [`tools/bench -rtdb`](../../tools/bench/rtdb.go), on the same machine as the [Phase 1](baseline-phase1.md) and [Phase 2](baseline-phase2.md) baselines. The bench runs used commit `eb976e2` (runs 2 and 3) and `b4e5daa` (run 1, before the fan-out fix of finding 1). The raw results are in [baseline-rtdb/](baseline-rtdb/): one JSON file per bench run and per chaos campaign.

**In short:** on a fanless M1 laptop shared with other agents, under heavy and varying load, with clients, server and Postgres on the same 8 cores:

- **No acknowledged write was lost, and none applied twice,** across 12 chaos runs: 81 SIGKILLs and 12 SIGTERMs under 8 to 32 concurrent clients, about 101,000 acknowledged writes. Every database's `seq` came out exactly equal to the number of writes that ran. onDisconnect writes ran on every normal close and every SIGTERM.
- **One database takes about 4,500 sets/s** at 32 concurrent writers (p50 6.8 ms), and 1,500/s from a single writer (p50 0.45 ms). Firebase publishes about 1,000/s per database.
- **One server held 10,000 WebSocket connections**, idle and with a listen each, using about 40 KiB of Go memory per connection (+5 KiB per listen). The ceiling on this machine is the macOS descriptor limit, about 10,200 per process.
- **Fan-out:** one write to 1,000 listeners on one path reaches them with p50 8 ms and p99 85 ms, at 20,000 deliveries/s.
- **Queries on large nodes are the weak spot.** A listen with `limitToLast(50)` reads and sorts the whole node: 35 ms at 10,000 children, 0.3–2.6 s at 100,000. Worse, every write under such a listen does it again, inside the database's single writer, so all of that database's writes wait (finding 2). Ten listens on one node multiplied that cost by ten; that part was a bug and is fixed (finding 1).
- **Long polling** performs within 30% of a WebSocket for writes.

## Hardware and software

| | |
|---|---|
| Machine | MacBook Air (M1, 2020), `MacBookAir10,1`, fanless |
| CPU | Apple M1, 8 cores (4 performance + 4 efficiency) |
| Memory | 8 GiB; swap 5 GiB, of which 4.3 GiB was in use by the end |
| OS | macOS 15.5 (24F74), Darwin 24.5.0 |
| Go | go1.27.2 darwin/arm64, GOMAXPROCS 8 |
| Postgres | 18.1, the DBngin build, local on 127.0.0.1:5432; data directory `/Volumes/Work/postbase` on an external APFS volume. `shared_buffers` 128 MB, `fsync` on, `synchronous_commit` on, `max_connections` 100 |
| Database pool | pgx v5.11.0 default, max(4, NumCPU) = **8 connections**, unless a case says otherwise |
| WebSocket | `github.com/coder/websocket` v1.8.15, compression off |
| Descriptors | `kern.maxfilesperproc` 10,240, `kern.maxfiles` 30,720 (6,000 in use by other programs), `ulimit -n` 1,048,576 (the kernel caps it at `maxfilesperproc`); `kern.ipc.somaxconn` 128; ephemeral ports 49152–65535 |
| Network | loopback only |
| Server | the real `aruma serve` binary in a process of its own, with `rtdb.enabled`, `rtdb.dev_open_rules` (loopback clients skip the rules engine), log level error, and the connection limits per address and per project raised to 1,000,000 |

**Noise.** The machine was shared with other agents' work: an Android emulator, Chrome renderers, Spotlight indexing and several other tools. The 1-minute load average, sampled every 10 s during run 2 and the loaded run, had a median of 23 and peaked at 119 (`baseline-rtdb/load-run2-and-loaded.txt`); it was 12–14 at the start and end of run 3. The machine was swapping. The three bench runs are reported as the median with the min–max range. The ranges are wide: read them as what the server does on a busy machine, and take the high end of throughput as closer to its capacity. A fourth run during the worst of it is kept apart (`run-loaded.json`, see Caveats).

## How to reproduce

```sh
# Durability: 6 SIGKILL cycles then a SIGTERM, per seed; -seed replays a run's operations and kills
go run ./tools/rtdb-chaos -seed 101 -runs 6 -cycles 6
go run ./tools/rtdb-chaos -seed 201 -runs 3 -cycles 8 -clients 32
go run ./tools/rtdb-chaos -seed 301 -runs 3 -cycles 7 -clients 16

# Load: every scenario, as published (about 10 minutes)
go run ./tools/bench -rtdb all -json run.json

# One scenario, smaller
go run ./tools/bench -rtdb query -rtdb-query-sizes 10000 -duration 5s
```

Both tools build `./cmd/aruma` (or take `-bin` / `-rtdb-bin`), create a database `<ARUMA_TEST_DB_PREFIX><name>_<random>` on the Postgres of `ARUMA_TEST_DATABASE_URL`, and drop it on exit. The chaos tool serves on port 8780, the bench on 8785 with its metrics on 8786. The shared code (the server process, a minimal wire-protocol client, a long-poll client, REST) is in [`tools/internal/rtdbkit`](../../tools/internal/rtdbkit).

## Durability under SIGKILL and SIGTERM

### Method

`tools/rtdb-chaos` starts the binary, creates a project through the console API, and drives its database with N clients. Each client owns a subtree, `/c/<client>`, so the expected value of that subtree follows from that client's own acknowledged writes in the order it sent them. The operations are drawn from the seed:

- sets of leaves and objects, including values over 16,384 characters, so the message is split into frames;
- multi-path updates at the root, with a null and a deep path among the children;
- updates below the subtree with nulls;
- a delete followed at once, without waiting, by a write beneath the deleted path;
- `increment` server values, alone and inside an update;
- transactions (a read, then a put conditional on the hash), on the client's own path and on shared paths where every client contends (`datastale`, then retry);
- children with push ids made as the SDKs make them;
- REST `PUT` (with and without `print=silent`), `PATCH` with nulls, `DELETE` and `POST`;
- for every connection: an onDisconnect set, an onDisconnect update, and one more set cancelled with `oc`; now and then a client closes its connection normally, reconnects, and waits to see its onDisconnect writes land.

Writes are sent one at a time or in bursts of up to four. Shared paths take only changes that commute: increments with a receipt in the client's subtree, plain increments, and transactions that add a key. A controller kills the server with SIGKILL after a random 0.3–3 s, keeps it down up to 0.5 s, and starts it again; after the last cycle it stops it with SIGTERM and starts it once more.

A write whose reply was lost (the connection died first) is **in flight**. After the restart, the client reads its subtree before it sends anything else. The server must have applied a prefix of that connection's in-flight writes, in order; the read says which prefix, and those writes join the ground truth. A write that changes only shared state is settled at the end. The final check reads the whole database over REST and requires:

- every subtree equal to the ground truth: nothing acknowledged is missing, and nothing appears that was not acknowledged or in flight;
- `/shared/inc` equal to the number of receipt increments that ran, and the plain counter within acknowledged + in flight;
- every acknowledged transaction's key present, in-flight ones optionally, and no key that was never sent;
- the onDisconnect writes of every session that ended normally or by SIGTERM present, those of sessions killed by SIGKILL absent, cancelled ones absent, and nothing else under `/presence`;
- `rtdb.databases.seq`, read from Postgres, exactly equal to the number of writes that ran (one per write, however many paths it changed, plus one per session whose onDisconnect writes ran). Between cycles, seq must never go back.

**The check catches real faults.** A server patched to acknowledge a batch before committing it (with a 20 ms sleep in between) failed the first run with 18 violations: acknowledged writes missing after a restart, and seq 31 short of the writes acknowledged. With a 2 ms window the same patch passed one run of 4 kills: the check needs a kill to land inside the window, so a narrow race may need many cycles.

### Results

| Campaign | Runs | Clients | SIGKILL | SIGTERM | Writes acknowledged | of them REST | In flight at a stop | applied | settled by shared state | Sessions | onDisconnect ran | lost to SIGKILL | Violations |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| seeds 101–106 | 6 | 8 | 36 | 6 | 46,454 | 2,530 | 499 | 94 | 26 | 582 | 299 | 281 | **0** |
| seeds 201–203 | 3 | 32 | 24 | 3 | 35,185 | 1,863 | 1,170 | 154 | 113 | 1,112 | 359 | 741 | **0** |
| seeds 301–303 | 3 | 16 | 21 | 3 | 19,715 | 1,052 | 568 | 113 | 48 | 522 | 190 | 327 | **0** |
| **Total** | 12 | | **81** | **12** | **101,354** | 5,445 | 2,237 | 361 | 187 | 2,216 | 848 | 1,349 | **0** |

Files: `chaos-8-clients.json`, `chaos-32-clients.json`, `chaos-16-clients.json` (the last on the final binary, with finding 1's fix; the fix touches only listens, which the chaos clients do not use). Before these campaigns, a first run of 4 seeds found two faults, both in the tool itself and both fixed before the published runs: a client that dialled across a restart attributed its session to the wrong server, and a REST `POST` lost in flight could not be settled.

**What the runs show:**

- **Acknowledged means committed.** The writer replies only after the transaction commits (ADR-019), and in 81 kills not one acknowledged write was missing.
- **No write ran twice.** In-flight writes landed exactly once or not at all (361 landed), and the increments and transactions on shared paths matched exactly.
- **seq is exact.** It counted every write that ran, including the 361 in-flight ones that landed and the onDisconnect writes, and never went back across a kill.
- **SIGTERM runs onDisconnect.** The graceful shutdown closes every connection and runs its onDisconnect writes before exit, in all 12 SIGTERMs.
- **SIGKILL loses onDisconnect writes, by design.** They live in the server's memory until the connection ends (finding 8). None ever appeared after a SIGKILL.

## Load

Three runs of `go run ./tools/bench -rtdb all`. Run 1 predates the fix of finding 1, so its query results stand only as the "before"; the other scenarios are unaffected. Each scenario starts a fresh server on its own database. Latencies are from sending a request to reading its reply (or the push, for fan-out), closed loop unless a case says otherwise, after 2 s of warm-up, over a 10 s window.

### Connections

The bench opens WebSockets 32 at a time in steps of 1,000, sampling the server's RSS (`ps`) and its Go runtime metrics (`/metrics` on the private address) after each step. Then each connection starts one listen at a path of its own.

| | Connections | Ramp | Server heap | Server stacks | Goroutines | Open fds | RSS |
|---|---|---|---|---|---|---|---|
| Idle | 10,000 of 10,000 | 905/s (876–924) | 216 MiB | 196 MiB | 20,015 | 10,019 | 275 MiB (93–367) |
| With a listen each | 10,000 | listen p50 3.9 ms, p99 17 ms | 260 MiB | 196 MiB | 20,015 | 10,019 | 456 MiB (23–469) |

Per connection: **2 goroutines, 20 KiB of stack and 20 KiB of heap, about 40 KiB**; a listen adds about 4.5 KiB of heap. RSS per connection came out between 24 and 44 KiB, but RSS is not reliable here: under memory pressure macOS compresses and swaps the process's pages, and the RSS of one run fell to 23 MiB with 10,000 connections open. The Go runtime's figures were steady to within 3% across runs.

**The limit is descriptors.** One per connection, plus 19 the server holds anyway. `kern.maxfilesperproc` is 10,240, so one server process on stock macOS holds about **10,220** connections, after which accepting fails with `EMFILE` (from the arithmetic; the failure itself was not provoked). The bench stops at 10,000, where its own process meets the same limit. Raising the limit needs `sysctl` as root, which these runs did not use. The connections also share `kern.maxfiles` (30,720 system-wide) with every other program.

### Fan-out

One writer sets a small object (about 90 bytes) at one path 20 times a second, for 200 writes; K connections listen at that path. The latency is from sending each write to each listener reading its push; the first 20 writes are not recorded. Every delivery arrived (0 lost in 661,200 over the three runs).

| K | Deliveries/s | p50 ms | p90 ms | p99 ms | max ms | Writer's ack p50 / p99 ms |
|---|---|---|---|---|---|---|
| 1 | 20 | 2.13 (1.28–2.42) | 3.31 | 12.2 (5.9–30.2) | 14.7 | 2.1 / 12.2 |
| 100 | 2,000 | 3.38 (2.08–3.67) | 5.51 | 11.0 (7.4–28.8) | 17.6 | 3.4 / 11.0 |
| 1,000 | 20,000 | 8.06 (7.73–13.5) | 18.4 | 84.9 (66.1–247) | 140 | 8.9 / 89.1 |
| 1, over long polling | 20 | 2.49 (1.52–2.92) | 3.64 | 29.9 (4.1–48.8) | 38.9 | 2.4 / 29.9 |

The writer's own ack grows with K: the pushes are built inside the writer, one per listener, before the ack goes out (finding 5). At K = 1,000 that adds about 7 ms to every write to that database. The open loop at 20 writes/s stays well below saturation; the ceiling of deliveries per second was not measured.

### Writes per second

Closed loop: each writer has its own connection and sets a small object at a path of its own (`/w/w<i>/k<n mod 64>`), waiting for each ack.

| Case | Writes/s | p50 ms | p90 ms | p99 ms |
|---|---|---|---|---|
| 1 database, 1 writer | 1,519 (824–2,231) | 0.45 (0.37–1.02) | 1.13 | 2.95 |
| 1 database, 8 writers | 3,228 (1,989–4,560) | 2.16 (1.61–2.33) | 3.41 | 6.95 |
| 1 database, 32 writers | **4,540 (2,339–5,094)** | 6.75 (6.03–9.96) | 8.19 | 12.7 |
| 1 database, 128 writers | 3,248 (2,867–5,344) | 32.5 (23.6–40.9) | 62.4 | 120 |
| 1 database, 32 REST writers (`PUT` with `print=silent`) | 3,286 (1,982–4,310) | 8.39 (7.14–14.4) | 13.2 | 26.2 |
| 10 databases, 16 writers each, default pool (8) | 4,049 (2,311–5,985) | 33.3 (26.5–35.7) | 56.6 | 159 |
| 10 databases, 16 writers each, `pool_max_conns=32` | **6,734 (3,961–10,398)** | 15.1 (12.7–18.4) | 39.3 | 143 |

0 failures in the 1,068,596 writes of this and the long-poll scenario over the three runs.

- **One database saturates at about 4,500–5,000 sets/s** on this machine. Its single writer groups whatever has queued, up to 64 commands, into one transaction, and runs each write's statements one after another: read the ancestors, delete the old subtree, insert the new rows with COPY. That is about four round trips to Postgres per write, on one connection, plus the seq update and the commit per batch (finding 4). More writers beyond 32 only add queueing.
- **Across databases it scales with the pool, not the CPU.** Each writer holds a pooled connection for its whole batch. Ten busy databases on the default pool of 8 queue for connections: 4,049/s, p50 33 ms. With 32 connections the same load runs at 6,734/s, p50 15 ms (finding 3; the [operator guide](../guides/rtdb-operator.md#6-capacity-descriptors-and-the-database-pool) now says so).
- **REST writes cost about a quarter more** than WebSocket writes at the same concurrency: each request resolves its project with a query and opens and closes a session.

### Queries on large nodes

The bench writes 10,000 and 100,000 children (`{"score": <random>, "name": "item <n>"}`) under `/q/n<size>`, 5,000 per `PATCH`. One connection then listens with `limitToLast(50)` and unlistens, repeatedly; the time is from `q` to its ok, which follows the data. Then it listens once more and another connection sets random children's `score`, one at a time. Runs 2 and 3, median (min–max):

| Children | Query | Listen p50 ms | Bytes pushed | Write under one listen, p50 ms | under ten listens | Write with no listen |
|---|---|---|---|---|---|---|
| 10,000 | `orderByKey().limitToLast(50)` | 37 (35–39) | 2.9 KB | 41 (40–41) | 40 (39–40) | 0.85 |
| 10,000 | `orderByChild('score').limitToLast(50)`, no `.indexOn` | 82 (72–91) | **580 KB** | 3.2 (0.8–5.6) | 8.3 | |
| 10,000 | `orderByChild('score').limitToLast(50)`, `.indexOn` | 215 (78–352) | 2.9 KB | 162 (51–273) | 126 (68–185) | |
| 100,000 | `orderByKey().limitToLast(50)` | 814 (336–1,292) | 3.0 KB | 1,168 (323–2,013) | | 0.79 |
| 100,000 | `orderByChild('score').limitToLast(50)`, no `.indexOn` | 1,460 (973–1,946) | **5.9 MB** | 1.05 | | |
| 100,000 | `orderByChild('score').limitToLast(50)`, `.indexOn` | 1,711 (839–2,584) | 3.0 KB | 1,233 (738–1,728) | | |

Before the fix of finding 1 (`query-10k-before-fix.json`, run alone in a quieter spell), a write under ten `orderByKey` listens took 302 ms against 35 ms under one, and under ten indexed listens 847 ms against 136 ms; after it, ten listens cost what one does (`query-10k-after-fix.json`: 48 and 52 ms).

- **A listen reads and sorts the whole node.** Without a persistent index (deferred, ADR-020), the server reads every row under the path and sorts in memory, then keeps 50. At 100,000 children that is 0.3–2.6 s per listen, and the listen's data comes only after it.
- **Every write under a filtered listen does it again** (finding 2). The writer re-reads the node, re-filters it, and diffs the window, inside the batch's transaction. At 100,000 children that is about a second per write, and every other write to that database waits behind it: one client's leaderboard listen caps the whole database at about one write per second.
- **`.indexOn` makes writes slower, not faster.** Without it, a child-ordered listen is served unfiltered and untagged (Firebase's documented behaviour): the listen ships the whole node, 5.9 MB at 100,000 children, but writes under it cost nothing extra, since the push is the write itself. With the index, the server filters, and pays for it on every write. In Firebase the index is what makes such queries cheap.

### Long polling against WebSocket

The same closed-loop writes as above, on one database, over each transport in turn. The long-poll client keeps one request held, as the JS SDK does, and sends each message in a new request, which releases the held one.

| Writers | WebSocket writes/s | p50 ms | Long poll writes/s | p50 ms |
|---|---|---|---|---|
| 1 | 648 (345–702) | 1.06 | 432 (375–564) | 1.65 |
| 8 | 1,572 (1,342–3,292) | 4.03 | 1,548 (1,001–2,439) | 4.92 |
| 32 | 1,635 (524–4,329) | 17.8 | 1,669 (562–3,080) | 18.1 |

A long-poll write costs one HTTP request and its reply comes back on the held one, so a single writer is about a third slower; with concurrency both are bound by the database's writer, and the long poll is within the noise of the WebSocket. Delivery to a long-poll listener (fan-out, K = 1) is close to a WebSocket's: p50 2.5 ms against 2.1 ms. This scenario's absolute numbers are lower than the writes scenario's: it ran last in each run, when the load from other programs was highest.

## Against Firebase's published limits

From [Firebase's limits page](https://firebase.google.com/docs/database/usage/limits) (as recorded in [D-articles-design.md](../research/rtdb/D-articles-design.md) and [H-rules-rest-lp.md](../research/rtdb/H-rules-rest-lp.md)):

| Firebase, per database | Aruma here | Assessment |
|---|---|---|
| About 1,000 writes/s ("not a hard limit") | 1,500/s from one writer, 4,500/s at 32 writers | **Above it**, on a laptop with Postgres on a USB volume. |
| 200,000 simultaneous connections | 10,000 measured; about 10,220 is this machine's descriptor limit per process | **Far below, and not measured beyond 10,000.** At about 45 KiB each, 200,000 connections would need about 9 GiB for connections alone, and a raised descriptor limit. Not verified on any machine. |
| About 100,000 responses/s | 20,000 deliveries/s measured (K = 1,000 at 20 writes/s), p99 85 ms; ceiling not measured | **Unknown.** Each listener's push is encoded separately inside the writer (finding 5), which bounds this per database. |
| Queries: "75 million nodes" under a path with listeners or queries | A filtered listen on 100,000 children takes up to 2.6 s and slows every write under it to about 1/s | **Not comparable** for large nodes until queries have an index of their own (finding 2). Fine up to a few thousand children. |
| 64 MB of writes per minute | enforced as a limit (`rtdb.limits.write_bytes_per_minute`) | Same. |

## Findings

1. **Each filtered listen read its node again on every write. Fixed** (`eb976e2`).
   - **Mechanism.** For each write, `fanout` called `taggedPushes` per listen, and each read the listen's whole node from Postgres and filtered it ([`events.go`](../../internal/services/rtdb/core/events.go)). Ten clients with the same `limitToLast(50)` on one node of 10,000 children meant ten full reads and sorts per write.
   - **Measured.** A write under ten such listens took 302 ms, against 35 ms under one (`query-10k-before-fix.json`).
   - **Fix.** For one change, the node at each path is read once, and each query's window over it filtered once, then shared by every listen; nodes are immutable. Ten listens now cost what one does. Regression test: `TestTaggedListensShareOneRead` counts the store's reads.
2. **Large nodes with filtered listens. Design, not changed.**
   - A listen reads the whole node; every write under a filtered listen re-reads and re-sorts it inside the database's single writer (`events.go`, `taggedPushes`), so it holds up all writes to that database. Measured: 40 ms per write at 10,000 children, about 1.2 s at 100,000.
   - This follows from sorting on demand with no persistent index (ADR-020, rtdb-plan.md "deferred: persistent `.indexOn` index, after measurement"). This is that measurement.
   - **Options:** maintain each window incrementally from the change (most writes touch one child, and a `limitToLast(50)` window can be updated from the changed child and the 51st candidate); or a persistent index for `.indexOn` (a Postgres expression index on the child value, keyed by parent path). Until then, keep filtered listens to nodes of a few thousand children.
3. **The default pool is too small for many busy databases. Configuration, documented.**
   - Each database's writer holds one pooled connection for its batch, so busy databases beyond the pool size queue for connections. The default pool is max(4, NumCPU), 8 here.
   - Measured: 10 databases at 16 writers each wrote 4,049/s (p50 33 ms) on the default pool, 6,734/s (p50 15 ms) with `pool_max_conns=32`.
   - The operator guide now says to size the pool above the number of databases that write at once. A server with many active projects may want the default raised; not changed here.
4. **About four Postgres round trips per write, one after another. Design, not changed.**
   - A set reads the ancestors (`ReadRows`), deletes the old subtree, and inserts the new rows with COPY (two round trips), all on the writer's one connection; the batch adds the seq update and the commit.
   - This bounds one database at about 4,500–5,000 sets/s on this machine, above Firebase's 1,000/s. Sending a batch's statements as one pgx batch, or folding delete and insert into one statement, would raise it.
5. **Fan-out is built inside the writer. Design, not changed.**
   - For each write, the writer encodes one push per listener (`ExportJSON`), and builds a JSON key per push to drop duplicates (`batchState.deliver`), all inside the transaction. At K = 1,000 this adds about 7 ms to every write to the database; the cost grows with listeners times value size.
   - Encoding each distinct push once and sharing it among sessions, and a cheaper duplicate key, would cut most of it; moving delivery after the commit is already how replies work.
6. **Connections cost about 45 KiB and one descriptor each. Expected.**
   - Two goroutines (a reader and a writer) per WebSocket, 20 KiB of stacks and 20 KiB of heap, plus about 5 KiB per listen. On a Linux server with `LimitNOFILE` raised, 100,000 connections would need about 4.5 GiB; not measured.
   - The per-address limit (500) and per-project limit (10,000) were raised for the bench; production keeps them.
7. **A write slower than 30 s fails as unavailable. Expected.** In the loaded run (`run-loaded.json`), with the machine swapping and a load average above 100, one `PATCH` of 5,000 children exceeded the writer's 30 s batch timeout and failed with 503 `The service is currently unavailable.`, applying nothing. Correct, and an operator should know the number.
8. **onDisconnect writes are lost when the server crashes. Design.** They are held in the server's memory until the connection ends. SIGTERM runs them; SIGKILL, an OOM kill or a power loss loses them (1,349 sessions in the chaos runs). Clients re-register them when they reconnect, as the SDKs do, so presence recovers once every client is back; until then, a client that never comes back stays "online". Firebase does not document what its servers do on a crash.

## Caveats

- **The machine was busy and swapping**, and the other programs' load changed from minute to minute. The ranges are wide for that reason; a dedicated machine should show the high ends or better. The fourth run, during the worst of it (load average up to 119), is kept as `run-loaded.json` and not in the tables: its single-database writes fell to 940–3,046/s and one query seed write failed (finding 7).
- **Clients, server and Postgres shared the 8 cores**, unlike production. The bench keeps the server in its own process, so its memory and descriptors are its own, but not its CPU.
- **Rules were not measured.** The bench uses `dev_open_rules`, so every operation skips the rules engine; a real ruleset adds its evaluation (and its data reads) to each write and listen.
- **Postgres ran with defaults** (`shared_buffers` 128 MB) on an external USB volume, with `fsync` and `synchronous_commit` on, as durability requires.
- **The chaos tool's seed fixes each client's operations and the kill schedule, not the interleaving.** A failure prints its seed; replaying it repeats the workload and the kill times, but where exactly a kill lands depends on timing. `-journal` keeps each client's log of what it sent and what came back.
