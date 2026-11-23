# Configuration

Aruma reads an optional YAML file (`--config` or `ARUMA_CONFIG`). Every key can be overridden by an `ARUMA_*` environment variable, and the environment wins over the file. Unknown keys are rejected, so a typo fails loudly. [`aruma.example.yaml`](../../aruma.example.yaml) shows every key with its default.

Check a configuration without starting the server:

```bash
aruma config check
```

## Reference

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `ARUMA_DATABASE_URL` | yes | | PostgreSQL connection URL. `pool_max_conns` in it sizes the connection pool; without it the pool holds at most max(16, 4 × CPUs) connections, up to 64, opened on demand. The server logs the size at startup and warns when Postgres's `max_connections` is too low for it, see the [operator guide](../guides/operations.md#6-capacity-descriptors-and-the-database-pool) |
| `ARUMA_PUBLIC_URL` | yes | | The address your SDKs reach, for example `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | yes | | 32 random bytes in base64 (`openssl rand -base64 32`). Every other key is derived from it |
| `ARUMA_LISTEN` | | `:8080` | The single public entry point |
| `ARUMA_METRICS_LISTEN` | | `127.0.0.1:9464` | Private address for `/metrics`. The value `-` disables it |
| `ARUMA_SIGNUP_OPEN` | | `false` | Let anyone create an account from the console |
| `ARUMA_PROJECT_DOMAIN` | | host of `public_url` | Base for `authDomain`, container names and service-account emails |
| `ARUMA_TRUSTED_PROXIES` | | | CIDR ranges or addresses whose `X-Forwarded-For` is believed. On the Document Database they are also the only peers whose `X-Forwarded-Proto: https` lets an admin credential arrive without TLS, see [Document Database](#document-database) |
| `ARUMA_SESSION_IDLE_TIMEOUT` | | `30m` | Console session idle lifetime |
| `ARUMA_SESSION_ABSOLUTE_TIMEOUT` | | `8h` | Console session absolute lifetime |
| `ARUMA_MAIL_DEV_MAILBOX` | | `true` if `public_url` is on loopback, otherwise `false` | Development mailbox: Auth emails of projects without SMTP are kept in the console instead of being sent. An SMTP server is configured per project from the console |
| `ARUMA_MAIL_SMTP_ALLOW_PRIVATE` | | `true` if `public_url` is on loopback, otherwise `false` | Allow an SMTP server on a loopback, private or link-local address (for example Postfix on the same machine). Off by default so the console's SMTP settings cannot reach the server's internal network or the cloud metadata address. Turn it on if your mail relay is local |
| `ARUMA_RTDB_ENABLED` | | `false` | Turn on the Realtime Database |
| `ARUMA_RTDB_DOMAIN` | | | Parent domain for database hosts: `https://<project_id>-default-rtdb.<domain>`. Needs a public DNS record, a certificate and a proxy that preserves the host, see the [Realtime Database operator guide](../guides/operations.md). Without a value, databases are served with `?ns=` on `public_url`, for local development only |
| `ARUMA_STORAGE_ENABLED` | | `false` | Turn on Cloud Storage for Firebase, see [Storage](#storage) |
| `ARUMA_STORAGE_DATA_DIR` | with Storage | | Absolute path of the directory that holds the objects' bytes, on a local disk. See the warning under [Storage](#storage) |
| `ARUMA_STORAGE_GC_GRACE` | | `1m` | How long a file no object points to any more waits before it is deleted, so a read that started before the change still finds it |
| `ARUMA_DOCUMENTS_ENABLED` | | `false` | Turn on the Document Database (Firestore-compatible), see [Document Database](#document-database) |
| `ARUMA_DOCUMENTS_DEV_OPEN_RULES` | | `false` | Let clients on this machine read and write every Document Database without admin credentials, until security rules arrive. For local development only: accepted only when `public_url` and `listen` are loopback addresses |
| `ARUMA_DOCUMENTS_DEV_OWNER_TOKEN` | | `false` | Accept the emulators' `Bearer owner` on the Document Database from this machine, as an admin of every project. For local development only: accepted only when `public_url` and `listen` are loopback addresses, see [Document Database](#document-database) |
| `ARUMA_PG_BIN` | | | Directory of the PostgreSQL tools when they are not on `PATH`. Used by `make dev` |

## Realtime Database instance

The Realtime Database keeps its listeners in memory, so it runs on one Aruma process per Postgres database. At startup the process takes a session advisory lock (`rtdb.instance`) on a dedicated connection, waiting up to 30 seconds so that a stop-then-start deploy takes over; it waits for the Document Database's lock at the same time, and opens its listener after both. A second process on the same database serves everything else, logs `rtdb: another Aruma instance serves rtdb on this Postgres database`, answers the Realtime Database (and the Document Database, whose lock the first process holds too) with `UNAVAILABLE` and `/readyz` with 503 until the locks are free; it takes them within seconds of the first process stopping. Aruma 0.4 does not take this lock: when you upgrade from 0.4, stop the old process before you start the new one, or both serve the Realtime Database and listeners on one miss the writes made on the other. If the process loses its Postgres session (a restart or failover), it closes its Realtime Database connections and takes the lock again, trying at least every 5 seconds, so it serves again within about 5 seconds of Postgres' return; clients reconnect. In Kubernetes use `strategy: Recreate`, or a rolling update leaves the new pod unready until the old one stops. The connection must reach Postgres itself: a pooler in transaction mode does not keep the lock.

## Server timeouts

The public listener's HTTP timeouts, under `server` in the file. They bound ordinary requests. Long-lived connections lift or renew their own deadlines once established, so these values never cut a healthy one: Realtime Database WebSockets, Document Database gRPC and WebChannel streams (after their first message, see `stream_first_message`), and Storage uploads and downloads, which must keep moving at `min_bytes_per_second`. A Realtime Database long poll is held 20 seconds, inside the write timeout. The private metrics listener keeps fixed timeouts.

| Key | Variable | Default | Range | Meaning |
|---|---|---|---|---|
| `read_header_timeout` | `ARUMA_SERVER_READ_HEADER_TIMEOUT` | `10s` | 1s to 1h, at most `read_timeout` | Wait for a request's headers |
| `read_timeout` | `ARUMA_SERVER_READ_TIMEOUT` | `60s` | 10s to 1h | Reading a whole request, body included |
| `write_timeout` | `ARUMA_SERVER_WRITE_TIMEOUT` | `60s` | 30s to 1h | Writing a response, counted from the end of the request's headers |
| `idle_timeout` | `ARUMA_SERVER_IDLE_TIMEOUT` | `120s` | 5s to 1h | A keep-alive connection with no request is closed |

## Auth rate limits

Auth's limits per client address (resolved through `trusted_proxies`), the same for every project, under `auth.limits` in the file. Each is a token bucket: it holds up to its burst and refills at its rate. A call over the limit answers `TOO_MANY_ATTEMPTS_TRY_LATER`. Every value is a count from 1 to 1,000,000, checked at startup. The console shows the values in force on the Auth settings page. The growing delay after wrong passwords (10 failures per project and email in 15 minutes, then 30 seconds doubling up to 15 minutes) and the email sending limit are fixed.

| Key | Variable | Default | Meaning |
|---|---|---|---|
| `requests_per_minute` | `ARUMA_AUTH_REQUESTS_PER_MINUTE` | `300` | Refill rate of the bucket every client call takes from |
| `request_burst` | `ARUMA_AUTH_REQUEST_BURST` | `100` | Size of that bucket |
| `sign_ups_per_minute` | `ARUMA_AUTH_SIGN_UPS_PER_MINUTE` | `2` | Refill rate of the bucket account creation takes from (about Firebase's 100 accounts per hour per address) |
| `sign_up_burst` | `ARUMA_AUTH_SIGN_UP_BURST` | `100` | Size of that bucket |

Behind a reverse proxy, list it in `trusted_proxies`, or every client shares the proxy's address and one budget.

## Storage

Storage is off by default. Turn it on with `storage.enabled: true` and give it `storage.data_dir`; the server refuses to start without an absolute `data_dir`. The bucket of each project is `<project_id>.<project_domain>`, and each project starts with locked rules (`allow read, write: if false;`) until you deploy its own.

> **`data_dir` warning.** The directory holds the bytes, and the database only what points to them, so the two belong together:
> - Use a local file system. NFS and other shared file systems are not supported: one server owns the directory under an exclusive lock.
> - Mount the disk before Aruma starts (systemd `RequiresMountsFor=<data_dir>`). Aruma refuses to start on an empty directory when its database expects files there, so an unmounted disk stops the server instead of losing uploads.
> - The directory is bound to one database. Back up both with one command, `aruma backup --out aruma.dump --storage-out aruma-storage.tar.gz`, while the server runs, and restore both together (the [operations guide](../guides/operations.md#docker) shows how). A copy of the directory made some other way while the server runs can miss files the dump names: files of overwritten and deleted objects are deleted `gc_grace` later.
> - Replace a running server by stopping it first. In Kubernetes use `strategy: Recreate`; a rolling update waits on the lock.
> - Off loopback, serve Storage over https. Aruma logs a warning at startup when it runs on plain http.

The limits, under `storage.limits` in the file. Each must be positive unless its line says otherwise; they are checked only when Storage is on.

| Key | Variable | Type | Default | Meaning |
|---|---|---|---|---|
| `object_bytes` | `ARUMA_STORAGE_OBJECT_BYTES` | bytes | `5368709120` (5 GiB) | One object, one request body, and what decompression may produce. At most 5 TiB, and at most `project_bytes` |
| `project_bytes` | `ARUMA_STORAGE_PROJECT_BYTES` | bytes | `5368709120` (5 GiB) | A project's quota, unfinished uploads included. `0` is unlimited |
| `min_free_bytes` | `ARUMA_STORAGE_MIN_FREE_BYTES` | bytes | `2147483648` (2 GiB) | Disk space no write may take, kept for Postgres and the system |
| `empty_session_ttl` | `ARUMA_STORAGE_EMPTY_SESSION_TTL` | duration | `10m` | The life of an upload session that has received no byte |
| `upload_idle_ttl` | `ARUMA_STORAGE_UPLOAD_IDLE_TTL` | duration | `1h` | After this long without progress, a session's unused reservation is freed. Longer than `empty_session_ttl` |
| `upload_session_ttl` | `ARUMA_STORAGE_UPLOAD_SESSION_TTL` | duration | `168h` (7 days) | An upload session's life. Longer than `upload_idle_ttl` |
| `console_session_ttl` | `ARUMA_STORAGE_CONSOLE_SESSION_TTL` | duration | `1h` | The life of an upload session the dashboard issues. At most `upload_session_ttl` |
| `anonymous_staged_fraction` | `ARUMA_STORAGE_ANONYMOUS_STAGED_FRACTION` | fraction | `0.2` | The share of `project_bytes` that unfinished uploads without a signed-in user may hold. Between 0 and 1 |
| `metadata_bytes` | `ARUMA_STORAGE_METADATA_BYTES` | bytes | `8192` | An object's custom metadata, keys and values together (download tokens count too) |
| `json_body_bytes` | `ARUMA_STORAGE_JSON_BODY_BYTES` | bytes | `65536` | A metadata update, an upload start, or the JSON part of a multipart upload. At most 1 MiB |
| `header_field_bytes` | `ARUMA_STORAGE_HEADER_FIELD_BYTES` | bytes | `1024` | One of `contentType`, `cacheControl`, `contentDisposition`, `contentEncoding`, `contentLanguage` |
| `upload_sessions_per_project` | `ARUMA_STORAGE_UPLOAD_SESSIONS_PER_PROJECT` | count | `1000` | Live upload sessions per project |
| `upload_sessions_per_user` | `ARUMA_STORAGE_UPLOAD_SESSIONS_PER_USER` | count | `50` | Live upload sessions per user |
| `upload_sessions_per_ip` | `ARUMA_STORAGE_UPLOAD_SESSIONS_PER_IP` | count | `100` | Live upload sessions per client address (IPv6 per /64, through `trusted_proxies`) |
| `upload_starts_per_minute_per_ip` | `ARUMA_STORAGE_UPLOAD_STARTS_PER_MINUTE_PER_IP` | count | `60` | Upload starts per client address. Service accounts and the owner's CLI token count only against `upload_starts_per_second` |
| `upload_starts_per_second` | `ARUMA_STORAGE_UPLOAD_STARTS_PER_SECOND` | count | `1000` | Upload starts in the whole server |
| `upload_requests_per_ip` | `ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP` | count | `64` | Concurrent upload and write requests per client address, both APIs together |
| `min_bytes_per_second` | `ARUMA_STORAGE_MIN_BYTES_PER_SECOND` | bytes | `4096` | An upload slower than this over 30 seconds is cut |
| `list_max_results` | `ARUMA_STORAGE_LIST_MAX_RESULTS` | count | `1000` | The largest page of a listing. At most 1000 |

Durations use Go's syntax (`90s`, `10m`, `168h`).

## Document Database

The Document Database is off by default. Turn it on with `documents.enabled: true`. It speaks the Firestore protocols on the one public port: gRPC (by `content-type: application/grpc`, over HTTP/2, with or without TLS) and REST under `/v1/projects/{project}/databases/{database}/documents`. Each project has its `(default)` database.

- **One instance.** The Document Database runs on one Aruma process per Postgres database: the process takes a session advisory lock on a dedicated connection at startup, waiting up to 30 seconds. Another process on the same database serves everything else and answers the Document Database with `UNAVAILABLE` until the lock is free. The connection must reach Postgres itself: a pooler in transaction mode (pgbouncer `pool_mode=transaction`) does not keep the lock.
- **HTTP/2 behind a proxy.** gRPC clients need HTTP/2 end to end. A TLS proxy in front of Aruma must speak HTTP/2 without TLS (h2c) to it; in Caddy, `transport http { versions h2c 2 }`. A proxy that falls back to HTTP/1.1 breaks the listeners. iOS trusts only certificates from a public authority.
- **Security rules** decide what an app's users reach: every project starts with closed rules (`allow read, write: if false;`) until its own are published (`firebase deploy --only firestore:rules` or the dashboard). Admins are past the rules, as in production: a service account of the project, the project's owner (`aruma_pat_`), and the dev owner below. So is anyone on this machine when `dev_open_rules` is on.

The Admin SDKs send the Document Database a JWT signed with the service account's key on every call, and Go sends it over plain HTTP/2 (h2c) as readily as over TLS. Anyone who sees it can replay it for up to an hour, so an admin credential (that JWT, `aruma_sat_` or `aruma_pat_`) is accepted only:

- over TLS to Aruma itself;
- from a proxy listed in `trusted_proxies` that sends `X-Forwarded-Proto: https`. The proxy must set the header itself rather than pass on the client's (Caddy does by default). Without the header the request counts as plain text and is refused, so a proxy on the same machine, which makes every client look local, is listed in `trusted_proxies` like any other;
- or, on a development server whose `public_url` and `listen` are both loopback addresses, from this machine.

Anything else is refused with `UNAUTHENTICATED`, and the client's address is logged. Download the key file from Aruma's console: a Google key file has no key here. Node's `firebase-admin` in front of a real server needs TLS, since over gRPC with `ssl: false` it sends only `Bearer owner`; with a private CA, point `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` at it.

| Key | Variable | Default | Meaning |
|---|---|---|---|
| `enabled` | `ARUMA_DOCUMENTS_ENABLED` | `false` | Serve the Document Database |
| `dev_open_rules` | `ARUMA_DOCUMENTS_DEV_OPEN_RULES` | `false` | Let clients on this machine read and write every database past the security rules, without admin credentials. Any page open in a browser on this machine is such a client. The server refuses to start with it unless `public_url` and `listen` are loopback addresses, and it applies only to loopback peers |
| `dev_owner_token` | `ARUMA_DOCUMENTS_DEV_OWNER_TOKEN` | `false` | Accept `Bearer owner`, which the Admin SDKs send in emulator mode, as an admin of every project, from a loopback peer only. The server refuses to start with it unless `public_url` and `listen` are loopback addresses, and logs a warning when it is on. Off, `owner` is refused and logged, as on every other surface |
| `index_enforcement` | `ARUMA_DOCUMENTS_INDEX_ENFORCEMENT` | `enforce` | How queries meet production's composite-index rule: `enforce` refuses a query that needs an index with production's error and Firebase console link; `warn` serves it and logs the index it needs; `off` serves every query, as the emulator does |

The limits, under `documents.limits` in the file. Each must be positive.

| Key | Variable | Type | Default | Meaning |
|---|---|---|---|---|
| `message_bytes` | `ARUMA_DOCUMENTS_MESSAGE_BYTES` | bytes | `17825792` (17 MiB) | One gRPC message, and one REST body: a 16 MiB document from a mobile client and its overhead. At least 1 MiB |
| `stream_first_message` | `ARUMA_DOCUMENTS_STREAM_FIRST_MESSAGE` | duration | `10s` | How long a stream may take to deliver its first message and credential; only then are the server's read and write timeouts (`server.read_timeout`, `server.write_timeout`) lifted for it |
| `stream_idle` | `ARUMA_DOCUMENTS_STREAM_IDLE` | duration | `5m` | How long a listen stream may go without a target, or a write stream without a request, before it ends with `UNAVAILABLE`; the client reopens it when it needs it |
| `stream_write_stall` | `ARUMA_DOCUMENTS_STREAM_WRITE_STALL` | duration | `30s` | How long a streamed answer (a batch get, a query) may wait on a client that stopped reading before it is cut |
| `server_stream_max` | `ARUMA_DOCUMENTS_SERVER_STREAM_MAX` | duration | `5m` | The longest a streamed answer may last when the client set no deadline; it then ends with `DEADLINE_EXCEEDED` |
| `streams_per_ip` | `ARUMA_DOCUMENTS_STREAMS_PER_IP` | count | `500` | Streams open at once from one client address (through `trusted_proxies`) |
| `streams_per_project` | `ARUMA_DOCUMENTS_STREAMS_PER_PROJECT` | count | `10000` | Listen and write streams open at once on one project's databases; one more is refused with `RESOURCE_EXHAUSTED` |
| `stream_outbox_bytes` | `ARUMA_DOCUMENTS_STREAM_OUTBOX_BYTES` | bytes | `67108864` (64 MiB) | What may wait to be sent on one listen stream. A client that leaves it full for `stream_write_stall` stopped reading: its stream ends with `RESOURCE_EXHAUSTED`. At least `message_bytes` |
| `targets_per_stream` | `ARUMA_DOCUMENTS_TARGETS_PER_STREAM` | count | `1000` | Targets of one listen stream; one more is removed with a `RESOURCE_EXHAUSTED` cause, and the stream stays open |
| `snapshot_rows` | `ARUMA_DOCUMENTS_SNAPSHOT_ROWS` | count | `1000000` | Documents one listen target's read goes through (its first result, a resume, a limit window's fill); past it the target is removed with a `RESOURCE_EXHAUSTED` cause |
| `stream_lag_events` | `ARUMA_DOCUMENTS_STREAM_LAG_EVENTS` | count | `4096` | Commits' events a listen stream may fall behind; past it its targets are reset and read again |
| `commit_concurrency` | `ARUMA_DOCUMENTS_COMMIT_CONCURRENCY` | count | `4` | Commits of one database that run in Postgres at once; the others wait in the process |
| `txn_wait_timeout` | `ARUMA_DOCUMENTS_TXN_WAIT_TIMEOUT` | duration | `20s` | How long a transaction, or a write outside one, waits for another transaction's lock before it is aborted with `ABORTED` "Too much contention on these documents. Please try again.", as production does |
| `txn_max_lifetime` | `ARUMA_DOCUMENTS_TXN_MAX_LIFETIME` | duration | `270s` | A transaction ends this long after it began, and its locks are freed |
| `max_open_txns` | `ARUMA_DOCUMENTS_MAX_OPEN_TXNS` | count | `1000` | Open locking transactions (those of service accounts and owners) per database; past it, beginning one is `ABORTED` |
| `max_open_unlocked_txns` | `ARUMA_DOCUMENTS_MAX_OPEN_UNLOCKED_TXNS` | count | `10000` | Open read-only and optimistic transactions (those end users open) per database; past it, beginning one is `ABORTED` |
| `max_open_txns_per_user` | `ARUMA_DOCUMENTS_MAX_OPEN_TXNS_PER_USER` | count | `100` | Open read-only and optimistic transactions of one signed-in end user per database, so that one user cannot take the others' share of `max_open_unlocked_txns`; past it, beginning one is `ABORTED`. Admins have no such bound, and callers without a credential share `max_open_unlocked_txns` alone |
| `max_ended_txns` | `ARUMA_DOCUMENTS_MAX_ENDED_TXNS` | count | `100000` | Records of ended transactions kept so that their ids answer as production's do until 300 s after they began (the wound text, a retry's age). Past it the oldest go early: such an id answers the expired text, and a retry of it takes a new age |
| `max_locks_per_txn` | `ARUMA_DOCUMENTS_MAX_LOCKS_PER_TXN` | count | `10000` | Documents and listings one transaction may lock, or read when optimistic (with the documents its listings returned); past it, the transaction is `ABORTED` |
| `composite_indexes` | `ARUMA_DOCUMENTS_COMPOSITE_INDEXES` | count | `1000` | Composite indexes per database (production's number with billing); past it, creating one is `RESOURCE_EXHAUSTED` |
| `field_overrides` | `ARUMA_DOCUMENTS_FIELD_OVERRIDES` | count | `1000` | Field overrides (single-field index configurations and TTL policies) per database, the database default aside; past it, adding one is `RESOURCE_EXHAUSTED` |
| `ttl_sweep_interval` | `ARUMA_DOCUMENTS_TTL_SWEEP_INTERVAL` | duration | `1h` | How often the TTL sweeper deletes the documents whose TTL field (a Timestamp) has passed, each through a normal commit, so listeners and triggers see the delete. A document is deleted up to one interval after its time, later when a database has more due than a pass takes; production promises "typically within 24 hours". Until then it is read and queried as any other |
| `ttl_sweep_batch` | `ARUMA_DOCUMENTS_TTL_SWEEP_BATCH` | count | `500` | Due documents the sweeper reads of one database at once. A pass takes at most 100 batches of each database (50,000 documents by default) and leaves the rest to the next pass |

Some bounds are fixed, not configured, and far above anything production was seen to accept (`parity-exceptions.json`, `request-work-bounds`): a Commit or BatchWrite holds at most 10,000 writes and 50,000 field transforms, and one commit's writes rebuild at most 2^22 values of documents an earlier write of the commit already wrote (each write rebuilds the top-level fields it changes). Past them the request is `INVALID_ARGUMENT`. An optimistic transaction's commit checks again what it read for at most `txn_wait_timeout`, then is `ABORTED` with the contention text.

## Security rules limits

The Firestore and Storage rules engine, under `rules.limits` in the file, each with an `ARUMA_RULES_<KEY>` variable (for example `ARUMA_RULES_REGEX_BYTES`). All are checked at startup.

**Firebase's limits** are part of compatibility: a value may lower one, and a value above Firebase's is refused, since it would allow what Firebase denies.

| Key | Default | Meaning |
|---|---|---|
| `call_depth` | `20` | The longest chain of function calls |
| `function_lets` | `10` | `let` bindings in one function |
| `function_args` | `7` | Parameters of one function |
| `expression_depth` | `98` | The deepest nesting of an expression |
| `match_depth` | `10` | Nested `match` blocks |
| `match_segments` | `100` | Path segments of nested `match` blocks together |
| `match_variables` | `20` | Path variables of nested `match` blocks together |
| `source_bytes` | `262144` | The size of a rules file |
| `expressions` | `1000` | Expressions evaluated for one request |
| `lookups_per_operation` | `10` | Documents one Firestore operation reads (`get`, `exists`) |
| `lookups_per_request` | `20` | Documents a Firestore batch or transaction reads |
| `storage_lookups` | `2` | Firestore documents one Storage request reads |
| `rulesets_per_project` | `2500` | The rulesets one project keeps |

**Aruma's limits** protect the server from hostile rules and may be set freely, within the checks named.

| Key | Default | Meaning |
|---|---|---|
| `member_chain` | `2000` | A chain of `.field` and `[index]` |
| `work` | `67108864` | Work units of one evaluation (patterns, collections) |
| `value_bytes` | `2097152` | One string or bytes value. At most `alloc_bytes` |
| `value_elements` | `1048576` | One list, map or set |
| `alloc_bytes` | `16777216` | The values one evaluation builds |
| `inflight_bytes` | `536870912` | The values every evaluation in the process builds together. At least `alloc_bytes`; past it, requests get `UNAVAILABLE`, which the SDKs retry |
| `regex_instructions` | `10000` | The compiled program of one pattern |
| `request_segments` | `1024` | The segments of a request path |
| `regex_cache_entries` | `256` | Patterns built during evaluation that a ruleset keeps |
| `regex_cache_bytes` | `4194304` | Their estimated programs |
| `regex_bytes` | `4194304` | The estimated programs of one ruleset's literal patterns together. A file past it fails to compile ("Regular expressions are too complex to compile safely"), where Firebase may accept it |
| `compiled_rulesets_bytes` | `134217728` | Compiled rulesets kept in memory, by estimated weight. One ruleset may weigh a quarter of it, or it fails to compile ("The rules are too large to keep compiled"). It must be at least four times `source_bytes` and `regex_cache_bytes` and five times `regex_bytes` together, so that any file within those limits compiles |

## Retention

Housekeeping runs at startup and then every hour. It deletes rows that nothing reads any more, in batches of 1,000 rows and at most 50 batches per table per pass, so a large backlog (after an upgrade, say) is worked off over the following hours without long locks. Each pass logs one `housekeeping` line with the rows it deleted per table, and the metric `aruma_housekeeping_purged_rows_total{table}` counts them.

Three tables are kept for a configurable number of days, under `retention` in the file. `0` keeps a table forever; a negative value is refused at startup.

| Key | Variable | Default | Table |
|---|---|---|---|
| `auth_audit_days` | `ARUMA_RETENTION_AUTH_AUDIT_DAYS` | `90` | `auth.audit`: a row per sign-in attempt (successful or denied), per email action code and per session cookie issued. Nothing in the server reads it; it is there for your own queries |
| `control_audit_days` | `ARUMA_RETENTION_CONTROL_AUDIT_DAYS` | `90` | `control.audit_log`: administrative actions from the console and the Management API, shown on the audit log pages. At least `7`: the database itself refuses to delete a row younger than seven days, or to change any row |
| `events_days` | `ARUMA_RETENTION_EVENTS_DAYS` | `30` | `events.changes`: the shared change log, a row per account created. Nothing in the server reads it yet |

Credentials and temporary rows go a fixed time after they stop working, with no setting:

| Rows | Deleted |
|---|---|
| Console sessions (`control.sessions`) | 1 day after their absolute expiry (`session.absolute_timeout`); revoked or idle sessions stop working at once |
| CLI tokens (`control.access_tokens`) | 1 day after revocation, 30 days after expiry (the console lists expired tokens until then) |
| Service account access tokens | 1 day after expiry |
| App users' refresh tokens | 30 days after the user is deleted or the session revoked, so that a client that comes back hears `USER_NOT_FOUND` or `TOKEN_EXPIRED` and signs out. A live refresh token is never deleted: refresh tokens do not expire, as in Firebase |
| Email action codes, Google and Apple sign-in sessions | 1 day after use or expiry |
| Auth outbox messages | 30 days after delivery or the last failed attempt |
| Storage upload sessions | at expiry (`storage.limits.*_ttl`) |
| Document Database versions, change log, operations and usage | by the Document Database's own job, every minute: versions past the database's `versionRetentionPeriod`, change log rows after an hour, finished operations after 30 days, usage hours after 31 days |
| A deleted project's data, Auth included | when its 30 days of deletion end |

## Minimal production example

```yaml
database_url: postgres://aruma:secret@127.0.0.1:5432/aruma?sslmode=require
public_url: https://aruma.example.com
master_key: "<output of: openssl rand -base64 32>"
trusted_proxies: ["10.0.0.0/8"]
```

Keep `master_key` safe, back it up, and keep it out of version control. Every other key is derived from it.

## Operator commands

```bash
aruma migrate up|status                       # the server never runs DDL and refuses to start on an old schema
aruma serve
aruma accounts create --email you@example.com # password from the terminal without echo, or from stdin
aruma accounts disable|enable|reset-password --email you@example.com
aruma backup --out aruma.dump [--storage-out aruma-storage.tar.gz]
                                              # pg_dump, custom format; with Storage, the files the dump names
aruma restore --in aruma.dump --yes           # with the server stopped (it refuses otherwise), in a single transaction
aruma config check
```

`migrate`, `backup` and `restore` need only `database_url` (and `backup --storage-out` the Storage `data_dir`), from the file given with `--config` or from the environment, so they run before the rest of the configuration is complete. `pool_max_conns` and pgx's other pool settings in the URL are left to the server.
