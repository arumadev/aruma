# Cloud Storage for Firebase: reference captures

What Aruma's Storage server must speak, recorded from the outside. The analysis is in
`docs/research/storage/` (A to E); this directory holds the raw exchanges.

## production/ — what exists, and why there is no production traffic here

**The owner's project has no default Cloud Storage bucket.** On 2026-10-23 both default bucket
names, `<project>.appspot.com` (the name before 30 Oct 2024) and `<project>.firebasestorage.app`
(the name after), answer `404` on the client API (`{"error":{"code":404,"message":"Not Found."}}`)
and on the GCS JSON API (`"The specified bucket does not exist."`, reason `notFound`), exactly
like a random control name, while an existing public bucket answers `200` to the same anonymous
calls. Creating a default bucket now needs the pay-as-you-go (Blaze) plan ("To provision a new
default bucket using the Firebase console or REST API, your project must be on the pay-as-you-go
Blaze pricing plan", firebase.google.com/docs/storage/faqs-storage-changes-announced-sept-2024,
read 2026-10-23), and the brief forbids billing changes, so none was created, no Storage rules were deployed to the project, and no
object was written there. Nothing needed restoring.

- `bucket_probe.json` — the probe itself (`capture/probe_bucket.mjs`): anonymous `GET`s only, no
  credentials, no writes. The project id is written as `<project>`.

