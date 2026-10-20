# G. RTDB tree storage in Postgres: literature + benchmark (2026-10-20)

Scope: Q2 (how to store the RTDB JSON tree), Q3 (queries without server-side indexes), Q8 (limits).
Throwaway experiment, not Aruma code. Source and raw outputs: `/Volumes/Work/aurma-project/.cache/research/bench-rtdb/`
(`main.go` harness+dataset, `m1.go`, `m3.go`, `m5.go`, `q3.go`, `results.jsonl` raw numbers, `explain.txt` + `explain-m1-range.txt` plans, `m3d1.out`).

## 0. Environment (exact)
- MacBookAir10,1, Apple M1, 8 cores, 8 GiB RAM (`sysctl hw.model machdep.cpu.brand_string hw.memsize hw.ncpu`). Go go1.27.2 darwin/arm64, pgx v5.11.0.
- PostgreSQL 18.1 (Postgres.app build), localhost TCP, single connection, **no server settings changed**. Defaults: synchronous_commit=on, shared_buffers=128MB, fsync=on, wal_sync_method=open_datasync, max_wal_size=1GB. Database default collation en_US.UTF-8 (so `COLLATE "C"` per column matters).
- "sync=false" rows use `SET synchronous_commit=off` on the benchmark session only (session-level, not server config). Both are reported.
- Disk: `/` 15 GiB free before, 14.3 GiB lowest observed, 14 GiB after; databases `aruma_bench_rtdb_{m1,m3d1,m3d2,m5}` created and **dropped** (verified with `pg_database` listing; aruma_dev etc. untouched).
- Caveat on durability numbers: on macOS Postgres' default WAL sync does not force the drive cache (`pg_test_fsync` on /Volumes/Work: open_datasync 623 us/op, fsync_writethrough 1.7 ms/op). So the N=1 `sync=true` figure (~6k writes/s, 0.11 ms commit) is optimistic versus a Linux server with a real NVMe/cloud disk (expect roughly 0.2-2 ms per commit, i.e. N=1 maybe 500-5,000 commits/s). Group-commit numbers (N>=16) depend far less on fsync cost. Inference, not measured on Linux.

## 1. Literature (short)
| Topic | Finding | Source |
|---|---|---|
| Hierarchy encodings | Adjacency list (parent id), nested sets, path enumeration/materialized path, closure table; Celko's trade-off: adjacency is cheap to write but needs recursion for subtrees, nested sets make subtree reads trivial but renumber on insert, path enumeration gives prefix scans, closure table stores all ancestor/descendant pairs (O(n*depth) rows). | Celko, *Trees and Hierarchies in SQL for Smarties*, 2nd ed., Morgan Kaufmann 2012 (chapter DOI 10.1016/b978-0-12-800761-7.00028-0, verified via Crossref); closure table in Karwin, *SQL Antipatterns* ch.3 (from memory, not re-fetched) |
| Order-preserving tree in an RDBMS | Tatarinov et al. compare Global/Local/Dewey order encodings for ordered XML; Dewey (a path of sibling ordinals, i.e. a materialized path) wins on updates and subtree queries. | Tatarinov et al., SIGMOD 2002, doi 10.1145/564691.564715 (verified via Crossref; conclusion from memory) |
| Shredding XML/JSON to relations | Florescu and Kossmann: the "edge table" (one row per node: source, ordinal, name, target/value) vs per-label tables vs inlining; the edge approach is simple and update-friendly but needs many self-joins for path queries. Shanmugasundaram et al.: DTD-based inlining. Argo (Chasseur, Li, Patel, WebDB 2013) shreds JSON into key/value rows, essentially our M1. | Florescu, Kossmann, "Storing and Querying XML Data using an RDBMS", IEEE Data Eng. Bull. 22(3) 1999 (INRIA RR-3680); Shanmugasundaram et al., VLDB 1999; Argo paper. **Not re-fetched** (citation from memory; verify before quoting) |
| Prefix scans on a path column | In the C locale a plain B-tree is usable for `LIKE 'prefix%'`; otherwise `text_pattern_ops` is needed. | https://www.postgresql.org/docs/18/indexes-opclass.html ("If you do use the C locale, you do not need the xxx_pattern_ops operator classes", verified by fetch) |
| jsonb update cost | Any update takes a row-level lock on the whole row; docs recommend limiting document size to lower contention. | https://www.postgresql.org/docs/18/datatype-json.html (verified by fetch) |
| TOAST | Rows wider than ~2 kB are compressed and/or moved out of line; an UPDATE that does not change a toasted column does not rewrite it, but changing one value inside a big jsonb rewrites the whole value. | https://www.postgresql.org/docs/18/storage-toast.html (verified; the second half is the consequence, measured below) |
| Firebase limits (Q8) | Write rate 1,000 writes/s per database ("not a hard limit ... may be rate-limited"); 200,000 simultaneous connections; depth 32; key 768 bytes; string 10 MB; single read 256 MB; single write 256 MB REST / 16 MB SDK; 75M nodes under a path with listeners/queries. | https://firebase.google.com/docs/database/usage/limits (fetched 2026-10-20) |

