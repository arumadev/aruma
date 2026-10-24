# توافق Cloud Storage for Firebase: الإصدار 0.3

- **الحالة:** بدأه S2-B وS2-C وS2-E كلٌّ على فرعه (2026-10-24)، وجمعه دمج S2 في وثيقة واحدة بقسم لكل مجال. يملؤه S3-D بنتائج `test/compat/storage` (مجموعة Storage في firebase-js-sdk وإعادة تسجيلات المحاكي)، وS7 بأرقامه (storage-layout.md).
- **المرجع السلوكي:** الإنتاج حيث رُصد (`test/oracle/storage/production`، وملاحظات Pyric على Storage حقيقي)، ثم المحاكي الرسمي من firebase-tools 15.34.0 (`test/oracle/storage/emulator`)، ثم مجموعات Google.
- **التصنيف:** الفروق كلها في ADR-023 §3 بأرقامها، كل منها بمصدره وتصنيفه: **قيد** (أشد من Firebase عمدًا)، أو **مؤقت** (بلا دليل إنتاج)، أو **أصح من المحاكي**. وكل قرار مؤقت موضع واحد في الكود، وكل صف أو بند هنا يحمل رقم بنده في ADR-023 §3.

## اختبارات Firebase الرسمية

(S3-D)

## الفروق

القائمة الكاملة في ADR-023 §3: البنود 1 إلى 37 من S0، وما بعدها أضافته مهام S2. ولكل مجال قسم واحد أدناه بالبنود التي تمسه وموضعها في الكود.

### صيغ السلك (`internal/services/storage/wire`)

تختبرها `golden_test.go` وأخواتها على كل تبادل مسجّل: كل مورد `/v0` يُفك في نوعه بلا حقل زائد ويُرمَّز بالبايتات نفسها، وكل مورد GCS بالـ JSON نفسه، وكل مسار وأمر و`Content-Range` و`Range` وجسم multipart وhash مسجّل يُحلَّل إلى ما رد به الخادم.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 16 | `crc32c` في `/v0` بـ base64 لأربعة بايتات، والمحاكي يكتبه عددًا عشريًا (`TestGoldenUploadChecksums` يتحقق أن الرقم واحد) | مؤقت | D §2.2، A §4 | `wire.CRC32C` |
| 36 | `etag` من `(generation, metageneration)` بشكل GCS (varint، مثل `CAEQAQ==`)، ويتغير مع PATCH؛ وetag الـ bucket من الـ metageneration (`CBE=` لـ 17، كالإنتاج) | أصح من المحاكي | B §7، `bucket_probe.json` | `wire.ETag`، `wire.BucketETag` |
| 29 | ترويسة `ETag` على الوسائط من `generation` وحده، وسم قوي بين علامتي تنصيص | مؤقت | ADR-022 §12 | `wire.MediaETag` |
| 38 | `Range` غير صحيح الصيغة يُتجاهل فيُرد 200، و`bytes=-0` 416 | مؤقت | RFC 9110 §14.2 | `wire.ParseRange` |
| 39 | `multipart/related` جزءان بالضبط، أولهما `application/json`، بلا ترميز نقل؛ و`X-Goog-Upload-Command` بأحرف صغيرة؛ و`Content-Range` بأشكاله الستة ومعها `bytes N-(N-1)/N`: غيرها 400. و`bytes N-(N-1)/N` (`bytes 0--1/0` لملف فارغ) جزء أخير فارغ مثل `bytes */N`، ولا يُقبل غيره مما نهايته قبل بدايته (`bytes 5-3/5`، و`N-(N-1)/M` بـ M≠N) | قيد؛ وشكل Node الأخير مؤقت: رد GCS في الإنتاج عليه غير مقيس | التسجيلات كلها داخلها؛ و`@google-cloud/storage` 8.4.0 `resumable-upload.js:619-623`، أُعيد إنتاجه أمام مسجّل في مراجعة S2-B | `wire.NewRelatedReader`، `wire.ParseUploadCommand`، `wire.ParseContentRange` |
| 40 | الـ metadata المخصّصة الفارغة تُحذف من رد `/v0` (المحاكي: `"metadata":{}`) | مؤقت | A §4 | `wire.FirebaseObject` |

