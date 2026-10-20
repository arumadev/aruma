# Track B: official RTDB emulator as behavioural oracle

Date 2026-10-20. Emulator: `firebase-database-emulator-v4.11.2.jar`, run directly (`java -jar ... --host 127.0.0.1 --port 9400`, Java 19), namespace `demo-test`, default rules open. All frames below are our own observations from the outside (raw WebSocket / curl). The JAR was not opened. Scripts: `/Volumes/Work/aurma-project/.cache/research/emu/scripts/` (lib.mjs, a1-a6.mjs, r*.sh). Everything is "verified (observed)" unless marked *inference*. Single-run observations: treat exact numbers as approximate where noted.

`--help` options: `--host`, `--port`, `--functions_emulator_host/port`, `--export_data_access_logs`, `--single_project_mode Warning|Error`. No flag for rules or import; rules are loaded over HTTP.

## 1. WebSocket

### 1.1 Handshake
- Request: `ws://127.0.0.1:9400/.ws?v=5&ns=demo-test`
- First server frame, immediately on open:
  `{"t":"c","d":{"t":"h","d":{"ts":1792507779527,"v":"5","h":"127.0.0.1:9400","s":"GdAQ4HP6X0SOwGPPHTbRJoKaNZrGYU3u"}}}`
- Fields: `ts` ms epoch, `v` string "5", `h` = the Host header as the client sent it (incl. port), `s` = 32 alphanumeric chars (random session id).
- No server "ping" or "0" frame was ever sent by the server in any run.
- Plain HTTP GET on `/.ws` returns nothing (empty reply).
- Long-poll exists too: `GET /.lp?start=t&ser=1&cb=1&v=5&ns=demo-test` returns `application/javascript` with `pLPCommand('start','2','SNEgE4OrFC'); pRTLPCB(0,[{"t":"c","d":{"t":"h",...}}]);` (same handshake JSON inside).

### 1.2 Keepalive / idle
| Scenario | Observed |
|---|---|
| Connection idle, client sends nothing | Closed by server at ~120.2 s (client saw code 1006), run once; a 60 s idle run was still open |
| Client sends text frame `0` every 40 s | Stayed open for 160 s+ (no server reply to `0`) |
- *Inference*: server idle timeout ~120 s, reset by any client frame; SDK sends `0` about every 45 s so this works. Server does not need to ping.

### 1.3 Request/response envelope
- Client: `{"t":"d","d":{"r":<n>,"a":<action>,"b":{...}}}`.
- Response: `{"t":"d","d":{"r":<n>,"b":{"s":"ok","d":<payload>}}}` (`s` is status string).
- Server push: `{"t":"d","d":{"a":<d|m|c>,"b":{"p":"path/no/leading/slash","d":<data>,"t":<tag?>}}}`.
- Paths in server pushes have no leading slash (`"p":"x"`, `"p":"f/y/z"`); root push would be `""`. Client sent `/x`.
- Pipelined requests (r=16,17,18 in one burst) are answered in order.