Why `'0'` follows `'/'`: `/` is byte 0x2F, `0` is 0x30, the next code point. In C collation (bytewise) every string with prefix `p/` is `>= 'p/'` and `< 'p0'`, so `path >= p||'/' AND path < p||'0'` is exactly the subtree of `p`, one B-tree range scan (plan below). `path LIKE 'p/%'` on a `COLLATE "C"` column is rewritten to the same range. Plain `path >= p AND path < p||'0'` would also match siblings like `p-x` ('-' is 0x2D), hence the separate exact lookup for the node itself (`UNION ALL`).
Caveat found while building it: string order of paths is not sibling key order when keys contain characters below '/' (for example '-' which every push ID starts with: "a-b/x" sorts before "a/x"). Reads must therefore sort siblings in the application (my reconstruction does, cost included in the numbers). Another M1 benefit: empty parents do not exist, so RTDB's "null removes the node and empty ancestors" rule is free.

## 2. Benchmark design
Dataset (chat shaped, generated deterministically in `main.go`): 500 rooms (`r1` has 2,000 messages, the other 499 have 297), so 150,203 messages x 3 leaves (text ~60 B string, uid 28 B string, at 13-digit number) = 450,609 leaves; plus 50,000 users x 3 leaves (name, online, lastSeen) = 150,000. **Total 600,609 leaves, 801,815 nodes (201,206 inner)**, 17.0 MB of raw JSON leaf values, push IDs 20 chars.
Models:
- **M1** `nodes(db int, path text COLLATE "C", value jsonb, PRIMARY KEY(db,path))`. Writes: plain upsert via `unnest` (one statement for N leaves). Also measured **"full set-semantics"**: per write, delete leaf ancestors + delete descendants range + upsert (one CTE statement), which is what a real `set` needs.
- **M3d1** `docs(db, key, doc jsonb)` with one doc per top-level child (2 docs: `rooms` ~22 MB, `users` ~5 MB), updates with nested `jsonb_set`. **M3d2** one doc per second-level child (`rooms/r1`, `users/<uid>`; 50,500 docs). The brief's M3 is d1; d2 is the sensible variant, shown for contrast.
- **M5** in-memory tree (`map[string]*node`) + append-only `log(seq bigserial, db, op, path, value jsonb)`. Write = insert log rows (commit) then apply to memory.
Method: single connection, single goroutine, warm cache (warm-up writes first), median/p95 over n iterations (n in tables), reads return the full JSON (reconstructed in Go for M1). Reads (d)-(f) ran before the extra messages of (c) were added, so r1 had exactly 2,000 messages for (e).

