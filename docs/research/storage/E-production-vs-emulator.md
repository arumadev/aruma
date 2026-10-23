# E: الإنتاج مقابل المحاكي في Cloud Storage for Firebase

- **التاريخ:** 2026-10-23. المهمة S0-C.
- **السؤال:** أين يختلف محاكي Storage عن إنتاج Firebase، وما الذي يجب أن يتبعه Aruma حين يختلفان؟
- **القاعدة (من الـ brief):** الإنتاج أولًا، ثم المحاكي كصندوق أسود، ثم مجموعات اختبار Google. ولا نكون أكثر تساهلًا من Firebase أبدًا.

## 0. الخلاصة

> **لا يوجد bucket افتراضي في مشروع Firebase الخاص بالمالك (المسمى `<project>` في الملفات)، فلا توجد تسجيلات إنتاج لعمليات Storage.**
>
> الاسمان الافتراضيان (`<project>.appspot.com` و`<project>.firebasestorage.app`) يردّان 404 على واجهة العملاء وعلى GCS JSON API لطلب بلا اعتماد، تمامًا مثل اسم عشوائي، بينما bucket عام موجود يرد 200 على الطلبات نفسها (`test/oracle/storage/production/bucket_probe.json`). وإنشاء bucket افتراضي اليوم يتطلب خطة Blaze ("To provision a new default bucket using the Firebase console or REST API, your project must be on the pay-as-you-go Blaze pricing plan"، firebase.google.com/docs/storage/faqs-storage-changes-announced-sept-2024). الـ brief يمنع تغيير الفوترة، **فلم يُنشأ bucket، ولم تُنشر قواعد Storage على المشروع، ولم يُكتب أي كائن، ولا شيء يحتاج إلى استعادة.**
>
> كل التسجيلات التفصيلية من المحاكي (`test/oracle/storage/emulator/`). وهذه الوثيقة تجمع ما نعرفه عن الإنتاج من مصادر أخرى، وتصنّف كل فرق.

مصادر "الإنتاج" المتاحة دون bucket، مرتبة بالثقة:

| الرمز | المصدر | ماذا يعطي | الثقة |
|---|---|---|---|
| **P** | تسجيلات إنتاج بلا اعتماد على bucket غير موجود (`production/bucket_probe.json`) | شكل الخطأ، وترويسات CORS وغيرها على أخطاء واجهة العملاء، ورسالة المسار المجهول | مرصود من الإنتاج، لكن لمسارات الخطأ وحدها |
| **DOC** | وثائق Firebase وGCS الرسمية | دلالات القواعد، والرموز، وصيغ GCS | رسمي، بلا تفاصيل السلك |
| **CT** | اختبارات المطابقة في firebase-tools (`scripts/storage-emulator-integration/conformance/*.test.ts`)، مكتوبة لتعمل على الإنتاج (`useProductionServers`، `env.ts:15-17`)، والحالات التي ليست `emulatorOnly` يُفترض أنها تمر على الإنتاج | حالات وأجسام محددة | ثانوي: لا نعرف متى شُغّلت على الإنتاج آخر مرة |
| **SDK** | كود المكتبات (A وB) | ما يرسله العميل وما يحتاجه | يقيني لجهة العميل، ولا يقول ما يرد به الإنتاج |
| **EMU** | تسجيلات المحاكي (`emulator/*.json`) وقراءة كوده (D) | كل شيء | المرتبة الثانية |

مفتاح التصنيف في الجداول:
- **=**: الإنتاج والمحاكي متفقان (بدليل مذكور).
- **≠**: يختلفان بدليل. Aruma يتبع الإنتاج.
- **؟**: سلوك الإنتاج غير معروف. القرار المؤقت لـ Aruma مذكور، ويُحسم بتسجيل إنتاج (القسم 6).

## 1. واجهة العملاء `/v0`: العناوين والأخطاء

