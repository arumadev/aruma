# aruma-redirect (Android)

Point the **official Firebase Android SDK**, in its normal production mode (no `useAuthEmulator`), at an Aruma
server. Add one dependency and one manifest line; no app code.

```xml
<application ...>
    <meta-data android:name="ArumaURL" android:value="https://aruma.example.com" />
</application>
```

## English

### How it works

The Firebase Auth SDK for Android opens its requests from the app process with `java.net.URL.openConnection()`
(a plain `HttpURLConnection`). Aruma serves Google's exact paths under its own host (ADR-014), so a redirect is only a
host rewrite:

| The SDK requests | aruma-redirect sends |
|---|---|
| `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` | `<ArumaURL>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` |
| `https://securetoken.googleapis.com/v1/token` | `<ArumaURL>/securetoken.googleapis.com/v1/token` |
| `https://identitytoolkit.googleapis.com/<path>` (v1, v2) | `<ArumaURL>/identitytoolkit.googleapis.com/<path>` |
| `https://firebasestorage.googleapis.com/v0/<path>` (Cloud Storage, the fallback below) | `<ArumaURL>/firebasestorage.googleapis.com/v0/<path>` |

- A `ContentProvider` in the library manifest (`initOrder` 1000, above FirebaseInitProvider's 100) runs before
  `Application.onCreate`. It reads `ArumaURL` and installs one process-wide `URLStreamHandlerFactory` for `https`.
- The handler rewrites only the hosts above and keeps the query. The connection returned is the platform's own
  `HttpURLConnection` for the rewritten URL, so method, headers, body, status codes and the error stream behave as usual.
- **Everything else is untouched.** The platform's own https handler is obtained through public API only: a URL
  created before the factory is installed carries it, and `new URL(thatUrl, spec)` reuses it. No reflection, no
  `com.android.okhttp` classes, no hooks, and no Google code is touched.

### Install

The library is published to Maven local for now:

```bash
cd sdk/android/aruma-redirect
JAVA_HOME="/Applications/Android Studio.app/Contents/jbr/Contents/Home" ./gradlew :aruma-redirect:publishToMavenLocal
```

In the app (Kotlin DSL; Groovy is the same with `mavenLocal()` and `implementation 'dev.aruma:aruma-redirect:0.1.0'`):

```kotlin
// settings.gradle.kts or the root build.gradle.kts
repositories { mavenLocal() }

// app/build.gradle.kts
dependencies { implementation("dev.aruma:aruma-redirect:0.1.0") }
```

**Flutter apps:** add the same two lines to the Android project, in `android/app/build.gradle(.kts)` and to the
repositories of `android/build.gradle(.kts)` (`allprojects { repositories { mavenLocal() } }`). Then put the
`ArumaURL` line in `android/app/src/main/AndroidManifest.xml`. Dart code stays the official `firebase_auth`, with no `useAuthEmulator`.
Use the `google-services.json` that Aruma generates for the Android app.

### Cloud Storage

The production path for Storage is the Gradle plugin [`dev.aruma.storage`](../aruma-storage-gradle) (ADR-022 §16.2): it
points firebase-storage at Aruma when the app is built, so download URLs are on Aruma too, in the unified form
`https://<aruma-host>/v0/b/<bucket>/o/<path>?alt=media&token=<uuid>`. This library adds two things:

- **The fallback.** Requests to `firebasestorage.googleapis.com` are redirected like Auth's, unless
  `ArumaStorageRedirect` is `false`, which refuses them instead (as on iOS), on https and plain http alike, so that
  even an old download URL on Google's host opened with `HttpURLConnection` never reaches Google. A refused request
  is answered on the device with 403 and nothing is sent: firebase-storage fails it at once with
  `StorageException.ERROR_NOT_AUTHORIZED` and the reason in the body (an `IOException` would be retried for minutes).
  Uploads, downloads, metadata, list and delete then work without the plugin, but
  `getDownloadUrl()` still returns a link on Google's host, which any loader outside `HttpURLConnection` (Coil,
  OkHttp, Glide with OkHttp, Cronet, MediaPlayer, WebView) or a shared link sends to Google.
- **The launch check.** At install it reads `NetworkRequest.PROD_BASE_URL` and logs which path is in place. If it is
  still Google's and the fallback is off, the app stops with an `ArumaRedirectException`. Set
  `ArumaStorageRedirect=false` in an app built with the plugin to make the plugin mandatory.

