# D: كيف ينفّذ محاكي Storage في firebase-tools البروتوكول

هذه الوثيقة للأفكار فقط. نكتب شيفرتنا بـ Go ولا ننقل الشيفرة. الهدف أن نعرف ما يقبله المحاكي وما يرده، وأين يختلف عن الإنتاج، حتى نقارن ذلك بتسجيلات الصندوق الأسود.

## المصادر

| المستودع | الإصدار | الـ commit | الرخصة | الاختصار في الاستشهاد |
|---|---|---|---|---|
| firebase-tools | 15.34.0 | `4e6eb923dd5e4a081eb227baaee58df85c17d044` | MIT | `FT src/...:سطر` |
| cloud-storage-rules-runtime (JAR مغلق المصدر، يُنزَّل عند التشغيل) | v1.1.3 | — | Google ToS (لم يُقرأ) | `FT src/emulator/downloadableEmulatorInfo.json:18-24` |

- المسار المحلي للقراءة فقط: `/Volumes/Work/aurma-project/.cache/research/src/firebase-tools`.
- **VERIFIED** تعني أن الادعاء مقروء في الشيفرة عند السطر المذكور.
- **INFERENCE** تعني استنتاجًا يحتاج إلى تأكيد بالتسجيل.
- الملف `README.md` (`FT src/emulator/storage/README.md:1-52`) يشرح التطوير والاختبارات فقط، ولا يعترف بأي انحراف.
- اختبارات المطابقة في `FT scripts/storage-emulator-integration/conformance/*.test.ts` يمكن تشغيلها على الإنتاج (`useProductionServers`، `env.ts:15-17`).
  - الحالات الموسومة `emulatorOnly` اعتراف ضمني بسلوك يخص المحاكي وحده.

## 0. البنية

VERIFIED: `FT src/emulator/storage/index.ts:39-157`، `FT src/emulator/storage/server.ts:16-170`.

- تطبيق Express واحد على المنفذ 9199 افتراضيًا (`FT src/emulator/constants.ts:15`). فيه راوتران:
  - `app.use("/v0", firebase)`.
  - `app.use("/", gcloud)`.
  - طلب لم يطابقه راوتر Firebase يسقط إلى راوتر GCS، فينتهي غالبًا بـ 501.
- **طبقتا تخزين تشتركان في نفس الخرائط:**
  - `storageLayer` تتحقق بالقواعد، ويستعملها مسار `/v0` (Firebase).
  - `adminStorageLayer` تقبل كل شيء، ويستعملها مسار GCS. VERIFIED: `index.ts:62-76`، `apis/gcloud.ts:25-26`، `rules/utils.ts:67-85`.
- `UploadService` يحفظ حالة الرفع في `Map` بالذاكرة. VERIFIED: `upload.ts:86-95`.
- `Persistence` تكتب البايتات على القرص في `${os.tmpdir()}/firebase/storage/blobs`. VERIFIED: `index.ts:155-157`، `persistence.ts:12-97`.
- `StorageRulesRuntime` عملية Java فرعية: `java -Djava.security.manager=disallow -Duser.language=en -jar cloud-storage-rules-runtime-v1.1.3.jar serve`. VERIFIED: `FT src/emulator/downloadableEmulators.ts:258-274`.
- `StorageCloudFunctions` يرسل أحداثًا إلى محاكي Functions إن كان يعمل (القسم 4.5).

## 1. جدول المسارات

### 1.1 Middleware عامة

VERIFIED: `server.ts:28-62`.

