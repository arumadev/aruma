# Reference for the security rules language (Firestore and Cloud Storage)

This directory is the "ground truth" (oracle) for the Security Rules language that Firestore and Cloud Storage share.
It was built in phase S0-A, before any engine code, so that the S1 builders could test their engine against it. It holds data and documents only:
no Go code, and the recording tools in `tools/` are written in Node.

The findings drawn from it (the limits with their numbers, the table of error semantics, the behavior of the functions, the differences between the emulator and production,
and what the Rules Test API could not express) are in the oracle findings note, which is not kept in this repository.
The differences between the emulator and the Rules Test API, case by case, are in [parity-exceptions.md](parity-exceptions.md).

## The held-out set

Part of the cases is held out from the S1 builders so that it does not enter the fuzz seeds or any tuning, and only the final test
**S1-E** reads it. It is not in this branch at all: it lives in the branch **`oracle-rules-heldout`** under
`test/oracle/rules/heldout/`, and none of its identifiers or content is in this branch's files. It holds:

- about a fifth of our cases recorded through the Rules Test API: every case that is not pinned is held out if the value of
  `sha256("aruma-s0a-rules-heldout-v1:<topic>/<id>")` (the first four bytes divided by 2^32) falls below 0.25
  (`heldOut` in `tools/oracle.mjs`). The "pinned" (`pin`) cases are the ones the findings document cites, so they stay here.
- The held-out hidane conditions (`spikes/rules-eval/cases/rules-heldout.json`) and the emulator results for them, and the
  Rules Test API results for them.
- The case-definition modules themselves (`cases-src/*.mjs`), because they define the held-out cases too. The JSON files here
  are self-contained: every case carries the rule, the request, the resource and the outcome.

The two sets were recorded in separate API requests, so no request, ruleset or response here contains a held-out case.

## Files

```
production/<topic>.json        our cases: what the Rules Test API decided (provenance: rules-test-api)
production/raw/<topic>.json    every request as sent, and its response as received (no credential and no token in them)
emulator/<topic>.json          the same cases on the official emulators (provenance: emulator)
parity-exceptions.md           every case where the emulator differed from the Rules Test API, and the Test API effects
imported/hidane/original/      hidane's files as they are (see below about the results file)
imported/hidane/normalised/    the emulator results for the hidane conditions in the unified schema
imported/pyric/original/       Pyric's observations and the corpus files they were recorded from, as they are
imported/pyric/normalised/     the same observations in the unified schema
tools/                         the recording, import and comparison tools
```

The topics (`<topic>`) in `production/` and `emulator/`:

| Topic | Service | What it covers |
|---|---|---|
| `encoding` | firestore | How the Test API reads JSON: int and float, strings that look like times, `request.time`, `request.auth`, the missing resource |
| `errors` | firestore | How an error propagates through `\|\|`, `&&`, `!`, the ternary, functions and `let`; missing keys, type errors, division by zero, int overflow |
| `limits` | firestore | Function call depth, recursion, the number of `let`s, expression complexity, the limit of a thousand expressions, ruleset size, `get()`/`exists()` |
| `strings` | firestore | `size` and Unicode, `matches` (RE2), `split`, `replace`, `lower`/`upper`, `trim`, indexing, conversions |
| `math` | firestore | int and float, `math.*`, `hashing.*`, bytes, latlng |
| `time` | firestore | timestamp and duration: fields, construction, arithmetic, units |
| `collections` | firestore | list, set, map and `diff` |
| `paths` | firestore | The path type, `$(var)`, match variables, `{x=**}` in rules_version 1 and 2, nested match |
| `methods` | firestore | `read`/`write` against `get`/`list`/`create`/`update`/`delete`, `request.method` |
| `storage` | storage | The fields of `resource` and `request.resource`, claims, the methods, `firestore.get/exists`, language basics |
| `storage-paths` | storage | `{name=**}` in 1 and 2, zero segments, nested match, bucket |
| `followups` | firestore | A second round that settled what the first left ambiguous (crc32, `lower`, `split('')`, `{rest=**}` in v1…) |
| `nesting`, `nesting-storage` | firestore, storage | Nested match under a block whose path ends in `{x=**}` or whose glob is followed by segments, in 1 and 2, and what the glob binds (added after review) |
| `hidane-conditions` | firestore | Each of hidane's conditions through the Test API, to compare with what the emulator decided for it |
| `deployed-firestore` | firestore | **Deployed rules and real requests** (provenance `deployed`, S7-R, 2026-10-24): `debug()`, the counts of `request.auth.token` from a real ID token, `get()`/`exists()` of a missing document, the order of `keys()`/`values()`. The method is in the `method` field, the requests and responses are in `raw/` (no token in them), and there is no file for it in `emulator/` |

