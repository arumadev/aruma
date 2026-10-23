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
>
> **قرار المالك بعد هذه الجولة:** لا تسجيلات إنتاج لـ Storage (لا Blaze). أدلة الإنتاج تأتي فقط من الوثائق، واختبارات المطابقة المكتوبة لتعمل على الإنتاج، وملاحظات Pyric على Storage حقيقي، وكود المكتبات.

مصادر "الإنتاج" المتاحة دون bucket، مرتبة بالثقة:

| الرمز | المصدر | ماذا يعطي | الثقة |
|---|---|---|---|
| **P** | تسجيلات إنتاج بلا اعتماد على bucket غير موجود (`production/bucket_probe.json`) | شكل الخطأ، وترويسات CORS وغيرها على أخطاء واجهة العملاء، ورسالة المسار المجهول | مرصود من الإنتاج، لكن لمسارات الخطأ وحدها |
| **PY** | ملاحظات Pyric على Storage حقيقي (github.com/davideast/pyric، commit `756d23efae7e0147db820b7ce9c1672ba2a6d8a6`، Apache-2.0): `packages/conformance/observations/storage-admin/admin-storage-metadata-download-url.json` (2026-09-24، firebase-admin 13.10.0) و`.../storage-rules/stdlib-realstorage-p2-native-object-fields.json` (2026-07-21، bucket `*.appspot.com` حقيقي). نسخ في `.cache/research/src/s0c/pyric-756d23e/` | سلوك الرموز والإبطال والـ metadata على `/v0`، وحقول الكائن الحقيقية | مرصود من الإنتاج على مشروع آخر، ملخصًا (لا أجسام HTTP خام) |
| **DOC** | وثائق Firebase وGCS الرسمية | دلالات القواعد، والرموز، وصيغ GCS | رسمي، بلا تفاصيل السلك |
| **CT** | اختبارات المطابقة في firebase-tools 4e6eb923 (`scripts/storage-emulator-integration/conformance/*.test.ts`)، مكتوبة لتعمل على الإنتاج حين يُضبط `useProductionServers` (القيمة المثبتة `false`، `env.ts:17`)، والحالات التي ليست `emulatorOnly` يُفترض أنها تمر على الإنتاج | حالات وأجسام محددة | ثانوي: لا نعرف متى شُغّلت على الإنتاج آخر مرة. **والحالات المعتمدة على القواعد ليست دليل إنتاج أصلًا**: ملف `storage.rules` الخاص بالاختبارات يُحمَّل في المحاكي فقط ولا يُنشر على مشروع الإنتاج، فتلك الحالات تجري هناك تحت قواعد المشروع نفسه. نسمها **CT-r** وتُعامل كدليل محاكٍ |
| **SDK** | كود المكتبات (A وB) | ما يرسله العميل وما يحتاجه | يقيني لجهة العميل، ولا يقول ما يرد به الإنتاج |
| **EMU** | تسجيلات المحاكي (`emulator/*.json`) وقراءة كوده (D) | كل شيء | المرتبة الثانية |

مفتاح التصنيف في الجداول:
- **=**: الإنتاج والمحاكي متفقان (بدليل مذكور).
- **≠**: يختلفان بدليل. Aruma يتبع الإنتاج.
- **؟**: سلوك الإنتاج غير معروف من المصادر أعلاه. القرار المؤقت لـ Aruma مذكور، ويُوثَّق كفرق محتمل في `docs/compat` (القسم 10).

## 1. واجهة العملاء `/v0`: العناوين والأخطاء

