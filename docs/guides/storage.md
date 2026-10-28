# Connecting your app to Aruma Cloud Storage

Your app keeps the official Firebase Storage SDKs. Aruma serves the same API under its own host. The Storage SDKs have Google's host compiled in, so each platform needs one small build-time step that points them at your server. After that, `getDownloadURL()` returns a link on Aruma, and the app code does not change. This guide is for people who use Cloud Storage for Firebase and are moving their app to Aruma. For all the differences and limits, see [compatibility](../reference/compatibility.md#cloud-storage) and [known differences](../reference/known-differences.md#cloud-storage).

## What works

| Feature | Status |
|---|---|
| Web, Android, iOS | Supported: uploads (simple, multipart, resumable), downloads, `getDownloadURL`, metadata, list, delete, `refFromURL` |
| Flutter | Partial: needs one `fromArumaUrl` call in app code when you read a download link back with `refFromURL` |
| Admin Node (`@google-cloud/storage`) and Admin Go (`cloud.google.com/go/storage`) | Supported, through the GCS JSON API and a service account |
| The `firebase` tool | Supported: `deploy --only storage` and `init storage` |
| Rules | The same language as Firebase. See [Security rules in Aruma](security-rules.md) |
| More than one bucket, signed URLs, `makePublic`, object ACLs | Not supported |

## 1. Turn Storage on

Storage is off by default. In `aruma.yaml`:

```yaml
storage:
  enabled: true
  data_dir: /srv/aruma/storage   # an absolute path on a local disk
```

The same keys are `ARUMA_STORAGE_ENABLED` and `ARUMA_STORAGE_DATA_DIR`. The server does not start with Storage on and no absolute `data_dir`. Restart Aruma after the change.

