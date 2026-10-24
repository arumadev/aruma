# FirebaseStorage for Aruma (iOS)

The official FirebaseStorage for iOS has Google's host compiled in: requests go to
`firebasestorage.googleapis.com`, and `downloadURL()` returns
`https://firebasestorage.googleapis.com:443/v0/b/…`. A run-time redirect (ArumaRedirect) can move
the requests, but not that link: `AsyncImage`, `AVPlayer`, `WKWebView` or anyone it is shared with
still sends it to Google. So Aruma's production path on iOS (ADR-022 §16.3, the owner's decision of
a build-time host on every platform) is FirebaseStorage built from source with one small patch,
delivered through a SwiftPM **mirror**.

## The patch

[`storage-host.patch`](storage-host.patch), against `FirebaseStorage/Sources` of firebase-ios-sdk:

- `Storage.swift` (`init`, line 287 in 12.19.x): the scheme, host and port come from `ArumaURL` in
  the app's Info.plist, the key ArumaRedirect reads too
  (new file `Internal/ArumaStorageOrigin.swift`). The host is written in lower case, and the
  scheme's default port is dropped.
- `var port: Int` becomes `Int?`: `nil` when `ArumaURL` has no port, so no port is written.
  It reaches the four places the port is used: `Storage.swift:225` (`useEmulator`) and `:287`,
  `Internal/StorageGetDownloadURLTask.swift:64` (the download URL) and `Internal/StorageUtils.swift:31`
  (every request). The download URL is then the unified form, literally:
  `https://<aruma-host>/v0/b/<bucket>/o/<name>?alt=media&token=<uuid>`.
- `Internal/StorageFetcherService.swift`: `http` is allowed when `ArumaURL` is `http` (loopback, or
  `ArumaAllowInsecureURL` for development). FirebaseStorage attaches tokens over `http` to
  loopback only, as before.
- **Without `ArumaURL`** FirebaseStorage behaves exactly as the official one
  (`https://firebasestorage.googleapis.com`, port 443) and logs one line saying so; ArumaRedirect, if
  present, catches those requests. FirebaseStorage's own unit tests pass unchanged.
- **A refused `ArumaURL`** (not `scheme://host[:port]`, a path, a query, `http` to another host,
  a port outside 1-65535, or a Google host: `*.googleapis.com`, `*.firebasestorage.app`,
  `*.appspot.com`) is logged, and Storage uses `arumaurl.invalid`, which never resolves: it fails
  instead of reaching Google. Aruma serves Storage from the root of its host, so `ArumaURL` has no
  path.
- **The marker:** the Objective-C class `FIRArumaStorageBuild` exists only in the patched build.
  ArumaRedirect checks it at launch (see its README).
- `Tests/Unit/ArumaStorageOriginTests.swift`: `ArumaURL` without a port gives `url.port == nil` in
  requests and download URLs, plus the cases above.

Every modified file says so in its header (Apache-2.0 §4(b)).

## The mirror