- **الأوقات** تُقص إلى الميلي ثانية ولا تُقرَّب، فلا يُكتب وقت بعد وقوعه. وكل وقت مسجّل من الإنتاج والمحاكي يعود بـ `wire.Time` كما هو.
- **أجسام الأخطاء** مضغوطة، والإنتاج يكتبها بمسافات بادئة؛ لا مكتبة تقرأ المسافات، ولا مكتبة `/v0` تقرأ الجسم أصلًا (A §5.1).

### خدمة البايتات (`internal/services/storage/media`)

تختبرها `headers_test.go` (كل نوع على كل وضع)، و`fetch_test.go` (جدول `Sec-Fetch-Site` × `Sec-Fetch-Dest` كاملًا)، و`range_test.go`، و`gunzip_test.go` (قنبلة 1 GiB بذاكرة ثابتة)، و`http_test.go` (خادم حقيقي: الهاشان كما يحللهما Go وNode، والقطع، ومهلة الكتابة). مصفوفة المتصفحات في S3-C.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 1 | كل الأنواع تحت `sandbox`، وPDF `attachment` قسرًا | قيد | ADR-022 §10.2 | `media.setHeaders`، `media.disposition` |
| 2 | 403 لطلبات العمّال (`Service-Worker`، و`Sec-Fetch-Dest` worker وsharedworker وserviceworker) من أي موقع، ولوجهات السكربت والأسلوب والـ worklets و`xslt` من `same-origin`. **وعلى HTTP العادي لا تُرسل `Sec-Fetch-*` فلا رفض**: يحمي هناك CSP اللوحة المضيَّقة (S5-C)، وتحذير عند الإقلاع | قيد | ADR-022 §10.1، Fetch Metadata §3 | `media.refused` |
| 21 | `Cache-Control` الافتراضي `private, max-age=0` على رابط الرمز | مؤقت | ADR-022 §10.5 | `media.cacheControl` |
| 28 | بلا `Content-Disposition` مخزّنة: `attachment; filename*=utf-8''<آخر مقطع>`، بهروب القيمة المخزّنة الافتراضية نفسه | مؤقت | CT، PY-RR | `internal/platform/disposition` (`Filename`)، و`media.replyDisposition` |
| 29 | `If-Range` يقارن بـ `ETag` المحتوى (من `generation`) مقارنة قوية | مؤقت | ADR-022 §12 | `media.ifRangeHolds` |
| 41 | `Cache-Control` على GCS بلا قيمة: `private, max-age=0` | مؤقت | GCS لكائن غير عام | `media.cacheControl` |
| 42 | تنزيل القواعد يحمل `private` دائمًا؛ و`Content-Type` فيه فاصلة أو غير صالح يُخدم `octet-stream` | قيد | RFC 9111 §3.5، Fetch «extract a MIME type» | `media.cacheControl`، `media.contentType` |
| 43 | 304 قبل `Range` (فلا 416 لنسخة مخبأة)، و`If-Range` بتاريخ 200، وجسم 416، وgzip غير صالح يُخدم كما خُزّن | مؤقت | RFC 9110 §13 | `media.notModified`، `media.writeError`، `media.gzipHeaderOK` |

- **فك الضغط:** بلا `Accept-Encoding` يقبل gzip (و`gzip;q=0` صريحًا يغلب `*`) يُرسل المفكوك بلا `Content-Encoding` ولا `Content-Length`، ويُتجاهل `Range`، وتبقى `x-goog-stored-content-encoding: gzip` وهاشا المخزّن، فلا يتحقق Go (`uncompressedByServer`) ولا Node (`safeToValidate`) من هاش على غير ما خُزّن. و`no-transform` يمنعه. والمخرج محدود بـ `object_bytes`، وتجاوزه `media.ErrDecompressedTooLarge`.
- **القطع:** كل فشل بعد سطر الحالة يقطعه `Serve` نفسه: يبلّغ `Limits.Cut` (للمقاييس) ثم `panic(http.ErrAbortHandler)`، فيرى العميل اتصالًا مقطوعًا لا جسمًا قصيرًا مختومًا، ولو عاد المعالج عاديًا (`TestCutIsSeenByTheClient`). ولا يعيد خطأ إلا قبل أول بايت.

### الكائنات والقوائم والرموز وCORS (S2-C)

