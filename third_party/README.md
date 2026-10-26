# third_party

Protocol definitions the Document Database speaks (ADR-027). Nothing here is Aruma's code. Each
directory keeps its upstream licence (Apache-2.0) and the upstream layout, so `import` paths resolve
unchanged.

| Directory | Upstream | Pinned at | What |
|---|---|---|---|
| `googleapis/` | [googleapis/googleapis](https://github.com/googleapis/googleapis) | commit `728c111a933ef4c7fc4cbc35894c559aa9eb2615` (2026-10-26) | `google/firestore/v1`, `google/firestore/admin/v1`, and their imports: `google/api` (7 files), `google/rpc/status.proto`, `google/type/{latlng,dayofweek}.proto`, `google/longrunning/operations.proto` |
| `firebase-js-sdk/` | [firebase/firebase-js-sdk](https://github.com/firebase/firebase-js-sdk) | tag `@firebase/firestore@4.19.0`, commit `cc7ead49187d024958a4a3bcee87436dc03be12c` | `packages/firestore/src/protos/google/firestore/v1/*.proto` and `verify.patch`, verbatim. Read by the merge check only, never compiled |

## The merge rule

googleapis is the base: it has the newest fields (`concurrency_mode`, `request_options`, the
pipeline protos), and Google's own Go and Node clients are generated from it. The clients we serve
are generated from firebase-js-sdk's copy (and the Android and iOS SDKs' copies, which carry the
same additions), which is googleapis with a patch. So:

1. A field or enum value present in both copies must have the same number and type in both.
2. A field present only in the firebase-js-sdk copy is added to the googleapis copy with **its exact
   number**, marked with a comment that starts `// Aruma: from firebase-js-sdk`.
3. If googleapis gives that number to another field, or reserves it, nothing is added: the check
   fails, and the conflict is decided in ADR-027 before the protos move.
4. A message, enum or file present only in firebase-js-sdk is not added by this rule: the check
   fails and names it.
5. Unknown fields are never dropped: the generated Go keeps them (protobuf-go's default), and the
   server must not discard them on a read-modify-write.

Today the rule adds one field, `google.firestore.v1.Write.verify = 5` (in the `operation` oneof,
`write.proto`). The web and mobile SDKs send it in transactions; googleapis does not have it (Kor
lost `verify` by generating from googleapis alone). firebase-js-sdk's own `update.sh` builds its copy
the same way, by applying `verify.patch` to googleapis.

`go run ./tools/protomerge` checks the rule, comparing every field and enum value of the
firebase-js-sdk copy with the googleapis copy. `make proto` runs it first, and its test
(`tools/protomerge`) runs with every other.

## Updating

1. Copy the new upstream files over the old ones, at a new pinned commit, and update the table.
2. Apply the merge rule again (today: re-add `verify` to `googleapis/google/firestore/v1/write.proto`).
3. `make proto`, then commit the protos and `internal/services/documents/wire/pb` together. CI's
   `make proto-check` fails if the generated code does not match the protos.

## Generation

`buf.yaml` and `buf.gen.yaml` at the repository root; `make proto` runs buf and its plugins at
pinned versions through `go run` (nothing to install) and names the output `*.gen.go`, as sqlc's
is. The RPC code is connect's handlers alone, with no grpc-go stubs (ADR-027). The Go packages live
inside the service (`internal/services/documents/wire/pb`):

| Proto package | Go package |
|---|---|
| `google.firestore.v1` | `wire/pb/firestorepb` (messages) and `wire/pb/firestorepb/firestorepbconnect` (connect handlers) |
| `google.firestore.admin.v1` | `wire/pb/adminpb` (and `adminpbconnect`) |
| `google.type` | `wire/pb/typepb` |
| `google.longrunning` | `wire/pb/longrunningpb` (and `longrunningpbconnect`) |
| `google.api`, `google.rpc` | not generated: `google.golang.org/genproto/googleapis/{api,rpc}`, which the error details (`google.rpc.Help`) and Google's own clients link too |

A Go binary may register a `.proto` file once. Aruma's binary registers `google/firestore/v1/*.proto`
and `google/type`, `google/longrunning` under the paths above, so it must never also link Google's
`cloud.google.com/go/firestore` (or genproto's `type` and `longrunning`): the Go client used in tests
lives in its own module (as `test/spike/documents/transport/clients/go` does).