## 3. Results (latency in ms, median / p95)
| Op | M1 rows/leaf | M3d2 doc/room-user | M3d1 doc/top-level | M5 mem+log |
|---|---|---|---|---|
| (a) write 1 leaf, sync | 0.27 / 0.44 (full semantics: 0.17 / 0.29) | 1.5 / 2.8 | **511 / 606** | 0.10 / 0.12 |
| (b) multipath 10 leaves, 1 tx | 0.40 / 1.0 (full: 0.56 / 1.1) | 8.6 / 19.4 | **1,305 / 1,685** | 0.13 / 0.52 |
| (c) new message (3 leaves) | 0.12 / 0.43 | 5.5 / 7.9 | **557 / 699** | 0.16 / 0.28 |
| (d) read one message | 0.067 / 0.085 (3 rows, 125 B) | 0.47 / 0.53 | **30 / 31** | <0.001 |
| (e) read room r1 (2,000 msgs, 6,000 rows, 293 KB) | 4.4 / 7.1 | 1.2 / 1.2 | **35 / 50** | 1.0 / 3.6 |
| (f) read all `users` (50k children, 150k rows, 5.0 MB) | **107 / 137** | 23 / 57 | 33 / 35 | 31 / 39 |
| (g) delete room subtree (2,000 msgs, 6,000 leaves) | 2.5 / 2.7 | 0.20 / 0.21 | **625 / 653** | 0.17 / 0.19 |
| Q3 unindexed: read `rooms/r1/messages` (children sent to client) | 3.8 / 4.7 | 1.1 / 2.6 | 34 / 37 | 1.0 / 4.0 |
| (i) size at load (table+indexes) | 112 MB (heap 69 MB) | 19 MB | 9 MB (all TOAST, compressed) | log 88 MB (+ ~110 MB RAM) |
| (i) size after benchmark incl. bloat | 130 MB | **469 MB** | **612 MB** | log 507 MB (3.77M rows incl. benchmark writes, not compacted) |

Notes: M1 (f) includes Go-side tree build + sort + encode of 150k leaves (not split out in the run); a cold EXPLAIN ANALYZE of the same range took 287 ms as a seq scan (`explain-m1-range.txt`). M3d1 reads cost 30+ ms even for one 125-byte message because the whole 22 MB jsonb is detoasted and decompressed (pglz). M3 bloat figures are after `VACUUM ANALYZE` (space is reusable, not returned): each write to a large TOASTed doc leaves a full dead copy.

### (h) Single-writer throughput (writes/s; one goroutine; txn = one group commit of N writes; duration 6 s per cell, 5 s for M3d1)
| Model | N=1 sync | N=16 sync | N=128 sync | N=1 async | N=16 async | N=128 async |
|---|---|---|---|---|---|---|
| M1 plain upsert | 6,066 | 43,487 | 104,630 | 17,449 | 100,121 | 140,820 |
| M1 full set-semantics | 5,735 | 15,486 | 19,233 | 9,734 | 17,804 | 19,748 |
| M3d2 (doc per room/user) | 718 | 736 | 870 | 917 | 970 | 955 |
| M3d1 (doc per top-level) | **2** | not run (would take >1 min per commit) | not run | 2 | n/a | n/a |
| M5 apply + log insert | 6,184 | 52,149 | 132,673 | 22,806 | 118,514 | 186,041 |
Txn latency (median/p95 ms) for sync: M1 N=1 0.11/0.36, N=16 0.22/0.80, N=128 1.1/1.6; M1-full N=128 6.2/8.5; M3d2 N=128 130/240; M5 N=128 0.77/1.4. Writes target random existing `at` leaves out of 150k, so they exercise random index/heap pages (not an append-only best case). Replay of the M5 log at cold start: **8.5 s for 3.77M log rows** (600k-leaf snapshot alone would be roughly 1.3 s, extrapolated, not measured), so M5 needs periodic snapshots/compaction.

### M5 memory per node (Go, live heap growth after GC, ids/uids cloned, 9 common field names interned)
| Variant | Heap for 600,609 leaves / 801,815 nodes | per leaf | per node | objects |
|---|---|---|---|---|
| map children (`map[string]*node`) | 110.3 MB | 184 B | 138 B | 1.98M |
| sorted-slice children | 100.7 MB | 168 B | 126 B | 1.78M |
Raw JSON value bytes are 17.0 MB of that. About 3.3 heap objects per leaf; the cost is per-object overhead (node struct, key string, value slice, small map), not data. Rule of thumb: ~170-185 B per leaf for this shape, so 10M leaves is about 1.8 GB. (An interned/typed-value encoding could roughly halve it; not tried.) An earlier version of my script printed 26 MB for the slice variant; that was a measurement bug (fixed, remeasured in a separate process; TotalAlloc and object counts agree with the table).

