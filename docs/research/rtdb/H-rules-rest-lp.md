# Track H: rules scope (Q4), REST (Q5), long polling (Q6), limits (Q8)

Date: 2026-10-20. Research only. VERIFIED = read in code/doc; INFERENCE = my reading; UNVERIFIED = not found.
Docs fetched with curl and text-extracted (copies in the session scratchpad `docs/`): firebase.google.com/docs/{reference/security/database, reference/security/database/regex, database/security/*, database/usage/limits, reference/rest/database, database/rest/*}.

## 0. Sources
| Repo | Commit | Licence |
|---|---|---|
| firebase-js-sdk (packages/database) | d7bb0e1 | Apache-2.0 |
| firebase-android-sdk | dcc8d0e | Apache-2.0 |
| firebase-ios-sdk | dc5821f | Apache-2.0 |
| firebase-admin-go | 66a72c3 | Apache-2.0 |
| firebase-admin-python | de45607 | Apache-2.0 |
| firebase-admin-java (9.11.0) | 2225373 | Apache-2.0 |
| firebase-admin-dotnet | beff1f9 | Apache-2.0 |
| firebase-admin-node | 08a3963 | Apache-2.0 |
| firebase-tools | 4e6eb92 | MIT |
| targaryen 3.1.0 | e4151e7 | ISC |
| bolt 0.8.4 | a0a3003 | Apache-2.0 |
| lark | c909918 | AGPL-3.0 (ideas only, nothing copied) |
| flakbase | 47ead96 | Apache-2.0 |

---
## Q4. Rules scope for v0.2

### 4.1 Complete language (official reference, https://firebase.google.com/docs/reference/security/database)
Structure: JSON doc `{"rules": {...}}`; nodes with `.read`, `.write`, `.validate`, `.indexOn`, `$wildcard` children. Access denied by default when no `.read/.write` at or above a path (core-syntax page). Admin SDK `setRules` accepts a string with comments (firebase-admin-node src/database/database.ts:262).

| Group | Items (all VERIFIED in the reference) |
|---|---|
| Rule types | `.read`, `.write`, `.validate`, `.indexOn` (string or array; key and priority indexed implicitly) |
| Variables | `auth` (null if unauthenticated; `provider`, `uid`, `token`), `auth.token.*` (email, email_verified, phone_number, name, sub, firebase.identities, firebase.sign_in_provider, firebase.tenant, plus iss/aud/auth_time/iat/exp and custom claims), `$location` capture (always string), `now` (ms since epoch, server clock), `root`, `data`, `newData` |
| `query.*` (rules-conditions page) | `query.orderByKey/orderByPriority/orderByValue` (bool), `query.orderByChild` (string path or null), `query.startAt/endAt/equalTo` (string/number/bool/null), `query.limitToFirst/limitToLast` (number or null) |
| RuleDataSnapshot methods | `val()`, `child(path)`, `parent()` (fails at root, expression counts as failure), `hasChild(path)`, `hasChildren([keys])`, `exists()`, `getPriority()`, `isNumber()`, `isString()`, `isBoolean()` |
| String | `.length`, `contains`, `beginsWith`, `endsWith`, `replace(sub, repl)` (replaces ALL, unlike JS), `toLowerCase`, `toUpperCase`, `matches(/re/)` |
| Operators | `+` (add and concat), unary/binary `-`, `*`, `/`, `%`, `===`, `!==`, `==` (treated as `===`), `!=` (treated as `!==`), `&&`, `||`, `!`, `>`, `<`, `>=`, `<=`, `?:` |
| Semantics | `.read` and `.write` cascade downward (a grant at a parent cannot be revoked by a child). `.validate` does NOT cascade: every `.validate` along the written subtree must pass; only evaluated for non-null new values (deletes skip it). `newData` is the merge of existing and written data. Wildcard keys never equal numbers (compare via `+ ''`). `data`/`newData`/`root` snapshots: `val()` of a node with children returns a sentinel, not an object. |
| Regex flavour (reference/security/database/regex) | NOT JS/PCRE. Supported: `\s \w \d \S \W \D`, `\` escape, `^` only as first char, `$` only as last char, `* + ? . ( ) a|b [..] [a-z] [^..]`, trailing flag `i` only. Not supported: `{n,m}` is claimed to "work as normal" in the intro line but absent from the symbol table (INCONSISTENT in doc), lookaround, backrefs, non-greedy, `g/m` flags. No capture, so greedy/lazy is moot. Targaryen fixtures include `/bar/ig` and `/\\{foo}/` so check what the real server does with them (UNVERIFIED). Cheapest implementation: translate to Go RE2 with the `^`/`$` position restriction enforced at rule-load time. |
| Rules file size limit | UNVERIFIED for RTDB. No RTDB page states one. Firestore rules have 256 KB source / 250 KB compiled (rules-language page); do not assume it applies. In the 2255 sampled files: median 1.2 KB, p90 6.9 KB, max 150 KB. |
| Deploy channel | `PUT /.settings/rules.json[?dryRun=true]` (firebase-tools src/rtdb.ts:47; 400 body `{error}` = syntax error), `GET /.settings/rules.json` and `?format=strict` (admin-node database.ts:220-257). firebase-tools also calls a separate metadata API `/namespaces/<db>/rulesets` (src/database/metadata.ts:76,91), not on the data host. |

Not in the reference but seen in real files (would be errors in real RTDB, so ignore): `isObject()`, `isArray()`, `numChildren()`, `auth.email`, `auth.id`, `.length()` as a call.

### 4.2 What real apps use (public GitHub sample)
Method: `gh search code --filename database.rules.json "size:<range>"` over 10 size ranges, 300 hits each (hit cap), 2995 unique repo/path, 2956 fetched via raw.githubusercontent.com, comments stripped, 191 unparsable or without a rules object, 510 exact duplicate contents removed. Analysed: **2255 unique files from 2227 repos**, 43,851 rule expressions. Scripts and raw data: `/Volumes/Work/aurma-project/.cache/research/gh/` (fetch.py, analyze.py, analysis.txt).
Caveats (INFERENCE): GitHub code search ranks arbitrarily and caps at 1000 per query, so this is a broad but not random sample; tutorial and sample repos are over-represented; hobby and abandoned projects dominate; closed-source production apps may use more features. Regexes are heuristics (ternary and arithmetic counts are upper bounds).

| Feature | files | % of 2255 |
|---|---|---|
| `$variable` capture | 2001 | 88.7 |
| `auth != null` | 1699 | 75.3 |
| `auth.uid` (any) | 1589 | 70.5 |
| `auth.uid === $var` | 1413 | 62.7 |
| `newData` (any) | 1332 | 59.1 |
| `.val()` | 1420 | 63.0 |
| `.validate` present | 1166 | 51.7 |
| `hasChild`/`hasChildren` | 997 | 44.2 |
| `.validate` with isString/isNumber/isBoolean | 909 | 40.3 |
| `.indexOn` | 791 | 35.1 |
| `root.child(...)` lookups | 867 | 38.4 |
| `data.exists()` | 880 | 39.0 |
| `data.child/parent` | 764 | 33.9 |
| `.validate` with `.length` | 607 | 26.9 |
| `now` | 335 | 14.9 |
| `auth.token.*` (any) | 329 | 14.6 |
| regex `matches` | 323 | 14.3 |
| `parent()` | 231 | 10.2 |
| open `.read/.write: true` at root | 183 | 8.1 |
| `auth.token.email` | 166 | 7.4 |
| other string methods (contains, beginsWith, endsWith, replace, toLower/Upper) | 145 | 6.4 |
| ternary `?:` (upper bound) | 122 | 5.4 |
| arithmetic (upper bound) | 102 | 4.5 |
| `query.*` | 77 | 3.4 |
| `auth.token.email_verified` | 58 | 2.6 |
| `auth.provider` | 6 | 0.3 |
| `getPriority` | 2 | 0.1 |

Method histogram over all expressions: child 78,952; val 49,792; exists 12,064; isString 9,719; isNumber 6,250; parent 4,244; hasChildren 4,162; matches 1,829; replace 1,352; isBoolean 1,313; hasChild 1,215; contains/beginsWith 207 each; toLowerCase 181; endsWith 79; getPriority 4. Variables: newData 53,069; auth.uid 28,027; root 19,771; data 11,884; auth.token.email 2,079; now 1,977; auth.token.email_verified 611; query.orderByChild 165.
Custom claims are common inside `auth.token` (admin 578, role 419, superadmin 135, roleMap 157, companyId 84, tenantId 90...).

Tiers (files that would break if a feature set were dropped):
| Dropped | files broken | % |
|---|---|---|
| regex only | 323 | 14.3 |
| regex + other string methods | 404 | 17.9 |
| + ternary, arithmetic, getPriority | 594 | 26.3 |
| + query.* | 630 | 27.9 |
| + now | 711 | 31.5 |
| + auth.token.* | 868 | 38.5 |
"Core only" (auth, `$var`, data/newData/root, child/parent/val/exists/hasChild(ren)/isX, boolean and comparison ops, `.length`, `.validate`, `.indexOn`) serves 61.5% of files.

### 4.3 Targaryen coverage (ISC, 3.1.0, https://github.com/goldibex/targaryen)
- Implements: `.read/.write/.validate` with cascade and non-cascade validate (lib/database/ruleset.js:63,161,205), `.indexOn` shape validation only (ruleset.js:50-56, no query-planning), `now` (ruleset.js option), `query.*` object (lib/database/query.js:7-22, orderByKey/Value/Priority and limits), RuleDataSnapshot methods and string methods (lib/parser/statement/member.js, lib/parser/string-methods.js), regex parser with the restricted subset (lib/parser/regexp/, spec 330 lines), auth with token claims.
- Does not: serve data, enforce key/size limits, transactions, or the multi-path `update` semantics beyond `tryPatch`.
- The "186 fixtures" are `test/spec/lib/parser/fixtures.json` -> `tests` (186 rule-expression cases; 28 must fail to parse, 72 fail at runtime; users: unauth 102, bob 81, uidWithEmail 3). Rough mapping by feature (a case can hit several): comparison ops 149, `auth` 126, `root` 48, `.val()` 22, string methods 20, `child()` 17, array/object literals 16, `hasChildren` 9, regex `matches` 9, logical 9, `exists` 6, `$wildcard` 6, `hasChild` 4, arithmetic 4, ternary 3, `isNumber/isString/isBoolean` 1, `parent()` 1. So the fixtures test the expression evaluator, with almost no coverage of `.validate`/cascade/query semantics. Those live in the other specs: test/spec/lib/database/index.js (1275 lines), ruleset.js (407), query.js (146), store.js (388), plus integration/tests.json (5 tests).
- INFERENCE: fixtures are usable as a conformance seed for the expression layer only; behaviour tests for cascade, validate-on-delete, merge-newData must be written or lifted from database/index.js.

### 4.4 Size of existing engines (cost proxy)
| Engine | Lines | Notes |
|---|---|---|
| targaryen lib/ (JS) | 4,397 (parser+statements+regexp 1,853; database 1,355) | full RTDB semantics; tests 5,036 lines |
| Lark rules (Rust, AGPL) | 7,479 incl. inline tests; about 4,470 excluding `#[cfg(test)]` | server/src/rules/{evaluator 1126, snapshot 2105, parser 748, expr/{lexer 619, parser 780, eval 910, value 850, ast 163, token 122}}; evaluator documents `now`, query, non-cascading validate (evaluator.rs:15,157,237) |
| Bolt (Apache) | src/rules-parser.pegjs 610; rules-generator.ts 1,048; ast.ts 766 | compiles a different DSL to rules JSON; does not evaluate; only a grammar-size proxy |
| flakbase rules.go | 91 | only reads `.indexOn`/`$` key shape; no expression evaluator |
| firebase-server (JS) | depends on targaryen ^3.1.0 (package.json:65) | confirms targaryen is the reusable reference |
Estimate (INFERENCE): a Go evaluator with lexer, Pratt parser, evaluator, snapshot adapter over Postgres, RE2 translation, and cascade logic is about 3,000 to 4,500 lines, comparable to Lark's non-test code. The expensive part is not the language but `root.child(...)`/`data.child(...)` snapshot reads from Postgres inside a write transaction (consistency, caching, cost per rule evaluation).

### 4.5 Conclusion Q4
- "Full language minus rare parts" is realistic. The whole language is small (about 40 named items) and fully specified by one reference page; nothing needs a Turing-complete runtime.
- What is rare: `getPriority` (0.1%), `auth.provider` (0.3%), `query.*` (3.4%), `auth.token.email_verified` (2.6%), non-regex string methods (6.4%), arithmetic/ternary (about 5%), `.indexOn` has no semantic effect on correctness.
- What is not rare and must ship in v0.2: `$vars`, `auth`, `auth.uid`, `auth.token` claims (14.6%, trivial: it is a map), `newData/data/root` + `child/parent/val/exists/hasChild(ren)/isX`, boolean/comparison ops, `.length`, `.validate` (non-cascade, skipped on delete), `now`, regex `matches` (14.3%, needs the restricted flavour), `.indexOn` parsed and accepted.
- Cheapest cut with small loss: drop `query.*` and `getPriority` and maybe ternary/arithmetic first. Even dropping all of {regex, string methods, ternary, arithmetic, getPriority, query} still serves about 72% of sampled files, but regex alone is 14% and cheap, so keep it.
- Whatever is unsupported must fail loudly at rule-deploy time (PUT `.settings/rules.json` returns 400 `{error}`), not silently deny.

---
## Q5. REST surface needed

### 5.1 Per Admin SDK (VERIFIED from source)
| SDK | Transport | Evidence |
|---|---|---|
| Node | WebSocket (wraps `@firebase/database-compat` `initStandalone`, auth sent as `Authorization: Bearer` header and `auth`/`gauth` action). Separate REST client only for rules: `GET/PUT <db>/.settings/rules.json`, `GET ... ?format=strict`, `content-type: application/json; charset=utf-8`, Bearer token | src/database/database.ts:130 (initStandalone), 191 (`RULES_URL_PATH`), 220-293 (getRules/getRulesJSON/setRules); js-sdk realtime/WebSocketConnection.ts:166-186 |
| Java 9.11.0 | WebSocket only (no REST data client, no LP). `wss://host/.ws?ns=<ns>&v=5[&ls=..]`, auth via `auth`/`gauth` actions | database/connection/HostInfo.java:36-45; PersistentConnectionImpl.java:64-65,773-776 |
| Go | REST only, no listeners | db/db.go, ref.go, query.go |
| Python | REST for everything, plus SSE for `listen()` | db.py, _sseclient.py |
| .NET | No database client exists (only Auth, Messaging) | FirebaseAdmin/FirebaseAdmin/ has Auth, Messaging, no Database; CONTRIBUTING.md:179 only mentions a DB for tests |

### 5.2 REST features used by Go and Python
| Feature | Go (66a72c3) | Python (de45607) |
|---|---|---|
| URL | `<base>/<path>.json` (db.go:125) | `<base>/<path>.json` (db.py:460 `_add_suffix`) |
| GET | ref.go:79 | db.py:219 |
| `shallow=true` | ref.go:106-108 (GetShallow) | db.py:219 |
| ETag read: header `X-Firebase-ETag: true`, response `ETag` | ref.go:86-90, 247-249 | db.py:216-217 |
| `If-None-Match: <etag>` -> 304 | ref.go:124-143 (GetIfChanged) | db.py:223-246 |
| PUT with `print=silent` (204) | ref.go:153-156 | db.py:263 |
| PUT with `If-Match: <etag>` -> 412 returns new value + ETag | ref.go:169-172 | db.py:292-298 |
| POST (push, returns `{"name":key}`) | ref.go:198 | via `push()` |
| PATCH `print=silent` | ref.go:218-221 | db.py:340 |
| DELETE | ref.go:289 | via set(None)/delete |
| Transaction = GET with `X-Firebase-ETag` then PUT `If-Match`, retried | ref.go:247-281 | db.py:376-420 (`transaction` loop; `TransactionAbortedError` after retries) |
| Query params `orderBy` (JSON-encoded string e.g. `"$key"`, `"$value"`, `"child/path"`), `limitToFirst`, `limitToLast`, `startAt`, `endAt`, `equalTo` (JSON-encoded) | query.go:181-203 | db.py:501 and following |
| Auth override query `auth_variable_override=<json>` | db.go:35,127-129 | db.py:818 |
| Emulator namespace `ns=<ns>` | db.go:61,131-133 (only emulator) | db.py:809 (only emulator) |
| Credentials | OAuth2 access token via Google HTTP client (Authorization: Bearer); emulator uses a fixed static token (`emulatorToken`) | `AuthorizedSession`; emulator `EmulatorAdminCredentials` |
| Error body | `{"error": "..."}` mapped to message, status mapped to code (db.go:146-155) | `_Client.handle_rtdb_error` |
| Production URL scheme | any (BaseURL from databaseURL) | production forced `https://<netloc>` (db.py:812-815); `http://host:port?ns=..` accepted only when `FIREBASE_DATABASE_EMULATOR_HOST` or emulator-style URL (db.py:836-850) |
Not found anywhere in these SDKs or firebase-tools: `X-Firebase-Decoding`, `format=export` (only in JS ReadonlyRestClient and `database:get --export`), `access_token` query param (they use the Bearer header; the query form is only in the REST docs).

### 5.3 SSE (Python `listen()`)
- db.py:350-373 (`listen`), 463-473 (`_listen_with_session`), `ListenerRegistration` db.py:106-137 runs a background thread; _sseclient.py:94-95 sets `Accept: text/event-stream`, 105-110 `session.get(url, stream=True)`, `KeepAuthSession` (30-44) keeps the Authorization header across redirects (the REST docs require handling 307), retry default 3000 ms and honours `retry:` field (74-85,146-148).
- Docs say "experimental"; it ignores auth overrides and timeout. Event types per REST reference: `put`, `patch`, `keep-alive`, `cancel` (reasons "Permission denied." or "The specified payload is too large, please request a location with less data."), `auth_revoked`. Python surfaces `put`/`patch` as `db.Event` (_sseclient parse; keep-alive filtered).
- So SSE is needed for exactly one official client feature: Python Admin `Reference.listen()`. No other official SDK listener needs it (Node/Java Admin and all client SDKs use the WebSocket). INFERENCE: skip SSE in 0.2 unless Python listen() is a stated goal.

### 5.4 firebase CLI (firebase-tools 4e6eb92, MIT)
| Command | Request | File |
|---|---|---|
| database:get | `GET <path>.json` with `shallow=true`, `print=pretty`, `format=export`, `orderBy`, `limitToFirst/Last`, `startAt/endAt/equalTo` (streamed response) | commands/database-get.ts:79-120 |
| database:set | `PUT <path>.json[?disableTriggers=true]`, body streamed | commands/database-set.ts:37-62 |
| database:update | `PATCH <path>.json[?disableTriggers=true]` | commands/database-update.ts:51-65 |
| database:push | `POST <path>.json`, response `{name}` | commands/database-push.ts:36-52 |
| database:remove | recursive: `GET ?shallow=true&limitToFirst=N`, then `PATCH ?print=silent&writeSizeLimit=tiny&disableTriggers=` with null children; fallback to chunked deletes when it fails (a 400 WRITE_TOO_BIG is the signal) | database/remove.ts:55-110; removeRemote.ts:50-60; listRemote.ts:40-44 |
| database:import | `GET ?shallow=true` (must be empty) then chunked `PATCH <path>.json` | database/import.ts:147-148, 230-231 |
| database:profile | `GET /.settings/profile.json` with `Accept: text/event-stream` (SSE) | profiler.ts:22,59-66 |
| database:settings:get/set | `GET/PUT /.settings/<name>.json` (defaultWriteSizeLimit, strictTriggerValidation, ...) | commands/database-settings-*.ts:36,41; database/settings.ts |
| deploy --only database | `PUT /.settings/rules.json[?dryRun=true]` | rtdb.ts:47 |
| host/namespace | origin from `FIREBASE_REALTIME_URL` env (api.ts:123) or instance databaseUrl; namespace added as subdomain if host contains firebaseio/firebasedatabase, else `?ns=<ns>` | utils.ts:97-136 |
| auth | all via `Client({auth:true})` = Google access token (Authorization: Bearer) | |
Instance create/list use the management API (not the data host), out of scope.

### 5.5 Minimum REST surface (INFERENCE from the above)
1. `GET/PUT/POST/PATCH/DELETE /<path>.json` with `?ns=`.
2. Params: `print=silent|pretty`, `shallow=true`, `orderBy` (JSON string; `"$key"`, `"$value"`, `"$priority"`, child path), `limitToFirst/Last`, `startAt/endAt/equalTo` (JSON values), `format=export` (priorities), `auth_variable_override`, `disableTriggers` (accept and ignore), `writeSizeLimit` (accept; honour `tiny` or just accept), `timeout` optional.
3. Headers: `Authorization: Bearer` (Google OAuth token or Firebase ID token, per REST auth doc also `?auth=` and `?access_token=`), `X-Firebase-ETag: true` -> `ETag`, `If-Match` (PUT/DELETE -> 412 with current value + ETag), `If-None-Match` (-> 304).
4. Status codes per REST doc (400 parse/too large/bad index/bad param mix, 401 auth/rules, 404 db not found, 412 etag, 500, 503). Error body `{"error":"..."}`.
5. `/.settings/rules.json` GET/PUT (+`dryRun`, `format=strict`) - needed by `firebase deploy` and Admin Node setRules.
6. Optional later: SSE on `/<path>.json` (Python listen, `database:profile` which also needs `/.settings/profile.json`), JSONP `callback=`, `download=`.
7. Because Go and Python Admin SDKs and the CLI exist as REST clients, Admin REST needs transactions via ETag (hash of the node, must be consistent with `If-Match` semantics: single ETag value only; ETags are only returned when `X-Firebase-ETag: true`).
JS client SDK also has a REST fallback (Repo.ts:218 `forceRestClient_ || beingCrawled()`; ReadonlyRestClient.ts:152-190: `GET <path>.json?ns=<ns>&format=export&auth=<token>&ac=<appcheck>` plus query params; 404 treated as null, 401 -> permission_denied). It is used for crawlers and a test hook, so INFERENCE: not needed.

---
## Q6. Long polling

### 6.1 Exact JS SDK logic (firebase-js-sdk d7bb0e1, packages/database/src)
1. Transport list is built per `Connection` in `TransportManager.initTransports_` (realtime/TransportManager.ts:55-82): `isSkipPollConnection = isWebSocketsAvailable && !WebSocketConnection.previouslyFailed()`; if `repoInfo.webSocketOnly` it is forced true (63-70). If skip: `transports_ = [WebSocketConnection]` (73). Else every available transport of `[BrowserPollConnection, WebSocketConnection]` in that order (75-80), so LP FIRST, WebSocket second.
2. `webSocketOnly = parsedUrl.scheme === 'ws' || 'wss'` (core/util/libs/parser.ts:88). A `databaseURL` of `wss://host` therefore forces WebSocket only. No port in the URL means `secure` stays true even for `ws://` (parser.ts:140-146). VERIFIED in code; not tested.
3. `WebSocketConnection.previouslyFailed()` = `PersistentStorage.isInMemoryStorage || localStorage['previous_websocket_failure'] === true` (WebSocketConnection.ts:279-285). In-memory storage (localStorage blocked/unavailable, storage.ts:36-53) therefore counts as "previously failed". The flag is written `true` on EVERY `open()` ("Assume failure until proven otherwise", line 162) and only removed in `markConnectionHealthy()` (288-290).
4. Healthy = `responsesRequiredToBeHealthy = 2` messages on the primary (WebSocketConnection.ts:269; Connection.ts:325-333 counts data messages and pongs), or after `healthyTimeout` 30 s if bytesReceived > 100 KiB (Connection.ts:44,152-185); a connection that sends >10 KiB but receives nothing is neither healthy nor closed. After handshake, if still unhealthy after 5 s the SDK sends pings (Connection.ts:38,461-473).
5. LP transport availability: false in Node SDK, true in browsers with `document` (BrowserPollConnection.ts:268-283); `forceLongPolling()` forces it (Database.ts:306-310, also sets `WebSocketConnection.forceDisallow`), `forceWebSockets()` disallows LP (Database.ts:298-301). Both warn if called after the first connection (`checkTransportInit`, 285-291). React Native (no `document`) and Node never use LP.
6. If LP is the initial transport: after the LP handshake, `onHandshake_` -> `tryStartUpgrade_` (Connection.ts:396-410) opens a WebSocket secondary (`s=<session id>` param) with a 60 s upgrade timeout (34,431-437). If that WebSocket gets healthy the flag is cleared and the next `Connection` is WebSocket-only. LP connect timeout is 30 s (BrowserPollConnection.ts:80). Script-tag/iframe JSONP transport has NO `onerror` handler (grep: none), so a 404 on `/.lp` is only detected by that 30 s timeout, after which `onDisconnect(everConnected=false)` fires.
7. Reconnect: every retry builds a new `Connection` -> a new `TransportManager` -> re-evaluates `previouslyFailed()` (Connection.ts:110). `PersistentConnection` backs off 1 s * 1.3 up to 5 min (PersistentConnection.ts:45-49,805-816).

### 6.2 Answer: does a 404 `/.lp` get stuck?
YES in the case that matters (INFERENCE from the code above, not run):
- Web SDK (browser) with `previous_websocket_failure=true` in localStorage (or no usable localStorage): initial transport = LP; WebSocket is attempted only as an upgrade after a successful LP handshake. With no `/.lp` the handshake never comes, LP times out in 30 s, the flag stays true, and every retry repeats LP-first. It never tries WebSocket again until the user clears site data or the app calls `forceWebSockets()`.
- How the flag gets set without any real network problem: any `open()` that does not reach 2 responses (tab closed or reload within a second or two of load, server restart during startup, Aruma closing early, mobile app backgrounded, a rules/permission rejection that ends the socket) leaves it true. So the "WebSocket blocked" population is larger than real blocking.
- Android, iOS (and Java/Go Admin) never use LP (no `.lp` code in firebase-android-sdk/firebase-database/src/main or FirebaseDatabase/Sources; grep empty), Node and React Native cannot use it. Only browser JS (including Flutter web, which wraps JS) is exposed.
- Is there a server reply to `/.lp` that sends the SDK back to WebSocket? From code: no. `reset` (host change, Connection.ts:439-451) restarts `start_()` on the same TransportManager so it stays LP-first; `shutdown` kills the connection. The only way back to WebSocket is a successful LP handshake followed by a healthy WebSocket upgrade (clears the flag). A minimal LP implementation that only completes the JSONP handshake `{ts, v, h, s}` and then closes would be enough to trigger the upgrade, but needs the iframe-JSONP framing (segment numbering `pN`, `start`/`close` commands, BrowserPollConnection.ts:158-240) and the server must accept the WebSocket upgrade with `s=<transport session>`. UNVERIFIED; this is real work (about the size of a second transport).

### 6.3 Real-world prevalence of WebSocket blocking
- No measured percentage found. Ably's blog says it plainly gives none; lists causes: firewalls not recognising the protocol, proxies closing "idle" connections, strict/outdated antivirus with firewall component, router TCP timeouts, mobile provider handling, heuristic firewall false positives; likely in "government institutions, schools, hospitals, call centers" (https://ably.com/blog/websocket-compatibility, fetched 2026-10-20).
- Socket.IO starts with HTTP long polling by default because "it is not always possible to establish a WebSocket connection, due to corporate proxies, personal firewall, antivirus software..."; a failed attempt costs up to 10 s (https://socket.io/docs/v4/how-it-works/). No percentage.
- Ably FAQ: browser SDK historically started on Comet and upgraded because some proxies break WebSockets subtly (https://faqs.ably.com/why-the-realtime-sdk-is-always-establishing-comet-connections-for-long-polling-rather-than-using-it-as-a-fallback-for-websockets).
- Old measurement (2009 Google investigation as quoted by https://www.teskalabs.com/blog/tcp_port_for_seacat, secondary source, UNVERIFIED): WebSocket reach about 95% on port 443, 86% custom port, 67% port 80. Plain ws:// over port 80 is the bad case; wss:// on 443 is much better because proxies cannot inspect it unless they MITM TLS.
- One anecdotal figure (<3% of clients on a fallback path) from an unrelated DEV post: not used.
- caniuse WebSockets: 97.49% "supported" usage in the data file (https://github.com/Fyrd/caniuse features-json/websockets.json, `usage_perc_y`, fetched 2026-10-20; snapshot date not stated), the Socket.IO page quotes 99.84%. Browser support is not the issue; the network path is.
- Firebase guidance: the reference marks `forceLongPolling`/`forceWebSockets` as INTERNAL, "for debugging and special cases"; no docs page on proxy troubleshooting found (reference for Database, firebase.google.com/docs/reference/node/firebase.database.Database). A js-sdk issue about slow fallback (https://github.com/firebase/firebase-js-sdk/issues/540) was answered with "check your router/proxy".

### 6.4 Conclusion Q6
- Skipping `/.lp` is acceptable for Android, iOS, Admin SDKs, Flutter/RN native, Node. For browser JS it risks two groups: (a) users on networks that really block WebSockets (small, concentrated in enterprise/government/school/hospital networks and TLS-inspecting proxies; magnitude unmeasured, likely low single-digit percent or less), and (b) users whose stored flag is stale for benign reasons (probably larger and invisible to the operator), who then get stuck for good.
- Mitigations, cheapest first (INFERENCE):
  1. Document that browser apps pointing at Aruma should call `forceWebSockets()` (Database.ts:298) before first use, or use a `wss://host` databaseURL (parser.ts:88 `webSocketOnly`). Both skip the LP-first path completely and the flag is then ignored.
  2. Make the first WebSocket healthy fast: make sure the server answers the first listen/auth requests promptly and sends a keepalive/pong so the 2-response threshold is reached in under a second; avoid closing on early rules/auth errors without replying.
  3. Serve a real minimal `/.lp` only if browser support for blocked networks becomes a requirement (second transport, large).
  4. A 404 on `/.lp` makes things worse than a fast refusal only in time (30 s timeout per attempt); a quick 4xx cannot be detected by the SDK anyway (no onerror).

---
## Q8. Limits

### 8.1 Official (https://firebase.google.com/docs/database/usage/limits, fetched 2026-10-20)
| Limit | Official value and wording |
|---|---|
| Simultaneous connections | 200,000 per database (100 on Spark); "equivalent to one mobile device, browser tab, or server app connected to the database" |
| Simultaneous responses | about 100,000/second per database, sustained |
| Cloud Functions per write | 1000, or 500 per region for v2 |
| Event size triggered by a write | 1 MB (larger writes succeed but do not trigger functions; `strictTriggerValidation` default on makes them fail) |
| Max depth of child nodes | 32 ("Each path in your data tree must be less than 32 levels deep") |
| Length of a key | 768 bytes, UTF-8, no new lines and none of `. $ # [ ] /` or ASCII control chars 0x00-0x1F and 0x7F |
| Max size of a string | 10 MB, UTF-8 |
| Single response served | 256 MB |
| Total nodes in a path with listeners/queries | 75 million (cumulative) |
| Query run time | 15 minutes |
| Write rate | 1,000 writes/second per database ("not a hard limit ... may be rate-limited") |
| Single write request | 256 MB from REST; 16 MB from the SDKs; multi-path updates same limit |
| Bytes written | 64 MB/minute |
| REST `writeSizeLimit` | tiny (target 1s), small (10s), medium (30s), large (60s), unlimited; "Realtime Database estimates the size of each write request and aborts requests that will take longer than the target time"; new databases default to `large` (reference/rest/database) |
| Streaming `cancel` cause | stream payload over 512 MB -> "The specified payload is too large, please request a location with less data." (reference/rest/database) |
| Rules file size | UNVERIFIED for RTDB |

### 8.2 Enforcement: client vs server (VERIFIED in code)
| Check | JS (core/util/validation.ts, Path.ts) | Android | iOS | Message / code |
|---|---|---|---|---|
| Invalid key chars | `INVALID_KEY_REGEX_ = /[\[\].#$\/\u0000-\u001F\u007F]/` and non-empty (validation.ts:45,59-61; used at 73) | ValidationPath/Validation.java:36,67-72 | FValidation.m | thrown synchronously: "contains an invalid key (x) ... Keys must be non-empty strings and can't contain ".", "#", "$", "/", "[", or "]"" (validation.ts:75-82). Never reaches the server. `.priority`, `.value`, `.sv` allowed |
| Path chars (refs) | `INVALID_PATH_REGEX_ = /[\[\].#$\u0000-\u001F\u007F]/` (51) | | | thrown |
| Key path length | 768 UTF-8 bytes for the whole key path (Path.ts:26,308-315) | ValidationPath.java:35,101-107 | not found | "has a key path longer than 768 bytes (N)." / "Data has a key path longer than ..." |
| Depth | 32 (Path.ts:23,318-325) | ValidationPath.java:36,109-114 | `kFirebaseMaxObjectDepth = 1000` (FConstants.m:182, cycle guard, FSnapshotUtilities.m:75) | "path specified exceeds the maximum depth that can be written (32) or object contains a cycle". iOS does NOT enforce 32 client-side (server must) |
| String leaf size | 10 MB (`MAX_LEAF_SIZE_`, validation.ts:56,146-160, checks `length > 3.3 MB` first, then UTF-8 length) | NOT found in firebase-database main source | 10 MB (FConstants.m:183; FValidation.m:186-206) | "contains a string greater than 10485760 utf8 bytes ..." (JS); "String exceeds max size of 10485760 utf8 bytes" (iOS) |
| undefined, function, NaN/Infinity, `.value` plus children | validation.ts:121-143, 167-174 | similar | similar | thrown |
| Whole-write size (16 MB SDK, 256 MB REST) | no client check; only the 16384-byte WebSocket frame splitting (WebSocketConnection.ts:44,371) | none seen | none seen | server-side |
| Per-write time-based `writeSizeLimit` | n/a | n/a | n/a | server-side, `defaultWriteSizeLimit` also applies to SDK writes |
| Permission/rules | | | | server |

### 8.3 Error strings and codes
- Server status strings the SDK understands (Android DatabaseError.java:108-118): `datastale`, `failure`, `permission_denied`, `disconnected`, `expired_token`, `invalid_token`, `maxretries`, `overriddenbyset`, `unavailable`; client-only `network_error`, `write_canceled`. JS maps an unknown status to `Error` with `code = status.toUpperCase()` and message `<status> at <path>: <reason>` (util.ts:473-491); known reasons: `too_big` = "The data requested exceeds the maximum size that can be accessed with a single request.", `permission_denied` = "Client doesn't have permission to access the desired data.", `unavailable` = "The service is unavailable".
- `too_big` is a READ error status (listen/get). The strings `write_too_big` and "Write too large" do NOT occur in any SDK source searched (js, android, ios, firebase-tools). Documented only on REST: `Error: WRITE_TOO_BIG: Data to write exceeds the maximum size that can be modified with a single request.` (reference/rest/database, `writeSizeLimit` section). A third-party issue reports a WebSocket import failing with `write_too_big` (https://github.com/FirebaseExtended/firebase-import/issues/45, title only, unverified). INFERENCE: on the wire the put/merge ack status is `write_too_big` and the JS error code `WRITE_TOO_BIG`; verify against the official emulator if exactness matters.
- REST: oversize PUT/POST -> HTTP 400; invalid child names -> 400; path too long -> 400; unindexed query -> 400 (docs rest/save-data "Error Conditions"); rules violation, expired/invalid token -> 401; DB missing -> 404; 503 unavailable. Python/Go surface body `{"error": ...}`.
- Index missing on query over WebSocket: the JS SDK only logs a warning (not an error); server still answers. REST 400 is the documented behaviour (INFERENCE: verify with emulator).
- Connection-count limit behaviour (what the server sends when over 200,000 or 100 on Spark): UNVERIFIED, not in SDK code.