### 1.4 Listen (`q`), put (`p`), merge (`m`)
| Step | Request | Observed |
|---|---|---|
| listen empty path | `{"r":2,"a":"q","b":{"p":"/x","h":""}}` | only `{"r":2,"b":{"s":"ok","d":{}}}`; **no data push for null** |
| put | `{"a":"p","b":{"p":"/x","d":{"a":1,"b":"hi"}}}` (not listening) | `{"r":3,"b":{"s":"ok","d":""}}` only |
| put while listening on same path | same | push `{"b":{"p":"x","d":{...}},"a":"d"}` **before** the ack, same millisecond |
| listen with data | `q /x` | push `a:"d"` with full value, THEN `{"r":4,"b":{"s":"ok","d":{}}}` (data first, ack second) |
| merge | `{"a":"m","b":{"p":"/x","d":{"b":2,"c":3}}}` | push `"a":"m"` with only the merged keys: `{"p":"x","d":{"b":2,"c":3}}`; then ack |
| merge with null | `m /f {"x":null,"w":9}` | push `a:"m"` `{"x":null,"w":9}` (nulls kept in merge payload) |
| put on child | `p /f/y/z d:3` | push `{"p":"f/y/z","d":3}` `a:"d"` (path as written, not rolled up) |
| listen on child of data written | `q /f/w` | push `{"p":"f/w","d":9}`, ack |
| listen with stale hash `h:"zzz"` | `q /f h:"zzz"` | full data push, ack (hash mismatch just resends everything) |
| put with `h` equal to current hash (= REST ETag of that path) | `p /h d:{s:2} h:"8n5k..."` | fan-out push to listener; ack `{"s":"ok","d":{"s":2}}` (ack carries data on conditional put) |
| put with wrong/stale `h` | `p /x/a d:5 h:"wronghash"` | `{"r":6,"b":{"s":"datastale","d":"Transaction ETag does not match"}}` |
| unlisten | `{"a":"n","b":{"p":"/f"}}` (`q:{}` optional) | `{"s":"ok","d":""}`; later writes produce no push to that socket |
| stats | `{"a":"s","b":{"c":{"sdk.js.10-0-0":1}}}` | `{"s":"ok","d":""}` |
| put `{}` / put `null` on path | `p /h d:{}` | deletes: listener gets `{"p":"h","d":null}` |
| put at root `/` | `p / d:{k:1}` | replaces whole tree; listener on `/h` gets `{"p":"h","d":null}` |
| put arrays | `p /arr d:[1,2]`, then listen | comes back as object `{"0":1,"1":2}` on WS (REST returns array, see 3) |
| number 1.0 | `p /num d:1`, listen | `d:1` |
- Note: the writer socket without a listener gets no data event, just the ack. With a listener on the same socket, event comes first, then ack.
- Unknown action `zzz`, non-JSON text, or a malformed key inside `d` (e.g. `{"a.b":1}`) produce a control frame and **the socket stays open**, no `r` response: `{"t":"c","d":{"t":"e","d":"ClientId[3]:ErrorId[1]: Error on incoming message"}}` (counters increment). Note the client's request is left without any response.
- Invalid path in a request gets a normal error: `p /bad/x.y` -> `{"s":"invalid_parameters","d":"Invalid token in path"}`.
- Increment with a non-number (`{".sv":{"increment":"x"}}`) -> same generic `t:"e"` control frame, no response.

### 1.5 Server values, priority
- `p /sv {"ts":{".sv":"timestamp"},"n":{".sv":{"increment":5}}}` then `p /sv/n {".sv":{"increment":5}}` -> `/sv` reads `{"n":10,"ts":1792507857299}`. Increment on a missing node starts from 0. Timestamp is ms epoch resolved by the server; in the onDisconnect case the timestamp is resolved at disconnect time.
- Priority via `.value`/`.priority`: `p /pr {".value":"v",".priority":7}` -> read back `{".value":"v",".priority":7.0}` (priority serialized as a double `7.0`, string priority `"abc"` kept). Children: `{"j":{".priority":2.0,"a":1},"k":{".value":1,".priority":"abc"}}`: `.priority` listed before data keys; numeric priority sorts before string.
- REST `GET` plain drops priority metadata (`{"x":1}`); `format=export` keeps `{".value":1,".priority":3.0}`.

### 1.6 onDisconnect
Actions: `o` (put), `om` (merge), `oc` (cancel; `b:{p}`), all ack `{"s":"ok","d":""}` immediately; nothing is applied until close.
- Registered on A: `o /od/put "gone"`, `om /od {m1:1,m2:{.sv:timestamp}}`, `o /od/cancelme "x"` then `oc /od/cancelme`, `o /od/inc {.sv:{increment:1}}`. Closed A.
- ~230 ms after the close, listener B on `/od` got **one** merge push: `{"p":"od","d":{"put":"gone","m1":1,"m2":1792507877395,"inc":1}}` `"a":"m"`. The cancelled one did not appear. The pushes from several disconnect writes arrived coalesced into one `m` frame.
- Under locked rules, `o` returns `permission_denied` at registration time (not at fire time).