| البند | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| bucket غير موجود | 404 `{"error":{"code":404,"message":"Not Found."}}` للقراءة والقائمة والرفع (P) | ينشئ الـ bucket ضمنيًا، ثم 403 "Permission denied. No READ\|WRITE permission." (`raw_upload.json`) | **≠** 404 بشكل الإنتاج |
| كائن غير موجود | 404 بالجسم نفسه أعلاه (P، على bucket مجهول) | 404 بنص `Not Found` غير JSON (`sdk_read.json`) | **≠** شكل الإنتاج. المكتبات لا تقرأ الجسم (A §5.1)، فالأثر تشخيصي فقط |
| مسار مجهول (`/v0/b/`، `/v0/x`) | 400 `{"error":{"code":400,"message":"Invalid HTTP method/URL pair."}}` (P) | يسقط إلى راوتر GCS فيرد 501 `Not Implemented` (D §1.1) | **≠** 400 بشكل الإنتاج |
| شكل JSON الخطأ | منسّق بمسافتين، `Content-Type: application/json; charset=UTF-8` (P) | سطر واحد | المسافات لا تهم أي عميل. نكتب JSON مضغوطًا |
| رفض القواعد | 403 (CT `should return a 403 on rules deny`، والرسالة في الـ SDK "User does not have permission") | 403 `{"error":{"code":403,"message":"Permission denied. No READ permission."}}` | **=** في الحالة. نص الرسالة في الإنتاج **؟** (لم يُرصد). نستعمل نص المحاكي مؤقتًا |
| `X-Content-Type-Options: nosniff` | على كل الردود المرصودة (P) | غائب | **≠** نرسلها دائمًا (وهو قرار ADR-022 أصلًا) |
| `Cache-Control` على ردود JSON | `private, max-age=0` (P) | غائب | نرسل `private, max-age=0` على JSON |

## 2. CORS

| البند | الإنتاج (P) | المحاكي (`raw_upload.json`) | قرار Aruma |
|---|---|---|---|
| `Access-Control-Allow-Origin` | `*` | يعكس الأصل | `*` كالإنتاج. لا نرسل `Allow-Credentials`، والمصادقة بترويسة لا بكعكة |
| الترويسات المكشوفة على ردود الخطأ العادية | `Content-Range, X-Firebase-Storage-XSRF` | قائمة ثابتة فيها كل `x-goog-upload-*` | ؟ على ردود النجاح. JS يحتاج قراءة `X-Goog-Upload-URL` و`X-Goog-Upload-Status` و`X-Goog-Upload-Size-Received` (A §6) |
| الترويسات المكشوفة على رد رفع (start فاشل) | `Content-Length, Content-Range, Date, Server, Transfer-Encoding, X-Firebase-Storage-XSRF, X-GUploader-UploadID, X-Goog-Upload-Status, X-Google-Trace`، ومعها `X-Goog-Upload-Status: final` | كما سبق | **مرصود جزئيًا**: الإنتاج يغيّر القائمة حسب نوع الطلب. نكشف قائمة الإنتاج + `X-Goog-Upload-URL` و`X-Goog-Upload-Size-Received` على ردود الرفع |
| preflight (`OPTIONS`) | على bucket غير موجود: 404 JSON بلا `Allow-Methods` (P). على bucket موجود ؟ | 204 يعكس الترويسات، والطرق `GET,HEAD,PUT,PATCH,POST,DELETE` | ؟ نرد 204 بالطرق والترويسات التي يرسلها العملاء (A §6.1) |

## 3. الرفع

### 3.1 الأنواع والأوامر

