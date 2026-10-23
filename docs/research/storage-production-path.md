# Cloud Storage في وضع الإنتاج: كيف تصل كل مكتبة رسمية إلى Aruma

- **الحالة:** تجربة مكتملة (S0-B)، 2026-10-24، فرع `s0-storage-path`. نظير أبحاث Auth: [web-production-redirect.md](web-production-redirect.md) و[android-production-redirect.md](android-production-redirect.md) و[ios-production-redirect.md](ios-production-redirect.md).
- **السؤال:** كيف تصل كل مكتبة Storage رسمية إلى خادم Aruma **في وضعها العادي**، دون `useEmulator` أو `connectStorageEmulator`؟ وما أقل إضافة تلزم لكل منصة؟
- **الجواب باختصار:**

| المنصة | ما يحمل الطلبات | مكتبة التحويل كما هي | بعد إضافة نطاق Storage إليها |
|---|---|---|---|
| الويب | `XMLHttpRequest` | لا تراها (تلفّ `fetch` وحده) | تعمل كلها، إلا رابط التنزيل في `<img>`. وتبديل ثابت النطاق وقت البناء يعمل كاملًا |
| Android | `HttpURLConnection` | لا تحوّلها (نطاقات Auth وحدها) | تعمل كلها، بلا خطّاف جديد |
| iOS | `URLSession` عبر GTMSessionFetcher | لا تحوّلها | تعمل كلها، والرفع في الخلفية أيضًا، بشرط أن يعيد الخادم رابط رفع مطلقًا إليه |
| Flutter | المكتبات الأصلية (والويب: مكتبة JS) | كالأصلية | كالأصلية، لكن HTTP من Dart (`dart:io`) لا يمرّ بالتحويل |
| Admin Node | gaxios عبر `@google-cloud/storage` | — | `firebase-admin` لا يصل إلا بلا مصادقة. `@google-cloud/storage` مباشرةً يعمل بمصادقة Aruma، إلا بدء الرفع المتقطع |
| Admin Go | `net/http` عبر `cloud.google.com/go/storage` | — | `option.WithEndpoint` يعمل كاملًا بمصادقة Aruma، ويُفضّل معه `storage.WithJSONReads()` |

- **الأداة:** [test/experiments/storage-path/](../../test/experiments/storage-path/) (طريقة التشغيل في README هناك).

## لماذا

ثلاث حقائق من كود المكتبات تحكم كل ما بعدها:

1. **النطاق ثابت في كل مكتبة،** ولا يغيّره إلا وضع المحاكي:
   - الويب: `DEFAULT_HOST = 'firebasestorage.googleapis.com'` (`implementation/constants.ts:24`).
   - Android: `PROD_BASE_URL` (`network/NetworkRequest.java:52`).
   - iOS: `host` و`scheme` و`port` في `Storage.swift:285-287`، وهي `internal` (`:306-308`).
   - وضع المحاكي على الجوال **http فقط**: Android يبني `"http://" + host` (`NetworkRequest.java:97-104`)، وiOS يضع `scheme = "http"` (`Storage.swift:210-225`). وعلى الويب http إلا لنطاقات Cloud Workstations (`service.ts:149-155`). فالمحاكي لا يصلح للإنتاج.
2. **رابط التنزيل تبنيه المكتبة من النطاق نفسه،** لا يأتي من الخادم:
   - الويب: `makeUrl(urlPart, host, protocol)` مع `service.host` (`metadata.ts:177-186`، `requests.ts:83-96`).
   - Android: `getHttpUri()` على `PROD_BASE_URL` (`GetDownloadUrlTask.java:63-65`، `StorageReferenceUri.java:38-46`).
   - iOS: `scheme` و`host` و`port` من `Storage` (`StorageGetDownloadURLTask.swift:62-64`).
   - فما دام التحويل يحدث في طبقة الشبكة، يعيد `getDownloadURL()` رابطًا إلى `firebasestorage.googleapis.com`.
3. **رابط الرفع المتقطع يحدّده الخادم:** المكتبات الثلاث تقرأ `X-Goog-Upload-URL` من رد أمر `start` وترسل القطع إليه كما هو، رابطًا مطلقًا:
   - الويب `requests.ts:441`، وAndroid `UploadTask.java:296-299`، وiOS (GTMSessionFetcher) `GTMSessionUploadFetcher.m:1064-1083`.
   - فإن أعاد Aruma رابطًا مطلقًا إلى نفسه، ذهبت القطع إليه مباشرةً دون أي تحويل. وهذا ما يحلّ مشكلة الرفع في الخلفية على iOS (أدناه).

## المنهج

- **قراءة الكود** في إصدارات مثبّتة (المراجع `ملف:سطر` في كل قسم). المصادر في `.cache/research/src/storage-path/`، وحزم Node وGo من مخزن الحزم. لم يُفكّ أي ثنائي.
- **القياس:** تطبيقات صغيرة بالمكتبات الرسمية أمام **خادم تسجيل** (`stub/main.go`):
  - ليس Aruma ولا يتحقق من شيء. يجيب بروتوكول Firebase Storage REST (`/v0/b/…`، والرفع multipart والمتقطع `X-Goog-Upload-*`) وبروتوكول GCS JSON، بقدر ما يكفي لتكمل المكتبة عملياتها.
  - يقبل البادئة التي تضيفها مكتبات التحويل (`/firebasestorage.googleapis.com/…`) أو بلا بادئة (شكل المحاكي)، على http وhttps.
  - يسجّل لكل طلب: المسار، والبادئة، ونوع `Authorization` وطوله فقط، وترويسات البروتوكول. ولا يسجّل رمزًا ولا مفتاحًا.
