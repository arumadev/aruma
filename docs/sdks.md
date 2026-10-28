# SDK packages

Your app keeps the official Firebase SDKs. The packages under `sdk/` only point those SDKs at your Aruma server, or add the Admin pieces that the official Admin SDKs cannot do against another host. None of them replaces a Firebase SDK.

The packages are not published to npm, Maven, CocoaPods or pub.dev yet. Install them from this repository, as each README describes. The install lines below are the ones those READMEs use.

## Client apps

| Path | Platform | What it does | Install |
|---|---|---|---|
| [`sdk/web/aruma-web`](../sdk/web/aruma-web/README.md) | Web, Node bundlers, Flutter web | Sends the web SDK's Auth requests to Aruma at run time. Rewrites the Storage and Firestore hosts at build time. Plugins for Vite, webpack, Rollup, esbuild, Next.js and Metro. | `npm install --save-dev @aruma/web` |
| [`sdk/android/aruma-redirect`](../sdk/android/aruma-redirect/README.md) | Android, Flutter on Android | Reads `ArumaURL` from the manifest and sends the Firebase SDK's Auth requests to Aruma. Cloud Storage requests are a fallback. | `implementation("dev.aruma:aruma-redirect:0.1.0")` |
| [`sdk/android/aruma-storage-gradle`](../sdk/android/aruma-storage-gradle/README.md) | Android, Flutter on Android | Gradle plugin that points Cloud Storage for Firebase at Aruma when the app is built, so download URLs are Aruma links. | `id("dev.aruma.storage") version "0.1.0"` |
| [`sdk/ios/ArumaRedirect`](../sdk/ios/ArumaRedirect/README.md) | iOS, Flutter on iOS | Reads `ArumaURL` from `Info.plist` and sends the Firebase SDK's Auth requests to Aruma. Cloud Storage requests are a fallback. | `pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'`, or add the Swift package |
| [`sdk/ios/firebase-storage-patch`](../sdk/ios/firebase-storage-patch/README.md) | iOS (SwiftPM) | A mirror of FirebaseStorage with Aruma's host patched in, so download URLs are Aruma links. | `swift package config set-mirror --original https://github.com/firebase/firebase-ios-sdk --mirror https://github.com/arumadev/firebase-ios-sdk` |
| [`sdk/flutter/aruma_storage`](../sdk/flutter/aruma_storage/README.md) | Flutter | Reads Aruma download links back with `refFromURL` (`fromArumaUrl`). | `aruma_storage:` with a `git:` dependency on this repository, path `sdk/flutter/aruma_storage` |

## Admin SDKs on your server

| Path | Platform | What it does | Install |
|---|---|---|---|
| [`sdk/node/admin-auth`](../sdk/node/admin-auth/README.md) | Node.js | Verifies Aruma ID tokens and session cookies and manages Aruma users, with `firebase-admin`'s own types and error codes. | `npm install @aruma/admin-auth firebase-admin@^14` |
| [`sdk/node/admin-storage`](../sdk/node/admin-storage/README.md) | Node.js | Reaches Aruma Cloud Storage with the official `@google-cloud/storage` client. | `npm install @aruma/admin-storage firebase-admin@^14` |
| [`sdk/go/adminauth`](../sdk/go/adminauth/README.md) | Go | Verifies Aruma ID tokens and session cookies and manages Aruma users, with `firebase.google.com/go/v4`'s own types. | `go get github.com/arumadev/aruma/sdk/go/adminauth` |
| [`sdk/go/adminstorage`](../sdk/go/adminstorage/README.md) | Go | Reaches Aruma Cloud Storage with the official `cloud.google.com/go/storage` client. | `go get github.com/arumadev/aruma/sdk/go/adminstorage` |

## What you need per service

- Auth: a redirect library (web, Android, iOS). Realtime Database: only the config file, which carries the `databaseURL`. See [Connecting a web app to Aruma Auth](guides/auth-web.md), [Realtime Database](guides/realtime-database.md) and [Verifying users on your server](guides/auth-admin.md).
- Cloud Storage: a build-time package on each platform, so download links point at Aruma. See [compatibility](reference/compatibility.md#cloud-storage).
- Document Database: see [Moving your app's Firestore to Aruma](guides/firestore-migration.md).