- **Where files live.** The bytes are files under `data_dir`. Everything that points to them (names, metadata, download tokens, upload sessions) is in PostgreSQL. Keep the two together: mount the disk before Aruma starts, and back up the directory with the database. See the `data_dir` warning in [configuration](../reference/configuration.md#storage).
- **Use https.** Off loopback, serve Storage over https. Aruma logs a warning at startup when it runs on plain `http`.
- **Without it,** the dashboard's Cloud Storage pages say "Cloud Storage is off on this server".

## 2. The bucket

Each project has one bucket: `<project_id>.<project_domain>`. You find it in the config files (`storageBucket` in the web config, `storage_bucket` in `google-services.json`, `STORAGE_BUCKET` in `GoogleService-Info.plist`) and in the dashboard (project > Cloud Storage > Bucket, with a copy button). You cannot create, list or delete buckets.

Changing `project_domain` changes the bucket name, so stored links break. See [known differences](../reference/known-differences.md#across-services).

New projects start with locked rules (`allow read, write: if false;`). Apps cannot read or write any file until you [publish your rules](#7-storage-rules).

## 3. Connect each platform

Keep the config file that Aruma generates for your app. Do not use `connectStorageEmulator`, `useStorageEmulator` or `useEmulator`. Aruma does not accept the emulators' unsigned tokens.

The install lines below are from source. The packages are not published yet. Use your clone of this repository, as [SDK packages](../sdks.md) describes.

### Web

```sh
npm install --save-dev /path/to/aruma/sdk/web/aruma-web
```

Add one line to the bundler's config. For Vite:

```js
// vite.config.js
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'https://aruma.example.com', storage: true })], // your server's public_url
});
```

The plugin rewrites Google's Storage host inside `@firebase/storage` while it builds. The URL is the root of your server, with no path. The app stays as it is:

```js
const app = initializeApp(firebaseConfig); // the Aruma config file
const storage = getStorage(app);           // no connectStorageEmulator
const r = ref(storage, `users/${uid}/photo.png`);
await uploadBytes(r, file, { contentType: 'image/png' });
const url = await getDownloadURL(r);       // https://aruma.example.com/v0/b/<bucket>/o/...?alt=media&token=...
```

- **An app that uses Aruma Auth must turn Storage on.** Without `storage: true`, the Vite plugin stops a build that includes `@firebase/storage`. The reason: the Storage SDK sends the user's ID token with every request, and it would go to Google.
- **Other bundlers:** `arumaStorage.webpack({ url })`, `.esbuild`, `.rollup` and `.rspack` (from `@aruma/web/unplugin`), `withArumaStorage(config, { url })` for Next.js (`@aruma/web/next`) and Expo (`@aruma/web/metro`). For Flutter web run `npx aruma-web flutter-web --url <url>` once, then `flutter build web`. The package README lists the tested versions.
- **The build fails** instead of shipping Google's host, for example when the SDK comes from a CDN. Import `firebase/storage` from npm.
- **Links stored before the switch** are on `firebasestorage.googleapis.com`, and `ref()` throws `storage/invalid-url` on them. Convert them with `toArumaUrl(link, 'https://aruma.example.com')` from `@aruma/web/storage`.
- **A fallback** for a build the plugin cannot reach (`installStorage` and `useArumaStorage` from `@aruma/web/storage`) redirects the requests. See the package README.

### Android

Android needs the Gradle plugin, and `aruma-redirect` for Auth. Build both from source in your clone:

```sh
cd /path/to/aruma/sdk/android/aruma-storage-gradle
./gradlew publishToMavenLocal
```

```kotlin
// settings.gradle.kts
pluginManagement {
    repositories { mavenLocal(); google(); mavenCentral(); gradlePluginPortal() }
}
```

```kotlin
// app/build.gradle.kts
plugins {
    id("com.android.application")
    id("dev.aruma.storage") version "0.1.0"
}

arumaStorage {
    url = "https://aruma.example.com"
}
```

Use the same server as `ArumaURL` in the manifest (from `aruma-redirect`, see its README). The plugin changes the one string that holds Google's base URL in `firebase-storage`, so requests and download links are on Aruma. The app code stays official:

```java
StorageReference ref = FirebaseStorage.getInstance().getReference("users/" + uid + "/photo.png");
ref.putBytes(bytes).addOnSuccessListener(t -> ref.getDownloadUrl());
```

- It needs the Android Gradle plugin 9 and was tested with `firebase-bom:35.0.2` (`firebase-storage` 23.0.0). The build fails if a newer release changes the class it patches.
- Android blocks `http` by default. For a development server, allow it in a debug-only `network_security_config`.
- Flutter apps put the same lines in `android/settings.gradle(.kts)` and `android/app/build.gradle(.kts)`.
- Without the plugin, `aruma-redirect` redirects Storage requests at run time, but `getDownloadUrl()` still names Google. Set `ArumaStorageRedirect` to `false` in the manifest to make the plugin mandatory.

### iOS

iOS uses a patched copy of the official FirebaseStorage, delivered through a SwiftPM mirror. Your `Package.swift` and the Xcode project keep the official URL and version.

1. Build the mirror from your clone with `sdk/ios/firebase-storage-patch/make-mirror.sh <tag>` and host it yourself. A public mirror is not published yet.
2. Point SwiftPM at it:

```sh
swift package config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk \
  --mirror <your mirror URL>
```

For an Xcode project, put the resulting `mirrors.json` in `<App>.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/configuration/`.

3. Add `ArumaURL` to `Info.plist`, the same key `ArumaRedirect` uses for Auth:

```xml
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

The app code stays official:

```swift
let ref = Storage.storage().reference(withPath: "users/\(uid)/photo.png")
_ = try await ref.putDataAsync(data)
let url = try await ref.downloadURL()
```

- Without `ArumaURL` the patched build behaves like the official one. A refused `ArumaURL` (a path, `http` to another host, a Google host) makes Storage fail instead of reaching Google.
- Commit your `Package.resolved`, and run `check-firebase-storage.sh` in CI to prove the mirror was used. Its README explains both.
- Prebuilt binaries (Firebase.zip, xcframeworks) cannot be patched. They only get the run-time fallback, with download links on Google.

### Flutter

`firebase_storage` calls the native SDKs on Android and iOS, so set up both platforms as above. Keep the `firebase_options.dart` that Aruma generates. Then add the helper package, for example with a path dependency:

```yaml
# pubspec.yaml
dependencies:
  aruma_storage:
    path: /path/to/aruma/sdk/flutter/aruma_storage
```

`getDownloadURL()` returns an Aruma link. `firebase_storage` accepts only Google's hosts in `refFromURL`, so convert the link first:

```dart
import 'package:aruma_storage/aruma_storage.dart';

final link = await ref.getDownloadURL();
final same = FirebaseStorage.instance.refFromURL(fromArumaUrl(link));
```

`fromArumaUrl` turns an Aruma or a Google link into the `gs://<bucket>/<path>` form. This is why Flutter is "Partial" in [compatibility](../reference/compatibility.md#cloud-storage). For Flutter web, use the web steps.

### Admin SDKs on your server

The official Admin SDKs reach Aruma only through a service account, through the GCS JSON API. A personal CLI token or a user token is refused with 401. Download the service account key from the dashboard.

**Node:** `@aruma/admin-storage` replaces the import of `firebase-admin/storage`. Its `getDownloadURL` is the one to use: the official one sends the token to Google.

```sh
npm install /path/to/aruma/sdk/node/admin-storage firebase-admin@^14
```

```js
import { initializeApp, cert } from 'firebase-admin/app';
import { getStorage, getDownloadURL } from '@aruma/admin-storage';

initializeApp({ credential: cert('service-account.json'), storageBucket: 'my-app.aruma.example.com' });
const file = getStorage().bucket().file('photos/cat.png');
await file.save(data, { contentType: 'image/png' });
const url = await getDownloadURL(file);
```

**Go:** `adminstorage` is separate from `firebase.App`.

```sh
go get github.com/arumadev/aruma/sdk/go/adminstorage
```

```go
client, err := adminstorage.NewClient(ctx,
	adminstorage.WithCredentialsFile("service-account.json"),
	adminstorage.WithDefaultBucket("my-app.aruma.example.com"))
bucket, err := client.DefaultBucket()
w := bucket.Object("photos/cat.png").NewWriter(ctx)
// write, then Close
url, err := client.DownloadURL(ctx, bucket.Object("photos/cat.png"))
```

Both find the server from `ARUMA_URL` or the key file's `token_uri`. Both refuse to start when `STORAGE_EMULATOR_HOST` or `FIREBASE_STORAGE_EMULATOR_HOST` is set. Signed URLs are not supported: use the download URL. Unsupported calls (`copyTo`, `compose`, `moveTo`, ACLs, holds, retention, bucket create and delete) answer 501. Copy works through `rewriteTo`.

### The `firebase` tool

The tool needs Aruma's address and a CLI token from the dashboard (Account > CLI tokens). Use https.

```sh
A=https://aruma.example.com
export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
  FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A \
  FIREBASE_TOKEN=<CLI token> FIREBASE_SKIP_INFORMATIONAL_IAM=1
firebase init storage --project chat-app
firebase deploy --only storage --project chat-app
```

## 4. Download URLs and tokens

`getDownloadURL()` returns:

```
https://aruma.example.com/v0/b/<bucket>/o/<path>?alt=media&token=<token>
```

It works in an `<img>`, a player, a WebView or a shared link. Things to know:

- **A link skips the rules.** Anyone who has the link can download the file. Treat the link as a secret.
- **The first request creates the token.** An object gets its first token when an app first asks for a link.
- **Overwriting a file creates a new token** (through the Firebase SDK API), so the old link stops working. Read the new link after each upload.
- **Revoke or create links** on the file's page in the dashboard. Revoking the last link makes a new one in its place.
- **Tokens count toward the 8 KiB custom metadata limit** (about 200 tokens on an object with no other metadata).
- **Clearing custom metadata** through the Firebase SDK keeps the tokens. Through the GCS API it clears them too. Do not do that for files whose links you share.
- **Cross-origin reads need CORS.** `fetch`, `getBytes` and `getBlob` from another origin fail until you set CORS on the bucket (dashboard > Cloud Storage > CORS, in the `cors.json` format of `gsutil`). The default is no rule. Images and video in a page need none.
- **Aruma serves files safely.** Content is sent under a sandboxing policy, PDFs download instead of rendering, and requests from service workers are refused with 403. HTML and SVG you upload cannot run scripts on Aruma's origin.

## 5. Uploads and size limits

Simple, multipart and resumable uploads work with the official SDKs. Pause, resume and cancel work. A 3 MB resumable upload is one of the operations the web, Android and iOS tests run. Defaults (the operator can change them under `storage.limits`, see [configuration](../reference/configuration.md#storage)):

| Limit | Default |
|---|---|
| One object | 5 GiB (at most 5 TiB, and at most the project quota) |
| A project's quota, unfinished uploads included | 5 GiB (`0` is unlimited) |
| Custom metadata per object | 8 KiB |
| An upload session | 7 days; 1 hour without progress frees its reservation; 10 minutes if it received no byte |
| Disk space kept free | 2 GiB |
| Listing page | 1000 results |

- **Slow uploads are cut.** A body slower than 4 KiB per second over 30 seconds gets 408. The resumable protocol continues from where it stopped.
- **Abuse limits.** Live upload sessions are capped per project (1000), per user (50) and per client address (100), and upload starts per address (60 a minute). A large batch from one client may get 429.
- **Quota.** When the project's quota is used up, uploads fail. The dashboard shows the use.

## 6. Preconditions and the GCS API

Preconditions (`ifGenerationMatch` and others) work. GCS XML API and signed POST policy uploads are not served. Upload through an SDK or the resumable API.

## 7. Storage rules

Rules decide what your app's users reach. Admin SDKs, the dashboard, a service account and the owner's CLI token are not bound by them. Download links skip them.

- **Deploy with the tool:** set `{"storage": {"rules": "storage.rules"}}` in `firebase.json`, then `firebase deploy --only storage`. Deploying a file that has not changed says "already up to date". A compile error is printed as `[E] line:column`.
- **Or use the dashboard:** project > Cloud Storage > Rules. It starts from Locked, Per-user folders or Any signed-in user. **Check** compiles the rules without saving. **Publish** saves them.
- **A project with no published rules is closed.**

A sample that gives each signed-in user a folder (the Android end-to-end test uses it):

```
rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /users/{uid}/{rest=**} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
  }
}
```

For the language, the deploy paths, `get()` lookups into Firestore and testing, see [Security rules in Aruma](security-rules.md).

## 8. The dashboard's Storage pages

Open your project in the dashboard and choose **Cloud Storage** in the left menu. Its pages:

| Page | What you do |
|---|---|
| Files | Browse folders, upload files (the page shows each upload's progress, with retry), create a folder, download and delete. A folder exists while a file is in it |
| A file's page | See its size, type, generation and a preview. Edit its Content-Type, Cache-Control, Content-Disposition, Content-Encoding, Content-Language and custom metadata. Create or revoke download links |
| Rules | Edit, check and publish `storage.rules` |
| Usage | See used space, uploads in progress, the number of files, the quota and the limits |
| CORS | Edit the bucket's CORS rules as JSON |

Dashboard writes are admin writes: they bypass the rules. Deleting a file stops its download links. Replacing a file through an upload does too.

## Known limits

The full list is in [known differences](../reference/known-differences.md#cloud-storage). The ones that matter most:

- One default bucket per project. A copy to another bucket answers "The specified bucket does not exist."
- Every Storage SDK needs the build-time step above. Without it, the SDK talks to Google.
- Signed URLs, signed POST policies, the XML API, `makePublic` (501) and object ACLs are not served. Rewrite tokens answer 400.
- Bucket CORS governs `alt=media` downloads only. Matching is literal or `*`, with no patterns.
- A single Aruma process owns the data directory. Do not put it on NFS or other shared file systems.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `storage/unauthorized` right away, or the web build stops at `@firebase/storage` | Storage was not turned on in the build step, so the SDK would send the ID token to Google | Add `storage: true` (Vite) or the Storage plugin for your bundler |
| `storage/invalid-url` on a stored link | The link is on `firebasestorage.googleapis.com` | Convert it with `toArumaUrl` |
| `refFromURL` throws in Flutter | `firebase_storage` accepts Google hosts only | Wrap the link with `fromArumaUrl` |
| `storage/unauthorized` or 403 on an app request | The rules do not allow it, or none are published | Publish rules and test with a real signed-in user |
| `fetch` or `getBytes` fails in the browser, but `<img>` works | No CORS rule on the bucket | Set CORS in the dashboard |
| Admin SDK gets 401 | The token is a CLI token or a user token | Use a service account |
| The server will not start | `storage.enabled` is on with no absolute `data_dir` | Set `storage.data_dir` |
| The old link stopped working | The file was overwritten, or its link was revoked | Read the new link from `getDownloadURL()` |
| 408 on an upload | Slower than 4 KiB per second for 30 seconds | Retry. The resumable upload continues |
