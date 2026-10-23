# Cloud Storage في وضع الإنتاج: كيف تصل كل مكتبة رسمية إلى Aruma

- **الحالة:** تجربة مكتملة (S0-B)، 2026-10-24، فرع `s0-storage-path`. نظير أبحاث Auth: [web-production-redirect.md](web-production-redirect.md) و[android-production-redirect.md](android-production-redirect.md) و[ios-production-redirect.md](ios-production-redirect.md).
- **المراجعة:** عُدّلت بعد مراجعة عدائية قُبلت كل نتائجها (التعديلات مذكورة في مواضعها). وفيها قرار يحتاج المالك: [رابط التنزيل](#رابط-التنزيل-تسريب-افتراضي-وقرار-للمالك).
- **السؤال:** كيف تصل كل مكتبة Storage رسمية إلى خادم Aruma **في وضعها العادي**، دون `useEmulator` أو `connectStorageEmulator`؟ وما أقل إضافة تلزم لكل منصة؟
- **الجواب باختصار:**

| المنصة | ما يحمل الطلبات | مكتبة التحويل كما هي | بعد إضافة نطاق Storage إليها |
|---|---|---|---|
| الويب | `XMLHttpRequest` | لا تراها (تلفّ `fetch` وحده) | تعمل العمليات، لكن رابط التنزيل على نطاق Google ويتسرّب من `<img>`. وتبديل النطاق وقت البناء يعيد رابطًا على Aruma |
| Android | `HttpURLConnection` | لا تحوّلها (نطاقات Auth وحدها) | تعمل العمليات بلا خطّاف جديد، ورابط التنزيل على نطاق Google |
| iOS | `URLSession` عبر GTMSessionFetcher | لا تحوّلها | تعمل العمليات، والرفع في الخلفية أيضًا بشرط رابط رفع مطلق إلى Aruma. ورابط التنزيل على نطاق Google |
| Flutter | المكتبات الأصلية (والويب: مكتبة JS) | كالأصلية | كالأصلية، لكن HTTP من Dart (`dart:io`) يذهب بالرابط إلى Google (مقيس) |
| Admin Node | gaxios عبر `@google-cloud/storage` | — | `firebase-admin` لا يصل إلا بلا مصادقة. و`@google-cloud/storage` مباشرةً يعمل بمصادقة Aruma، إلا بدء الرفع المتقطع ما لم يُضبط `universeDomain`. و`getDownloadURL` في firebase-admin يرسل رمز Aruma إلى Google |
| Admin Go | `net/http` عبر `cloud.google.com/go/storage` | — | عميل Storage منفصل بـ `option.WithEndpoint` و`storage.WithJSONReads()` يعمل كاملًا بمصادقة Aruma. وعلى `firebase.NewApp` يكسر الخدمات الأخرى |

- **الأداة:** [test/experiments/storage-path/](../../test/experiments/storage-path/) (طريقة التشغيل في README هناك).
- **لم يسجّل الدخول مستخدم في أي تجربة:** كل طلبات العملاء وصلت بلا `Authorization`. فما يُقال عن إرفاق الرمز في طلبات العملاء مأخوذ من الكود لا من القياس.

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
   - iOS: `scheme` و`host` و`port` من `Storage` (`Internal/StorageGetDownloadURLTask.swift:62-64`).
   - فما دام التحويل يحدث في طبقة الشبكة، يعيد `getDownloadURL()` رابطًا إلى `firebasestorage.googleapis.com`. **وهذا الرابط صلاحية وصول كاملة إلى ملف في Aruma** (القسم الخاص به أدناه).
3. **رابط الرفع المتقطع يحدّده الخادم:** المكتبات الثلاث تقرأ `X-Goog-Upload-URL` من رد أمر `start` وترسل القطع إليه كما هو، رابطًا مطلقًا:
   - الويب `requests.ts:441`، وAndroid `UploadTask.java:296-299`، وiOS (GTMSessionFetcher) `GTMSessionUploadFetcher.m:1064-1083`.
   - فإن أعاد Aruma رابطًا مطلقًا إلى نفسه، ذهبت القطع إليه مباشرةً دون أي تحويل. وهذا ما يحلّ مشكلة الرفع في الخلفية على iOS (أدناه).

## المنهج

- **قراءة الكود** في إصدارات مثبّتة، والمراجع `ملف:سطر` في كل قسم:
  - مكتبات JS وAndroid وiOS وGTMSessionFetcher في `.cache/research/src/storage-path/` (من وسوم الإصدارات، وsources jar لـ Android).
  - Flutter من مخزن pub: `.cache/pub/hosted/pub.dev/firebase_storage-14.0.1` و`firebase_storage_web-3.11.15` و`firebase_core_web-4.1.0` و`http-1.6.0`.
  - حزم Node من `node_modules` التجربة، وحزم Go من مخزن الوحدات.
  - لم يُفكّ أي ثنائي.
- **القياس:** تطبيقات صغيرة بالمكتبات الرسمية أمام **خادم تسجيل** (`stub/main.go`):
  - ليس Aruma ولا يتحقق من شيء. يجيب بروتوكول Firebase Storage REST (`/v0/b/…`، والرفع multipart والمتقطع `X-Goog-Upload-*`) وبروتوكول GCS JSON، بقدر ما يكفي لتكمل المكتبة عملياتها.
  - يقبل البادئة التي تضيفها مكتبات التحويل (`/firebasestorage.googleapis.com/…`) أو بلا بادئة (شكل المحاكي)، على http وhttps.
  - يسجّل لكل طلب: المسار، والبادئة، ونوع `Authorization` وطوله فقط، وترويسات البروتوكول. ولا يسجّل رمزًا ولا مفتاحًا.
- **العمليات:** رفع صغير، ورفع متقطع 3 MB (ومن ملف على الجوال)، وgetMetadata، وgetDownloadURL، وتنزيل، وlist، وdelete. ثم **رابط التنزيل خارج المكتبة**: `fetch` و`<img>` في الويب، و`HttpURLConnection` في Android، و`URLSession.shared` في iOS، و`dart:io` في Flutter.
- **ما تعنيه ✓:**
  - getBytes وdata (iOS) والتنزيل في Node وGo: قورن المحتوى بايتًا ببايت.
  - getFile وgetStream (Android)، وwrite(toFile:) (iOS)، وgetBlob (الويب): **قورن الطول وحده**.
  - getDownloadURL: نجح الاستدعاء. وحيث يُعلَّم ⚠ فالرابط على نطاق Google، وهو خطر لا نجاح (الحقيقة 2).
- **لكل منصة ثلاثة أوضاع:** المحاكي للمقارنة، ومكتبة التحويل كما هي، وأقل إضافة (spike).
- **لا اتصال بـ Google:** حارس شبكة في كل تشغيل، وكل طلب إلى Google يُرفض ويُسجَّل:

| | الحارس |
|---|---|
| الويب | Chromium لا يحلّ اسمًا إلا localhost (`--host-resolver-rules`)، وكل طلب فاشل يُسجَّل |
| Android | iptables يرفض كل حزمة من uid التطبيق خارج loopback (`adb root`)، و`NetworkGuard` يسجّل |
| iOS | `LoopbackGuard` (URLProtocol) في جلسات default وephemeral |
| Node | `test/experiments/netguard.cjs`، ويسجّل `scenario.mjs` ما كان الطلب المرفوض سيحمله (الطريقة، والعنوان، ونوع `Authorization`) |
| Go | `HTTPS_PROXY` إلى وكيل في الخادم يرفض كل طلب ويسجّله |

- لم يخرج طلب HTTP واحد إلى Google في أي تشغيل.
- لكن على Android حلّ نظام التشغيل (netd، لا التطبيق) اسم `firebasestorage.googleapis.com` قبل أن يرفض iptables الاتصال. وهذا استعلام DNS لا أكثر، كما في بحث Auth.

**الإصدارات:**

| | الإصدار |
|---|---|
| الويب | firebase 13.1.0 (`@firebase/storage` 0.14.6)، بناء CDN كما يحمّله Flutter web، في Chromium (Playwright 1.64.1). وبناء Node للمكتبة نفسها لتجربة `host` |
| Android | firebase-storage 23.0.0 (BoM 35.0.2)، AVD `aruma-a34` (API 33) |
| iOS | FirebaseStorage 12.19.2 وGTMSessionFetcher 5.3.1، محاكي iPhone 16 Pro Max (iOS 18.5) |
| Flutter | Flutter 3.44.2، وfirebase_core 5.1.0 وfirebase_storage 14.0.1 (يجلبان BoM 35.1.0، وFirebase iOS 12.19.3، وJS 13.1.0)، على AVD `aruma-a34` |
| Admin Node | firebase-admin 14.5.0، و`@google-cloud/storage` 8.4.0، وgoogle-auth-library 11.3.1، وgtoken 8.0.0، وgaxios 7.3.1، على Node 24.21.0 |
| Admin Go | firebase-admin-go 4.22.0، و`cloud.google.com/go/storage` 1.62.1، و`google.golang.org/api` 0.304.0، و`cloud.google.com/go/auth` v0.24.1-0.20261001053825، على Go 1.27.2 |

## الويب

**طبقة الشبكة:**
- بناء المتصفح يرسل كل طلب بـ `XMLHttpRequest`: `new XMLHttpRequest()` ثم `xhr.open(method, url, true)` (`platform/browser/connection.ts:43` و`:77`). ويُنشأ لكل طلب، فيصل إليه تعديل `XMLHttpRequest.prototype.open` متى ثُبّت.
- بناء Node يستعمل `fetch` (`platform/node/connection.ts:196-211`).
- الرفع: `uploadBytes` multipart، و`uploadBytesResumable` متقطع إن زاد الحجم على 256 KiB (`task.ts:172-173`).
- الترويسات على **كل** طلب، والقطع منها: `Authorization: Firebase <idToken>` و`X-Firebase-Storage-Version` و`X-Firebase-GMPID` و`X-Firebase-AppCheck` (`implementation/request.ts:237-260`).

**ما يعترضها:** `@aruma/web` يلفّ `fetch` وحده، لنطاقي Auth (`sdk/web/aruma-web/src/index.js:17` و`:140-161`). فلا يرى طلبات Storage.

**الـ spike:** `sdk/web/aruma-web/src/storage-spike.js` يعدّل `XMLHttpRequest.prototype.open` ويلفّ `fetch`، لنطاق Storage وحده.

**النتائج** (`web.sh`، Chromium):

| العملية | emulator | prod، `@aruma/web` كما هو | prod + spike (http) | prod + spike (https) | prod + تبديل الثابت (https) |
|---|---|---|---|---|---|
| رفع صغير (multipart) | ✓ | ✗ | ✓ | ✓ | ✓ |
| رفع متقطع 3 MB | ✓ | ✗ | ✓ | ✓ | ✓ |
| getMetadata | ✓ | ✗ | ✓ | ✓ | ✓ |
| getDownloadURL | ✓ | ✗ | ⚠ رابط Google | ⚠ رابط Google | ✓ رابط Aruma |
| getBytes 3 MB، getBlob (الطول) | ✓ | ✗ | ✓ | ✓ | ✓ |
| list | ✓ | ✗ | ✓ | ✓ | ✓ |
| رابط التنزيل بـ `fetch` | ✓ | — | ✓ (لفّ fetch حوّله) | ✓ | ✓ |
| رابط التنزيل في `<img>` | ✓ | — | **✗ تسرّب** (إلى Google، رفضه الحارس) | **✗ تسرّب** | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ | ✓ |
| **المجموع** | **11/11** | **1/11** | **10/11، منها ⚠ واحدة** | **10/11، منها ⚠ واحدة** | **11/11** |

- `@aruma/web` كما هو: 5 عناوين مختلفة إلى `firebasestorage.googleapis.com` رفضها الحارس، وصفر طلب وصل إلى الخادم.
- **CORS:** الصفحة على أصل والخادم على آخر، فسبق كل طلب طلبُ preflight. رأى الخادم 10 طلبات `OPTIONS` في كل وضع إنتاجي.
  - ولا يقرأ المتصفح `X-Goog-Upload-URL` إلا إذا أعلنه الخادم في `Access-Control-Expose-Headers`. والحال نفسها مع Google، فليس شرطًا جديدًا.

**تبديل النطاق: ما قيس وما لم يُقَس:**
- **الـ 11/11 قيست على بناء CDN معدَّل يدويًا،** لا على إضافة Vite:
  - استُبدل النص `"firebasestorage.googleapis.com"` (يرد مرة واحدة في `firebase-storage.js`) بـ `"<aruma-host>/firebasestorage.googleapis.com"`.
  - فصار `storage.host` نطاق Aruma مع المسار، وكل الروابط إليه، ورابط التنزيل منها، فعمل في `<img>`.
- **ما يواجهه البناء الحقيقي من npm (لم يُبنَ ولم يُقَس):**
  - في `dist/index.esm.js` النص بعلامتي اقتباس مفردتين: `const DEFAULT_HOST = 'firebasestorage.googleapis.com';` (`:27`)، وهو في تعليق أيضًا (`:2810`). وفي بناءي Node: `index.node.cjs.js:31` و`node-esm/index.node.esm.js:27`. فالإضافة تطابق تعريف الثابت لا أي ظهور للنص، وتفشل إن لم تجده.
  - وضع `build` في `@aruma/web/vite` يرفض Vite قبل 8 (`sdk/web/aruma-web/src/vite.js:89-93`)، لأن مُحسّن الاعتماديات في خادم التطوير لا يمرّ بالإضافة قبل Rolldown.
  - البروتوكول ثابت `_protocol = 'https'` (`service.ts:179`، `dist/index.esm.js:3246`). فـ Aruma على http (تطوير محلي) يحتاج تبديله أيضًا. وفي الإنتاج https فلا يلزم.
  - **والمضيف المقترح هو نطاق Aruma وحده بلا بادئة،** فيصير الرابط `https://<aruma>/v0/b/…` (الشكل الموحّد، أدناه). أما البادئة التي جُرّبت فتنتج شكلًا لا يفهمه iOS.
- **البديل لمن لا يستعمل Vite: `getStorage(app).host = '<aruma-host>'`** (مقيس ببناء Node، `web.sh node-host`):
  - المُعدِّل موجود في الكود (`service.ts:218-224`) ويعيد حساب الدلو، لكنه **ليس في الأنواع العامة** (`dist/storage-public.d.ts`)، فهو واجهة داخلية قد تتغير.
  - النتيجة: الرفع وgetBytes وdelete عبر Aruma، و`getDownloadURL()` يعيد `https://<aruma>/v0/b/…`، وجلبه بـ `fetch` نجح (200).
  - هو سطر كود في التطبيق، لا صفر كود، ويجب أن يسبق أي عملية Storage.

## Android

**طبقة الشبكة:** `java.net.HttpURLConnection`، لا OkHttp ولا Cronet:
- `NetworkRequest.createConnection()` يبني `new URL(...)` ويعطيه لـ `connectionFactory` (`network/NetworkRequest.java:298-315`).
- وهو `HttpURLConnectionFactoryImpl`: `(HttpURLConnection) url.openConnection()` (`network/connection/HttpURLConnectionFactoryImpl.java:27-29`).
- الحقل `connectionFactory` خاص بالحزمة "for test purposes" (`NetworkRequest.java:70-72`)، فليس واجهة.
- كل رفع متقطع، حتى الصغير: `start` ثم قطع (`UploadTask.java:273-300`). وكل طلب، والقطع منها، يحمل `Authorization` إن وُجد مستخدم (`NetworkRequest.java:324`).
- قُرئت ترويسة `X-Goog-Upload-URL` بمطابقة حرفية للمفتاح (`NetworkRequest.java:490-499`). لكن خريطة ترويسات المنصة لا تفرّق بين الأحرف، فنجح الإملاءان (أدناه).

**ما يعترضها:** factory الـ `URLStreamHandler` في `aruma-redirect` يرى كل `URL.openConnection` في العملية. لكنه لا يحوّل إلا نطاقات Auth (`AuthEndpoints.java`). فلا يلزم خطّاف جديد، بل نطاق جديد.

**الـ spike:** `ArumaRedirect.setStorageSpike(true)` يضيف `firebasestorage.googleapis.com` إلى النطاقات المحوَّلة.

**النتائج** (`android.sh`، instrumented test، عملية جديدة لكل وضع):

| العملية | emulator | prod، `aruma-redirect` كما هو | prod + spike (http) | prod + spike (https) |
|---|---|---|---|---|
| putBytes صغير | ✓ | ✗ "Unable to obtain an upload URL" | ✓ | ✓ |
| putBytes 3 MB | ✓ | ✗ | ✓ | ✓ |
| putFile 3 MB | ✓ | ✗ | ✓ | ✓ |
| getMetadata | ✓ | ✗ | ✓ | ✓ |
| getDownloadUrl | ✓ | ✗ | ⚠ رابط Google | ⚠ رابط Google |
| getBytes (المحتوى)، getFile وgetStream (الطول) | ✓ | ✗ | ✓ | ✓ |
| listAll | ✓ | ✗ | ✓ | ✓ |
| رابط التنزيل بـ `HttpURLConnection` | ✓ | — | ✓ (الـ factory حوّله) | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ |
| **المجموع** | **12/12** | **1/12** | **12/12، منها ⚠ واحدة** | **12/12، منها ⚠ واحدة** |
| حزم رفضها iptables | 0 | 320 | 0 | 0 |

- `aruma-redirect` كما هو: سجّل `NetworkGuard` 10 اتصالات إلى `firebasestorage.googleapis.com`، ورفض iptables حزمها (320 حزمة)، ولم يصل إلى الخادم شيء.
- مع الـ spike: مرّ 13 طلبًا بالتحويل (سجل `ArumaRedirect` في logcat): 3 بدايات رفع، وطلبا بيانات وصفية، و4 تنزيلات، وlist، و3 حذف. أما **9 طلبات قطع** فذهبت مباشرةً إلى رابط الرفع المطلق الذي أعاده الخادم.
- **تنويعتان** (prod + spike): الخادم يكتب أسماء الترويسات بإملاء Go (`X-Goog-Upload-Url`): 12/12. والخادم يعيد رابط رفع على نطاق Google: 12/12، و22 طلبًا محوَّلًا، أي القطع أيضًا.
- https: CA الخادم المؤقت في `network_security_config` للتطبيق التجريبي وحده.

## iOS

**طبقة الشبكة:** `URLSession` عبر GTMSessionFetcher:
- `StorageFetcherService` يبني `GTMSessionFetcherService` لكل تطبيق ودلو، ويضع عليه `StorageTokenAuthorizer` (`Internal/StorageFetcherService.swift:37-60`، والمُفوِّض في `:56`). وكل fetcher عادي يأخذ إعداد `ephemeral` (`GTMSessionFetcher.m:1072-1082`)، فيمرّ بـ URLProtocol الذي تضعه `ArumaRedirect` أولًا.
- http مسموح للخادم في وضع المحاكي وحده (`StorageFetcherService.swift:57-60`). والرمز لا يُرفق إلا لـ https أو loopback (`Internal/StorageTokenAuthorizer.swift:41-49`).
- الرفع `GTMSessionUploadFetcher` (`StorageUploadTask.swift:102-122`). **وبدء الرفع POST إلى مسار الكائن نفسه:** `/v0/b/<bucket>/o/<name>?uploadType=resumable&name=<name>` (`StorageUploadTask.swift:76-96` مع `Internal/StorageUtils.swift:25-48`)، لا إلى `/o?name=` كالويب وAndroid.
- **القطع بلا مُفوِّض:** كل قطعة fetcher جديد من `[GTMSessionFetcher fetcherWithRequest:chunkRequest]` (`GTMSessionUploadFetcher.m:1573`)، لا من الخدمة التي عليها المُفوِّض. فلا `Authorization` ولا `X-Firebase-AppCheck` ولا `X-Firebase-GMPID` على القطع.
  - مقيس: طلب `start` من iOS حمل `X-Firebase-Gmpid` و`X-Firebase-Storage-Version`، والقطعة (PUT) لم تحمل أيًّا منهما. أما Android فحمل `X-Firebase-Gmpid` على القطع أيضًا.
  - الخادم إذن لا يستطيع أن يفوّض قطع iOS إلا برابط الرفع نفسه (الشرط 11).

**الرفع في الخلفية:**
- `GTMSessionUploadFetcher` يجعل `useBackgroundSession = YES` افتراضيًا (`GTMSessionUploadFetcher.m:330`). لكنه يطبّقه على القطع وحدها، و`start` في المقدمة دائمًا (`:2047-2052` و`:1583-1585`).
- وFirebaseStorage يطفئه على المحاكي، وحيث لا تدعمه البيئة. فيبقى لـ **`putFile` على جهاز حقيقي** (`StorageUploadTask.swift:111-121`).
- جلسة الخلفية لا تستشير أي URLProtocol، فلا تراها `ArumaRedirect`. لكن القطع تذهب إلى `X-Goog-Upload-URL` كما أعاده الخادم.

**النتائج** (`ios.sh`، عملية جديدة لكل وضع):

| العملية | emulator | prod، `ArumaRedirect` كما هو | prod + spike (http) | prod + spike (https) |
|---|---|---|---|---|
| putData صغير، putData 3 MB، putFile 3 MB | ✓ | ✗ (`-1009`، رفضها الحارس) | ✓ | ✓ |
| getMetadata | ✓ | ✗ | ✓ | ✓ |
| downloadURL | ✓ | ✗ | ⚠ رابط Google بـ `:443` صريح | ⚠ كذلك |
| data 3 MB (المحتوى)، write(toFile:) (الطول) | ✓ | ✗ | ✓ | ✓ |
| listAll | ✓ | ✗ | ✓ | ✓ |
| رابط التنزيل بـ `URLSession.shared` | ✓ | — | ✓ (حوّله URLProtocol) | ✓ |
| delete | ✓ | ✗ | ✓ | ✓ |
| **المجموع** | **11/11** | **1/11** | **11/11، منها ⚠ واحدة** | **11/11، منها ⚠ واحدة** |

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

- **الجوال:** `firebase_storage` يستدعي المكتبة الأصلية: BoM في `firebase_storage-14.0.1/android/build.gradle:85-86`، و`Firebase/Storage` في `ios/firebase_storage.podspec:37`. فما يصحّ للأصلية يصحّ له.
  - والمحاكي `useStorageEmulator` http، ويحوّل `localhost` و`127.0.0.1` إلى `10.0.2.2` على Android ما لم يُمرَّر `automaticHostMapping: false` (`lib/src/firebase_storage.dart:160-183`).
  - و`refFromURL` يحلّل الرابط **في Dart**، لا في المكتبة الأصلية (`lib/src/firebase_storage.dart:111-125`، `lib/src/utils.dart:39-89`). فلا يقبل إلا نطاق Google أو `localhost`/`10.0.2.2`.
- **الويب:** `firebase_core_web` يحمّل مكتبة JS 13.1.0 من `https://www.gstatic.com/firebasejs/…` (`firebase_core_web-4.1.0/lib/src/firebase_sdk_version.dart:9`). فطلبات Storage هي `XMLHttpRequest` الويب نفسها، ولا يصلح تبديل الثابت وقت البناء. ويصلح مُعدِّل `host` (أعلاه)، لكن من JS لا من Dart.
  - و`getData()` على الويب لا يستعمل المكتبة: يطلب `getDownloadURL()` ثم ينزّل بـ `package:http` (`firebase_storage_web-3.11.15/lib/src/reference_web.dart:118-133`).
  - و`BrowserClient` في http 1.6.0 يستدعي `window.fetch` (`http-1.6.0/lib/src/browser_client.dart:36-40`). فلا يغطيه إلا لفّ `fetch` يشمل نطاق Storage.
- **HTTP من Dart:** `Image.network` و`package:http` على الجوال يستعملان `dart:io` بمقابسه الخاصة، لا `HttpURLConnection` ولا `URLSession`. فلا تراها مكتبات التحويل الأصلية (مقيس أدناه).

**النتائج** (`flutter.sh`، Android، `aruma-redirect` مع الـ spike يثبّته `MainActivity` قبل المحرك):

| العملية | emulator | prod + spike (http) |
|---|---|---|
| putData صغير، putData 3 MB | ✓ | ✓ |
| getMetadata | ✓ | ✓ |
| getDownloadURL | ✓ | ⚠ رابط Google |
| getData 3 MB (الطول)، listAll، delete | ✓ | ✓ |
| رابط التنزيل بـ `dart:io` `HttpClient` | ✓ (إلى `127.0.0.1`) | **✗ تسرّب**: ذهب إلى `firebasestorage.googleapis.com` ورفضه iptables (`Connection refused`) |
| **المجموع** | **9/9** | **8/9، منها ⚠ واحدة** |
| حزم رفضها iptables | 0 | 32 (كلها من `dart:io`) |

- مرّ 8 طلبات بـ `aruma-redirect` (سجل logcat)، والقطع إلى رابط الرفع المطلق مباشرةً، كما في Android الأصلي.
- `dart:io` لم يمرّ بالـ factory: هذا ما يحدث لـ `Image.network(await ref.getDownloadURL())`.

## Admin Node

**ما يفعله `firebase-admin`:**
- `getStorage()` يبني `new Storage({ projectId, credentials: { private_key, client_email } })` لاعتماد `cert()`، أو `new Storage()` لـ ADC. وأي اعتماد آخر خطأ `invalid-credential` (`src/storage/storage.ts:73-95`). فلا مكان فيه لـ `apiEndpoint` ولا لـ `token_uri`.
- `FIREBASE_STORAGE_EMULATOR_HOST` يصير `STORAGE_EMULATOR_HOST=http://…` (`storage.ts:48-59`).
- **`getDownloadURL(file)`** يطلب البيانات الوصفية من `STORAGE_EMULATOR_HOST || https://firebasestorage.googleapis.com` + `/v0` (`lib/storage/index.js:65-67`)، بـ `file.storage.makeAuthenticatedRequest` (`lib/storage/utils.js:5-11`). أي بالعميل المفوَّض الذي بُني به الملف (`nodejs-common/util.js:494-501`).

**وما يفعله `@google-cloud/storage` 8.4.0** (المسارات في `build/cjs/src/`):
- `STORAGE_EMULATOR_HOST` أو `apiEndpoint` يجعلان `customEndpoint = true` (`storage.js:461-472`). ومع المتغير يصير عنوان JSON هو المتغير **بلا `/storage/v1`** (`storage.js:474`)، فالطلبات إلى `/b/<bucket>/o/…`.
- مع `customEndpoint` لا مصادقة، إلا بـ `useAuthWithCustomEndpoint: true` (`nodejs-common/util.js:495-500`).
- `save()` و`createWriteStream()` متقطعان افتراضيًا (`file.js:1569`). و`startResumableUpload_` لا يمرّر `useAuthWithCustomEndpoint` (`file.js:3531-3560`، بخلاف `createResumableUpload` في `:1293`). والرفع المتقطع يتجاوز المصادقة لنطاق خارج «الكون» إن لم يصله ذلك الخيار (`resumable-upload.js:124-147`). فهذان يبدآن الجلسة **بلا مصادقة**.
- **لكن «الكون» نفسه مخرج:** `universeDomain` (`storage.js:459`) يُمرَّر إلى الرفع المتقطع (`file.js:3556`)، والرفع يُبقي العميل المفوَّض إن كان اسم المضيف هو الكون أو تحته (`resumable-upload.js:132-139`).
- `keyFilename` يمرّ بـ gtoken، وهو يثبّت `https://oauth2.googleapis.com/token` (`gtoken/build/cjs/src/index.cjs:55`). وهي نتيجة T22 نفسها في [experiments.md](experiments.md).

**النتائج** (`admin-node.sh`، مفتاح حساب خدمة مؤقت `token_uri` فيه للخادم، https إلا حيث ذُكر http):

| الإعداد | النتيجة | Authorization على الخادم |
|---|---|---|
| `firebase-admin` + `cert()`، بلا متغير | 1/8. تبادل الرمز إلى `oauth2.googleapis.com` (رفضه الحارس) | — |
| `firebase-admin` + `cert()` + `STORAGE_EMULATOR_HOST=http://…` | 8/8 | **لا شيء، في كل الطلبات** |
| الشيء نفسه مع `https://…` | 8/8، و`getDownloadURL` بالشكل الموحّد `https://<host>/v0/b/…` | لا شيء |
| `FIREBASE_STORAGE_EMULATOR_HOST=host:port` | 8/8 (http) | لا شيء |
| `firebase-admin` + `arumaCredential` (T22) | 0/1: `storage/invalid-credential` | — |
| `@google-cloud/storage` + `apiEndpoint` | 9/10 | لا شيء |
| + `useAuthWithCustomEndpoint` + `keyFilename` | 2/10. gtoken إلى `oauth2.googleapis.com` (رفضه الحارس). ونجح `save()` المتقطع وحده، لأنه لم يطلب رمزًا أصلًا | — |
| + `useAuthWithCustomEndpoint` + `authClient` يأخذ رمزه من `token_uri` Aruma | 9/10 | `Bearer` على الكل، **إلا بدء `save()` المتقطع وقطعه** |
| الشيء نفسه + `universeDomain` = مضيف Aruma (للـ Storage وللـ `authClient`) | 9/10 | **`Bearer` على الكل، والرفع المتقطع منه** |
| الشيء نفسه و`apiEndpoint` ببادئة `/storage.googleapis.com` | 9/10، والبادئة محفوظة في كل المسارات | كالسطر قبل السابق |

- مع `@google-cloud/storage` العمليات 10: الست، والالتفاف أدناه، و`getDownloadURL` من firebase-admin، ونسخة الغلاف منه، وinit.
- **`getDownloadURL` من firebase-admin على ملف من `@google-cloud/storage`** هي الفاشلة في كل سطر 9/10: أرسلت `GET https://firebasestorage.googleapis.com/v0/b/…/o/s0%2Fsmall.txt`، **ومعها `Authorization: Bearer <رمز Aruma>`** حيث العميل مفوَّض بـ Aruma (الأسطر الثلاثة الأخيرة)، ورفضها الحارس. سجّل `scenario.mjs` الطلب قبل رفض المقبس.
- **ونسخة الغلاف منها** (`wrapper.getDownloadURL` في `scenario.mjs`): البيانات الوصفية من أصل Aruma بالعميل نفسه، ورابط `https://<aruma>/v0/b/…?alt=media&token=…`. نجحت في كل سطر فيه عميل يصل إلى Aruma.
- **الالتفاف الآخر للرفع المتقطع:** `createResumableUpload()` (بـ `Bearer`)، ثم `save(data, { uri })`. الاستعلام والقطعة بعده بلا مصادقة، كما في GCS: رابط الجلسة نفسه هو التفويض.
- `authClient` هنا `OAuth2Client` من google-auth-library، و`refreshHandler` يستدعي `arumaCredential(key).getAccessToken()`.
- لم يُجرَّب `universeDomain` مع `keyFilename` (google-auth-library قد يوقّع JWT ذاتيًا لكون غير Google)، ولا مع نطاق فرعي.

## Admin Go

**ما يفعله `cloud.google.com/go/storage` 1.62.1:**
- مع `STORAGE_EMULATOR_HOST` يُضاف `option.WithoutAuthentication()` قبل خيارات المستخدم (`storage.go:160-201`). وقيس أن `option.WithCredentialsFile` لا يلغيه: لا `Authorization`.
- `option.WithEndpoint` يُستعمل للـ JSON API كما هو. لكن:
  - **القراءة الافتراضية XML** إلى `<scheme>://<endpoint host>/<bucket>/<object>`، بلا مسار الـ endpoint (`storage.go:216-231`، `http_client.go:884-891`).
  - **والرفع إلى `/upload/storage/v1/…` مطلقًا من الجذر** (`google.golang.org/api/storage/v1/storage-gen.go:11385-11387`)، فتسقط أي بادئة.
- الرفع المتقطع يرسل `X-GUploader-No-308: yes` وينتظر 200 مع `X-Http-Status-Code-Override: 308` بدل 308 (`internal/gensupport/resumable.go:109-124` و`:281-284`). وقطعه POST لا PUT.
- **المصادقة** عبر `cloud.google.com/go/auth`: مفتاح حساب الخدمة يُبادَل عند `token_uri` الذي في الملف (`credentials/filetypes.go:148`)، إلا إذا كان في الملف `universe_domain` غير Google فيوقّع JWT ذاتيًا (`:136-141`). مقيس: وصل تبادل الرمز إلى الخادم في كل تشغيل. (كانت الوثيقة تنسبه إلى `golang.org/x/oauth2`، وذاك ما قاسته T22 في Aruma نفسه.)
- **`firebase-admin-go` يمرّر خيارات التطبيق كلها إلى كل خدمة:** إلى Storage (`storage/storage.go:37-45`)، وإلى FCM التي تأخذ الـ endpoint من `transport.NewHTTPClient(ctx, c.Opts...)` (`messaging/messaging.go:915-924`)، وإلى Firestore (`firebase.go:110-120`). ويحوّل `FIREBASE_STORAGE_EMULATOR_HOST` إلى `STORAGE_EMULATOR_HOST`.

**النتائج** (`admin-go.sh`، http):

| الإعداد | النتيجة | Authorization | ملاحظة |
|---|---|---|---|
| `app.Storage` + المفتاح، بلا شيء آخر | 1/7. تبادل الرمز وصل إلى الخادم، ثم 7 `CONNECT storage.googleapis.com:443` رفضها الحارس | — | — |
| `STORAGE_EMULATOR_HOST` | 7/7 | **لا شيء** | قراءة XML `/s0path.appspot.com/s0%2Fbig.bin` |
| `option.WithEndpoint(<aruma>/storage/v1/)` على `firebase.NewApp` | 7/7 | `Bearer` على الكل | قراءة XML من الجذر |
| + `storage.WithJSONReads()` | 7/7 | `Bearer` | القراءة `/storage/v1/…?alt=media` |
| endpoint ببادئة `/storage.googleapis.com/storage/v1/` + JSON reads | 7/7 | `Bearer` | **الرفع إلى `/upload/storage/v1/…` بلا البادئة** |
| `storage.NewClient` منفصلًا + `WithEndpoint` | 7/7 | `Bearer` | قراءة XML من الجذر |
| **`storage.NewClient` منفصلًا + `WithEndpoint` + `WithJSONReads()`** | **7/7** | `Bearer` | كل المسارات تحت `/storage/v1/` و`/upload/storage/v1/` |
| **`WithEndpoint` على `firebase.NewApp`، ثم `app.Messaging(ctx).Send`** | **فشل**: 404 | `Bearer` | **ذهب FCM إلى `<aruma>/storage/v1//projects/s0path/messages:send`** |

## جدول النتائج المجمّع

الأرقام: العمليات الناجحة من كل العمليات، و⚠ عدد ما نجح منها برابط تنزيل على نطاق Google.

| المنصة | المحاكي | الإنتاج، مكتبة التحويل كما هي | الإنتاج + أقل إضافة (http) | الإنتاج + أقل إضافة (https) | رابط التنزيل خارج المكتبة |
|---|---|---|---|---|---|
| الويب | 11/11 | 1/11 | 10/11 (⚠ 1) | 10/11 (⚠ 1) | `fetch` ✓، `<img>` يتسرّب. مع تبديل الثابت: 11/11 بلا ⚠ |
| Android | 12/12 | 1/12 | 12/12 (⚠ 1) | 12/12 (⚠ 1) | `HttpURLConnection` ✓ |
| iOS | 11/11 | 1/11 | 11/11 (⚠ 1)، والخلفية ✓ | 11/11 (⚠ 1)، والخلفية ✓ | `URLSession.shared` ✓ |
| Flutter (Android) | 9/9 | (المكتبة الأصلية، كـ Android) | 8/9 (⚠ 1) | (كـ Android) | `dart:io` يتسرّب |
| Admin Node | 8/8 بلا مصادقة (http وhttps) | — | — | `@google-cloud/storage` + `authClient`: 9/10 بـ `Bearer`، إلا بدء `save()` المتقطع (ومع `universeDomain` حتى هو)، و`getDownloadURL` من firebase-admin يرسل الرمز إلى Google | — |
| Admin Go | 7/7 بلا مصادقة | — | عميل منفصل بـ `WithEndpoint` و`WithJSONReads`: 7/7 بـ `Bearer` | لم يُقَس | — |

## رابط التنزيل: تسريب افتراضي، وقرار للمالك

**ما يتسرّب ليس بيانات وصفية بل صلاحية:**
- الرابط الذي يعيده `getDownloadURL()` في أوضاع التحويل وقت التشغيل: `https://firebasestorage.googleapis.com/v0/b/<bucket>/o/<path>?alt=media&token=<token>`.
- واسم الدلو في Aruma هو `<projectId>.<ProjectDomain>` (`internal/tenancy/core/projects.go:49`)، أي أن الرابط يحمل نطاق Aruma نفسه.
- و`token` رمز التنزيل الذي يفتح الملف دون القواعد (خطة S3). فمن يحصل على الرابط يبدّل المضيف بنطاق Aruma ويقرأ الملف، **حتى يُلغى الرمز**.
- فكل حمّال لا يغطيه التحويل يرسل هذه الصلاحية إلى خوادم Google، ويفشل التحميل. وكذلك كل رابط يُخزَّن أو يُشارَك.

**ما يتسرّب افتراضيًا** (تطبيق يستعمل الرابط كما أعادته المكتبة، مع التحويل وقت التشغيل):

| الحمّال | المنصة | الحال | الدليل |
|---|---|---|---|
| `Image.network`، `CachedNetworkImage` (عبر `dart:io`) | Flutter الجوال | يتسرّب | **مقيس** (`dart:io` `HttpClient`، Android) |
| `<img>` (و`<video>` وCSS بالآلية نفسها) | الويب | يتسرّب | **مقيس** لـ `<img>`. الباقي من الآلية: طلبات لا تمرّ بـ XHR ولا `fetch` |
| جلسة `URLSession` في الخلفية | iOS | يتسرّب | جزئيًا: مقيس أن الخلفية تتجاهل URLProtocol، والتنزيل نفسه لم يُقَس |
| Coil، OkHttp، Glide مع OkHttp، Cronet | Android | يتسرّب | لم يُقَس (لا يمرّ بـ `URLStreamHandler`) |
| `MediaPlayer`، `DownloadManager`، WebView | Android | يتسرّب | لم يُقَس (عمليات النظام أو حزمة Chromium) |
| `AVPlayer`، `WKWebView` | iOS | يتسرّب | لم يُقَس (خارج URLProtocol التطبيق) |
| رابط يُشارَك أو يُخزَّن ثم يُفتح في مكان آخر | كل المنصات | يتسرّب | من الآلية |
| `fetch` (مع لفّه)، `HttpURLConnection` (مثل Glide الافتراضي)، `URLSession.shared` (مثل `AsyncImage`) | الويب، Android، iOS | لا يتسرّب | **مقيس** |

**الخيارات** (لكلٍّ ما يثبته الدليل، وما يكلّف):

| الخيار | المنصة | ما يحلّ | الكلفة والقيود | الدليل |
|---|---|---|---|---|
| تبديل النطاق وقت البناء (إضافة Vite) | الويب | كل شيء: الروابط على Aruma من المصدر | Vite 8 فقط، ولا يصلح لـ Flutter web (CDN). سطر في `vite.config` | 11/11 على بناء CDN معدَّل يدويًا. الإضافة لم تُبنَ |
| مُعدِّل `getStorage(app).host` | الويب، وربما Flutter web (من JS، على نسخة JS التي ينشئها `firebase_storage_web`) | كل شيء كالسابق | سطر كود قبل أي عملية. واجهة داخلية غير منشورة في الأنواع | مقيس ببناء Node. لم يُجرَّب في Flutter web |
| تبديل `PROD_BASE_URL` في bytecode وقت البناء (إضافة Gradle) | Android | كل شيء: `NetworkRequest.java:52` يغذّي الطلبات (`:97-104`) والرابط (`StorageReferenceUri.java:38-46`) والتحليل (`Util.java:101`) | **يعدّل كود Google المُوزَّع داخل التطبيق: قرار قانوني للمالك** ([android-production-redirect.md](android-production-redirect.md):201-202). و`PROD_BASE_URL` حقل `static final Uri` يُبنى في `<clinit>`، فيُبدَّل النص في ثوابت الصنف | لم يُجرَّب |
| ترقيع المصدر وقت البناء | iOS | كل شيء: `host` في `Storage.swift:285` | SwiftPM وCocoaPods يبنيان FirebaseStorage من المصدر، فيمكن، لكنه القرار القانوني نفسه، وصيانة نسخة معدّلة | لم يُجرَّب |
| `toArumaUrl(url)` في كل حزمة Aruma (JS وKotlin وSwift وDart) | الكل | يحوّل رابط Google (بـ `:443` أو بدونه) إلى الشكل الموحّد قبل العرض أو الحفظ أو المشاركة | سطر عند كل استعمال. ما يُنسى يتسرّب | لم يُبنَ |
| `HttpOverrides.global` في حزمة Dart | Flutter الجوال | `Image.network` و`CachedNetworkImage` وكل `dart:io` | سطر في `main()`. ولا يغطي ما يُفتح بالرابط خارج Dart | لم يُجرَّب |
| Interceptor لـ OkHttp | Android | Coil وOkHttp وGlide-OkHttp | سطر عند بناء كل `OkHttpClient`. ولا يغطي Cronet ولا النظام | لم يُجرَّب |
| إلغاء رموز التنزيل وتدويرها من Aruma | الخادم | يحدّ أثر ما تسرّب، ولا يمنعه | واجهة في S3 واللوحة | — |

- **تفضيل الكاتب** (والقرار للمالك): تبديل النطاق وقت البناء في الويب افتراضيًا، و`toArumaUrl` في كل الحزم، و`HttpOverrides` وInterceptor OkHttp مدمجين في حزمتي Flutter وAndroid بسطر واحد، وإلغاء الرموز في S3. أما تعديل كود Google (Android وiOS) فلا يُبنى إلا بقرار قانوني صريح.
- **التوثيق وحده ليس خيارًا:** التسريب يقع دون أن يكتب المطوّر سطرًا خاطئًا.

## شكل رابط التنزيل عبر المنصات

كل منصة تنتج شكلًا وتقبل في `refFromURL` أشكالًا مختلفة. فرابط تحفظه منصة قد ينكسر في أخرى أو يتسرّب منها:

| ما تقبله `refFromURL` ← | رابط Google | رابط Google بـ `:443` (ما ينتجه iOS) | الشكل المسبوق `<aruma>/firebasestorage.googleapis.com/v0/…` | الشكل الموحّد `<aruma>/v0/…` |
|---|---|---|---|---|
| الويب، النطاق الافتراضي | ✓ | **✗** `invalid-url` | ✗ | ✗ |
| الويب، `host = <aruma>` | ✗ | ✗ | **✗ صامت**: دلو `firebasestorage.googleapis.com` ومسار `v0/b/…` | ✓ |
| Android (`Util.normalize`) | ✓ | ✓ | ✗ `IllegalArgumentException` | ✗ |
| iOS (`StoragePath`) | ✓ | ✓ | ✗، و`reference(forURL:)` ينهي التطبيق (`fatalError`) | ✓ |
| Flutter (Dart، `partsFromHttpUrl`) | ✓ | ✓ | ✗ | ✗ |

- صفّا الويب **مقيسان** (`web.sh node-host`). والباقي من الكود: Android `Util.java:76-132` (يقبل مضيفًا يبدأ بنطاق Google أو تحته فقط، `:101-122`)، وiOS `Internal/StoragePath.swift:81-89` (أي مضيف، بشرط `/v0/b/` أول المسار) و`Storage.swift:140-159`، وFlutter `lib/src/utils.dart:39-89`.
- وما تنتجه كل منصة: الويب وAndroid رابط Google، وiOS رابط Google بـ `:443` (مقيس)، والويب بعد `host` الشكل الموحّد (مقيس)، و`firebase-admin` مع `STORAGE_EMULATOR_HOST` الشكل الموحّد (مقيس).

**المقترح: شكل موحّد واحد، `https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=…`:**
- يخدمه Aruma من جذر نطاقه (هو أصلًا شكل المحاكي، وما يحتاجه `getDownloadURL` في Admin، الشرط 2).
- يفهمه iOS والويب (بعد تبديل النطاق) دون تعديل، ويُحمَّل في أي حمّال دون أن يمرّ بـ Google.
- لا يفهمه Android ولا Flutter ولا الويب في وضع التحويل وقت التشغيل. فتلزمها `fromArumaUrl` (عكس `toArumaUrl`) قبل `refFromURL`، أو تبديل `PROD_BASE_URL` في Android.
- **لا يُستعمل الشكل المسبوق للروابط** أبدًا: لا تفهمه أي منصة، ويفسده الويب بصمت. وتبديل النطاق في إضافة Vite يكون إلى نطاق Aruma وحده، بلا بادئة.
- **في خطة اختبار S6:** مصفوفة `refFromURL` عبر المنصات: الرابط الذي ينتجه `getDownloadURL` على كل منصة (والشكل الموحّد) يُحلَّل على كل منصة أخرى، ويجب أن يعطي الدلو والمسار نفسيهما أو يفشل فشلًا صريحًا.

## ما يلزم خادم Aruma (لـ S3 وS4)

ظهرت هذه من القياس، وكلها (عدا 11) يعمل بها الخادم التجريبي:

1. **`X-Goog-Upload-URL` مطلق إلى `public_url` الخادم** (`https://aruma.example.com/v0/b/…?upload_id=…`). وإلا ذهبت قطع الرفع في الخلفية على iOS إلى Google.
2. **مساران للـ REST نفسه:** `/firebasestorage.googleapis.com/v0/…` (مكتبات التحويل) و`/v0/…` من الجذر (المحاكي، و`getDownloadURL` في Admin، والشكل الموحّد).
3. **بدء الرفع المتقطع على مسار الكائن أيضًا:** iOS يرسل POST إلى `/v0/b/<bucket>/o/<name>?uploadType=resumable&name=<name>`.
4. **الفروق بين العملاء في البروتوكول نفسه:**
   - القطع: POST في الويب وAndroid، وPUT في iOS (قطعة واحدة بـ `upload, finalize`).
   - الحجم الكلي عند `start`: الويب في `X-Goog-Upload-Header-Content-Length`، وiOS في `X-Goog-Upload-Content-Length`، وAndroid لا يرسله (`ResumableNetworkRequest.java:24-27`).
   - نوع المحتوى: `X-Goog-Upload-Header-Content-Type` في الويب وAndroid، و`X-Goog-Upload-Content-Type` في iOS.
5. **CORS** على كل مسارات REST: إجابة preflight لترويسات `Authorization` و`X-Firebase-*` و`X-Goog-Upload-*`، و`Access-Control-Expose-Headers` لـ `X-Goog-Upload-URL` و`-Status` و`-Size-Received` و`-Chunk-Granularity`.
6. **GCS JSON من الجذر** وليس تحت بادئة فقط:
   - `/storage/v1/…` و`/upload/storage/v1/…` و`/download/storage/v1/…`.
   - و`/b/<bucket>/o/…` (Node مع `STORAGE_EMULATOR_HOST`)، إن قُبل ذلك المسار أصلًا (الشرط 10).
   - وقراءة XML `/<bucket>/<object>` إن أردنا Go دون `WithJSONReads`. وهذا يمسّ توجيه ADR-008، فالأسلم أن نوثّق `WithJSONReads()` ولا نخدم XML.
7. **`X-GUploader-No-308: yes`** يُجاب بـ 200 و`X-Http-Status-Code-Override: 308` (Go). و`Content-Range: bytes 0-*/*` يعني «هذا آخر الجسم» (Node).
8. **المسار خام:** أسماء الكائنات فيها `%2F`. فالتوجيه على `EscapedPath()`، لا على مسار مفكوك.
9. **إملاء أسماء الترويسات لا يهم** للمكتبات المقيسة. ويُكتب كما تكتبه Google احتياطًا.
10. **Admin بلا مصادقة:** `firebase-admin` (Node) و`STORAGE_EMULATOR_HOST` (Node وGo) لا يرسلون `Authorization`، وبدء `save()` المتقطع كذلك. **قبولها يعني أن يكون Aruma أكثر تساهلًا من Firebase**، وهذا ممنوع. فتُرفض، ويُوثَّق الطريق المصادَق (التوصية).
11. **رابط الرفع صلاحية:** قطع iOS بلا `Authorization` ولا App Check ولا GMPID (مقيس، أعلاه). فـ:
    - `upload_id` غير قابل للتخمين (128 بت على الأقل)، ويُربط عند `start` بالمشروع والدلو واسم الكائن وهوية الطالب (uid، أو حساب الخدمة) وقرار القواعد المتّخذ حينها، والحجم ونوع المحتوى المعلنين.
    - القطع تُقبل بالـ `upload_id` وحده، و`finalize` يتحقق من الحجم ونوع المحتوى مقابل ما فُوِّض عند `start`. و`Authorization` إن جاء على قطعة لا يوسّع شيئًا، ويُرفض إن خالف هوية `start`.
    - للجلسة عمر محدود، و`upload_id` لا يُكتب في أي سجل (كالرموز).
    - لم يسجّل الدخول مستخدم في أي تجربة، فهذا من الكود والترويسات المقيسة، لا من طلب مفوَّض فعلي.

## المخاطر

1. **رابط التنزيل صلاحية تتسرّب افتراضيًا إلى Google** (القسم الخاص به). وهو أخطر ما في هذا البحث، وحلّه قرار للمالك.
2. **أشكال الروابط لا تتوافق بين المنصات** (القسم الخاص بها): رابط من iOS يرفضه الويب حتى دون Aruma، والشكل المسبوق يُفسد الويب بصمت أو يُنهي تطبيق iOS.
3. **الرفع في الخلفية (iOS، `putFile` على جهاز):** جلسة الخلفية لا ترى URLProtocol (مقيس). يحميه الشرط 1 على الخادم. ولم يُجرَّب على جهاز حقيقي.
4. **مكتبات تستبدل `XMLHttpRequest` أو `fetch`** (polyfills وأدوات مراقبة لا تستدعي الأصل) تُسقط التحويل في الويب. وكذلك factory آخر لـ `URLStreamHandler` في Android، وURLProtocol يسبق `ArumaRedirect` في iOS. وهي مخاطر Auth نفسها.
5. **تغيّر المكتبة:**
   - لو انتقلت firebase-storage إلى OkHttp أو Cronet، أو بدّل الويب `fetch` بـ XHR، يذهب الطلب إلى Google **ومعه `Authorization: Firebase <idToken>`** الذي أصدره Aruma.
   - العلاج: تثبيت الإصدارات، وتشغيل هذه التجارب أو e2e بحارس في CI قبل كل ترقية (S7).
6. **رموز Admin إلى Google:** `getDownloadURL` من firebase-admin يرسل رمز وصول Aruma إلى Google (مقيس). وأي غلاف لا يعيد تعريفه يرث ذلك.
7. **الذاكرة في iOS:** `ArumaRedirect` يحمل جسم الطلب كله حتى 16 MiB. مع رابط رفع مطلق لا تمرّ القطع به أصلًا (مقيس). وإلا فشل رفع ملف أكبر من 16 MiB.
8. **TLS:**
   - قيس https على الويب (CA مثبّت بالمفتاح العام في Chromium)، وAndroid (`network_security_config`)، وiOS (المقدمة والخلفية)، وNode (`NODE_EXTRA_CA_CERTS`).
   - Go على http فقط: على macOS يتحقق Go بمخزن النظام وحده، وتعديله خارج حدود التجربة.
   - في الإنتاج تكفي شهادة عامة لنطاق Aruma.

## التوصية لكل منصة

القاعدة كما في Auth (ADR-014): Aruma يخدم مسارات Google تحت نطاقه، والمكتبات تحوّل النطاق وحده. وفي كل المنصات، الشرطان 1 و11 على الخادم. ورابط التنزيل قرار للمالك من خيارات القسم الخاص به.

| المنصة | ما يُبنى في S6 | ما يضيفه المطوّر |
|---|---|---|
| الويب (Vite 8) | `@aruma/web/vite` يبدّل تعريف `DEFAULT_HOST` في `@firebase/storage` (بأي علامة اقتباس) إلى **نطاق Aruma وحده**، ويفشل البناء إن لم يجده | السطر نفسه في `vite.config`. لا كود |
| الويب (بلا Vite 8) وFlutter web | `install({ url })` يلفّ `XMLHttpRequest.prototype.open` و`fetch` لنطاق Storage، و`useArumaStorage(storage)` يضبط `storage.host` | `install` أول استيراد (أو سكربت module في `web/index.html` قبل `flutter_bootstrap.js`)، وسطر `useArumaStorage` |
| Android | `aruma-redirect` يضيف `firebasestorage.googleapis.com` إلى النطاقات، بالفشل المغلق نفسه. لا خطّاف جديد. ومعه `toArumaUrl`/`fromArumaUrl` وInterceptor OkHttp | الاعتمادية و`ArumaURL` في المانيفست كما في Auth، وما يقرّره المالك لرابط التنزيل |
| iOS | `ArumaRedirect` يضيف النطاق، والجسم يُقرأ كما هو اليوم. ومعه `toArumaUrl`/`fromArumaUrl` | الحزمة و`ArumaURL` في Info.plist كما في Auth، وما يقرّره المالك لرابط التنزيل |
| Flutter (الجوال) | كالأصلية، وحزمة Dart فيها `HttpOverrides` و`toArumaUrl`/`fromArumaUrl` | سطر Gradle وسطر Podfile كما في Auth، وسطر في `main()` |
| Admin Node | حزمة `@aruma/admin-storage` (كـ `@aruma/admin-auth`): تعيد `Storage` من `@google-cloud/storage` بـ `apiEndpoint` و`universeDomain` = نطاق Aruma و`authClient` من `token_uri` و`useAuthWithCustomEndpoint`. **وتصدّر `getDownloadURL` خاصًا بها** (الشكل الموحّد، من أصل Aruma). **وترفض البدء إن وُجد `STORAGE_EMULATOR_HOST` أو `FIREBASE_STORAGE_EMULATOR_HOST`** كما ترفض `@aruma/admin-auth` متغير محاكي Auth | `getStorage` و`getDownloadURL` من الحزمة بدل `firebase-admin/storage` |
| Admin Go | حزمة `adminstorage` (كـ `sdk/go/adminauth`) تعيد `*storage.Client` من `cloud.google.com/go/storage` بـ `option.WithEndpoint(<aruma>/storage/v1/)` و`storage.WithJSONReads()` والمفتاح، **منفصلًا عن `firebase.App`**، وترفض `STORAGE_EMULATOR_HOST`. وتصدّر `DownloadURL` بالشكل الموحّد | `adminstorage.NewClient(ctx, ...)` بدل `app.Storage(ctx)`. **ولا `option.WithEndpoint` على `firebase.NewApp`**: يكسر FCM (مقيس) وFirestore (من الكود) |

- **لا يُعتمد وضع المحاكي** لأي منصة في الإنتاج: http على الجوال، ولا مصادقة في Admin.
- **Node:** طريق `universeDomain` مقيس على مضيف يساوي الكون. ويُعاد قياسه في S4 على مفتاح فيه `universe_domain`، وعلى نطاق فرعي، قبل اعتماده بدل `createResumableUpload`.

## ما لم يُقَس

- **أي طلب مفوَّض من عميل:** لم يسجّل الدخول مستخدم، فلم يُقَس إرفاق `Authorization` على طلبات العملاء ولا على القطع (الويب وAndroid من الكود يرفقانه، وiOS لا يرفقه على القطع، والترويسات الأخرى مقيسة).
- حمّالات جدول التسريب المعلَّمة «لم يُقَس»، وكل خيارات رابط التنزيل عدا مُعدِّل `host` في الويب.
- إضافة Vite على بناء npm (قيس بناء CDN معدَّل يدويًا)، و`_protocol` على http.
- `refFromURL` على Android وiOS وFlutter: من الكود، لا مقيس.
- جهاز iOS حقيقي: `putFile` الفعلي في الخلفية من FirebaseStorage نفسه (قيس `GTMSessionUploadFetcher` بجلسة خلفية على المحاكي)، وتطبيق في الخلفية أو مُنهى أثناء الرفع.
- جهاز Android حقيقي، وإصدارات Android غير API 33.
- Flutter على iOS وعلى الويب (المصدر يكفي للحكم، والويب مقيس بالمكتبة نفسها).
- Go على https، وFirestore مع `WithEndpoint` على التطبيق (من الكود؛ FCM مقيس).
- Admin Python وJava وC++/Unity، وfirebase-tools (`firebase deploy --only storage` لا يمسّ هذا المسار).
- App Check (لم تُضبط)، والتحقق الحقيقي: الخادم التجريبي لا يتحقق من الرموز.
- سلوك Firebase الحقيقي: لم يُتصل بـ Google. وأشكال الردود من الخادم التجريبي، لا من الإنتاج. وتسجيلها مهمة S0-A.
