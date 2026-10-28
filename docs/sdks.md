# SDK packages

Your app keeps the official Firebase SDKs. The packages under `sdk/` only point those SDKs at your Aruma server, or add the Admin pieces that the official Admin SDKs cannot do against another host. None of them replaces a Firebase SDK, except the iOS Storage mirror, which is the official FirebaseStorage with one patch. Flutter Storage also needs one helper call in app code (`fromArumaUrl`).

The packages are not published to npm, Maven, CocoaPods or pub.dev yet. Install them from your clone of this repository, as each README describes. The install lines below are the from-source forms. The Go packages are modules in this repository and need no registry.

## Client apps

| Path | Platform | What it does | Install |
|---|---|---|---|
| [`sdk/web/aruma-web`](../sdk/web/aruma-web/README.md) | Web, Node bundlers, Flutter web | Sends the web SDK's Auth requests to Aruma at run time. Rewrites the Storage and Firestore hosts at build time. Plugins for Vite, webpack, Rollup, esbuild, Next.js and Metro. | `npm install --save-dev /path/to/aruma/sdk/web/aruma-web` |
| [`sdk/android/aruma-redirect`](../sdk/android/aruma-redirect/README.md) | Android, Flutter on Android | Reads `ArumaURL` from the manifest and sends the Firebase SDK's Auth requests to Aruma. Cloud Storage requests are a fallback. | Run `./gradlew :aruma-redirect:publishToMavenLocal` in the package, then add `mavenLocal()` and `implementation("dev.aruma:aruma-redirect:0.1.0")` |
| [`sdk/android/aruma-storage-gradle`](../sdk/android/aruma-storage-gradle/README.md) | Android, Flutter on Android | Gradle plugin that points Cloud Storage for Firebase at Aruma when the app is built, so download URLs are Aruma links. | Run `./gradlew publishToMavenLocal` in the package, then add `mavenLocal()` to `pluginManagement` and `id("dev.aruma.storage") version "0.1.0"` |
| [`sdk/ios/ArumaRedirect`](../sdk/ios/ArumaRedirect/README.md) | iOS, Flutter on iOS | Reads `ArumaURL` from `Info.plist` and sends the Firebase SDK's Auth requests to Aruma. Cloud Storage requests are a fallback. | `pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'`, or add the Swift package from your clone |
| [`sdk/ios/firebase-storage-patch`](../sdk/ios/firebase-storage-patch/README.md) | iOS (SwiftPM) | A mirror of FirebaseStorage with Aruma's host patched in, so download URLs are Aruma links. | Build the mirror with `make-mirror.sh` and host it yourself (a public mirror is not published yet), then `swift package config set-mirror --original https://github.com/firebase/firebase-ios-sdk --mirror <your mirror URL>` |
| [`sdk/flutter/aruma_storage`](../sdk/flutter/aruma_storage/README.md) | Flutter | Reads Aruma download links back with `refFromURL` (`fromArumaUrl`). | `aruma_storage:` with `path: /path/to/aruma/sdk/flutter/aruma_storage` in `pubspec.yaml` |

## Admin SDKs on your server

| Path | Platform | What it does | Install |
|---|---|---|---|
| [`sdk/node/admin-auth`](../sdk/node/admin-auth/README.md) | Node.js | Verifies Aruma ID tokens and session cookies and manages Aruma users, with `firebase-admin`'s own types and error codes. | `npm install /path/to/aruma/sdk/node/admin-auth firebase-admin@^14` |
| [`sdk/node/admin-storage`](../sdk/node/admin-storage/README.md) | Node.js | Reaches Aruma Cloud Storage with the official `@google-cloud/storage` client. | `npm install /path/to/aruma/sdk/node/admin-storage firebase-admin@^14` |
| [`sdk/go/adminauth`](../sdk/go/adminauth/README.md) | Go | Verifies Aruma ID tokens and session cookies and manages Aruma users, with `firebase.google.com/go/v4`'s own types. | `go get github.com/arumadev/aruma/sdk/go/adminauth` |
| [`sdk/go/adminstorage`](../sdk/go/adminstorage/README.md) | Go | Reaches Aruma Cloud Storage with the official `cloud.google.com/go/storage` client. | `go get github.com/arumadev/aruma/sdk/go/adminstorage` |

## What you need per service

- Auth: a redirect library (web, Android, iOS). Realtime Database: only the config file, which carries the `databaseURL`. See [Connecting a web app to Aruma Auth](guides/auth-web.md), [Realtime Database](guides/realtime-database.md) and [Verifying users on your server](guides/auth-admin.md).
- Cloud Storage: a build-time package on each platform, so download links point at Aruma. See [compatibility](reference/compatibility.md#cloud-storage).
- Document Database: see [Moving your app's Firestore to Aruma](guides/firestore-migration.md).
