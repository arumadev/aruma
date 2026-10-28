# ArumaRedirect

Point the **official Firebase iOS SDK**, in its normal production mode (no `useEmulator`), at an
Aruma server. Add the package and one Info.plist key; no app code.

```xml
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

## How it works

Firebase Auth on iOS has its hosts compiled in. Aruma serves Google's exact paths under its own
host (ADR-014), so a redirect is only a host rewrite:

| The SDK requests | ArumaRedirect sends |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<ArumaURL>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<ArumaURL>/securetoken.googleapis.com/<path>` |
| `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` | `<ArumaURL>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` |
| `https://firebasestorage.googleapis.com/v0/<path>` (Cloud Storage, the fallback below) | `<ArumaURL>/firebasestorage.googleapis.com/v0/<path>` |

- A `URLProtocol` handles those families and nothing else. Every other request
  (Installations, Analytics, Firestore, …) passes through untouched.
- It keeps the method, headers, query and body. It reads `httpBodyStream`, which is how
  URLProtocol usually receives a body.
- It performs the request with its own URLSession. That session never contains the protocol, and
  forwarded requests are marked, so nothing loops. The response, data and errors are relayed to the
  SDK under the original Google URL. Timeouts, cancellation and server redirects behave as usual.
- It installs itself before `main`: an Objective-C `+load` in the `ArumaRedirectLoader` target
  reads `ArumaURL` and registers the protocol. Then it puts the protocol first in
  `URLSessionConfiguration.default` and `.ephemeral`, which is where Firebase's sessions come from.
  `URLProtocol.registerClass` alone only reaches `URLSession.shared`.

## Install

**Swift Package Manager (Xcode):** add the package, then the `ArumaRedirect` product to the app target.

**CocoaPods, including Flutter apps:** in `ios/Podfile`, inside the app target:

```ruby
pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'
```

Flutter needs no plugin: there is no Dart API. The pod is linked into Runner, and `+load` runs
when the app starts. In Flutter's Swift Package Manager mode, add the package in Xcode instead.

Then add `ArumaURL` to `Info.plist`. Keep `GoogleService-Info.plist` from Aruma.

## Cloud Storage

The production path for Storage is the **patched FirebaseStorage** from the SwiftPM mirror
([`../firebase-storage-patch`](../firebase-storage-patch), ADR-022 §16.3). It reads the same
`ArumaURL`, talks to Aruma directly, and `downloadURL()` returns the unified form
`https://<aruma-host>/v0/b/<bucket>/o/<name>?alt=media&token=<uuid>`, which works in any loader
(`AsyncImage`, `AVPlayer`, `WKWebView`, a shared link) without passing through Google. This package
adds four things:

- **The launch check.** SwiftPM can silently resolve the official firebase-ios-sdk (no
  `mirrors.json`, or the mirror URL named next to the official one), and resolution still
  succeeds. At install, ArumaRedirect looks for `FIRStorage` and the patched build's marker class
  `FIRArumaStorageBuild` (`ArumaRedirect.firebaseStorageBuild`: `.absent`, `.patched` or
  `.official`). The official build is logged in a loud banner. With `ArumaStorageRedirect` false it
  is refused: the app stops at launch (`install(url:)` in code throws
  `ArumaRedirectError.officialFirebaseStorage` and blocks every Auth and Storage request). In CI,
  [`check-firebase-storage.sh`](../firebase-storage-patch/check-firebase-storage.sh) fails the
  build before that.
- **The fallback.** Requests of an official FirebaseStorage to `firebasestorage.googleapis.com` are
  rewritten like Auth's, and the first one logs that the build is the official one. Uploads,
  downloads, metadata, list and delete then work, but `downloadURL()` still names Google
  (`https://firebasestorage.googleapis.com:443/…`), and anything outside the app's URLSessions
  sends it there. Set `ArumaStorageRedirect` to `false` in an app built with the patch to make
  the patch mandatory. Any request to that host that cannot be rewritten (dot segments, http,
  another port) fails rather than reaching Google, in both modes.