- **العمليات:** رفع صغير، ورفع متقطع 3 MB (ومن ملف على الجوال)، وgetMetadata، وgetDownloadURL، وتنزيل (بايتات، ومن الجوال إلى ملف أو stream)، وlist، وdelete. ثم **رابط التنزيل خارج المكتبة**: `fetch` و`<img>` في الويب، و`HttpURLConnection` في Android، و`URLSession.shared` في iOS، و`dart:io` في Flutter.
- **لكل منصة ثلاثة أوضاع:** المحاكي للمقارنة، ومكتبة التحويل كما هي، وأقل إضافة (spike).
- **لا اتصال بـ Google:** حارس شبكة في كل تشغيل، وكل طلب إلى Google يُرفض ويُسجَّل:

| | الحارس |
|---|---|
| الويب | Chromium لا يحلّ اسمًا إلا localhost (`--host-resolver-rules`)، وكل طلب فاشل يُسجَّل |
| Android | iptables يرفض كل حزمة من uid التطبيق خارج loopback (`adb root`)، و`NetworkGuard` يسجّل |
| iOS | `LoopbackGuard` (URLProtocol) في جلسات default وephemeral |
| Node | `test/experiments/netguard.cjs` |
| Go | `HTTPS_PROXY` إلى وكيل في الخادم يرفض كل طلب ويسجّله |

- لم يخرج طلب HTTP واحد إلى Google في أي تشغيل.
- لكن على Android حلّ نظام التشغيل (netd، لا التطبيق) اسم `firebasestorage.googleapis.com` قبل أن يرفض iptables الاتصال. وهذا استعلام DNS لا أكثر، كما في بحث Auth.

**الإصدارات:**

| | الإصدار |
|---|---|
| الويب | firebase 13.1.0 (`@firebase/storage` 0.14.6)، بناء CDN كما يحمّله Flutter web، في Chromium (Playwright 1.64.1) |
| Android | firebase-storage 23.0.0 (BoM 35.0.2)، AVD `aruma-a34` (API 33) |
| iOS | FirebaseStorage 12.19.2 وGTMSessionFetcher 5.3.1، محاكي iPhone 16 Pro Max (iOS 18.5) |
| Flutter | Flutter 3.44.2، وfirebase_core 5.1.0 وfirebase_storage 14.0.1 (يجلبان BoM 35.1.0، وFirebase iOS 12.19.3، وJS 13.1.0)، على AVD `aruma-a34` |
| Admin Node | firebase-admin 14.5.0، و`@google-cloud/storage` 8.4.0، وgoogle-auth-library 11.3.1، وgtoken 8.0.0، على Node 24.21.0 |
| Admin Go | firebase-admin-go 4.22.0، و`cloud.google.com/go/storage` 1.62.1، و`google.golang.org/api` 0.304.0، على Go 1.27.2 |

## الويب

**طبقة الشبكة:**
- بناء المتصفح يرسل كل طلب بـ `XMLHttpRequest`: `new XMLHttpRequest()` ثم `xhr.open(method, url, true)` (`platform/browser/connection.ts:43` و`:77`). ويُنشأ لكل طلب، فيصل إليه تعديل `XMLHttpRequest.prototype.open` متى ثُبّت.
- بناء Node يستعمل `fetch` (`platform/node/connection.ts:196-211`).
- الرفع: `uploadBytes` multipart، و`uploadBytesResumable` متقطع إن زاد الحجم على 256 KiB (`task.ts:172-173`).
- الترويسات: `Authorization: Firebase <idToken>` و`X-Firebase-Storage-Version` و`X-Firebase-GMPID` و`X-Firebase-AppCheck` (`implementation/request.ts:237-260`).

**ما يعترضها:** `@aruma/web` يلفّ `fetch` وحده، لنطاقي Auth (`sdk/web/aruma-web/src/index.js:17` و`:140-161`). فلا يرى طلبات Storage.

**الـ spike:** `sdk/web/aruma-web/src/storage-spike.js` يعدّل `XMLHttpRequest.prototype.open` ويلفّ `fetch`، لنطاق Storage وحده.

**النتائج** (`web.sh`، Chromium):

| العملية | emulator | prod، `@aruma/web` كما هو | prod + spike (http) | prod + spike (https) | prod + تبديل الثابت (https) |
|---|---|---|---|---|---|
| رفع صغير (multipart) | ✓ | ✗ | ✓ | ✓ | ✓ |
| رفع متقطع 3 MB | ✓ | ✗ | ✓ | ✓ | ✓ |
| getMetadata | ✓ | ✗ | ✓ | ✓ | ✓ |
| getDownloadURL | ✓ | ✗ | ✓ (رابط Google) | ✓ (رابط Google) | ✓ (رابط Aruma) |
| getBytes 3 MB، getBlob | ✓ | ✗ | ✓ | ✓ | ✓ |
| list | ✓ | ✗ | ✓ | ✓ | ✓ |
| رابط التنزيل بـ `fetch` | ✓ | — | ✓ (لفّ fetch حوّله) | ✓ | ✓ |
| رابط التنزيل في `<img>` | ✓ | — | **✗** (إلى Google، رفضه الحارس) | **✗** | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ | ✓ |
| **المجموع** | **11/11** | **1/11** | **10/11** | **10/11** | **11/11** |