44. **مؤقت: CORS يطابق الأصل حرفيًا أو `*`، والطريقة حرفيًا أو `*`.** و`*` في الطرق أي طريقة، كما تقول وثيقة GCS (`json_api/v1/buckets`، `cors[].method[]`: «'*' is permitted in the list of methods, and means 'any method'»). ولا أنماط داخل الأصل (`https://*.example.com`)، ولا طي لحالة الأحرف: لم نجد دليلًا على ما يقبله GCS غير ذلك، فلا نكون أوسع منه. وأول قاعدة تطابق تقرر. `core/cors.go` (`MatchCORS`).
45. **مؤقت: `pageToken` مربوط بالقائمة كلها:** المشروع والـ bucket والبادئة والفاصل و`startOffset` و`endOffset` والسطح، لا الخمسة الأولى وحدها (ADR-022 §13). فرمز صفحة على قائمة بإزاحة أخرى أو من سطح آخر 400. وحجم الصفحة يتغير بين الصفحات بلا رفض. `core/pagetoken.go`.
46. **مؤقت: `delete_token` لرمز ليس للكائن يرفع الـ metageneration وحده** ولا يُرد بخطأ، كما يفعل المحاكي (`tokens.json`). `core/tokens.go`.
47. **قيد: الرموز تُحسب في `metadata_bytes`** (8 KiB) كما يحملها GCS في الـ metadata المخصّصة تحت `firebaseStorageDownloadTokens`. فـ `create_token` يُرفض 400 حين تتجاوز الرموز والـ metadata معًا الحد (قرابة مئتي رمز لكائن بلا metadata). `core/metadata.go` (`metadataSize`).
48. **مؤقت: `"metadata": null` على GCS يمسح الرموز أيضًا**، لأنها في الـ metadata المخصّصة هناك (دلالة GCS)، فيُبطل الرابط القديم، والقراءة التالية المصرّح بها على `/v0` تولّد رمزًا. وعلى `/v0` يمسح الـ metadata المخصّصة وحدها (البند 17). `core/metadata.go` (`surfacePatch`).
49. **قيد: مفاتيح الـ metadata المخصّصة غير فارغة وبلا محارف تحكم، ولا NUL في قيمها** (jsonb لا يحفظه)، و`DEL` مرفوض مع ما دون U+0020 في الحقول الخمسة التي تصير ترويسات (ومنها TAB). القيم تقبل السطر الجديد. `core/metadata.go` (`checkPatch`).
50. **مؤقت: البادئة بمقطع فارغ على `/v0`:** تُحذف البادئة التي فيها مقطع فارغ قبل الفاصل الأخير (`listAll//`)، وتبقى `listAll/bar/` من `listAll/bar//` (CT `should not include show invalid prefixes and items`). وبادئة طلب تبدأ بـ `/` أو فيها `//` قائمة فارغة. **وما يُحذف يُحسب على الصفحة** كما لو أُعيد، فالصفحة لا تفحص أكثر من `maxResults` مدخلًا ولا تتجاوز `maxResults+1` بحثًا، وقد تأتي ناقصة أو فارغة ومعها `nextPageToken` (والمكتبات تتابع الرمز). `core/names.go` (`prefixHasEmptySegment`)، و`core/list.go`.

### الرفع والكتابة والحياة (S2-D)