### 1.7 Queries
Listen with query: `{"a":"q","b":{"p":"/qq","q":{"i":"s","l":2,"vf":"l"},"t":1,"h":""}}` (i = orderBy child, l = limit, vf = "l" limitToFirst; `sp` = startAt value, etc.).
| Case | Observed |
|---|---|
| no `.indexOn` | response `{"s":"ok","d":{"w":["no_index"]}}`. The data push is **untagged and unfiltered** (all 4 children, limit ignored). Same ack for repeated identical listens. Warning only on this listen. |
| with `.indexOn:["s"]` on `/qq` | data push is **tagged and filtered**: `{"p":"qq","d":{"b":{"s":1},"d":{"s":0}},"t":1}`; ack `{"s":"ok","d":{}}` (no `w`) |
| later write entering the window `p /qq/e {s:-1}` | push `{"p":"qq/e","d":{"s":-1},"t":1}` only. **No explicit remove event for the evicted child**; client must compute eviction |
| write changing an existing child's sort value | `p /qq/a/s -5` -> push `{"p":"qq/a","d":{"s":-5},"t":1}` (path is the child, data is the whole child) |
| unlisten query | `n {p:/qq,q:{...},t:1}` -> `ok` |
| `sp` (startAt) + `l` | accepted, empty result when no index |
- Tag `t` on pushes only when the listen is a query *and* the query is served by an index.
- *Inference*: SDK relies on tag to route pushes to the right query view; Aruma must send `t` for query listens, and must send no-index warnings in the `w` array if it does not index.

### 1.8 Auth
| Request | Response |
|---|---|
| `auth {cred:"owner"}` | `{"s":"invalid_token","d":"Could not parse auth token."}` (REST accepts `owner`, WS does not) |
| `auth {cred:"garbage"}` | same invalid_token |
| `auth {cred:<alg:none JWT>}` with payload `{uid,sub,user_id,iat,exp}` and empty signature | `{"s":"ok","d":{"auth":{"uid":"u1","token":{"exp":..,"user_id":"u1","uid":"u1","iat":1,"sub":"u1"},"provider":"anonymous","user_id":"u1"},"expires":null}}` |
| `unauth {}` | `{"s":"ok","d":"unauthenticated"}`; **and a listen that is no longer permitted is cancelled by the server**: push `{"a":"c","b":{"p":"users/u1","q":null}}` before the ack |
| `gauth {cred:"x"}` | `{"s":"ok","d":{"auth":null,"expires":1792511482}}` (expires ~1 h ahead; token not validated) |
| `appcheck {token:"x"}` | `{"s":"invalid_token","d":"Could not parse token."}` |
- Signature is not checked at all (empty signature accepted). `auth.provider` is always "anonymous"; the `token` object echoes the JWT claims as sent (the emulator reads claims such as `uid`, and shows `sub`; a Firebase ID token has `user_id`/`sub`, no `uid`, and the `uid` field in `auth` came from the payload I supplied, *not verified whether it falls back to `sub`*).
- Server revocation: the **`a:"c"` frame** (listen cancelled, `q:null`) is how the server cuts a listen it can no longer serve. Changing rules to locked while B was listening did **not** send any `c` or event (checked once): rules are evaluated on listen and write, not retroactively.

### 1.9 Large payloads and frame splitting
- Server -> client: a 60 KB put observed by listener B arrived as a frame count then chunks: frames `"4"`, then 3 x 16384 chars, then 11423 chars (text frames). First frame is a bare decimal count; chunks concatenate to one JSON message. Chunk size 16384.
- Client -> server, single frame: 64 KB OK; **>= 100 KB single text frame closes the socket with code 1002** (protocol error), no JSON error. 64 KB-100 KB boundary not pinned down.
- Client -> server, SDK-style (count frame, then 16 KB chunks): works at 1, 3, 9, 10 MB (ack `ok`). At 11 MB and 16 MB the server returns the generic `t:"e"` control frame and the socket stays open; the data is not written.
- Single 1-11 MB frames: closed with 1002 / 1006.

### 1.10 Ordering, ack timing
See section 2.

## 2. Two-client fan-out (A writes, B listens, both listen on `/f`)
| Write | B receives | A receives |
|---|---|---|
| `p /f {x:1,y:{z:2}}` | `d` full value, ~1 ms after A's ack time | `d` event then ack |
| `p /f/y/z 3` | `{"p":"f/y/z","d":3}` | same event, then ack |
| `m /f {x:null,w:9}` | `"a":"m"` with `{x:null,w:9}` (diff, not full) | same, then ack |
| concurrent: A `p /f/c fromA`, B `p /f/c fromB` | order: fromB, then fromA, then own ack | order identical: fromB, fromA |
- Events carry exactly what was written (path + written value or merge map), not a diff against the previous value, not a snapshot of the listened node. No `ts` or sequence id on pushes.
- On every connection that has a matching listener, events are delivered in one global order (A and B saw `fromB, fromA`); the writer sees its own event first, then its ack. Last writer wins (`/f/c` ended `fromA`).
- Event for a listener is emitted at or before the writer's ack; A's ack and B's event within the same millisecond window in these runs (ack-before-fanout cannot be ruled out under load).