- `@aruma/web` كما هو: 5 عناوين مختلفة إلى `firebasestorage.googleapis.com` رفضها الحارس، وصفر طلب وصل إلى الخادم.
- **CORS:** الصفحة على أصل والخادم على آخر، فسبق كل طلب طلبُ preflight. رأى الخادم 10 طلبات `OPTIONS` في كل وضع إنتاجي.
  - ولا يقرأ المتصفح `X-Goog-Upload-URL` إلا إذا أعلنه الخادم في `Access-Control-Expose-Headers`. والحال نفسها مع Google، فليس شرطًا جديدًا.
- **تبديل الثابت:** استُبدل النص `"firebasestorage.googleapis.com"` (يرد مرة واحدة في `firebase-storage.js`) بـ `"<aruma-host>/firebasestorage.googleapis.com"`.
  - فصار `storage.host` نطاق Aruma مع المسار، وكل الروابط إليه، ورابط التنزيل منها، فعمل في `<img>`.
  - و`Location.makeFromUrl` يبني تعبيره من المضيف نفسه (`location.ts:92-103`)، فبقي `refFromURL` متسقًا.
  - هذا ما تفعله إضافة Vite في `@aruma/web` لـ Auth (`mode: 'build'`)، ويحتاج أن تُحزَم المكتبة مع التطبيق. أما Flutter web فيحمّلها من `www.gstatic.com`، فلا يصلح له.

## Android

**طبقة الشبكة:** `java.net.HttpURLConnection`، لا OkHttp ولا Cronet:
- `NetworkRequest.createConnection()` يبني `new URL(...)` ويعطيه لـ `connectionFactory` (`network/NetworkRequest.java:298-315`).
- وهو `HttpURLConnectionFactoryImpl`: `(HttpURLConnection) url.openConnection()` (`network/connection/HttpURLConnectionFactoryImpl.java:27-29`).
- الحقل `connectionFactory` خاص بالحزمة "for test purposes" (`NetworkRequest.java:70-72`)، فليس واجهة.
- كل رفع متقطع، حتى الصغير: `start` ثم قطع (`UploadTask.java:273-300`). وقُرئت ترويسة `X-Goog-Upload-URL` بمطابقة حرفية للمفتاح (`NetworkRequest.java:490-499`). لكن خريطة ترويسات المنصة لا تفرّق بين الأحرف، فنجح الإملاءان (أدناه).

**ما يعترضها:** factory الـ `URLStreamHandler` في `aruma-redirect` يرى كل `URL.openConnection` في العملية. لكنه لا يحوّل إلا نطاقات Auth (`AuthEndpoints.java`). فلا يلزم خطّاف جديد، بل نطاق جديد.

**الـ spike:** `ArumaRedirect.setStorageSpike(true)` يضيف `firebasestorage.googleapis.com` إلى النطاقات المحوَّلة.

**النتائج** (`android.sh`، instrumented test، عملية جديدة لكل وضع):

| العملية | emulator | prod، `aruma-redirect` كما هو | prod + spike (http) | prod + spike (https) |
|---|---|---|---|---|
| putBytes صغير | ✓ | ✗ "Unable to obtain an upload URL" | ✓ | ✓ |
| putBytes 3 MB | ✓ | ✗ | ✓ | ✓ |
| putFile 3 MB | ✓ | ✗ | ✓ | ✓ |
| getMetadata، getDownloadUrl | ✓ | ✗ | ✓ (رابط Google) | ✓ (رابط Google) |
| getBytes، getFile، getStream (3 MB) | ✓ | ✗ | ✓ | ✓ |
| listAll | ✓ | ✗ | ✓ | ✓ |
| رابط التنزيل بـ `HttpURLConnection` | ✓ | — | ✓ (الـ factory حوّله) | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ |
| **المجموع** | **12/12** | **1/12** | **12/12** | **12/12** |
| حزم رفضها iptables | 0 | 320 | 0 | 0 |

- `aruma-redirect` كما هو: سجّل `NetworkGuard` 10 اتصالات إلى `firebasestorage.googleapis.com`، ورفض iptables حزمها (320 حزمة)، ولم يصل إلى الخادم شيء.
- مع الـ spike: مرّ 13 طلبًا بالتحويل (سجل `ArumaRedirect` في logcat): 3 بدايات رفع، وطلبا بيانات وصفية، و4 تنزيلات، وlist، و3 حذف. أما **9 طلبات قطع** فذهبت مباشرةً إلى رابط الرفع المطلق الذي أعاده الخادم.
- **تنويعتان** (prod + spike): الخادم يكتب أسماء الترويسات بإملاء Go (`X-Goog-Upload-Url`): 12/12. والخادم يعيد رابط رفع على نطاق Google: 12/12، و22 طلبًا محوَّلًا، أي القطع أيضًا.
- https: CA الخادم المؤقت في `network_security_config` للتطبيق التجريبي وحده.