| البند | الإنتاج | المحاكي | التصنيف |
|---|---|---|---|
| رفع بلا `X-Goog-Upload-Protocol` = media خام بـ `?name=` | نعم (CT `should default to media upload`) | نعم (`raw_upload.json`) | **=** |
| `X-Goog-Upload-Command` بلا بروتوكول = resumable | نعم (CT `without upload protocol set`) | نعم | **=** |
| أوامر الجلسة بعد `start` بلا `Authorization`، وبـ `PUT` | مقبولة (CT `should accept subsequent resumable upload commands without an auth header`) | مقبولة | **=** ومطلوب لـ iOS (A §1) |
| `finalize` منفردًا بجسم فارغ | مقبول (CT نفسه) | مقبول | **=** |
| إنهاء مكرر بعد نجاح | 200 (CT) | 200 بجسم `OK` بلا metadata (D §2.2) | **=** في الحالة. الجسم في الإنتاج ؟. نعيد الـ metadata (iOS يقرأ الجسم، A §1) |
| إنهاء مكرر بعد 403 | 403 مرة أخرى مع `final` (CT) | نفسه | **=** |
| `cancel` مرتين | 200 ثم 200 (CT) | نفسه | **=** |
| `cancel` بعد `final` | 400 (CT) | 400 | **=** |
| `cancel`/أي أمر لجلسة مجهولة | 404 (CT) | 404 نصًا (`raw_resumable.json`) | **=** في الحالة. الجسم JSON في Aruma |
| `finalize` على جلسة ملغاة | 400 في المرتين (CT) | 400 | **=** |
| `query` | `X-Goog-Upload-Status` = `active` ثم `cancelled` ثم `final` حسب الحالة (CT `upload status`) | نفسه، ومعه `X-Goog-Upload-Size-Received` (`raw_resumable.json`) | **=** |
| رد `start` | 200، و`x-goog-upload-url` مطلق (CT) | 200 بجسم `OK`، ومعه `x-goog-upload-chunk-granularity: 10000` و`x-goog-upload-control-url: ""` | **=** في الأساس. لا نرسل `chunk-granularity` (iOS يقرّب الإزاحة به، A §6.5) |
| multipart بنوع محتوى غير صالح | 400 مع رسالة (CT) | 400 | **=** |
| multipart والاسم في `metadata.name` وحده | ؟ | 400 (`raw_upload.json`) | ؟ نقبله كما يفعل سطح GCS، فلا عميل رسمي يعتمد على الرفض |

### 3.2 ما لا يتحقق منه المحاكي

| البند | الإنتاج | المحاكي | قرار Aruma |
|---|---|---|---|
| `X-Goog-Upload-Offset` لا يطابق المستلَم (فجوة أو تداخل) | ؟ | يتجاهل الإزاحة ويُلحق الجسم كما هو (حجم 30 من 20، `raw_resumable.json`) | **لا نقلّد المحاكي.** القطعة المكررة كليًا تُقبل بلا أثر وتعيد الحجم الحالي، وأي إزاحة أخرى تُرفض 400 مع `X-Goog-Upload-Status: active` و`X-Goog-Upload-Size-Received`، حتى يرسل العميل `query` (A §7) |
| الإنهاء بحجم أقل من `X-Goog-Upload-Header-Content-Length` | ؟ | يُقبل بالحجم الفعلي | نرفض 400 حين أُعلن الحجم ولم يطابق. Android لا يعلن الحجم، فلا فحص له |
| حد حجم الطلب | ؟ (حدود GCS 5 TiB للكائن) | 130 MB لكل طلب (D §1.1) | حد قابل للضبط (S2) |
| تقييم القواعد في الرفع المتقطع | عند الإنهاء على الأقل (CT: `start` بلا مصادقة 200، والإنهاء 403 لأن `request.resource.size == 0`). هل يُقيَّم أيضًا عند `start`؟ ؟ | عند الإنهاء فقط، بهوية `start` | الإنهاء هو الحكم. لا نرفض عند `start` بمعلومات ناقصة (الحجم غائب في Android). نحد من الإساءة بحدود الجلسات والحجم |

### 3.3 القواعد على الرفع

- **الكتابة فوق كائن موجود تُقيَّم `create` في الإنتاج والمحاكي معًا (=).**
  - DOC: `create` "Applies to writes to file contents"، و`update` "Applies to updates to (pre-existing) file metadata" (firebase.google.com/docs/storage/security/core-syntax).
  - CT: `should upload replace existing file` يكتب فوق `upload/replace.txt` والقاعدة `allow read, create` فقط.
  - EMU: `guarded: overwrite a locked object` نجح مع رفض شرط `update` (`rules.json`).
  - و`resource` في `create` هو الكائن الموجود إن وُجد (CT `allowIfNoExistingFile`: `allow create: if resource == null` يرفض الكتابة الثانية).
  - **أثر على محرك القواعد (S1/S2):** `update` في Storage = PATCH البيانات الوصفية وحده، و`create` = كل رفع.
- **التحقق من الصلاحية قبل الوجود (= على الأرجح):** في المحاكي، كائن غير موجود تحت قاعدة رافضة يعطي 403 لا 404 (`rules.json`). الإنتاج ؟، لكن القاعدة تُقيَّم على المسار و`resource` = `null`، فالنتيجة نفسها متوقعة. نتبع المحاكي، فهو الأقل كشفًا للمعلومات.

