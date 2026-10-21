# Production reference captures

Frames and REST exchanges recorded from **production Firebase Realtime Database** on 2026-10-21, with the owner's permission, against one of the owner's projects. The project id, uid, email and display name are redacted (`<project>`, `<uid>`, `<email>`, `<name>`), and so are ID tokens (`<id-token>`, `<expired-id-token>`, `<alg-none-token-for-the-same-uid>`). The instance is in `europe-west1` (`*.firebasedatabase.app`).

Two runs, each writing only under `/aruma-ref/<run>/` and removing it afterwards (each file names its `run`):

- `r7-20261021-c1add4`, 08:27-08:43 UTC: `handshake`, `auth`, `denied`, `no_index`, `oversize`, `numbers`, `lp`, `cleanup`. Some connections of this run failed before the hello (see below), so a few scenarios ran again; `auth.json` reads data an earlier attempt left under the run root.
- `r7-20261021-adfc21`, 09:13-09:15 UTC: `rest_auth`, `rest` (recaptured on a clean root), `cancel` (with more query shapes), and `auth_expired`, with an ID token that had expired at 09:13:40.

- `capture_production.mjs` records these scenarios from production, the emulator 4.11.2 or Aruma, from a config file (see its header). The format is that of the emulator goldens one directory up, plus a `ms` offset per frame; REST calls are `rest` frames with the response headers that matter.
- `sdk_production.mjs` runs the same cases through the official JS SDK (firebase 13.0.0 in Node) and records values, error codes and messages, and the SDK's warnings: `sdk.json` (first run).
- The signed-in user is a password user of the project. The rules were the project's own plus, for the length of each run, this block; they were saved before and restored byte for byte after each run:

```
"aruma-ref": {"$run": {"open": {".read": "auth != null && auth.uid === '<uid>'",
                                ".write": "auth != null && auth.uid === '<uid>'",
                                "v": {".validate": "newData.isNumber()"}},
                       "public": {".read": true}}}
```

(`public` only in the second run.) Some connections from the capture machine to production failed before the hello or timed out (`connection attempt N failed`, in the notes); the script connects again, and REST calls that got no response are sent again. Run it with `node --dns-result-order=ipv6first` if that happens often.

Checked by `TestProductionGoldenServerFrames` and `TestProductionCancelledQueries` (this package: every frame production sent is one this package encodes, and a cancelled listen is named as production names it) and `TestRTDBRESTMatchesProduction` (test/acceptance: the REST answers replayed against Aruma). The comparison with the emulator and Aruma is in `docs/compat/rtdb-v0.2.md` ("الإنتاج والمحاكي").