51. **مؤقت: الشروط المسبقة على كائن غير موجود.** `ifGenerationMatch=0` وحده ينجح؛ و`ifGenerationMatch=N` و`ifGenerationNotMatch` و`ifMetagenerationMatch` و`ifMetagenerationNotMatch` كلها 412. وثائق GCS لـ `ifGenerationNotMatch`: «If no live object exists, the precondition fails»، والبقية بالقياس (الأشد). `core/commit.go` (`checkRowPreconditions`، و`writeObjectRow` لا يُدرج حين يحتاج الشرط كائنًا).
52. **مؤقت: الرموز لا تُورث عبر الأجيال.** كل إنشاء عبر `/v0` واللوحة يولّد رمزًا جديدًا ولو كتب فوق كائن له رموز، والكتابة عبر GCS بلا رمز إلا ما يضبطه Admin، والإنشاء بعد حذف لا يحمل رمز المحذوف. الأساس ADR-022 §9 («رمز جديد دائمًا» عند الإنشاء عبر `/v0`، وPyric `afterSave.hasDownloadTokens: false` لـ GCS) ودلالة GCS (generation جديد بـ metadata الرفع وحده)؛ ولا تسجيل إنتاج للكتابة فوق موجود. فرابط تنزيل قديم يُبطل بالكتابة فوقه. `core/insert.go` (`incomingObject`)، و`ReplaceObject` يكتب رموز الجيل الجديد وحدها. (`TestTokensDoNotCarryOver`)
53. **مؤقت: الحجم المخالف أثناء الرفع.** قطعة تتجاوز الحجم المعلن عند `start` أو الكلي في `Content-Range` أو `X-Goog-Upload-Content-Length` تجعل الجلسة `final` بالحجم المخالف (400)، لا 413؛ وتجاوز `object_bytes` 413 و`final`. وبايت بعد إنهاء نقل ملفه (ثم فشل عابر) حجم مخالف أيضًا. وجسم أقصر من `Content-Length` يحفظ ما وصل ويرد 400 مع `active`، فيسأل العميل بـ `query`. `core/uploads.go` (`WriteUpload`)، و`core/insert.go` (`stream`).
54. **قيد: حجز جسم مجهول الطول بخطوات.** يحجز 8 MiB قبل كل خطوة (ADR-022 §6.5)، وتُنصَّف الخطوة حتى 64 KiB قبل الرفض، ويُرد الفائض عند نهاية الطلب. فقرب الحصة وتحت التزامن قد يُرفض (402 و`final`) رفع بلا طول معلن كان سيتسع، ولا يُتجاوز الحد أبدًا. والطول المعلن يُحجز كله مرة واحدة فلا يتأثر. `core/insert.go` (`stream`)، و`TestQuotaUnderConcurrency`.
55. **مؤقت: الشرط المسبق الفاشل عند إنهاء جلسة GCS نهائي** (قرار المسؤول في مراجعة S2-D). يُحفظ 412 ويُعاد على الإنهاء المكرر و`query`، ويُحرَّر ملف staging أو المفتاح المنقول وحجزهما فورًا، بدل أن تبقى الجلسة `active` تحجز بايتاتها سبعة أيام. ولا يصل `/v0` واللوحة إليه أصلًا (`checkPre`). لا تسجيل لرد GCS هنا. `core/uploads.go` (`sessionFinal`، `decodeSessionError`).
56. **قيد: حدود الجلسات تصمد أمام الجلسات الفارغة.** عند `start` تُحسب كل جلسات العنوان (لكل /64 ولكل مشروع) وكل جلسات الـ `uid`، فارغها أيضًا، فلا يصنع عميل واحد صفوفًا وملفات بلا حد؛ وحد المشروع يعد الجلسات التي فيها بايت. وعند أول بايت متين تُعد الحدود الثلاثة من جديد على الجلسات الحية، تحت قفل استشاري للمشروع في معاملة تسجيل البايت (وفي معاملة `start` كذلك)، فلا يمر اثنان من عدّ واحد؛ وتجاوزها هناك 402 و`final` (لا رمز نهائي أقرب في العقد). ADR-022 §6.5 كان لا يعد الفارغة في الحدود. `core/uploads.go` (`checkSessionLimits`، `errTooManyLive`).

### ربط القواعد (S2-E)

الربط مطابق لكل ملاحظات Pyric على bucket منشور (PY-RR، 182 من 182 مسبارًا: الإنشاء والتعديل والكتابة فوق موجود والتعديل فوق ما رفعه العميل وGCS)، ولمسبار `native-object-fields`، ولحالتي list في المحاكي (`aruma/storage/read_list` و`aruma/storage-paths/list_on_folder`)، و26 من 31 حالة `get` في Test API على مورد `default`، والخمس الباقية قراءات Firestore التي ترفض حتى v0.4 (`core/rules_oracle_test.go`).

57. **مؤقت: ترتيب `metadata` في القواعد بالمفتاح** (`rules.Map`)، فـ `values()` مرتبة كـ `keys()`. Test API وحدها تحفظ ترتيب الطلب، وهي تأخذ `request` حرفيًا؛ jsonb لا يحفظ ترتيبًا، ولا قياس من المحاكي ولا المنشور. إن قاسه تسجيل تغيّر `metadataValue` وحده، ومعه تخزين يحفظ الترتيب. `core/rules_fields.go`.
58. **مؤقت: `resource` سبعة عشر مفتاحًا:** مفاتيح `request.resource` الخمسة عشر (PY-RR) و`timeCreated` و`updated`، وغير المضبوط `null` إلا `contentEncoding` (`'identity'`، كما قاسه PY-RR في `request.resource`)، و`storageClass` = `'STANDARD'`. `core/rules_fields.go` (`existing`).
59. **مؤقت: list ببادئة لا تنتهي بـ `/` يُقرَّر على مجلدها** ومقطع مجهول، فلا تسمح قاعدة `p/a/{x}` بقائمة `p/a` التي تعيد `p/ab`. `core/rules.go` (`rulePaths`).
60. **قيد: اسم بمقاطع أكثر من `request_segments` للـ ruleset** (1024 افتراضيًا) **يُرفض 403**، لا 503. `core/rules.go` (`ruleRequest`).
61. **قيد: مصدر قواعد فيه NUL يُرفض 400**، والـ rulesets لكل مشروع حتى `rules.limits.rulesets_per_project` (2,500). `tenancy/core/rules.go`.