| البند | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| bucket غير موجود | 404 `{"error":{"code":404,"message":"Not Found."}}` للقراءة والقائمة والرفع (P) | ينشئ الـ bucket ضمنيًا، ثم 403 "Permission denied. No READ\|WRITE permission." (`raw_upload.json`) | **≠** 404 بشكل الإنتاج |
| كائن غير موجود | 404 بالجسم نفسه أعلاه (P، على bucket مجهول) | 404 بنص `Not Found` غير JSON (`sdk_read.json`) | **≠** شكل الإنتاج. المكتبات لا تقرأ الجسم (A §5.1)، فالأثر تشخيصي فقط |
| مسار مجهول (`/v0/b/`، `/v0/x`) | 400 `{"error":{"code":400,"message":"Invalid HTTP method/URL pair."}}` (P) | يسقط إلى راوتر GCS فيرد 501 `Not Implemented` (D §1.1) | **≠** 400 بشكل الإنتاج |
| شكل JSON الخطأ | منسّق بمسافتين، `Content-Type: application/json; charset=UTF-8` (P) | سطر واحد | المسافات لا تهم أي عميل. نكتب JSON مضغوطًا |
| رفض القواعد | 403: الـ SDK يعطي `storage/unauthorized` بالرسالة "User does not have permission to access ..." على Storage حقيقي (PY `stdlib-realstorage-p2-native-object-fields.json`، `diagnostics`)، و`unauthorized` لا يأتي إلا من 403 (A §5.2) | 403 `{"error":{"code":403,"message":"Permission denied. No READ permission."}}` | **=** في الحالة. نص جسم الخطأ في الإنتاج **؟** (لا يقرؤه أي SDK). نستعمل نص المحاكي مؤقتًا |
| `X-Content-Type-Options: nosniff` | على كل الردود المرصودة (P) | غائب | **≠** نرسلها دائمًا (وهو قرار ADR-022 أصلًا) |
| `Cache-Control` على ردود JSON | `private, max-age=0` (P) | غائب | نرسل `private, max-age=0` على JSON |

## 2. CORS

| البند | الإنتاج (P) | المحاكي (`raw_upload.json`) | قرار Aruma |
|---|---|---|---|
| `Access-Control-Allow-Origin` | `*`، لكنه **مرصود على ردود 404/400 فقط** لـ bucket غير موجود (P). وعلى تنزيل `alt=media` تحكمه إعدادات CORS للـ bucket: "To download data directly in the browser, you must configure your Cloud Storage bucket for cross-origin access (CORS)." (firebase.google.com/docs/storage/web/download-files) | يعكس الأصل دائمًا | **؟** انظر الإطار أدناه. لا نرسل `Allow-Credentials` في أي حال، فالمصادقة بترويسة لا بكعكة |
| الترويسات المكشوفة على ردود الخطأ العادية | `Content-Range, X-Firebase-Storage-XSRF` | قائمة ثابتة فيها كل `x-goog-upload-*` | ؟ على ردود النجاح. JS يحتاج قراءة `X-Goog-Upload-URL` و`X-Goog-Upload-Status` و`X-Goog-Upload-Size-Received` (A §6) |
| الترويسات المكشوفة على رد رفع (start فاشل) | `Content-Length, Content-Range, Date, Server, Transfer-Encoding, X-Firebase-Storage-XSRF, X-GUploader-UploadID, X-Goog-Upload-Status, X-Google-Trace`، ومعها `X-Goog-Upload-Status: final` | كما سبق | **مرصود جزئيًا**: الإنتاج يغيّر القائمة حسب نوع الطلب. نكشف قائمة الإنتاج + `X-Goog-Upload-URL` و`X-Goog-Upload-Size-Received` على ردود الرفع |
| preflight (`OPTIONS`) | على bucket غير موجود: 404 JSON بلا `Allow-Methods` (P). على bucket موجود ؟ | 204 يعكس الترويسات، والطرق `GET,HEAD,PUT,PATCH,POST,DELETE` | ؟ نرد 204 بالطرق والترويسات التي يرسلها العملاء (A §6.1) |

