# Where the emulator differs from the Rules Test API (development set)

- **What was compared:** every case in `production/` (what the Rules Test API decided, 2026-10-23/24) was run on the
  official emulator (`emulator/`), and the outcome class was compared: `allow`, `deny` or `error`. Messages are not
  compared, since each emulator words them differently. The hidane conditions were compared with what hidane recorded
  from the same emulator (v1.22.0).
- **The emulators:** `cloud-firestore-emulator-v1.22.0.jar` and `cloud-storage-rules-runtime-v1.1.3.jar`, on
  Java 21, as black boxes. Details in [README.md](README.md).
- **Totals:** our topics: 617 identical, 29 different, 43 not run on the emulator (the reason is in `emulator/<topic>.json`:
  `request.time` cannot be chosen through REST, or the Test API itself rejected the request, or auth is not an ID token, or the
  path is not a document). hidane conditions: 1324 identical, 9 different, 12 not comparable.
- **Conclusion:** we found no difference at all in the semantics of the language itself between the emulator and the Test API. Every
  difference below is either an effect of how the Test API takes its inputs, or of how the emulator builds the request, or a limit
  the Test API does not apply.
- **What we do not know:** the Test API is not the deployed production service. What this file says about "production" means the Test API, except
  where we mention `deployed` (Pyric's notes on real Storage).

## Test API effects

What follows is behavior of the Test API itself, not of the language; the engine does not imitate it, and S1-E compares these cases with the deployed behavior.

| Effect | Cases | Deployed / emulator |
|---|---|---|
| It takes `request` literally: a field that was not sent is undefined. With no `auth`, `request.auth == null` is an error, "Property auth is undefined on object"; with `auth: null` it is true | `aruma/encoding/auth_absent`, `aruma/storage/auth_absent`; and `aruma/encoding/auth_null`, `aruma/storage/auth_null` | Emulator: `request.auth == null` is true for a request with no user |
| The same for `request.time` (`Property time is undefined`), `request.query` and `request.resource` | `aruma/encoding/time_unset_is_timestamp`, `aruma/encoding/request_query_on_get`, `aruma/methods/list_query_limit`, `aruma/storage/request_resource_on_get` | A real request always has `time`. The Storage emulator gives `request.resource == null` on read; the Firestore emulator leaves it undefined (`aruma/encoding/request_resource_null_on_get`) |
| It takes `resource` literally: with no `resource`, `resource == null` is an error, "Null value error"; with `resource: null` it is true | `aruma/encoding/resource_absent_is_null`, `aruma/storage/resource_absent_on_get`; and `aruma/encoding/resource_null_is_null`, `aruma/storage/resource_null_on_get` | Emulator: a missing document has `resource == null` |
| The resource is not derived from the path: a Firestore resource has no `id` and no `__name__`, and a Storage object has no field other than what was sent | `aruma/encoding/resource_name`, `aruma/hidane-conditions/rules/138`…`149`, `aruma/storage/create_name_absent` | The emulator gives `resource.id` and `resource.__name__` |
| The result of a mocked `get()` has no `id` and no `__name__` | `aruma/storage/fs_get_id`, `aruma/storage/fs_get_name` | The emulator gives both (and its `__name__` has the prefix `projects/<p>`) |
| `get()`/`exists()` of a document that is not mocked: "Function not found error: Name: [get]" | `aruma/limits/get_unmocked`, `aruma/storage/fs_get_unmocked`, and `pyric/firestore/get-missing-doc/0`, `/1` and `/3` | The emulator really looks: `get()` of a missing document gives `null`. **Deployed also gives `null`:** the [rules.firestore](https://firebase.google.com/docs/reference/rules/rules.firestore) reference says `get()` returns "the document, or null if it does not exist" (read 2026-10-24). And the "error" in Pyric's `rules-firestore-get-missing-doc` is an effect of the Test API's mocking, not of the language: there the document is unmocked, not missing, and its message is "Function not found error: Name: [get]". No `deployed` note separates the two (`missing-get` in `stdlib-realstorage-p3-lookup-budget-iam-enabled` rejects in both). So the engine gives `null` (S1-C), and the cases above stay as recorded because they measure the Test API |
| A mocked `get()` with result `undefined` = "Service call error"; with result `value: null`, `get()` returns the value `null` | `aruma/limits/get_mock_null`, `aruma/limits/get_mock_value_null` | — |
| No limit on the number of `get()`/`exists()` calls: 50 mocked documents are accepted, and 11 in Storage | `aruma/limits/exists_21`, `aruma/limits/exists_50`, `aruma/storage/fs_lookups_3`, `aruma/storage/fs_lookups_11` | The emulator rejects from 11 documents (`aruma/limits/exists_11`). Deployed Storage rejects the third distinct document (`pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/three`) |
| JSON numbers: an integral one is an int even if written `2.0` or `1e2`; one above 2^53 is read as a double | `aruma/encoding/json_2_0_is_int`, `aruma/encoding/json_2_0_is_float`, `aruma/encoding/json_1e2_is_int`, `aruma/encoding/json_2pow53_plus1_is_int` | A document with `doubleValue: 2.0` is a float in the emulator |
| A string in RFC 3339 format becomes a timestamp | `aruma/encoding/json_ts_string_is_timestamp`, `aruma/encoding/json_ts_string_is_string` | In the emulator it stays a string |
| A string starting with `/databases/` becomes a path, and a `{latitude, longitude}` map becomes a latlng | `aruma/hidane-conditions/rules/11`, `aruma/hidane-conditions/rules/145`, `aruma/hidane-conditions/rules/150` | In a real document the string is a string and the map is a map |
| `request.auth.uid` can be any type | `aruma/encoding/auth_uid_int` | ID token: always a string |
| It accepts the method `write` in the request and matches it against `allow write` | `aruma/methods/method_write_verb` | No real request has the method `write` |
| A Firestore path is not required to name a document (an odd number of segments is accepted) | `aruma/followups/nested_under_recursive_deep` | The emulator rejects the path |
| A Storage path ending in `/` is rejected: "should be URL-encoded" | `aruma/storage-paths/list_on_prefix` | — |
| `debug()` does not exist: "Function not found error: Name: [debug]" | `aruma/errors/debug_function`, `aruma/storage/debug` | Emulator: `debug(x)` returns `x`. Deployed does too: S7-R measured it (`production/deployed-firestore.json`) |
| The source size is measured on the request message (see the results document) | `aruma/followups/size_comment_262119`, `aruma/followups/size_comment_262120` | The emulator is not tested here |

## Emulator differences (not from the Test API)

| Difference | Cases | Detail |
|---|---|---|
| The Firestore emulator's lookup limit counts two evaluations | `aruma/limits/exists_get_same_6` | `exists()` and `get()` on 6 documents: the Test API accepts, and the emulator rejects with "Service call error" because the first evaluation (which always fails when it reads `resource`, firebase-tools #6252) counts toward the limit. `aruma/limits/exists_10` is accepted by both, and `aruma/limits/exists_11` and `aruma/limits/get_11` are rejected by the emulator |
| `list` in Storage | `aruma/storage/read_list`, `aruma/storage/method_list`, `aruma/storage-paths/list_on_folder` | The Test API matches the given path itself against `match`; the emulator treats the path as a folder and matches its children: `list` on `p` is accepted by `match /p/{f}`, and `list` on `p/a` is not. We did not check deployed |
| `__name__` of the result of `firestore.get()` in the Storage emulator | `aruma/storage/fs_get_name` | The emulator gives the name with the prefix `projects/<p>/databases/...` as the Firestore emulator sends it, so comparing it with `/databases/...` gives `false` |

## All the different cases

### Our topics

| case | condition | Test API | emulator | message |
|---|---|---|---|---|
| `aruma/encoding/json_2_0_is_float` | resource.data.g is float | deny | allow |  |
| `aruma/encoding/json_2_0_is_int` | resource.data.g is int | allow | deny | evaluation error at L14:21 for 'get' @ L14, false for 'get' @ L14 |
| `aruma/encoding/json_1e2_is_int` | resource.data.g is int && resource.data.g == 100 | allow | deny | evaluation error at L17:21 for 'get' @ L17, false for 'get' @ L17 |
| `aruma/encoding/json_ts_string_is_string` | resource.data.t is string | deny | allow |  |
| `aruma/encoding/json_ts_string_is_timestamp` | resource.data.t is timestamp | allow | deny | evaluation error at L32:21 for 'get' @ L32, false for 'get' @ L32 |
| `aruma/encoding/resource_absent_is_null` | resource == null | error | allow | Error: firestore.rules line [65], column [21]. Null value error. |
| `aruma/encoding/resource_name` | resource.__name__ == /databases/(default)/documents/c_resource_name/doc | error | allow | Error: firestore.rules line [83], column [21]. Property __name__ is undefined on object. |
| `aruma/encoding/request_resource_null_on_get` | request.resource == null | allow | error | Property resource is undefined on object. for 'get' @ L86, Property resource is undefined on object. for 'get' @ L86 |
| `aruma/encoding/auth_absent` | request.auth == null | error | allow | Error: firestore.rules line [116], column [21]. Property auth is undefined on object. |
| `aruma/errors/debug_function` | debug(true) | error | allow | Error: firestore.rules line [11], column [21]. Function not found error: Name: [debug]. |
| `aruma/limits/exists_11` | 11 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_20` | 20 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_21` | 21 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_50` | 50 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/get_11` | 11 get() calls on distinct documents, all mocked | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [get] … |
| `aruma/limits/exists_get_same_6` | exists() and get() on each of 6 documents (12 calls, 6 documents) | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/get_unmocked` | get() of a document the test case does not mock | error | allow | Error: firestore.rules line [6], column [21]. Function not found error: Name: [get]. |
| `aruma/limits/get_mock_null` | get() mocked to return no document | error | allow | Error: firestore.rules line [6], column [21]. Service call error. Function: [get] … |
| `aruma/methods/list_query_limit` | request.query.limit <= 10 | error | deny | false for 'list' @ L68 |
| `aruma/storage-paths/list_on_folder` | list on the folder p/a | allow | deny |  |
| `aruma/storage/resource_absent_on_get` | resource == null | error | allow | Error: storage.rules line [50], column [21]. Null value error. |
| `aruma/storage/request_resource_on_get` | request.resource == null | error | allow | Error: storage.rules line [53], column [21]. Property resource is undefined on object. |
| `aruma/storage/auth_absent` | request.auth == null | error | allow | Error: storage.rules line [77], column [21]. Property auth is undefined on object. |
| `aruma/storage/read_list` | true | allow | deny |  |
| `aruma/storage/method_list` | request.method == 'list' | allow | deny |  |
| `aruma/storage/fs_get_unmocked` | firestore.get(/databases/(default)/documents/users/alice) == null | error | allow | Error: storage.rules line [134], column [21]. Function not found error: Name: [firestore.get]. |
| `aruma/storage/fs_get_id` | firestore.get(/databases/(default)/documents/users/alice).id == 'alice' | error | allow | Error: storage.rules line [137], column [21]. Property id is undefined on object. |
| `aruma/storage/fs_get_name` | firestore.get(…/users/alice).__name__ == /databases/(default)/documents/users/alice | error | deny | Error: storage.rules line [140], column [21]. Property __name__ is undefined on object. |
| `aruma/storage/debug` | debug(true) | error | allow | Error: storage.rules line [5], column [21]. Function not found error: Name: [debug]. |

### hidane conditions

| case | condition | Test API | emulator (hidane) | Test API message |
|---|---|---|---|---|
| `aruma/hidane-conditions/rules/138` | `resource.__name__ == /databases/(default)/documents/rooms/r1` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/139` | `resource.__name__ == /databases/$(database)/documents/rooms/$(room)` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/140` | `request.path == /databases/$(database)/documents/rooms/$(room)` | deny | allow | (not a difference: on our side the document is read from `hd0140/r1`, not from `rooms/r1`) |
| `aruma/hidane-conditions/rules/141` | `resource.id == 'r1'` | error | allow | Property id is undefined on object. |
| `aruma/hidane-conditions/rules/142` | `resource.id == room` | error | allow | Property id is undefined on object. |
| `aruma/hidane-conditions/rules/144` | `resource.__name__[4] == 'r1'` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/147` | `path('/databases/(default)/documents/rooms/r1') == resource.__name__` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/148` | `resource.__name__ == path('/databases/' + database + '/documents/rooms/r1')` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/149` | `string(resource.__name__) == '/databases/(default)/documents/rooms/r1'` | error | allow | Property __name__ is undefined on object. |

Not comparable (12): the conditions that read `one_f`, `geo`, `ref` or `by`, whose values in `production/hidane-conditions.json`
carry the tag `comparable: false`. Some of them exposed the encoding effects above (`rules/11`, `rules/145` and `rules/150`).
