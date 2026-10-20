# Track A: RTDB wire protocol and official SDKs (research notes)

Date: 2026-10-20. Research only; nothing was built or run.

## 0. Sources and licences
| Repo | Commit | Licence | Paths read |
|---|---|---|---|
| firebase-js-sdk | d7bb0e16f2b13ba15f74b690596eb70ef145d4f6 | Apache-2.0 | packages/database (v1.1.5), packages/database-compat (v2.1.7) |
| firebase-android-sdk | dcc8d0eb52ea95d7822bb8e0fb2f45c161804e90 | Apache-2.0 | firebase-database |
| firebase-ios-sdk | dc5821fd8c986596363fef8feec4b0e8c1f0ded3 | Apache-2.0 | FirebaseDatabase |

Clones are sparse + blobless under `/Volumes/Work/aurma-project/.cache/research/src/`. Not cloned (sparse): `scripts/`, `config/` of js-sdk (so the emulator test runner is only known from package.json).
Docs fetched (WebFetch): firebase.google.com/docs/database/usage/limits, /database/web/structure-data, /database/rest/retrieve-data, /database/rest/save-data. The page /docs/reference/rest/database came back as navigation only (not usable); Firecrawl had no credits. Presence/offline docs pages were not fetched (behaviour taken from code).
Path convention below: JS = `firebase-js-sdk/packages/database/src/`, AND = `firebase-android-sdk/firebase-database/src/main/java/com/google/firebase/database/`, IOS = `firebase-ios-sdk/FirebaseDatabase/Sources/`. Line numbers are from these commits.
No official wire-protocol spec exists; everything here is derived from client code. "VERIFIED" = read in code/docs. "INFERENCE" = my reading.

## 1. Connection lifecycle
### URL
| Client | URL | Evidence |
|---|---|---|
| JS | `wss://<internalHost>/.ws?` + `v=5` + `s=<transport session>` (only when upgrading from LP) + `ls=<last session id>` + `ac=<app check token>` + `p=<app id>` + `r=f` (only when page host is the Firebase console) + `ns=<ns>` | realtime/Constants.ts:18-41; realtime/WebSocketConnection.ts:116-148; core/RepoInfo.ts:118-149 |
| Android | `wss://<host>/.ws?ns=<ns>&v=5[&ls=<last session>]`; app id and app check go in headers `X-Firebase-GMPID`, `X-Firebase-AppCheck`, plus `User-Agent` | AND connection/HostInfo.java:39-56; connection/WebsocketConnection.java:182-195 |
| iOS | `wss://<internalHost>/.ws?v=5&ns=<ns>[&ls=..]`; headers for app check, user agent, google app id | IOS Core/FRepoInfo.m:173-189; Realtime/FWebSocketConnection.m:525-540 |
| Node JS | as JS plus headers `User-Agent: Firebase/5/<ver>/<platform>/<Node|AdminNode>`, `X-Firebase-GMPID`, `Authorization: Bearer <token>`, `X-Firebase-AppCheck` | WebSocketConnection.ts:166-186 |

- `ns` is added by JS only when `host != internalHost`, host is not `*.firebaseio.com`/`firebaseio-demo.com`, or the namespace differs from the first host label (RepoInfo.ts:103-109, parser.ts `includeNamespaceInQueryParams`). Android always sends it. iOS always sends it (but derives ns from the host; known issue already in the brief).
- The server must therefore accept: `ns` in query, absent `p`/`ac` (Android, iOS), and credentials in headers.
- `ws://` is used when the databaseURL is `http://` (secure=false).

