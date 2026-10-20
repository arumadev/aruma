# F. databaseURL shape, per-SDK parsing, TLS/DNS operations (Decision Q1)

Date 2026-10-20. All repo refs are file:line at the recorded short commit. Licenses: all Firebase SDK repos Apache-2.0
(read LICENSE headers); Lark is AGPL-3.0 (read for ideas only, nothing copied); CertMagic Apache-2.0.

Commits: firebase-js-sdk d7bb0e1 | firebase-android-sdk dcc8d0e | firebase-ios-sdk dc5821f | firebase-admin-go 66a72c3 |
firebase-admin-python de45607 | firebase-admin-java 2225373 | firebase-admin-node 08a3963 | firebase-admin-dotnet beff1f9 |
firebase-cpp-sdk 08af16d | flutterfire 14363dd | lark c909918 (all under .cache/research/src/).

## 1. How each SDK parses databaseURL (verified)

### Web JS (also Node Admin, Flutter web)
`packages/database/src/core/util/libs/parser.ts` (d7bb0e1)
- L129-151 scheme, host, path, query split. Host = text up to first `/` or `?`.
- L154-157 a port in host => `secure = scheme is https|wss`; no port => always secure.
- L163-174: host `localhost` => namespace empty; host with <= 2 dot-labels => no namespace from host;
  **3+ labels => namespace = first label (lowercased)**.
- L176-178: `if ('ns' in queryParams) namespace = queryParams['ns']` -> **`?ns=` always wins, any label count**.
- L98: `includeNamespaceInQueryParams = namespace !== subdomain`.
- Wire: `core/RepoInfo.ts` L103-108 `repoInfoNeedsQueryParam` true if internalHost differs, **`isCustomHost()` (L66: domain is not firebaseio.com / firebaseio-demo.com)** or the flag above; L139 `params['ns'] = namespace`; L129 `wss://<internalHost>/.ws?` . So on ANY custom host the JS SDK sends `ns=` on /.ws and /.lp. Host header = host[:port] as typed (browser/ws library; inference for header).
- Default when no URL: `api/Database.ts:135` `${projectId}-default-rtdb.firebaseio.com`.
- Emulator: `api/Database.ts:148-150` env host => `http://${host}?ns=${namespace-from-original-URL}`; `connectDatabaseEmulator(db, host, port)` (Database.ts ~L350-390) keeps namespace from databaseURL.
- validateUrl (`core/util/validation.ts:385-404`): namespace must be a valid key (hyphens fine) except host localhost; URL must have no child path.
- `WebSocketConnection.ts:182` Node: `Authorization: Bearer` header on the upgrade for Admin; `:45` keepalive 45 s.

### Android client
`firebase-database/src/main/java/.../core/utilities/Utilities.java` L39-86 (dcc8d0e)
- L53-57 `ns` query param, else first host label lowercased (any label count). L61-67 port => secure only for https/wss, host gets `:port`; no port => secure.
- `core/RepoInfo.java` L48-64: always `<scheme>://<internalHost>/.ws?ns=<ns>&v=5`. L66-74 `applyEmulatorSettings` replaces host/port, secure=false, **keeps namespace**.
- => `?ns=` works on every host shape. First label is the fallback.

### iOS client (the odd one)
`FirebaseDatabase/Sources/Utilities/FUtilities.m` L147-217 (dc5821f). The exact condition, L183-199:
```
NSArray *parts = [urlComponents.host componentsSeparatedByString:@"."];
if ([parts count] == 3) {  namespace = [parts[0] lowercaseString];   // L184-185: ns param NOT looked at
} else { ...ns query item...; if (!namespace) namespace = [parts[0] lowercaseString]; }
```
- Exactly 3 labels => first label, `?ns=` ignored. Any other count (1, 2, 4, 5...) => `?ns=` if present, else first label.
- No scheme => `http://` is prepended (L149-151). Port => secure only for https/wss (L175-181).
- Wire `Core/FRepoInfo.m` L181: `<scheme>://<internalHost>/.ws?v=5&ns=<ns>` always sent. Emulator `Api/FIRDatabase.m` L179-195 keeps namespace.
- `127.0.0.1` has 4 parts -> ns param honoured; `localhost` 1 part -> honoured.