## 4. البيانات الوصفية

| الحقل | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| الحقول الموجودة بعد رفع خام | `name, bucket, generation, metageneration, timeCreated, updated, storageClass, size, md5Hash, contentEncoding, contentDisposition, crc32c, etag, downloadTokens` كلها، و`metageneration="1"`، و`size` نص، و`contentEncoding="identity"` (CT `should set default metadata`) | نفسها (`sdk_upload.json`) | **=** |
| `contentType` الافتراضي | `application/octet-stream` (CT) | نفسه | **=** |
| `generation` | رقم بالميكروثانية، نصًا (DOC GCS: "The content generation of this object") | `Date.now()` بالميلي ثانية | **≠** الميكروثانية، كما في GCS. المكتبات لا تفترض طولًا |
| `crc32c` في صيغة `/v0` | نص (CT)، وصيغته ؟ | عدد عشري نصًا (`"338055207"`) | **؟** نستعمل base64 لأربعة بايتات big-endian كما في GCS، فلا عميل `/v0` يقرأه (A §4). يُحسم بتسجيل |
| `crc32c` في GCS | base64 (DOC) | base64 (`admin_node.json`) | **=** |
| `etag` | يتغير مع كل تغيير في المحتوى أو الـ metadata (DOC GCS) | sha1 ثابت بعد الإنشاء، لا يتغير مع PATCH (D §3.1) | **≠** يتغير مع `metageneration` |
| `contentDisposition` الافتراضي | نص موجود (CT) | `inline`، ويُحفظ في الكائن | ؟ في القيمة. نعيد `inline` ولا نحفظه، ونفرض نحن `Content-Disposition` آمنًا عند التنزيل (ADR-022) |
| التواريخ | ؟ | `2026-10-23T20:44:29.104Z` | **صيغة إلزامية** للعملاء: ثلاث خانات و`Z` (A §4) |
| PATCH: `null` يحذف الحقل، و`metadata.{k}: null` يحذف المفتاح | (CT `shoud allow deletion of settable metadata fields by setting to null`، و`...custom metadata by setting to null`) | نفسه (`sdk_metadata.json`) | **=** |
| PATCH: حقول للقراءة فقط أو مجهولة | ؟ | تُتجاهل، و`metageneration` يرتفع (`sdk_metadata.json`) | نتجاهلها، ونتجاهل `null` لها خصوصًا (iOS يرسل `null` لـ `downloadTokens` و`etag`، A §1) |
| PATCH: `metadata: null` | ؟ | يمحو كل الـ metadata المخصّصة | **؟** حرج لـ iOS (A §8.1). مؤقتًا نتبع المحاكي ودلالة GCS، ونسجّل طلب iOS حقيقيًا في S6 |
| ترويسات التنزيل | ؟ | `x-goog-hash`، و`x-goog-generation`، و`x-goog-metadatageneration` (الإنتاج في GCS: `x-goog-metageneration`)، و`content-encoding` و`cache-control` فارغة حين لا قيمة | لا نرسل ترويسة فارغة. نتبع أسماء GCS. ونضيف ترويسات الحماية (ADR-022) |
| `Range` | ؟ | 206 لنطاق واحد، والنطاق غير المرضي يعطي 200 كاملًا (D §3.3) | 206 لنطاق واحد، و416 لغير المرضي كما في HTTP. Android يحتاج 206 للاستئناف (A §1) |

## 5. رموز التنزيل