**إطار القرار لـ ADR-022 (CORS على التنزيل):** الوثيقة أعلاه تعني أن `getBytes`/`getBlob`/`fetch(downloadURL)` من متصفح على أصل آخر **تفشل في الإنتاج** حتى يضبط المالك CORS للـ bucket (حقل `cors` في مورد bucket في GCS، يُضبط بـ `gsutil cors set` أو `buckets.patch`). أما طلبات الـ API الأخرى (metadata، list، الرفع) فلا نعرف سياستها على bucket موجود، والمرصود `*` على الأخطاء فقط. الخياران:
1. **إعداد CORS لكل bucket** يُطبَّق على `alt=media`، بسطح إدارة مقابل: اللوحة، و`buckets.get/patch` بحقل `cors` على سطح GCS. هذا السطح **غير موجود في خطة v0.3** (B §10 لا يذكر `cors`، وC لا يذكر أداة تضبطه)، فيُضاف أو يُؤجَّل صراحة.
2. **السماح لكل الأصول على `alt=media`** مع توثيقه فرقًا في `docs/compat`: أكثر تساهلًا من bucket بلا إعداد (يقرأ المتصفح ما لا يقرؤه في الإنتاج)، لكن الحماية الفعلية هي القواعد ورمز التنزيل لا CORS، والرابط برمز يُفتح أصلًا بلا CORS خارج المتصفح.

التوصية للـ ADR: الخيار 1 بإعداد افتراضي يطابق bucket Firebase الجديد (بلا CORS على التنزيل)، مع إعداد في اللوحة. وإن أُجّل السطح، فالخيار 2 مع التوثيق.

## 3. الرفع

### 3.1 الأنواع والأوامر

| البند | الإنتاج | المحاكي | التصنيف |
|---|---|---|---|
| رفع بلا `X-Goog-Upload-Protocol` = media خام بـ `?name=` | نعم (CT `should default to media upload`) | نعم (`raw_upload.json`) | **=** |
| `X-Goog-Upload-Command` بلا بروتوكول = resumable | نعم (CT `without upload protocol set`) | نعم | **=** |
| أوامر الجلسة بعد `start` بلا `Authorization`، وبـ `PUT` | مقبولة (CT `should accept subsequent resumable upload commands without an auth header`) | مقبولة | **=** ومطلوب لـ iOS (A §1) |
| `finalize` منفردًا بجسم فارغ | مقبول (CT نفسه) | مقبول | **=** |
| إنهاء مكرر بعد نجاح | 200 (CT) | 200 بجسم `OK` بلا metadata (D §2.2) | **=** في الحالة. الجسم في الإنتاج ؟. نعيد الـ metadata (iOS يقرأ الجسم، A §1) |
| إنهاء مكرر بعد 403 | 403 مرة أخرى مع `final` (CT-r: يعتمد على قاعدة `disallowSize0` التي لا تُنشر على الإنتاج، فهو دليل محاكٍ) | نفسه | **؟** نتبع المحاكي |
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
| تقييم القواعد في الرفع المتقطع | ؟ (CT-r `should return 403 when resumable upload is unauthenticated`: `start` 200 والإنهاء 403 بقاعدة `request.resource.size != 0`، لكن القاعدة لا تُنشر على الإنتاج، فهو دليل محاكٍ) | عند الإنهاء فقط، بهوية `start` | الإنهاء هو الحكم. لا نرفض عند `start` بمعلومات ناقصة (الحجم غائب في Android). نحد من الإساءة بحدود الجلسات والحجم |

### 3.3 القواعد على الرفع

- **الكتابة فوق كائن موجود تُقيَّم `create`: الوثائق والمحاكي متفقان، والإنتاج نفسه لم يُرصد.**
  - DOC: في مثال القواعد الرسمي، `create` "Applies to writes to file contents"، و`update` "Applies to updates to (pre-existing) file metadata" (firebase.google.com/docs/storage/security/core-syntax، قُرئت 2026-10-23).
  - EMU: `guarded: overwrite a locked object` نجح مع رفض شرط `update` (`rules.json`)، والمصدر يقيّم كل رفع بـ `create` (`FT files.ts:320-330`).
  - CT-r فقط، لا دليل إنتاج: `should upload replace existing file` (القاعدة `allow read, create`) و`allowIfNoExistingFile` (`allow create: if resource == null` يرفض الكتابة الثانية، أي أن `resource` في `create` هو الكائن الموجود). القاعدتان من `scripts/storage-emulator-integration/storage.rules` الذي لا يُنشر على الإنتاج، و`useProductionServers` مثبت على `false` (`conformance/env.ts:17`).
  - PY لا يحسمها: ملاحظات Pyric على الأفعال (`rules-storage-verbs-umbrella-granular.json`) من Rules Test API، تقيّم الفعل الذي يحدده الطلب، ولا تقول أي فعل يطلقه رفع فوق كائن موجود.
  - **أثر على محرك القواعد (S1/S2):** نعتمد `create` لكل رفع و`update` لـ PATCH البيانات الوصفية وحده، ونوثّق أن الدليل وثائق + محاكٍ، والبند مفتوح في القسم 10.