### Handshake and control messages (server to client, text frames of JSON)
- Envelope: `{"t":"c","d":{"t":"<ctl>","d":<payload>}}` for control, `{"t":"d","d":{...}}` for data. Client wraps its requests as `{"t":"d","d":{"r":N,"a":..,"b":..}}` (Connection.ts:223-227; AND connection/Connection.java:113-121).
- Hello (VERIFIED): `{"t":"c","d":{"t":"h","d":{"ts":<server ms epoch>,"v":"5","h":"<host>","s":"<session id>"}}}`. Connection.ts:335-404; AND connection/Connection.java:205-215; IOS Realtime/FConnection.m:226-262.
  - `ts` -> `.info/serverTimeOffset = ts - now` (PersistentConnection.ts:930-933). Must be a number or iOS closes the connection (FConnection.m:244-255).
  - `v` only compared with "5"; mismatch only logs a warning (Connection.ts:398-400); iOS ignores it.
  - `h` replaces the host for the next connection; cached only if it starts with `s-` (RepoInfo.ts:62-84). iOS caches any valid host in user defaults and validates it against `^[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?\z` (no underscore, no IPv6) and ignores invalid ones (IOS Core/FRepoInfo.m:36-52,118-146). iOS tolerates a missing `h`; JS/Android would store undefined/null (INFERENCE: always send a valid `h`). JS emulator mode overrides `h` with the user's host (Connection.ts:348-351).
  - `s` = session id, sent back as `ls` on the next connect.
- `r` reset (payload = host): JS: if still connecting, close and restart on the new host; if already connected, close (Connection.ts:440-452). Android: caches host, closes with reason SERVER_RESET, which also resets backoff (Connection.java:223-235; PersistentConnectionImpl.java ~640-655).
- `s` shutdown (payload = reason string): client reports it, does NOT reconnect (interrupt "server_kill") (Connection.ts:361-364, PersistentConnection.ts:879-882; IOS FPersistentConnection.m:462-467). Android exception: reason exactly `"Invalid appcheck token"` triggers up to 3 reconnects before giving up (PersistentConnectionImpl.java, onKill).
- `e` error (payload string): JS only logs (Connection.ts:368-369). Android/iOS: unknown control types are ignored.
- `o` pong: reply to client ping `{"t":"c","d":{"t":"p","d":{}}}`. JS sends pings only until the connection is "healthy" (below). Android/iOS do not send pings (VERIFIED for Android Connection.java; iOS FConnection.m not exhaustively checked).
- `a`/`n` controls exist only for the long-poll to websocket upgrade (Connection.ts:240-304).
- Malformed control/missing `t`: Android and iOS close the connection (Connection.java:140-149,189-196).

### Keepalive, framing, timeouts
| Item | Value | Evidence |
|---|---|---|
| Max frame size, client to server | 16384 chars | WebSocketConnection.ts:44, AND WebsocketConnection.java:39, IOS Constants/FConstants.m:107 |
| Multi-frame message | sender first sends a text frame containing only the decimal frame count, then N frames of <=16384 chars; receiver treats a frame as a count if length <= 6 and numeric, else a complete 1-frame message | WebSocketConnection.ts:292-355; java:249-310 |
| Server-to-client frame size | no limit enforced by clients; they reassemble by count. Server should use the same prefix for large messages. Comment says the server is "supposed to send up to 9999 frames" | WebSocketConnection.ts:318-320 |
| Keepalive | client sends text frame `0` after 45 s without any send/receive; timer resets on every frame in either direction | ts:45,425-435; java:37,312-335; IOS FConstants.m:108 |
| Server must not send `0` | JS parses `Number("0")` as a frame count of 0 and then buffers forever (INFERENCE from ts:316-329); Android ignores it (java:285-289) | |
| Connect timeout | 30 s (Android, iOS); JS: 30 s "healthy" timeout | java:38; IOS FConstants.m:109; ws ts:274 |
| JS health rule | healthy after 2 data responses; if no healthy at 30 s: keep if >100 KB received (mark healthy) or >10 KB sent (keep, not healthy), else close; after 5 s unhealthy it sends `p` pings | Connection.ts:33-44,152-186,464-482 |
| Idle close (Android only) | after 60 s with no listens, gets, pending requests, onDisconnects or puts, the client drops the socket itself and resumes on next use | PersistentConnectionImpl.java:277,1435-1477 |
| `get` offline timeout | 3 s connect wait on Android/iOS before falling back to cache | Repo.java:64; IOS FConstants.m:110 |

