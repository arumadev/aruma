# Benchmarks

These are measured numbers for Aruma, with the machine, the method and the command to repeat each one. Read the limits at the end before you compare them with anything else.

The Auth, Realtime Database and platform numbers come from one laptop, and the Document Database, Storage and soak numbers from a cloud VM. Both machines were also running other work. They show what the code does on modest hardware. They are not a promise for your server. Run the tools below on your own hardware before you size a deployment.

## What was measured

| Service | Measured | Not measured yet |
|---|---|---|
| Auth | Sign-up, password sign-in, token refresh, custom-token sign-in, ID token verification | Phone and other sign-in methods, a large user table, a separate load-generator host, TLS |
| Realtime Database | Durability under kill, connections, fan-out, writes per second, queries on large nodes, long polling | Security rules (the load runs used open rules), more than 10,000 connections, the ceiling of fan-out, a long soak |
| Management API and console | Reads, service-account token exchange, login | Writes at scale |
| Document Database | Durability under kill, commits, 500-write batches, reads, queries with and without a composite index, Listen fan-out to 500 listeners, a 90-minute soak | Security rules (the runs used `documents.dev_open_rules`), a large database, more than 500 listeners, aggregations, a separate load host |
| Storage | Durability under kill, uploads and downloads of 1, 20 and 200 MB at 1, 8 and 32 clients (200 MB at 1 only), a 90-minute soak | Security rules (open rules), 200 MB objects in parallel, a disk of its own, TLS |

## Machine and software

| | |
|---|---|
| Machine | MacBook Air (M1, 2020), fanless, 8 GiB of memory |
| CPU | Apple M1, 8 cores (4 performance, 4 efficiency) |
| OS | macOS 15.5 |
| Go | 1.27.2 (darwin/arm64), `GOMAXPROCS` 8 |
| Postgres | 18.1, local, default settings: `shared_buffers` 128 MB, `fsync` on, `synchronous_commit` on. The data directory was on an external USB disk |
| Database pool | 8 connections: pgx's default then, max(4, CPUs). Since v0.5 Aruma's default is max(16, 4 × CPUs), at most 64: 32 on this machine |
| Network | Loopback only, plain HTTP/1.1 with keep-alive, no TLS |
| Layout | Load generator, Aruma and Postgres on the same 8 cores |
| Date | October 2026 (Auth and platform on 18 October, Realtime Database on 21 October) |

Other programs used the machine during every run: browser tests, an emulator and indexing. Their load changed from minute to minute. Each table gives the median of three runs, with the range in brackets where it matters. Treat differences under about 15% as noise. Tail latencies (p99 and above) and the password scenarios move the most.

## How the load tool works

`tools/bench` creates a throwaway database, starts Aruma with production settings (including the real password-hashing cost), seeds projects and users through the public APIs, and sends requests. It runs each scenario for a 2 second warm-up and then a 10 second measured window.

- **Closed loop** (the default). A fixed number of workers each send the next request as soon as the last one returns. This gives throughput at that concurrency.
- **Open loop** (`-rate`). Requests are sent on a fixed schedule, and latency counts from the scheduled time. If the machine stalls, the queueing a real client would see is included.
- Percentiles come from a histogram with at most 1.6% error, and never understate. Only 2xx replies count toward latency. Other statuses are counted separately.
- The per-address rate limits are bypassed on purpose: the bench spreads requests over 65,536 client addresses, so it measures the work behind the limiter and not the 429 replies.

The bench needs a local Postgres named by `ARUMA_TEST_DATABASE_URL`. It creates a database called `aruma_bench_<random>` and drops it on exit.

## Auth

Measured with `tools/bench`. Closed loop, 10 s per scenario, median of three runs. Concurrency is 8, or 4 for the two password scenarios (the server hashes at most four passwords at once).

