# Benchmarks

These are measured numbers for Aruma, with the machine, the method and the command to repeat each one. Read the limits at the end before you compare them with anything else.

All numbers come from one laptop that was also running other work. They show what the code does on modest hardware. They are not a promise for your server. Run the tools below on your own hardware before you size a deployment.

## What was measured

| Service | Measured | Not measured yet |
|---|---|---|
| Auth | Sign-up, password sign-in, token refresh, custom-token sign-in, ID token verification | Phone and other sign-in methods, a large user table, a separate load-generator host, TLS |
| Realtime Database | Durability under kill, connections, fan-out, writes per second, queries on large nodes, long polling | Security rules (the load runs used open rules), more than 10,000 connections, the ceiling of fan-out, a long soak |
| Management API and console | Reads, service-account token exchange, login | Writes at scale |
| Document Database | Nothing | Everything: no throughput, latency or load numbers exist yet |
| Storage | Nothing | Everything: no upload or download throughput numbers exist yet |

## Machine and software

| | |
|---|---|
| Machine | MacBook Air (M1, 2020), fanless, 8 GiB of memory |
| CPU | Apple M1, 8 cores (4 performance, 4 efficiency) |
| OS | macOS 15.5 |
| Go | 1.27.2 (darwin/arm64), `GOMAXPROCS` 8 |
| Postgres | 18.1, local, default settings: `shared_buffers` 128 MB, `fsync` on, `synchronous_commit` on. The data directory was on an external USB disk |
| Database pool | pgx default, 8 connections |
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
| Writes, 10 databases, default pool (8 connections) | 4,049/s, p50 33 ms |
| Writes, 10 databases, pool of 32 | 6,734/s, p50 15 ms |
| Connections held by one server | 10,000 (idle, and with one listener each) |
| Memory per connection | about 40 KiB, plus about 5 KiB per listener |
| Fan-out, 1 write to 1,000 listeners (20 writes/s) | 20,000 deliveries/s, p50 8 ms, p99 85 ms |
| Fan-out, 1 write to 100 listeners | p50 3.4 ms, p99 11 ms |
| Long polling against WebSocket | Within about 30% for a single writer, equal under concurrency |

Zero failures in about 1.07 million writes. Every one of 661,200 fan-out deliveries arrived.

What to know:

- **One database saturates at about 4,500 to 5,000 sets per second** on this machine. Each database has a single writer, and more than about 32 clients only adds queueing.
- **Size the Postgres pool above the number of databases that write at once.** Ten busy databases on the default pool of 8 queue for connections. Raising `pool_max_conns` to 32 gave 66% more throughput. See the [operator guide](guides/operations.md).
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

## Document Database and Storage

There are no benchmark numbers yet. Fault injection and load testing of the Document Database are listed as not done in the [changelog](CHANGELOG.md#v040). `tools/bench` has no Document Database or Storage scenarios. Do not assume these services perform like the Realtime Database figures above.

## Limits of these numbers

- **A laptop, not a server.** It is fanless and can throttle. Four of the eight cores are efficiency cores.
- **Everything on one machine.** Throughput belongs to the load generator, Aruma and Postgres together. A separate load host would raise it.
- **Loopback, no TLS.** Real clients add round trips and handshakes. Aruma expects TLS from a reverse proxy in front of it.
- **Small, warm data.** Tens of projects and a few thousand users fit in memory. Years of data, table growth and vacuum were not measured.
- **A shared, busy machine.** See the noise note above. A dedicated machine should match or beat the high end of every range.
- **Rules were not measured** for the Realtime Database. A real rule set adds its evaluation to every write and listen.
- **One Postgres setup.** Defaults, on a USB disk, with `fsync` on. Commit latency depends on your disk.