Implication (INFERENCE): the server needs a read idle timeout longer than 45 s plus slack (e.g. 90-120 s) or must send its own WS pings; the 60 s Read/WriteTimeout in Aruma's HTTP server cannot apply to hijacked sockets. Browsers answer WS ping frames automatically; the clients themselves rely only on the `0` text frame.

### Reconnect / backoff
| Client | Min | Multiplier | Max | Jitter | Reset |
|---|---|---|---|---|---|
| JS | 1 s | 1.3 | 5 min (30 s if admin credential) | delay = random() * (delay - time since last attempt) | to 1 s if the last connection lasted > 30 s, on browser `online`, on tab visible when delay is at max, on `resume()`; no delay for first attempt | PersistentConnection.ts:45-49,742-820 |
| Android | 1 s | 1.3 | 30 s | 0.7 (delay in [0.3d, d]) | signalSuccess if lifetime > 30 s or SERVER_RESET or interrupt; first retry after success is immediate | util/RetryHelper.java:65-117; PersistentConnectionImpl.java:344-349 |
| iOS | 1 s | 1.3 | 30 s | same helper | same rules | IOS FConstants.m:112-115; FPersistentConnection.m:440-466 |

Stops reconnecting: server `s` shutdown (not Android app-check case), `goOffline()`/interrupt, JS `onOnline false`, Android idle (resumes on next use). Auth failures do not stop it but raise the delay (see section 2). A failed first connect (never reached open) makes JS drop the cached `s-` host (Connection.ts:497-515).

## 2. Request/response actions
Request: `{"r":<int>,"a":"<action>","b":{...}}`. Response: `{"t":"d","d":{"r":<same int>,"b":{"s":"ok"|"<error>","d":<data or message>}}}`. Responses are matched by `r` only (PersistentConnection.ts:642-658). A top-level `{"d":{"error":".."}}` makes JS throw and iOS log (PersistentConnection.ts:652; IOS FPersistentConnection.m:423).
Request numbers are per connection starting at 1 (JS) / 0 (Android counter). Paths: JS sends `"/a/b"`; Android and iOS (via FPath wireFormat) send `"a/b"` and `"/"` for root (AND ConnectionUtils.java:33-48). Server must normalise both (INFERENCE; iOS wireFormat not read).

| Action | Body | Response | SDK handling |
|---|---|---|---|
| `s` stats | `{c:{"sdk.js.9-1-0":1,...}}` sent once after first connect | `ok` | status only logged; Android sends nothing if empty. Server may just ack. |
| `q` listen | `{p, h:"<hash of client cache>"}` and for non-default queries `{q:<query obj>, t:<tag>}`; Android also `ch:{hs:[hashes],ps:[paths]}` when cached node > 1024 bytes (SyncTree.java:82,126; ps = n-1 split paths for n hashes); iOS also sends `ch` (FPersistentConnection.m:955-965) | `{s:"ok", d:{ "w":["no_index"]? }}` or error status | on error the listener is removed and the error code is surfaced (`permission_denied`, `too_big`, `unavailable`, else "Unknown Error") (util.ts:473-491). Android does `serverBody.containsKey` on `d` so `d` must be an object (INFERENCE from PersistentConnectionImpl.java listen callback): always send `d:{}`. JS tolerates null/missing. Warning `no_index` is logged only. |
| `n` unlisten | `{p}` or `{p, q, t}` for tagged | none expected | fire and forget |
| `p` put | `{p, d:<json, with .value/.priority export format>, [h]}` | `ok`, `permission_denied`, `datastale` (only when `h` was sent), `too_big`, other | success: ack the pending write; failure: write rolled back locally (events revert) and callback error (Repo.ts:564-589) |
| `m` merge (update) | `{p, d:{childPath:value,...}, [h]}` | same | same; child keys may be multi-segment paths |
| `g` get | `{p, q:<query obj, {} for default>}` | `{s:"ok", d:<data in export format>}` / `{s:<err>, d:<message>}` | `d` is the raw data (null for missing); error rejects the promise with the message string (PersistentConnection.ts:194-223; AND PersistentConnectionImpl.java get) |
| `o` / `om` / `oc` | `{p, d}` (d null for cancel) | `ok` or error | queued until connected; re-sent on reconnect; JS also keeps its local onDisconnect tree to run if the connection drops |
| `auth` / `gauth` | `{cred:<token>}` plus optional `authvar` (gauth/Admin override) / `noauth:true` | `ok`, `invalid_token`, `expired_token`, `permission_denied`... | JS chooses `auth` if the token is a legacy secret format (`isValidFormat`), else `gauth` (PersistentConnection.ts:380-408). Android parses a "gauth" JSON-string token else `auth`. iOS sends `auth` only. Non-ok: token dropped, forced refresh, socket closed, reconnect. After 3 `invalid_token` (Android) / `invalid_token`+`permission_denied` (JS) the delay jumps to max (JS: 30 s; Android: its max). |
| `unauth` | `{}` | ignored | sent when the user signs out while connected |
| `appcheck` | `{token}` | `ok` or error | failure never closes the socket (server closes if enforcement is on) |
| `unappcheck` | `{}` | ignored | JS has a typo `unappeck` (PersistentConnection.ts:371); Android correct; iOS not present |
| `u` | legacy untagged unlisten in iOS constants | n/a | not used |

