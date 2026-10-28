# Flutter Cloud Storage e2e

The official firebase_storage and firebase_auth for Flutter, in production mode (no
`useStorageEmulator`, no `useAuthEmulator`), against a real Aruma server, on the production path
of ADR-022 §16.4: the native SDKs built for Aruma.

```sh
test/e2e/flutter-storage/run.sh android   # dev.aruma.storage; debug and release (R8) on the device
test/e2e/flutter-storage/run.sh ios       # the patched FirebaseStorage mirror through set-mirror (SwiftPM)
```

`run.sh` documents its steps and variables. Each run makes the platform folder afresh
(`flutter create`, then `prepare.py`), so the repository holds only:

- `app/`: the app. `lib/scenario.dart` is the test; `lib/guard.dart` the Dart half of the network
  guard. Versions are pinned in `pubspec.yaml` (and `pubspec.lock`).
- `e2e_native/`: a Flutter plugin, the native half: the guard (a ProxySelector on Android, a
  URLProtocol on iOS, both installed when the engine registers its plugins) and the channel the
  app asks what the native build holds (`NetworkRequest.PROD_BASE_URL` and the plugin's notice on
  Android, `ArumaRedirect.firebaseStorageBuild` on iOS). It also links aruma-redirect /
  ArumaRedirect from this repository, for Auth.
- `prepare.py`: what an app adds to its platform folders, as the READMEs of the native libraries
  say. Each edit must find its anchor once, so a new `flutter create` template is noticed.

## What the app checks

- **The operations:** putData, putData 3 MB (resumable), putFile 3 MB, putString (base64),
  getMetadata, updateMetadata, getDownloadURL, getData 3 MB and writeToFile 3 MB (bytes compared),
  listAll, list in pages of 3, the download URL through dart:io's `HttpClient`, the download URL in
  `Image.network` (a PNG, decoded), delete. `getDownloadURL()` must equal
  `http://127.0.0.1:<port>/v0/b/<bucket>/o/<name>?alt=media&token=<the server's token>` literally.
- **The round trip:** `refFromURL(link)` throws (firebase_storage's Dart parser,
  `utils.dart:24`, `:49-56`), and `refFromURL(fromArumaUrl(link))` gives the same bucket and
  `fullPath`, for the operations' link and for the names `a b+é.txt`, an Arabic name with Arabic-Indic digits (`\u0635\u0648\u0631\u0629 \u0661.png`, "picture 1") and
  `50% off?#&=1.txt`, whose bytes are then read through both references and the link. The same
  holds for a link on a production host (`https://storage.example.com/…`).
- **Refused:** links to `e2e/<uid>//x.txt`, `/e2e/<uid>/x.txt` and `e2e/<uid>/x/`: `fromArumaUrl`
  throws `NotStorageUrlException`, as the Android and iOS versions do.
- **What differs by SDK, as against Google:** on iOS an upload's `bytesTransferred` and
  `totalBytes` include the resumable upload's first request (the JSON metadata), because
  FirebaseStorage takes GTMSessionUploadFetcher's counts (`totalSent = initialBodySent +
  currentOffset + totalBytesSent`); the object's `size` is the bytes sent on both platforms. And
  FirebaseStorage (iOS) escapes names with its own `GCSEscapedString`, which leaves
  `$ , = : @` unencoded: `50% off?#&=1.txt` gives `…%26=1.txt` on iOS and `…%26%3D1.txt` on
  Android. Both are built by the SDK from the metadata's `name`, not by Aruma, and both name the
  same object; the test requires each SDK's own spelling exactly.
- **The guards:** each half refuses (or records) its own probe to 192.0.2.1 first; at the end no
  Dart socket and no native request went anywhere but this device, and no Storage request went
  through the run-time fallback (`ArumaStorageRedirect` is false).

The server and the native SDKs use `127.0.0.1`, not `localhost`: firebase_storage takes
`localhost` and `10.0.2.2` as the emulator's hosts and would parse those links, as it would not a
production host's.