## iOS

**طبقة الشبكة:** `URLSession` عبر GTMSessionFetcher:
- `StorageFetcherService` يبني `GTMSessionFetcherService` لكل تطبيق ودلو (`Internal/StorageFetcherService.swift:37-60`). وكل fetcher عادي يأخذ إعداد `ephemeral` (`GTMSessionFetcher.m:1072-1082`)، فيمرّ بـ URLProtocol الذي تضعه `ArumaRedirect` أولًا.
- http مسموح للخادم في وضع المحاكي وحده (`StorageFetcherService.swift:57-60`). والرمز لا يُرفق إلا لـ https أو loopback (`Internal/StorageTokenAuthorizer.swift:41-49`).
- الرفع `GTMSessionUploadFetcher` (`StorageUploadTask.swift:102-122`). **وبدء الرفع POST إلى مسار الكائن نفسه:** `/v0/b/<bucket>/o/<name>?uploadType=resumable&name=<name>` (`StorageUploadTask.swift:76-96` مع `StorageUtils.swift:25-48`)، لا إلى `/o?name=` كالويب وAndroid.

**الرفع في الخلفية:**
- `GTMSessionUploadFetcher` يجعل `useBackgroundSession = YES` افتراضيًا (`GTMSessionUploadFetcher.m:330`). لكنه يطبّقه على القطع وحدها، و`start` في المقدمة دائمًا (`:2047-2052` و`:1583-1585`).
- وFirebaseStorage يطفئه على المحاكي، وحيث لا تدعمه البيئة. فيبقى لـ **`putFile` على جهاز حقيقي** (`StorageUploadTask.swift:111-121`).
- جلسة الخلفية لا تستشير أي URLProtocol، فلا تراها `ArumaRedirect`. لكن القطع تذهب إلى `X-Goog-Upload-URL` كما أعاده الخادم.

**النتائج** (`ios.sh`، عملية جديدة لكل وضع):

| العملية | emulator | prod، `ArumaRedirect` كما هو | prod + spike (http) | prod + spike (https) |
|---|---|---|---|---|
| putData صغير، putData 3 MB، putFile 3 MB | ✓ | ✗ (`-1009`، رفضها الحارس) | ✓ | ✓ |
| getMetadata، downloadURL | ✓ | ✗ | ✓ (رابط Google بـ `:443` صريح) | ✓ |
| data 3 MB، write(toFile:) | ✓ | ✗ | ✓ | ✓ |
| listAll | ✓ | ✗ | ✓ | ✓ |
| رابط التنزيل بـ `URLSession.shared` | ✓ | — | ✓ (حوّله URLProtocol) | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ |
| **المجموع** | **11/11** | **1/11** | **11/11** | **11/11** |

- `ArumaRedirect` كما هو: رفض `LoopbackGuard` 27 طلبًا إلى `firebasestorage.googleapis.com`، ولم يصل إلى الخادم شيء.
- تنويعة رابط رفع على نطاق Google (prod + spike): 11/11. القطع (3 MB في طلب واحد، لأن `uploadChunkSizeBytes = .max`، `Storage.swift:109`) مرّت بـ URLProtocol، وهو يحمل الجسم في الذاكرة حتى 16 MiB (`Endpoints.swift`، `RequestBody.limit`).

**تجربتا الخلفية** (على المحاكي، بـ `GTMSessionUploadFetcher` مباشرةً كما يبنيه `putFile` على الجهاز):

| التجربة | النتيجة |
|---|---|
| رفع 1 MB، الخادم على http، رابط رفع مطلق إليه | ✓. `start` مرّ بـ `ArumaRedirect`، والقطعة من **جلسة خلفية** (`isUsingBackgroundSession = true`) إلى `http://127.0.0.1:8824` مباشرةً |
| الشيء نفسه على https (CA أُضيف بـ `simctl keychain add-root-cert`) | ✓. القطعة إلى `https://localhost:8825`، والتحقق من الشهادة في `nsurlsessiond` بثقة النظام |
| URLProtocol يجيب كل طلب بـ 299، في جلسة ephemeral | أجاب هو (299) |
| الـ URLProtocol نفسه في جلسة background | **تجاهلته الجلسة**، ووصل الطلب إلى الخادم (200) |

النتيجة: لو أعاد Aruma رابط رفع على نطاق Google، لذهبت قطع `putFile` على الجهاز **إلى Google مباشرةً**. ولهذا لم تُجرَّب تلك التنويعة في الخلفية. **فالرابط المطلق إلى Aruma شرط على الخادم، لا تحسين.**

## Flutter

