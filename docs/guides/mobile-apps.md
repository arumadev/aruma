# Pointing mobile apps at Aruma

Your Android, iOS and Flutter apps keep the official Firebase SDKs. You do not fork them and you do not change your app's Dart, Kotlin or Swift code to call Aruma. A few small Aruma packages redirect the SDKs' requests to your server, and the config files tell the SDKs which project and database to use.

The packages are not published to Maven, CocoaPods or pub.dev yet. Install them from your clone of this repository, as below and in [SDK packages](../sdks.md). In the paths below, `<aruma>` is your clone.

## What you need per service

| Service | Android | iOS | Flutter |
|---|---|---|---|
| Auth | `aruma-redirect` and the `ArumaURL` manifest line | `ArumaRedirect` and the `ArumaURL` Info.plist key | Both native libraries |
| Realtime Database | Only the config file | Only the config file | Only the config files |
| Cloud Storage | `aruma-redirect` and the `dev.aruma.storage` Gradle plugin | `ArumaRedirect`, the patched FirebaseStorage mirror, and `ArumaURL` | Both, plus `aruma_storage` |
| Firestore | The host set in code | The host set in code | The host set in code |

The status of each platform is in [compatibility](../reference/compatibility.md). Everything on mobile was tested on emulators and simulators. It has not been tested on real devices.

## 1. Get the config file

In the dashboard, open your project and choose **Add app**. Pick Android (enter the package name) or iOS (enter the bundle ID), and register it. Then download the config file:

- **Android:** `google-services.json`. It holds every Android app in the project, so one file serves all of them. Move it into your app module's directory (usually `app/`) and apply the Google services Gradle plugin, which reads it at build time.
- **iOS:** `GoogleService-Info.plist`. Drag it into the root of your Xcode project and add it to every target. Call `FirebaseApp.configure()` when the app starts.
- **Flutter:** use both files in the Android and iOS projects. Keep the `firebase_options.dart` that Aruma generates.

The dashboard generates the file fresh each time you download it, so download it again whenever you need it. The same files come from the command line: `arumactl apps sdkconfig` (see [getting started](../getting-started.md)).

These files carry your project's API key, which has the form `Aru...` and is 39 characters long. Treat it as opaque. They also carry the Realtime Database address (`firebase_url` in `google-services.json`, `DATABASE_URL` in `GoogleService-Info.plist`) when that service is on. They do not carry the Firestore host.

The API key can be restricted to your app: by Android package name and SHA-1 certificate fingerprint, and by iOS bundle ID. A correct restriction passes and a wrong one is refused.

## 2. How the SDKs are redirected

The Auth SDKs have Google's hosts compiled in. Aruma serves the same paths under its own host, so a redirect is only a host rewrite. The Aruma libraries do that rewrite, and nothing else.

### Android: `aruma-redirect`

The library reads `ArumaURL` from your manifest, and sends the Firebase SDK's Auth requests to Aruma. Requests to hosts it does not know pass through untouched. Without `ArumaURL`, it installs nothing.

Build it once from your clone:

```sh
cd <aruma>/sdk/android/aruma-redirect
./gradlew :aruma-redirect:publishToMavenLocal
```

Then add it to the app:

```kotlin
// settings.gradle.kts or the root build.gradle.kts
repositories { mavenLocal() }

// app/build.gradle.kts
dependencies { implementation("dev.aruma:aruma-redirect:0.1.0") }
```

```xml
<!-- AndroidManifest.xml -->
<application ...>
    <meta-data android:name="ArumaURL" android:value="https://aruma.example.com" />
</application>
```

You can set the URL in code instead, before Firebase's first request, for example in `Application.attachBaseContext`: `ArumaRedirect.install("https://aruma.example.com")`.

Other manifest keys:

| Name | Value | Meaning |
|---|---|---|
| `ArumaAllowInsecureURL` | `true` | Accept `http` to any host. Development only. |
| `ArumaRedirectLog` | `true` | Log the host and path of each redirected request. Never the query, headers or body. |
| `ArumaStorageRedirect` | `false` | Refuse Storage requests to Google's host instead of redirecting them. Use it when you build with the Gradle plugin (below). Default `true`. |

The library fails closed. If `ArumaURL` is set but invalid, or another library already installed a process-wide URL stream handler factory, the app stops at launch with an `ArumaRedirectException` that says why. It does not let a password go to Google.

It works on the transport the current Firebase Auth SDK uses (`HttpURLConnection`). If a future Firebase Auth release changes transport, the redirect would not see those requests. Pin the Firebase version and test each upgrade.

### Android: `aruma-storage-gradle`

Cloud Storage for Firebase builds its requests and its download links from one host constant. The Gradle plugin `dev.aruma.storage` replaces that constant with your server when the app is built. Requests go to Aruma, and `getDownloadUrl()` returns an Aruma link that any image loader, player, WebView or shared link can open.

Build it once from your clone:

```sh
cd <aruma>/sdk/android/aruma-storage-gradle
./gradlew publishToMavenLocal
```

```kotlin
// settings.gradle.kts
pluginManagement {
    repositories { mavenLocal(); google(); mavenCentral(); gradlePluginPortal() }
}

// root build.gradle.kts
plugins { id("dev.aruma.storage") version "0.1.0" apply false }

// app/build.gradle.kts
plugins {
    id("com.android.application")
    id("dev.aruma.storage")
}

arumaStorage {
    url = "https://aruma.example.com"
}
```

The plugin needs the Android Gradle plugin 9 and an application module. The URL is the root of the host, with no path. Use the same server as `ArumaURL`, without a path. `allowInsecure = true` accepts `http` to any host, for development only.

Without the plugin, `aruma-redirect` still redirects Storage requests at run time, so uploads and downloads work. But `getDownloadUrl()` then returns a link on Google's host, which any loader outside `HttpURLConnection` sends to Google. Set `ArumaStorageRedirect` to `false` in the manifest to make the plugin mandatory: an app built without it then stops at launch.

The plugin checks its own work and fails the build if it cannot change the Storage SDK exactly as expected, so it never ships Google's host silently. The tested Storage SDK is firebase-storage 23.0.0 (BoM 35.0.2). A Storage release that changes the constant fails the build instead of passing. It also writes a notice file into the app, because the change modifies an Apache-2.0 library.

### iOS: `ArumaRedirect`

`ArumaRedirect` reads `ArumaURL` from `Info.plist` and rewrites the Firebase SDK's Auth requests to your server. It installs itself before `main`. Every other request (Installations, Analytics, Firestore) passes through untouched.

Add the Swift package from your clone to Xcode and add the `ArumaRedirect` product to the app target. With CocoaPods:

```ruby
pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'
```

```xml
<!-- Info.plist -->
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

Or install in code, first in `application(_:didFinishLaunchingWithOptions:)`, and leave the key out of Info.plist:

```swift
import ArumaRedirect
try ArumaRedirect.install(url: URL(string: "https://aruma.example.com")!)
```

Other Info.plist keys:

| Key | Type | Meaning |
|---|---|---|
| `ArumaAllowInsecureURL` | Bool | Accept `http` to a host that is not loopback. Development only. |
| `ArumaRedirectLog` | Bool | Log the method, host and path of each rewrite. |
| `ArumaStorageRedirect` | Bool | Default `true`. `false` makes the patched FirebaseStorage mandatory: an app that links the official one stops at launch. |

It fails closed too. If `ArumaURL` is present but refused, Auth and Storage requests fail with `URLError.cannotConnectToHost`. They are never sent to Google.

### iOS: the patched Storage SDK

FirebaseStorage on iOS has Google's host compiled in, so a run-time redirect cannot fix the download links it returns. Aruma's way is the official FirebaseStorage built from source with one small patch. It reads the same `ArumaURL`, talks to Aruma directly, and `downloadURL()` returns an Aruma link.

You deliver it through a Swift Package Manager **mirror**. Your `Package.swift`, your Xcode project and FlutterFire keep the official URL and version. A public mirror is not published yet. Build the mirror from your clone with `make-mirror.sh` and host it yourself (see `sdk/ios/firebase-storage-patch/README.md`). The patched version that has been tested is 12.19.3. Then point SwiftPM at your mirror:

```sh
swift package config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk \
  --mirror <your mirror URL>