| Scenario | Requests per second | p50 | p99 | Limited by |
|---|---|---|---|---|
| ID token verification on the server (`accounts:lookup`) | 6,451 | 1.0 ms | 3.8 ms | 6 database queries per request |
| Token refresh | 1,898 | 4.1 ms | 7.6 ms | Signing the new ID token (RSA-2048) |
| Custom-token sign-in | 1,616 | 4.6 ms | 15.3 ms | Signing the new ID token |
| Password sign-in | 95 | 40 ms | 71 ms | Password hashing (argon2id), by design |
| Sign-up | 89 | 43 ms | 86 ms | Password hashing (argon2id), by design |

No request failed: 0 errors in 307,083 requests across the three runs.

At a fixed 600 requests per second (open loop), refresh and custom-token sign-in answer in 2 to 3 ms at the median, and a lookup in 1.3 ms. At 50 requests per second, password sign-in and sign-up answer in 29 ms.

**Password hashing sets the ceiling for sign-in and sign-up.** One verification costs 24 to 26 ms of CPU at the production setting. With four running at once, this machine tops out at about 105 to 120 per second. The scenarios reach about 80% of that. A server with more cores raises it. The cost is the security setting, so do not lower it to chase a number.

**The Go library** (`sdk/go/adminauth`) verifies an ID token on your own server, with no call to Aruma once the keys are cached:

| | 1 core | 4 cores | 8 cores |
|---|---|---|---|
| Time per token | 49 µs | 17 µs | 16 µs |
| Tokens per second | about 20,600 | about 59,000 | about 64,000 |

Checking revocation on top costs one server round trip, about 128 µs per token across 8 goroutines on loopback.

### Reproduce

```sh
# The Auth scenarios, three closed-loop runs
go run ./tools/bench -json run.json \
  -scenarios auth-signup,auth-signin-password,auth-refresh,auth-lookup,auth-signin-custom-token

# Open loop at a fixed rate
go run ./tools/bench -rate 600 -c 64 -scenarios auth-refresh,auth-lookup,auth-signin-custom-token
go run ./tools/bench -rate 50 -login-c 16 -scenarios auth-signup,auth-signin-password

# The floors: password hashing and RSA signing on your machine
go test ./tools/bench -run '^$' -bench 'Argon2|RS256' -cpu 1,4,8

# The Go library's local verification (from sdk/go/adminauth)
go test -run '^$' -bench Verify -benchtime 3s -cpu 1,4,8
```

## Realtime Database

Two tools measure it. `tools/rtdb-chaos` checks durability. `tools/bench -rtdb` measures load against the real `aruma serve` binary in its own process. The load runs used open rules (`rtdb.dev_open_rules`), so rule evaluation is not included.

### Durability

The chaos tool drives a database with 8 to 32 clients and kills the server at random with SIGKILL, then restarts it. At the end it reads the whole database and compares it with what the clients were told.

| Result over 12 runs | |
|---|---|
| SIGKILL / SIGTERM stops | 81 / 12 |
| Acknowledged writes | 101,354 |
| Acknowledged writes lost | 0 |
| Writes applied twice | 0 |
| Violations of any check | 0 |

Every database's write counter matched the number of writes that ran. `onDisconnect` writes ran on every normal close and every SIGTERM. They are lost on SIGKILL, an out-of-memory kill or a power cut, because they are held in server memory until the connection ends. Clients register them again when they reconnect.

### Load

Median of three runs, with the range in brackets. Closed loop unless noted.

| Measurement | Result |
|---|---|
| Writes, 1 database, 1 writer | 1,519/s (824 to 2,231), p50 0.45 ms |
| Writes, 1 database, 8 writers | 3,228/s, p50 2.2 ms |
| Writes, 1 database, 32 writers | 4,540/s (2,339 to 5,094), p50 6.8 ms, p99 12.7 ms |
| Writes, 1 database, 128 writers | 3,248/s, p50 33 ms |
| Writes, 32 REST writers | 3,286/s, p50 8.4 ms |
| Writes, 10 databases, pool of 8 (the default then) | 4,049/s, p50 33 ms |
| Writes, 10 databases, pool of 32 | 6,734/s, p50 15 ms |
| Connections held by one server | 10,000 (idle, and with one listener each) |
| Memory per connection | about 40 KiB, plus about 5 KiB per listener |
| Fan-out, 1 write to 1,000 listeners (20 writes/s) | 20,000 deliveries/s, p50 8 ms, p99 85 ms |
| Fan-out, 1 write to 100 listeners | p50 3.4 ms, p99 11 ms |
| Long polling against WebSocket | Within about 30% for a single writer, equal under concurrency |