- **الجوال:** `firebase_storage` يستدعي المكتبة الأصلية: BoM في `android/build.gradle:85-86`، و`Firebase/Storage` في `ios/firebase_storage.podspec:37` (firebase_storage 14.0.1). فما يصحّ للأصلية يصحّ له. والمحاكي `useStorageEmulator` http، ويحوّل `localhost` و`127.0.0.1` إلى `10.0.2.2` على Android ما لم يُمرَّر `automaticHostMapping: false` (`lib/src/firebase_storage.dart:160-183`).
- **الويب:** `firebase_core_web` يحمّل مكتبة JS 13.1.0 من `https://www.gstatic.com/firebasejs/…` (`firebase_sdk_version.dart:9`). فطلبات Storage هي `XMLHttpRequest` الويب نفسها، ولا يصلح تبديل الثابت وقت البناء.
  - و`getData()` على الويب لا يستعمل المكتبة: يطلب `getDownloadURL()` ثم ينزّل بـ `package:http` (`firebase_storage_web/lib/src/reference_web.dart:118-133`).
  - و`BrowserClient` في http 1.6.0 يستدعي `window.fetch` (`browser_client.dart:36-40`). فلا يغطيه إلا لفّ `fetch` يشمل نطاق Storage.
- **HTTP من Dart:** `Image.network` و`package:http` على الجوال يستعملان `dart:io` بمقابسه الخاصة، لا `HttpURLConnection` ولا `URLSession`. فلا تراها مكتبات التحويل الأصلية.

**النتائج** (`flutter.sh`، Android، `aruma-redirect` مع الـ spike يثبّته `MainActivity` قبل المحرك):

| العملية | emulator | prod + spike (http) |
|---|---|---|
| putData صغير، putData 3 MB | ✓ | ✓ |
| getMetadata، getDownloadURL | ✓ | ✓ (رابط Google) |
| getData 3 MB، listAll، delete | ✓ | ✓ |
| رابط التنزيل بـ `dart:io` `HttpClient` | ✓ (إلى `127.0.0.1`) | **✗** `Connection refused`: ذهب إلى `firebasestorage.googleapis.com` ورفضه iptables |
| **المجموع** | **9/9** | **8/9** |
| حزم رفضها iptables | 0 | 32 (كلها من `dart:io`) |

- مرّ 8 طلبات بـ `aruma-redirect` (سجل logcat)، والقطع إلى رابط الرفع المطلق مباشرةً، كما في Android الأصلي.
- `dart:io` لم يمرّ بالـ factory: هذا ما سيحدث لـ `Image.network(await ref.getDownloadURL())`.

## Admin Node

**ما يفعله `firebase-admin`:**
- `getStorage()` يبني `new Storage({ projectId, credentials: { private_key, client_email } })` لاعتماد `cert()`، أو `new Storage()` لـ ADC. وأي اعتماد آخر خطأ `invalid-credential` (`src/storage/storage.ts:73-95`). فلا مكان فيه لـ `apiEndpoint` ولا لـ `token_uri`.
- `FIREBASE_STORAGE_EMULATOR_HOST` يصير `STORAGE_EMULATOR_HOST=http://…` (`storage.ts:48-59`).
- `getDownloadURL(file)` يطلب البيانات الوصفية من `STORAGE_EMULATOR_HOST || https://firebasestorage.googleapis.com` + `/v0` (`src/storage/index.ts:74-90`).

**وما يفعله `@google-cloud/storage` 8.4.0** (المسارات في `build/cjs/src/`):
- `STORAGE_EMULATOR_HOST` أو `apiEndpoint` يجعلان `customEndpoint = true` (`storage.js:461-472`). ومع المتغير يصير عنوان JSON هو المتغير **بلا `/storage/v1`** (`storage.js:474`)، فالطلبات إلى `/b/<bucket>/o/…`.
- مع `customEndpoint` لا مصادقة، إلا بـ `useAuthWithCustomEndpoint: true` (`nodejs-common/util.js:495-500`). والرفع المتقطع يتجاوزها أيضًا لنطاق غير Google (`resumable-upload.js:123-147`).
- `createResumableUpload` يمرّر `useAuthWithCustomEndpoint` (`file.js:1293`)، و**`startResumableUpload_` لا يمرّره** (`file.js:3531-3560`). فـ `save()` و`createWriteStream()` المتقطعان يبدآن الجلسة **بلا مصادقة** مهما كان الإعداد.
- `keyFilename` يمرّ بـ gtoken، وهو يثبّت `https://oauth2.googleapis.com/token` (`gtoken/build/cjs/src/index.cjs:55`). وهي نتيجة T22 نفسها في [experiments.md](experiments.md).

**النتائج** (`admin-node.sh`، مفتاح حساب خدمة مؤقت `token_uri` فيه للخادم، https إلا حيث ذُكر http). العمليات 7 مع `firebase-admin` (ومعها `getDownloadURL`)، و7 مع `@google-cloud/storage` (ومعها الالتفاف أدناه)، وinit:

| الإعداد | النتيجة | Authorization على الخادم |
|---|---|---|
| `firebase-admin` + `cert()`، بلا متغير | 1/8. تبادل الرمز إلى `oauth2.googleapis.com` (رفضه الحارس) | — |
| `firebase-admin` + `cert()` + `STORAGE_EMULATOR_HOST=http://…` | 8/8 | **لا شيء، في كل الطلبات** |
| الشيء نفسه مع `https://…` | 8/8 | لا شيء |
| `FIREBASE_STORAGE_EMULATOR_HOST=host:port` | 8/8 (http) | لا شيء |
| `firebase-admin` + `arumaCredential` (T22) | 0/1: `storage/invalid-credential` | — |
| `@google-cloud/storage` + `apiEndpoint` | 8/8 | لا شيء |
| + `useAuthWithCustomEndpoint` + `keyFilename` | 2/8. gtoken إلى `oauth2.googleapis.com` (رفضه الحارس). ونجح `save()` المتقطع وحده، لأنه لم يطلب رمزًا أصلًا | — |
| + `useAuthWithCustomEndpoint` + `authClient` يأخذ رمزه من `token_uri` Aruma | 8/8 | `Bearer` على الكل، **إلا بدء `save()` المتقطع وقطعه** |
| الشيء نفسه و`apiEndpoint` ببادئة `/storage.googleapis.com` | 8/8، والبادئة محفوظة في كل المسارات | كالسابق |

