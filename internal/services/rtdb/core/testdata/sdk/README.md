# RTDB SDK vectors

Hash, ordering and export vectors computed by the official Firebase JS SDK code, `@firebase/database` 1.1.5 (`dist/index.node.cjs.js`), run in Node 24 on 2026-10-20. Source references point to firebase-js-sdk commit `d7bb0e16f2b13ba15f74b690596eb70ef145d4f6` (`packages/database/src`).

- `hash.json`: `nodeFromJSON(json).hash()` plus the canonical pre-SHA-1 string, and `ieee754`: the SDK's `doubleToIEEE754String` against the real IEEE-754 bits. They differ for |v| < 2^-971 and for some values at or next to a power of two (e.g. 2^53-1).
- `order.json`: `nameCompare` (sorted keys, key pairs), `VALUE_INDEX`, `PRIORITY_INDEX` and `PathIndex` comparisons, as signs.
- `export.json`: `val(false)` and `val(true)` (export format) for each input.
- `emulator_etags.json`: REST ETags from the emulator (v4.11.2) for sample values from `hash.json`, compared with the SDK hash. They match except for the numbers where the SDK's IEEE-754 string is wrong; there the emulator hashes the real bits.

Regenerate:

```
node gen.js <path>/node_modules/@firebase/database        # hash.json, order.json, export.json
node emulator_etags.mjs 127.0.0.1:9400                    # needs the emulator on that port
```

`gen.js` compiles the bundle in memory with a hook on `sha1()` and an export of internal functions (it writes nothing into node_modules). If `@firebase/app` is not installed, it uses a stub that the bundle needs only to register itself (`source.appStub`).