| البند | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| إنشاء رمز مع رفع `/v0` | نعم (CT: `downloadTokens` موجود بعد رفع خام) | نعم | **=** |
| رفع عبر GCS (Admin) | لا رمز: بعد `testBucket.upload()` يعطي `create_token` رمزًا واحدًا بالضبط (CT `should generate new token on create_token`) | لا رمز، ثم يُنشأ تلقائيًا عند أول `GET /v0` (D §3.4) | **=** في الرفع. الإنشاء التلقائي عند القراءة ؟ (D يراه أثرًا جانبيًا). لا ننشئ عند القراءة إلا إن أثبت تسجيلٌ ذلك؛ `getDownloadURL` بلا رمز يفشل في JS بـ `storage/no-download-url` (A §1.14) |
| الرمز يفتح الملف دون القواعد | نعم (DOC: رابط التنزيل "publicly accessible"، وهو ما يعيده `getDownloadURL`) | نعم، ولأي هوية أو بلا هوية (`rules.json`) | **=** |
| الرمز على رابط البيانات الوصفية بلا `alt=media` | ؟ | يعيد الـ metadata بلا مصادقة | **؟** نقصره على `alt=media` (أضيق)، ونسجّله في الإنتاج |
| `create_token`/`delete_token` | بـ OAuth المالك (`Authorization: Bearer <access token>`، كما في CT)، و403 لغيره (CT)، و400 لقيمة غير `true` (CT) | بـ `Bearer owner`، و403 "Missing admin credentials." للمستخدم (`tokens.json`) | **=** في الدلالة. Aruma: لمسؤول اللوحة أو حساب خدمة فقط |
| حذف آخر رمز | يُنشأ رمز جديد فورًا (CT `should regenerate a new token if the last remaining one is deleted`) | نفسه | **=** |
| زرع رمز من العميل بـ PATCH `metadata.firebaseStorageDownloadTokens` | ؟ | **يُضاف** إلى الرموز (اتحاد) ويعمل فورًا (`tokens.json`) | **؟ وقرار أمني:** لا يقبل Aruma هذا المفتاح من عملاء `/v0` (يُتجاهل بصمت) حتى يثبت تسجيل الإنتاج غير ذلك. Admin عبر GCS يضبطه كما في GCS |
| إبطال رمز بـ PATCH من Admin (`metadata.firebaseStorageDownloadTokens` = قيمة جديدة) | الرمز مخزّن كـ custom metadata في GCS، فالاستبدال يُبطل القديم (DOC GCS، INFERENCE) | اتحاد، فلا إبطال (D §3.4) | **≠** الاستبدال كما في GCS |
| نسخ كائن (rewrite) | الرموز تُنسخ ما لم تُرسل metadata جديدة، وتُحذف حين تُستبدل الـ metadata (CT `should preserve firebaseStorageDownloadTokens`، `should remove ... when overwriting custom metadata`) | نفسه | **=** |

## 6. القوائم

| البند | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| مصفوفتا `prefixes` و`items` حاضرتان ولو فارغتين | نعم (CT `zero element list array should still be present`) | نعم | **=** |
| عنصر نائب للمجلد (`abc/`) | لا يُدرج تحت نفسه، ويظهر `abc` بادئة (CT) | نفسه | **=** |
| أسماء فيها مقطع فارغ (`a//b`، `/x`) | تُحذف من العناصر، وتبقى البادئات الصالحة (CT `should not include show invalid prefixes and items`) | نفسه | **=** |
| `prefixes` بشرطة في آخرها في الرد | ؟ | نعم (`.../list/d/`) | نتبع GCS (بالشرطة)، والمكتبات تحذفها |
| `nextPageToken` | معتم (أسلوب GCS) | اسم العنصر التالي نصًا | **≠** رمز معتم موقّع، ونحذف الحقل في آخر صفحة (A §1.8) |
| `pageToken` غير صالح | ؟ (GCS يرد 400) | 200 من أول القائمة | **≠** 400 |
| `maxResults` | 1..1000 في الـ SDK (`sdk_list.json`: 0 و1001 مرفوضان محليًا). في الخادم ؟ | بلا حد (5000 قُبل) | نقص إلى 1000 |
| `prefixes` والترقيم | ؟ | `prefixes` كاملة في كل صفحة | نرقّم البادئات مع العناصر كما في GCS |
| بادئة بلا `/` في آخرها مع `delimiter` | ؟ | 400 برسالة JSON | ؟ المكتبات ترسل `/` دائمًا. نتبع المحاكي مؤقتًا |
| قاعدة `list` | تُقيَّم على البادئة | نفسه، و`get` مسموح مع `list` مرفوض يعمل (`rules.json`) | **=** |

## 7. المصادقة