- **التحقق من الصلاحية قبل الوجود (= على الأرجح):** في المحاكي، كائن غير موجود تحت قاعدة رافضة يعطي 403 لا 404 (`rules.json`). الإنتاج ؟، لكن القاعدة تُقيَّم على المسار و`resource` = `null`، فالنتيجة نفسها متوقعة. نتبع المحاكي، فهو الأقل كشفًا للمعلومات.

## 4. البيانات الوصفية

| الحقل | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| الحقول الموجودة بعد رفع خام | `name, bucket, generation, metageneration, timeCreated, updated, storageClass, size, md5Hash, contentEncoding, contentDisposition, crc32c, etag, downloadTokens` كلها، و`metageneration="1"`، و`size` نص، و`contentEncoding="identity"` (CT `should set default metadata`) | نفسها (`sdk_upload.json`) | **=** |
| `contentType` الافتراضي | `application/octet-stream` (CT) | نفسه | **=** |
| `generation` | نص بالميكروثانية، مثل `"1784647629568292"` (PY `serverFields`)، ويتقدم مع كل كتابة للبايتات ويثبت مع PATCH، بينما `metageneration` و`updated` يتقدمان مع PATCH (PY `metadata-update-relations`، `byte-overwrite-*-relations`) | `Date.now()` بالميلي ثانية | **≠** الميكروثانية. المكتبات لا تفترض طولًا |
| `crc32c` في صيغة `/v0` | نص (CT)، وصيغته ؟ | عدد عشري نصًا (`"338055207"`) | **؟** نستعمل base64 لأربعة بايتات big-endian كما في GCS، فلا عميل `/v0` يقرأه (A §4). يُحسم بتسجيل |
| `crc32c` في GCS | base64 (DOC) | base64 (`admin_node.json`) | **=** |
| `etag` | يتغير مع كل تغيير في المحتوى أو الـ metadata (DOC GCS) | sha1 ثابت بعد الإنشاء، لا يتغير مع PATCH (D §3.1) | **≠** يتغير مع `metageneration` |
| `contentDisposition` الافتراضي | نص موجود في الـ metadata (CT). وتنزيل كائن رُفع عبر GCS بلا قيمة يأتي بـ `Content-Disposition: attachment; filename*=testFile` (CT `serves the right content`، `firebase-js-sdk.test.ts:464-490`) | `inline` في الـ metadata ويُحفظ في الكائن؛ `attachment` عند التنزيل حين لا قيمة (D §3.3) | ؟ في قيمة الـ metadata. نعيد `inline` ولا نحفظه، ونفرض نحن `Content-Disposition` آمنًا عند التنزيل (ADR-022) |
| التواريخ | `2026-07-21T15:27:09.639Z` (PY `serverFields`) | `2026-10-23T20:44:29.104Z` | **=** وصيغة إلزامية للعملاء: ثلاث خانات و`Z` (A §4) |
| PATCH: `null` يحذف الحقل، و`metadata.{k}: null` يحذف المفتاح | (CT `shoud allow deletion of settable metadata fields by setting to null`، و`...custom metadata by setting to null`) | نفسه (`sdk_metadata.json`) | **=** |
| PATCH: حقول للقراءة فقط أو مجهولة | ؟ | تُتجاهل، و`metageneration` يرتفع (`sdk_metadata.json`) | نتجاهلها، ونتجاهل `null` لها خصوصًا (iOS يرسل `null` لـ `downloadTokens` و`etag`، A §1) |
| PATCH: `metadata: null` | ؟ | يمحو كل الـ metadata المخصّصة | **؟** حرج لـ iOS (A §8.1). مؤقتًا نتبع المحاكي ودلالة GCS، ونسجّل طلب iOS حقيقيًا في S6 |
| ترويسات التنزيل | ؟ | `x-goog-hash`، و`x-goog-generation`، و`x-goog-metadatageneration` (الإنتاج في GCS: `x-goog-metageneration`)، و`content-encoding` و`cache-control` فارغة حين لا قيمة | لا نرسل ترويسة فارغة. نتبع أسماء GCS. ونضيف ترويسات الحماية (ADR-022) |
| `Range` | 206 مع `Content-Range: bytes 2-5/64` على رابط الرمز (PY `admin-storage-metadata-download-url.json`، `rangeGet`) | 206 لنطاق واحد، والنطاق غير المرضي يعطي 200 كاملًا (D §3.3) | **=** في 206. Android يحتاج 206 للاستئناف (A §1) |
| `Range` على كائن فارغ | ؟ | 200 بجسم فارغ (D §3.3) | **200 بالجسم الكامل (الفارغ)، لا 416.** JS يرسل دائمًا `Range: bytes=0-N` مع `maxDownloadSizeBytes` ويقبل `[200, 206]` فقط (`JS implementation/requests.ts:227-230`)، والنطاق غير قابل للإرضاء على كائن طوله 0 (RFC 9110 §14.1.1: يُرضى النطاق إن كان `first-pos` أقل من الطول)، فـ 416 يكسر `getBytes(ref, n)` لكل ملف فارغ. والخادم "MAY ignore the Range header field" (RFC 9110 §14.2) فيرد 200. لغير الفارغ: 416 حين يبدأ النطاق بعد آخر بايت (Android يلحق جسم 200 بلا فحص، A §1.11) |
| كائن مخزّن بـ `Content-Encoding: gzip` على `/v0` `alt=media` | بلا `Accept-Encoding: gzip`: يُفك الضغط ويُرسل بلا `Content-Encoding` ولا `Content-Length`؛ معها: يُرسل مضغوطًا مع `Content-Encoding: gzip` (CT `gzip`، `firebase.endpoints.test.ts:481-550`، ليست `emulatorOnly` ولا تعتمد على القواعد). وهو decompressive transcoding في GCS: "the response containing the object does not contain a Content-Encoding or Content-Length header"، و`Range` "is silently ignored" حين يحدث (cloud.google.com/storage/docs/transcoding) | نفسه (D §3.3) | **=** ونطبّقه على `/v0` وسطح GCS معًا. **هذا يصحح توصية B §6.3** ("أرسل البايتات كما خُزّنت" دائمًا)، انظر التصحيح هناك. ومع التحويل يُتجاهل `Range` فيُرد 200 كاملًا: Android يلحقه بلا فحص 206 (A §1.11)، وهو سلوك الإنتاج أيضًا، فنوثّقه ولا نغيّره |
| `Cache-Control` المخزّن على تنزيل مصادق | ؟ (INFERENCE: الحقل في GCS وُجد ليُرسل ترويسةً مع الكائن، ولم نرصده على `/v0`) | يُرسل كما خُزّن (`sdk_read.json`) | **قرار لـ ADR-022.** `cacheControl` يضبطه العميل نفسه (`updateMetadata`)، فقيمة `public` على رد طلبٍ يحمل `Authorization` تسمح للمخابئ المشتركة بخزن كائن خاص وتقديمه لغير صاحبه (RFC 9111 §3.5: لا يُخزَّن رد طلب فيه `Authorization` إلا مع `public` أو `s-maxage` أو `must-revalidate`). الاقتراح: على كل تنزيل قررت فيه القواعد (طلب فيه `Authorization`)، يُستبدل `public` بـ `private` ويُحذف `s-maxage`. وعلى رابط الرمز (`?token=`) يُرسل كما خُزّن، فالرابط نفسه هو الصلاحية |