## 4. Q3: unindexed query vs `.indexOn` emulation (M1, `q3.go`, plans in `explain.txt`)
r1 had 2,305 messages at this point (6,915 leaf rows).
| Query | median / p95 ms | rows sent | JSON bytes |
|---|---|---|---|
| No index: send all 2,305 children, client sorts by `at` and keeps 50 (this is what `orderByChild('at').limitToLast(50)` costs without `.indexOn`, plus network) | 7.6 / 8.2 | 6,915 | 330 KB |
| Expression + partial index on M1, top 50 by `at` | **0.25 / 0.31** | 150 | 6 KB |
| Separate index table `idx`, top 50 by `at` | **0.21 / 0.36** | 150 | 6 KB |
| New message write: no index / expression index / idx table (2 stmts, 1 tx) | 0.15 / 0.12 / 0.14 | | |
Index sizes: expression index 8.9 MB, `idx` table 27 MB (14 MB heap) for 150k indexed children. Build: 0.6 s and 1.5 s.
SQL (expression index, feasible only for ONE hard-coded child name):
```
CREATE INDEX nodes_at_expr ON nodes (db, (regexp_replace(path,'/[^/]+/at$','')), ((value #>> '{}')::numeric)) WHERE path LIKE '%/at';
WITH top AS (SELECT left(path,length(path)-3) AS mp FROM nodes WHERE db=1 AND path LIKE '%/at'
  AND regexp_replace(path,'/[^/]+/at$','')=$1 ORDER BY (value #>> '{}')::numeric DESC LIMIT 50)
SELECT n.path,n.value FROM top t JOIN nodes n ON n.db=1 AND n.path>=t.mp||'/' AND n.path<t.mp||'0';
```
Plan: backward index scan for 50 keys, then 50 PK range probes (0.55 ms total). Problems: (1) one DDL per indexed child name and it applies to every parent in the database, not to one path (Postgres cannot create an index "for path X" other than via a partial-index predicate, which would need DDL per path); (2) the numeric cast fails or silently mis-orders when `at` is a string/bool/null somewhere (RTDB orders null < false < true < numbers < strings < objects, and mixed types are legal), so the `::numeric` cast would error on insert; (3) planner needs the query to repeat the predicate verbatim.
What a real `.indexOn` needs: a maintained table, e.g. `idx(db, parent_path, index_key, type_rank smallint, sort_num double precision, sort_str text COLLATE "C", child_key text COLLATE "C", PRIMARY KEY(db,parent_path,index_key,type_rank,sort_num,sort_str,child_key))`, written in the same transaction as every write under an indexed path (insert/delete/update of the child's key leaf; a subtree set or delete under the parent must delete and rebuild idx rows), rebuilt in the background when rules change `.indexOn`, and the query = index range scan with startAt/endAt/limit, then fetch the children by PK range. I measured the numeric-only version: write overhead is one extra insert in the same transaction (measured 0.14 ms vs 0.15 ms baseline, i.e. within noise), read goes from 7.6 ms / 330 KB to 0.2 ms / 6 KB. Because RTDB without `.indexOn` does the filtering on the server too (Firebase warns and downloads), Aruma can ship v0.2 with the unindexed path (server evaluates by loading the children, which is what row 1 measures) and add `idx` later.

## 5. Interpretation (Q2, Q3, Q8)
1. **Meets 1,000 writes/s with headroom?** M1: yes, by a wide margin, even on a laptop with random-page writes: 6k writes/s at N=1 (optimistic on macOS fsync, see caveat), 43k at N=16, 105k at N=128, and still 15-19k/s with full set-semantics (3 index probes per write). M5: same or better (6k/52k/133k) because it adds only a log insert. **M3d2: about 720-970 writes/s regardless of group size, i.e. at the Firebase limit with no headroom** (each write rewrites a ~300 KB TOASTed room doc; group commit does not help because N writes in the same doc = N rewrites). **M3d1 (jsonb per top-level child) fails: 2 writes/s**, 0.5 s per write, 625 ms to delete a room, 30 ms to read a single message, 612 MB of bloat from 80 tiny updates. M3 is out for any non-trivial tree; at most usable if docs are small and bounded.
2. **Where M1 hurts**: large subtree reads. Reading 2,000 messages (6,000 rows) costs 4.4 ms (p95 7); reading 50k users (150k rows, 5 MB) costs ~107 ms (p95 137), about 1,000x a single-message read and 3-5x slower than M3d2/M5 for the same data (23 ms / 31 ms); cold cache is much worse (90 ms for 9.7k rows, 287 ms for the 150k seq scan). Size on disk is ~112 MB for 17 MB of values: ~6.6x amplification (path text repeated in every row and in the PK index: 43 MB index + 69 MB heap), versus 19 MB for M3d2. Deep and wide reads are therefore the pressure points. Mitigations to test later (not measured): keep the leaf value as `jsonb` only when needed, stream the cursor straight into the JSON encoder instead of building a tree (walk rows in path order with a stack; sibling order still needs care), cache hot subtrees in memory (that is M5).
3. **M5 (memory + log)** is the fastest in everything it can serve from RAM (message read < 1 us, room 1 ms, all users 31 ms, delete 0.17 ms) and its write path is the same cost as M1's plain upsert. Costs: ~170-185 B per leaf of RAM (600k leaves = 110 MB; 10M leaves ~1.8 GB); the whole database of every project must fit in RAM or be demand-loaded; cold start replays the log (8.5 s for 3.8M rows) unless snapshotted; the log grows without bound (507 MB after the benchmark) so needs compaction into a snapshot; and it is a single-node design (fits Aruma's single node first, but blocks later scale-out without a cache invalidation story).
4. **Recommendation for the plan (my inference from these numbers):** store the tree as **M1 (row per leaf, path COLLATE "C", jsonb leaf value)**: simplest to get right, transactional multi-path updates are natural (one statement), subtree delete is one range delete, writes are 40x over the Firebase limit with group commit, and per-project isolation is just a `db`/project column or a schema. Put an in-memory cache of hot listened-to subtrees (an M5-lite) on top only if read-heavy listeners prove slow. For `set` semantics implement ancestor-leaf + descendant deletion in the same statement (cost measured above: still 15-19k writes/s at N=16-128). Defer `.indexOn` to an `idx` table; ship v0.2 with client-side-equivalent evaluation (server loads children and filters), and enforce Firebase-like caps to keep it affordable: the measured cost of an unindexed 2,300-child query is only ~4-8 ms server side, so the cap that matters is response size (they allow 256 MB reads) rather than CPU.
5. **Limits (Q8)** worth enforcing from the numbers: 1,000 writes/s per database is trivially achievable; per-write size cap 16 MB from SDK (256 MB REST) is the real cost driver for M1 (a 16 MB write is ~200k leaves in one transaction; I did not measure it); depth 32 and key length 768 B bound the path column (max path length can reach tens of KB; B-tree index entries are limited to about 2.7 KB per btree page, so **a path longer than ~2,700 bytes will fail to insert into the PK index**, from Postgres limits, not tested here. Needs a hash column or a path length cap below Firebase's 32 x 768 B).

## 6. Threats to validity
- Single machine, localhost, one connection, warm cache, 8 GB RAM (tables fit in shared_buffers/OS cache). macOS fsync semantics (see section 0). No concurrent readers during writes. Results are medians over 15-600 iterations; M3d1 uses n=5-15 because each op is slow.
- (g) deletes a room of 2,000 messages that was just inserted (hot in cache). M5 group-commit rows do not include network/serialization of incoming events or fan-out.
- Go-side JSON reconstruction for M1 uses a nested map and sorts keys; a streaming encoder would be faster.
- Citations marked "from memory" were not re-fetched; the Postgres and Firebase pages were fetched on 2026-10-20.