- إن وُجدت `access-control-request-private-network` يُرد `access-control-allow-private-network: true` (issue #4227).
- CORS: `cors({origin: true})`. الأصل يُعكس، والترويسات تُعكس، والطرق هي الافتراضية.
  - `exposedHeaders`: `content-type` و`x-firebase-storage-version` و`X-Goog-Upload-Size-Received` و`x-goog-upload-url` و`x-goog-upload-command` و`x-gupload-uploadid` و`x-goog-upload-header-content-length` و`x-goog-upload-header-content-type` و`x-goog-upload-protocol` و`x-goog-upload-status` و`x-goog-upload-chunk-granularity` و`x-goog-upload-control-url`.
- `bodyParser.raw` بحد **130mb** للأنواع `application/x-www-form-urlencoded` و`multipart/related` على مستوى التطبيق.
  - بقية المحللات في الراوترات بنفس الحد. الحد الأقصى للرفع إذن 130MB في كل الأحوال.

### 1.2 مسارات خاصة بالمحاكي (بلا مصادقة)

| الطريقة والمسار | المدخل | الرد | المصدر |
|---|---|---|---|
| `POST /internal/export` | JSON `{path, initiatedBy?}` | 200 ("OK")، أو 400 نصًا: "Export request body must include 'path'." | `server.ts:64-74` |
| `PUT /internal/setRules` | `{rules:{files:[{name,content,resource?}]}}`. ملف واحد يعني قواعد لكل الـ buckets، وأكثر من ملف يستلزم `resource` لكل ملف | 200 `{"message":"Rules updated successfully"}`. 400 `{"message":"Request body must include 'rules.files' array"}` أو رسالة الحقول الناقصة. 400 `{"message":"There was an error updating rules, see logs for more details"}` عند خطأ ترجمة | `server.ts:103-159` |
| `POST /internal/reset` | — | 200. يمسح الملفات والـ buckets والرفوع الجارية | `server.ts:161-164`، `index.ts:99-104` |
| `GET /v0/` | — | `{"emulator":"storage"}` | `apis/firebase.ts:540-542` |

- يستدعي Hub التصدير كالتالي: `POST /internal/export` بالترويسة `Content-Type: application/json` والجسم `{path:"<tmp>/storage_export", initiatedBy}`. VERIFIED: `FT src/emulator/hubExport.ts:135-140,319-345`.

### 1.3 سطح Firebase (`/v0`)، ملف `apis/firebase.ts`

**middleware المسارات التي فيها bucket** (`:96-112`). كل مسار يطابق `/.*\/b\/(.+?)\/.*/`:

- يُنشئ الـ bucket تلقائيًا.
- إن لم يوجد ruleset محمّل لذلك الـ bucket يرد **403** بالجسم `{"error":{"code":403,"message":"Permission denied. Storage Emulator has no loaded ruleset."}}`.

| الطريقة والمسار | المعاملات والترويسات المقروءة | الحالات والردود | المصدر |
|---|---|---|---|
| `GET /b/:bucket/o/:object` | `object` مرمّز (`%2F`) وتطبق عليه `decodeURIComponent`. `?token=` و`?alt=media` و`Authorization` و`Range` و`Accept-Encoding` | 200 بالبيانات الوصفية (`OutgoingFirebaseMetadata`)، أو البايتات مع `alt=media`. 206 للنطاق. 404 بـ `sendStatus` ونصه "Not Found". 403 `{"error":{"code":403,"message":"Permission denied. No READ permission."}}` | `:114-150` |
| `GET /b/:bucket/o` | `prefix` (يجب أن يكون فارغًا أو ينتهي بـ `/`) و`delimiter` و`pageToken` و`maxResults` و`Authorization` | 200 `{nextPageToken?, prefixes:[], items:[{name,bucket}]}`. 400 `{"error":{"code":400,"message":"The prefix parameter is required to be empty or ends with a single / character."}}`. 403 "...No LIST permission." | `:153-199` |
| `POST` أو `PUT` على `/b/:bucket/o/:object?` | الاسم من المسار أو من `?name=`. `X-Goog-Upload-Protocol` (`resumable` أو `multipart`، وغيرهما media). `X-Goog-Upload-Command` (`start`/`query`/`cancel`/`upload`/`finalize` و`upload, finalize`). `?upload_id=` و`Content-Type` و`Authorization` | القسم 2 | `:201-396,497-514` |
| `POST ...:object?create_token=true` | `Authorization` يجب أن يكون admin | 200 بالبيانات الوصفية. 400 إن كانت القيمة غير `true`. 403 `{"error":{"code":403,"message":"Missing admin credentials."}}`. 404 | `:398-456` |
| `POST ...:object?delete_token=<t>` | admin | مثل `create_token` | `:430-456` |
| `PATCH /b/:bucket/o/:object` | JSON من `IncomingMetadata`، و`Authorization` | 200 بالبيانات الوصفية، مع `Content-Disposition`/`Content-Encoding`/`Cache-Control`/`Content-Language`. 403 "...No WRITE permission.". 404 | `:465-496,547-560` |
| `PUT` مع `X-HTTP-Method-Override: patch` | — | مثل `PATCH` | `:29-33,497-508` |
| `DELETE /b/:bucket/o/:object` | `Authorization` | 204. 403 "...No WRITE permission.". 404 | `:516-538` |

### 1.4 سطح GCS (`/`)، ملف `apis/gcloud.ts`

**middleware المسارات التي فيها bucket** (`:84-88`): تُنشئ الـ bucket تلقائيًا ولا تتحقق من شيء.

| الطريقة والمسار | ما يُقرأ | الرد | المصدر |
|---|---|---|---|
| `GET /b` (بلا `/storage/v1`) | — | `{"kind":"storage#buckets","items":[CloudStorageBucketMetadata]}` | `:90-95` |
| `GET` على `/b/:b/o/:o` و`/download/storage/v1/b/:b/o/:o` و`/storage/v1/b/:b/o/:o` | `alt=media` و`Range` و`Accept-Encoding`. **`generation` يُتجاهل** | 200 بـ `CloudStorageObjectMetadata` أو البايتات. 404 بالشكل في القسم 5.3 | `:97-125` |
| `PATCH /b/:b/o/:o` فقط (بلا `/storage/v1`) | JSON | 200 بالبيانات الوصفية. 404 | `:127-145` |
| `GET` على `/b/:b/o` و`/storage/v1/b/:b/o` | `prefix` (أي نص) و`delimiter` و`pageToken` و`maxResults` | 200 `{"kind":"storage#objects","nextPageToken"?,"prefixes"?,"items"?:[...]}` | `:147-171` |
| `DELETE` على `/b/:b/o/:o` و`/storage/v1/b/:b/o/:o` | — | 204. 404 | `:173-192` |
| `POST /upload/storage/v1/b/:b/o` | `?uploadType=` أو الترويسة `X-Goog-Upload-Protocol`. `?name=` و`X-Upload-Content-Type` و`Content-Type` | القسم 2.2 | `:274-372` |
| `PUT /upload/storage/v1/b/:b/o?upload_id=` | الجسم كاملًا | 200 بالبيانات الوصفية. 400 و404 | `:194-228` |
| `POST /b/:b/o/:o/acl` | JSON `{entity, role}` | 200 `storage#objectAccessControl` شكلي (`etag:"someEtag"`). **لا أثر له** ("Cloud Storage ACLs are not supported"). يرفع `metageneration` | `:230-272` |
| `POST /b/:b/o/:o/(copyTo\|rewriteTo)/b/:db/o/:do` | JSON بيانات وصفية للهدف. `rewriteToken` غير مدعوم | 200: الكائن في `copyTo`. `{"kind":"storage#rewriteResponse","totalBytesRewritten","objectSize","done":true,"resource"}` في `rewriteTo` | `:464-514` |
| `GET /:bucket/:object(**)` | بأسلوب XML API | البايتات فقط | `:374-391` |
| `POST /:bucket` | `multipart/form-data` (POST policy): الحقل `key` والملف و`x-goog-meta-*` و`content-type`/`cache-control`/`content-disposition`/`content-encoding`/`content-language` | 204. 400 "Content-Type must be multipart/form-data" أو "Missing 'key' or file.". **التوقيع والسياسة لا يُتحقق منهما** | `:393-462` |
| أي شيء آخر (`/**`) | — | 501 بنص "Not Implemented"، أو JSON `"endpoint not implemented"` مع `STORAGE_EMULATOR_DEBUG` | `:516-524` |

ملاحظات على المسارات:

- المسارات بصيغة `/b/...` بلا `/storage/v1` مفتوحة لأن مكتبات GCS القديمة كانت تأخذ `STORAGE_EMULATOR_HOST` كـ baseUrl كاملًا. INFERENCE من التسجيل المزدوج، ومن تعليق `FT src/emulator/constants.ts:91-94`.
- **`PATCH` و`acl` و`copyTo`/`rewriteTo` غير مسجلة تحت `/storage/v1/`**، فتعطي 501 إن طُلبت بالبادئة. VERIFIED بالغياب في الجدول أعلاه. أثر ذلك على `@google-cloud/storage` الحديث غير مؤكد.
- `GET /storage/v1/b/:bucket` (bucket get)، والإدراج غير المرفوع، و`compose`، وIAM، وnotifications، وقوائم ACL: كلها 501. VERIFIED بالغياب.

## 2. آلة حالات الرفع

### 2.1 الحالة المشتركة

VERIFIED: `upload.ts:7-33,86-251`.

- السجل `Upload{id(UUID), bucketId, objectId, type(MEDIA|MULTIPART|RESUMABLE), path, status, metadata?, size, authorization?, prevResponseCode?}`.
- الحالات: `active` و`cancelled` و`final`. الرفع الواحد (media وmultipart) يولد بحالة `final`.
- ملف التدريج اسمه المنطقي `encodeURIComponent("{uploadId}_b_{bucket}_o_{object}")`.
  - تربطه `Persistence` باسم UUID عشوائي على القرص، ويُلحق إليه بـ `appendFileSync` (`upload.ts:249-251`، `persistence.ts:32-40`).
- **التحقق من الحجم أو الإزاحة غير موجود.**
  - `X-Goog-Upload-Offset` و`X-Goog-Upload-Header-Content-Length` و`X-Upload-Content-Length` و`Content-Range` لا تُقرأ أبدًا.
  - كل جزء يُلحق كما هو، و`size += byteLength`.
  - VERIFIED: `upload.ts:177-185`، وغياب هذه الترويسات في `apis/*.ts`.
- عند الإنهاء تجري `StorageLayer.uploadObject` (`files.ts:290-342`):
  1. تقرأ البايتات من التدريج، وتبني `StoredFileMetadata` (فيه md5 وcrc32c والحجم).
  2. تقيّم القواعد بالعملية **CREATE دائمًا**، حتى لو كان الكائن موجودًا، مع `before` و`after`.
  3. عند الرفض يُحذف ملف التدريج ويُرمى `ForbiddenError`.
  4. عند القبول يُعاد تسمية التدريج إلى المفتاح `path.join(bucket, object)` ويُرسل حدث `finalize`.
- سجلات `_uploads` لا تُحذف أبدًا إلا بـ `reset`. VERIFIED: `upload.ts:92-95`.

### 2.2 بروتوكول Firebase

VERIFIED: `apis/firebase.ts:201-396`.

| الخطوة | الطلب | الرد |
|---|---|---|
| media | `POST /v0/b/{b}/o?name=x` بجسم خام. `Content-Type` يصبح `contentType` | 200 `OutgoingFirebaseMetadata` |
| multipart | `X-Goog-Upload-Protocol: multipart` و`Content-Type: multipart/related; boundary=...` بجزأين بالضبط: JSON ثم البيانات | 200. 400 بنص `Bad content type. ...` أو `Unexpected number of parts in request body` (`:357-385`، `multipart.ts:139-158`) |
| start | `X-Goog-Upload-Command: start` (مع `X-Goog-Upload-Protocol: resumable` أو بدونه) وجسم JSON بالبيانات الوصفية. يُحفظ `Authorization` | 200 "OK" بترويسات: `x-goog-upload-chunk-granularity: 10000` و`x-goog-upload-control-url: ""` و`x-goog-upload-status: active` و`x-gupload-uploadid` و`x-goog-upload-url: {proto}://{Host}/v0/b/{b}/o?name=..&upload_id=..&upload_protocol=resumable`. 400 بلا اسم (`:248-273`) |
| query | `X-Goog-Upload-Command: query` و`?upload_id` | 200 مع `X-Goog-Upload-Size-Received` و`x-goog-upload-status` (`active`/`cancelled`/`final`). 404 (`:280-293`) |
| cancel | `cancel` | 200، حتى لو كان ملغى مسبقًا. 400 إن كان `final`. 404 (`:295-307`) |
| upload | أمر يحتوي `upload` بلا `finalize` | 200 "OK" مع `x-goog-upload-status: active` و`x-gupload-uploadid`. 400 إن لم يكن `active` (ملغى أو منتهٍ). 404 (`:309-325`) |
| finalize | أمر يحتوي `finalize`، ومنه `upload, finalize` | 200 بالبيانات الوصفية مع `x-goog-upload-status: final`. 403 بالجسم `{"error":{"code":403,"message":"Permission denied. No WRITE permission."}}` و`x-goog-upload-status: final`. 400 للملغى. 404 (`:329-348`) |
| finalize مكرر | — | `x-goog-upload-status: final` و`sendStatus(prevResponseCode)`. **بعد نجاح سابق يُرد 200 "OK" بلا بيانات وصفية**، وبعد 403 يُرد 403 مجددًا (`:340-343`، `upload.ts:234-247`) |

- **القواعد تُقيَّم عند الإنهاء فقط، بترويسة `Authorization` الملتقطة في `start`.** الأوامر اللاحقة لا تحتاج مصادقة.
  - اختبارات المطابقة غير الموسومة `emulatorOnly` تؤكد أن هذا سلوك الإنتاج أيضًا. VERIFIED: `conformance/firebase.endpoints.test.ts:157-194,222-275`.
- `X-Goog-Upload-Command` بلا `start` وبلا `?upload_id` يعطي 400 (`:275-277`). وجود الترويسة وحدها يكفي لاعتبار الرفع resumable (`:241`).
- مع كل رفع Firebase ناجح:
  - **رمز تنزيل** `firebaseStorageDownloadTokens=randomUUID()` إن لم يُرسل العميل واحدًا (`:209-216`).
  - `contentDisposition = "inline"` إن غابت. **تُكتب في الكائن المخزَّن نفسه، لا في الرد فقط** (`:233-235`).

### 2.3 بروتوكول GCS

VERIFIED: `apis/gcloud.ts:28-35,194-228,274-372`.

| الخطوة | الطلب | الرد |
|---|---|---|
| media (الافتراضي) | `POST /upload/storage/v1/b/{b}/o?name=x[&uploadType=media]`، و`Content-Type` | 200 `CloudStorageObjectMetadata` |
| multipart | `?uploadType=multipart` و`Content-Type: multipart/related; boundary=..`. الاسم من `?name` أو من `metadata.name` بعد حذف `/` في أوله. يُدمج `{contentType: X-Upload-Content-Type, ...json}` | 200. 400 `{"error":{"code":400,"message":"Bad content type. ..."}}` |
| resumable: start | `?uploadType=resumable`، والاسم من `?name` أو من جسم JSON `name`، و`X-Upload-Content-Type` | 200 مع `Location: {proto}://{Host}/upload/storage/v1/b/{b}/o?name=..&uploadType=resumable&upload_id=..`. 400 بلا اسم |
| resumable: data | `PUT <Location>` بأي جسم | **يُلحق الجسم ويُنهى فورًا**، ثم 200 بالبيانات الوصفية. أي `PUT` لاحق يعطي 400 لأن الرفع لم يعد active. 404 |

- **`Content-Range` لا يُقرأ، و308 `Resume Incomplete` لا يُرسل أبدًا، واستعلام الحالة (`Content-Range: bytes */N`) غير مدعوم.**
  - المصدر يعترف: "GCS emulator resumable upload capabilities are limited and this test asserts its broken state"، ومعه "TODO: Current GCS upload implementation only supports a single `upload` step". VERIFIED: `conformance/gcs.endpoints.test.ts:199-235`.
- خلل في media بلا `name`: `sendStatus(400)` ثم يكمل التنفيذ بلا `return`، فيرمي `TypeError` بعد إرسال الرد. VERIFIED: `gcloud.ts:357-366`.
- رفع GCS **لا يُنشئ رمز تنزيل**، إلا إذا أرسل العميل `metadata.firebaseStorageDownloadTokens`. VERIFIED: غياب المنطق في `gcloud.ts` مقارنة بـ `firebase.ts:209-216`.
- بيانات multipart: كل جزء يجب أن يكون **ترويسة واحدة** (`Content-Type`) ثم سطرًا فارغًا ثم البيانات. ترويسة إضافية مثل `Content-Transfer-Encoding` تفسد التحليل. INFERENCE من `multipart.ts:107-121`.
  - `Content-Type` جزء البيانات يُتجاهل، والنوع يأتي من JSON.
  - علامات الاقتباس حول `boundary` تُزال (`:56`).

## 3. نموذج البيانات الوصفية

### 3.1 `StoredFileMetadata` (الحالة الداخلية، وهي أيضًا صيغة التصدير)

VERIFIED: `metadata.ts:16-95`.

| الحقل | النوع والتوليد |
|---|---|
| `name`, `bucket` | نص |
| `generation` | `number` = **`Date.now()` بالميلي ثانية**، عند كل رفع أو نسخ جديد (`:51`) |
| `metageneration` | يبدأ بـ 1، ويزيد في كل `update()`. هذا يشمل إنشاء رمز أو حذفه وطلب ACL (`:50,237`) |
| `contentType` | الافتراضي `application/octet-stream` (`:52`) |
| `storageClass` | `STANDARD` |
| `size` | `byteLength` |
| `md5Hash` | MD5 بترميز base64 (`:521-525`) |
| `crc32c` | **نص عشري** لعدد uint32، مثل `"1234567"` (`:84`). الحساب بجدول متعدد الحدود `0x82F63B78` (`crc.ts:5-44`) |
| `etag` | `sha1("{generation}/{metageneration}")` بترميز base64 بلا آخر حرف (`:527-532`). **يُحسب في الإنشاء فقط ولا يتغير مع `update()`** (`:60-64,192-243`) |
| `timeCreated`, `updated` | `Date`. `updated` يتغير مع كل `update()` |
| `contentEncoding`, `contentDisposition`, `contentLanguage`, `cacheControl`, `customTime?` | اختيارية. `customTime` لا يمكن ضبطه عبر `update()` |
| `downloadTokens` | `string[]` |
| `customMetadata?` | `{[k]: string}`. القيم غير النصية تُحوَّل بـ `JSON.stringify` في البناء (`:65-74`)، وبـ `String(v)` في `update()` (`:227`) |

دلالة `update(incoming)` (`:192-243`):

- الحقل `undefined` يعني تجاهله، والحقل `null` يعني حذفه. هذا يشمل `contentDisposition` و`contentType` و`contentLanguage` و`contentEncoding` و`cacheControl`.
- `metadata: null` يمحو الكل.
- `metadata: {k: null}` يحذف مفتاحًا، وبقية المفاتيح تُدمج.
- الحقول المجهولة تُتجاهل (`IncomingMetadata`، `:302-310`).

### 3.2 الصيغتان الخارجيتان

VERIFIED: `metadata.ts:312-352,402-498,506-519`.

| الحقل | Firebase (`OutgoingFirebaseMetadata`) | GCS (`CloudStorageObjectMetadata`) |
|---|---|---|
| `kind` | غائب | `"storage#object"` |
| `id` | غائب | `"{bucket}/{name}/{generation}"` |
| `selfLink` | غائب | `{registry host}/storage/v1/b/{b}/o/{enc(name)}` |
| `mediaLink` | غائب | `{registry host}/download/storage/v1/b/{b}/o/{enc}?generation=..&alt=media` |
| `generation`, `metageneration`, `size` | نصوص | نصوص |
| `crc32c` | **نص عشري** | **base64** لأربعة بايتات big-endian (`crc.ts:50-59`) |
| `md5Hash`, `etag` | base64 | base64 |
| `timeCreated`, `updated` | `YYYY-MM-DDTHH:mm:ss.sssZ` | نفس الصيغة، ويضاف `timeStorageClassUpdated = timeCreated` |
| `contentEncoding` | `"identity"` إن غاب | يغيب إن لم يُضبط |
| `contentDisposition`, `contentLanguage`, `cacheControl` | تُنسخ، فتغيب في JSON إن كانت `undefined` | تُنسخ إن وُجدت |
| `customTime` | غائب | إن وُجد |
| `downloadTokens` | **نص مفصول بفواصل**، وقد يكون `""` | غائب كحقل، ويُدمج في `metadata.firebaseStorageDownloadTokens` |
| `metadata` | `customMetadata` كما هي، بلا الرموز | `customMetadata` ومعها `firebaseStorageDownloadTokens`، ويُحذف الحقل إن كان فارغًا |

- البيانات الوصفية للـ bucket (`metadata.ts:354-385`) ثابتة:
  - `{kind:"storage#bucket", id, name, selfLink:"{host}/v1/b/{id}", projectNumber:"000000000000", metageneration:"1", location:"US", storageClass:"STANDARD", etag:"====", locationType:"multi-region", timeCreated, updated}`.
  - **`selfLink` بلا `/storage`.**

### 3.3 ترويسات التنزيل (`alt=media`)

VERIFIED: `apis/shared.ts:8-64`.

- `Accept-Ranges: bytes`.
- `Content-Type` = النوع أو `application/octet-stream`.
- `Content-Disposition: {contentDisposition||"attachment"}; filename*={RFC5987(basename)}`. **بلا البادئة `utf-8''`** (`rfc.ts:8-12`).
- `ETag` و`Cache-Control` (أو `""`) و`x-goog-generation`.
- **`x-goog-metadatageneration`**، لا `x-goog-metageneration`.
- `x-goog-storage-class` و`x-goog-hash: crc32c=<b64>,md5=<b64>`.
- gzip:
  - إن كان `contentEncoding=gzip` والعميل لا يقبل gzip، يُفك الضغط ويُرسل `Transfer-Encoding: chunked` ويُتجاهل `Range`.
  - وإلا يُرسل `Content-Encoding` كما هو.
- النطاق: **نطاق واحد فقط** (`req.range(..., {combine:true})[0]`)، فيرد 206 مع `Content-Range: bytes s-e/total`.
  - النطاق غير المرضي يُتجاهل ويُرد 200 كاملًا، بلا 416.

### 3.4 رموز التنزيل

| الحدث | السلوك | المصدر |
|---|---|---|
| رفع عبر `/v0` | يُنشأ UUID إن لم يُرسل `metadata.firebaseStorageDownloadTokens` | `apis/firebase.ts:209-216` |
| بناء البيانات الوصفية أو أي `update()` | إن وُجد `customMetadata.firebaseStorageDownloadTokens` يُقسم على `,` ويُضم **اتحادًا** إلى `downloadTokens`، ثم يُحذف من `customMetadata` | `metadata.ts:93-94,149-163,239` |
| `GET /v0/...` (بيانات وصفية أو بايتات) وقائمة الرموز فارغة | يُنشأ رمز تلقائيًا، فيزيد `metageneration` ويُرسل حدث `metadataUpdate` | `apis/firebase.ts:139-141`، `metadata.ts:245-248` |
| `?create_token=true` (admin) | يُلحق UUID | `files.ts:496-506` |
| `?delete_token=t` (admin) | يُزال الرمز. إن أصبحت القائمة فارغة يُنشأ رمز جديد فورًا. حذف رمز غير موجود لا يفعل شيئًا لكنه يزيد `metageneration` | `metadata.ts:250-263`، `files.ts:513-523` |
| `?token=` صالح على `GET /v0` | **يتجاوز القواعد كليًا**، للبيانات الوصفية والبايتات معًا | `files.ts:154-168` |
| نسخ (`copyTo`/`rewriteTo`) | تُنسخ الرموز إن لم تُرسل `metadata` جديدة غير فارغة | `files.ts:370-377` |

**الإبطال:**

- `PATCH` بـ `metadata.firebaseStorageDownloadTokens` جديد **لا يزيل** الرموز القديمة، لأن الدمج اتحاد.
- `metadata.firebaseStorageDownloadTokens: null` لا يفعل شيئًا، لأن الرموز ليست في `customMetadata`.
- الإبطال الوحيد هو `delete_token` الإداري.
- VERIFIED من `metadata.ts:149-163,216-235`. اختلافه عن الإنتاج INFERENCE (القسم 6).

## 4. القواعد والمصادقة

### 4.1 البروتوكول مع JAR

VERIFIED: `rules/runtime.ts:107-421`، `rules/types.ts:28-89`.

- يتواصل الطرفان بأسطر JSON عبر stdin وstdout. لكل طلب `id` متزايد.
- الاستجابات تُطابَق بـ `id` أو `server_request_id`. التأطير على `\n`، وهو إصلاح issues #6194 و#6865 (`:200-252`).
- الإجراءات:

| الإجراء | الطلب | الرد |
|---|---|---|
| تحميل | `{id, action:"load_ruleset", context:{rulesetName:"<عداد>", source:{files:[{name,content}]}}}` | `{id, status, result:{rulesVersion}, errors[], warnings[]}` |
| تقييم | `{id, action:"verify", context:{rulesetName, service:"firebase.storage", path:"/b/{bucket}/o/{object}", method, delimiter?, variables:{resource, request}}}` | `{result:{permit:bool}, errors, warnings}` |
| طلب وثيقة | يرد JAR بـ `{action:"fetch_firestore_document", context:{path}, server_request_id}` | يجلبها المحاكي من محاكي Firestore بـ `GET /v1/projects/{p}{path}` (عبر `EmulatorRegistry.client(FIRESTORE, {auth:true})`)، ثم يرد بنفس الـ id: `{result:{name,fields}, status:"ok"}`. **أي خطأ يصبح `status:"not_found"`** (`:491-507`) |

- قيم المتغيرات (`ExpressionValue`، `:428-489`، `expressionValue.ts:40-60`) تُرمَّز كالتالي:
  - `string_value` و`bool_value`.
  - `int_value` للأعداد الصحيحة، و`float_value` لغيرها.
  - `timestamp_value` بصيغة `toSerializedDate`.
  - `list_value` و`set_value` و`map_value{fields}` و`null_value` و`path_value{segments:[{simple}]}`.
- **الحالات التي تعني الرفض:**
  - وجود `errors` في `verify`، لأن `permitted` يكون `undefined`.
  - غياب ruleset.
  - LIST مع `rules_version < 2`، ومعه التحذير "List operations are only allowed for rules_version='2'".
  - VERIFIED: `runtime.ts:64-73,411-420`، `rules/utils.ts:108-114,136`.

### 4.2 ما يُمرَّر كـ `request` و`resource`

VERIFIED: `runtime.ts:361-385,513-557`، `metadata.ts:126-147`.

- **`resource`** هو الكائن الموجود (`before`) بصيغة `RulesResourceMetadata`، أو `null`. حقوله:
  - `name` و`bucket` و`generation`(int) و`metageneration`(int) و`size`(int).
  - `timeCreated` و`updated` (timestamp).
  - `md5Hash` و`crc32c` (نص عشري) و`etag`.
  - `contentDisposition` و`contentEncoding` و`contentType` و`metadata`.
  - **بلا `contentLanguage` وبلا `cacheControl`.**
- **`request`** خريطة فيها:
  - `path`: `path_value` لكل مقاطع `/b/{bucket}/o/...`، مقسومة على `/`.
  - `time`: الآن.
  - `resource`: الكائن المقترح (`after`) أو `null`.
  - `auth`: `null`، أو `{uid: payload.user_id, token: payload}`.
  - `uid` يؤخذ من **`user_id`** وليس من `sub`.
- `request.params` و`request.method` لا يُمرَّران. VERIFIED بالغياب.

### 4.3 ربط العمليات

VERIFIED: `files.ts:151-493`، `rules/types.ts:3-11`.

| العملية | `method` | `before` و`after` |
|---|---|---|
| `GET` بيانات وصفية أو بايتات (`/v0`) | `get` | `before` = الموجود |
| list (`/v0`) | `list` | لا شيء. المسار `b/{bucket}/o/{prefix بلا / أخيرة}`، و`delimiter` يُمرَّر |
| أي رفع (media/multipart/resumable) | **`create` دائمًا** | `before` = الموجود أو لا شيء، و`after` = الجديد |
| `PATCH` | `update` | `before` و`after` = الموجود بعد تطبيق التغييرات على نسخة |
| `DELETE` | `delete` | `before` |

- الإحالة من `read` إلى `get`+`list` ومن `write` إلى `create`+`update`+`delete` تتم داخل JAR. INFERENCE.
- التحقق من الصلاحية **يسبق** التحقق من الوجود. فكائن غير موجود مع قاعدة رافضة يعطي 403، ومع قاعدة قابلة يعطي 404. VERIFIED: `files.ts:159-175,212-225`.

### 4.4 المصادقة

VERIFIED: `rules/utils.ts:96-141`، `runtime.ts:513-529`.

- **التجاوز الإداري:** `Authorization` يساوي حرفيًا `Bearer owner` أو `Firebase owner`. يتجاوز القواعد على `/v0`، وهو شرط `create_token`/`delete_token`. تستعمله واجهة Emulator UI.
- **الرمز:** `authorization.split(" ")[1]`، أي الكلمة الثانية مهما كان المخطط (`Firebase`/`Bearer`/غيرهما).
  - يُفك بـ `jwt.decode` **بلا تحقق من التوقيع** ولا من `exp`/`aud`/`iss`.
  - الرمز غير القابل للفك يعطي `auth = null`. أي JWT غير موقّع (`alg:none`) مقبول.
- **سطح GCS لا يقرأ `Authorization` أبدًا.**
  - `getAdminOnlyFirebaseRulesValidator` يعيد `true` دائمًا، ومعه TODO: "This should check for valid admin credentials some day" (`rules/utils.ts:67-85`).
  - النسخ يمرر `"Bearer owner"` ثابتًا، ومعه TODO عن التحقق من OAuth (`gcloud.ts:480-483`).

### 4.5 إدارة القواعد وأحداث Functions

- **إدارة القواعد** (`rules/manager.ts:39-177`، `rules/config.ts:22-86`):
  - ruleset واحد لكل الـ buckets، أو ruleset لكل bucket مأخوذ من targets في `.firebaserc`.
  - الإعداد متعدد العناصر **يشترط `target`**.
  - الملف مراقَب بـ chokidar، ويُعاد تحميله عند التغيير بعد 5ms.
  - عند خطأ ترجمة يبقى الـ ruleset السابق.
- **أحداث Functions** (`cloudFunctions.ts:19-118`):
  - ترسل `POST /functions/projects/{p}/trigger_multicast` مرتين: بصيغة legacy (`google.storage.object.{finalize|metadataUpdate|delete}`)، وبصيغة CloudEvents (`google.cloud.storage.object.v1.{finalized|metadataUpdated|deleted}`).
  - `archive` لا يُطلق أبدًا.

## 5. التخزين والقوائم والأخطاء

### 5.1 التخزين

VERIFIED: `files.ts:117-132,525-527,537-655`، `persistence.ts`، `index.ts:99-124`.

- الكائنات في `Map<string, StoredFile>`، والمفتاح `path.join(bucket, object)`. الـ buckets في `Map` تُملأ تلقائيًا من أي طلب.
- البايتات على القرص بأسماء UUID، والخريطة `_diskPathMap` في الذاكرة.
- `reset` يفرغ الخرائط فقط. `stop` يحذف المجلد كله، فلا بقاء بلا تصدير.
- **صيغة التصدير** (`firebase emulators:export` أو `--export-on-exit`) في المجلد `storage_export/`:
  - `buckets.json` بالشكل `{"buckets":[{"id":"..."}]}`.
  - `blobs/<uuid>`.
  - `metadata/<encodeURIComponent(uuid)>.json`. محتواه `StoredFileMetadata` كـ JSON بمسافتين، بلا الحقول التي تبدأ بـ `_`، و`crc32c` نص عشري، و`downloadTokens` مصفوفة.
  - في `firebase-export-metadata.json` يُكتب `"storage":{"version":"<cli>","path":"storage_export"}` (`hubExport.ts:135-140`).
- **الاستيراد** (`--import`، `controller.ts:973-977`):
  - يمشي على `metadata/`. مسار الـ blob هو المسار النسبي بلا `.json`.
  - الملفات الناقصة يُتخطى ما يخصها مع تحذير.
- الرفوع الجارية لا تُصدَّر. بعد الاستيراد يعيد `getPreviousResponseCode` القيمة 200 (`upload.ts:241-247`).
- التصدير يستدعي `listBuckets()`. إن لم يوجد bucket قد يطلب `adminSdkConfig` من `firebase.googleapis.com` الحقيقي لمشروع غير `demo-` (`files.ts:134-144,543`).

### 5.2 القوائم

VERIFIED: `files.ts:415-493`، `apis/firebase.ts:153-199`، `apis/gcloud.ts:147-171`.

- مسح خطي لكل الملفات. التصفية بـ `bucket` ثم `startsWith(prefix)`.
- مع `delimiter`:
  - الكائن يدخل `items` إن لم يحتوِ الفاصل بعد البادئة.
  - وإلا يُضاف `name.slice(0, idx+len(delimiter))` إلى `prefixes`.
- الترتيب: `items` بترتيب نصي (code-unit)، و`prefixes` مرتبة.
- **`pageToken` = اسم أول عنصر في الصفحة التالية**، نصًا مكشوفًا.
  - إن لم يوجد الاسم (مثلًا حُذف)، تبدأ القائمة **من أولها** (`:474-479`).
- **`maxResults` افتراضيه 1000 وبلا حد أعلى.**
  - الترقيم يطبَّق على `items` فقط. **`prefixes` تعاد كاملة في كل صفحة.**
- `/v0` يحذف من الرد الأسماء والبادئات التي فيها مقطع فارغ أو `/` في أولها (`isValidNonEncodedPathString`، `:562-585`)، ويعيد `items:[{name,bucket}]` و`prefixes:[]` دائمًا.
- GCS يعيد `items` كاملة، ويحذف `prefixes`/`items` إن كانت فارغة.

### 5.3 أشكال الأخطاء

| الحالة | `/v0` (Firebase) | GCS |
|---|---|---|
| 400 | غالبًا `sendStatus(400)` بنص "Bad Request". خطأ البادئة JSON `{"error":{"code":400,"message":...}}`. multipart نص خام | غالبًا `sendStatus(400)`. multipart JSON `{"error":{"code":400,"message"}}`. POST policy نص خام |
| 403 | `{"error":{"code":403,"message":"Permission denied. No READ\|LIST\|WRITE permission."}}`، أو "Missing admin credentials."، أو "...no loaded ruleset." | `sendStatus(403)` (لا يكاد يحدث) |
| 404 | `sendStatus(404)`، نص "Not Found" بلا JSON | `{"error":{"code":404,"message":"No such object: {b}/{o}","errors":[{"message","domain":"global","reason":"notFound"}]}}`. مع `alt=media` الرسالة نصًا (`gcloud.ts:530-550`) |
| 413 | `bodyParser` (أكبر من 130mb): صفحة خطأ Express | مثلها |
| 501 | يسقط إلى GCS | `sendStatus(501)` |

- **لا يوجد `status` نصي** (مثل `NOT_FOUND`) في أي جسم. INFERENCE: الإنتاج يختلف (القسم 6).

## 6. انحرافات معروفة عن الإنتاج

### 6.1 يعترف بها المصدر

1. ACL غير مدعومة، وكل طرقها تنجح بلا أثر. VERIFIED: `gcloud.ts:234-237`.
2. لا تحقق من بيانات اعتماد admin على سطح GCS، فكل طلب يُعامل كمالك. VERIFIED: `rules/utils.ts:78-81`، `gcloud.ts:480-483`.
3. رفع GCS القابل للاستئناف يدعم خطوة `upload` واحدة فقط، بلا تقسيم. VERIFIED: `conformance/gcs.endpoints.test.ts:199,234`.
4. `rewriteTo` مع `rewriteToken` غير مدعوم، فيسقط إلى 501. VERIFIED: `gcloud.ts:468-471`، `conformance/gcs-js-sdk.test.ts:717`.
5. POST policy الموقّع يُقبل عبر نقطة المحاكي بلا تحقق، والاختبار `emulatorOnly`. VERIFIED: `conformance/gcs.endpoints.test.ts:350-352`.
6. معاملات الاستعلام لا يُتحقق من تكرارها. VERIFIED: `gcloud.ts:149`.
7. CORS مفتوح مع عكس الأصل، ومعه التعليق "similar to production behavior". VERIFIED: `server.ts:39-41`.
   - `storage.googleapis.com` الإنتاجي يخضع لإعداد CORS الخاص بالـ bucket. INFERENCE.
8. issues مذكورة في التعليقات:
   - #4227 (Private Network Access).
   - #6194 و#6865 (تأطير stdout الذي كان يعلّق الطلبات).
   - #3915 (تأخير الكتابة القديم).
   - b/242314185 (تغطية الاختبارات، `conformance/*.endpoints.test.ts:23,35`).

### 6.2 مستنتجة (INFERENCE ما لم يُذكر غير ذلك)

1. **الرموز غير موثقة:** أي JWT يُقبل بلا توقيع ولا انتهاء صلاحية، و`auth.uid` يأتي من `user_id` فقط. VERIFIED في الشيفرة: `runtime.ts:517-526`.
   - Aruma يجب أن يتحقق، ويرفض `alg:none` و`Bearer owner` (ADR-010 S-3).
2. **`Bearer owner` و`Firebase owner` يتجاوزان القواعد** على `/v0`. VERIFIED: `rules/utils.ts:117-119,139-141`.
3. **الكتابة فوق كائن موجود تُقيَّم `create` لا `update`.** VERIFIED: `files.ts:320-330`.
   - **تصحيح من المنسّق: هذا ليس انحرافًا.** الوثائق الرسمية تجعل `create` لكل كتابة لمحتوى الملف و`update` لتعديل البيانات الوصفية فقط، واختبار مطابقة يعمل على الإنتاج يؤكده (انظر 7.1).
4. `generation` بالميلي ثانية، والإنتاج بالميكروثانية (16 رقمًا).
   - `etag` لا يتغير بعد تعديل البيانات الوصفية، وصيغته sha1 لا صيغة GCS.
5. `crc32c` في صيغة `/v0` نص عشري، والإنتاج على الأرجح base64 كما في GCS.
6. ترويسة `x-goog-metadatageneration` بدل `x-goog-metageneration`.
   - `Content-Disposition` بلا `utf-8''`.
   - `contentDisposition: "inline"` يُحفظ في الكائن عند رفع `/v0`.
7. أخطاء `/v0` من نوع 404 و400 نص خام. الإنتاج يعيد على الأرجح `{"error":{"code":404,"message":"Not Found."}}`.
   - غلاف GCS ينقصه `status`.
8. **مفتاح التخزين `path.join(bucket, object)`** يطبّع `//` و`./` و`../`. فالكائنان `a//b` و`a/b` يتصادمان، والاسم `a/../b` يصبح `b`.
   - على Windows يتغير الفاصل (`files.ts:525-527,631-638`).
9. **فك ترميز مزدوج** على `/v0`: Express يفك `req.params`، ثم يُطبَّق `decodeURIComponent` مرة ثانية (`firebase.ts:121,403,470,520`).
   - الأسماء التي فيها `%` قد تتغير، أو ترمي `URIError` فتعطي 500.
   - سطح GCS يفك مرة واحدة.
10. لا شروط مسبقة: `ifGenerationMatch` و`ifMetagenerationMatch` تُتجاهل.
    - لا إصدارات كائنات، و`?generation=` يُتجاهل.
    - `fields` و`projection` و`predefinedAcl` تُتجاهل.
    - لا `compose`، ولا bucket get/insert/delete، ولا PATCH تحت `/storage/v1`.
11. **الـ bucket يُنشأ ضمنيًا** من أي طلب، فلا 404 لـ bucket مجهول. ومع ذلك `/v0` يرد 403 "no loaded ruleset" لـ bucket خارج targets في وضع تعدد الأهداف.
12. الحد الأقصى 130MB لكل طلب، بلا تقسيم حقيقي في GCS.
    - Firebase resumable يعلن `chunk-granularity: 10000` ولا يفرضه.
    - لا تحقق من الإزاحة ولا من الحجم المعلن.
13. القوائم:
    - `pageToken` مكشوف، ويُعاد البدء إن كان مجهولًا.
    - `maxResults` بلا سقف، والإنتاج سقفه 1000 على الأرجح.
    - `prefixes` غير مرقَّمة.
    - قواعد `list` تُقيَّم على البادئة لا على كل عنصر.
14. **الرموز:**
    - تُنشأ تلقائيًا عند أي `GET` على `/v0` (تأثير جانبي يرفع `metageneration`).
    - تتجاوز القواعد حتى لطلب البيانات الوصفية.
    - لا يمكن إبطالها بتعديل `firebaseStorageDownloadTokens`.
    - في الإنتاج تُخزن كـ custom metadata عادية، فاستبدالها أو حذفها يُبطل القديم.
15. `resource` في القواعد ينقصه `contentLanguage`، والإنتاج يذكره في وثائق `resource`. VERIFIED الغياب: `metadata.ts:131-146`.
16. `/internal/*` بلا مصادقة. `listBuckets` قد يتصل بـ Google الحقيقي. لا مهلة ولا تنظيف لرفوع resumable المهجورة.
17. Range متعدد غير مدعوم، والنطاق غير المرضي يرد 200 بدل 416.
18. مسار رفع GCS لا يُنشئ رمز تنزيل، فالملف المرفوع بـ Admin SDK لا رمز له حتى أول `GET /v0`. السلوك الإنتاجي هنا غير مؤكد.

### 6.3 ما يعنيه هذا لتصميم Aruma

- نأخذ من المحاكي:
  - **فصل السطحين.**
  - تأجيل تقييم القواعد إلى الإنهاء، مع حفظ هوية `start`.
  - دلالة `null` مقابل `undefined` في التحديث.
  - بنية `prevResponseCode` لتكرار الإنهاء.
- لا نأخذ منه:
  - التحقق الغائب.
  - `path.join`.
  - رموز الاتحاد.
  - الترقيم بالاسم المكشوف. نستعمل رمزًا معتمًا موقّعًا.
- كل البنود المستنتجة في 6.2 تحتاج تأكيدًا من تسجيلات الإنتاج قبل اعتماد سلوك مختلف.

## ملاحظات من التسجيلات

المصدر: `test/oracle/storage/emulator/*.json`، الجولة `s0c-20261023-emu` بتاريخ 2026-10-23، المحاكي نفسه (firebase-tools 15.34.0، والـ JAR v1.1.3 على OpenJDK 21) خلف وكيل تسجيل. كل بند أدناه **مرصود** (OBSERVED)، واسم الملف بين قوسين. ما يؤكد قراءة الشيفرة أعلاه لا يُكرَّر إلا إن أضاف تفصيلًا.

### 7.1 ما أكدته التسجيلات من قراءة الشيفرة

- **الرفع يُقيَّم `create` حتى فوق كائن موجود** (`rules.json`، الخطوة `guarded: overwrite a locked object (update)`): القاعدة تسمح بـ `create` وحده، وشرط `update` يرفض، فنجح الرفع.
  - وهذا **يطابق الإنتاج** بحسب الوثائق الرسمية: `create` "Applies to writes to file contents"، و`update` "Applies to updates to (pre-existing) file metadata" (firebase.google.com/docs/storage/security/core-syntax، قُرئت 2026-10-23). واختبار المطابقة `should upload replace existing file` (يُشغَّل على الإنتاج أيضًا) يكتب فوق ملف لا يسمح له إلا `read, create`. فالبند 6.2-3 أعلاه **ليس انحرافًا**؛ `update` في Storage لـ PATCH البيانات الوصفية وحده.
- **القواعد في الرفع المتقطع تُقيَّم عند الإنهاء**: الطلب `start` والقطعة الأولى يردّان 200، ثم `upload, finalize` يرد 403 مع `x-goog-upload-status: final` (`rules.json`، `unauthenticated resumable upload` و`limited: resumable too big`). الـ SDK يعيد `storage/unauthorized` بالحالة 403.
- **التحقق من الصلاحية قبل الوجود**: B يقرأ ملفًا غير موجود في مجلد A فيأخذ 403 لا 404، وA يقرأ ملفًا غير موجود في مجلده فيأخذ 404 (`rules.json`). وحذف كائن غير موجود تحت قاعدة تقرأ `resource.metadata` يعطي 403 (`guarded: delete missing object`): `resource` يساوي `null` فيصير الشرط خطأً فرفضًا.
- **الإزاحات لا تُفحص**: قطعة بإزاحة 15 بعد 10 بايتات، ثم قطعة بإزاحة 5، كلتاهما 200، والحجم النهائي 30 (`raw_resumable.json`). والإنهاء بـ 4 بايتات بعد إعلان 10 ينجح بحجم 4.
- **رموز التنزيل تتجاوز القواعد** للبايتات وللبيانات الوصفية: رابط `getDownloadURL` لملف في مجلد A يعمل بلا مصادقة وبمصادقة B، و`?token=` على رابط البيانات الوصفية (بلا `alt=media`) يعيد البيانات الوصفية بلا مصادقة (`rules.json`).
- **الرموز اتحاد لا استبدال**: PATCH من العميل بـ `metadata.firebaseStorageDownloadTokens: "mine-1"` **يضيف** `mine-1` إلى الرموز ويعمل رابطه بعدها، والحقل `downloadTokens` في جسم PATCH يُتجاهل (`tokens.json`). أي أن **عميلًا له صلاحية `update` يستطيع زرع رمز يعرفه** في المحاكي؛ سلوك الإنتاج هنا غير معروف (E، البند 3.4).
- **`create_token`/`delete_token`** للمسؤول فقط (`Bearer owner`)، وللمستخدم 403 `{"error":{"code":403,"message":"Missing admin credentials."}}`. حذف رمز غير موجود يرد 200 ويرفع `metageneration`، و`create_token=true&delete_token=x` معًا يرد 200 (`tokens.json`).
- **أي JWT يُقبل**: رمز `alg:none` مزوّر يقرأ (200)، والمخطط `Bearer` يُقبل لرمز المستخدم كما `Firebase`، والنص العشوائي يعطي 403 (`sdk_read.json`، `rules.json`).

### 7.2 أشياء لم تظهر في قراءة الشيفرة

- **`start` يرد 200 بجسم `OK`** (`text/plain`)، و`X-Goog-Upload-URL` مبني من ترويسة `Host` التي وصلته (هنا الوكيل `127.0.0.1:8865`)، فالخادم خلف وكيل يجب أن يبني الرابط من المضيف الخارجي. شكله: `http://<host>/v0/b/<b>/o?name=<enc>&upload_id=<id>&upload_protocol=resumable`.
- **ردود القطع الوسطى** فيها `x-goog-upload-status: active` و`x-gupload-uploadid` وجسم `OK`، **بلا** `X-Goog-Upload-Size-Received`؛ هذه الترويسة لا تأتي إلا مع `query` (`sdk_upload.json`، `raw_resumable.json`).
- **تقطيع JS المرصود**: ملف 700 KiB = `start` ثم `upload` بـ 262144 بايت عند الإزاحة 0، ثم `upload, finalize` بالباقي 454656 دفعة واحدة (حجم القطعة تضاعف إلى 512 KiB فاتسع للباقي). ملف 1 KiB عبر `uploadBytesResumable` أُرسل multipart (`sdk_upload.json`).
- **`getBytes(ref, 4)`** يرسل `Range: bytes=0-4` (خمسة بايتات، النهاية شاملة)، فيرد المحاكي 206 بـ `Content-Range: bytes 0-4/14`، والـ SDK يقص النتيجة إلى 4 (`sdk_read.json`).
- **ترويسات التنزيل الفارغة**: `content-encoding: ''` و`cache-control: ''` تُرسل فارغة حين لا قيمة، و`content-disposition: inline; filename*=r.txt` (`sdk_read.json`). ورد PATCH (JSON) يحمل هو أيضًا `content-disposition` و`cache-control` الخاصين بالكائن (`sdk_metadata.json`).
- **bucket مجهول**: `/v0/b/no-such-bucket/...` يعطي 403 "No READ permission." للقراءة و"No WRITE permission." للرفع، لأن الـ bucket يُنشأ ضمنيًا والقواعد تطابق `/b/{bucket}` (`raw_upload.json`). الإنتاج يرد 404 `{"error":{"code":404,"message":"Not Found."}}` (`production/bucket_probe.json`).
- **`GET /v0/b/<b>/o/`** (اسم فارغ) يُعامل كقائمة على الجذر: 403 "No LIST permission." (`raw_upload.json`).
- **multipart**: الاسم في `metadata.name` وحده يعطي 400 "Bad Request" (المحاكي يشترط `?name=`)، وحين يختلف الاسمان يغلب الاسم في الاستعلام. الـ boundary الخاطئ يعطي 400 `text/html` بنص "Unexpected number of parts in request body" (`raw_upload.json`).
- **CORS**: الـ preflight يرد 204 ويعكس الأصل والترويسات المطلوبة، والطرق `GET,HEAD,PUT,PATCH,POST,DELETE` (`raw_upload.json`).
- **القوائم**: `prefixes` في الرد بشرطة في آخرها (`.../list/d/`) والـ SDK يحذفها؛ و`nextPageToken` هو اسم العنصر التالي نصًا؛ و`prefixes` تتكرر في كل صفحة؛ و`pageToken=garbage` يعيد الصفحة الأولى بـ 200؛ و`maxResults=5000` يُقبل؛ و`startOffset` يُتجاهل؛ والبادئة بلا `/` في آخرها مع `delimiter` تعطي 400 بجسم JSON (`sdk_list.json`).
- **PATCH بحقول للقراءة فقط أو مجهولة** (`size`، `md5Hash`، `generation`، `name`، `bogus`) يرد 200 ويتجاهلها ويرفع `metageneration`، وJSON غير صالح يعطي 400 (`sdk_metadata.json`).

### 7.3 مسار Admin (سطح GCS)

- **firebase-admin (Node) في وضع المحاكي** (`admin_node.json`):
  - البيانات الوصفية والقوائم والحذف والنسخ على `/b/<b>/o/...?` **بلا `/storage/v1`**، والرفع على `/upload/storage/v1/b/<b>/o?uploadType=multipart|resumable&name=`.
  - **لا ترويسة `Authorization` على أي طلب.** ومعه `getDownloadURL` من Admin يطلب `/v0/b/<b>/o/<o>` بلا مصادقة، فرفضته القواعد بـ 403.
  - `save()` الافتراضي resumable حتى لـ 13 بايتًا: `POST` بجسم `{}` و`x-upload-content-type`، ثم `PUT` واحد بـ `Content-Range: bytes 0-*/*` و`transfer-encoding: chunked`. و`resumable: false` يرسل multipart.
  - `copy()` و`move()` عبر `POST .../rewriteTo/b/<b>/o/<dst>` بجسم `{}`، و`move` = rewrite ثم `DELETE`.
  - `ifGenerationMatch=0` على كائن موجود **نجح** (يجب 412)، و`bucket.exists()` (`GET /b/<b>?`) يعطي 404.
- **`@google-cloud/storage` بـ `apiEndpoint`** (`admin_gcs_endpoint.json`): المسارات `/storage/v1/b/...` بـ `Authorization: Bearer`، لكن **طلبات الرفع المتقطع (`POST` البدء و`PUT` القطع) بلا `Authorization`**. و`PATCH /storage/v1/...` يعطي 501. والرفع بقطع 4 MiB: المحاكي أنهى الكائن مع أول `PUT` (`Content-Range: bytes 0-4194303/*`) ورد 200، فاكتشف العميل عدم التطابق (`FILE_NO_UPLOAD`) وحذف الكائن.
- **firebase-admin-go** (`admin_go.json`):
  - التنزيل عبر صيغة XML API: `GET /<bucket>/<object بـ %2F>` لا `alt=media`.
  - القطع ترسل `X-Guploader-No-308: yes`؛ المحاكي رد 200 بجسم `OK` فقرأه Go كـ JSON وفشل (`invalid character 'O'`).
  - الرفع دفعة واحدة يرسل `crc32c` داخل JSON الـ multipart.
  - `Update` (PATCH) و`CopierFrom` (rewrite) تحت `/storage/v1` يعطيان 501، و`DoesNotExist` (`ifGenerationMatch=0`) تجاهله المحاكي، و`Bucket.Attrs` يعطي 404.