## 5. رموز التنزيل

| البند | الإنتاج | المحاكي | التصنيف وقرار Aruma |
|---|---|---|---|
| إنشاء رمز مع رفع `/v0` | نعم (CT: `downloadTokens` موجود بعد رفع خام) | نعم | **=** |
| رفع عبر GCS (Admin) | لا رمز بعد الحفظ (PY `afterSave.hasDownloadTokens: false`؛ CT `firebase.endpoints.test.ts:640-687`: بعد `testBucket.upload()` يعطي `create_token` رمزًا واحدًا بالضبط) | لا رمز (D §3.4) | **=** |
| **GET البيانات الوصفية المصرّح به على `/v0` لكائن بلا رموز** | **يُنشئ رمزًا ويحفظه**: PY `getDownloadURLWithoutToken`: `mintsTokenIntoMetadata: true`، `urlCarriesMintedToken: true`، `mintedTokenIsUuid: true` (firebase-admin `getDownloadURL` = `GET /v0/b/<b>/o/<o>` فقط، B §1.10)؛ وCT `returns url pointing to the expected host` (`firebase-js-sdk.test.ts:451-462`، `it` عادية): كائن رُفع عبر GCS بلا رموز، ثم `getDownloadURL()` من عميل الويب يعيد رابطًا فيه `?alt=media&token=` | يُنشئ رمزًا (`FT apis/firebase.ts:139-141`) ويرفع `metageneration` | **=** وإلزامي: بدونه يفشل `getDownloadURL` لكل ملف رفعه Admin (JS `storage/no-download-url`، A §1.14). يحدث بعد نجاح فحص القواعد فقط (طلب غير مصرّح به لا يكتب شيئًا)، ويُكتب ذريًا مع رفع `metageneration` |
| الرمز يفتح الملف دون القواعد | نعم: GET بلا مصادقة بالرمز 200 (PY `unauthenticatedGet.status: 200`)، و DOC: رابط التنزيل "publicly accessible" | نعم، ولأي هوية أو بلا هوية (`rules.json`) | **=** |
| شكل رابط التنزيل | `https://firebasestorage.googleapis.com/v0/b/<bucket>/o/<encoded path>?alt=media&token=<uuid>`، والمفتاحان `alt` و`token` فقط (PY `downloadURL`) | نفسه على مضيف المحاكي | **=** |
| الرمز على رابط البيانات الوصفية بلا `alt=media` | ؟ | يعيد الـ metadata بلا مصادقة | **؟** نقصره على `alt=media` (أضيق)، ونوثّق الفرق المحتمل |
| مكان الرموز في رد `/v0` | `downloadTokens` حقل مستقل، و`metadata` **لا يحتوي** المفتاح `firebaseStorageDownloadTokens` (PY `firebaseMetadataEndpoint.metadataHasTokenKey: false`، و`customMetadataKeys: ["note"]` مع أن الرمز مخزّن في custom metadata على GCS، `afterSet.firebaseStorageDownloadTokensEqualsSet: true`) | نفسه: الرموز خارج `customMetadata` (D §3.2) | **=** ونخفيه من `metadata` في كل ردود `/v0` |
| `create_token`/`delete_token` | بـ OAuth المالك (`Authorization: Bearer <access token>`، كما في CT)، و403 لغيره (CT)، و400 لقيمة غير `true` (CT) | بـ `Bearer owner`، و403 "Missing admin credentials." للمستخدم (`tokens.json`) | **=** في الدلالة. Aruma: لمسؤول اللوحة أو حساب خدمة فقط |
| حذف آخر رمز | يُنشأ رمز جديد فورًا (CT `should regenerate a new token if the last remaining one is deleted`) | نفسه | **=** |
| **رموز يرسلها العميل** (`metadata.firebaseStorageDownloadTokens` في multipart أو في جسم `start` أو في PATCH) | ؟ | تُقبل: الرفع يحفظ الرمز المرسل بدل توليد واحد (D §2.2، `FT apis/firebase.ts:209-216`)، وPATCH **يضيفه** اتحادًا ويعمل فورًا (`tokens.json`) | **؟ وقاعدة أمنية واحدة لـ Aruma:** على `/v0` يُحذف المفتاح `firebaseStorageDownloadTokens` من الـ metadata المخصّصة الواردة **قبل** تقييم القواعد وقبل الحفظ، في multipart و`start` وPATCH معًا؛ والرمز عند الإنشاء يولّده الخادم دائمًا. فالعميل لا يستطيع زرع رمز يعرفه، ولا تراه القواعد في `request.resource.metadata` ولا في `resource.metadata` (في المحاكي لا تراه أيضًا، D §4.2؛ وفي الإنتاج ؟). Admin عبر GCS يضبطه كما في GCS |
| إبطال رمز من Admin (حذف المفتاح أو استبداله في custom metadata) | الرابط القديم 403، و`getDownloadURL` بعدها يُنشئ رمزًا جديدًا يعمل 200 (PY `revocation`: `tokenRemoved`، `revokedUrlStatus: 403`، `mintsNewToken: true`، `newUrlStatus: 200`). و`setMetadata({metadata:{note:null}})` يحذف `note` ويُبقي الرمز (PY `nullRemovesKey`) | اتحاد، فلا إبطال بتعديل الـ metadata (D §3.4) | **≠** الاستبدال والحذف يُبطلان، كما في الإنتاج |
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
| قاعدة `list` | "Applies to list and listAll requests" (DOC core-syntax)، وتُقيَّم على البادئة | نفسه، و`get` مسموح مع `list` مرفوض يعمل (`rules.json`) | **=** |
| `nextPageToken` بقيمة JSON `null` | ؟ | لا يُرسل | **ممنوع**: Android يحوّل `null` إلى النص `"null"` ويرسل `pageToken=null` (A §1.8، §7) |

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
| رمز التنزيل من Admin (`getDownloadURL` في firebase-admin) | يطلب metadata من `firebasestorage.googleapis.com` بـ OAuth (B §1.10)، ويعمل لملف بلا رمز لأن الخادم يُنشئ الرمز (PY، القسم 5) | يطلب `/v0` **بلا مصادقة** في وضع المحاكي، فترفضه القواعد (`admin_node.json`) | **≠** في وضع المحاكي. يُقرَّر مع القسم 9 |

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

