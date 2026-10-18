# Performance baseline: Phase 2 Auth

Results for task T25: the baseline of sign-up, sign-in, refresh and ID token verification. Measured on 2026-10-18 with [`tools/bench`](../../tools/bench) at commit `e012c2b` (`e012c2b50c4f1db81d8cce91b8d69f82f91633c2`), on the same machine and with the same method as the [Phase 1 baseline](baseline-phase1.md). The raw results are in [baseline-phase2/](baseline-phase2/): one JSON file per run, the CPU profiles, and the output of the Go benchmarks.

**In short:** on a fanless M1 laptop shared with other agents' test suites, with load generator, server and Postgres on the same 8 cores:

- **ID token verification on the server** (`accounts:lookup`) serves about **6,500/s** at concurrency 8, p50 1.0 ms. It is bound by its 6 database queries, not by RSA: the signature check is 3% of CPU.
- **Refresh** (Secure Token grant) serves about **1,900/s** and **custom token sign-in** about **1,600/s**, p50 4–5 ms. Both are bound by signing the new ID token: RSA-2048 is 50–58% of all CPU, about 1.6 ms per signature.
- **Password sign-in and sign-up** serve about **90–95/s**, p50 about 40 ms, bound by argon2id, as designed and as Phase 1's console login.
- **The Go library** (`sdk/go/adminauth`) verifies an ID token locally in **49 µs on one core, 15–17 µs per token across 8 goroutines** (about 60,000/s), with no request to the server once the keys are cached.
- No request failed: 0 errors in the 307,083 requests of the three published runs.
- Four findings, one of them a denial of service: each rate limiter tracks at most 100,000 client addresses and refuses every new address once full (finding 1).

## Hardware and software

