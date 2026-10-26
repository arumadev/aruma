# Configuration

Aruma reads an optional YAML file (`--config` or `ARUMA_CONFIG`). Every key can be overridden by an `ARUMA_*` environment variable, and the environment wins over the file. Unknown keys are rejected, so a typo fails loudly. [`aruma.example.yaml`](../aruma.example.yaml) shows every key with its default.

Check a configuration without starting the server:

```bash
aruma config check
```

## Reference

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `ARUMA_DATABASE_URL` | yes | | PostgreSQL connection URL |
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
| `ARUMA_RTDB_DOMAIN` | | | Parent domain for database hosts: `https://<project_id>-default-rtdb.<domain>`. Needs a public DNS record, a certificate and a proxy that preserves the host, see the [Realtime Database operator guide](guides/rtdb-operator.md). Without a value, databases are served with `?ns=` on `public_url`, for local development only |
| `ARUMA_STORAGE_ENABLED` | | `false` | Turn on Cloud Storage for Firebase, see [Storage](#storage) |
| `ARUMA_STORAGE_DATA_DIR` | with Storage | | Absolute path of the directory that holds the objects' bytes, on a local disk. See the warning under [Storage](#storage) |
| `ARUMA_STORAGE_GC_GRACE` | | `1m` | How long a file no object points to any more waits before it is deleted, so a read that started before the change still finds it |
| `ARUMA_DOCUMENTS_DEV_OWNER_TOKEN` | | `false` | Accept the emulators' `Bearer owner` on the Document Database from this machine, as an admin of every project. For local development only: accepted only when `public_url` and `listen` are loopback addresses, see [Document Database](#document-database) |
| `ARUMA_PG_BIN` | | | Directory of the PostgreSQL tools when they are not on `PATH`. Used by `make dev` |

## Storage

Storage is off by default. Turn it on with `storage.enabled: true` and give it `storage.data_dir`; the server refuses to start without an absolute `data_dir`. The bucket of each project is `<project_id>.<project_domain>`, and each project starts with locked rules (`allow read, write: if false;`) until you deploy its own.

> **`data_dir` warning.** The directory holds the bytes, and the database only what points to them, so the two belong together:
> - Use a local file system. NFS and other shared file systems are not supported: one server owns the directory under an exclusive lock.
> - Mount the disk before Aruma starts (systemd `RequiresMountsFor=<data_dir>`). Aruma refuses to start on an empty directory when its database expects files there, so an unmounted disk stops the server instead of losing uploads.
> - The directory is bound to one database. `aruma backup` copies only the database: back up the directory with it, and restore both together.
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

The Admin SDKs send the Document Database a JWT signed with the service account's key on every call, and Go sends it over plain HTTP/2 (h2c) as readily as over TLS. Anyone who sees it can replay it for up to an hour, so an admin credential (that JWT, `aruma_sat_` or `aruma_pat_`) is accepted only:

- over TLS to Aruma itself;
- from a proxy listed in `trusted_proxies` that sends `X-Forwarded-Proto: https`. The proxy must set the header itself rather than pass on the client's (Caddy does by default). Without the header the request counts as plain text and is refused, so a proxy on the same machine, which makes every client look local, is listed in `trusted_proxies` like any other;
- or, on a development server whose `public_url` and `listen` are both loopback addresses, from this machine.

Anything else is refused with `UNAUTHENTICATED`, and the client's address is logged. Download the key file from Aruma's console: a Google key file has no key here. Node's `firebase-admin` in front of a real server needs TLS, since over gRPC with `ssl: false` it sends only `Bearer owner`; with a private CA, point `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` at it.

| Key | Variable | Default | Meaning |
|---|---|---|---|
| `dev_owner_token` | `ARUMA_DOCUMENTS_DEV_OWNER_TOKEN` | `false` | Accept `Bearer owner`, which the Admin SDKs send in emulator mode, as an admin of every project, from a loopback peer only. The server refuses to start with it unless `public_url` and `listen` are loopback addresses, and logs a warning when it is on. Off, `owner` is refused and logged, as on every other surface |

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
aruma backup --out aruma.dump                 # pg_dump, custom format
aruma restore --in aruma.dump --yes           # with the server stopped, in a single transaction
aruma config check
```