### Admin Java
`src/main/java/.../database/utilities/Utilities.java` L45-91 (2225373): same as Android (ns param, else first label; port kept; secure = https|wss). WebSocket `core/RepoInfo.java` L48-64 `/.ws?ns=...&v=5`. `FirebaseDatabase.java` L139-147: **URL with a path is rejected** ("should point to the root"). Emulator: `util/EmulatorHelper.java` L33-70 builds `http://<host><path>?ns=<name>` from FIREBASE_DATABASE_EMULATOR_HOST.

### Admin Node
`src/database/database.ts` L120-124: `require('@firebase/database-compat/standalone')` => the same JS parser/wire as above (WebSocket). Separate rules client (L203-210, L325-331 `extractNamespace`: `ns` param else first label) does `GET <url>/.settings/rules.json` over REST.

### Admin Go (REST only, no WebSocket)
`db/db.go` (66a72c3)
- L168-193 `parseURLConfig`: if scheme != https -> emulator parse; else if env FIREBASE_DATABASE_EMULATOR_HOST -> emulator; else **production: `BaseURL = dbURL` verbatim, Namespace = ""** (L190).
- L125 `req.URL = fmt.Sprintf("%s%s.json", BaseURL, path)`; ns is added as `?ns=` query only when Namespace != "" (L128-130), i.e. only emulator mode.
- Consequence: https URL **with `?ns=`/path/trailing slash breaks** (`https://h/?ns=x` + `/a/b.json` => `https://h/?ns=x/a/b.json`; trailing slash gives `//a/b.json`, server tolerance unverified). Namespace on https must come from the Host header only.
- L195-218 `parseEmulatorHost`: **rejects anything containing `//`** (L196) so `http://localhost:9000/?ns=x` fails; accepted forms are bare `host:port?ns=x`, or `host.with.dots:port` (ns = first dotted label, test `db_test.go:104`). Tested with Go 1.27 `url.ParseRequestURI`: `localhost:9000?ns=x` parses, **`127.0.0.1:9000?ns=x` errors** ("invalid URI for request"), `chat-default-rtdb.localhost:9000` parses (scheme-like prefix).
- Env var form also expects `host:port?ns=` (db_test.go:39,106).

### Admin Python (REST + SSE, no WebSocket)
`firebase_admin/db.py` (de45607)
- L829-842: scheme != https => emulator mode requires `http` and exactly one non-empty `?ns=` (L844-852); base_url=`http://netloc`, param `ns` added (L808-809).
- L813 production: **`base_url = f'https://{parsed_url.netloc}'` — path and `?ns=` are dropped, no ns param sent.** Namespace must come from Host.
- Env var emulator (L835-837): ns = first label of the databaseURL host, base `http://<env host>`.

### Admin .NET
No Realtime Database support: `FirebaseAdmin/FirebaseAdmin/` contains only Auth and Messaging (beff1f9). Out of scope.

### Flutter `firebase_database`
Passes the URL string to native: `ios/.../FLTFirebaseDatabaseUtils.swift:39-42` `Database.database(app:url:)`, Android plugin `FirebasePluginDatabase...kt:75` reads DATABASE_URL, web uses JS interop (`firebase_database_web.dart`). `lib/src/firebase_database.dart:12-13` strips one trailing `/`. => behaves exactly like the underlying iOS / Android / JS SDK.

### Unity / C++ (desktop)
`database/src/desktop/util_desktop.cc` `ParseUrl::Parse` L269-363 (08af16d), used by `core/repo.cc:98-104`:
- Scheme only `http` (insecure) or `https`/none (secure); hostname = up to first `/`; chars limited to alnum, `-`, `.`, `:digits` (L341-343) so a `?` directly after the host is an **error**.
- **Namespace = first dot-separated segment of the host (L330-332). There is no `?ns=` support and no emulator API** (grep for emulator: no hits in database/src/{desktop,common,include}). Host `localhost:9000` => ns `localhost`; `127.0.0.1:9000` => ns `127`.
- Wire `connection/host_info.cc:73`: `<ws|wss>://<host>/.ws?ns=<ns>&v=5`.
- On mobile/Mac the C++/Unity SDK wraps the Android/iOS SDKs (inference); Unity wrapping the C++ SDK (inference, firebase-unity-sdk not cloned).