Order after (re)connect: auth (Android waits for its reply before continuing; JS sends immediately), appcheck, all listens, outstanding puts in order, queued onDisconnects, gets (JS restoreState_ 1035-1069; AND restoreState 1149-1180).
iOS holds put completion callbacks until all outstanding listens are acked (`unackedListensCount`, FPersistentConnection.m:790-800,985-992); JS comment says puts "depend on having received the corresponding data update from the server before they complete" (PersistentConnection.ts:1040). So the server must answer listens in request order and push listen data before the listen's ok (INFERENCE: this is the only way a client's own write ack can be ordered after its listen data).
Transactions: `p` with `h`=hash of last-known value at the transaction path; `datastale` -> client re-runs with fresh data (max 25 retries then `maxretries`) (Repo.ts:118,1179-1197; AND Repo.java:836-838,1160). Sent hash-carrying puts are cancelled with `disconnected` if the socket drops (PersistentConnection.ts:935-952).
Client-only status codes (never sent by server): `disconnected`, `maxretries`, `overriddenbyset`, `write_canceled`, `network_error`. Server codes known to Android: `datastale`, `failure`, `permission_denied`, `expired_token`, `invalid_token`, `unavailable` (DatabaseError.java:104-122). iOS treats anything other than `ok` as failure (`failed` default).

## 3. Server pushes
`{"t":"d","d":{"a":"<action>","b":{...}}}` (no `r`).
| a | b | Client behaviour |
|---|---|---|
| `d` | `{p, d, [t]}` | replace data at `p` (d may be null = delete). iOS requires BOTH `p` and `d` keys present or the push is dropped (IOS FPersistentConnection.m:1091-1094): always include `d`, even for null. |
| `m` | `{p, d:{childPath:val}, [t]}` | merge; Android drops an empty merge |
| `rm` | `{p, d:[{s:<start path>|absent, e:<end path>|absent, m:<data>}], [t]}` | range merge (Android and iOS only; JS has no handler and logs "Unrecognized action"). Response form for large listens when client sent compound hash `ch`. Optional for a server because JS never sends `ch`... but Android/iOS DO send `ch` for >1 KB caches; the server can ignore `ch` and send full `d` (INFERENCE; the SDKs handle a plain overwrite anyway). |
| `c` | `{p, [q]}` | listen revoked: JS removes the listen with matching query id and raises `permission_denied`; Android/iOS remove ALL listens at `p` |
| `ac` | `{s:"<status>", d:"<reason>"}` | auth revoked: token cleared, forced refresh, socket closed, reconnect (PersistentConnection.ts:985-1004; AND onAuthRevoked) |
| `apc` | `{s,d}` | app check revoked; clears token, no close (Android and JS; iOS not present) |
| `sd` | `{msg}` | printed to console only |
Tag `t`: client assigns an increasing integer to each non-default query (path + query id) (SyncTree.ts:555-563). All data pushes for that query carry `t`; pushes without `t` apply to the untagged "complete" view at the path. Listen/unlisten for default queries (no limit/range, priority order) carry no `q`/`t`.
Full result vs diff: the client always accepts a full overwrite `d` for the listen path (tagged or not). Hash `h` allows the server to skip data when it matches the client's cache; ignoring `h` and always sending a full snapshot is protocol-legal (INFERENCE; no client requires a hash-match optimisation). After the first push the server sends only deltas (`d` at changed child path, `m` for multi-child changes). For limited/ranged queries the client re-applies the range and limit filters itself (core/view/filter/LimitedFilter.ts:96-175), so the initial window must be right and later deltas must include items entering the window (the client cannot fetch the replacement for a child that falls out; INFERENCE from LimitedFilter fullLimitUpdateChild_).
`w` warnings: `d.w:["no_index"]` in the listen response only produces a client log line.

