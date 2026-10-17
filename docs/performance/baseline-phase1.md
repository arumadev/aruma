# Performance baseline: Phase 1 platform

Results for exit criterion X-8 ("baseline published on declared hardware"). Measured on 2026-10-18 with [`tools/bench`](../../tools/bench) at commit `0a0f8c7` (`0a0f8c7fc49c35a740766c92109d6798f6a4d7f5`). The raw results are in [baseline-phase1/](baseline-phase1/), one JSON file per run.

**In short:** on a fanless M1 laptop shared with other work, with load generator, server and Postgres on the same 8 cores:

- The read paths of the Management API and the console serve about **8,000–14,000 requests/s** at concurrency 8, with p99 under 2.5 ms.
- The service account token exchange serves about **7,000/s**.
- Login is about **90/s**, bound by argon2id, as designed.
- No request failed: 0 errors in the 4.5 million requests across all published runs.

## Hardware and software

| | |
|---|---|
| Machine | MacBook Air (M1, 2020), `MacBookAir10,1`, fanless |
| CPU | Apple M1, 8 cores (4 performance + 4 efficiency), L2 12 MiB (P) / 4 MiB (E) |
| Memory | 8 GiB |
| OS | macOS 15.5 (24F74), Darwin 24.5.0 |
| Go | go1.27.2 darwin/arm64 (`go.mod`: go 1.26.0), GOMAXPROCS 8 |
| Postgres | 18.1, the DBngin build (`/Users/Shared/DBngin/postgresql/18.1`), local on 127.0.0.1:5432. Data directory on an external USB APFS volume. Defaults otherwise: `shared_buffers` 128 MB, `fsync` on, `synchronous_commit` on, `wal_sync_method` open_datasync, `max_connections` 100 |
| Database pool | pgx v5.11.0 default: max(4, NumCPU) = **8 connections** |
| Network | loopback only (127.0.0.1), plain HTTP/1.1 with keep-alive |

## How to reproduce

```sh
# The published runs: three closed-loop runs of every scenario
go run ./tools/bench -json run.json

# Open loop at a fixed arrival rate, up to 64 requests in flight
go run ./tools/bench -rate 3000 -c 64 -scenarios healthz,mgmt-list-projects,mgmt-web-config,console-list-apps,oauth-token
go run ./tools/bench -rate 60 -login-c 16 -scenarios console-login

# Concurrency sweep and pool size
go run ./tools/bench -scenarios mgmt-web-config,console-list-apps -c 32 -duration 5s -warmup 1s
go run ./tools/bench -scenarios mgmt-web-config,console-list-apps -c 32 -duration 5s -warmup 1s -db-pool-max 32

# Load generator in its own process
go run ./tools/bench -serve          # prints the -url/-token/-email/-password to use below
go run ./tools/bench -url http://127.0.0.1:PORT -token aruma_pat_… -email … -password …

# Where the time goes, and the floor under login
go run ./tools/bench -scenarios oauth-token -cpuprofile cpu.pprof && go tool pprof -top -cum cpu.pprof
go test ./tools/bench -run '^$' -bench Argon2 -benchtime 100x -cpu 1,4
```

It needs only the local Postgres named by `ARUMA_TEST_DATABASE_URL`, the same one the tests use. Each run creates a database `aruma_bench_<random>` and drops it on exit, including on Ctrl-C.

The published runs used a binary built from a clean `git archive` of `0a0f8c7`. Other sessions had uncommitted changes in the working tree at the time, and a plain `go run` would have measured those. The commit is recorded in every JSON file.

## Method

**Setup.** The bench creates a throwaway database, migrates it and starts Aruma in its own process on a loopback port. It is wired like `aruma serve`: production argon2id cost (m=19 MiB, t=2, p=1, at most 4 hashes at once), the JSON request log (written to `io.Discard`, so formatting is paid), the same `http.Server` timeouts, and the default pool. It then seeds 10 projects through the public console API with a CLI token, as a developer would. Each project gets a web, an Android and an iOS app, and the first also gets a service account key. Finally it signs in once to get a session cookie.

