# المسار A: بروتوكول Firebase Storage كما تراه المكتبات الرسمية (بحث)

- **التاريخ:** 2026-10-23. بحث فقط: لم يُبنَ شيء، ولم يُشغَّل شيء، ولم يُستدعَ أي API لـ Google.
- **السؤال:** ماذا ترسل مكتبات الويب وAndroid وiOS الرسمية إلى `/v0/b/{bucket}/o` بالضبط، وماذا تنتظر في الرد، حتى يعمل خادم Aruma معها دون تعديل؟
- **الطريقة:** قراءة الكود المصدري سطرًا بسطر. لا توجد مواصفة رسمية منشورة لهذا البروتوكول.

## 0. المصادر والرخص

| المستودع | الـ commit / الإصدار | الرخصة | المسارات المقروءة |
|---|---|---|---|
| firebase-js-sdk | `3a5d6a04eb68f8534222cf41dd34ec2ea113a21e` (`@firebase/storage` 0.14.6) | Apache-2.0 | `packages/storage/src` |
| firebase-android-sdk | `ee683fe3d43aff8b0eab6f5ca342ec7280a3fdd3` | Apache-2.0 | `firebase-storage/src/main/java/com/google/firebase/storage` |
| firebase-ios-sdk | `81e706741b26e6e691b49ecfa19d3e9aa88c0bec` | Apache-2.0 | `FirebaseStorage/Sources` |
| gtm-session-fetcher (محرّك الرفع والإعادة في iOS) | `7eebf481798d2863684e4a7c8779325a7e44e13e` | Apache-2.0 | `Sources/Core/GTMSessionUploadFetcher.m`, `GTMSessionFetcher.m` |
| firebase-tools (للمقارنة فقط، ليس عميلًا) | 15.34.0, `4e6eb923dd5e4a081eb227baaee58df85c17d044` | MIT | `src/emulator/storage` |

النسخ في `/Volumes/Work/aurma-project/.cache/research/src/s0c/` (والأصل في `PROVENANCE.md` هناك). firebase-tools في `../firebase-tools`.

**اختصارات الاستشهاد** (أرقام الأسطر من هذه الـ commits):
- `JS` = `firebase-js-sdk/packages/storage/src/`
- `AND` = `firebase-android-sdk/firebase-storage/src/main/java/com/google/firebase/storage/`
- `IOS` = `firebase-ios-sdk/FirebaseStorage/Sources/`
- `GTM` = `gtm-session-fetcher/Sources/Core/GTMSessionUploadFetcher.m`، و`GTMF` = `.../GTMSessionFetcher.m`
- `FT` = `firebase-tools/src/emulator/storage/`: شاهد إضافي، لأن المحاكي الرسمي يعمل مع المكتبات الثلاث.

**الوسوم:** `[V]` = VERIFIED (قرأته في الكود). `[I]` = INFERENCE (استنتاجي، أو سلوك منصة لم أقرأ كودها).

## 1. الخلاصة: ما يفاجئ

1. **iOS يرسل قطع الرفع والـ query والـ cancel بـ `PUT` ودون أي ترويسة مصادقة.** لا `Authorization`، ولا `X-Firebase-*`. فرابط الجلسة هو الصلاحية نفسها. `[V]` GTM 1295, 1540-1573. والمحاكي يحفظ `Authorization` من طلب `start` لهذا السبب (`FT apis/firebase.ts:257-258`).
2. **iOS يبدأ الرفع على `/o/{object}`، لا على `/o`:** `POST /v0/b/{b}/o/{enc}?uploadType=resumable&name={enc}`. `[V]` IOS StorageUploadTask.swift:77-96 (يبني على `baseRequest` للمرجع نفسه، IOS StorageTask.swift:100). والمحاكي يقبل `POST|PUT /b/:bucket/o/:objectId?` (`FT apis/firebase.ts:497-514`).
3. **أسماء ترويسة الحجم والنوع في `start` تختلف بين المنصات:**
   - JS: `X-Goog-Upload-Header-Content-Length` و`X-Goog-Upload-Header-Content-Type`.
   - Android: `X-Goog-Upload-Header-Content-Type` وحدها، بلا حجم.
   - iOS: `X-Goog-Upload-Content-Length` و`X-Goog-Upload-Content-Type`، بلا `Header-`.
   - `[V]` JS requests.ts:427-433; AND network/ResumableNetworkRequest.java:26 + ResumableUploadStartRequest.java:43-45; GTM 566-572.
4. **Android يرسل `updateMetadata` بـ `PUT` مع `X-HTTP-Method-Override: PATCH`.** `[V]` AND network/UpdateMetadataNetworkRequest.java:34,39-41.
5. **JS لا يرسل `uploadType` أبدًا**، ويرسل `name` وحده. فنوع الرفع يُعرف من `X-Goog-Upload-Protocol`. `[V]` JS requests.ts:329-331,361,424.
6. **التواريخ تُحلَّل بصيغة صارمة فيها ثلاث خانات للميلي ثانية بالضبط** (`2024-05-01T12:34:56.789Z`). بغيرها:
   - Android يعيد 0. `[V]` AND internal/Util.java:48,52-67.
   - iOS يعيد `nil`. `[V]` IOS StorageMetadata.swift:228-241.
7. **iOS يفسد قائمة الملفات بصمت** إن احتوى أي عنصر في `items` على قيمة غير نصية: يحوّل إلى `[[String: String]]`. `[V]` IOS StorageListResult.swift:60-66.
   - والشيء نفسه في `metadata` المخصّصة: يحوّلها إلى `[String: String]`. `[V]` IOS StorageMetadata.swift:164.
8. **`nextPageToken: ""` يدخل `listAll` في حلقة لا تنتهي** على المنصات الثلاث.
   - يتوقف الدوران عند `null` أو عند غياب الحقل فقط.
   - والرمز الفارغ لا يُرسل في الطلب التالي، فتُطلب الصفحة الأولى من جديد.
   - `[V]` JS reference.ts:363 + requests.ts:190؛ AND StorageReference.java:628 + network/ListNetworkRequest.java:75؛ IOS StorageReference.swift:342.
9. **iOS لا يعيد المحاولة على 500 ولا على 429.** يعيد على 408/502/503/504 وانقطاع الشبكة فقط. `[V]` GTMF 3618-3643; IOS Internal/StorageFetcherService.swift:82-93.
10. **iOS يرسل `null` في PATCH** لكل حقل أعاده الخادم ولا يعرفه `dictionaryRepresentation`، مثل `downloadTokens` و`etag` و`crc32c`. فإن عامل الخادم `null` كحذف، **مسح iOS رموز التنزيل**. `[V]` IOS StorageMetadata.swift:108-125,205-207,247-255.
11. **Android يلحق بايتات الاستئناف بالملف دون فحص 206.** فإن تجاهل الخادم `Range: bytes=N-` وأعاد 200 بالملف كله، فسد الملف. `[V]` AND FileDownloadTask.java:141-147,199-212.
12. **JS لا يقرأ جسم الخطأ إطلاقًا.**
    - `serverResponse` في المتصفح = `xhr.statusText`، وفي Node = نص فارغ.
    - وكشف App Check يبحث في الـ `statusText` عن النص `Firebase App Check token is invalid`.
    - `[V]` JS platform/browser/connection.ts:118-123; platform/node/connection.ts:97-99; requests.ts:108-117.
13. **لا مكتبة تتحقق من `md5Hash` أو الحجم بعد الرفع.** `[V]` JS requests.ts:559-562 (TODO)؛ ولا استخدام لـ md5 في AND/IOS غير القراءة.
14. **getDownloadURL بلا رموز:** JS يرمي `storage/no-download-url`، وiOS يرمي `unknown`، و**Android ينجح ويعيد `null`**. `[V]` JS reference.ts:472-475; IOS Internal/StorageGetDownloadURLTask.swift:32-40; AND GetDownloadUrlTask.java:58-70,79-86.

## 2. العنوان الأساسي والترويسات المشتركة

### 2.1 العنوان

