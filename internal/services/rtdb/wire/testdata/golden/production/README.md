# Production reference captures

Frames and REST exchanges recorded from **production Firebase Realtime Database** on 2026-10-21, with the owner's permission, against one of the owner's projects (the project id, uid, email and display name are redacted: `<project>`, `<uid>`, `<email>`, `<name>`; ID tokens are `<id-token>`). The instance is in `europe-west1` (`*.firebasedatabase.app`). Every write went under `/aruma-ref/<run>/open`, run `r7-20261021-c1add4`, and was removed afterwards.

- `capture_production.mjs` records the scenarios (`handshake`, `auth`, `denied`, `cancel`, `no_index`, `oversize`, `rest`, `numbers`, `lp`, `cleanup`, and `auth_expired` when given an expired token) from production, the emulator 4.11.2 or Aruma, from a config file (see its header). The same file format as the emulator goldens one directory up, plus a `ms` offset per frame; REST calls are `rest` frames with the response headers that matter.
- `sdk_production.mjs` runs the same cases through the official JS SDK (firebase 13.0.0 in Node) and records values, error codes and messages, and the SDK's warnings: `sdk.json`.
- The signed-in user is a password user of the project; the rules were the project's own plus, for the length of the run, this block, restored byte for byte afterwards:

```
"aruma-ref": {"$run": {"open": {".read": "auth != null && auth.uid === '<uid>'",
                                ".write": "auth != null && auth.uid === '<uid>'",
                                "v": {".validate": "newData.isNumber()"}}}}
```

Some connections from the capture machine to production failed before the hello or timed out (`connection attempt N failed`, in the notes); the script connects again, and REST calls that got no response are sent again. Run it with `node --dns-result-order=ipv6first` if that happens often.

`TestProductionGoldens` (wire) checks that these files carry production provenance and that every frame production sent decodes and re-encodes field for field. The comparison with the emulator and Aruma is in `docs/compat/rtdb-v0.2.md` ("الإنتاج والمحاكي").