## 3. REST
Base `http://127.0.0.1:9400/<path>.json?ns=demo-test`. All responses: `Content-Type: application/json; charset=utf-8`, `Cache-Control: no-cache`, `Access-Control-Allow-Origin: *`, `X-Firebase-Project-Id`, `X-Firebase-Project-Number: 123456789`, `X-Firebase-Uuid`. `ns` absent -> project id `fake-server`, data still separate from `demo-test`.

| Request | Response |
|---|---|
| `PUT /r {"a":1,"b":{"c":"x","d":[1,2,3]},"n":1.0,"f":1.5,"big":12345678901234567890}` | 200 echo, keys **sorted**: `{"a":1,"b":{"c":"x","d":[1,2,3]},"big":1.2345678901234567E19,"f":1.5,"n":1.0}` |
| `GET` | same body; `print=pretty` indents with `"a" : 1` (space before colon) |
| `GET ?shallow=true` | `{"n":true,"big":true,...}` (values replaced by `true`, key order not sorted) |
| `GET` missing | 200 `null` |
| `PUT ... print=silent` | 204 empty |
| `PATCH {"a":null,"p":{"q":1}}` | 200 echoes request `{"a":null,"p":{"q":1}}` |
| `POST /posts` | 200 `{"name":"-P4On0N3Mie2S8cJhYcP"}`; ids are 20 chars, leading `-`, sorted ascending by time |
| `DELETE` | 200 `null` |
| invalid JSON body | 400 `{"error" : "Invalid data; couldn't parse JSON object, array, or value."}` (pretty-printed JSON with space before colon) |
| `GET ?format=export` | same but keeps `.priority`/`.value` |
| arrays | `PUT ["a","b","c"]` -> `["a","b","c"]`; `{"0":"a","2":"c"}` -> `["a",null,"c"]`; `{"0":"a","1":"b"}` -> `["a","b"]` (array rule applied on read when keys are integer-like and dense enough) |
| `PUT {}` | 200 `null`, node deleted; root listing no longer has it |
| numbers | stored as sent: `1` stays `1`, `1.0` stays `1.0`, `1e3` -> `1000.0`, `-0.0` kept, 20-digit int -> `1.2345678901234567E19` (Java double style) |
| `GET /.json?shallow=true` | root keys, unordered |
| `orderBy="$key"&limitToLast=2` | works without index: `{"c":..,"d":..}` |
| `orderBy="s"&limitToFirst=2` without index | 400 `{"error" : "Index not defined, add \".indexOn\": \"s\", for path \"/items\", to the rules"}` (REST *errors*, WS only warns) |
| `limitToFirst` without orderBy | 400 `orderBy must be defined when other query parameters are defined` |
| `orderBy=s` (unquoted) | 400 `orderBy must be a valid JSON encoded path` |
| `orderBy="$value"` no index | 400 `Index not defined, add ".indexOn": ".value", ...` |
| `orderBy="$priority"` | 200 (no index needed) |
| `auth=bad` token with open rules | 200 (ignored) |
| `OPTIONS` | 200, `Allow: OPTIONS,GET,POST,PUT,DELETE,PATCH` |
| `HEAD` | 405 `{"error" : "Unsupported request type"}` |
| missing `.json` suffix | 400 text/plain `Append .json to your URL to use the REST API.` |
| unknown dot path (`/.inspect`, `/.status`, `/.reset`, `/.info`) | 404 text/plain `not found` |

### ETag / if-match
- `GET` with `X-Firebase-ETag: true` -> response header `ETag: 8n5kEPKa77xiDo6xsHQrcom3mJ4=` (base64 SHA-1-like 28 chars) and `Access-Control-Expose-Headers: ETag`. Same value the WS accepts as `h`.
- `PUT` with correct `if-match` -> 200 with new data; replay with the old ETag -> **412 Precondition Failed**, body is the **current value** (JSON), headers carry the **current ETag**.
- Wrong etag on `DELETE` -> 412 same shape. `if-match: null_etag` on a missing node -> succeeds (200), on existing node -> 412.