`ArumaUrls.fromArumaUrl(url)` turns any download URL (the unified form, Google's, iOS's with `:443`) into the
`gs://<bucket>/<path>` that `FirebaseStorage.getReferenceFromUrl` accepts in every mode; it refuses the prefixed form
`<aruma>/firebasestorage.googleapis.com/v0/...`, which no platform reads. `ArumaUrls.toArumaUrl(url)` moves a link on
Google's host to the unified form on Aruma, for apps on the fallback.

### Configuration (manifest `<meta-data>` in `<application>`)

| Name | Value | |
|---|---|---|
| `ArumaURL` | `https://aruma.example.com[/prefix]` | Absent or empty: the library does nothing. |
| `ArumaAllowInsecureURL` | `true` | Accept `http` to any host. Development only. |
| `ArumaRedirectLog` | `true` | Log host and path of each redirected request. Never the query, headers or body. |
| `ArumaStorageRedirect` | `false` | Do not redirect Cloud Storage at run time: requests to Google's Storage host are refused, and the app needs `dev.aruma.storage` or stops at launch. Default `true`. |

`http` is accepted without the flag only for loopback: `localhost`, `127.0.0.0/8` and `::1`, as in the web and iOS
libraries. A debuggable app (`android:debuggable`, which every debug build is) may also use `10.0.2.2` (the Android
emulator's alias of the development machine) and `10.0.3.2` (Genymotion's): on a real device those are ordinary
private addresses, so a release build needs `https` there or the explicit flag. Names under `.localhost` are not
accepted: RFC 6761 reserves them for loopback, but Android's resolver does not guarantee it. Android blocks
cleartext traffic by default: for `http://10.0.2.2:<port>` add a `networkSecurityConfig` that allows it, in debug
builds only.

Code instead of manifest: call `ArumaRedirect.install("https://aruma.example.com")` before Firebase's first request
(for example in `Application.attachBaseContext`). Installing the same URL twice is a no-op; a different URL throws.
From code, `http://10.0.2.2:<port>` needs `install(url, BuildConfig.DEBUG, false)`: the library cannot see the
debuggable flag without a `Context`.

### Safety

- **Fail closed.** If `ArumaURL` is set but invalid, or `URL.setURLStreamHandlerFactory` was already called by someone
  else, or Cloud Storage would reach Google (above), the app stops at launch with an `ArumaRedirectException` that says
  why, instead of letting a password go to Google.
- An Auth request that cannot be rewritten safely (explicit non-443 port, credentials in the URL, dot segments in the
  path, a trailing-dot host written to get around a match) is refused with an `IOException`, never sent to Google.
- Never logs keys, tokens or bodies. Without `ArumaURL`, nothing is installed.

### Limits

- **One `URLStreamHandlerFactory` per process.** A second one cannot be set (the JDK throws). If another library
  installs its own (for example an old `okhttp-urlconnection` `ObsoleteUrlFactory`), this library fails closed.
- **Tied to the current transport.** It sees what goes through `URL.openConnection`. If a future Firebase Auth release
  moves to OkHttp, Cronet or gRPC, the redirect will not see those requests and **they would go to Google**. Pin the Firebase
  version and re-run a network-guarded conformance pass on each upgrade.
- Auth, and Cloud Storage as a fallback. Firestore (gRPC) and Realtime Database (WebSocket) do not use
  `URL.openConnection`.
- Not covered: phone auth, Google sign-in, App Check, reCAPTCHA (they may go through Play services).
- Tested on an API 33 emulator, in debug and in a release build shrunk by R8 (the consumer rules keep the provider,
  the public class and the configuration error). No physical device yet.

Run the unit tests: `./gradlew :aruma-redirect:testDebugUnitTest`.

## العربية

### الفكرة

مكتبة Firebase Auth لـ Android ترسل طلباتها من عملية التطبيق نفسها عبر `java.net.URL.openConnection()`. وAruma يخدم مسارات Google نفسها
تحت نطاقه (ADR-014)، فالتحويل مجرد تبديل نطاق. هذه المكتبة تثبّت `URLStreamHandlerFactory` لـ https يحوّل ثلاثة نطاقات
(`www.googleapis.com/identitytoolkit/v3/relyingparty/*` و`securetoken.googleapis.com/*` و`identitytoolkit.googleapis.com/*`)
إلى `<ArumaURL>/<النطاق الأصلي>/<المسار>`، ويترك كل شيء آخر لمعالج المنصة. SDK يبقى في وضع الإنتاج، دون `useAuthEmulator`.

### التثبيت