## 10. البنود المفتوحة

**قرار المالك: لا تسجيلات إنتاج لـ Storage.** فهذه البنود تبقى مفتوحة إلا إن حسمها دليل من المصادر المسموحة (الوثائق، وCT غير المعتمدة على القواعد، وملاحظات Pyric، وكود المكتبات). وما لا يُحسم يُبنى بالقرار المؤقت المذكور ويُوثَّق فرقًا محتملًا في `docs/compat`. ملاحظات Pyric الأخرى في `packages/conformance/observations/storage-rules/` (مثل `rules-storage-upload-primitives-boundaries.json`) لم تُقرأ كلها، وهي أول مكان يُبحث فيه.

1. **إزاحة خاطئة ومكررة في الرفع المتقطع، وحجم لا يطابق المعلن** (3.2).
2. **هل تُقيَّم القواعد عند `start`؟** (3.2).
3. **الكتابة فوق كائن موجود: `create` أم `update`؟** الدليل اليوم وثائق + محاكٍ فقط (3.3).
4. **`crc32c` في صيغة `/v0`** (4).
5. **`metadata: null` في PATCH**، ورد iOS الحقيقي (4، وA §8.1).
6. **رموز يرسلها العميل** والرمز على رابط الـ metadata بلا `alt=media` (5). والقرار المؤقت أضيق من المحاكي.
7. **نص رسالة 403** وجسم إنهاء مكرر ناجح (1، 3.1).
8. **CORS على ردود النجاح والـ preflight على bucket موجود**، وقرار إعداد CORS لكل bucket (2).
9. **القوائم:** `pageToken` غير صالح، و`maxResults` أكبر من 1000، و`prefixes` عبر الصفحات، والبادئة بلا `/` (6).
10. `Bearer <idToken>` على `/v0` (7).
11. `Cache-Control` على التنزيل المصادق (4، قرار ADR-022).

أداة التسجيل (`capture/capture.mjs`) تبقى للمحاكي ولـ Aruma نفسه. ولو تغير القرار يومًا، يلزمها مسجّل يلف `fetch` لأن الـ SDK لا يمر بالوكيل في وضع الإنتاج (README)، وتسري شروط القاعدة 8 في الـ brief.