### Streaming (`Accept: text/event-stream`)
```
event: put\ndata: {"path":"/","data":null}        (initial, even for empty node)
event: put\ndata: {"path":"/","data":{"a":1}}      (PUT /sse)
event: put\ndata: {"path":"/b","data":2}           (PUT /sse/b)
event: patch\ndata: {"path":"/","data":{"a":null,"c":3}}   (PATCH)
event: put\ndata: {"path":"/","data":null}         (DELETE)
event: keep-alive\ndata: null                      (every 30 s: observed at t+30, +60, +90, +120 exactly)
```
Each event followed by a blank line. No `auth_revoked`/`cancel` event was triggered in these runs.

### Error shapes
| Condition | Status | Body |
|---|---|---|
| key with `. # $ [ ] /` or empty key in body | 400 | `{"error" : "Invalid data; couldn't parse key beginning at 1:2. Key value can't be empty or contain $ # [ ] / or ."}` (position = line:col of key) |
| control char in key (`\u0001`) | 400 | same message |
| `.` or `$` in URL path (`/a.b.json`, `/a%24b.json`) | 400 | `{"error" : "Invalid path: Invalid token in path"}` |
| PATCH with non-object | 400 | `Invalid data; couldn't parse JSON object. Are you sending a JSON object with valid key names?` |
| PATCH `{"x/y":5}` | 200 `{"x":{"y":5}}` (slash in PATCH key = deep path allowed); `{"x/y":5,"x":1}` -> 400 same message as above |
| path deeper than 32 | 400 `Invalid path: Path specified exceeds the maximum depth that can be written (32).` (path of 31 segments OK, 32 rejected) |
| data nested deeper than 31 | 400 `Invalid data; couldn't parse JSON object, array, or value.` (30 and 31 levels OK, 32 rejected) |
| body > ~10 MB (REST) | 10 MB OK (204), 11 MB 400 `Invalid data; couldn't parse JSON object, array, or value.` (generic message, no "too large") |
| permission denied (rules) | **401** `{"error" : "Permission denied"}` (no trailing dot) |
| rules endpoint without owner token | **403** `{"error" : "Permission denied."}` (trailing dot) |
| bad rules | 400 `{"error" : "1:30: Unexpected end of expression\n"}` |

## 4. Rules
- Load: `PUT /.settings/rules.json?ns=demo-test` with `Authorization: Bearer owner`, body `{"rules":{...}}` (plain JSON; the `rules` wrapper is required for the GET to round-trip) -> 200 `{"status":"ok"}`. Syntax errors in expressions rejected at load with the 400 above. `GET` same path returns the pretty-printed document (4-space indent). Without bearer: 403. Per-namespace.
- Admin bypass: `Authorization: Bearer owner` header works for data REST (reads/writes under locked rules succeed). `?access_token=owner` works; `?auth=owner` does NOT (401).
- Locked rules `{".read":false,".write":false}`:
  - REST GET/PUT/PATCH/POST/DELETE -> 401 `{"error" : "Permission denied"}`.
  - WS `q`, `p`, `m`, `o` -> `{"s":"permission_denied","d":"Permission denied"}` with the request's `r`; socket stays open. After a valid `auth`, still denied on `/l`.
- `.write: "auth != null"`: unauthenticated REST PUT 401; with `?auth=<alg:none jwt>` 200. WS before auth: `permission_denied`; after auth: allowed.
- `$uid` wildcard `.read: "auth != null && auth.uid === $uid"`: own path 200 (null), other uid 401. WS listen on own path before auth -> `permission_denied`; after `auth` -> data.
- `.validate` (`newData.isString() && newData.val().length < 5`): violating write -> 401 / WS `permission_denied` / `Permission denied` (same as rule denial; **validation failure is indistinguishable from permission denial**). Unknown child under `"$other": {".validate": false}` -> denied. Whole-object PUT `{"name":"al","age":3}` ok; with extra key `x` denied; PATCH with invalid child denied.
- `data`-based write rule (`!data.exists() || data.child("o").val() === auth.uid`): first write ok, second user denied, owner update ok.
- `.read` on a parent grants children; rules evaluated per request, not retroactively on existing listens (see 1.8); `unauth` cancels listens that are no longer allowed with `a:"c"`.
- Server pushes events only to listeners whose read rule passed at listen time (read rule on listened path).
- Rule `auth` object for the WS token: `uid`, `token` (claims), `provider:"anonymous"`.