1. انشر المكتبة محليًا: `./gradlew :aruma-redirect:publishToMavenLocal` (من `sdk/android/aruma-redirect`).
2. في التطبيق: أضف `mavenLocal()` إلى المستودعات، و`implementation("dev.aruma:aruma-redirect:0.1.0")` إلى `app/build.gradle`.
   **تطبيق Flutter:** السطران نفساهما في مشروع `android/` (الاعتمادية في `android/app/build.gradle`، والمستودع في `android/build.gradle`).
3. في `AndroidManifest.xml` داخل `<application>`:
   `<meta-data android:name="ArumaURL" android:value="https://aruma.example.com" />`
4. استخدم `google-services.json` الذي يولّده Aruma لتطبيق Android. لا كود إضافي، ولا `useAuthEmulator`.

التثبيت تلقائي: `ContentProvider` في المكتبة (`initOrder` أعلى من FirebaseInitProvider) يقرأ `ArumaURL` قبل `Application.onCreate`.
وبديله في الكود: `ArumaRedirect.install("https://...")` قبل أول طلب.

### الإعداد والأمان

- `ArumaURL` غائب أو فارغ: لا تفعل المكتبة شيئًا.
- http مقبول دون إعداد إضافي لعناوين loopback فقط: `localhost` و`127.0.0.0/8` و`::1`، كما في مكتبتي الويب وiOS.
  ويُقبل كذلك `10.0.2.2` (المحاكي) و`10.0.3.2` (Genymotion) في تطبيق قابل للتنقيح (`debuggable`، أي كل بناء debug)
  فقط، لأنهما على جهاز حقيقي عنوانان خاصان عاديان. أسماء `.localhost` غير مقبولة: يحجزها RFC 6761 لـ loopback لكن
  محلّل أندرويد لا يضمن ذلك. وغير ذلك يحتاج `ArumaAllowInsecureURL=true` للتطوير فقط (ومن الكود:
  `install(url, BuildConfig.DEBUG, false)`). ويلزم أيضًا `networkSecurityConfig` يسمح بـ cleartext في بناء debug وحده.
- `ArumaRedirectLog=true` يسجّل النطاق والمسار فقط، لا الاستعلام ولا الترويسات ولا الأجسام.
- **الفشل مغلق:** `ArumaURL` غير صالح، أو `setURLStreamHandlerFactory` مستدعى من مكتبة أخرى: يتوقف التطبيق عند الإقلاع
  برسالة واضحة بدل أن تذهب كلمة المرور إلى Google. وطلب Auth لا يمكن تحويله بأمان (منفذ غير 443، أو بيانات اعتماد في العنوان،
  أو `..` في المسار) يُرفض بـ `IOException`.

### الحدود

- factory واحد لكل عملية: إن ثبّتت مكتبة أخرى factory فالمكتبة تفشل مغلقةً.
- **Cloud Storage:** المسار الأساسي إضافة Gradle [`dev.aruma.storage`](../aruma-storage-gradle) وقت البناء، فيصير رابط التنزيل
  على Aruma. وهذه المكتبة تحوّل `firebasestorage.googleapis.com` احتياطًا، فإن كان `ArumaStorageRedirect=false` رفضت طلباته على https وhttp (كما في iOS)، فلا يصل رابط تنزيل قديم على Google إليها: يُجاب الطلب على الجهاز بـ 403 ولا يُرسل شيء، فتفشل firebase-storage في الحال بـ `ERROR_NOT_AUTHORIZED` (و`IOException` كانت تُعاد دقائق)، وتفحص عند
  الإقلاع `NetworkRequest.PROD_BASE_URL`: إن بقي على Google ولا تحويل، يتوقف التطبيق. و`ArumaUrls.fromArumaUrl` يحوّل أي رابط
  تنزيل إلى `gs://` الذي يقبله `getReferenceFromUrl` في كل وضع، و`ArumaUrls.toArumaUrl` ينقل رابط Google إلى الشكل الموحّد.
- مرتبطة بالنقل الحالي (`URL.openConnection`): لو انتقلت Firebase Auth لاحقًا إلى OkHttp أو Cronet أو gRPC فلن تُرى طلباتها
  **وستذهب إلى Google**. ثبّت إصدار Firebase وأعد اختبار التوافق بحارس شبكة عند كل ترقية.
- Auth فقط: Firestore (gRPC) وRealtime Database (WebSocket) لا يمرّان بهذه الطبقة. وغير مغطّى: الهاتف ودخول Google وApp Check وreCAPTCHA.
- جُرّبت على محاكي API 33، في debug وفي release مصغّر بـ R8 (قواعد المستهلك تبقي المزوّد والصنف العام وخطأ الإعداد). لم تُجرَّب على جهاز حقيقي بعد.