**Scenarios.**

| Scenario | Request | What it exercises | DB queries per request (measured) |
|---|---|---|---|
| `healthz` | `GET /healthz` | HTTP stack, middleware, request log | 0 |
| `mgmt-list-projects` | `GET /v1beta1/projects` | CLI token HMAC and lookup, then the owner's project list | 2 |
| `mgmt-web-config` | `GET /v1beta1/projects/-/webApps/{app}/config` | token, project resolved from the app id and ownership check, config file generated | 6 (see finding 1) |
| `console-list-apps` | `GET /console/v1/projects/{p}/apps` with the session cookie | session lookup, ownership, app list | 3 |
| `oauth-token` | `POST /oauth2/v1/token`, RS256 JWT bearer grant | key lookup, RSA signature check, token insert (a write) | 3 |
| `console-login` | `POST /console/v1/auth:login` | argon2id verify, session insert (a write) | 2 |

Read requests cycle through the 10 projects and their web apps. The "DB queries" column is the server's pgx pool acquire count divided by requests in the measured window. It matches the code exactly, since every query takes one acquire.

**Driver.**
- **Closed loop by default.** A fixed number of workers each send their next request as soon as the last one returns. This gives throughput at that concurrency, with latency as per-request service time.
- **Open loop with `-rate`.** Requests are scheduled at a fixed rate, and latency is measured from each request's *scheduled* time, as in wrk2. When the server or the machine stalls, the queueing a real client would see is counted. In a closed loop it is hidden, because the generator itself slows down (coordinated omission).
- **Warm-up and measured window.** Every scenario runs 2 s of unrecorded warm-up (connections, pool, Postgres caches), then a 10 s measured window. A request belongs to the window if it started (or was scheduled) inside it. Requests still in flight at the end are awaited, so the slowest are never dropped.
- **Percentiles.** Nearest-rank, from a log-linear histogram (HdrHistogram layout: exact below 128 ns, then 64 buckets per power of two, so at most 1.6% error and never understated). Min, max, mean and count are exact. Latencies count 2xx responses only; anything else is counted by status. The histogram and driver have unit tests (`go test ./tools/bench`), including one showing that the open loop exposes queueing which the closed loop hides.

**Two deliberate choices.**
- **Credential endpoints are rate-limited per client address** (login 10/min, token 120/min with a burst of 60). From one loopback address the bench would only measure 429s. In-process mode therefore trusts loopback as a proxy, and `console-login` and `oauth-token` send every request with a fresh `X-Forwarded-For` address. This measures the work behind the limiter, not the limiter.
- **Service account assertions are signed up front**, 32 of them, valid for an hour, and reused. Otherwise the bench's own RSA signing, about 1 ms per assertion, would compete with the server for CPU. The server does not reject replayed assertions, the same as Google (security review I-7), so the requests are valid.