| | JS | Android | iOS |
|---|---|---|---|
| الإنتاج | `https://firebasestorage.googleapis.com/v0...`<br>`makeUrl = ${protocol}://${host}/v0${urlPart}` | `https://firebasestorage.googleapis.com/v0` | `scheme=https`, `host=firebasestorage.googleapis.com`, **`port=443` صريح** |
| المصدر | JS implementation/constants.ts:24; url.ts:23-33; service.ts:178-179 | AND network/NetworkRequest.java:52 | IOS Storage.swift:289-291; Internal/StorageUtils.swift:22-25 |
| المحاكي | `connectStorageEmulator(s, host, port, {mockUserToken?})`:<br>`host="host:port"`، و`protocol='http'`.<br>`https` فقط إن كان المضيف Cloud Workstation.<br>`mockUserToken` يصير رمز `Authorization` | `useEmulator(host, port)` يعطي `http://host:port/v0` | `useEmulator(withHost:port:)` يضبط `scheme="http"` والمضيف والمنفذ، ويسمح بـ `http` في GTM |
| مصدر المحاكي | JS service.ts:140-163 | AND FirebaseStorage.java:188-190; network/NetworkRequest.java:97-104; internal/StorageReferenceUri.java:38-42 | IOS Storage.swift:210-226; Internal/StorageFetcherService.swift:57-60 |

**البنية:**
- `/v0/b/{bucket}/o` للقائمة، ولبدء الرفع في JS وAndroid، وللرفع متعدد الأجزاء.
- `/v0/b/{bucket}/o/{object}` لما سوى ذلك. `[V]` JS implementation/location.ts:49-57; IOS Internal/StorageUtils.swift:49-58.

ملاحظات:
- iOS يضع المنفذ صراحة دائمًا، فقد تظهر روابط مثل `https://host:443/v0/...` في `getDownloadURL`. `[V]` IOS Internal/StorageGetDownloadURLTask.swift:61-64. ظهور `:443` في النص الناتج `[I]` (سلوك `URLComponents`).
- JS في المتصفح يستعمل `XMLHttpRequest` لا `fetch`. `[V]` JS platform/browser/connection.ts:43.
  - فأي تحويل في وضع الإنتاج يلفّ `fetch` وحده (كما في `docs/research/web-production-redirect.md` لـ Auth) **لن يلتقط** طلبات Storage في المتصفح. `[I]`
  - وفي Node يستعمل `fetch`. `[V]` JS platform/node/connection.ts:196-212.

### 2.2 ترميز المسار

| | الكائن في المسار | `name` في الاستعلام |
|---|---|---|
| JS | `encodeURIComponent` للـ bucket وللمسار، فـ `/` يصير `%2F`، والمسافة `%20`، و`+` يصير `%2B`. `[V]` location.ts:49-57 | `encodeURIComponent`. `[V]` url.ts:35-48 |
| Android | الـ bucket دون ترميز (`appendEncodedPath`). المسار `appendPath(path)`: مقطع واحد، `/` يصير `%2F`. `[V]` internal/StorageReferenceUri.java:46-51. وترميز `/` نفسه `[I]` (سلوك `Uri.Builder` في Android) | `Uri.Builder.appendQueryParameter`. `[V]` network/NetworkRequest.java:304-311 |
| iOS | `GCSEscapedString`: المسموح `A-Za-z0-9-._~!$'()*,=:@`، والباقي يُرمَّز، ومنه `/` و`+` و`&` و`;`. `[V]` Internal/StorageUtils.swift:60-67 | `uploadType=resumable&name=<GCSEscaped>`. `[V]` StorageUploadTask.swift:91-94. والقائمة عبر `URLQueryItem`، مع استبدال `+` بـ `%2B` يدويًا. `[V]` StorageUtils.swift:27-39 |

النتائج على الخادم:
- يصل الكائن **مقطعًا واحدًا مرمَّزًا** يحتوي `%2F`. يجب فكّ ترميزه مرة واحدة، دون تقسيم على `/` قبل الفك.
  - في Go: `r.URL.Path` يفكّ `%2F` إلى `/`. فالمطلوب التوجيه على `r.URL.EscapedPath()`. `[I]`
- لا تصل `+` خامًا تعني مسافة من أي مكتبة. `[V]` (الأسطر أعلاه)
- المكتبات تطبّع المسار قبل الإرسال: تحذف الشرطات في أوله وآخره والمكررة.
  - `[V]` JS implementation/path.ts:37-47; AND internal/Slashes.java:59-77; IOS Internal/StoragePath.swift:114,147-148.

### 2.3 الترويسات المشتركة

| الترويسة | JS | Android | iOS |
|---|---|---|---|
| `Authorization` | `Firebase <token>` إن كان الرمز غير فارغ. `[V]` request.ts:232-239 | `Firebase <token>` إن كان غير فارغ. `[V]` NetworkRequest.java:323-327 | `Firebase <token>` إن وُجد مزوّد Auth وأعاد رمزًا. `[V]` Internal/StorageTokenAuthorizer.swift:51-65 |
| `X-Firebase-Storage-Version` | `webjs/<SDK_VERSION>` (أو `webjs/AppManager`). `[V]` request.ts:241-247; index.ts:54-60 | `Android/<BuildConfig.VERSION_NAME>`. `[V]` NetworkRequest.java:334-335 | `x-firebase-storage-version: ios/<FirebaseVersion()>`. `[V]` StorageTokenAuthorizer.swift:31-33 |
| `X-Firebase-GMPID` | **لا يُرسل أبدًا**: `_appId` ثابت على `null`. `[V]` service.ts:180,330; request.ts:249-253 | `x-firebase-gmpid: <applicationId>` دائمًا. `[V]` NetworkRequest.java:62,93 | `x-firebase-gmpid: <googleAppID>` دائمًا. `[V]` StorageTokenAuthorizer.swift:35-36 |
| `X-Firebase-AppCheck` | إن كان الرمز `!== null` (قد يكون رمزًا بديلًا عند الخطأ). `[V]` request.ts:255-262; service.ts:280-294 | `x-firebase-appcheck` إن كان غير فارغ. `[V]` NetworkRequest.java:63,329-333; internal/Util.java:159-181 | إن وُجد مزوّد App Check. `[V]` StorageTokenAuthorizer.swift:66-81 |
| أخرى | — | `Content-Length` دائمًا. `Content-Type: application/json` لأجسام JSON فقط. `[V]` NetworkRequest.java:356-364 | `User-Agent` فيه `(GTMSUF/1)` لطلبات الرفع. `[V]` GTM 579-590 |

**متى تُحذف ترويسات المصادقة:**
- JS وAndroid: حين لا يوجد مستخدم مسجّل، أو لا App Check. والطلب يُرسل رغم ذلك. `[V]`
- iOS، قاعدة خاصة: إن كان المخطط `http` والمضيف ليس loopback (`localhost` أو `127.0.0.1` أو `::1`):
  - **تُنزع** `Authorization` و`X-Firebase-AppCheck`.
  - ويفشل الطلب بخطأ `unauthenticated`.
  - إلا في بناء DEBUG مع `allowInsecureTokenAttachment`.
  - `[V]` StorageTokenAuthorizer.swift:41-49,83-107.
  - **فعلى Aruma أن يعمل على HTTPS** لأي جهاز iOS حقيقي.
- iOS: قطع الرفع، والـ query، والـ cancel **بلا أي ترويسة من هذه**. `[V]` GTM 1540-1573 (`[GTMSessionFetcher fetcherWithRequest:]` بلا authorizer، والتعليق في 1295).

## 3. العمليات

### 3.1 ملخص

| العملية | JS | Android | iOS |
|---|---|---|---|
| getMetadata | `GET /o/{obj}` | `GET /o/{obj}` | `GET /o/{obj}` |
| download | `GET /o/{obj}?alt=media`، و`Range: bytes=0-N` اختياري | `GET /o/{obj}?alt=media`، و`Range: bytes=N-` عند الاستئناف | `GET /o/{obj}?alt=media` |
| getDownloadURL | GET metadata، ثم يبني الرابط محليًا | مثله | مثله |
| updateMetadata | `PATCH /o/{obj}` | `PUT /o/{obj}` + `X-HTTP-Method-Override: PATCH` | `PATCH /o/{obj}` |
| delete | `DELETE /o/{obj}` | `DELETE /o/{obj}` | `DELETE /o/{obj}` |
| list | `GET /o?prefix=&delimiter=/&maxResults=&pageToken=` | مثله، دون `prefix` في الجذر | مثله، دون `prefix` في الجذر |
| رفع صغير | `POST /o?name=` multipart (`uploadBytes` دائمًا، أو resumable ≤ 256 KiB) | لا يوجد: resumable دائمًا | لا يوجد: resumable دائمًا |
| بدء resumable | `POST /o?name=` | `POST /o?name=&uploadType=resumable` | **`POST /o/{obj}?uploadType=resumable&name=`** |
| قطعة / query / cancel | `POST <upload-url>` (لا cancel) | `POST <upload-url>` | **`PUT <upload-url>`** |