### واجهة GCS JSON لـ Admin (S4-A)

تختبرها `internal/services/storage/gcs/*_test.go` فوق `core` حقيقية وPostgres، و`TestIsolationStorageServiceAccount` في `test/acceptance` برموز حقيقية من مفاتيح الحسابات. وجُرّبت مرة يدويًا بـ `@google-cloud/storage` 8.4.0 (`exists` و`getMetadata` و`setMetadata` و`copy` و`move` و`getFiles` و`setCorsConfiguration` و`delete`) وبـ `cloud.google.com/go/storage` 1.62.1 (`Attrs` و`Update` و`CopierFrom` و`Objects` و`Bucket.Update` و`Delete`): كلها كما في الإنتاج، و`exists()` يعيد `false` على 404، وGo يرى `ErrObjectNotExist` و`ErrBucketNotExist`. الرفع والوسائط في S4-B وS4-C.

62. **قيد: حسابات الخدمة وحدها.** `Authorization: Bearer aruma_sat_…` لحساب من مشروع الـ bucket؛ رمز CLI الشخصي أو رمز مستخدم 401 `authError` «Invalid Credentials»، وبلا رمز 401 `required`، وحساب مشروع آخر 403 `forbidden` بنص GCS («… does not have storage.objects.get access …»). وطلب على جلسة رفع (`upload_id`) بلا اعتماد: المعرّف هو الصلاحية. `gcs/auth.go`.
63. **مؤقت: 404 قبل 401.** الـ bucket يُحل أولًا، فغير الموجود 404 «The specified bucket does not exist.» لأي طالب، كما رد الإنتاج على المجهول (`production/bucket_probe.json`). `gcs/auth.go` (`admit`).
64. **مؤقت: الشروط التي لا تقع على الصف المقفل.** `sourceGeneration` و`ifSourceGenerationMatch` وأخواتها في rewrite تُفحص على المصدر كما قُرئ قبل النسخ، و`ifMetagenerationMatch`/`NotMatch` على الـ bucket قبل `SetCORS`: `core.Rewrite` و`core.SetCORS` لا تأخذانها، فتغيير بينهما يمر. و`?generation=` على PATCH وDELETE يصير `ifGenerationMatch` يُفحص على الصف المقفل، والجيل غير الحالي 404 لا 412 (البند 10). `gcs/objects.go` (`pinned`، `checkSource`)، و`gcs/buckets.go`.
65. **قيد: لا حقل يُتجاهل وهو يبدو مطبَّقًا.** في جسم PATCH وrewrite والرفع: `acl` و`temporaryHold` و`eventBasedHold` و`customTime` و`retention` و`contexts` و`kmsKeyName` و`customerEncryption` و`storageClass` غير `STANDARD` 501، وقيمها الفارغة (`null` و`false` و`[]`، كمورد قُرئ وأُعيد) تُتجاهل، وحقول المخرجات (`kind` و`generation` و`size` …) تُتجاهل. وقيمة `metadata` غير نص ولا `null` 400. و`buckets.patch` بأي حقل غير `cors` 400، و`cors: null` يمسحها، وجسم بلا `cors` لا يغيّر شيئًا. `gcs/resource.go` (`decodeAttrs`)، و`gcs/buckets.go` (`decodeBucketPatch`).
66. **مؤقت: 501 وما حوله.** ما في واجهة GCS ولم يدخل v0.3 501 `notImplemented`: `copyTo` (المكتبات تنسخ بـ `rewriteTo`) و`compose` و`moveTo` و`restore` والـ ACL وIAM والإشعارات وقائمة الـ buckets وإنشاؤها وحذفها وتحديثها بـ PUT و`objects.update` بـ PUT وإنشاء كائن بلا رفع وترويسات CSEK و`predefinedAcl`. وما ليس في الواجهة أصلًا 404 «Not Found». و`rewriteToken` مُرسَل 400، فلا نصدر رموزًا (`done:true` دائمًا)، و`maxBytesRewrittenPerCall` يُتجاهل. وفي list: `versions` و`softDeleted` و`includeTrailingDelimiter` و`includeFoldersAsPrefixes` بقيمة `true`، و`matchGlob` غير فارغ، 400 (ADR-022 §13). `gcs/gcs.go`، و`gcs/objects.go`.
67. **مؤقت: ما لا تسجيل له.** مورد الـ bucket بموقع اسمي (`US`، `multi-region`)، و`timeCreated` إنشاء المشروع، و`updated` آخر تغيير لـ CORS، وبلا `generation`. وأسباب الأخطاء: 412 «At least one of the pre-conditions you specified did not hold.» `conditionNotMet` (نص GCS)، و413 `uploadTooLarge`، و429 `rateLimitExceeded` لكثرة الجلسات، و410 للجلسة الملغاة، و503 `backendError` مع `Retry-After: 1`. وكل رد JSON بـ `Cache-Control: no-cache, no-store, max-age=0, must-revalidate` كالإنتاج. `gcs/resource.go`، و`gcs/errors.go`.