Zero failures in about 1.07 million writes. Every one of 661,200 fan-out deliveries arrived.

What to know:

- **One database saturates at about 4,500 to 5,000 sets per second** on this machine. Each database has a single writer, and more than about 32 clients only adds queueing.
- **Size the Postgres pool above the number of databases that write at once.** Ten busy databases on a pool of 8 queue for connections. Raising `pool_max_conns` to 32 gave 66% more throughput, which is why the default is now max(16, 4 × CPUs), at most 64. See the [operator guide](guides/operations.md).
- **The connection limit on this machine is the operating system.** macOS allows about 10,240 open files per process, so one server holds about 10,220 connections. A Linux server with a raised `LimitNOFILE` was not measured.
- **Queries on large nodes are the weak spot.** A `limitToLast(50)` listener reads and sorts the whole node, because there is no persistent index yet. That takes about 35 ms at 10,000 children and 0.3 to 2.6 seconds at 100,000. Every write under such a listener repeats the work inside the database's single writer, so it holds up all writes to that database. Keep filtered listeners to nodes of a few thousand children.
- **A write that takes longer than 30 seconds fails** with `503 The service is currently unavailable` and applies nothing. This happened once, on a machine that was swapping with a load average above 100.

For comparison, Firebase documents about 1,000 writes per second per database and 200,000 simultaneous connections. Aruma was above the first figure on this laptop. It was not tested anywhere near the second.

### Reproduce

```sh
# Durability: 6 SIGKILL cycles then a SIGTERM, per seed. A seed replays a run's operations and kills.
go run ./tools/rtdb-chaos -seed 101 -runs 6 -cycles 6
go run ./tools/rtdb-chaos -seed 201 -runs 3 -cycles 8 -clients 32
go run ./tools/rtdb-chaos -seed 301 -runs 3 -cycles 7 -clients 16

# Load: every scenario (about 10 minutes)
go run ./tools/bench -rtdb all -json run.json

# One scenario, smaller
go run ./tools/bench -rtdb query -rtdb-query-sizes 10000 -duration 5s
```

The scenarios are `conns`, `fanout`, `writes`, `query` and `lp`. Both tools build `./cmd/aruma` unless you pass `-bin` (chaos) or `-rtdb-bin` (bench). The chaos tool serves on port 8780. The bench serves on 8785 and its metrics on 8786.

## Management API and console

These are the paths a developer uses, not end-user traffic. Closed loop, concurrency 8 (4 for login), median of three runs, 4.5 million requests in all, 0 failures.

| Scenario | Requests per second | p50 | p99 |
|---|---|---|---|
| `GET /healthz` | 45,160 | 0.15 ms | 0.61 ms |
| List projects | 13,787 | 0.50 ms | 1.34 ms |
| Console: list apps | 12,723 | 0.58 ms | 1.34 ms |
| Fetch a web app config | 8,315 | 0.91 ms | 1.87 ms |
| Service-account token exchange | 7,191 | 1.0 ms | 2.3 ms |
| Console login | 93 | 42 ms | 55 ms |

Throughput stops growing at about 16 workers on this machine, and a bigger database pool did not help: the 8 cores are the limit. Token exchange and login are rate limited per client address (120 token exchanges per minute with a burst of 60, and 10 logins per minute). Behind a reverse proxy, set `trusted_proxies`, or every client shares the proxy's address and one budget. See [configuration](reference/configuration.md).