## The unified schema `aruma.rules-oracle/v1`

Each of the normalised JSON files is an object with `schema` and `rulesets` (ruleset texts shared by name),
`resources` (resources shared by name), and `cases`. Each case:

| Field | Meaning |
|---|---|
| `id` | A stable identifier: `aruma/<topic>/<id>`, `hidane/<name>` or `pyric/<service>/<scenario>/<n>` |
| `service` | `firestore` or `storage` |
| `source` | Who recorded it: `aruma`, `hidane` or `pyric` |
| `provenance` | What it was recorded on: `deployed` (rules actually deployed and real requests), `rules-test-api` (`projects.test`), `emulator` |
| `recordedAt`, `version` | When, and on which version (the API, or the emulator and Java version, or the source commit) |
| `description` | A short description |
| `expression` | The condition, if the case is a single condition |
| `ruleset` or `rulesetRef` or `rulesetGenerated` | The ruleset as text, or its name in `rulesets`, or its generator (`generate` in `tools/oracle.mjs`, for the size cases). In hidane's `rulesets` there is a template containing `{{expr}}` that `expression` replaces |
| `request` | The request in the Test API shape: `method`, `path`, `auth`, `time`, `resource` (that is, `request.resource`), `query` |
| `resource` or `resourceRef` | The existing resource in the Test API shape (Firestore: `{data: …}`; Storage: the object itself), or its name in `resources` |
| `functionMocks` | The mock of `get()`/`exists()` (or `firestore.get()`/`firestore.exists()` in Storage) in the Test API shape |
| `valueEncoding` | Absent = JSON as the Test API reads it; `firestore-rest` = typed Firestore REST values (hidane) |
| `rawNumbers` | `[{from, to}]`: in the text of the request as sent, the number `from` was written as the text `to` (for example `2.0`, which `JSON.stringify` does not write) |
| `expected` | The outcome: `outcome` is one of `allow`, `deny` and `error`, with `reason` (`false` or `no-matching-statement`) or `errorKind` (`evaluation`, `compile` or `request`), and `message` as it came |
| `visited` | The values of the top-level expressions the Test API visited (`visitedExpressions`) |
| `emulator` | `skip: <reason>` if running it on the emulator was not possible |

This is the schema of `production/` and `imported/*/normalised/`. The files in `emulator/` are comparison results, not cases, and their shape
(`{id, production, emulator}` with the origin fields at file level) is described in the `emulator/` section below.

`error` means the decision is a denial caused by an evaluation error (or by a ruleset that does not compile, or by a request the API itself rejected). `deny`
is a denial because the condition is `false` or because no `allow` statement matches. Note: the Test API returns the same message for an error in
any statement, so the reference is `outcome`; the messages and line numbers are for reading and diagnosis.

## How each file was produced

### `production/` (our source, provenance `rules-test-api`)

