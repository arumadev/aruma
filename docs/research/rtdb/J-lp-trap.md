# J: long-poll trap experiment (2026-10-20)
Setup: /Volumes/Work/aurma-project/.cache/research/lp-trap/ (run.mjs, page/, results.json, run.log). firebase@12.19.0, Chromium (Playwright 1.64.1), official RTDB emulator 4.11.2 behind a node proxy (9431 -> 9430), page served on 9432.
Note: the emulator handshake returns h=localhost:9430; the client then reconnects there, bypassing the proxy. The proxy rewrites that to 127.0.0.1:9431 (same length) in the WS stream. Without this the restart test is invalid (first run was discarded for that reason).
Mode B = /.lp answers 404. Mode A = /.lp forwarded. Restart = proxy closed 10 s, then reopened. Wait limit 120 s.

| # | Scenario | Recovered? | Time | Evidence |
|---|---|---|---|---|
| 1 | B baseline, fresh | yes | 0.26-0.5 s | "Websocket connecting to ws://127.0.0.1:9431/.ws", "Primary connection is healthy"; flag "true" at connect, null once healthy; REST write to /x seen |
| 2 | B restart (10 s down) | NO | not within 120 s | flag "true"; after proxy up, only GET /.lp?start=t... (14 requests, backoff 2 s to 38 s), all 404, zero /.ws upgrades; "Connecting via long-poll", "Longpoll is closing itself" |
| 3 | B reload, same context | NO | not within 120 s | page loaded with flag already "true"; only /.lp requests, no /.ws |
| 4 | A restart | yes | 2.1 s after proxy up (page showed connected true at ~13.4 s) | one /.lp?start=t request, then connected. Flag was still "true" at that instant; upgrade back to WS and flag clearing NOT verified |
| 5a | B + forceWebSockets(), restart | yes | 4.1 s after up | only /.ws retries ("Websocket connecting ... ls=..."), UPGRADE logged at proxy-up+4 s, never /.lp except one stray request |
| 5b | B + forceWebSockets(), flag pre-seeded true, fresh | yes | 0.26 s | direct /.ws, flag cleared |
| 5c | B, no force, flag pre-seeded true, fresh context | NO | not within 60 s | first request GET /.lp?start=t&ser=...; "Long-poll script failed to load" then retry loop; never /.ws |
| 6 | B, localStorage get/setItem throw | NO | not within 30 s | first and only requests are /.lp (404). SDK treats blocked storage as "previous websocket failure"; first connection goes straight to /.lp, no /.ws attempt |

Key log lines: "c:0:2:0 Connecting via long-poll to http://127.0.0.1:9431/.lp?start=t&ser=..." ; "Long-poll script failed to load" ; "Longpoll is closing itself".
Mechanism seen in S2: during downtime the first reconnect tries WebSocket and fails, which sets the flag; every later attempt (also after the server is back) goes to /.lp.
Conclusion: CONFIRMED. A server with no /.lp strands any browser client that failed one WebSocket attempt (restart, flaky network), and strands browsers where localStorage is unavailable from the very first connect. The flag persists across reloads (same origin). Only forceWebSockets() (an app-side opt-in) avoids it. Aruma must implement /.lp (or the plan must document that web apps call forceWebSockets()).
Caveat: Chromium headless only; Node/mobile/other SDKs not tested.
Processes (emulator, proxy, web server, browser) all stopped.