`[V]` JS requests.ts:155-587, reference.ts:252-291; AND network/*.java; IOS Internal/*.swift, StorageUploadTask.swift, StorageDownloadTask.swift:158-163; GTM 1547.

### 3.2 getMetadata

- `GET /v0/b/{b}/o/{obj}`، بلا استعلام.
- الرد: **200 بالضبط** في JS (`successCodes=[200]`)، و2xx في Android، و<300 في iOS.
  - `[V]` JS requests.ts:155-172, requestinfo.ts:62; AND NetworkRequest.java:485-487; GTMF 3476.
- الجسم: كائن JSON يمثّل الـ metadata (القسم 4).
  - JS: نص ليس كائن JSON يعطي `storage/unknown`. `[V]` requests.ts:53-69; json.ts:23-37.
  - iOS: يعطي `unknown` "Invalid data returned from the server". `[V]` Internal/StorageGetMetadataTask.swift:29-37.
  - Android: يتسامح، فالجسم غير الصالح يصير `{}`. `[V]` NetworkRequest.java:203-217.

### 3.3 التنزيل

- **JS:** `GET .../o/{obj}?alt=media`. `[V]` requests.ts:211-232.
  - مع `maxDownloadSizeBytes` يضيف `Range: bytes=0-<max>` (أكبر من المطلوب ببايت)، ويقبل 200 أو 206، ثم يقصّ محليًا لأن "GCS may not honor the Range header". `[V]` requests.ts:227-230; reference.ts:157-199.
- **Android:**
  - `alt=media`، و`Range: bytes=<start>-` فقط إن كان البدء ≠ 0. `[V]` network/GetNetworkRequest.java:31-46.
  - يقبل 2xx أو 308. `[V]` StreamDownloadTask.java:243-245.
  - يقارن `ETag` بين المحاولات: إن تغيّر، أعاد البدء (FileDownloadTask) أو فشل بـ 409 داخلي (StreamDownloadTask). `[V]` FileDownloadTask.java:213-225; StreamDownloadTask.java:133-139.
  - يحسب المجموع من `Content-Length` + الإزاحة. `[V]` FileDownloadTask.java:212.
  - يلحق بالملف عند الاستئناف (`FileOutputStream(file, true)`) دون فحص 206. `[V]` FileDownloadTask.java:141-147.
- **iOS:**
  - يستبدل الاستعلام بـ `alt=media`. `[V]` StorageDownloadTask.swift:158-163.
  - `getData(maxSize:)` يقارن `expectedContentLength` والمستلَم بـ `maxSize`، ولا يرسل `Range`. `[V]` StorageReference.swift:197-230,561-566; StorageDownloadTask.swift:220-222.
  - الاستئناف بعد الإيقاف بـ `resumeData` من `NSURLSession`. `[V]` StorageDownloadTask.swift:166-168. وأن هذا يرسل `Range`/`If-Range` `[I]` (سلوك المنصة).
- المصادقة في المنصات الثلاث بترويسة `Authorization`، لا بـ `token`. `[V]`
- الرابط ذو `token` يُستعمل خارج المكتبة (`<img>`، المتصفح)، فيجب أن يعمل **دون** `Authorization`. `[I]`
- والمحاكي يقبل `token` بديلًا عن القواعد. `FT apis/firebase.ts:119-124`

### 3.4 getDownloadURL

| | JS | Android | iOS |
|---|---|---|---|
| الطلب | GET metadata. `[V]` requests.ts:234-251 | GET metadata. `[V]` GetDownloadUrlTask.java:74-77 | GET metadata. `[V]` Internal/StorageGetDownloadURLTask.swift:22-25 |
| الحقل | `downloadTokens` ويجب أن يكون **string**. يُقسَم على `,`، ويُؤخذ الأول. `[V]` metadata.ts:156-189 | `optString("downloadTokens")`، ثم `split(",")[0]`. `[V]` GetDownloadUrlTask.java:58-62 | `as? String` غير فارغ، ثم `components(separatedBy: ",")[0]`. `[V]` swift:49-56 |
| الرابط | `<protocol>://<host>/v0/b/<enc bucket>/o/<enc name>?alt=media&token=<t>`، والـ bucket والاسم **من الرد**. `[V]` metadata.ts:175-188 | `getHttpUri()` للمرجع + `alt=media&token=`. `[V]` swift:63-66 | `bucket` و`name` **من الرد**، مرمّزان بـ `GCSEscapedString`. `[V]` swift:57-71 |
| بلا رموز | `storage/no-download-url`. `[V]` reference.ts:472-475 | نجاح بقيمة `null`. `[V]` NetworkRequest.java:505-509 | `unknown` "Failed to retrieve a download URL." `[V]` swift:32-40 |

ملاحظة: `FullMetadata.downloadTokens` معرَّف في أنواع JS، لكنه **لا يُملأ أبدًا**، لأنه غائب عن جدول الـ mappings. `[V]` JS public-types.ts:227; metadata.ts:65-112.

### 3.5 updateMetadata

- **JS:** `PATCH`، و`Content-Type: application/json; charset=utf-8`. `[V]` requests.ts:253-275.
  - الجسم من `toResourceString`: الحقول القابلة للكتابة وحدها. `[V]` metadata.ts:191-206; 70-109.
    - `name` (من `fullPath`)، و`md5Hash`، و`cacheControl`، و`contentDisposition`، و`contentEncoding`، و`contentLanguage`، و`contentType`، و`metadata` (من `customMetadata`).
  - `undefined` يُحذف بـ `JSON.stringify`، و`null` يبقى `null`، أي "احذف". `[V]` (سلوك `JSON.stringify` معروف)
  - و`metadata` يُرسل كائنًا كما هو، وقد تحمل مفاتيحه `null`.
  - لو مرّر المستخدم كائنًا من `getMetadata` لأُرسل `name` و`md5Hash` أيضًا. `[V]`
- **Android:** `PUT` + `X-HTTP-Method-Override: PATCH`، و`Content-Type: application/json`. `[V]` network/UpdateMetadataNetworkRequest.java:27-47.
  - الجسم يضم الحقول التي ضبطها المستخدم فقط: `contentType`، و`metadata` (الخريطة كاملة)، و`cacheControl`، و`contentDisposition`، و`contentEncoding`، و`contentLanguage`. `[V]` StorageMetadata.java:271-294.
  - `Builder(original)` يورث علامة "ضبطه المستخدم"، و`parseJSON` يضبطها لكل حقل جاء من الخادم. فتعديل metadata مقروءة يرسل كل حقول المحتوى. `[V]` StorageMetadata.java:113-125,338-372.
  - القيمة `null` تُرسل JSON `null`. `[I]` (سلوك `org.json.JSONObject(Map)`)
- **iOS:** `PATCH`، و`Content-Type: application/json; charset=UTF-8`. `[V]` Internal/StorageUpdateMetadataTask.swift:23-35.
  - الجسم `updatedMetadata()`: فرق بين `dictionaryRepresentation()` الحالي والقاموس الأصلي. `[V]` StorageMetadata.swift:205-207,247-272.
    - المفاتيح الموجودة في الأصل والغائبة الآن تصير `NSNull`، أي `null`. **ومنها كل حقل أعاده الخادم ولا يعرفه iOS** (`downloadTokens` و`etag` و`crc32c` و`storageClass` وغيرها).
    - النصوص المتساوية تُحذف، و`metadata` يُطرح منه تكراريًا.
    - الأرقام مثل `size` تُرسل دائمًا (مقارنة `String` مع `Int64` تفشل).
  - إن كان الكائن جديدًا (`StorageMetadata()`)، فالأصل فارغ، ويُرسل التمثيل كاملًا:
    - `"bucket": ""`. `[V]` swift:111,158
    - `"name"` إن وُجد.
    - و`"metadata": null` حين لا توجد metadata مخصّصة. `[I]` (تحويل `Optional.none` داخل `AnyHashable` إلى `NSNull`)
- الرد في المنصات الثلاث: كائن metadata كامل (iOS يفشل إن لم يكن JSON). `[V]` StorageUpdateMetadataTask.swift:39-47.

### 3.6 الحذف

- `DELETE /o/{obj}`.
- JS يقبل 200 أو 204. `[V]` requests.ts:277-291.
- Android 2xx. `[V]` DeleteStorageTask.java:57-60.
- iOS <300، والجسم يُتجاهل. `[V]` Internal/StorageDeleteTask.swift:24-33.
- المحاكي يعيد 204. `FT apis/firebase.ts:537`

### 3.7 list / listAll

**الطلب:** `GET /v0/b/{b}/o` مع:
- `prefix` = `<path>/`. في الجذر: JS يرسل `prefix=` فارغًا، وAndroid وiOS لا يرسلانه.
  - `[V]` JS requests.ts:181-186; AND network/ListNetworkRequest.java:59-62; IOS Internal/StorageListTask.swift:26-29.
- `delimiter=/` دائمًا. `[V]` JS reference.ts:405-411; AND ListNetworkRequest.java:66; IOS swift:34.
- `maxResults`: 1..1000، يُتحقق منه في العميل. `[V]` JS reference.ts:394-403; AND StorageReference.java:562-565; IOS StorageReference.swift:394-397.
  - `listAll` لا يرسله في المنصات الثلاث. تعليق JS: "maxResults is 1000 by default". `[V]` JS reference.ts:356-358; AND StorageReference.java:617,630.
- `pageToken`: يُرسل فقط إن لم يكن فارغًا. `[V]` JS requests.ts:190; AND ListNetworkRequest.java:75-77.

**الرد** `{prefixes: string[], items: [{name, bucket}], nextPageToken?: string}`:
- `prefixes`: تُحذف شرطة مائلة واحدة من آخر كل واحدة. `[V]` JS list.ts:58-66; AND ListResult.java:47-57; IOS StorageListResult.swift:50-58.
- `items[].name`: الاسم الكامل.
  - Android يرمي `JSONException` إن غاب `name`. `[V]` ListResult.java:59-65
  - iOS يشترط أن تكون **كل** قيم العنصر نصوصًا. `[V]` swift:60-66
  - JS يأخذ `item['name']` فقط. `[V]` list.ts:68-75
- `nextPageToken`: احذفه في آخر صفحة. لا ترسل نصًا فارغًا (البند 1.8).
- المحاكي يعيد `items` بالشكل `{name, bucket}` فقط، مع `nextPageToken` و`prefixes`. `FT apis/firebase.ts:190-198`

### 3.8 الاختيار بين multipart وresumable

| | القاعدة |
|---|---|
| JS | `uploadBytes`/`uploadString` = **multipart دائمًا**، مهما كان الحجم. `[V]` reference.ts:252-273,303-316<br>`uploadBytesResumable` = multipart إن كان `size ≤ 256 KiB`، وإلا resumable. `[V]` task.ts:172-174,184-204 |
| Android | resumable دائمًا: `putBytes` و`putFile` و`putStream`. `[V]` UploadTask.java:240-244,273-300 |
| iOS | resumable دائمًا عبر `GTMSessionUploadFetcher`. `[V]` StorageUploadTask.swift:102-107 |

لا مكتبة تستعمل رفع `media` البسيط (جسم خام بلا ترويسة بروتوكول). `[V]` (لا أثر له في الكود). المحاكي يدعمه احتياطًا فقط. `FT apis/firebase.ts:387-395`

### 3.9 multipart (JS وحده)

`[V]` JS requests.ts:304-376:
```
POST /v0/b/{bucket}/o?name=<encodeURIComponent(fullPath)>
X-Goog-Upload-Protocol: multipart
Content-Type: multipart/related; boundary=<B>
(+ Authorization / X-Firebase-Storage-Version / X-Firebase-AppCheck)

--<B>\r\n
Content-Type: application/json; charset=utf-8\r\n\r\n
<metadata JSON>\r\n
--<B>\r\n
Content-Type: <contentType>\r\n\r\n
<bytes>\r\n
--<B>--
```
- `<B>` = أرقام فقط: سلسلتا `Math.random()` بلا `0.`. `[V]` requests.ts:333-341
- **لا `\r\n` بعد الحدّ الختامي.** `[V]` requests.ts:356
- أجزاء الـ metadata من `toResourceString`. `[V]` requests.ts:304-316,343:
  - `name` = المسار الكامل.
  - `contentType`: إن غاب، يُشتق من نوع الـ Blob، ثم `application/octet-stream`.
  - ثم ما ضبطه المستخدم من `cacheControl` و`contentDisposition` و`contentEncoding` و`contentLanguage` و`md5Hash` و`metadata`.
  - `size` يُضبط داخليًا لكنه **غير قابل للكتابة**، فلا يُرسل.
- الرد: **200** + كائن metadata. نص غير JSON يعطي `storage/unknown`. `[V]` requests.ts:365-375.
- لا حاجة لـ `X-Goog-Upload-Status`.

### 3.10 resumable: البدء (`start`)

| | JS | Android | iOS |
|---|---|---|---|
| الطريقة والرابط | `POST /v0/b/{b}/o?name=<p>` | `POST /v0/b/{b}/o?name=<p>&uploadType=resumable` | `POST /v0/b/{b}/o/{p}?uploadType=resumable&name=<p>` |
| `X-Goog-Upload-Protocol` | `resumable` | `resumable` | `resumable` |
| `X-Goog-Upload-Command` | `start` | `start` | `start` |
| الحجم الكلي | `X-Goog-Upload-Header-Content-Length` | **لا يُرسل** (قد يكون stream) | `X-Goog-Upload-Content-Length` |
| نوع المحتوى | `X-Goog-Upload-Header-Content-Type` | `X-Goog-Upload-Header-Content-Type` | `X-Goog-Upload-Content-Type` |
| `Content-Type` | `application/json; charset=utf-8` | `application/json` إن وُجد جسم | `application/json; charset=UTF-8` |
| الجسم | JSON: `name` و`contentType` دائمًا، والباقي إن ضُبط | JSON بالحقول التي ضبطها المستخدم **دون `name`**. أو **لا جسم** (`Content-Length: 0`) | `dictionaryRepresentation()`: `name` و`contentType` و`"bucket": ""` و`metadata` (أو `null` `[I]`) |
| المصادقة | نعم | نعم | نعم |
| المصدر | requests.ts:415-454 | network/ResumableUploadStartRequest.java:32-78; UploadTask.java:273-300; NetworkRequest.java:345-364 | StorageUploadTask.swift:72-107; GTM 566-590; StorageMetadata.swift:108-125 |

تحديد نوع المحتوى في العميل:
- JS: الـ metadata، ثم نوع الـ Blob، ثم `application/octet-stream`. `[V]` requests.ts:293-316
- Android: الـ metadata، ثم `ContentResolver.getType(uri)`، ثم `application/octet-stream`. `[V]` UploadTask.java:275-285
- iOS: الـ metadata، ثم الامتداد عبر `UTType`، ثم `application/octet-stream`. `[V]` StorageUploadTask.swift:339-341; StorageUtils.swift:69-77

**الرد المطلوب:**

| | JS | Android | iOS |
|---|---|---|---|
| الحالة | **200 بالضبط** | 2xx | <300 |
| `X-Goog-Upload-Status` | **مطلوب = `active`**، وإلا `storage/unknown`. `[V]` requests.ts:400-413,437-447 | إن كان `final` فشل الرفع ("server has terminated..."). `[V]` UploadTask.java:356-366,531-537 | `final` أو `cancelled` يعني فشلًا (501 داخلي). المجهول assert في DEBUG فقط. `[V]` GTM 1056-1081 |
| `X-Goog-Upload-URL` | مطلوب نصًا. `[V]` requests.ts:439-446 | مطلوب، وإلا "Unable to obtain an upload URL". `[V]` UploadTask.java:296-299,328-334 | مطلوب غير فارغ. `[V]` GTM 1064-1083 |
| الجسم | يُتجاهل | يُتجاهل | **يجب أن يكون فارغًا** (assert في DEBUG). `[V]` GTM 1036-1049 |
| اختياري | — | — | `X-Goog-Upload-Chunk-Granularity` يحاذي القطع. `[V]` GTM 1822-1829,1934-1948 |

- الرابط يُستعمل كما هو في المنصات الثلاث، فيجب أن يكون **مطلقًا** بالمخطط والمضيف والمنفذ.
  - `[V]` JS requests.ts:459-487 (`url` مباشرة)؛ AND UploadTask.java:298 (`Uri.parse`)؛ GTM 1083 (`URLWithString`).
- `X-Goog-Upload-Control-URL` لا تقرؤه أي مكتبة. `[V]` (لا أثر له)
- المحاكي يبني الرابط بالشكل `/v0/b/{b}/o?name=..&upload_id=..&upload_protocol=resumable`. `FT apis/firebase.ts:261-272`

### 3.11 resumable: القطع

| | JS | Android | iOS |
|---|---|---|---|
| الطريقة | `POST` | `POST` | **`PUT`** |
| الترويسات | `X-Goog-Upload-Command` و`X-Goog-Upload-Offset` + Auth/Version/AppCheck. **بلا** `X-Goog-Upload-Protocol` | `X-Goog-Upload-Protocol: resumable` و`Command` و`Offset` و`Content-Length` + Auth/GMPID/AppCheck/Version | `X-Goog-Upload-Protocol: resumable` و`Command` و`Offset` و`Content-Length` و`X-Goog-Upload-Content-Length` (الكلي) و`User-Agent`. **بلا مصادقة** |
| الأوامر | `upload`، أو `upload, finalize` للأخيرة. و`finalize` وحده إن كانت البايتات المتبقية صفرًا | `upload`، أو `upload, finalize`، أو **`finalize` بجسم فارغ** | `upload`، أو `upload, finalize`، أو `finalize` إن كان الطول المتبقي 0 |
| حجم القطعة | يبدأ 256 KiB، **×2 بعد كل نجاح**، وحده الأقصى **16 MiB**. يعود 256 KiB بعد أي خطأ | يبدأ 256 KiB، ×2 حتى **32 MiB**. يعود 256 KiB بعد الفشل. قد يُقيَّد بالذاكرة | ملف: **الملف كله في طلب واحد** (`chunkSize=Int64.max`). بيانات: 10 MiB في iOS، و100 MiB في macOS. أو `uploadChunkSizeBytes`. القطعة الأخيرة قد تبتلع حتى 2499 بايتًا زائدة |
| المصدر | requests.ts:497-587; task.ts:287-343 | network/ResumableUploadByteRequest.java:31-84; UploadTask.java:59-60,463-512 | GTM 56-64,1284-1340,1540-1581,1915-1987; IOS Storage.swift:106-109 |

- Android يرسل `finalize` منفردًا بجسم فارغ في حالتين:
  - حجم الملف مضاعف دقيق لحجم القطعة. فـ `reachedEnd` لا يصبح `true` إلا حين تعيد القراءة التالية `-1`.
  - الملف بطول صفر.
  - `[V]` internal/AdaptiveStreamBuffer.java:99-115,144-146; UploadTask.java:465-476; ResumableUploadByteRequest.java:53-58.
- **الرد المطلوب للقطعة الوسيطة:**
  - JS: **200** + `X-Goog-Upload-Status: active`. `[V]` requests.ts:563
  - Android: 2xx. `[V]` UploadTask.java:514-537
  - iOS: <300. `[V]` GTMF 3476
  - **308 يفشل في الثلاث:**
    - JS: ليس ضمن `successCodes`.
    - Android: `isResultSuccess` يتطلب 2xx، فيُقرأ error stream ويُضبط استثناء. `[V]` NetworkRequest.java:391-395,414-416
    - iOS: ≥300 خطأ. `[V]` GTMF 3476,3517-3531
- **`X-Goog-Upload-Size-Received` في رد القطعة:**
  - JS يتجاهله، ويحسب `current + bytesToUpload`. `[V]` requests.ts:564
  - Android يتجاهله.
  - iOS **يعتمده** إزاحة جديدة إن وُجد، وإلا يجمع الإزاحة الحالية مع `Content-Length`. `[V]` GTM 1693-1702
- **الرد الأخير (`final`):**
  - JS: 200 + `X-Goog-Upload-Status: final` + جسم metadata JSON. وإلا `storage/unknown`. `[V]` requests.ts:563-577
  - Android: يحلل الجسم بتسامح (غير JSON يعطي metadata فارغة، ويبقى الرفع ناجحًا). `[V]` UploadTask.java:492-507; NetworkRequest.java:203-217
  - iOS:
    - GTM يأخذ جسم القطعة عند `final` أو `cancelled`. `[V]` GTM 1716-1736
    - ثم Firebase يشترط كائن JSON، وإلا "Invalid data returned from the server". `[V]` StorageUploadTask.swift:183-194
    - و`cancelled` في رد ناجح يتحول خطأ. `[V]` GTM 1719-1726

### 3.12 resumable: الـ query

| | الطلب | ما يُقرأ من الرد | الاستعمال |
|---|---|---|---|
| JS | `POST <url>`، و`X-Goog-Upload-Command: query`، بلا جسم، مع Auth | **200**، و`X-Goog-Upload-Status` ∈ {active, final} (وإلا unknown)، و`X-Goog-Upload-Size-Received` غير فارغ ورقمي (وإلا unknown) | الرقم إزاحة جديدة دون مقارنة. `final` يعني `GET` للـ metadata ثم النجاح |
| Android | `POST <url>`، و`X-Goog-Upload-Protocol: resumable`، و`Command: query`، مع Auth | `final` يعني فشلًا "server has terminated the upload session". الحجم فارغ = 0 | أصغر من المرفوع محليًا: فشل "server lost a chunk". أكبر: يتقدم في الـ stream |
| iOS | `PUT <url>`، و`Command: query`، جسم فارغ، **بلا Auth** | `cancelled`، أو `active` بلا Size-Received، يعني فشلًا 501. `final` أو حجم ≥ الكلي يعني **انتهى، وجسم رد الـ query هو النتيجة** | يُقرَّب الرقم إلى أسفل حسب الـ granularity إن وُجدت |
| المصدر | requests.ts:459-491; task.ts:257-285,191-193,345-365 | UploadTask.java:373-431; network/ResumableUploadQueryRequest.java | GTM 1197-1255,1946-1948; StorageUploadTask.swift:183-194 |

الأثر: حين تكون الجلسة منتهية بنجاح، يجب أن يحمل رد الـ query جسم الـ metadata كاملًا.
- iOS يستعمله نتيجةً، وإلا فشل الرفع رغم نجاحه. `[I]` (مستنتج من GTM 1242-1246 + 1716-1736)
- JS يجلبها بـ GET منفصل. `[V]`
- Android يفشل على أي حال. `[V]`

متى يُرسل الـ query؟
- JS: بعد خطأ قطعة بحالة قابلة للإعادة، أو بعد الإيقاف ثم الاستئناف. `[V]` task.ts:120-144
- Android: بعد أي فشل قطعة، أو عند الاستئناف، أو عند `putFile(..., existingUploadUri)`. `[V]` UploadTask.java:240-244,340-352
- iOS: بعد خطأ قابل للإعادة، أو 4xx (عدا 404) مع `active` وقطعة طولها > 0، أو بعد الاستئناف. `[V]` GTM 1669-1687,1857-1869

### 3.13 resumable: الإلغاء والإيقاف ومدة الجلسة

- **JS لا يرسل `cancel` أبدًا**: يقطع الـ XHR فقط. `[V]` (لا `'cancel'` في `X-Goog-Upload-Command`؛ task.ts:409-421)
- Android: `POST <url>`، و`X-Goog-Upload-Protocol: resumable`، و`X-Goog-Upload-Command: cancel`، مع Auth، والرد يُتجاهل. `[V]` network/ResumableUploadCancelRequest.java:29-50; UploadTask.java:540-567
- iOS: `PUT <url>`، و`Command: cancel`، جسم فارغ، بلا Auth. `[V]` GTM 1257-1282,1872-1879
- الإيقاف في المنصات الثلاث يقطع الطلب الجاري، والاستئناف يبدأ بـ query. `[V]` (أعلاه)
- Android يكشف رابط الجلسة للتطبيق "valid for approximately one week" لاستئناف الرفع لاحقًا. `[V]` UploadTask.java:609-617
  - فعلى جلسات Aruma أن تعيش أيامًا لا دقائق. `[I]`

## 4. مورد الـ metadata (ما يحلّله كل عميل)

| الحقل | JS | Android | iOS | التوصية |
|---|---|---|---|---|
| `bucket` | كما هو. `[V]` metadata.ts:70 | `optString`. `[V]` StorageMetadata.java:341 | `as? String`، وإلا `""`. **ويُستعمل في `getDownloadURL`.** `[V]` swift:158; GetDownloadURLTask.swift:57 | string |
| `name` | يصير `fullPath`، و`name` = آخر مقطع. `[V]` metadata.ts:73-83 | `mPath`. `[V]` java:340 | `path`، و`name` = آخر مقطع. `[V]` swift:173-176 | string، المسار الكامل |
| `generation`, `metageneration` | كما هو (النوع string). `[V]` metadata.ts:71-72 | `optString` (يحوّل الرقم). `[V]` java:339,342 | `Int` أو `String` رقمي إلى `Int64`. غير ذلك = 0. `[V]` swift:166-167,221-226 | string رقمي (أسلوب GCS) |
| `size` | `Number(size)`. `[V]` metadata.ts:88-100 | `optLong` (يقبل نصًا أو رقمًا). `[V]` java:345 | `Int` أو `String`. `[V]` swift:165 | string رقمي (كما في المحاكي `FT metadata.ts:341`) |
| `timeCreated`, `updated` | نص خام. `[V]` metadata.ts:101-102 | `yyyy-MM-dd'T'HH:mm:ss.SSSZ` بعد تحويل `Z` إلى `-0000`. الفشل = 0. `[V]` Util.java:48,52-67 | `yyyy'-'MM'-'dd'T'HH':'mm':'ss.SSSZZZZZ`. الفشل = `nil`. `[V]` swift:228-241 | **`2006-01-02T15:04:05.000Z` بالضبط**: 3 خانات، ولاحقة `Z` |
| `md5Hash` | كما هو. `[V]` | `optString`. `[V]` java:346 | `as? String`. `[V]` swift:170 | base64، دون تحقق في العميل |
| `contentType`, `cacheControl`, `contentDisposition`, `contentEncoding`, `contentLanguage` | كما هي. `[V]` metadata.ts:104-108 | نص غير `null`. `[V]` java:356-371 | `as? String`. `[V]` swift:159-163 | string. وغيابها مقبول |
| `metadata` (مخصّصة) | يصير `customMetadata`. `[V]` metadata.ts:109 | `getJSONObject`، ثم `getString` لكل مفتاح. `[V]` java:348-354 | `as? [String: String]`: **قيمة واحدة غير نصية تُسقط الخريطة كلها**. `[V]` swift:164 | كائن قيمه نصوص فقط |
| `downloadTokens` | نص فيه فواصل، لـ `getDownloadURL` فقط. `[V]` metadata.ts:166-177 | `optString`. `[V]` GetDownloadUrlTask.java:59 | `as? String`. `[V]` GetDownloadURLTask.swift:51-53 | **string** مفصول بفواصل، لا مصفوفة |
| حقول أخرى (`etag`, `crc32c`, `storageClass`...) | تُتجاهل | تُتجاهل | تُحفظ في `initialMetadata`، ثم تُرسل `null` في PATCH (3.5) | ضعها إن شئت، وتجاهل `null` لها في PATCH |

مثال رد متوافق (على نسق المحاكي، `FT metadata.ts:312-352`):
```json
{"name":"images/a b.png","bucket":"demo.appspot.com","generation":"1729700000000000",
 "metageneration":"1","contentType":"image/png","timeCreated":"2026-10-23T10:00:00.123Z",
 "updated":"2026-10-23T10:00:00.123Z","size":"2048","md5Hash":"1B2M2Y8AsgTpgAmY7PhCfg==",
 "contentEncoding":"identity","contentDisposition":"inline","metadata":{"k":"v"},
 "downloadTokens":"0b7c...-uuid"}
```
- المحاكي يولّد رمز تنزيل UUID عند الرفع إن لم يوجد. `FT apis/firebase.ts:210-216`
- ويولّده كذلك عند أول GET إن كانت القائمة فارغة. `FT apis/firebase.ts:139-141`

## 5. الأخطاء وإعادة المحاولة

### 5.1 جسم الخطأ

لا مكتبة تحلّل جسم الخطأ لتحديد الرمز. التحديد بالحالة وحدها. `[V]`
- JS: `serverResponse` = `statusText` في المتصفح، و`''` في Node. `[V]` request.ts:168-176; browser/connection.ts:118-123; node/connection.ts:97-99
- Android: الجسم الخام في `IOException` كسبب. `[V]` NetworkRequest.java:414-416
- iOS: الجسم في `userInfo["ResponseBody"]`. `[V]` StorageError.swift:59-61; GTMF 160-174

الشكل الموصى به، كالمحاكي وGCS، للتشخيص فقط: `{"error":{"code":403,"message":"Permission denied."}}`. `FT apis/firebase.ts:104-109`

### 5.2 من الحالة إلى الرمز

| HTTP | JS (`storage/...`) | Android (`StorageException`) | iOS (`StorageErrorCode`) |
|---|---|---|---|
| 400 | `unknown` | `ERROR_UNKNOWN` | `unknown` ("Unknown 400 error from backend") |
| 401 | `unauthenticated`. أو `unauthorized-app` إن احتوى `statusText` على "Firebase App Check token is invalid" | `ERROR_NOT_AUTHENTICATED` | `unauthenticated` |
| 402 | `quota-exceeded` | `ERROR_UNKNOWN` | `quotaExceeded` |
| 403 | `unauthorized` | `ERROR_NOT_AUTHORIZED` | `unauthorized` |
| 404 | `object-not-found` لعمليات الكائن (metadata, bytes, URL, update, delete). **`unknown`** للقائمة والرفع والـ query | `ERROR_OBJECT_NOT_FOUND` لأي طلب | `objectNotFound` لأي طلب، ومنه جلسة رفع مفقودة |
| 409 | `unknown` | **`ERROR_INVALID_CHECKSUM`** | `unknown` |
| 412 | `unknown` | `ERROR_UNKNOWN` | `unknown` |
| 429 | يُعاد، ثم `retry-limit-exceeded` | يُعاد، ثم `ERROR_UNKNOWN` (httpResult=429) | **لا يُعاد**، و`unknown` |
| 500 | يُعاد، ثم `retry-limit-exceeded` | يُعاد، ثم `ERROR_UNKNOWN` | **لا يُعاد**، و`unknown` |
| 502/503/504 | يُعاد، ثم `retry-limit-exceeded` | يُعاد، ثم `ERROR_UNKNOWN` | يُعاد، ثم `unknown` ("Unexpected 503...") |
| شبكة | يُعاد، ثم `retry-limit-exceeded` | يُعاد، ثم `ERROR_RETRY_LIMIT_EXCEEDED` | timeout أو connection-lost أو offline: يُعاد |
| إلغاء | `canceled` | `ERROR_CANCELED` | `cancelled` |

المصادر:
- `[V]` JS requests.ts:100-153, request.ts:168-186, error.ts:92-124
- `[V]` AND StorageException.java:76-94; ExponentialBackoffSender.java:60-65
- `[V]` IOS StorageError.swift:49-92; GTMF 3618-3643; Internal/StorageFetcherService.swift:82-93

ملاحظات:
- كشف App Check في JS يعتمد عبارة السبب في HTTP/1.1. وهي فارغة في HTTP/2 وفي Node، فهو عمليًا لا يعمل إلا إن كتب الخادم عبارة سبب مخصّصة. `[I]`
- لا مكتبة تحوّل استجابة إلى `bucket-not-found` أو `project-not-found`، مع أن الرموز معرّفة. `[V]` (لا مسار يُنتجها)

### 5.3 جدول الإعادة

| | ما يُعاد | الجدول | الحد |
|---|---|---|---|
| JS (`backoff.ts`) | 5xx، و408، و429، وفشل الشبكة. `[V]` utils.ts:24-40; request.ts:127-140 | الطلب الأول فورًا، ثم الانتظار `(w + rand[0,1)) s` حيث `w` = 2، 4، 8، ...، ثم 64 ثابتًا. `[V]` backoff.ts:49,100-111,136 | مهلة كلية: 2 دقيقة (`maxOperationRetryTime`) لغير الرفع، و10 دقائق (`maxUploadRetryTime`) للرفع: start وquery وmultipart. `[V]` constants.ts:36,43; requests.ts:163,364,435,486 |
| JS: قطع الرفع | الطلب نفسه يُرسل بـ `retry=false`، فحالة 5xx أو 408 أو 429 تصعد إلى `UploadTask`. لكن فشل الشبكة يُعاد داخل `backoff` بالإزاحة نفسها. `[V]` task.ts:314-320; request.ts:129-139 | `sleepTime` = 1s، ثم ×2 (لا يُصفَّر بعد النجاح)، ثم query ثم استئناف. `[V]` task.ts:127-139,154-155,195-198 | `sleepTime > maxUploadRetryTime` (600s) يعطي `retry-limit-exceeded`. `[V]` task.ts:104-106,129-131 |
| Android (`ExponentialBackoffSender`) | 5xx، و408، و429، و-2 (شبكة/IOException). `[V]` ExponentialBackoffSender.java:60-65 | `(s + rand[0,250)) ms` حيث `s` = 1، 2، 4، 8، 16، 32 ثانية. يتوقف التضاعف بعد ≥30s. والشبكة الغائبة تعيد `s` إلى 1s. `[V]` java:37-39,85-106 | upload: 10 دقائق. download/getMetadata/delete/list: 10 دقائق. updateMetadata/getDownloadUrl: دقيقتان. `[V]` FirebaseStorage.java:55-58; GetMetadataTask.java:54; DeleteStorageTask.java:52; ListTask.java:56; UpdateMetadataTask.java:49; GetDownloadUrlTask.java:55 |
| Android: قطع الرفع | أي فشل: query مع إعادة، ثم إعادة القطعة. `[V]` UploadTask.java:340-352 | `sleepTime` 1s، ثم ×2، **ويُصفَّر بعد قطعة ناجحة**. `[V]` UploadTask.java:441-461 | يفشل حين يتجاوز `sleepTime` قيمة `maxChunkUploadRetry` (60s). `[V]` UploadTask.java:105,341-349; FirebaseStorage.java:56 |
| iOS (GTMSessionFetcher) | 408، و502، و503، و504، و`NSURLErrorTimedOut`، و`NetworkConnectionLost`، و`notConnectedToInternet` (من Firebase). **لا 500 ولا 429.** `[V]` GTMF 3618-3643; StorageFetcherService.swift:82-93 | الفاصل الأدنى عشوائي بين 1 و2 ثانية، ×2. و`Retry-After` يُحترم مع 503. `[V]` GTMF 149-151,3850-3867,3735-3754 | يتوقف حين يبلغ الفاصل التالي `maxRetryInterval`: 64s للعمليات (من 120s)، و512s للرفع والتنزيل (من 600s). `[V]` IOS Storage.swift:289-299,324-337; StorageFetcherService.swift:41; StorageUploadTask.swift:123 |
| iOS: قطع الرفع | الأخطاء القابلة للإعادة، أو 4xx عدا 404 مع `active` وطول > 0: query بدل الإعادة المباشرة. `[V]` GTM 1596-1621,1669-1687 | كما سبق | كما سبق |

تنبيه iOS: مع `Retry-After` يتخطى GTM فحص الحد الأقصى (`if (shouldDoRetry && ![self hasRetryAfterInterval])`). فـ 503 مع `Retry-After` دائمًا قد يعني إعادة بلا نهاية. `[V]` GTMF 3681-3700

### 5.4 أخطاء الرفع خاصة

- **جلسة مفقودة أو منتهية (4xx على رابط الجلسة):** لا مكتبة تعيد بدء جلسة جديدة تلقائيًا.
  - JS: 4xx في القطعة يعطي `ERROR` فورًا. 404 هنا = `unknown`. `[V]` task.ts:127-143; requests.ts:585
  - Android: query، ثم فشل.
  - iOS: 404 فشل مباشر، يظهر **`objectNotFound`**. وباقي 4xx مع `active` يعطي query.
- **`final` مع حالة غير 2xx** (مثل رفض القواعد عند الإنهاء):
  - JS: يتحدد الرمز بالحالة وحدها (403 = `unauthorized`)، ولا تُقرأ الترويسة. وإن كانت الحالة 5xx: query، ثم `final`، ثم GET metadata، وقد ينجح.
  - Android: `mServerStatus = final` يعطي `FAILURE`، والرمز من `mResultCode`: 403 = `NOT_AUTHORIZED`، و5xx = `RETRY_LIMIT_EXCEEDED` لأنها حُوّلت إلى -2. `[V]` UploadTask.java:356-366,531-537,571-577
  - iOS: لا query لأن الحالة ليست `active`، فيفشل بالحالة: 403 = `unauthorized`. `[V]` GTM 1675-1691
  - المحاكي يفعل هذا: 403 + `x-goog-upload-status: final` + JSON. `FT apis/firebase.ts:221-229`

## 6. قيود أخرى على الخادم

1. **CORS (الويب):** `getResponseHeader` يعيد `null` لأي ترويسة غير مكشوفة في الطلبات عبر الأصل. `[I]` (سلوك المتصفح)
   - **اكشف:** `X-Goog-Upload-URL` و`X-Goog-Upload-Status` و`X-Goog-Upload-Size-Received`. JS يحتاجها. `[V]` requests.ts:406,441,471
   - **واسمح بالترويسات:** `Authorization` و`X-Firebase-Storage-Version` و`X-Firebase-AppCheck` و`X-Firebase-GMPID` و`X-Goog-Upload-Protocol` و`X-Goog-Upload-Command` و`X-Goog-Upload-Header-Content-Length` و`X-Goog-Upload-Header-Content-Type` و`X-Goog-Upload-Offset` و`Content-Type` و`Range`.
   - **والطرق:** GET وPOST وPATCH وDELETE.
   - المحاكي: يعكس الأصل وكل الترويسات، ويكشف قائمة مشابهة. `FT server.ts:39-59`
2. **حالة أحرف الترويسات:**
   - XHR و`fetch` لا يفرّقان بين الحالات. `[I]`
   - Android يقرأ من `getHeaderFields()`. `[V]` NetworkRequest.java:489-499
   - iOS يقرأ من `allHeaderFields` عبر `objectForKey:`. `[V]` GTMF 2204-2220; GTM 843
   - كلاهما لا يفرّق عمليًا، لأن إنتاج Google على HTTP/2 بأسماء صغيرة. `[I]`
   - ومع ذلك: أرسل الأسماء بالضبط، `X-Goog-Upload-URL` لا `X-Goog-Upload-Url` الذي يُنتجه `Header().Set` في Go. والقيم حساسة للحالة: `active` و`final` و`cancelled`. `[V]`
3. **جسم القطعة خام مهما كان `Content-Type`.**
   - JS قد يرسل نوع الـ Blob، أو لا شيء. `[I]`
   - Android لا يضبطه، وقد يضيف `HttpURLConnection` قيمة افتراضية. `[I]`
   - المحاكي يقرأ `application/x-www-form-urlencoded` خامًا تحسبًا لذلك. `FT server.ts:61-62`
4. **ثبّت القطعة كاملة أو لا شيء.** JS يعيد إرسال الطلب نفسه بالإزاحة نفسها بعد فشل الشبكة. فإن ثبّت الخادم جزءًا من جسم مبتور، صار الطلب المعاد غير متسق. `[V]` request.ts:129-139 + `[I]` (الأثر)
5. **iOS والـ granularity:** إن أرسل الخادم `X-Goog-Upload-Chunk-Granularity`، قرّب iOS إزاحة الـ query إلى أسفل، فقد يعيد إرسال بايتات مستلمة. `[V]` GTM 1946-1948. الأسلم ألا تُرسل.
6. **الرفع متعدد الأجزاء:** يجب أن يقبل المحلل غياب `CRLF` بعد `--B--`. `[V]` requests.ts:356
7. **لا تعتمد على `uploadType`، ولا على ترتيب معاملات الاستعلام.** Android يبنيها من `HashMap`. `[V]` ResumableUploadStartRequest.java:67-72
8. **اسم الكائن في البدء:**
   - JS: `?name=` وفي الجسم.
   - Android: `?name=` وحده.
   - iOS: `?name=` وفي المسار وفي الجسم.
   - المصدر الموحَّد هو `?name=`، ثم المسار احتياطًا (كما في المحاكي `FT apis/firebase.ts:203-205`). `[V]`
9. **`X-Goog-Upload-Offset`:**
   - JS وAndroid يرسلان ما يظنانه مستلمًا.
   - iOS يرسل الإزاحة من `Size-Received`، أو من حسابه الخاص.
   - المحاكي يتجاهل الإزاحة كليًا (`continueResumableUpload(uploadId, body)`). `FT apis/firebase.ts:309-325`

## 7. ما يجب أن يفعله خادم Aruma

**التوجيه والعناوين**
- [ ] خدمة `/v0/b/{bucket}/o` و`/v0/b/{bucket}/o/{object}`.
  - `{object}` مقطع واحد مرمَّز بـ `%2F`.
  - يُفك مرة واحدة من المسار الخام (في Go: `EscapedPath()`، لا `URL.Path`).
- [ ] قبول `POST` **و`PUT`** على `/o` وعلى `/o/{object}` للرفع.
- [ ] `PUT` مع `X-HTTP-Method-Override: PATCH` = تحديث metadata.
- [ ] `PATCH /o/{object}` = تحديث metadata.
- [ ] `GET /o/{object}` = metadata. و`?alt=media` = البيانات. `DELETE /o/{object}`. `GET /o` = قائمة.
- [ ] رابط الجلسة في `X-Goog-Upload-URL` **مطلق**، بالمخطط والمضيف والمنفذ كما رآها العميل، ويقبل `POST` و`PUT`.
- [ ] HTTPS للإنتاج. iOS يرفض إرسال الرموز على http لغير loopback.

**المصادقة**
- [ ] `Authorization: Firebase <idToken>`. غيابه = مستخدم مجهول (`request.auth == null` في القواعد)، لا خطأ.
- [ ] `X-Firebase-AppCheck` و`X-Firebase-GMPID` اختياريان. لا تشترطهما. JS لا يرسل GMPID أبدًا.
- [ ] رابط الجلسة صلاحية بذاته: معرّف عشوائي غير قابل للتخمين.
  - قبول القطع والـ query والـ cancel **دون مصادقة** (iOS).
  - ربط هوية الرافع المحققة عند `start` بالجلسة.
  - تقييم القواعد عند الإنهاء بالهوية المحفوظة، دون إعادة فحص انتهاء صلاحية الرمز. (ملاحظة المنسّق: التقييم عند `start` أيضًا لم يثبت في الإنتاج، وAndroid لا يعلن الحجم عند `start`؛ انظر E §3.2.)

**الرفع**
- [ ] تحديد النوع بـ `X-Goog-Upload-Protocol` (`multipart` أو `resumable`) أو بوجود `X-Goog-Upload-Command`. معامل `uploadType` يُتجاهل.
- [ ] multipart: `multipart/related`، والجزء الأول JSON للـ metadata، والثاني البايتات. والرد 200 + metadata.
- [ ] `start`:
  - قبول جسم فارغ أو JSON جزئي.
  - تجاهل `bucket` و`size` و`generation` وأمثالها.
  - الاسم من `?name=`، ثم من المسار.
  - النوع من `contentType` في الجسم، ثم `X-Goog-Upload-Header-Content-Type` أو `X-Goog-Upload-Content-Type`، ثم `application/octet-stream`.
  - الحجم الكلي **اختياري**: من `X-Goog-Upload-Header-Content-Length` أو `X-Goog-Upload-Content-Length`، أو لا شيء.
- [ ] رد `start`: **200**، وجسم فارغ، و`X-Goog-Upload-Status: active`، و`X-Goog-Upload-URL`. بلا `X-Goog-Upload-Chunk-Granularity`.
- [ ] القطع:
  - تحليل `X-Goog-Upload-Command` قائمة مفصولة بفاصلة مع مسافات (`upload, finalize`).
  - قبول `finalize` منفردًا بجسم فارغ.
  - الجسم خام مهما كان `Content-Type`.
  - التثبيت ذري: القطعة تُقبل كاملة بطول `Content-Length` أو تُرفض كلها.
- [ ] القطعة الوسيطة: **200** (لا 308 أبدًا)، و`X-Goog-Upload-Status: active`، و`X-Goog-Upload-Size-Received: <n>` صحيحًا، وجسم فارغ.
- [ ] الإنهاء: **200**، و`X-Goog-Upload-Status: final`، والجسم metadata JSON كاملة، مع `Content-Type: application/json`.
- [ ] `query`: **200**، و`X-Goog-Upload-Status`، و`X-Goog-Upload-Size-Received` دائمًا. وإن كانت الجلسة `final` فالجسم هو الـ metadata (لأجل iOS).
- [ ] `cancel`: 200. والجلسة تصير `cancelled`، وكل طلب لاحق عليها 4xx مع `X-Goog-Upload-Status: cancelled`.
- [ ] رفض القواعد أو خطأ نهائي أثناء الجلسة: 4xx مع `X-Goog-Upload-Status: final`، حتى لا يدخل iOS في حلقة query.
- [ ] إزاحة لا تطابق المستلَم: 4xx مع `X-Goog-Upload-Status: active`. iOS سيرسل query، وAndroid يُصلح نفسه بالـ query، أما JS فيفشل. `[I]`
  - بديل أكثر تسامحًا: إن كانت القطعة مكررة بالكامل (`offset + len ≤ received`)، أجب 200 `active` بالحجم الحالي. `[I]`
- [ ] الجلسات تبقى أيامًا (Android يعرض رابطها للاستئناف لاحقًا).
- [ ] توليد `downloadTokens` (UUID) عند إنشاء الكائن إن لم يوجد.

**الـ metadata**
- [ ] الحقول:
  - `name`، و`bucket`.
  - `generation` و`metageneration` و`size`: نصوص رقمية.
  - `timeCreated` و`updated` بصيغة `YYYY-MM-DDTHH:MM:SS.mmmZ`: ثلاث خانات بالضبط، ولاحقة `Z`.
  - `md5Hash` (base64).
  - `contentType` و`cacheControl` و`contentDisposition` و`contentEncoding` و`contentLanguage`.
  - `metadata`: كائن قيمه نصوص فقط.
  - `downloadTokens`: نص مفصول بفواصل.
- [ ] PATCH:
  - الحقول القابلة للتعديل: `contentType` و`cacheControl` و`contentDisposition` و`contentEncoding` و`contentLanguage` و`metadata`.
  - `null` على حقل منها = حذفه.
  - `metadata` تُدمج مفتاحًا مفتاحًا، و`null` على مفتاح يحذفه.
  - **كل ما سواها يُتجاهل حتى لو كان `null`.** ويشمل: `name`، و`bucket` (iOS يرسل `""`)، و`size`، و`generation`، و`metageneration`، و`timeCreated`، و`updated`، و`md5Hash`، و`etag`، و`crc32c`، و**`downloadTokens`**.
  - الرد: الـ metadata كاملة.
  - أثر `"metadata": null` كليًا يحتاج قرارًا (القسم 8).

**التنزيل**
- [ ] `alt=media` مع `Authorization`، أو مع `?token=<أحد downloadTokens>` دون مصادقة.
- [ ] `Content-Length` صحيح دائمًا، و`ETag` ثابت لمحتوى الكائن.
- [ ] `Range: bytes=a-b` و`bytes=a-`: **206** مع `Content-Range`.
  - تجاهل `Range` وإعادة 200 يُفسد استئناف Android.

**القائمة**
- [ ] `prefix` فارغ أو غائب = الجذر. و`delimiter=/`.
- [ ] `maxResults` بين 1 و1000، وافتراضيًا 1000. `[I]`
- [ ] الرد `{"prefixes":["a/b/"],"items":[{"name":"a/x","bucket":"b"}],"nextPageToken":"..."}`.
  - العناصر بحقول نصية فقط.
  - **احذف `nextPageToken` في آخر صفحة.** لا تُرسله فارغًا.

**الحذف**
- [ ] 204 أو 200 بجسم فارغ.

**الأخطاء**
- [ ] 401 للرمز غير الصالح. 403 لرفض القواعد. 404 للكائن غير الموجود. 402 لتجاوز الحصة (إن طُبّقت).
- [ ] الجسم `{"error":{"code":N,"message":"..."}}`.
- [ ] تجنّب 409، لأن Android يعرضه `invalid-checksum`.
- [ ] الضغط المؤقت: 503 مع `Retry-After` معقول ومحدود، فـ iOS لا يعيد على 429 ولا على 500.
- [ ] لا 308 في أي رد.

**الويب (CORS)**
- [ ] عكس الأصل، والسماح بالترويسات المذكورة في 6.1 وبطرق GET وPOST وPUT وPATCH وDELETE.
- [ ] كشف `X-Goog-Upload-URL` و`X-Goog-Upload-Status` و`X-Goog-Upload-Size-Received` (و`X-Goog-Upload-Chunk-Granularity` و`Content-Range` و`ETag` احتياطًا).

**الترويسات**
- [ ] أسماء ترويسات الرد بالحالة الدقيقة (`X-Goog-Upload-URL`)، بإسناد مباشر إلى الخريطة في Go بدل `Header().Set`.

## 8. ما بقي غير مؤكد (يحتاج تجربة)

1. **`"metadata": null` من iOS** حين لا توجد metadata مخصّصة: هل يرسله فعلًا؟ (جسر `Optional.none` في `AnyHashable`) `[I]`
   - إن أرسله، فتطبيق دلالة GCS ("null يمسح الكل") يمسح metadata المستخدم عند أي `updateMetadata` بكائن جديد.
   - يُحسم بالتقاط طلب حقيقي من تطبيق iOS نحو المحاكي.
2. **حساسية أسماء الترويسات** في `HttpURLConnection` على Android وفي `allHeaderFields` على iOS. `[I]` والتوصية تتجنب المسألة.
3. **رد إنتاج Google على إزاحة خاطئة أو قطعة مكررة.** لم يُقرأ في أي مصدر. المحاكي يتجاهل الإزاحة.
4. **حجم الصفحة الافتراضي** للقائمة في الإنتاج: 1000 مأخوذ من تعليق في JS فقط.
5. **كشف App Check في JS عبر `statusText`:** لا يعمل عمليًا على HTTP/2. هل نكتب عبارة سبب مخصّصة على HTTP/1.1؟ أولوية منخفضة.
6. **ترميز `/` في `Uri.Builder.appendPath` على Android:** مستنتج من سلوك المنصة، لا من كود مقروء هنا. والمحاكي يعمل معه، وهذا يرجّحه.