- `tools/record-production.mjs` takes the topic modules (in the held-out branch), splits each topic into the development
  and held-out sets, gathers the conditions of each set into rulesets of 60 cases (a match per case:
  `match /c_<id>/{doc}`), and sends each ruleset with its cases in one request to
  `POST https://firebaserules.googleapis.com/v1/projects/chatapp-8ae21:test`. A case whose condition does not compile
  is taken out of the bundle and recorded alone; the limit, size and match-rule cases are recorded alone from the start.
- `projects.test` evaluates the given source without deploying it and without reading any data: **no rules were deployed** to the project.
- The token: from the firebase CLI login on this machine, through the `lib/auth.js` module of firebase-tools itself
  (`tools/rulesapi.mjs`). It is not printed and not written to any file; it is sent in the `Authorization` header only, so the request body
  does not contain it.
- `production/raw/<topic>.json`: for each call, the `request`, `status` and `response`. `request.body` is the request body as
  sent; if it contains a number that does not survive `JSON.parse` unchanged (such as `2.0` or `9223372036854775807`), the body is in
  `request.bodyText` as literal text instead. `request.requestSha256` is the hash of the whole body sent. Only the size cases
  have the ruleset text replaced by its generator, size and hash (`content: {generated, sha256, bytes}`).
  The calls recorded before 2026-10-24 were first saved with only the ruleset hash and the case paths;
  `tools/rebuild-raw.mjs` rebuilt their bodies from the normalised cases the way the recorder builds them, and checked that the hash
  of every ruleset matches the hash recorded at the time (no call failed), and marked them `rebuilt: true`.
- Every case is sent with `expectation: ALLOW`; `SUCCESS` = `allow`. Anything else: `error` if an
  `errorPosition` or a message `Error:` came, otherwise `deny`.
- Defaults: `request.auth` is `alice` with fixed claims (`ALICE` in `tools/oracle.mjs`),
  `request.time` is `2026-01-15T10:20:30.123Z`, and the resource is `DOC` (Firestore) or `OBJECT` (Storage), unless
  the case says otherwise. The Test API takes `request` and `resource` literally: a field that is not sent is undefined in the
  rules, so a request with no user sends `auth: null` and a missing document sends `resource: null`.