### What reaches the server
| SDK | Transport | `ns=` on wire | Namespace source on server |
|---|---|---|---|
| JS / Node Admin / Flutter web | WS `/.ws` (+`/.lp`) | yes on custom hosts | `ns` param |
| Android / iOS / Java Admin / C++ | WS | always | `ns` param |
| Go Admin (https) | REST `/path.json` | **no** | **Host header only** |
| Python Admin (https) | REST + SSE | **no** | **Host header only** |
| Go / Python emulator mode (http) | REST | yes (`?ns=`) | `ns` param |

## 2. What Firebase itself does
- Default `<project>-default-rtdb.firebaseio.com` (3 labels, first label = ns; JS `Database.ts:135`).
- Regional: https://firebase.google.com/docs/database/locations table: `DATABASE_NAME.firebaseio.com` (us-central1), `DATABASE_NAME.europe-west1.firebasedatabase.app`, `DATABASE_NAME.asia-southeast1.firebasedatabase.app` (4 labels). No SDK special-cases the region form: it is just a 4-label host whose first label is the ns (JS parser L169-173, Android L53-57, iOS else-branch L186-199 falls to first label, Java L73-74). So Firebase's own regional URLs prove the 4-label first-label scheme works on all SDKs. (The firebaseio.com special case is only `isCustomHost`, JS RepoInfo.ts:66 / Android RepoInfo.java:88.)

## 3. Matrix (ns = resulting namespace; OK / BREAK; "chat" = `chat-default-rtdb`)

| URL | JS / Node / Flutter-web | Android / Java / Flutter-Android | iOS / Flutter-iOS | Go Admin | Python Admin | C++ / Unity desktop |
|---|---|---|---|---|---|---|
| U1 `https://chat.rtdb.example.com` (4 labels) | chat OK | chat OK | chat OK (4 parts, no ns -> first label) | chat OK (Host) | chat OK (Host) | chat OK |
| U2 `https://chat.example.com` (3 labels) | chat OK | chat OK | chat OK | chat OK (Host) | chat OK (Host) | chat OK |
| U3 `https://aruma.example.com/?ns=chat` | chat OK (ns wins) | chat OK | **aruma BREAK** (3 parts, ns ignored) | **BREAK** (URL concat) | **BREAK** (ns dropped, Host=aruma) | **aruma BREAK** (path `?ns=`) |
| U4 `https://db.aruma.example.com/?ns=chat` | chat OK | chat OK | chat OK (4 parts) | **BREAK** | **BREAK** (ns dropped; Host=db.*) | **db BREAK** |
| U5 `https://example.com/?ns=chat` (2 labels) | chat OK | chat OK | chat OK (2 parts) | **BREAK** | **BREAK** | **example BREAK** |
| U6 `http://127.0.0.1:9000/?ns=chat` | chat OK | chat OK (secure=false from http+port) | chat OK (4 parts) | **BREAK** (`//` rejected; also IP not parseable bare) | chat OK (emulator branch) | **127 BREAK** |
| U7 `http://localhost:9000/?ns=chat` | chat OK | chat OK | chat OK (1 part) | **BREAK** (`//`); `localhost:9000?ns=chat` OK | chat OK | **localhost BREAK** |

Only U1/U2 (namespace in the first host label) are correct on every SDK. Every `?ns=` shape fails on at least iOS (3 labels), Go, Python and C++/Unity desktop. (`wss`/`ws` schemes and path prefixes were not matrixed: Java Admin and JS reject a child path in databaseURL, `Database.ts:~165`, `FirebaseDatabase.java:139`, so a path prefix is not viable.)