```sh
go run ./tools/bench -json run.json                  # every platform scenario, three runs
go run ./tools/bench -rate 3000 -c 64 -scenarios healthz,mgmt-list-projects,mgmt-web-config,console-list-apps,oauth-token
go run ./tools/bench -serve                          # start a server and run the load from another process
```

## Document Database

Measured on a different machine from the sections above, in November 2026, on the code of v0.5.0 before release:

| | |
|---|---|
| Machine | Cloud VM, 4 vCPUs (Intel Xeon at 2.1 GHz), 15 GiB of memory, Linux 6.18 |
| Postgres | 18.6 in Docker on the same VM, default settings, `fsync` on, on the VM's virtual disk |
| Database pool | The v0.5 default, max(16, 4 × CPUs): 16 |
| Layout | Load generator, Aruma and Postgres on the same 4 vCPUs, loopback, gRPC over h2c, no TLS |

The VM was shared with other test jobs. Each number gives the 1-minute load average when its scenario started (`load1`): the tools themselves raise it, so read it as a sign of how busy the rest of the machine was before each run. One run of each scenario, not a median: treat differences under about 25% as noise.

### Durability

`tools/docs-chaos` drives one database with 8 to 12 writers over gRPC and REST, and 2 or 3 Listen streams, and kills the server with SIGKILL at random every 0.5 to 3 seconds. Writers commit single writes, batches of 2 to 20 documents with increments on shared counters, BatchWrite, and read-write transactions (pessimistic for admin clients, optimistic for end users). A write whose reply was lost is read back after the restart.

| Result over 10 runs | |
|---|---|
| SIGKILL / SIGTERM stops | 100 / 10 |
| Acknowledged commits (with a commit time) | 27,557 |
| Acknowledged transactions | 6,209 (of them, 799 refused `ABORTED` by contention and retried) |
| Replies lost to a kill, then read back | 873 (88 had been applied) |
| Acknowledged writes lost | 0 |
| Refused or unapplied writes found applied | 0 |
| Commits split (part of their documents applied) | 0 |
| Increments lost or applied twice | 0 (every counter equals the sum of the documents that exist) |
| Gaps or repeats in transactional counters | 0 |
| Seq gaps, or commit times not rising with seq or with real time | 0 |
| Listen resumes checked against the query at their read time | 253 resumes, 0 differences, 0 documents sent again |

### Load

`tools/dsbench`, closed loop, 2 s warm-up and a 10 s window per scenario, over gRPC with an admin token. The collection queried holds 20,000 documents with `a` in 0..99, so each query matches 200.

| Scenario | Clients | Per second | p50 | p95 | p99 | load1 |
|---|---|---|---|---|---|---|
| Commit, 1 write | 1 | 378 | 2.5 ms | 3.5 ms | 4.8 ms | 1.03 |
| Commit, 1 write | 8 | 783 | 9.3 ms | 16.9 ms | 22.5 ms | 1.10 |
| Commit, 1 write | 32 | 618 | 51 ms | 66 ms | 78 ms | 1.17 |
| Commit, 500 writes | 1 | 18 (9,000 documents/s) | 50 ms | 83 ms | 99 ms | 1.82 |
| Commit, 500 writes | 4 | 48 (24,000 documents/s) | 83 ms | 161 ms | 421 ms | 1.69 |
| Get one document | 32 | 5,074 | 6.0 ms | 10.8 ms | 13.8 ms | 2.16 |
| Query `a == x`, limit 50 (single-field index) | 8 | 603 | 12.6 ms | 21.0 ms | 25.8 ms | 4.12 |
| Query `a == x` order by `b` desc, limit 50, no composite index (`index_enforcement: off`) | 8 | 38 | 209 ms | 276 ms | 310 ms | 5.08 |
| The same query with the composite index (`a`, `b` desc) | 8 | 595 | 12.9 ms | 21.3 ms | 26.3 ms | 5.77 |
| Listen fan-out: 10 writes/s to one document, 100 listeners | 100 | 910 deliveries/s | 7.0 ms | 11.4 ms | 143 ms | 5.81 |
| Listen fan-out: 10 writes/s, 500 listeners | 500 | 4,350 deliveries/s | 11.7 ms | 18.8 ms | 27 ms | 4.52 |