- **`ArumaUrls.fromArumaUrl(url, bucket:)`** checks a download URL (the unified form, Google's,
  with or without `:443`) and returns the `gs://<bucket>/<name>` that `Storage.reference(forURL:)`
  reads back to the same object. `reference(forURL:)` ends the app (`fatalError`) on a URL it
  cannot parse or on another bucket; this throws instead. The name is written raw, not
  percent-encoded, because FirebaseStorage reads a `gs://` name as it is. Names that FirebaseStorage
  would collapse into another object (an empty segment, a leading or trailing `/`) are refused.

  ```swift
  let ref = storage.reference(forURL: try ArumaUrls.fromArumaUrl(link, bucket: storage.reference().bucket))
  ```

  `ArumaUrls.toArumaUrl(url)` moves a link on Google's host to the unified form on `ArumaURL`, for
  apps on the fallback.
- **CocoaPods.** [`Scripts/aruma_storage.rb`](Scripts/aruma_storage.rb) applies the same patch to
  the FirebaseStorage pod, for the releases still published to CocoaPods (the last is 12.19.0;
  Firebase stopped publishing there in October 2026). `pod install` fails if a line of the patch
  is not found exactly once:

  ```ruby
  post_install do |installer|
    require_relative '<aruma>/sdk/ios/ArumaRedirect/Scripts/aruma_storage'
    ArumaStorage.patch!(installer)
  end
  ```

  It reads the patch from `<aruma>/sdk/ios/firebase-storage-patch/`, so use it from a checkout of
  this repository (`ARUMA_STORAGE_PATCH` names another copy).

## Configuration (Info.plist)

| Key | Type | Meaning |
|---|---|---|
| `ArumaURL` | String | The Aruma server. Absent or empty: the package does nothing. The patched FirebaseStorage reads it too, and needs it at the root of the host (no path). |
| `ArumaAllowInsecureURL` | Bool | Accept `http` to a host that is not loopback. Development only. |
| `ArumaRedirectLog` | Bool | Log each rewrite: method, host and path. |
| `ArumaStorageRedirect` | Bool | Default `true`: rewrite an official FirebaseStorage's requests. `false`: the patch is mandatory, and an app linking the official FirebaseStorage stops at launch. |

Safety:
- **https only.** `http` is accepted only for `localhost`, `127.0.0.0/8` and `::1`, unless
  `ArumaAllowInsecureURL` is set.
- **Fails closed.** When `ArumaURL` is present but refused, Auth and Storage requests fail with
  `URLError.cannotConnectToHost`. They are never sent to Google with the user's password.
- **Logs.** Logs never contain the query (it holds the API key), headers, tokens or bodies.

## Code instead of Info.plist

```swift
import ArumaRedirect

try ArumaRedirect.install(url: URL(string: "https://aruma.example.com")!)
```

Call it before Firebase sends its first Auth request, for example first in
`application(_:didFinishLaunchingWithOptions:)`. Leave `ArumaURL` out of Info.plist; the loader
then does nothing.

## Limits

- **Background URLSessions** ignore custom protocols. Firebase Auth does not use them.
  FirebaseStorage's `putFile` on a device sends its chunks from one, to the upload URL the server
  returned; Aruma returns an absolute URL to itself, so they reach Aruma either way.
- **The fallback buffers bodies** up to 16 MiB. The patched FirebaseStorage does not go through it.
- **Auth, and Storage as a fallback.** Firestore uses gRPC on its own sockets, not URLSession, so a
  URLProtocol cannot redirect it. Later phases need another mechanism.
- **The SDK's URLs are an implementation detail.** If a Firebase release moves Auth to new hosts
  or paths, they pass through unchanged and fail against Google with Aruma's API key. Nothing
  leaks to Aruma. Pin the Firebase version and run the conformance suite before upgrading.
- **Other Firebase pods still call Google.** With Analytics, Crashlytics or Remote Config in the
  app, the Installations SDK sends one `POST https://firebaseinstallations.googleapis.com/…` at
  launch, even with data collection off. It carries no Auth data. Leave those pods out if the
  app must not contact Google at all.

## Tests

```sh
swift test
```

The tests cover URL rewriting, body streams, forwarding through the protocol against a fake
server (no network), errors, timeouts, cancellation, redirects, Info.plist parsing, the
`+load` loader, the Storage fallback and `ArumaUrls`. The CocoaPods helper has its own:
`ruby Scripts/aruma_storage_test.rb`.

## License

Apache-2.0.
