# S0-B: how each official Storage SDK reaches a self-hosted server in production mode

The harness behind [docs/research/storage-production-path.md](../../../docs/research/storage-production-path.md).
Every runner starts the recording stub, runs the official SDK in one or more modes, and prints, per
mode, each operation's result and the requests the stub saw (where they arrived, under which
redirect prefix, with which kind of `Authorization`). Nothing talks to Google: each runner has a
network guard, and a request to Google is refused and listed as `BLOCKED`.

| Runner | SDK | Modes | Guard |
|---|---|---|---|
| `admin-node.sh` | firebase-admin 14.5.0, @google-cloud/storage 8.4.0 | 9 configurations (`admin-node/scenario.mjs`) | `../netguard.cjs` |
| `admin-go.sh` | firebase-admin-go 4.22.0, cloud.google.com/go/storage 1.62.1 | 6 configurations (`admin-go/main.go`, a module of its own) | `HTTPS_PROXY` = the stub's refusing proxy |
| `web.sh` | firebase 13.1.0 (gstatic CDN build) in Chromium | emulator, prod-asis, prod-xhr, prod-xhr-https, prod-build | Chromium resolves only localhost; failed requests are recorded |
| `android.sh` | firebase-storage 23.0.0 (BoM 35.0.2), instrumented test | emulator, prod-asis, prod-spike, prod-spike-https | iptables on the app's uid (`adb root`) + `NetworkGuard` |
| `ios.sh` | FirebaseStorage 12.19.2, XCTest in a host app on the simulator | emulator, prod-asis, prod-spike, prod-spike-https, background upload (http, https), background probe | `LoopbackGuard` (default and ephemeral sessions) |
| `flutter.sh` | firebase_storage 14.0.1 (firebase_core 5.1.0) on Android | emulator, prod-spike | iptables, as `android.sh` |

The operations: upload (small, and a 3 MB resumable one; on Android and iOS also from a file),
getMetadata, getDownloadURL, download (bytes; on the web also a Blob, on Android and iOS also to a
file or a stream), list, delete, and then the download URL opened outside the SDK (`fetch` and
`<img>` on the web, `HttpURLConnection` on Android, `URLSession.shared` on iOS).

## Running

```sh
source .cache/env.sh                                    # or the same variables
test/experiments/storage-path/admin-node.sh             # ~1 min
test/experiments/storage-path/admin-go.sh               # ~30 s (builds offline from the module cache)
test/experiments/storage-path/web.sh                    # ~1 min
ANDROID_SERIAL=emulator-8830 JAVA_HOME=<JDK 17+> test/experiments/storage-path/android.sh   # ~5 min
test/experiments/storage-path/ios.sh                    # first build ~10 min, then ~3 min
ANDROID_SERIAL=emulator-8830 JAVA_HOME=<JDK 17+> \
  S0PATH_NDK_DIR=~/Library/Android/sdk/ndk/28.2.13676358 S0PATH_CMAKE_DIR=~/Library/Android/sdk/cmake/3.22.1 \
  test/experiments/storage-path/flutter.sh              # flutter create + build, ~5 min
```

Two runners share a port range only one at a time: `android.sh`, `flutter.sh`, `web.sh` and the
Admin runners default to 8820-8822 (give one `S0PATH_PORT=8827` to run them side by side), `ios.sh`
to 8824-8826.

Each takes mode names as arguments to run only those, e.g. `web.sh prod-xhr` or
`ios.sh testBackgroundUpload`. Results land in `.generated/<runner>/` (`results.jsonl`, the stub's
`requests.jsonl`, logs); `summarize_results.py results.jsonl requests.jsonl --paths` prints them again.

| Variable | Default | |
|---|---|---|
| `S0PATH_PORT` | 8820 (iOS: 8824) | first of three stub ports: http, https, guard proxy. The web page uses 8823 (`S0PATH_PAGE_PORT`) |
| `S0PATH_UPLOAD_URL` | `self` | the stub's `X-Goog-Upload-URL`: `self` (this server, absolute) or `google` (`https://firebasestorage.googleapis.com/…`, so the chunks must go through the redirect) |
| `S0PATH_HEADER_CASE` | `exact` | response header names as Google spells them (`X-Goog-Upload-URL`) or Go's canonical form (`X-Goog-Upload-Url`) |
| `S0PATH_RUN_SUFFIX` | | Android and iOS: a suffix for the `.generated` folder, to keep variant runs apart |

Requirements: Go, Node 20+, python3; for the web, the Playwright Chromium in `PLAYWRIGHT_BROWSERS_PATH`;
for Android, a JDK 17+, the SDK in `ANDROID_HOME` (default `/Volumes/Work/android-sdk`) and an
emulator image with `adb root` (the iptables guard); for iOS, Xcode and XcodeGen, and a booted iPhone
simulator (the stub's throwaway CA is added to it with `simctl keychain add-root-cert`); for Flutter,
the Flutter SDK (the run publishes aruma-redirect to `.generated/flutter/m2` and makes the platform
folders with `flutter create`).

The web page loads the CDN build of the SDK (what Flutter web fetches from `www.gstatic.com`) from
`web/node_modules`, under `/firebasejs/13.1.0/`. Nothing is intercepted, so CORS preflights reach
the stub.

## The stub

`stub/main.go` is not Aruma and checks nothing (no rules, no tokens). It keeps objects in memory and
answers the Firebase Storage REST API (`/v0/b/…`, multipart and the `X-Goog-Upload-*` resumable
protocol) and the GCS JSON API (`/storage/v1/…`, `/b/…`, `/upload/storage/v1/…`, `/download/…`, Go's
XML reads `/{bucket}/{name}`, and `X-GUploader-No-308`), under the prefixes the redirect libraries add
(`/firebasestorage.googleapis.com`, `/storage.googleapis.com`) or none. `/oauth2/v1/token` grants any
assertion. It writes one JSON line per request; the `Authorization` header is reduced to its scheme
and length, and `token`, `key` and `access_token` query values are replaced.

## The spikes

The production-mode runs use two spikes in this branch, marked `SPIKE (S0-B)`, that S6 replaces:

- `sdk/web/aruma-web/src/storage-spike.js`: `installStorage({ url })` patches
  `XMLHttpRequest.prototype.open` and `fetch` for `firebasestorage.googleapis.com`.
- `sdk/android/aruma-redirect` and `sdk/ios/ArumaRedirect`: `setStorageSpike(true)` adds
  `firebasestorage.googleapis.com` to the redirected hosts (off by default; nothing else changes).