No request failed, and every fan-out delivery arrived (9,100 and 43,500).

What to know:

- **Single commits saturate at about 800 per second with 8 clients** on 4 vCPUs. More clients only queue: at 32 the p50 is 51 ms for fewer commits. Commits of one database go through `commit_concurrency` (4) slots and one sequencer row.
- **Batch your writes.** A 500-write batch commits 9,000 to 24,000 documents per second, against 800 for single writes.
- **Create the composite index your query needs.** Without it the query is about 16 times slower here (209 ms against 13 ms at the median), and it grows with the collection: the reference path reads every document of the collection. With the default `index_enforcement: enforce` such a query is refused with the console link to create the index, as in production.
- **Fan-out to 500 listeners** of one document adds about 12 ms at the median.

A noisy-neighbour run with a tool that is not in the repository (one hot project among four, pool of 16) gave the quiet projects a p50 of 5 to 8 ms (baseline 5 ms) while the hot project wrote 1,522 RTDB sets/s or 508 Document Database commits/s, and 18 to 20 ms (p99 270 ms) while it read a 50,000-child RTDB node in a loop (load1 1.9 at start).

## Storage

The same machine and method as the Document Database section. The data directory is on the VM's virtual disk, which Postgres shares.

### Durability

`tools/storage-chaos` drives one bucket with 8 clients, each owning 6 objects, with simple, multipart and resumable uploads of 32 bytes to 12 MB, deletes, metadata updates and downloads, and kills the server with SIGKILL at random every 0.5 to 3 seconds. Each object's bytes name their version and size, so any download that is cut short, padded or of another version is caught.