- **الالتفاف المقيس للرفع المتقطع:** `createResumableUpload()` (بـ `Bearer`)، ثم `save(data, { uri })`. الاستعلام والقطعة بعده بلا مصادقة، كما في GCS: رابط الجلسة نفسه هو التفويض.
- `authClient` هنا `OAuth2Client` من google-auth-library، و`refreshHandler` يستدعي `arumaCredential(key).getAccessToken()` (`admin-node/scenario.mjs`).

## Admin Go

**ما يفعله `cloud.google.com/go/storage` 1.62.1:**
- مع `STORAGE_EMULATOR_HOST` يُضاف `option.WithoutAuthentication()` قبل خيارات المستخدم (`storage.go:160-201`). وقيس أن `option.WithCredentialsFile` لا يلغيه: لا `Authorization`.
- `option.WithEndpoint` يُستعمل للـ JSON API كما هو. لكن:
  - **القراءة الافتراضية XML** إلى `<scheme>://<endpoint host>/<bucket>/<object>`، بلا مسار الـ endpoint (`storage.go:216-231`، `http_client.go:884-891`).
  - **والرفع إلى `/upload/storage/v1/…` مطلقًا من الجذر** (`google.golang.org/api/storage/v1/storage-gen.go:11385-11387`)، فتسقط أي بادئة.
- الرفع المتقطع يرسل `X-GUploader-No-308: yes` وينتظر 200 مع `X-Http-Status-Code-Override: 308` بدل 308 (`internal/gensupport/resumable.go:109-124` و`:281-284`). وقطعه POST لا PUT.
- `firebase-admin-go` يحوّل `FIREBASE_STORAGE_EMULATOR_HOST` إلى `STORAGE_EMULATOR_HOST`، ويمرّر خيارات التطبيق كلها إلى `storage.NewClient` (`storage/storage.go:37-45`).
- `golang.org/x/oauth2` يحترم `token_uri` (T22).

**النتائج** (`admin-go.sh`، http):

| الإعداد | النتيجة | Authorization | مسارات تستحق الانتباه |
|---|---|---|---|
| `app.Storage` + المفتاح، بلا شيء آخر | 1/7. تبادل الرمز وصل إلى الخادم، ثم 7 `CONNECT storage.googleapis.com:443` رفضها الحارس | — | — |
| `STORAGE_EMULATOR_HOST` | 7/7 | **لا شيء** | قراءة XML `/s0path.appspot.com/s0%2Fbig.bin` |
| `option.WithEndpoint(<aruma>/storage/v1/)` | 7/7 | `Bearer` (من `token_uri`) على الكل | قراءة XML من الجذر |
| + `storage.WithJSONReads()` | 7/7 | `Bearer` | القراءة `/storage/v1/…?alt=media` |
| endpoint ببادئة `/storage.googleapis.com/storage/v1/` + JSON reads | 7/7 | `Bearer` | **الرفع إلى `/upload/storage/v1/…` بلا البادئة** |
| `storage.NewClient` مباشرةً + `WithEndpoint` | 7/7 | `Bearer` | قراءة XML من الجذر |

## جدول النتائج المجمّع

الأرقام: العمليات الناجحة من كل العمليات. ✓ تعني نجاح الكل.

| المنصة | المحاكي | الإنتاج، مكتبة التحويل كما هي | الإنتاج + أقل إضافة (http) | الإنتاج + أقل إضافة (https) | رابط التنزيل خارج المكتبة |
|---|---|---|---|---|---|
| الويب | 11/11 | 1/11 | 10/11 | 10/11 | `fetch` ✓، `<img>` ✗. مع تبديل الثابت: 11/11 |
| Android | 12/12 | 1/12 | 12/12 | 12/12 | `HttpURLConnection` ✓ |
| iOS | 11/11 | 1/11 | 11/11 (والخلفية ✓) | 11/11 (والخلفية ✓) | `URLSession.shared` ✓ |
| Flutter (Android) | 9/9 | (المكتبة الأصلية، كـ Android) | 8/9 | (كـ Android) | `dart:io` ✗ |
| Admin Node | 8/8 بلا مصادقة (http وhttps) | — | — | `@google-cloud/storage` + `authClient`: 8/8 بـ `Bearer`، إلا بدء `save()` المتقطع | — |
| Admin Go | 7/7 بلا مصادقة | — | `WithEndpoint`: 7/7 بـ `Bearer` | لم يُقَس (أدناه) | — |

## ما يلزم خادم Aruma (لـ S3 وS4)

ظهرت هذه من القياس، وكلها يعمل بها الخادم التجريبي:

1. **`X-Goog-Upload-URL` مطلق إلى `public_url` الخادم** (`https://aruma.example.com/firebasestorage.googleapis.com/v0/b/…?upload_id=…`). وإلا ذهبت قطع الرفع في الخلفية على iOS إلى Google.
2. **مساران للـ REST نفسه:** `/firebasestorage.googleapis.com/v0/…` (مكتبات التحويل) و`/v0/…` (المحاكي، و`getDownloadURL` في Admin Node).
3. **بدء الرفع المتقطع على مسار الكائن أيضًا:** iOS يرسل POST إلى `/v0/b/<bucket>/o/<name>?uploadType=resumable&name=<name>`.
4. **الفروق بين العملاء في البروتوكول نفسه:**
   - القطع: POST في الويب وAndroid، وPUT في iOS (قطعة واحدة بـ `upload, finalize`).
   - الحجم الكلي عند `start`: الويب في `X-Goog-Upload-Header-Content-Length`، وiOS في `X-Goog-Upload-Content-Length`، وAndroid لا يرسله (`ResumableNetworkRequest.java:24-27`).
   - نوع المحتوى: `X-Goog-Upload-Header-Content-Type` في الويب وAndroid، و`X-Goog-Upload-Content-Type` في iOS.
5. **CORS** على كل مسارات REST: إجابة preflight لترويسات `Authorization` و`X-Firebase-*` و`X-Goog-Upload-*`، و`Access-Control-Expose-Headers` لـ `X-Goog-Upload-URL` و`-Status` و`-Size-Received` و`-Chunk-Granularity`.
6. **GCS JSON من الجذر** وليس تحت بادئة فقط:
   - `/storage/v1/…` و`/upload/storage/v1/…` و`/download/storage/v1/…`.
   - و`/b/<bucket>/o/…` (Node مع `STORAGE_EMULATOR_HOST`).
   - وقراءة XML `/<bucket>/<object>` إن أردنا Go دون `WithJSONReads`. وهذا يمسّ توجيه ADR-008، فالأسلم أن نوثّق `WithJSONReads()` ولا نخدم XML.
7. **`X-GUploader-No-308: yes`** يُجاب بـ 200 و`X-Http-Status-Code-Override: 308` (Go). و`Content-Range: bytes 0-*/*` يعني «هذا آخر الجسم» (Node).
8. **المسار خام:** أسماء الكائنات فيها `%2F`. فالتوجيه على `EscapedPath()`، لا على مسار مفكوك.
9. **إملاء أسماء الترويسات لا يهم** للمكتبات المقيسة. ويُكتب كما تكتبه Google احتياطًا.
10. **Admin بلا مصادقة:** `firebase-admin` (Node) و`STORAGE_EMULATOR_HOST` (Node وGo) لا يرسلون `Authorization`، وبدء `save()` المتقطع كذلك. **قبولها يعني أن يكون Aruma أكثر تساهلًا من Firebase**، وهذا ممنوع. فتُرفض، ويُوثَّق الطريق المصادَق (التوصية).

## المخاطر

1. **رابط التنزيل يشير إلى Google** في كل وضع تحويل وقت التشغيل (الحقيقة 2):
   - يعمل حيث يحمّله ما يغطيه التحويل: `fetch` المغلّف في الويب، و`HttpURLConnection` في Android (مثل Glide الافتراضي)، و`URLSession` في iOS (مثل `AsyncImage` وKingfisher).
   - ويفشل في: `<img>` و`<video>` وCSS في الويب، وOkHttp وCoil وCronet في Android، و`Image.network` و`dart:io` في Flutter، وأي رابط يُشارَك خارج التطبيق.
   - **وحين يفشل يذهب الطلب إلى Google** حاملًا اسم الدلو ومسار الكائن ورمز التنزيل. Google لا تملك الكائن فترفضه، لكنه تسريب بيانات وصفية إلى طرف ثالث.
   - العلاج على الويب: تبديل الثابت وقت البناء. وعلى الجوال لا حلّ في طبقة الشبكة؛ الخيارات في التوصية.
2. **الرفع في الخلفية (iOS، `putFile` على جهاز):** جلسة الخلفية لا ترى URLProtocol (مقيس). يحميه الشرط 1 على الخادم. ولم يُجرَّب على جهاز حقيقي.
3. **مكتبات تستبدل `XMLHttpRequest` أو `fetch`** (polyfills وأدوات مراقبة لا تستدعي الأصل) تُسقط التحويل في الويب. وكذلك factory آخر لـ `URLStreamHandler` في Android، وURLProtocol يسبق `ArumaRedirect` في iOS. وهي مخاطر Auth نفسها.
4. **تغيّر المكتبة:**
   - لو انتقلت firebase-storage إلى OkHttp أو Cronet، أو بدّل الويب `fetch` بـ XHR، يذهب الطلب إلى Google **ومعه `Authorization: Firebase <idToken>`** الذي أصدره Aruma.
   - العلاج: تثبيت الإصدارات، وتشغيل هذه التجارب أو e2e بحارس في CI قبل كل ترقية (S7).