**Repetition and noise.** The machine was shared with other sessions (an iOS simulator, macOS storage indexing, other agents' builds and browser tests). A sampler summed the `%CPU` of every process except the bench and Postgres every 3 s. During the published runs these **co-tenants used 1.5–2.0 cores on average (peaks 2.5–5)**. The closed-loop suite ran three times back to back. Below, each column is the median of the three runs, with the min–max range in parentheses.

## Results

### Closed loop, concurrency 8 (login: 4), 10 s per scenario, three runs

| Scenario | ok/s | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Failed |
|---|---|---|---|---|---|---|---|
| `healthz` | 45,160 (42,620–52,442) | 0.145 | 0.315 | 0.61 (0.53–0.65) | 1.01 | 4.7 | 0 of 1,402,217 |
| `mgmt-list-projects` | 13,787 (13,591–14,347) | 0.500 | 0.877 | 1.34 (1.33–2.05) | 2.26 | 10.5 | 0 of 417,252 |
| `mgmt-web-config` | 8,315 (8,035–8,351) | 0.909 | 1.294 | 1.87 (1.85–2.13) | 3.08 | 15.8 | 0 of 247,012 |
| `console-list-apps` | 12,723 (12,332–13,380) | 0.582 | 0.885 | 1.34 (1.28–1.41) | 2.23 | 7.0 | 0 of 384,345 |
| `oauth-token` | 7,191 (5,425–7,464) | 1.008 | 1.655 | 2.29 (2.29–4.92) | 5.24 | 24.2 | 0 of 200,790 |
| `console-login` | 93 (93–98) | 41.9 | 47.2 | 55.1 (53.5–56.1) | 62.9 | 62.9 | 0 of 2,848 |

Files: `closed-loop-run1.json` to `closed-loop-run3.json`. Peak RSS of the bench process, client and server together: 234–266 MiB.

### Open loop at a fixed rate

3,000 requests/s per scenario, up to 64 in flight, two runs. Login: 60/s, up to 16 in flight, one run. Every scheduled request was sent and answered (30,000 of 30,000; 600 of 600).

| Scenario | p50 ms (run 1 / 2) | p90 ms | p99 ms | p99.9 ms | max ms |
|---|---|---|---|---|---|
| `healthz` | 0.162 / 0.162 | 0.186 / 0.262 | 0.26 / 3.01 | 0.57 / 6.68 | 1.5 / 14.3 |
| `mgmt-list-projects` | 0.229 / 0.225 | 0.254 / 0.252 | 0.47 / 0.48 | 1.15 / 1.16 | 4.6 / 4.4 |
| `mgmt-web-config` | 0.344 / 0.332 | 0.401 / 0.369 | 1.03 / 0.74 | 20.2 / 1.13 | 24.6 / 1.6 |
| `console-list-apps` | 0.260 / 0.248 | 0.647 / 0.270 | 8.91 / 0.42 | 33.3 / 0.75 | 37.0 / 1.4 |
| `oauth-token` | 0.352 / 0.422 | 0.942 / 0.999 | 4.00 / 2.08 | 26.7 / 14.2 | 32.4 / 19.7 |
| `console-login` (60/s) | 31.2 | 33.0 | 40.4 | 41.6 | 41.6 |

At about a third of capacity the median holds steady across runs, but p99 and above swing by an order of magnitude, and in different scenarios each run. The bench recorded how late it sent requests relative to the schedule: up to 18 ms in the bad windows (run 1, `mgmt-web-config` and `console-list-apps`), under 0.4 ms in the good ones. When the load generator itself falls behind, the whole machine stalled, not the server. The open loop charges that stall to latency, so these tails are an upper bound for this machine. Files: `open-loop-3000.json`, `open-loop-3000-run2.json`, `open-loop-login-60.json`.

### Concurrency and the database pool

`mgmt-web-config` and `console-list-apps`, closed loop, 5 s each, default pool of 8 connections:

| c | web-config ok/s | p50 ms | p99 ms | list-apps ok/s | p50 ms | p99 ms | acquires that waited | mean acquire, web-config / list-apps |
|---|---|---|---|---|---|---|---|---|
| 1 | 2,998 | 0.328 | 0.57 | 4,803 | 0.203 | 0.39 | 0% | 0 |
| 4 | 6,061 | 0.614 | 1.20 | 9,376 | 0.385 | 0.89 | 0% | 0 |
| 16 | 8,792 | 1.72 | 3.31 | 13,833 | 1.04 | 2.79 | 92–99% | 0.12 / 0.10 ms |
| 32 | 8,764 | 3.47 | 5.90 | 14,057 | 2.10 | 4.52 | 100% | 0.42 / 0.47 ms |
| 64 | 8,767 | 7.08 | 11.27 | 14,350 | 4.26 | 7.08 | 100% | 1.03 / 1.22 ms |

Throughput levels off between c=8 and c=16. From there latency grows in proportion to concurrency (Little's law), and the queue forms at the pool. At c=64, `mgmt-web-config` waits 6 × 1.03 ms ≈ 6.2 ms of its 7.1 ms median for connections.

A bigger pool does not help. At c=32, with the pool raised through `pool_max_conns`:

| Pool | web-config ok/s | p50 / p99 ms | list-apps ok/s | p50 / p99 ms | acquires that waited |
|---|---|---|---|---|---|
| 8 (default) | 8,764 | 3.47 / 5.90 | 14,057 | 2.10 / 4.52 | 100% |
| 16 | 9,270 | 3.21 / 7.41 | 14,392 | 1.98 / 5.96 | 91–97% |
| 32 | 9,476 | 2.98 / 10.35 | 14,567 | 1.87 / 7.80 | 0% |

Throughput gains are within noise (+3–8%), and p99 gets worse. With the pool out of the way, the queue moves to the CPU, which bench, server and 8–32 Postgres backends share. On this machine the 8 cores are the limit and the default pool is the right size. Files: `concurrency-c*.json`, `pool-16-c32.json`, `pool-32-c32.json`.

### Load generator in its own process

The same suite against `bench -serve` from a second process (`two-process.json`):

| Scenario | ok/s | p50 ms | p99 ms |
|---|---|---|---|
| `healthz` | 46,353 | 0.152 | 0.54 |
| `mgmt-list-projects` | 13,552 | 0.541 | 1.34 |
| `mgmt-web-config` | 7,908 | 0.967 | 1.84 |
| `console-list-apps` | 10,962 | 0.647 | 2.06 |
| `oauth-token` | 6,528 | 1.11 | 2.65 |
| `console-login` | 59 | 64.5 | 138 |

The five fast scenarios fall within or near the in-process range, so sharing a process with the client does not distort them noticeably. Login was lower in this run. A two-process run of the same suite at `9480310`, earlier the same evening (not published), gave 94/s with p50 41 ms. Login is the most sensitive scenario to co-tenant CPU (see below), so read this as noise, not as an effect of the process split.

## Where the time goes

CPU profiles of the bench process (client and server together) for 10 s of each scenario, from `-cpuprofile`:

- **`mgmt-web-config`.**
  - 69% of samples are in socket system calls: HTTP to the client, and the Postgres wire protocol for 6 round trips per request.
  - The handler accounts for 41% cumulative. Of that, `resolveApp` takes 32%, and it runs twice per request (finding 1). The first call, through `appOf`, is 17% of all CPU.
  - Request logging (slog JSON) is 0.5%.
- **`oauth-token`.**
  - `ExchangeAssertion` is 47% cumulative. The three queries dominate: `GetServiceAccountByEmail` 18%, `ListActiveKeysByEmail` 11%, the `CreateOAuthToken` insert 9%.
  - The RSA-2048 signature check is only 7.5%. "Crypto-heavy" here means verification, which is cheap; the expensive signing happens on the client.
- **`console-login`.**
  - argon2id is **81% of all CPU**. Everything HTTP and SQL is about 2%.
  - A further 14% is the Go runtime waking threads (`pthread_cond_signal`). The stacks end in the runtime, so this profile cannot attribute it to specific code.

**Login is argon2-bound by design.**
- One verify at the production cost takes **24–26 ms** on this machine (`BenchmarkArgon2Verify`).
- Four in parallel, the server's gate, take **8.4–9.3 ms per verify** (`BenchmarkArgon2VerifyParallel -cpu 4`). That is a ceiling of about **110–120 logins/s**, not the 160 that 4 × 1/25 ms would suggest. Four hashes at 19 MiB each compete for memory bandwidth, and half the cores are efficiency cores.
- The login scenario reaches about 93/s, roughly 80% of that ceiling. The rest goes to the request's other work, during which the gate sits idle with only 4 workers.
- At a fixed 60 logins/s, below the ceiling, p50 is 31 ms: one hash plus about 6 ms for everything else.
- `golang.org/x/crypto/argon2` has assembly only for amd64 and loong64. On arm64 it runs the generic Go code (`processBlockGeneric` in the profile), so an amd64 server with SSE4.1 may hash faster per core. This is information for sizing, not a defect.

## Findings

1. **`GET /v1beta1/projects/{p}/{webApps|iosApps|androidApps}/{app}/config` resolves the app twice.**
   - [`internal/admin/mgmt/handlers.go:360`](../../internal/admin/mgmt/handlers.go) calls `appOf` (→ `core.GetApp` → `resolveApp`: project lookup plus app lookup) only to check the platform.
   - Line 364 then calls `core.AppConfig`, whose first step ([`internal/tenancy/core/config.go:29`](../../internal/tenancy/core/config.go)) is the same `resolveApp`.
   - **Cost:** 2 of the endpoint's 6 queries are redundant, and the first `resolveApp` is 17% of process CPU under this load.
   - **Fix options:** check the platform on the app `AppConfig` already returns, or pass the resolved app in. Either should bring the endpoint to 4 queries, in line with `console-list-apps`.
   - **Severity: low.** The endpoint still serves about 8,000/s here, and it runs when a developer fetches config files, not on end-user traffic. Not fixed here: this task does not touch `internal/`.
2. **`pool_max_conns` in `ARUMA_DATABASE_URL` breaks `aruma migrate up`.**
   - pgxpool accepts pgx's pool parameters in the URL, and `aruma serve` honours them; the pool experiment above used exactly that.
   - `store.Migrate` ([`internal/store/migrate.go:34`](../../internal/store/migrate.go), called by `aruma migrate up` with the same variable) opens the URL with `database/sql`, which sends unknown parameters to Postgres. It fails with: `FATAL: unrecognized configuration parameter "pool_max_conns" (SQLSTATE 42704)` (reproduced while building the bench).
   - An operator who sizes the pool this way must strip the parameter for migrations. Low severity.
3. **The per-address limits cap one client's token and login throughput by design.** From the code, not measured: [`internal/admin/oauth/oauth.go:35`](../../internal/admin/oauth/oauth.go) and [`internal/admin/console/console.go:55`](../../internal/admin/console/console.go).
   - Token endpoint: `NewLimiter(120, 60)`, a sustained 2 requests/s per address with a burst of 60.
   - Login: 10/min with a burst of 10 (ADR-010 S-6).
   - Behind a reverse proxy without `trusted_proxies`, every client shares the proxy's address, and so shares one bucket. 120 token exchanges per minute for the whole instance is easy to reach with several backends refreshing tokens.
   - This is configuration (`trusted_proxies` is described in ADR-008 and `aruma.example.yaml`), but the number is worth knowing when sizing.

No other performance problem showed up: zero failures in every run, and pool waits only beyond the point where the CPU is already saturated.

## Caveats

- **A laptop, not a server.** Fanless (it can throttle under sustained load; runs were 10 s per scenario to limit this), 4 of its 8 cores are efficiency cores, 8 GiB of RAM.
- **Shared machine.** Co-tenants used 1.5–2 cores on average, with bursts. This shows most in tails (p99 and above) and in login.
  - Across the three closed-loop runs, the database-backed read scenarios agree within ±5% on throughput and p50.
  - `healthz` varied by about ±12%, and one `oauth-token` run was 25% below the others.
  - Treat differences of that size as noise.
- **Everything on one box.** Bench, server and Postgres compete for the same cores. Throughput is a property of this whole system, not of Aruma alone; a separate load-generator host would raise the numbers.
- **Loopback networking.** No network latency, no TLS (Aruma expects TLS from a reverse proxy in front of it). Real clients add round-trip time and handshakes.
- **Small, warm dataset.** 10 projects and 30 apps fit in `shared_buffers`. Writes (one session row per login, one token row per exchange: about 70,000 rows per token run) go to a fresh database. Neither reflects years of data.
- **Postgres on an external USB volume** with `fsync` on. Commit latency for the two write scenarios depends on that disk.
- **Phase 1 surface only.** The data services of later phases are not covered. Rerun the same tool, extended with new scenarios, to compare.