| Result over 7 runs | |
|---|---|
| SIGKILL / SIGTERM stops | 70 / 7 |
| Acknowledged uploads, deletes and metadata updates | 16,231 |
| Resumable uploads acknowledged | 4,247 |
| Resumable chunks whose reply was lost, then resumed from the offset the server reported | 1,803 (66 at another offset than the client's) |
| Acknowledged changes lost, refused changes applied, objects served with wrong or missing bytes | 0 |
| Object rows without their whole file, checked while the server was down after each kill | 0 |
| Orphan files left by the kills (counted while down, last 3 runs) | 53, all deleted by the sweep at the next boot |
| Files left at the end, after gc_grace and the session expiry | 0 |

### Load

`tools/dsbench`, closed loop, simple uploads (`/v0`, one request per object) and downloads (`alt=media`), 2 s warm-up and a 10 s window. Each client overwrites its own object, so the GC deletes the replaced files during the run.

| Object | Clients | Uploads | Downloads | Upload p50 / p99 | Download p50 / p99 | load1 |
|---|---|---|---|---|---|---|
| 1 MB | 1 | 106/s (106 MB/s) | 885/s (885 MB/s) | 9.1 / 16 ms | 1.0 / 3.4 ms | 3.6 |
| 1 MB | 8 | 242/s (242 MB/s) | 2,125/s | 31 / 64 ms | 3.1 / 13 ms | 3.5 |
| 1 MB | 32 | 168/s (168 MB/s) | 2,130/s | 167 / 858 ms | 13 / 46 ms | 4.6 |
| 20 MB | 1 | 216 MB/s | 1,482 MB/s | 90 / 129 ms | 13 / 24 ms | 6.5 |
| 20 MB | 8 | 364 MB/s | 2,516 MB/s | 406 / 845 ms | 57 / 148 ms | 6.0 |
| 20 MB | 32 | 362 MB/s | 2,637 MB/s | 1.7 / 3.3 s | 229 / 481 ms | 8.4 |
| 200 MB | 1 | 240 MB/s | 1,660 MB/s | 0.8 / 1.1 s | 118 / 149 ms | 9.0 |

No request failed. 200 MB objects at 8 and 32 clients were not measured: the VM's disk did not have the room.

What to know:

- **Uploads are bound by the disk**: every object is written, fsynced with its directory, and committed with a synchronous Postgres commit before the reply. About 240 to 360 MB/s here; more clients only add latency, and 32 clients uploading 1 MB objects got less than 8.
- **Downloads are bound by the CPU** at about 2.1 to 2.6 GB/s here, from the page cache.

## Soak

`tools/soak` runs all four services at once for as long as you ask, samples the server every minute and fits a line to each series after a warm-up. In 90 minutes on the machine above (the default workload: 20 operations per second per service over 2,000 keys, 50 Realtime Database and 50 Document Database listeners):

| Series (slope fitted from minute 30 to 90) | First | Last | Slope per hour | Verdict |
|---|---|---|---|---|
| Resident memory | 127 MB | 126 MB | −2 MB (r −0.18) | flat |
| Go heap in use | 52 MB | 76 MB | +10 MB (r 0.27) | flat (GC sawtooth) |
| Goroutines | 302 | 295 | −6 (r −0.39) | flat |
| Open files | 109 | 99 | −8 (r −0.51) | flat |
| Postgres connections | 18 | 18 | −2 (r −0.35) | flat |
| Data directory | 575 MB | 590 MB | −10 MB (r −0.15) | flat: the Storage GC keeps up |
| Database size | 50 MB | 98 MB | +49 MB (r 0.99) | still growing at 90 minutes, see below |

361,759 operations, 21 failed (all Realtime Database writes in the minute an outside test run terminated the server's lock session, see the limits below), 6.4 million listener deliveries, and every key's last acknowledged write was there at the end (4,501 keys).

The database grew all through the 90 minutes. Part of it is by design and stops: the Document Database keeps an hour of changelog and versions (`documents` retention), which was still filling at minute 90, and that run's Auth workload created a new user every 5 seconds. The tool now signs in a fixed pool of users instead, and its default warm-up for a long run is 6 hours, past every retention window, so a 72-hour run tells growth that stops from growth that does not. A 90-minute run cannot.

### Reproduce

```sh
# Durability: 10 SIGKILL cycles then a SIGTERM, per seed
go run ./tools/docs-chaos -seed 301 -runs 3 -cycles 10
go run ./tools/storage-chaos -seed 401 -runs 3 -cycles 10

# Load: every Document Database and Storage scenario (about 6 minutes), on a server it starts
go run ./tools/dsbench -start -scenarios all -json run.json
go run ./tools/dsbench -start -scenarios storage -sizes 200 -parallel 1,8 -max-bytes 8000000000

# Soak: on your own server, for 72 hours; the exit status is the verdict
go run ./tools/soak -bin /usr/local/bin/aruma -d 72h -out soak.csv
```

The chaos tools serve on ports 8790 and 8795, `dsbench -start` on 8796, the soak on 8797 and its metrics on 8798; each takes `-port`.

## Limits of these numbers

- **A laptop, not a server.** It is fanless and can throttle. Four of the eight cores are efficiency cores.
- **Everything on one machine.** Throughput belongs to the load generator, Aruma and Postgres together. A separate load host would raise it.
- **Loopback, no TLS.** Real clients add round trips and handshakes. Aruma expects TLS from a reverse proxy in front of it.
- **Small, warm data.** Tens of projects and a few thousand users fit in memory. Years of data, table growth and vacuum were not measured.
- **A shared, busy machine.** See the noise note above. A dedicated machine should match or beat the high end of every range.
- **Rules were not measured** for the Realtime Database. A real rule set adds its evaluation to every write and listen.
- **One Postgres setup.** Defaults, on a USB disk (laptop) or a shared virtual disk (VM), with `fsync` on. Commit latency and Storage upload throughput depend on your disk.
- **The Document Database and Storage numbers are one run each** on a shared VM, not medians of three.
- **The soak ran 90 minutes, not 72 hours.** Memory, goroutines, files, connections and the data directory were flat; the database size needs a run longer than its retention windows to judge.