## 5. Limits / edges (summary)
| Item | Observed |
|---|---|
| forbidden key chars `. # $ [ ] /` and empty key | rejected (REST 400, WS `t:"e"` control frame without `r` reply) |
| max path depth | 32 segments rejected; 31 OK |
| max data nesting | 31 levels OK; 32 rejected |
| REST body size | 10 MB OK; 11 MB rejected (400 generic) |
| WS single text frame | >= 100 KB closes socket with 1002 |
| WS chunked message | up to 10 MB OK; 11 MB -> `t:"e"` and not written |
| empty object / `null` | delete the node |
| arrays | stored as objects with keys "0","1"; REST renders as array when dense; WS renders as object |
| numbers | emulator preserves `1` vs `1.0` in REST; WS listener saw `1` for a `1.0`-ish write sent as JSON `1` |
| push id | `-` + 19 chars, e.g. `-P4On0N3Mie2S8cJhYcP` |

## 6. Emulator-only / unusual endpoints (from outside)
- `PUT/GET /.settings/rules.json?ns=` (owner bearer). Not on production RTDB (production uses management API).
- `GET /` -> 400 text `Append .json to your URL to use the REST API.`
- `DELETE /.json?ns=demo-test` wipes the namespace when rules allow (used by the Firebase CLI test flow).
- `X-Firebase-Project-Id` response header derived from `ns`; `X-Firebase-Project-Number` always `123456789`; project id `fake-server` when `ns` absent.
- Namespaces auto-created on first use (`other-ns` worked), each with its own rules.
- No `/.inspect`, `/.status`, `/.reset`, `/.info` (404).

## Behaviours Aruma must match (inference)
1. Handshake frame shape (`ts`,`v`,`h`,`s`) and `h` echoing the request Host; 32-char session id.
2. Envelope + action set: `s`,`q`,`n`,`p`,`m`,`o`,`om`,`oc`,`auth`,`unauth`,`gauth`,`appcheck`; `0` keepalive from client; response `{r,b:{s,d}}`; statuses `ok`, `datastale`, `permission_denied`, `invalid_token`, `invalid_parameters`.
3. Event ordering: pushes to the writer's own listeners precede its ack; listeners receive writes in a single order consistent with the final value; merges are pushed as `m` with only written keys.
4. Listen: no data push for null; data push precedes ack; ack `d` carries `{w:[...]}` warnings (`no_index`), tagged pushes (`t`) for indexed queries, and no explicit eviction events.
5. Conditional put via `h` = hash equal to REST ETag; `datastale` / HTTP 412 with current value and ETag.
6. Server frame splitting at 16384-char chunks with count frame; accept client chunked frames up to the size limit; large single-frame rejection.
7. Cancel frame `a:"c"` (path, `q:null`) when a listen becomes unauthorized (also the natural mechanism for disabled/revoked users), and `a:"d"/"m"` with no leading slash paths.
8. Error semantics: REST 401 `Permission denied` for rules, 400 JSON `error` strings above for bad data/paths, validation indistinguishable from permission denial.
9. Server values (`timestamp`, `increment`) resolved server-side; onDisconnect applied once on close with server values resolved at that time and cancelled by `oc`.
10. Key/path/depth rules: forbidden chars, 32-deep limits, 10 MB-ish node write, null/empty-object deletion, dense-integer-key arrays on REST.
11. SSE `put`/`patch`/`keep-alive` (30 s), initial `put` of `/`; `print=silent` 204; `shallow`; `orderBy` requiring JSON-quoted path and `.indexOn` for non-key/priority/value orders (REST errors, WS warns).

## Behaviours that look emulator-specific (inference)
- `owner` bearer and `?access_token=owner`; rules loaded via `/.settings/rules.json`; `X-Firebase-Project-Number: 123456789`; `fake-server` project; auto-created namespaces.
- `auth` accepts any `alg:none` / unsigned JWT; `provider` fixed to `anonymous`; `gauth` accepts anything; `appcheck` rejects everything.
- WS: untagged unfiltered result for a limit query without index (emulator ignores limit when no index); production behaviour for that case is *not verified* here.
- Number preservation `1.0` vs `1` and Java-style `1.2345678901234567E19`; `.priority` as `7.0`; REST key sort in output; `shallow` key order not sorted.
- WS single-frame close 1002 at ~64-100 KB; 120 s idle close; generic `ClientId[n]:ErrorId[m]: Error on incoming message` control frames with no `r` reply; 10 MB write cap with a generic message (production has documented limits of 16 MB REST / 256 MB SDK writes, unverified here).
- Rules not re-evaluated on existing listens; production behaviour not verified.
- Process left clean: emulator (PID started on 127.0.0.1:9400) stopped; no listeners remain on 9400-9420.