## 4. Queries
Wire query object `q` (core/view/QueryParams.ts:35-47,386-420):
`sp` start value, `sn` start key, `sin` start inclusive (bool; false for startAfter), `ep` end value, `en` end key, `ein` end inclusive, `l` limit, `vf` `"l"` (limitToFirst) or `"r"` (limitToLast), `i` index: absent = priority, `".value"`, `".key"`, or the child path string for orderByChild (e.g. `"a/b"`). Android/iOS use the same keys (AND `query.queryParams.get("i")`). A query is sent only when non-default; for `get` the object is always sent (`{}` default).
`sn`/`en` absent means MIN_NAME/MAX_NAME. startAt with a value and no key starts at the first child equal to the value.
Ordering (VERIFIED, core/snap/LeafNode.ts:55,227-250; util.ts:254-280; PriorityIndex.ts):
- Value type order: null < false < true < numbers (numeric) < strings (UTF-16 code unit compare of JS strings) < objects. Within priority index: nodes ordered by priority (null priority first, then numbers, then strings), ties by key.
- Key compare: keys that look like 32-bit ints (`^-?(0*)\d{1,10}$`, within int32) sort first, numerically (ties by length), then other keys by plain string comparison.
- orderByChild: ordered by the child value using the type order above, ties by key; missing child sorts as null (first). orderByValue similar on the node itself.
- Filtering: done by the server for the initial window and deltas; the client also re-filters (ranged/limited filters), so extra data is tolerated, missing data is not. REST `shallow`, `equalTo` map to `startAt=endAt`.
- Index absent: server answers with `w:["no_index"]` and still returns correct data (client filters).

## 5. Writes
- Data format on the wire for writes: JSON; leaf with priority is `{".value":x,".priority":p}`; object priority is a `.priority` child key (`val(exportFormat)`, LeafNode.ts val; ChildrenNode.ts:225-232). Server must store priorities and return them in the same export format in pushes and `g`.
- Server values resolved by the SERVER at write time: `{".sv":"timestamp"}` and `{".sv":{"increment":n}}` (increment of non-number existing value results in n). Also valid inside `.priority` (ServerValues.ts:74-155). The client applies an estimate locally first (`Date.now() + serverTimeOffset`).
- Own-write ordering: the client applies writes locally immediately (optimistic events), keeps them in a write tree, and layers server pushes under them. The ack (`r` response) removes the pending write; the client then raises events only if the server value differs (Repo.ts:564-589, WriteTree). Server needs: push for the confirmed write sent BEFORE the put ack for the same client when it has an overlapping listener (INFERENCE; avoids flicker, matches iOS `ackPuts` design) — a put ack arriving before the data push is still safe because the local write tree masks it until acked, but the final value may flash old data (INFERENCE).
- Put with `h`: server compares `h` with the hash of its current value at `p`; mismatch -> `{s:"datastale"}`, no write. Hash function VERIFIED (SHA-1 then base64 of a canonical string):
  - leaf: `[priority:<ph>:]<typeof>:<value>`; booleans `boolean:true`; numbers via IEEE-754 double as 16 lowercase hex digits (`number:<hex>`) (util.ts:393-448); strings raw. `ph` = `number:<hex>` or `string:<text>`.
  - object: `[priority:<ph>:]` then for each child in priority-index order with non-empty hash: `:<key>:<childhash>`; empty object hash = `""`; whole string sha1/base64, empty string stays `""` (ChildrenNode.ts:235-255).
  - Needed identically on the server for `datastale` and optional hash short-circuit. JS also uses the hash of the client's `serverCache` for `q` (`hashFn`).