Everything else below comes from the **local Storage emulator**. The emulator is an oracle of
the second rank (brief: production, then the emulator as a black box, then Google's SDK suites);
`docs/research/storage/E-production-vs-emulator.md` lists where the emulator is known or
suspected to differ from production, and which of those a production capture must settle once
a project with a bucket is available.

## emulator/ — the Storage emulator of firebase-tools 15.34.0

- Server: `firebase emulators:start --only storage,auth --project demo-aruma-ref` from
  firebase-tools 15.34.0 (npm), Storage rules runtime `cloud-storage-rules-runtime-v1.1.3.jar`
  on OpenJDK 21.0 (Homebrew `openjdk@21`; firebase-tools 15 refuses Java < 21), Auth emulator for
  the two test users. Config: `capture/emulator/firebase.json`, rules
  `capture/emulator/storage.rules` (the `aruma-ref` block described there), bucket
  `demo-aruma-ref.appspot.com`. Ports: storage 8860, auth 8861, hub 8862, logging 8863, recording
  proxy 8865.
- Clients: `firebase` 13.1.0 (web SDK, run in Node 24.21 where it uses `fetch`),
  `firebase-admin` 14.5.0 with `@google-cloud/storage` 8.4.0, `@google-cloud/storage` 8.4.0
  directly, and `firebase.google.com/go/v4` 4.22.0 with `cloud.google.com/go/storage` 1.62.1
  (`capture/go-admin`, Go 1.27.2).
- Captured 2026-10-23, run `s0c-20261023-emu`; every object lived under
  `aruma-ref/s0c-20261023-emu/` and `cleanup.json` shows the prefix empty at the end
  (`"remaining": 0`).

How: `capture/capture.mjs` starts a recording reverse proxy in front of the server and points
every client at it (`connectStorageEmulator`, `FIREBASE_STORAGE_EMULATOR_HOST`, `apiEndpoint`), so
each file holds what the official client put on the wire, what the server answered, and what
the client made of it. To run it again:

```
cd test/oracle/storage/capture && npm ci
(cd emulator && JAVA_HOME=<jdk21> firebase emulators:start --only storage,auth --project demo-aruma-ref)
(cd go-admin && go build -o /tmp/go-admin .)
GO_ADMIN=/tmp/go-admin node capture.mjs emulator/config.json ../emulator
```

The same script can record against Aruma later (`target: "aruma"`, `upstream` = Aruma's origin).
It cannot record production as it stands: the web SDK reaches production only without
`connectStorageEmulator`, so a production run needs the recorder moved from the proxy into a
`fetch` wrapper (and the Admin path an `apiEndpoint`-free run with real credentials). That is
left for when a bucket exists.

### File format

```
{ scenario, description, source, version, captured, script, clients, run, bucket,
  steps: [ { step, detail?, result: {ok}|{error}|{status},
             exchanges: [ { request: {method, url, headers, body},
                            response: {status, headers, body}, elapsedMs, ms } ] } ],
  notes: [] }
```

`body` is `{text}` for printable UTF-8 up to 16 KiB, `{base64, length}` for small binary bodies,
or `{length, sha256, head, tail}` / `{length, sha256, headBase64}` for large ones. `result` is the
SDK's value or its error (`code`, `status`, `message`, `serverResponse`), or the HTTP status for
raw requests. Hop headers (`connection`, `keep-alive`) and the response `date` are dropped.

### Redaction

ID tokens (`<id-token-a>`, `<id-token-b>`), uids (`<uid-a>`), emails, the fixed OAuth token of
`admin_gcs_endpoint` (`<access-token>`), download tokens (`<download-token-N>`, learnt from
`downloadTokens` and `firebaseStorageDownloadTokens`) and resumable session ids
(`<upload-id-N>`, from `X-GUploader-UploadID` and `upload_id=`) are replaced before anything is
written; the same value keeps the same placeholder within a file. Other UUIDs (multipart
boundaries, `x-goog-gcs-idempotency-token`) are not secret and are kept. A forged `alg:none`
token would be written `<jwt>`. The emulator's tokens grant nothing outside this machine; the
redaction is there so the same script is safe on production.

### Scenarios

| File | What |
|---|---|
| `sdk_upload.json` | `uploadBytes` (multipart), `uploadString` raw/base64/data_url, `uploadBytesResumable` 1 KiB (multipart) and 700 KiB (resumable: start, 256 KiB chunk, `upload, finalize`), full metadata, a unicode name, overwrite, empty file |
| `sdk_read.json` | `getMetadata`, `getBytes` (+ `maxDownloadSizeBytes` → `Range`), `getDownloadURL`, the URL fetched without auth, with `Range`, with a wrong token, without token; `Bearer` instead of `Firebase`; not-found errors |
| `sdk_metadata.json` | `updateMetadata`: set, add/remove custom keys with `null`, clear a field, empty update, missing object; raw PATCH with `metadata:null`, read-only fields, invalid JSON |
| `sdk_list.json` | `list` with `maxResults`/`pageToken` to the end, `listAll`, sub folder, empty prefix, SDK-side validation (0, 1001), raw: no delimiter, prefix without `/`, `maxResults` 1000/5000, bad `pageToken`, `startOffset` |
| `sdk_delete.json` | `deleteObject`, then get and delete again |
| `rules.json` | denials and allows under `storage.rules`: unauthenticated (multipart and resumable), size, contentType, custom metadata, `update` rule on `updateMetadata`, another user's folder, `get` vs `list`, no rule, update/delete guarded by stored metadata, download token vs rules, public read, forged and garbage tokens |
| `tokens.json` | tokens created on upload, `create_token`/`delete_token` as user and as admin (`Bearer owner`), revocation, `getDownloadURL` after it, client PATCH of `firebaseStorageDownloadTokens` and `downloadTokens` |
| `raw_resumable.json` | client resumable protocol by hand: start, query, offsets right/gap/overlap, `upload, finalize`, query/upload after final, cancel, short finalize, bad command, unknown session |
| `raw_upload.json` | raw body with `?name=` (no protocol header), `uploadType=media`, `POST /o/<name>`, hand-made multipart (name only in metadata; name in both), bad boundary, no name, unknown bucket, empty name, CORS preflight and `Origin` |
| `admin_node.json` | firebase-admin (Node) in emulator mode: `save` multipart/resumable (13 B, 6 MiB), `exists`, `getMetadata`, `setMetadata` with `null`, `download` (+range), `getFiles`, `copy`/`move` (rewrite), admin `getDownloadURL`, 404s, bucket get, `ifGenerationMatch=0`, writing where client rules forbid |
| `admin_gcs_endpoint.json` | `@google-cloud/storage` with `apiEndpoint` + `useAuthWithCustomEndpoint` (production-shaped `/storage/v1/...` paths and `Authorization: Bearer`), incl. a 9 MiB upload in 4 MiB chunks |
| `admin_go.json` | firebase-admin-go: Writer single-shot and chunked (9 MiB / 4 MiB), Attrs, Update, readers (XML-API path), Objects with delimiter, CopierFrom, `DoesNotExist` precondition, deletes, bucket Attrs |
| `cleanup.json` | `deleteFiles` on the run prefix and the empty listing after it |