Option (c) findings: port-per-database works for no SDK as namespace carrier (no SDK reads the port as ns); path prefix rejected (above); `ns` from a subdomain of the Aruma host IS option (a)/(a').

## 4. Operations of host-per-database

### Certificates
- Wildcard needs DNS-01: LE FAQ https://letsencrypt.org/docs/faq/ "Wildcard issuance must use the DNS-01 challenge."; challenge types https://letsencrypt.org/docs/challenge-types/ : HTTP-01 "cannot be used to issue wildcard certificates", TLS-ALPN-01 "cannot be used to validate wildcard domains"; DNS-01 needs a DNS provider API for automation, and CNAME/NS delegation of `_acme-challenge` to another zone is allowed (limits the stored credential's blast radius).
- Wildcard covers exactly one label: `*.rtdb.example.com` covers `chat.rtdb.example.com`, not deeper (standard RFC 6125 behaviour; Caddy docs say "only the left-most label is a wildcard", https://caddyserver.com/docs/automatic-https).
- Let's Encrypt limits (https://letsencrypt.org/docs/rate-limits/, fetched 2026-10-20): 50 new certificates per registered domain per 7 days (refill 1 / 202 min); 5 per exact identifier set per 7 days; 300 new orders per account per 3 h; 5 authorization failures per identifier per account per hour. Subdomains share the registered-domain bucket. ARI renewals exempt from all; non-ARI renewals exempt from the order and per-registered-domain limits. => per-name certs cap at about 50 new databases per week per registered domain; a wildcard is 1 cert for all.
- On-demand TLS, Caddy (https://caddyserver.com/docs/automatic-https, /caddyfile/options): cert obtained on first handshake; "must be both enabled and restricted"; `ask` endpoint: a 2xx response authorises issuance, otherwise the handshake fails. Needs port 80/443 reachable, wildcard DNS A record still required for the names to resolve. Caddy DNS-provider support is separate community modules (custom build); not verified here which are in the stock binary.
- CertMagic (https://github.com/caddyserver/certmagic): Apache-2.0; embeddable in a Go binary; `OnDemand` config with `DecisionFunc(name) error` (our `ask` equivalent) / allowlist; default throttle 10 obtain/renew per minute; HTTP-01 and TLS-ALPN-01 per-name, DNS-01 via libdns providers is the only wildcard route; failures retried with backoff up to 30 days.

### DNS / Cloudflare
- Wildcard record is one `*.D` A/AAAA record. Cloudflare: "Customers on all plans can create and proxy wildcard DNS records" (https://developers.cloudflare.com/dns/manage-dns-records/reference/wildcard-dns-records/); exact records override the wildcard for that name and below.
- **Cloudflare Universal SSL covers only the apex and first-level subdomains** on a full-setup zone: `a.b.example.com` is "not covered" (https://developers.cloudflare.com/ssl/edge-certificates/universal-ssl/limitations/); deeper needs Advanced Certificate Manager (paid) + Total TLS or an uploaded cert. So option (a) 4 labels behind the Cloudflare proxy on free plan has no edge cert; the 3-label form (a') is covered by free Universal SSL. Grey-cloud (DNS only) + own cert avoids this.
- Cloudflare WebSockets on all plans, idle timeout not documented on the page; use heartbeats (https://developers.cloudflare.com/network/websockets/). JS SDK already pings every 45 s (`WebSocketConnection.ts:45`).

### Peers
- Lark (AGPL, ideas only): `<project>.<domain>` = default DB, `<database>--<project>.<domain>` = named DB, double hyphen; `docs/DEPLOYMENT.md` "DNS: clients connect to a per-database hostname" (L~86-98) requires wildcard `*.<LARKDB_DOMAIN>` A/AAAA and a wildcard cert from CertMagic via DNS-01 with a Cloudflare API token (or supply `TLS_CERT_FILE`/`TLS_KEY_FILE`). Host parsing `edge/proxy/larkdb.go:~749-773` (split at first `--`). Docs do NOT state why `--`; **inference**: keeps the database in the same single label so one `*.domain` DNS record and one wildcard cert cover everything, and the first label stays a valid RTDB namespace for SDKs that read only the first label. Lark does not use `?ns=` in its hostname scheme.
- Supabase: `https://<project_ref>.supabase.co` (https://supabase.com/docs/guides/api) — 3 labels, first label = project.
- PocketBase / Appwrite: one host (Appwrite: project id in request header; page fetched did not confirm — unverified, not needed for the decision).

## 5. Option (a') — first label is the namespace, 3 labels
`https://chat-default-rtdb.example.com`: matrix row U2, correct on every SDK incl. iOS and Go/Python.
Costs: wildcard `*.example.com` on the registered domain (or on a dedicated domain); namespace names compete with other hostnames at that level (www, aruma, mail, api): needs a reserved-label list, and exact DNS records override the wildcard so existing hosts keep working. Benefits: free Cloudflare Universal SSL covers it; one wildcard cert `*.example.com` can also cover the Aruma main host. Generalisation: the server can take a configured RTDB base domain D (any depth); `D = rtdb.example.com` gives (a), `D = example.com` gives (a'). Both satisfy the SDK rule as long as the database label is the FIRST label.

## 6. Recommendation (inference) and operator steps
Namespace must be carried by the first host label; `?ns=` cannot be the primary mechanism (fails iOS-3-label, Go, Python, C++/Unity). Implement:
1. Config `rtdb_domain` D. Host `<ns>.<D>`, with ns = `<projectId>-default-rtdb` (later `<name>` for extra databases; RTDB names are hyphen-safe single labels). Server derives ns from the Host header first, accepts `?ns=` too (needed for all SDKs when SDKs are pointed at plain `http://127.0.0.1:port`, and for emulator APIs); if both are present and differ, reject (inference: prevents cross-project confusion).
2. Document two supported D shapes: D = dedicated 2-label domain or a shared one (`example.com` -> `chat-default-rtdb.example.com`, 3 labels, works with Cloudflare free Universal SSL) or D = `rtdb.example.com` (4 labels, needs own cert when proxied by Cloudflare). Recommended default for docs: 3-label.
3. Operator steps: (i) DNS `*.D` A/AAAA (and apex of D) -> Aruma host. (ii) Certificate, choose one: A) wildcard `*.D` via DNS-01 (Caddy with a DNS module, or lego/certbot/acme.sh; narrow API token or `_acme-challenge` CNAME delegation) = 1 cert, no per-project issuance, no rate-limit exposure; B) Caddy on-demand TLS with `ask` -> an Aruma endpoint returning 2xx only for existing project namespaces (no DNS token, port 80/443 open, <= ~50 new databases/week/registered domain); C) behind Cloudflare: proxied wildcard record, 3-label D so Universal SSL applies, plus origin cert. (iii) Put the exact URL in each client config: console shows `databaseURL` as `https://<ns>.<D>`. (iv) Heartbeats shorter than any proxy idle timeout.
4. Aruma binary: terminate TLS in a fronting proxy first (Caddy); optional later embedding CertMagic (Apache-2.0) with a DecisionFunc backed by the project table.
5. Reserve labels (www, aruma, api, mail, admin, console, s-*) so a project cannot shadow other hosts (`s-` prefix is cached by SDKs as a "sticky host", per known facts).

### Local development
- Server on `http://127.0.0.1:9000` (or localhost), no TLS, `?ns=` required: JS, Android, iOS, Java Admin, Python Admin: `http://127.0.0.1:9000/?ns=<project>-default-rtdb` works (matrix U6), or the SDK emulator calls (`connectDatabaseEmulator`, `useEmulator(host, port)`, `FIREBASE_DATABASE_EMULATOR_HOST`) which keep ns from databaseURL while only replacing host/port, forcing insecure. Node Admin env form: JS L148-150.
- Go Admin: use `localhost:9000?ns=<ns>` (no scheme; IP literals fail) or env var in the same form.
- C++/Unity desktop: no ns param, no emulator API: needs a host whose first label is the ns, e.g. `http://chat-default-rtdb.localhost:9000`. On this macOS (Darwin 24.5) `getaddrinfo("chat-default-rtdb.localhost")` FAILED (tested with Python socket), so a hosts-file entry is required there (other OSes unverified). Aruma should also serve the Host-derived path on plain HTTP in dev mode.
- Android emulator reaches host machine via 10.0.2.2 (unverified here); cleartext policy on Android 9+ not verified.

## Unverified / gaps
- Exact Host header strings sent by each SDK's WebSocket library (assumed = URL host[:port]).
- Go Admin tolerance for `//` in paths on the Aruma server (depends on router).
- Firecrawl search was unavailable (out of credits); Lark's reason for `--` is inference.
- Unity's use of firebase-cpp-sdk desktop (inference).
