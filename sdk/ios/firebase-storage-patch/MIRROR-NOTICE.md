# This is a modified firebase-ios-sdk

Every tag in this repository is the official firebase-ios-sdk release of the same name
(https://github.com/firebase/firebase-ios-sdk, Copyright Google LLC, Apache License 2.0), with one
change made by The Aruma Authors, so that FirebaseStorage talks to an Aruma server
(https://github.com/arumadev/aruma):

- `FirebaseStorage/Sources/Storage.swift`: Storage reads its scheme, host and port from `ArumaURL`
  in the app's Info.plist (new file `FirebaseStorage/Sources/Internal/ArumaStorageOrigin.swift`),
  and `port` is optional, so download URLs carry no `:443`.
- `FirebaseStorage/Sources/Internal/StorageUtils.swift`, `StorageGetDownloadURLTask.swift`: the
  optional port.
- `FirebaseStorage/Sources/Internal/StorageFetcherService.swift`: `http` is allowed when `ArumaURL`
  is `http` (loopback, or `ArumaAllowInsecureURL`).
- `FirebaseStorage/Tests/Unit/ArumaStorageOriginTests.swift`: tests of the above.

Without `ArumaURL`, FirebaseStorage behaves exactly as the official release and logs one line
saying so. No other file differs from the official release. Each modified file says so in its
header. The patch is `sdk/ios/firebase-storage-patch/storage-host.patch` in the Aruma repository,
and every tag here is one commit, built from the official tag by `make-mirror.sh`.

Use it through a SwiftPM mirror, so that your `Package.swift`, your Xcode project and FlutterFire
keep the official URL and version:

```sh
swift package config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk \
  --mirror https://github.com/arumadev/firebase-ios-sdk
```

Licensed under the Apache License, Version 2.0, as the official release (see `LICENSE`).