[`make-mirror.sh <upstream-tag>`](make-mirror.sh) clones the official tag, applies the patch
(`git apply --check` first), runs `FirebaseStorageUnit` (the official tests and the patch's) on
macOS, and pushes **one commit with no parent, tagged with the official name itself**:
`12.19.3` in the mirror is the patched 12.19.3. The mirror never carries an unpatched commit or
tag, and the commit has a fixed identity (The Aruma Authors) and the official commit's date, so
rebuilding it on any machine gives the same revision.

**A published tag is never moved.** SwiftPM records each tag's revision on first use
(`~/Library/org.swift.swiftpm/security/fingerprints`), and a machine that saw `12.19.3` refuses a
moved one: "Revision … does not match previously recorded value" (measured, Xcode 16.4). So
`make-mirror.sh` refuses to move a tag, and a fix to the patch ships with the next official
release's tag.

```sh
ARUMA_MIRROR=git@github.com:arumadev/firebase-ios-sdk.git sdk/ios/firebase-storage-patch/make-mirror.sh 12.19.3
```

Why the official names, not `12.19.3-aruma.1`: SwiftPM resolves `from: "12.19.3"`, a range, and
FlutterFire's `exact:` to the official tag, never to a pre-release of it. With the same names, no
requirement changes anywhere. [`MIRROR-NOTICE.md`](MIRROR-NOTICE.md) goes into each commit as
`ARUMA-NOTICE.md`.

## Using it

Point SwiftPM at the mirror; `Package.swift`, the Xcode project and FlutterFire keep the official
URL and version:

```sh
# A Swift package
swift package config set-mirror \
  --original https://github.com/firebase/firebase-ios-sdk \
  --mirror https://github.com/arumadev/firebase-ios-sdk
```

An Xcode project reads its mirrors from
`<App>.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/configuration/mirrors.json` (or the
`.xcworkspace`'s `xcshareddata/swiftpm/configuration/`). `swift package config set-mirror` writes
that file for a package; run it in any directory with a `Package.swift` and copy
`.swiftpm/configuration/mirrors.json` there, as `test/e2e/ios-storage/run.sh` does. A
`Package.resolved` keeps the official URL as `location` with the mirror's revision; one that pins
the official revision is re-pinned to the mirror's (measured with SwiftPM 6.1.2), and the e2e
resolves afresh anyway.

**The CI gate (recommended).** SwiftPM never refuses the official package, so check what it
resolved: [`check-firebase-storage.sh`](check-firebase-storage.sh) fails when the
firebase-ios-sdk checkout has no `ARUMA-NOTICE.md`. Run it in CI after resolution, or as the app
target's first "Run Script" build phase (User Script Sandboxing off for that target):

```sh
"${SRCROOT}/<aruma>/sdk/ios/firebase-storage-patch/check-firebase-storage.sh"            # Xcode's SourcePackages
sdk/ios/firebase-storage-patch/check-firebase-storage.sh <-clonedSourcePackagesDirPath>  # CI
```

The e2e app has it as its first build phase. At run time, ArumaRedirect also checks the build
(the `FIRArumaStorageBuild` class) and, with `ArumaStorageRedirect` false, stops the app.

Then add `ArumaURL` to the app's Info.plist (and ArumaRedirect for Auth):

```xml
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

Measured on this repository's machine (Xcode 16.4, Swift 6.1.2); see
`test/e2e/ios-storage/README.md` for the numbers:

- `xcodebuild` honours the project's `mirrors.json`; SwiftPM resolves `exact:`, `from:` and a range
  to the mirror's patched commit.
- `flutter build ios` in Swift Package Manager mode honours `mirrors.json` in
  `ios/Runner.xcworkspace/xcshareddata/swiftpm/configuration/`.
- **Do not name the mirror URL directly instead.** With the mirror URL in the app and the official
  URL in another package (FlutterFire's `exact:`), SwiftPM neither refuses nor warns: it silently
  checks out the official, unpatched repository for the identity `firebase-ios-sdk`, in either
  order. That alternative is safe only for an app whose graph names firebase-ios-sdk nowhere else.

**CocoaPods** (the frozen releases Firebase still published there, the last being 12.19.0):
`sdk/ios/ArumaRedirect/Scripts/aruma_storage.rb` applies this same patch in `post_install`.
**Firebase.zip / xcframeworks** are prebuilt binaries and cannot be patched: they take the
run-time fallback (ArumaRedirect) alone, with download URLs on Google.

## License

firebase-ios-sdk is Copyright Google LLC, under the Apache License 2.0; the patched files keep that
license, and the patch and these scripts are Apache-2.0 too. See `NOTICE` at the repository root.

---

## العربية

مكتبة FirebaseStorage الرسمية لـ iOS تحمل نطاق Google في كودها، فيعيد `downloadURL()` رابطًا على Google بـ `:443`، ولا
يحوّله التحويل وقت التشغيل. فمسار الإنتاج على iOS (ADR-022 §16.3، قرار المالك: تبديل النطاق وقت البناء في كل منصة) هو
FirebaseStorage مبنية من المصدر برقعة صغيرة، تُسلَّم عبر **مرآة** SwiftPM.

- **الرقعة** (`storage-host.patch`): `Storage.swift` يقرأ المخطط والمضيف والمنفذ من `ArumaURL` في Info.plist (الملف الجديد
  `Internal/ArumaStorageOrigin.swift`)، و`var port: Int` يصير `Int?` فلا يُكتب منفذ حين لا منفذ في `ArumaURL`، في المواضع
  الأربعة (`Storage.swift:225` و`:287`، و`StorageGetDownloadURLTask.swift:64`، و`StorageUtils.swift:31`). و`http` مسموح حين
  يكون `ArumaURL` على loopback. وبلا `ArumaURL` يبقى سلوك المكتبة الرسمي مع سطر سجل، وتمر اختباراتها الرسمية. و`ArumaURL`
  المرفوض يذهب إلى `arumaurl.invalid` فيفشل ولا يصل إلى Google.
- **المرآة** (`make-mirror.sh <tag>`): commit واحد بلا أب، موسوم بالاسم الرسمي نفسه، بعد مرور `FirebaseStorageUnit`. ولا وسم
  غير مرقّع فيها. وتاريخه تاريخ الـ commit الرسمي، فإعادة بنائها تعطي المراجعة نفسها.
- **الاستعمال:** `swift package config set-mirror --original https://github.com/firebase/firebase-ios-sdk --mirror
  https://github.com/arumadev/firebase-ios-sdk`، وفي مشروع Xcode ملف `mirrors.json` تحت
  `xcshareddata/swiftpm/configuration/`، وتُضاف `ArumaURL` إلى Info.plist. ويحترم `xcodebuild` و`flutter build ios` المرآة (مقيس).
- **بوابة CI:** `check-firebase-storage.sh` يُفشل البناء إن لم يكن في نسخة firebase-ios-sdk المحلولة `ARUMA-NOTICE.md`، في CI
  أو مرحلة "Run Script" أولى في هدف التطبيق. وفي التشغيل تبحث `ArumaRedirect` عن صنف العلامة `FIRArumaStorageBuild`.
- **لا يُكتب رابط المرآة مكان الرسمي:** إن طلبت حزمة أخرى (FlutterFire) الرابط الرسمي، لا يرفض SwiftPM ولا يحذّر، بل يأخذ
  المستودع الرسمي غير المرقّع بصمت (مقيس بـ SwiftPM 6.1.2). فـ `set-mirror` وحده الطريق الآمن.
- **CocoaPods** للإصدارات المجمّدة (آخرها 12.19.0) بـ `aruma_storage.rb`، و**Firebase.zip** يأخذ الاحتياط وحده.
- **الرخصة:** Apache-2.0، وكل ملف معدَّل يذكر التعديل في رأسه، والإشعار في `NOTICE`.
