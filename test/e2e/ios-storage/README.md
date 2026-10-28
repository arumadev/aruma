# iOS Cloud Storage e2e

The official FirebaseStorage and FirebaseAuth for iOS, in production mode (no `useEmulator`),
against a real Aruma server, on the production path of ADR-022 §16.3: FirebaseStorage built from
the patched mirror (`sdk/ios/firebase-storage-patch`), reached through
`swift package config set-mirror`, so the app keeps the official URL and version.

```sh
test/e2e/ios-storage/run.sh               # SwiftPM: the mirror, set-mirror, exactVersion 12.19.3
test/e2e/ios-storage/run.sh --cocoapods   # the FirebaseStorage 12.19.0 pod patched by aruma_storage.rb
test/e2e/ios-storage/measure-mirror.sh    # how SwiftPM treats the mirror (resolution only)
test/e2e/ios-storage/flutter-mirror.sh    # whether `flutter build ios` honours it
```

`run.sh` documents its steps and variables. The tests (`Tests/StorageE2ETests.swift`) are shared by
both modes:

- **The eleven operations:** putData, putData 3 MB (resumable), putFile 3 MB, getMetadata,
  updateMetadata, downloadURL, data 3 MB (bytes compared), write(toFile:) (bytes compared),
  listAll, the download URL through `URLSession.shared`, delete. `ArumaURL` is given as
  `http://LOCALHOST:<port>`, and `downloadURL()` must equal
  `http://localhost:<port>/v0/b/<bucket>/o/<name>?alt=media&token=<the server's token>` literally.
- **Names:** `a b+é.txt` and an Arabic name with Arabic-Indic digits (`\u0635\u0648\u0631\u0629 \u0661.png`, "picture 1"): `reference(forURL: link)`,
  `reference(forURL: ArumaUrls.fromArumaUrl(link, bucket:))` and the original reference have the same
  `fullPath` and read the same bytes.
- **Background upload:** FirebaseStorage turns the background session off on the simulator; the
  test keeps it on (`BackgroundUploads`), so `putFile` runs as on a device. The chunk must come from a
  background session and go to the upload URL on Aruma.
- **The build:** `ArumaRedirect.firebaseStorageBuild` is `.patched`, and in SwiftPM mode the host
  app's first build phase, `check-firebase-storage.sh`, passed (run.sh also checks that it fails
  on a checkout without the patch).
- **The guard:** the proof is in the tests. LoopbackGuard refuses every URLSession request that
  leaves the machine, no Storage request may go to Google (ArumaRedirect's fallback is off:
  `ArumaStorageRedirect=false`), and FetcherLog records the address of every GTMSessionFetcher
  request, the background chunks included. run.sh also samples the app's sockets with lsof; that
  can miss a short connection, so it is a second look, not proof.

## Results (2026-10-24, Xcode 16.4, Swift 6.1.2, iPhone 16 Pro Max simulator, iOS 18.5)

| | SwiftPM (mirror, 12.19.3) | CocoaPods (12.19.0 pod) |
|---|---|---|
| Operations | 11/11, no ⚠ | 11/11, no ⚠ |
| `downloadURL` | `http://localhost:9440/v0/b/…/o/e2e%2F…%2Fsmall.txt?alt=media&token=<uuid>`, literally the unified form | the same |
| Names | 3 references, same `fullPath` and bytes | the same |
| Background `putFile` | 1 chunk, background session, `PUT 127.0.0.1:9440/v0/b/…/o?upload_id=…` | the same |
| Requests to Google | 0 Storage; Auth (2) through ArumaRedirect | the same |
| App sockets off the machine (lsof samples) | 0 | 0 |

- The patched FirebaseStorage logs `FirebaseStorage (Aruma build): Storage uses http://localhost:9440 (ArumaURL)`.
- The simulator's `nsurlsessiond` talks to Apple (17.253.0.0/16) and CDNs for itself; its sockets are
  listed, not judged. The chunk's address is checked in the test.
- `make-mirror.sh 12.19.3`: `FirebaseStorageUnit` on macOS, **170 tests, 0 failures**: the 163
  official ones unchanged, without `ArumaURL` (the official endpoint and port 443, one log line),
  and the patch's 7, among them `ArumaURL` without a port giving `url.port == nil`.

## updateMetadata on iOS (ADR-023 §3-17, A §8.1)

Recorded from FirebaseStorage's own fetchers: [`recorded/update-metadata.json`](recorded/update-metadata.json).

- `updateMetadata(StorageMetadata())` with one field sends `{"metadata": null, "bucket": "", "cacheControl": "no-cache"}`:
  **yes, iOS sends `"metadata": null`** whenever `customMetadata` is nil, and Aruma then clears the
  custom metadata, as ADR-023 §3-17 decides (the emulator and GCS do the same).
- After `getMetadata()`, it sends `null` for every field it does not model:
  `{"etag": null, "storageClass": null, "crc32c": null, "downloadTokens": null, "size": 12, "metadata": {…}}`.
  Aruma keeps the download token.

## The mirror, measured

`measure-mirror.sh` (SwiftPM 6.1.2, resolution of small packages):

| Case | Result |
|---|---|
| `from: "12.19.3"`, `exact:`, `"12.19.3"..<"13.0.0"`, with set-mirror | the mirror's patched commit; `Package.resolved` keeps the official URL as `location`, with the mirror's revision |
| A `Package.resolved` pinning the official revision, then set-mirror | re-pinned to the mirror's commit, no error |
| The app names the mirror URL directly, a dependency (as FlutterFire) names the official URL | **no error and no warning: SwiftPM silently takes the official repository** (unpatched checkout, official revision), in either order |
| Both name the official URL, set-mirror | the mirror, for both |

So **set-mirror is the only safe delivery.** Replacing the official URL with the mirror's in an
Xcode project is not refused when another package names the official URL: the build silently uses
the unpatched FirebaseStorage. That alternative is safe only for an app whose graph names
firebase-ios-sdk nowhere else.

- **Xcode:** `xcodebuild` (Xcode 16.4) honours `mirrors.json` in the project's
  `project.xcworkspace/xcshareddata/swiftpm/configuration/`: `run.sh` checks the pin and the checkout.
  The Xcode IDE was not driven; it uses the same resolver and file.
- **`flutter build ios`** (Flutter 3.44.2, Swift Package Manager on in `pubspec.yaml`): honours
  `mirrors.json` in `ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/`. With
  firebase_core 4.15.2 and firebase_storage 13.6.1 (whose Package.swift names the official URL with
  `exact: "12.19.3"`), `Package.resolved` pins the mirror's commit, the checkout under
  `build/ios/SourcePackages` is the patched one, and `flutter build ios --simulator --debug` builds
  (90 s) an app whose binary carries `ArumaStorageOrigin`. Two limits:
  - FlutterFire's current line (firebase_core 5.x, firebase_storage 14.x) asks for
    firebase-ios-sdk 13.1.1, whose manifest needs Swift 6.2 (Xcode 26.2). This machine has Xcode
    16.4, so that line was not built here; the mirror needs a 13.1.1 tag built with Xcode 26.
  - `flutter create` writes iOS 13.0; FlutterFire's packages need 15.0 (the script raises it).
  Running Flutter's Storage against Aruma is S6-F.