- Empty/`null` write deletes. Multi-path update `m` with keys containing `/` is atomic across keys.
- Write size: SDK limit 16 MB per write, REST 256 MB (limits page). A `too_big` status is recognised by clients.

## 6. Long polling
- JS only (realtime/BrowserPollConnection.ts): URL `https://host/.lp?` with params `start`, `id`, `pw`, `ser`, `cb`, `seg`, `ts`, `d`, `disconn`, `dframe`; data sent in 1870-byte URL segments; keepalive request every 25 s; 30 s connect timeout; JSONP/iframe based, so only browsers (BrowserPollConnection.isAvailable false in Node; needs `document`).
- When used: initial transport is LP only if (a) `WebSocket` is unavailable, (b) `forceLongPolling()`, or (c) localStorage flag `previous_websocket_failure` is set (set when a ws is opened, cleared only after 2 responses; in-memory fallback storage counts as failed) (TransportManager.ts:57-84; WebSocketConnection.ts:162,279-290). A databaseURL scheme `ws://`/`wss://` forces websocket only. After the handshake it tries to upgrade to websocket.
- Android and iOS: no long-polling code at all (grep of main sources for "longpoll|.lp" returns nothing). Only WebSocket.
- Inference for v0.2: a server that supports only `/.ws` is correct for Android, iOS, Node and all browsers that can open a websocket. Browsers behind proxies that block WebSocket on the first try will fail the first connect, set the failure flag, and the next attempt begins with `/.lp` (404) — they would then be stuck. Skipping LP is acceptable if the documented limitation is stated; `forceWebSockets()` avoids the LP path.