As in Phase 1: MacBook Air (M1, 2020), 8 cores (4 performance + 4 efficiency), 8 GiB, macOS 15.5, go1.27.2 darwin/arm64 with GOMAXPROCS 8, Postgres 18.1 local on 127.0.0.1:5432 with default settings (`fsync` and `synchronous_commit` on, data on an external USB APFS volume), pgx pool of 8 connections, loopback HTTP/1.1 with keep-alive. See the [Phase 1 table](baseline-phase1.md#hardware-and-software) for the details.

## How to reproduce

```sh
# The published runs: three closed-loop runs of the Auth scenarios
go run ./tools/bench -json run.json -scenarios auth-signup,auth-signin-password,auth-refresh,auth-lookup,auth-signin-custom-token

# Open loop at a fixed arrival rate
go run ./tools/bench -rate 600 -c 64 -scenarios auth-refresh,auth-lookup,auth-signin-custom-token
go run ./tools/bench -rate 50 -login-c 16 -scenarios auth-signup,auth-signin-password

# Where the time goes: one profile per scenario
go run ./tools/bench -scenarios auth-refresh -cpuprofile cpu.pprof && go tool pprof -top -cum cpu.pprof

# The floors: argon2id and RS256 on this machine
go test ./tools/bench -run '^$' -bench 'Argon2|RS256' -cpu 1,4,8

# The Go library's local verification, against a live server it starts (from sdk/go/adminauth)
go test -run '^$' -bench Verify -benchtime 3s -cpu 1,4,8

# The limiter cap (finding 1): a new client address per request
go run ./tools/bench -scenarios auth-lookup -client-addrs 0 -duration 20s
```

The bench needs only the local Postgres named by `ARUMA_TEST_DATABASE_URL`. Each run creates a database `aruma_bench_<random>` and drops it on exit; the library benchmark starts `test/smoke/genconfig -serve`, which does the same. As in Phase 1, the published runs used a binary built from a clean `git archive` of the commit above, which every JSON file records.

## Method

**Setup.** Unchanged from Phase 1: a throwaway database, Aruma started in the bench's process on a loopback port and wired like `aruma serve` (production argon2id cost m=19 MiB, t=2, p=1, at most 4 hashes at once; JSON request log to `io.Discard`; the same `http.Server` timeouts; the default pool), 10 projects with a web, Android and iOS app each, created through the console API with a CLI token. For the Auth scenarios the bench then, still through the public APIs:

- reads the API key of the first project's web app, as a developer copies it from the config;
- creates a service account key in that project;
- signs up **256 email/password users** through `accounts:signUp`, four at a time (the argon2id gate), and keeps each one's ID token and refresh token;
- signs **64 custom tokens** the way the official `createCustomToken` does (RS256 with the service account key, the Identity Toolkit audience, a `uid` and a developer claim), each for its own uid, and signs in once with each, so the users exist.

All of this happens before the measured phase. ID tokens and custom tokens live an hour, longer than any run.

**Scenarios.** All use the first project and its API key (`?key=`), as the Firebase SDKs do.

| Scenario | Request | What it exercises | Pool acquires per request (measured) | Statements |
|---|---|---|---|---|
| `auth-signup` | `POST accounts:signUp` with a new email each time | argon2id hash; transaction: user, password, provider, `auth.user.created` event, refresh token; ID token signed | 5 | 2 key + config + email check, then a transaction of 8 |
| `auth-signin-password` | `POST accounts:signInWithPassword`, cycling through the 256 users | argon2id verify; transaction: refresh token, last login; ID token signed; audit row | 7 | 2 key + config + user + password, a transaction of 4, the audit insert |
| `auth-refresh` | `POST /securetoken.googleapis.com/v1/token`, `grant_type=refresh_token` as a form (the web SDK's encoding), cycling through the 256 refresh tokens | refresh token and user lookup, ID token signed, two updates (`last_used_at`, `last_refresh_at`) | 8 | 8 |
| `auth-lookup` | `POST accounts:lookup` with an ID token, cycling through 256 | ID token verified on the server: signing key lookup, RS256 check, user, providers and password rows | 6 | 6 |
| `auth-signin-custom-token` | `POST accounts:signInWithCustomToken`, cycling through the 64 custom tokens | service account key lookup, RS256 check; transaction: user, refresh token, last login; ID token signed | 5 | 2 key + 2 service account, then a transaction of 5 |

"Pool acquires" is the pgx acquire count over requests in the measured window, as in Phase 1. A transaction takes one acquire for all its statements, so unlike Phase 1's scenarios the acquire count is not the statement count; the last column counts statements from the code. The argon2id-bound scenarios run with 4 workers, the size of the server's hashing gate (`-login-c` changes it); the others with 8. Warm-up 2 s, measured window 10 s, percentiles from the same histogram; see [Phase 1's method](baseline-phase1.md#method).

### Rate limits and how they interact with the numbers

The Auth endpoints are limited in memory per client address (`internal/identity/api/api.go`):

| Limit | Applies to | Budget |
|---|---|---|
| per address | every client call: sign-up, sign-in, refresh, lookup, … | 300/min, burst 100 |
| sign-up per address | `accounts:signUp` without an ID token | 2/min, burst 100 |
| email per address | `accounts:sendOobCode` | 60/h, burst 20 |
| backoff per project and email | `accounts:signInWithPassword` | after 10 failures within 15 min, locked 30 s, doubling to 15 min; a success resets it; attempts in flight count against the threshold |

From one address the bench would measure 429-style refusals (`400 TOO_MANY_ATTEMPTS_TRY_LATER`) after 100 requests. As for Phase 1's console login, in-process mode trusts loopback as a proxy and every Auth request carries an `X-Forwarded-For` address. What changed is how addresses are chosen:

- **Phase 1 used a new address for every request.** That is no longer possible at Auth's rates. Each limiter tracks at most 100,000 addresses and refuses newcomers when full ([`ratelimit.go`](../../internal/platform/ratelimit/ratelimit.go), `maxKeys`). At 7,000 lookups/s a new address per request fills the table in about 14 s, and from then on every request fails (finding 1, run `limiter-cap-fresh-addrs.json`).
- **The bench now cycles through 65,536 addresses** (`-client-addrs`, default 65,536; 0 restores the old behaviour). That stays under the cap. At the highest rate measured, about 7,700/s, each address comes back every 8.6 s, once or twice per scenario. That is far inside 300/min with a burst of 100, and sign-up, at 100/s, reuses an address every 11 minutes. So the limits never trigger, and the numbers measure the work behind them, as in Phase 1. Phase 1's scenarios are equally unaffected: their limits are per endpoint, and each address is reused too rarely to reach them.
- **Per-email backoff only counts failures.** Every measured sign-in succeeds and resets its entry, so a single user would not be locked out. One user would still meet the in-flight cap, which lets at most 10 attempts on one email run at once. The bench cycles through 256 distinct users, so with 4 workers no two attempts on the same email are ever in flight together, as with real traffic.
- **In production one address gets 300 Auth calls per minute.** That is about 5/s sustained, after a burst of 100. One browser refreshes its token once an hour, so this matters only behind a proxy without `trusted_proxies`, where every user shares one address and so one budget, as finding 3 of Phase 1 described for the console.

**Noise.** The machine was shared with other agents running Playwright end-to-end suites (several headless Chromium renderers and Node workers), an iOS simulator and macOS indexing. A sampler summed the `%CPU` of every process except the bench and Postgres every 3 s. During the three published runs these co-tenants used **0.95–1.64 cores on average (peaks 2.2–5.2)**, the same order as in Phase 1, and the 1-minute load average stood between 5 and 12. The three runs ran back to back; each column below is the median, with the min–max range in parentheses.

## Results

### Closed loop, concurrency 8 (argon2id-bound: 4), 10 s per scenario, three runs

| Scenario | ok/s | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | Failed |
|---|---|---|---|---|---|---|---|
| `auth-signup` | 89 (72–96) | 42.5 (38.8–48.8) | 53.5 | 86 (85–155) | 139 | 139 | 0 of 2,572 |
| `auth-signin-password` | 95 (56–96) | 40.4 (38.8–53.5) | 53.5 | 71 (64–262) | 138 | 138 | 0 of 2,465 |
| `auth-refresh` | 1,898 (1,309–2,040) | 4.10 (3.83–4.33) | 5.51 | 7.6 (6.9–40.4) | 18.6 | 42.7 | 0 of 52,473 |
| `auth-lookup` | 6,451 (6,215–7,659) | 0.999 (0.983–1.13) | 1.62 | 3.8 (2.3–5.2) | 9.8 | 49.6 | 0 of 203,249 |
| `auth-signin-custom-token` | 1,616 (1,323–1,694) | 4.59 (4.59–4.85) | 6.75 | 15.3 (8.8–27.0) | 29.9 | 89.1 | 0 of 46,324 |

Files: `closed-loop-run1.json` to `closed-loop-run3.json`. Peak RSS of the bench process, client and server together: 200–247 MiB.

The low ends of the ranges come from co-tenant bursts, not from the server:

- In run 2 the co-tenants used 2.5–4.3 cores while the two argon2id scenarios ran (the sampler's 3-second samples line up with them). Those two scenarios fell to 72/s and 56/s, and recovered in run 3.
- In run 1 refresh was 1,309/s with p99 40 ms, during a co-tenant peak of 5.2 cores.
- Read this as Phase 1 did: about ±10% on throughput between quiet runs, and much wider in the tails.

The single-scenario profiling runs, which ran just after, landed inside these ranges: sign-up 103/s, sign-in 94/s, refresh 1,925/s, lookup 7,224/s, custom token 1,591/s.

A second set of three runs (`closed-loop-set-b-run*.json`) came about 20 minutes later. Its co-tenants averaged 1.4–1.7 cores, slightly more than during the published set. Its medians were 80 and 84/s for sign-up and sign-in, 1,733/s for refresh, 6,764/s for lookup and 1,142/s for custom token sign-in, with 0 failures in 274,419 requests. Those figures are within or below the published ranges; custom token sign-in was 30% lower. The published set is the first one, chosen before the second was run.

### Open loop at a fixed rate

Two runs at 600 requests/s per scenario, up to 64 in flight, and one at 50/s, up to 16 in flight, for the argon2id scenarios. They ran in a quieter spell: co-tenants averaged 0.6–0.8 cores, with peaks of 1.5–3.8. Every scheduled request was sent and answered (6,000 of 6,000; 500 of 500). The bench never fell more than 3.3 ms behind its schedule.

| Scenario | rate | p50 ms (run 1 / 2) | p90 ms | p99 ms | p99.9 ms | max ms |
|---|---|---|---|---|---|---|
| `auth-refresh` | 600/s | 2.16 / 2.16 | 3.60 / 2.26 | 39.9 / 4.16 | 66.1 / 7.54 | 70.5 / 12.5 |
| `auth-lookup` | 600/s | 1.25 / 1.28 | 1.39 / 1.39 | 1.70 / 1.61 | 2.65 / 2.29 | 5.4 / 3.0 |
| `auth-signin-custom-token` | 600/s | 2.49 / 2.75 | 3.28 / 3.90 | 4.06 / 5.44 | 6.16 / 8.19 | 8.9 / 11.4 |
| `auth-signup` | 50/s | 29.4 | 30.9 | 32.5 | 35.5 | 35.5 |
| `auth-signin-password` | 50/s | 29.1 | 31.7 | 36.7 | 47.7 | 47.7 |

- **Below capacity, a refresh or custom token sign-in costs about 2–3 ms, and a lookup 1.3 ms.** A password sign-in or sign-up costs 29 ms: one argon2id hash at about 25 ms, plus 4 ms for everything else.
- **Refresh's p99 of 40 ms in run 1** came with 2% of pool acquires waiting. It did not recur in run 2.
- **Earlier, on the loaded machine (load average 10–25, co-tenants averaging 2.4–4.2 cores), the same runs fell behind.**
  - Custom token sign-in in one run (p90 495 ms, 51% of acquires waiting), refresh in the next (p90 361 ms, 29% waiting).
  - The schedule lagged by up to 470 ms: the stalls were the machine's, not a server limit.
  - Files: `open-loop-600-loaded.json`, `open-loop-600-loaded-run2.json`.

Files: `open-loop-600.json`, `open-loop-600-run2.json`, `open-loop-argon2-50.json`.

### The Go library: local verification

`BenchmarkVerifyIDToken` in `sdk/go/adminauth` verifies real Aruma ID tokens. A live server is started by the package's `TestMain` on a fresh database, the library fetches the discovery document and the JWKS over loopback, and then it verifies from its cache. GOMAXPROCS goroutines verify at once; three runs of 3 s each (`adminauth-go-bench.txt`):

| | -cpu 1 | -cpu 4 | -cpu 8 |
|---|---|---|---|
| `VerifyIDToken`, µs per token | 48.6 (48.5–52.6) | 17.0 (16.5–17.3) | 15.7 (14.4–17.3) |
| tokens/s | ≈ 20,600 | ≈ 59,000 | ≈ 64,000 |
| `VerifyIDTokenAndCheckRevoked`, pooled transport, µs per token | 363 (359–368) | 169 (167–177) | 128 (123–129) |
| HTTP requests per verification | 0 / 1 | 0 / 1 | 0 / 1 |

- **Local verification makes no request to the server** once the keys are cached for the 300 s their `Cache-Control` allows. It costs 5.9 KB and 72 allocations per token. RSA verification is about 37 µs of the 49 µs (`BenchmarkRS256Verify`). The rest is mostly decoding the payload twice, once into a map for `Claims` and once into `auth.Token`, as firebase-admin-go does.
- **With the revocation check** each verification is one Admin API lookup, so the cost becomes a server round trip: about 7,800/s with 8 goroutines, close to the server-side `auth-lookup` above. Both paths do the same database work.
- **The library's default HTTP client fails under that load** (finding 2). With 8 goroutines it ran out of loopback ports within seconds (`connect: can't assign requested address`). The table's last row therefore uses a transport that keeps 64 idle connections per host.

## Where the time goes

One 10 s closed-loop run per scenario with `-cpuprofile`, which covers the bench process (load generator and server). A second sampler read the cumulative CPU time of the bench process and of the bench database's Postgres backends every second, so the database server's own CPU is counted too. Shares below are of that total (Go process plus Postgres). Profiles: `cpu-<scenario>.pprof`.

| Scenario | ok/s | CPU (cores) | CPU per request | argon2id | RSA sign | RSA verify | pgx (DB client) | Postgres | load generator | other |
|---|---|---|---|---|---|---|---|---|---|---|
| `auth-signup` | 103 | 3.93 | 38 ms | 37% + 49%* | 0.8% | – | 4.1% | 3.3% | 0.1% | 6% |
| `auth-signin-password` | 94 | 3.86 | 41 ms | 43% + 43%* | 1.6% | – | 4.2% | 2.6% | 0.1% | 6% |
| `auth-refresh` | 1,925 | 5.34 | 2.8 ms | – | **58%** | – | 18% | 14% | 1.9% | 8% |
| `auth-lookup` | 7,224 | 4.95 | 0.69 ms | – | – | 3.2% | **36%** | **28%** | 8.0% | 23% |
| `auth-signin-custom-token` | 1,591 | 5.39 | 3.4 ms | – | **50%** | 0.8% | 25% | 14% | 1.3% | 8% |

"pgx" is every sample whose stack holds a pgx frame: protocol encoding plus the socket system calls of the Postgres wire protocol. "Load generator" is the bench's HTTP client. "Other" is the server's HTTP stack, JSON, request log and the runtime.

\* **The argon2id scenarios need care.** The profile puts 37–43% in argon2 and another 43–49% in `runtime.pthread_cond_signal`, the runtime waking a thread. The process-level accounting says most of that second share is argon2 too:

- One hash at the production cost, with four running at once, takes 8.4–9.4 ms of wall time per hash over 4 cores (`BenchmarkArgon2VerifyParallel -cpu 4`). That is **34–38 ms of CPU per hash**.
- The scenarios used 38–41 ms of CPU per request in total, so argon2id accounts for about 90% of it.
- The pure argon2 benchmark, profiled alone, shows 88% in argon2 and no `pthread_cond_signal` at all.

The signal-based profiler on macOS evidently charges compute time to threads that make system calls when hashing runs beside network and database work. Phase 1's console login showed the same symptom at 14%. The profiles of the other three scenarios do not have this problem (`pthread_cond_signal` 1–2%).

**What bounds each path:**

- **Sign-up and sign-in: argon2id.**
  - One hash or verify costs 24–26 ms on one core. Four at once, the server's gate, take 8.4–9.4 ms each.
  - That gives a ceiling of about 105–120/s on this machine. The scenarios reach 90–95/s, about 80% of it, the same as Phase 1's login.
  - Everything else (RSA signature, nine statements, events) is under 10% of CPU.
  - On arm64, `golang.org/x/crypto/argon2` runs the generic Go code, so an amd64 server may hash faster per core.
- **Refresh and custom token sign-in: the RS256 signature of the new ID token.**
  - One RSA-2048 signature takes 1.1–1.3 ms on one core (`BenchmarkRS256Sign`). Under the scenarios' load it took about 1.6 ms of CPU per request, since half the cores are efficiency cores and they share with Postgres.
  - Signing alone gives 2,300–3,900 signatures/s with 8 goroutines on this machine (`BenchmarkRS256Sign -cpu 8`, 260–440 µs each, measured under the same co-tenant load). With the database work beside it, refresh reaches about 1,900/s.
  - RS256 is fixed by the token contract (ADR-004): Firebase's verifiers accept nothing else. Adding cores is the way to scale it.
  - The custom token's own RS256 check costs 0.8%. Verifying is about 30 times cheaper than signing.
- **Lookup (server-side verification): the database.**
  - Six queries per request take 64% of all CPU: 36% on the client side in pgx, 28% in Postgres.
  - The RSA check is 3%, and resolving the API key alone is 14% of the bench process (finding 3).
  - The scaling matches Phase 1's `mgmt-web-config`, also 6 queries, at 8,300/s.

## Findings

1. **A rate limiter that is full refuses every new client.** This is a denial of service.
   - **Mechanism.** `ratelimit.Limiter` and `ratelimit.Backoff` keep at most 100,000 keys ([`internal/platform/ratelimit/ratelimit.go`](../../internal/platform/ratelimit/ratelimit.go), `maxKeys`). When full and nothing can be swept, `Allow` refuses every key it does not know yet: "refuse newcomers rather than grow without bound". A key is swept only when its bucket has been idle long enough to refill completely, which is burst ÷ rate. That is 20 s for the Auth per-address limit, **50 minutes for the sign-up limit**, 20 minutes for the email limit, and 30 s for the console token limit.
   - **Measured.** `auth-lookup` with a new address per request (`-client-addrs 0`) served 99,063 requests. After that, every request answered `400 TOO_MANY_ATTEMPTS_TRY_LATER` (3,654 in the last seconds of the window; `limiter-cap-fresh-addrs.json`). Addresses already in the table keep working; only new ones are refused (from the code).
   - **Exposure.**
     - IPv6 clients are keyed by /64, and a single /48 allocation holds 65,536 of them, so 100,000 keys need about two /48s.
     - Holding the Auth per-address table full needs about 5,000 requests/s.
     - Holding the sign-up table full needs only 100,000 sign-up attempts per 50 minutes, about **33/s**. The key is recorded before the request is validated, so invalid sign-ups count too.
     - While a table is full, no new address can sign up, or make any client call at all.
     - The same applies behind a proxy listed in `trusted_proxies` that passes client-supplied `X-Forwarded-For` through.
   - **Options.**
     - Evict the oldest or least-used entries instead of refusing (an LRU, or a sampled eviction).
     - Or key coarser once full, for example IPv6 by /48.
     - Or cap per prefix as well as globally.
   - **Severity: medium.** It needs many source addresses, which IPv6 makes cheap. Not fixed here: this task does not change server code.
2. **The Go library's default HTTP client exhausts loopback ports under concurrent revocation checks.**
   - **Mechanism.** `adminauth.NewClient` without `WithHTTPClient` uses `&http.Client{Timeout: 30 * time.Second}` ([`sdk/go/adminauth/client.go`](../../sdk/go/adminauth/client.go)), and so `http.DefaultTransport`, which keeps only **2 idle connections per host**. Every `VerifyIDTokenAndCheckRevoked` (and every Admin call) is a request to Aruma. With more than 2 in flight, the extra connections are opened and closed each time, and each closed one sits in TIME_WAIT.
   - **Measured.** With 8 goroutines, `BenchmarkVerifyIDTokenAndCheckRevoked/default-transport-8` failed twice out of three with `dial tcp 127.0.0.1:…: connect: can't assign requested address`: the machine had run out of ephemeral ports. A transport with `MaxIdleConnsPerHost: 64` ran the same load cleanly, at 123–129 µs per verification. Below that concurrency the two did not differ measurably.
   - **Fix.** The library should default to its own transport with a larger idle pool, for example a clone of `http.DefaultTransport` with `MaxIdleConnsPerHost` near 100. Until then, servers that check revocation on every request should pass `WithHTTPClient`.
   - **Severity: medium for servers that check revocation on every request.** Low otherwise: local verification makes no requests.
3. **Small per-request costs on the Auth paths**, each worth a query or a commit:
   - **API key resolution takes two queries where one would do.** `ProjectForKey` ([`internal/tenancy/core/dataplane.go:25`](../../internal/tenancy/core/dataplane.go)) runs `LookupAPIKey`, which already joins `control.projects` for the state, then `GetProject` only to read the project id. Every Auth client call pays it: 2 of lookup's 6 queries, and 14% of the process CPU in `auth-lookup`. Selecting `p.project_id` in `LookupAPIKey` would save one round trip on every client call.
   - **Server-side verification reads the signing key from the database on every request.** `tokens.Manager.Verify` ([`internal/identity/tokens/manager.go`](../../internal/identity/tokens/manager.go)) runs `GetSigningKey`, unmarshals the JWK and rebuilds the RSA public key each time, although signing keys are already cached in `signers`. A small cache keyed by kid, dropped on rotation as `signers` is, would remove one of lookup's 6 queries. It would also help every call that takes an ID token (update, delete, link). Together with the item above, lookup would go from 6 queries to 4.
   - **Refresh commits twice after signing.** `TouchRefreshToken` and `TouchRefresh` ([`internal/identity/core/sessions.go`](../../internal/identity/core/sessions.go), `Refresh`) are two separate autocommit updates, so each refresh pays two WAL flushes. With `fsync` on, refresh is the scenario that keeps Postgres busiest per request after lookup (14% of CPU). One statement updating both rows, or one transaction, would halve that. The password sign-in likewise writes its audit row in a commit of its own after the session's transaction.
   - **The ID token is signed inside the sign-in transaction.** `startSession` mints the token, an RSA signature of about 1.1–1.6 ms, after `TouchLogin` has locked the user's row and while the transaction holds a pooled connection. This applies to sign-up, password sign-in and custom token sign-in. Refresh signs outside any transaction. When CPU is short, the connection and the row lock are held for as long as the signature takes, so CPU queueing can turn into pool queueing. The measurements do not single this out: on the loaded machine custom token sign-in and refresh each fell behind in one of two runs. Signing after the commit would release both sooner, and the token depends only on data the transaction has already read.
   - **Severity: low.** Each is a few percent to a sixth of a path's cost. None is a defect in behaviour. Not fixed here: this task does not change server code.
4. **The bench's own spreading would have hit finding 1.** The fix lives in `tools/bench` (`-client-addrs`). Phase 1's `oauth-token` at 7,200/s used about 86,000 fresh addresses in its 12 s, close to the cap; Auth's rates go beyond it. This was fixed in the tool, as allowed, by cycling through a bounded pool. It is recorded here because the Phase 1 numbers stay valid: none of its runs reached 100,000 addresses in one limiter, as their zero failures show.

No other performance problem showed up: zero failures in every published run, no pool waits in the closed loop at concurrency 8, and every path bound by the work it is designed to do (argon2id, RSA signing, or its queries).

## Caveats

- **A loaded, shared laptop.** Co-tenants used about 1–1.6 cores on average in the published runs and up to 5 in bursts. Several of the extra runs met a 1-minute load average above 15. Tails (p99 and above) and the argon2id scenarios are the most sensitive; treat differences under about 15% as noise.
- **Everything on one box.** As in Phase 1, the numbers belong to bench, server and Postgres together on 8 cores. The load generator's own share is small (0.1–8% of CPU).
- **Fresh, small Auth tables.** A few thousand users, refresh tokens and audit rows in one project, all in `shared_buffers`. The writes of a refresh-heavy production instance (one `last_used_at` update per refresh) will grow the tables and their bloat over months.
- **Loopback, no TLS.** Real clients add round trips and handshakes. Token refresh in particular happens once an hour per signed-in user, so 1,900/s corresponds to about 6.8 million active sessions per hour on this laptop.
- **Auth only.** The Phase 1 scenarios were not rerun for this baseline. The bench still runs them, and `go run ./tools/bench` runs both sets.