| البند | الإنتاج | المحاكي | قرار Aruma |
|---|---|---|---|
| التحقق من رمز المستخدم | توقيع وصلاحية و`aud`/`iss` | `jwt.decode` بلا تحقق؛ رمز `alg:none` مزوّر يقرأ (`rules.json`) | **≠** تحقق كامل عبر `internal/platform/idtoken` |
| المخطط | `Firebase <idToken>` للعملاء (SDK)، و`Bearer <OAuth>` للمسؤول على `/v0` أيضًا (CT يستعمل `Bearer` لـ `create_token`) | أي مخطط، والكلمة الثانية تُفك (`Bearer <idToken>` قُبل) | نقبل `Firebase <idToken>` للمستخدمين، و`Bearer` لرموز وصول المسؤول. `Bearer <idToken>` ؟ في الإنتاج، فلا نقبله |
| تجاوز القواعد | OAuth للمالك أو حساب خدمة (Admin) | `Bearer owner` أو `Firebase owner` حرفيًا | **≠** لا كلمة سحرية (ADR-010) |
| uid في القواعد | `sub` | `user_id` | من الرمز المحقق كما في RTDB |

## 8. سطح GCS (Admin SDKs)

| البند | الإنتاج | المحاكي (`admin_*.json`) | التصنيف وقرار Aruma |
|---|---|---|---|
| المصادقة | OAuth `Bearer` إلزامي | لا يقرأ `Authorization` إطلاقًا (D §4.4) | **≠** OAuth إلزامي (حسابات الخدمة الحالية) |
| مسارات البيانات الوصفية | `/storage/v1/b/...` | `/storage/v1/b/...` و`/b/...` | نقبل الصيغتين؛ الثانية لأن عملاء Node في وضع المحاكي يرسلونها (القسم 9) |
| `PATCH` و`rewriteTo` تحت `/storage/v1` | مدعومان | 501 (`admin_gcs_endpoint.json`، `admin_go.json`) | **≠** مدعومان |
| الرفع المتقطع بقطع | `308` مع `Range: bytes=0-N` بين القطع (DOC GCS)، أو 200 مع `X-Http-Status-Code-Override: 308` حين يرسل العميل `X-Guploader-No-308: yes` (Go يرسلها، B) | ينهي الكائن مع أول `PUT` ويرد 200، فيفشل Node (`FILE_NO_UPLOAD`) وGo (`invalid character 'O'`) | **≠** بروتوكول GCS كاملًا، كما في B |
| الشروط المسبقة (`ifGenerationMatch=0` وأخواتها) | 412 `conditionNotMet` (DOC GCS) | تُتجاهل، والكتابة تمت (`admin_node.json`، `admin_go.json`) | **≠** نطبقها |
| `GET /storage/v1/b/<bucket>` (`bucket.exists()`، `Bucket.Attrs`) | مورد الـ bucket | 404 (`admin_node.json`، `admin_go.json`) | **≠** مورد الـ bucket |
| تنزيل Go | صيغة XML API: `GET https://storage.googleapis.com/<bucket>/<object>` (B) | `GET /<bucket>/<object>` مدعوم | **=** يجب أن ندعمه |
| `generation` وأخطاء 404 | JSON GCS بـ `errors[].reason`، و`error.code` رقم يساوي حالة HTTP (Node `exists()` يعتمد عليه، B) | نفسه (`admin_node.json`) | **=** |
| `crc32c` في رد الرفع | base64 (DOC) | base64 (`admin_node.json`) | **=** وإلزامي: Node يحذف الكائن ويرمي `FILE_NO_UPLOAD` إن غاب أو اختلف (B §9) |
| `crc32c` المرسل من Go في multipart | يُتحقق منه ويُرفض الاختلاف (DOC، B) | يُتجاهل | **≠** نتحقق |
| أرقام JSON (`generation`، `size`، ...) | نصوص | نصوص | **=** وإلزامي: وسوم `,string` في Go ترفض الأرقام (B) |
| رمز التنزيل من Admin (`getDownloadURL` في firebase-admin) | يطلب metadata من `firebasestorage.googleapis.com` بـ OAuth (B) | يطلب `/v0` **بلا مصادقة** في وضع المحاكي، فترفضه القواعد (`admin_node.json`) | ؟ في وضع Aruma؛ يُقرَّر مع القسم 9 |

## 9. نتيجة تصميمية: كيف يصل Admin إلى Aruma؟

التسجيلات تكشف قيدًا لم يكن في الخطة:

- **firebase-admin (Node) لا يقبل `apiEndpoint`.** يبني عميل GCS من شهادة حساب الخدمة أو من ADC فقط (`firebase-admin-node src/storage/storage.ts:74-93`). والطريق الوحيد إلى خادم غير Google هو `FIREBASE_STORAGE_EMULATOR_HOST`/`STORAGE_EMULATOR_HOST`، **وفيه لا يُرسل `Authorization` على أي طلب** (`admin_node.json`). وGo مثله مع `STORAGE_EMULATOR_HOST` (`admin_go.json`).
- و`@google-cloud/storage` مباشرة بـ `apiEndpoint` و`useAuthWithCustomEndpoint: true` يرسل `Bearer` على طلبات JSON وعلى multipart، **لكن طلبي بدء الرفع المتقطع وقطعه خرجا بلا `Authorization`** (`admin_gcs_endpoint.json`). السبب خلل في العميل: `startResumableUpload_` لا يمرر الخيار بينما `createResumableUpload` يمرره (B §9).
- Aruma لا يقبل كتابة بلا اعتماد (ADR-010)، فلا يكفي "وضع المحاكي" لـ Admin. الخيارات لـ ADR-022/S4:
  1. **مسار الإنتاج** كما في Auth وRTDB: توجيه `storage.googleapis.com` و`firebasestorage.googleapis.com` إلى Aruma مع بقاء المكتبة في وضعها العادي (شهادة الخدمة وOAuth). يحتاج تجربة S6 لكل لغة.
  2. Go: `firebase.NewApp(ctx, cfg, option.WithEndpoint(...), ...)` مع اعتمادات حساب الخدمة: firebase-admin-go يمرر الخيارات إلى **كل** الخدمات (B §9)، فيصل `Bearer` إلى Aruma على كل الطلبات ومنها قطع الرفع. لكن `WithEndpoint` واحد لكل الخدمات، فيحتاج تجربة مع Auth وRTDB في التطبيق نفسه.
  3. Node: `@google-cloud/storage` مباشرة بـ `apiEndpoint` و`useAuthWithCustomEndpoint`. وغياب المصادقة عن بدء الرفع المتقطع لا يُحل بقبول جلسة بدأها طلب بلا `Authorization` (تساهل مرفوض)، بل بـ `resumable: false` (multipart يحمل `Authorization`) أو بإصلاح عند العميل.
  - قبول GCS بلا مصادقة من شبكة "موثوقة" **مرفوض** أيضًا.
- هذه المسألة تُحسم في ADR-022 قبل S4.

## 10. ما يحسمه تسجيل الإنتاج حين يتوفر bucket

الأداة جاهزة إلا من نقطة واحدة: `capture/capture.mjs` يسجّل عبر وكيل، والـ SDK لا يمر بالوكيل في وضع الإنتاج. يلزم مسجّل يلف `fetch` في Node (README). والبنود المفتوحة، بالأولوية:

1. **إزاحة خاطئة ومكررة في الرفع المتقطع، وحجم لا يطابق المعلن** (3.2).
2. **هل تُقيَّم القواعد عند `start`؟** (3.2).
3. **`crc32c` في صيغة `/v0`** (4).
4. **`metadata: null` في PATCH**، ورد iOS الحقيقي (4، وA §8.1).
5. **PATCH من العميل لـ `firebaseStorageDownloadTokens`**، والرمز على رابط الـ metadata بلا `alt=media`، والإنشاء التلقائي للرمز عند القراءة (5).
6. **نص رسالة 403** وجسم إنهاء مكرر ناجح (1، 3.1).
7. **CORS على ردود النجاح والـ preflight على bucket موجود** (2).
8. **القوائم:** `pageToken` غير صالح، و`maxResults` أكبر من 1000، و`prefixes` عبر الصفحات، والبادئة بلا `/` (6).
9. `Bearer <idToken>` على `/v0` (7).

شروط التسجيل (الـ brief، القاعدة 8): قواعد `capture/emulator/storage.rules` تُضاف ككتلة `aruma-ref` بجانب قواعد المشروع، بعد حفظ القواعد الحالية، وتُستعاد بايتًا ببايت بعد الجولة، والبيانات تحت `aruma-ref/<run-id>/` وحدها وتُحذف ويُتحقق من الفراغ.
