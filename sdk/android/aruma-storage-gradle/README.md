# dev.aruma.storage (Android Gradle plugin)

Point the **official Cloud Storage for Firebase SDK for Android** (`com.google.firebase:firebase-storage`), in its
normal production mode (no `useEmulator`), at an Aruma server **when the app is built**. Its requests go to Aruma, and
so do the download URLs it returns: `getDownloadUrl()` gives the unified link
`https://<aruma-host>/v0/b/<bucket>/o/<path>?alt=media&token=<uuid>`, which any image loader, player, WebView or
shared link opens on Aruma. This is the production path of ADR-022 §16.2; aruma-redirect's run-time redirect of
Storage is the fallback.

```kotlin
// app/build.gradle.kts
plugins {
    id("com.android.application")
    id("dev.aruma.storage")
}

arumaStorage {
    url = "https://aruma.example.com"
}
```

## English

### What it changes

firebase-storage builds every request and every download URL from one field,
`com.google.firebase.storage.network.NetworkRequest.PROD_BASE_URL`, which its static initializer sets with
`Uri.parse("https://firebasestorage.googleapis.com/v0")`. The plugin replaces that one string in that one class with
`<url>/v0`, using the Android Gradle plugin's instrumentation API (`AsmClassVisitorFactory`, `InstrumentationScope.ALL`).
Nothing else in the class or the library is read or changed. The field is read with `getstatic` everywhere, so no
other class holds a copy, and R8 runs after the change.

The URL is written as Aruma serves it: the host in lower case (refFromURL compares it that way), no default port (no
`:443`), and no path. Use the same server as aruma-redirect's `ArumaURL`, without a path.

### It fails closed, three times

1. **At instrumentation:** a `NetworkRequest` that does not load the text exactly once, in its static initializer,
   stops the build. A firebase-storage release that moved or renamed it is never built half-changed.
2. **At the end of the build:** `verify<Variant>ArumaStorage` reads the variant's final classes (after every
   instrumentation, `ScopedArtifacts` ALL) and fails unless `NetworkRequest` loads the Aruma base and no
   firebase-storage class still holds Google's. It catches what layer 1 cannot see: an `excludes` pattern on
   `variant.instrumentation`, a stale cached transform, another plugin's transform. The APK and the App Bundle are
   packaged from its output (below), so neither can be built without it.
3. **At launch, in aruma-redirect:** an app whose `PROD_BASE_URL` is still Google's stops with an
   `ArumaRedirectException`, unless Storage is redirected at run time. Set `ArumaStorageRedirect` to `false` in the
   manifest to make the plugin mandatory.

### The notice

firebase-storage is Apache-2.0, and §4(b) asks a modified file to carry a prominent notice. The verification task
writes `META-INF/aruma/firebase-storage-MODIFIED.txt` into the app's Java resources: which file was changed, from
which text to which, by which plugin and version. A path of its own, because the packaging's default excludes may drop
the usual `META-INF/NOTICE` names. It is in the APK and in the APKs a store derives from the App Bundle
(test/e2e/android-storage checks both, the second with `bundletool build-apks --mode=universal`).

### Options

| `arumaStorage { }` | | |
|---|---|---|
| `url` | `https://aruma.example.com[:port]` | Required. No path, query or credentials. |
| `allowInsecure` | `true` | Accept `http` to any host. Development only. |

`http` is accepted without the flag for loopback (`localhost`, `127.0.0.0/8`, `::1`) and, in a debuggable variant,
for the emulators' aliases of the development machine (`10.0.2.2`, `10.0.3.2`), as in aruma-redirect. Android blocks
cleartext by default: allow it in a debug-only `networkSecurityConfig`.

### Install

Published to Maven local for now:

```bash
cd sdk/android/aruma-storage-gradle
./gradlew publishToMavenLocal
```

```kotlin
// settings.gradle.kts
pluginManagement {
    repositories { mavenLocal(); google(); mavenCentral(); gradlePluginPortal() }
}
// the root build.gradle.kts
plugins { id("dev.aruma.storage") version "0.1.0" apply false }
```

Requires the Android Gradle plugin 9 (built against 9.0.1) and an application module. **Flutter apps:** the same
lines in `android/settings.gradle(.kts)` and `android/app/build.gradle(.kts)`.

### Limits

- **Pinned to the class and the text,** as read from the published source of firebase-storage 23.0.0
  (`NetworkRequest.java:52`). A release that changes either fails the build; it never ships Google's URL silently.
  Re-run the e2e on every firebase-storage upgrade.
- Download URLs built before the app used the plugin are on Google's host. `ArumaUrls.toArumaUrl` in aruma-redirect
  moves them to Aruma, and `ArumaUrls.fromArumaUrl` turns any of them into the `gs://` URL that
  `getReferenceFromUrl` accepts.
- With the plugin, `getReferenceFromUrl` takes links on Aruma's host (in lower case) and refuses Google's, as the
  library compares hosts with its base.
- Tested on an API 33 emulator with BoM 35.0.2 (firebase-storage 23.0.0), in debug. Not yet on a physical device.

Run the unit tests: `./gradlew test`.

## العربية

### الفكرة

مكتبة firebase-storage لـ Android تبني كل طلب وكل رابط تنزيل من حقل واحد، `NetworkRequest.PROD_BASE_URL`، يضبطه
`<clinit>` بـ `Uri.parse("https://firebasestorage.googleapis.com/v0")`. هذه الإضافة تبدّل ذلك النص وحده في ذلك الصنف وحده
إلى `<url>/v0` وقت البناء، بواجهة instrumentation في AGP. فتذهب الطلبات إلى Aruma، ويعيد `getDownloadUrl()` الرابط الموحّد
على Aruma، فيعمل في أي حمّال (Coil وGlide وWebView وMediaPlayer) ولا يتسرّب إلى Google. هذا مسار الإنتاج (ADR-022 §16.2)،
والتحويل وقت التشغيل في aruma-redirect احتياط.

- المضيف يُكتب lowercase، وبلا منفذ افتراضي (لا `:443`)، وبلا مسار.
- **الفشل مغلق بثلاث طبقات:** الـ visitor يرمي إن لم يجد النص مرة واحدة في `<clinit>`، ومهمة `verify<Variant>ArumaStorage`
  تفحص الأصناف النهائية وتفشل إن بقي نص Google (مثلًا حين يُستثنى الصنف بـ `excludes`)، وaruma-redirect يوقف التطبيق عند
  الإقلاع إن بقي `PROD_BASE_URL` على Google بلا تحويل لـ Storage (`ArumaStorageRedirect=false` يجعل الإضافة إلزامية).
- **الإشعار:** المهمة تكتب `META-INF/aruma/firebase-storage-MODIFIED.txt` (Apache-2.0 §4(b)) في موارد التطبيق، فيصل إلى الـ APK
  وإلى الـ APK المشتق من AAB. والـ APK والـ AAB لا يُبنيان بدون المهمة.
- **الحدود:** مثبّتة على اسم الصنف والنص من المصدر المنشور (firebase-storage 23.0.0). أي إصدار يغيّرهما يُفشل البناء
  صراحةً. ويُعاد e2e عند كل ترقية.
