# aruma_storage (Flutter)

Use the **official `firebase_storage`** for Flutter, in its normal production mode (no
`useStorageEmulator`), with an Aruma server, and read its download URLs back with `refFromURL`.

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

## The production path

On Android and iOS, firebase_storage calls the native SDKs, so a Flutter app takes their production
path (ADR-022 §16.4). The app is built for Aruma, and `getDownloadURL()` returns the unified link
`https://<aruma-host>/v0/b/<bucket>/o/<path>?alt=media&token=<uuid>`, which `Image.network`,
`CachedNetworkImage`, `package:http` and a shared link open on Aruma. Nothing in Dart changes.

| | What to add | |
|---|---|---|
| Android | the Gradle plugin [`dev.aruma.storage`](../../android/aruma-storage-gradle) in `android/settings.gradle.kts` and `android/app/build.gradle.kts`, and [`aruma-redirect`](../../android/aruma-redirect) for Auth | `arumaStorage { url = "https://aruma.example.com" }` |
| iOS | Swift Package Manager on (`enable-swift-package-manager`), and the [patched FirebaseStorage mirror](../../ios/firebase-storage-patch) through `swift package config set-mirror`, in `ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/mirrors.json`; [`ArumaRedirect`](../../ios/ArumaRedirect) for Auth | `ArumaURL` in `ios/Runner/Info.plist` |

- Keep the `firebase_options.dart` that Aruma generates.
- Set `ArumaStorageRedirect` to `false` (manifest `<meta-data>` on Android, `Info.plist` on iOS) to
  make the build-time path mandatory: an app built without it then stops at launch instead of
  sending download URLs to Google.
- Without the build step, aruma-redirect / ArumaRedirect redirect the native SDKs' requests at
  run time (the fallback), but `getDownloadURL()` names Google, and `Image.network` (dart:io, which
  no native redirect sees) sends it there.
- FlutterFire's line on Firebase iOS 12.x was measured (firebase_core 4.15.2, firebase_storage
  13.6.1, firebase_auth 6.7.1; firebase-ios-sdk 12.19.3). The 5.x line needs firebase-ios-sdk
  13.1.1, whose manifest needs Swift 6.2 (Xcode 26.2), and a mirror tag of that release.
- Flutter web: the build step of [`@aruma/web`](../../web/aruma-web) (ADR-022 §16.1).

## fromArumaUrl

firebase_storage parses an http(s) URL **in Dart**, and takes only `firebasestorage.googleapis.com`,
`localhost` and `10.0.2.2` as Firebase hosts (`lib/src/utils.dart:24`, `:49-56` in 13.6.1 and
14.0.1). So `refFromURL(await ref.getDownloadURL())` throws on Aruma's host (an `AssertionError` in
debug, a `TypeError` in release), where Firebase does not. `fromArumaUrl` turns the link into the
`gs://<bucket>/<path>` that `refFromURL` reads back to the same object:

```dart
import 'package:aruma_storage/aruma_storage.dart';

final link = await ref.getDownloadURL();
final same = FirebaseStorage.instance.refFromURL(fromArumaUrl(link));
```

The package is not published to pub.dev yet. Use a path dependency on your clone of this repository:

```yaml
# pubspec.yaml
dependencies:
  aruma_storage:
    path: /path/to/aruma/sdk/flutter/aruma_storage
```

A `git:` dependency on `https://github.com/arumadev/aruma` with `path: sdk/flutter/aruma_storage` also works.

- Any host is accepted: Aruma's, Google's (with or without `:443`). The name is percent-decoded
  strictly as UTF-8 (`+` stays `+`) and written raw, because `refFromURL` passes a `gs://` path on
  as it is: encoding it would name another object. A `gs://` URL comes back unchanged.
- It throws `NotStorageUrlException` (a `FormatException`) on anything else, including the prefixed
  form `<aruma>/firebasestorage.googleapis.com/v0/...`, which no platform reads, and on names no
  reference can hold: an empty segment (`a//b`), a leading or a trailing `/`. firebase_storage
  trims one leading and one trailing `/`, and the native SDKs collapse `//`, so those would name
  another object. The Android and iOS `fromArumaUrl` refuse the same names.
- No dependencies: plain Dart, usable from any Flutter platform.

`test/e2e/flutter-storage` runs it on a device against a real server. Unit tests: `dart test`.