```

An Xcode project reads its mirrors from `mirrors.json` in `<App>.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/configuration/`. Do not name the mirror URL directly in the app instead. If another package names the official URL, SwiftPM silently checks out the unpatched official repository.

SwiftPM never refuses the official package, so check what it resolved. ArumaRedirect checks at launch, and logs a loud banner if it finds the official build. The script `check-firebase-storage.sh` in the patch folder fails a CI build that resolved anything else. Commit your `Package.resolved` and resolve in CI from it.

For CocoaPods, the frozen releases up to 12.19.0 can take the same patch through `aruma_storage.rb` in the `ArumaRedirect` package, in your `post_install` hook. Prebuilt `Firebase.zip` or xcframeworks cannot be patched. They get the run-time fallback only, with download links on Google's host.

### Flutter

Flutter needs no Aruma Dart package for Auth, Realtime Database or Firestore. The official FlutterFire plugins call the native Android and iOS SDKs, so you set up the two native sides above:

- **Android:** add `aruma-redirect` and `ArumaURL` in `android/app/build.gradle(.kts)` and `android/app/src/main/AndroidManifest.xml`, with `mavenLocal()` in the repositories of `android/build.gradle(.kts)`. For Storage, add the `dev.aruma.storage` plugin in `android/settings.gradle(.kts)` and `android/app/build.gradle(.kts)`.
- **iOS:** add `pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'` to `ios/Podfile`, and `ArumaURL` to `ios/Runner/Info.plist`. For Storage, turn on Swift Package Manager (`enable-swift-package-manager`) and put the mirror in `ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/mirrors.json`.
- **Web:** use `@aruma/web`, as in [auth-web.md](auth-web.md).

Keep the official `firebase_auth` and `firebase_storage` in Dart, with no `useAuthEmulator` and no `useStorageEmulator`.

Flutter Storage needs the `aruma_storage` package for one reason. `firebase_storage` parses http and https links in Dart and accepts only Google's hosts, `localhost` and `10.0.2.2`. So `refFromURL` on an Aruma download link throws. `fromArumaUrl` turns the link into the `gs://` form that `refFromURL` reads:

```yaml
# pubspec.yaml
dependencies:
  aruma_storage:
    path: <aruma>/sdk/flutter/aruma_storage
```

```dart
import 'package:aruma_storage/aruma_storage.dart';

final link = await ref.getDownloadURL();
final same = FirebaseStorage.instance.refFromURL(fromArumaUrl(link));
```

