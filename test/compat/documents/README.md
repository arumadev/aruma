# Firebase's own Firestore integration tests, against the Document Database

`run.sh` runs the Firestore integration tests that Firebase publishes in firebase-js-sdk
(`packages/firestore`, `test/integration` and `test/lite`, in Node over gRPC), written for the Firestore
emulator, unmodified, against a throwaway Aruma server, or against the official emulator to record the
baseline. CI runs it in the `documents-compat` job.

```sh
test/compat/documents/run.sh                       # integration, persistence and lite, on Aruma
test/compat/documents/run.sh lite                  # one suite
test/compat/documents/run.sh --prepare             # only clone and install (CI, before the egress guard)
test/compat/documents/run.sh --target emulator --update-baseline   # record baseline-emulator.json
```

A test that passes on the emulator (`baseline-emulator.json`) and does not pass on Aruma fails the run
unless `classified.json` lists it with its reason. A listed test that passes again, or is no longer
run, fails the run too, so the list stays exact. The script's header lists every setting.

## What runs

| Suite | Command upstream | Tests |
|---|---|---|
| `integration` | `vitest run --project=node test/integration` (`test:node`) | the API tests under `test/integration/api`, `api_internal` and `remote` |
| `persistence` | the same with `USE_MOCK_PERSISTENCE=YES` (`test:node:persistence`) | the same, each also with the IndexedDB-backed cache |
| `lite` | `TEST_PLATFORM=node_lite vitest run --project=node test/lite` (`test:lite`) | the lite SDK |

Not run: the browser project (the emulator cannot serve WebChannel, so there is no baseline), and the
pipeline tests (skipped upstream on the emulator and on Standard edition).

## Last results

firebase-js-sdk `firebase@13.0.0`, `cloud-firestore-emulator-v1.22.0.jar`:

| Suite | Tests | Emulator | Aruma | Classified |
|---|---|---|---|---|
| `integration` | 919 | 545 passed, 4 failed, 370 skipped | 544 passed, 5 failed, 370 skipped | 1 |
| `persistence` | 1,907 | 1,171 passed, 7 failed, 729 skipped | 1,169 passed, 9 failed, 729 skipped | 2 |
| `lite` | 444 | 198 passed, 246 skipped | 197 passed, 1 failed, 246 skipped | 1 |

The tests that fail on the emulator fail on Aruma too and are not compared: the `auth.test.ts` cases of
`api_internal`, which call Google's Auth servers and are refused by the network guard.

The classified differences (`classified.json`):

- `unsupported-feature`: "can keep docs separate with multi-db when online" opens the databases `db1`
  and `db2`, which the emulator creates on first use. Aruma serves `(default)` only and answers
  `NOT_FOUND`, as production does for a database that does not exist
  (`docs/reference/known-differences.md`; `test/oracle/documents/corpus/f0-missing-database.json`).
- `index-rule`: the lite "queries requiring indexes" block, which upstream runs against the emulator
  only, aggregates `sum` and `average` over two fields of a collection with a random name. Production
  refuses that without a composite index (`indexes-105.json`, `sum2_all` and `multi_agg`), and so does
  Aruma.

A test of `database.test.ts` has the current time in its name (`set/update should reject: <date>`);
`summarize.mjs` replaces the date, so runs compare by name.

## How it reaches Aruma

`test/smoke/genconfig -serve -documents-dev-owner -documents-open-rules` starts the server on a throwaway
database; the open-rules switch is `documents.dev_open_rules`, the emulator's "no rules" behaviour. The
tests read `FIRESTORE_TARGET_BACKEND=emulator`, `FIRESTORE_EMULATOR_PORT` and
`FIRESTORE_EMULATOR_PROJECT_ID` (Aruma's project id, since the emulator accepts any), and connect with
`ssl: false`, as against the emulator.

Aruma enforces composite indexes as production does (`documents.index_enforcement: enforce`), and the
emulator does not. `indexes.mjs` therefore creates, through the Admin API, the indexes of Firebase's own
`firestore_composite_index_config.tf` (the file Firebase applies to its production test project) in
both scopes. Aruma refuses the file's two indexes that name a field twice, `[a, testId, a DESC]` and
`[rating CONTAINS, testId, pages, rating]` (known-differences.md, "Index fields"). The first serves the
last query of "OR Queries can use or queries", so `indexes.mjs` adds `[testId, a DESC]`, the index
Aruma's error names, which is the one production's rule asks for in the recorded shapes
(`eq_eq_ord_eqfield`, `ltl_eq`). Any other refused index fails the run.

## Install

The upstream code is cloned at `firebase@13.0.0` into `.cache/upstream/firebase-js-sdk` of the main
checkout (`ARUMA_CACHE_DIR`), the clone the Auth runner uses, and installed with yarn from its lockfile;
`build:deps` of `packages/auth` and the `webchannel-wrapper` build give the packages the Firestore
sources import. A stamp file skips the install on later runs. About 1.5 GB, and several minutes cold.
Under `test/ci/egress-guard.sh` the script installs nothing and stops if the stamp is missing.