## 7. Platform differences
| Topic | Web (JS) | Android | iOS |
|---|---|---|---|
| Transport | WebSocket (+ LP in browsers) | WebSocket (tubesock) | WebSocket (SRWebSocket/URLSessionWebSocketTask on watchOS) |
| Disk persistence | none (memory cache only) | `setPersistenceEnabled(true)`, SQLite, default cache 10 MB (core/Context.java:37, core/persistence/*) | `persistenceEnabled`, LevelDB, `persistenceCacheSizeBytes` (Sources/Persistence/*) |
| keepSynced | Not available (JS has no keepSynced) | `Query.keepSynced(true)` = permanent listener, persisted | same |
| Effect on server | none | on reconnect client sends `q` with hash/compound hash of persisted data; tracked queries mean listens for old queries are re-sent with their cached hash | same |
| Compound hash `ch` / `rm` | not sent / not handled | sent if cached node > 1KB; handles `rm` | sent; handles `rm` |
| Auth action | `auth` or `gauth` | `auth` or `gauth` (JSON token) | `auth` only |
| App check | URL param `ac` + `appcheck` action | header + `appcheck` action | header + `appcheck` action |
| Reconnect max | 5 min | 30 s | 30 s |
| Idle disconnect | no | yes after 60 s with no use | not found (grep for idle in FPersistentConnection.m returned nothing) |
| Ping `p` | until healthy | no | no |
| Max frame | 16384 | 16384 | 16384 |
| Handshake h | stored; cached if `s-` | stored in cached host | validated and cached |
| REST fallback | `forceRestClient` or crawlers: GET `<path>.json?ns=..&format=export&auth=..&ac=..` + query params; 401 -> permission_denied, 404 -> null; read-only, no push (core/ReadonlyRestClient.ts:184-253) | no | no |

## 8. REST API
VERIFIED from docs (/docs/database/rest/retrieve-data, /save-data); the top-level reference page was not extractable.
- URL: `https://<host>/<path>.json`; methods GET, PUT (overwrite), POST (push, returns `{"name":"<key>"}`), PATCH (update children, multi-path), DELETE (200 with `null`). `X-HTTP-Method-Override` header supported.
- Query params: `auth` (secret or ID token; debug header `X-Firebase-Auth-Debug`), `print=pretty|silent` (silent => 204 on GET/PUT/POST/PATCH), `callback` (JSONP), `shallow=true` (objects become `true`, cannot be combined with filters), `timeout` (e.g. `3s`, default and max 15min, bad value => 400), `orderBy` (`"$key"`, `"$value"`, `"$priority"`, or child path; one key at a time; needs `.indexOn`), `limitToFirst`, `limitToLast`, `startAt`, `endAt`, `equalTo` (inclusive bounds), `format=export` (used by the JS SDK, includes priorities; not on the fetched pages = UNVERIFIED in docs), `access_token` and `writeSizeLimit` (not on the fetched pages; UNVERIFIED), `ns` (JS SDK adds it).
- Conditional: `X-Firebase-ETag: true` request header returns `ETag`; `if-match` on PUT/DELETE; mismatch gives 412 with current value + new ETag; no ETag for PATCH.
- Status codes: 200, 204, 400 (unparseable, too large, invalid key, path too long, unknown server value, undefined index, unsupported query param, timeout), 401 (bad/expired token or rules), 404 (no database), 412, 500, 503.
- Server values via REST: only `timestamp` documented (no `increment` on that page).
- Streaming (SSE): `Accept: text/event-stream`, follow 307 redirects, include `auth`. Events: `put` `{path, data}` (replace), `patch` `{path, data}` (replace per key), `keep-alive` (data null), `cancel` (null; rules no longer allow read), `auth_revoked` (string). Keep-alive interval is not stated (UNVERIFIED).
- Limits (https://firebase.google.com/docs/database/usage/limits): 200,000 simultaneous connections per database (Spark 100); ~100,000 simultaneous responses/sec; depth 32; key <= 768 bytes UTF-8; string <= 10 MB; single read response <= 256 MB; 75 million nodes in a path with listeners/queries; query run time <= 15 min; writes 1,000/s (soft); write request 256 MB REST / 16 MB SDK; 64 MB/min bytes written; event for a function 1 MB. Keys cannot contain `. $ # [ ] /` or ASCII control chars 0-31, 127; values cannot contain control characters either (structure-data page). Writes of invalid keys fail client-side in SDKs (validation in JS `core/util/validation.ts`, not read).

## 9. Official tests we could run against Aruma (inventory only; none run)
| Suite | Path | Server needed | How URL is configured | Size | Needs | Blockers |
|---|---|---|---|---|---|---|
| JS unit (no server) | packages/database/test/{compound_write,connection,sortedmap,sparsesnapshottree,path,pushid,parser,repoinfo,queryconstraint,transport,websocketconnection,node}.test.ts | no | none | ~120 `it()` | Node, mocha via `npm run test:node` (nyc/ts-node) | needs npm install of the monorepo (not allowed here) |
| JS modular integration | packages/database/test/exp/integration.test.ts | yes | env `RTDB_EMULATOR_PORT` (+ `RTDB_EMULATOR_NAMESPACE`) => `http://localhost:<port>?ns=<ns>`, else `config/project.json` databaseURL (test/helpers/util.ts:36-65) | 28 | Node or Karma | needs auth-less access (rules must allow all); the package's `test:emulator` script downloads Google's emulator jar via `scripts/emulator-testing/database-test-runner.ts` (not cloned; we can instead export the two env vars and run mocha directly) |
| JS compat integration | packages/database-compat/test/*.test.ts: query (166 `it`), transaction (50), database (35), order (21), order_by (15), promise (14), datasnapshot (13), info (6), servervalues (5), browser/crawler_support (12) | yes | same env vars (database-compat/test/helpers/util.ts:28-60) | ~337 | Node (`test:node`) or Karma (`test:browser`, Chrome) | same; some tests assume rules allowing writes, and `.info` behaviour; crawler tests are browser-only |
| Android instrumented | firebase-database/src/androidTest (DataTest, QueryTest, TransactionTest, OrderTest, OrderByTest, InfoTest, RealtimeTest, connection/*, core/*, integration/*, FirebaseDatabaseTest, etc.) | yes | `firebase_database_url` string resource from google-services.json (IntegrationTestValues.java:13-45); emulator test uses `db.useEmulator("10.0.2.2", 9000)` | ~500 `@Test` | Android emulator/device + Gradle | heavy toolchain; namespace derived by splitting host at first `.` |
| Android JVM unit | firebase-database/src/test | mostly no | n/a | ~403 `@Test` | Gradle/Robolectric | not server tests |
| iOS integration | FirebaseDatabase/Tests/Integration (FIRDatabaseQueryTests, FTransactionTest, FOrderByTests, FKeepSyncedTest, FPersist, FRealtime, FConnectionTest, FIRAuthTests, FDotInfo, ...) | yes | `FTestHelpers.databaseURL`: GoogleService-Info databaseURL or default `http://localhost:9000` (Tests/Helpers/FTestHelpers.m:66-73) | ~377 test methods (850 incl. unit) | macOS + Xcode/xcodebuild | requires full Xcode build of the monorepo; localhost:9000 default makes pointing it at Aruma easy |
| iOS unit | FirebaseDatabase/Tests/Unit | mostly no | | ~470 | Xcode | protocol-level helpers (FConnectionTest, FPersistentConnectionTests) are mocks |
The js `scripts/` and `config/` folders were outside the sparse checkout, so the emulator runner and `config/project.json` content were not read.

## Minimum server surface for v0.2 (INFERENCE from the facts above)
1. HTTP: `GET /.ws?v=5&ns=..` WebSocket upgrade, accept optional `ls`, `p`, `ac`, `s`; read credentials from headers too (`X-Firebase-AppCheck`, `X-Firebase-GMPID`, `Authorization`). No `/.lp`. Optional REST: `/<path>.json` GET/PUT/POST/PATCH/DELETE with `auth`, `print`, `shallow`, `orderBy` family, `format=export`, SSE stream (needed for the JS REST fallback and curl users; can follow after the WS path).
2. After upgrade immediately send the hello frame `{t:"c",d:{t:"h",d:{ts,v:"5",h:<valid host, no underscore>,s:<session id>}}}`.
3. Framing: parse count-prefixed multi-frame messages (client frames <=16384, count frame first); ignore text frame `0`; send count prefix when a reply exceeds ~16 KB; never send `0`. Reply `{t:"c",d:{t:"o",d:{}}}` to a `p` ping control. Read idle timeout >= ~2x45 s; allow bodies larger than the 1 MB HTTP cap on this path.
4. Actions: `s` (ack), `q` (with and without `q`/`t`), `n`, `p`, `m` (with optional `h` -> `datastale`), `g`, `o`, `om`, `oc`, `auth`/`gauth`/`unauth`, `appcheck`/`unappcheck` (accept as ok at first). Every request answered with `{r, b:{s,d}}`; listen ok always has `d` as an object.
5. Pushes: `d` and `m` with `p` and `d` keys always present and `t` for tagged queries; `c` and `ac` to cut listens/sessions (disabled user); `s` control then close for shutdown. Skip `rm`, `apc`, `sd` (clients tolerate their absence); ignore `ch`.
6. Semantics: paths with or without leading slash; priority and `.value/.priority` export format; key ordering (int-like keys first); value type order; queries on priority, `$key`, `$value`, child; `l`, `vf`, `sp/sn/sin`, `ep/en/ein`; server values `timestamp` and `increment`; hash function per section 5 for `datastale`; onDisconnect set/merge/cancel executed server-side when the socket closes; per-connection ordering: listens answered before later puts' acks; push before ok for a listen.
7. Limits to enforce with matching statuses: key <= 768 bytes and forbidden chars, depth 32, write <= 16 MB, `too_big` for oversized reads, 100k/200k connection counts are upper references only.
