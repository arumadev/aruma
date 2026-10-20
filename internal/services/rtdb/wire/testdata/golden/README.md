# RTDB wire golden frames

Raw WebSocket text frames (protocol v5) captured from the official Firebase Realtime Database emulator, `firebase-database-emulator-v4.11.2.jar`, on 2026-10-20. One JSON file per scenario; `frames` lists every frame in both directions in the order the client saw it (`send`, `recv`, `close`, and `http` for the raw upgrade responses in `origin.json`). Frames longer than 1000 chars are stored as `len` + `sha256`. `notes` records missing replies, who closed each socket, and what was observed.

Values such as `ts`, session ids `s`, `X-Firebase-Uuid` and server timestamps change on every run; compare structure, not those values.

Regenerate (Node >= 22, ports 9400-9419):

```
java -jar ~/.cache/firebase/emulators/firebase-database-emulator-v4.11.2.jar --host 127.0.0.1 --port 9400
node capture.mjs 127.0.0.1:9400
```

`SHA256SUMS` pins every `*.json` file and `capture.mjs` (check with `shasum -a 256 -c SHA256SUMS`).

`capture_r3.mjs` records the phase R3 scenarios the same way (listens, server-side queries, listen revocation and, as `lp_*.json`, long polling over `/.lp`). Each scenario runs in its own namespace, whose rules and data it resets through REST first and lists under `setup`; long-poll HTTP exchanges are recorded as `http_req`/`http_resp`/`http_abort` frames. Regenerate with `node capture_r3.mjs 127.0.0.1:9400`, or `SCENARIOS=query_get,lp_upgrade node capture_r3.mjs 127.0.0.1:9400` for some of them.