This is why Flutter Storage is marked Partial in [compatibility](../reference/compatibility.md#cloud-storage). `fromArumaUrl` throws `NotStorageUrlException` for anything that is not a Storage link.

## 3. Connecting from a device: addresses and TLS

For any server that is not on your own machine, use **https with a certificate from a public authority**. Use the same public address in `ArumaURL` everywhere.

- iOS trusts only its bundled root certificates. A self-signed certificate or one from a private authority does not connect.
- The Aruma libraries accept `http` only for loopback (`localhost`, `127.0.0.0/8`, `::1`). Any other `http` host needs `ArumaAllowInsecureURL`, and that flag is for development only.
- On Android, a debuggable app may also use `http` to `10.0.2.2` (the emulator's name for your computer) and `10.0.3.2` (Genymotion). On a real device those are ordinary private addresses, so a release build needs https there. Android blocks cleartext traffic by default, so for `http://10.0.2.2:<port>` add a `networkSecurityConfig` that allows it, in debug builds only. If you set the URL in code, pass the debug flag yourself: `ArumaRedirect.install(url, BuildConfig.DEBUG, false)`.
- To reach a server on your computer's `127.0.0.1` from an Android emulator or device, use `adb reverse tcp:<port> tcp:<port>`.
- The iOS Simulator can use `http://127.0.0.1:<port>` or `localhost` with `ArumaURL`.
- Firestore uses gRPC. A TLS proxy in front of Aruma must speak HTTP/2 to it (see [Firestore](firestore.md)).

## 4. Release builds

- **Turn off the development flags.** `ArumaAllowInsecureURL`, a debug-only cleartext config and `http` URLs belong in debug builds only. Use https in release.
- **Android minification (R8).** The library's consumer rules keep what it needs. A release build shrunk by R8 was tested on an API 33 emulator, with Auth and with Storage.
- **Android Storage.** The plugin's verification task runs before the APK or App Bundle is packaged, so neither can be built without it. The App Bundle's derived APKs carry the notice file.
- **iOS Storage.** Run `check-firebase-storage.sh` in CI, and set `ArumaStorageRedirect` to `false` in release builds, so an app that resolved the official FirebaseStorage fails at launch and not in front of a user.
- **Pin Firebase versions.** The redirect depends on how each Firebase SDK builds its requests, and the Storage patches on the exact SDK text. Pin the versions and run your Aruma tests on every upgrade. On Android, a version the plugin cannot change fails the build.
- **Real devices.** Nothing here has been tested on a physical device yet. Test your release build on one before you ship.
- **Other Google calls.** On iOS, Analytics, Crashlytics and Remote Config send one request to `firebaseinstallations.googleapis.com` at launch, even with data collection off. It carries no Auth data. Leave those pods out if the app must never contact Google.

## 5. Notes per service

### Auth

Email and password, anonymous sign-in, linking, custom tokens, password reset, email verification, token refresh and revocation work with the redirect library. Phone sign-in, multi-factor authentication, email-link sign-in, and Facebook and GitHub sign-in are not supported.

On Android, phone auth, Google sign-in through Play services, App Check and reCAPTCHA are not covered by `aruma-redirect`. See [compatibility](../reference/compatibility.md#auth) for the full table, and [Verifying users on your server](auth-admin.md) for the server side.

### Realtime Database

The only setup is the config file, which carries the `databaseURL`. The database address has two forms:

- **Production:** one host per database, such as `https://chat-app-default-rtdb.rtdb.example.com`. The server needs `rtdb.domain`, a wildcard DNS record and certificates. See [operations.md](operations.md#realtime-database).
- **Development:** `http://127.0.0.1:8080/?ns=chat-app-default-rtdb`. On Android it needs `http` permission for the host (a `networkSecurityConfig`) and `adb reverse`. On iOS it works only on `127.0.0.1` or `localhost`: on a host with three labels, the SDK takes the first label and ignores `ns`.

Do not call `useEmulator` and do not pass `mockUserToken`. Details are in [Realtime Database](realtime-database.md).

### Cloud Storage

Use the build-time package on each platform so download links point at Aruma: the Gradle plugin on Android, the patched FirebaseStorage on iOS, and both on Flutter. Details of the service are in [Cloud Storage](storage.md).

### Firestore

Firestore is a **release candidate**, and the official Android and iOS Firestore suites have not run yet. Neither the config files nor `aruma-redirect` / `ArumaRedirect` carry the Firestore host, because Firestore uses gRPC and not `URLSession` or `HttpURLConnection`. Set the host in code before first use. See [Using Firestore with Aruma](firestore.md#android-ios-and-flutter) for the snippets, and [compatibility](../reference/compatibility.md#firestore) for the exact status.