### الرفع والوسائط على GCS (S4-B، S4-C)

تختبرها `internal/services/storage/gcs/{upload,download}_test.go` طلبًا طلبًا كما يرسلها Node 8.4.0 وGo 1.62.1، و`TestStorageGCSWithTheAdminSDKs` في `test/acceptance` (بـ `ARUMA_GCS_SDK=1`) بالمكتبتين نفسيهما عبر وكيل يسجّل كل تبادل: `save()` في طلب واحد (`bytes 0-*/*`) ومقطّعًا (308 و`Range`) و`resumable:false`، وGo بـ multipart وبالجلسة (200 مع `X-Http-Status-Code-Override: 308`، والجزء الأخير الفارغ `bytes */N`)، والـ checksums المختلفة 400 بلا كائن، و`ifGenerationMatch=0` على موجود 412، و`download()` بتحققه الافتراضي من CRC، وقراءات Go بـ `WithJSONReads` كاملة وبنطاق ومثبّتة على generation، و`ReaderObjectAttrs.CacheControl` يساوي `Attrs().CacheControl`.

68. **مؤقت: إلغاء الجلسة** بـ `DELETE` على رابطها 499 بلا جسم (وثيقة GCS: «the response contains a 499 status code»)، ومرة ثانية 499، وبعد الإنهاء 400، وطلب بعده على الجلسة 410. لا يرسله أي من العميلين (B §10.3). وطلب على الجلسة بلا `Content-Range` هو الكائن كله في طلب واحد، كـ `bytes 0-*/*`. `gcs/upload.go` (`resumeUpload`).
69. **مؤقت: بدء الرفع يُحسب على `upload_starts_per_minute_per_ip`** (media وmultipart وresumable)، والتجاوز 429 `rateLimitExceeded` مع `Retry-After`. والعنوان عنوان النظير: `gcs.Options` بلا `TrustedProxies` بعد، فخلف وكيل تُحسب كل البدايات على عنوانه. `gcs/upload.go` (`allowStart`).
70. **مؤقت: ما لا تسجيل له في الرفع.** جسم أبطأ من `min_bytes_per_second` على 30 ثانية 408، وجسم انقطعت قراءته 400، وتجاوز حد الجسم عند المدخل 413 `uploadTooLarge`. و`Content-Length` يخالف طول `Content-Range` 400 قبل حفظ شيء، وطلب `bytes */*` بجسم 400. و`crc32c` أو `md5Hash` في JSON يناقضه `X-Goog-Hash` في الطلب نفسه 400. و`X-Upload-Content-Length` حجم معلن، والزيادة عليه 400 وتنهي الجلسة. `gcs/upload.go`.
71. **قيد: `Retry-After` على الضغط الحقيقي وحده** (معدل البدء، و`core.ErrOverloaded`)، لا على 503 من عطل الخادم (`core.ErrUnavailable`: أرضية القرص، أو القاعدة، أو التزام لم يُحسم، أو إيقاف الخادم): المكتبات التي تحترمه تعيد بلا حد. `gcs/upload.go` (`refuse`)، ويمر به كل رفض من `core` في الرفع والتنزيل.