- Date: 2026-10-23/24. Version: `firebaserules.googleapis.com v1 projects.test` (the API does not state its
  engine's version).

### `emulator/` (provenance `emulator`)

`tools/record-emulator.mjs` runs the same `production/` cases on the official emulators as black boxes (no
JAR was opened):

- Firestore: `cloud-firestore-emulator-v1.22.0.jar` on Java 21 (`--host 127.0.0.1 --port 8840`, project
  `demo-oracle`). The rules are loaded with `PUT /emulator/v1/projects/demo-oracle:securityRules`, the case's documents
  (the resource and the targets of `get()`/`exists()`) are written as the owner, and then the request is sent through REST with an unsigned ID token
  carrying the case's `auth`. Through REST one cannot choose `request.time` or send `request.query`, so those cases are skipped.
- Storage: `cloud-storage-rules-runtime-v1.1.3.jar` on Java 21, through the same stdin/stdout protocol that
  the Storage emulator in firebase-tools 13.23.0 uses (`tools/storagert.mjs`), and the variables (`request`,
  `resource`) are built the way firebase-tools builds them, with the case's values.
- `tools/compare.mjs` writes the tables of [parity-exceptions.md](parity-exceptions.md).
- The shape of `emulator/<topic>.json` differs from the normalised cases: at file level it has `schema`, `topic`, `service`, `set`,
  `source` (`aruma`), `provenance` (`emulator`), `recordedAt` and `version` (the emulator and the Java version), which apply to
  all its cases; and each case is `{id, production, emulator}`: `id` is the case identifier in `production/<topic>.json`,
  `production` is a copy of `expected` there, and `emulator` is what the emulator decided in the same fields (`outcome`, `reason` or
  `errorKind`, `message`, and sometimes `lookups` for the `firestore.get/exists` paths the Storage emulator asked for, and `note`),
  or `{skip: <reason>}` if it was not run.
- The emulator is started and stopped by its process id (pid), not by a name pattern.

### `imported/hidane/` (github.com/hidane-dev/hidane, MIT or Apache-2.0; we use it under Apache-2.0)

- Commit `abd4664b0d610781eb3f0fec45642e7bc9953533`. Imported by `tools/import-hidane.mjs`.
- `original/results/rules-conditions-official-v1.22.0.development.json`: hidane's file
  `results/rules-conditions-official-v1.22.0.json` (what the official Firestore emulator v1.22.0 decided for each condition),
  **modified**: its 106 `heldout/*` lines were moved to the held-out branch (one line per result, so the rest is the bytes of the original
  itself, and nothing else changed; it is marked as modified in [NOTICE](../../../NOTICE)). hidane recorded it on 2026-10-11
  with an emulator under an English locale.
- `original/cases/rules.json` and `cel-spec.json` as they are. `cel-spec.json` is taken from
  google/cel-spec (v0.25.3, Apache-2.0), as the file itself says.
- `original/tools/conditions.py` and `rules_conditions.py`: the two tools that produced the file, for the record.
- `normalised/rules-conditions-emulator.json`: the results in the unified schema (provenance `emulator`, the document
  in typed REST values).
- `production/hidane-conditions.json` (ours): the same conditions through the Test API. The document in the closest JSON: `ts` is an
  RFC 3339 string (the Test API reads it as a timestamp), and `big` is the number `9223372036854775807`; as for `one_f`, `geo`, `ref` and `by`,
  the Test API cannot express their types, so the cases that read them have `note: comparable: false`.

### `imported/pyric/` (github.com/davideast/pyric, Apache-2.0)

- Commit `756d23efae7e0147db820b7ce9c1672ba2a6d8a6`. Imported by `tools/import-pyric.mjs`.
- `original/observations/{firestore-rules,storage-rules}/` as they are, `original/rules-corpus/` the scenario
  files they were recorded from (TypeScript, as they are), and `original/src/rules-math-cases.ts`, which they import.
- `normalised/{firestore,storage}-rules.json`: each case of the corpus with the verdict the observation recorded for it,
  and the request in the Test API shape as Pyric builds it. The `rules-*` ones were recorded through the Test API (provenance `rules-test-api`).
  The `stdlib-realstorage-*` ones were recorded on real Storage with deployed rules (`deployed`); their rules are built by Pyric's runner,
  so they are in `rulesets` when the observation keeps them, and otherwise `rulesetNote` points to the runner. Values that are not a decision
  (relations between fields, times) are recorded with `outcome: fact`.
- Metadata: no real email addresses and no keys. Pyric's project ids (`agentive-de`,
  `digame-mas`, `genkit-idx`), the bucket names and the run identifiers in the messages were left as they are, since they are labels only.

License notices are in [NOTICE](../../../NOTICE).

## Re-recording

It needs Node 24, a logged-in firebase CLI, and Java 21 for the emulators. On this machine Node connections to
Google over IPv6 fail, so you need:

```sh
export NODE_OPTIONS="--no-network-family-autoselection --dns-result-order=ipv4first"
cd test/oracle/rules/tools

# The cases (the cases-src modules in the oracle-rules-heldout branch):
node record-production.mjs --dev ../production --heldout <heldout>/production <cases-src>/*.mjs hidane-conditions.mjs

# The emulators (the Firestore emulator running on 127.0.0.1:8840):
node record-emulator.mjs --firestore 127.0.0.1:8840 --out ../emulator ../production/<topic>.json …
node compare.mjs ../emulator ../production

# The old request bodies in raw/ (once; it does not touch what was recorded as sent):
node rebuild-raw.mjs ../production

# Import:
node import-hidane.mjs <hidane checkout> <commit> <heldout dir>
node import-pyric.mjs <pyric checkout> <commit>
```

A topic recorded earlier (its file in `--dev` exists) is skipped; delete its file to record it again.