5. **الذاكرة في iOS:** `ArumaRedirect` يحمل جسم الطلب كله حتى 16 MiB. مع رابط رفع مطلق لا تمرّ القطع به أصلًا (مقيس). وإلا فشل رفع ملف أكبر من 16 MiB.
6. **TLS:**
   - قيس https على الويب (CA مثبّت بالمفتاح العام في Chromium)، وAndroid (`network_security_config`)، وiOS (المقدمة والخلفية)، وNode (`NODE_EXTRA_CA_CERTS`).
   - Go على http فقط: على macOS يتحقق Go بمخزن النظام وحده، وتعديله خارج حدود التجربة.
   - في الإنتاج تكفي شهادة عامة لنطاق Aruma.
7. **Admin Node** ليس له طريق مصادَق عبر `firebase-admin`. ويحتاج المطوّر سطرًا مختلفًا (التوصية)، أو حزمة من Aruma.

## التوصية لكل منصة

القاعدة كما في Auth (ADR-014): Aruma يخدم مسارات Google تحت نطاقه، والمكتبات تحوّل النطاق وحده. وفي كل المنصات، الشرط 1 على الخادم.

| المنصة | ما يُبنى في S6 | ما يضيفه المطوّر |
|---|---|---|
| الويب (Vite) | `@aruma/web/vite` يبدّل `"firebasestorage.googleapis.com"` في `@firebase/storage` وقت البناء، كما يفعل لـ Auth في `mode: 'build'`، ويفشل البناء إن لم يجده | السطر نفسه في `vite.config`. لا كود. **وروابط التنزيل تعمل في `<img>`** |
| الويب (بلا Vite) وFlutter web | `install({ url })` يشمل Storage: `XMLHttpRequest.prototype.open` و`fetch` معًا | `install` أول استيراد، أو في Flutter سكربت module في `web/index.html` قبل `flutter_bootstrap.js`. وتوثيق حدّ `<img>`: `getBlob()` ثم `URL.createObjectURL` |
| Android | `aruma-redirect` يضيف `firebasestorage.googleapis.com` إلى النطاقات، بالفشل المغلق نفسه. لا خطّاف جديد | لا شيء فوق Auth: الاعتمادية و`ArumaURL` في المانيفست |
| iOS | `ArumaRedirect` يضيف النطاق، والجسم يُقرأ كما هو اليوم | لا شيء فوق Auth: الحزمة و`ArumaURL` في Info.plist |
| Flutter (الجوال) | كالأصلية، وحزمة Dart صغيرة اختيارية (`HttpOverrides.global`) تحوّل `dart:io` لرابط التنزيل | سطر Gradle وسطر Podfile كما في Auth. ولـ `Image.network`: سطر `HttpOverrides` أو `getData()` |
| Admin Node | حزمة `@aruma/admin-storage` (كـ `@aruma/admin-auth`) تعيد `Storage` من `@google-cloud/storage` بـ `apiEndpoint` و`authClient` من `token_uri`، والرفع المتقطع عبر `createResumableUpload` | `getStorage` من الحزمة بدل `firebase-admin/storage`. ولا يُدعم `STORAGE_EMULATOR_HOST` في الإنتاج |
| Admin Go | لا مكتبة: إعداد موثّق | `firebase.NewApp(ctx, cfg, option.WithCredentialsFile("key.json"), option.WithEndpoint("https://aruma.example.com/storage/v1/"), storage.WithJSONReads())` ثم `app.Storage(ctx)` |

- **رابط التنزيل على الجوال قرار يحتاج المالك،** لأن كل حلوله سطر كود: helper يعيد كتابة الرابط، أو `HttpOverrides` في Flutter، أو توثيق المكتبات التي تعمل. ونوصي بالتوثيق في v0.3، وبحزمة Flutter الصغيرة في S6.
- **لا يُعتمد وضع المحاكي** لأي منصة في الإنتاج: http على الجوال، ولا مصادقة في Admin.
- **Go:** `option.WithEndpoint` في `NewApp` يصل إلى كل خدمات التطبيق. لم يُقَس أثره على Auth وغيرها، ويُقاس في S4 قبل التوثيق. والبديل `storage.NewClient` منفصلًا.

## ما لم يُقَس

- جهاز iOS حقيقي: `putFile` الفعلي في الخلفية من FirebaseStorage نفسه (قيس `GTMSessionUploadFetcher` بجلسة خلفية على المحاكي)، وتطبيق في الخلفية أو مُنهى أثناء الرفع.
- جهاز Android حقيقي، وإصدارات Android غير API 33.
- Flutter على iOS وعلى الويب (المصدر يكفي للحكم، والويب مقيس بالمكتبة نفسها من CDN).
- Go على https، وأثر `WithEndpoint` على خدمات `firebase-admin-go` الأخرى.
- Admin Python وJava وC++/Unity، وfirebase-tools (`firebase deploy --only storage` لا يمسّ هذا المسار).
- App Check (لم تُضبط، فلم تُرسل `X-Firebase-AppCheck`)، والمصادقة الحقيقية: الخادم التجريبي لا يتحقق من الرموز، والعمليات بلا مستخدم مسجّل.
- سلوك Firebase الحقيقي: لم يُتصل بـ Google. وأشكال الردود من الخادم التجريبي، لا من الإنتاج. وتسجيلها مهمة S0-A.
