# توافق Cloud Storage for Firebase: الإصدار 0.3

- **الحالة:** بدأه S2-B وS2-C وS2-E كلٌّ على فرعه (2026-10-24)، وجمعه دمج S2 في وثيقة واحدة بقسم لكل مجال، وأضاف دمج S3 إلى S5 أقسام واجهاتها. وملأ S3-D قسم «اختبارات Firebase الرسمية» بنتائج `test/compat/storage` (2026-10-24)، ويضيف S7 أرقامه (storage-layout.md).
- **المرجع السلوكي:** الإنتاج حيث رُصد (`test/oracle/storage/production`، وملاحظات Pyric على Storage حقيقي)، ثم المحاكي الرسمي من firebase-tools 15.34.0 (`test/oracle/storage/emulator`)، ثم مجموعات Google.
- **التصنيف:** الفروق كلها في ADR-023 §3 بأرقامها، كل منها بمصدره وتصنيفه: **قيد** (أشد من Firebase عمدًا)، أو **مؤقت** (بلا دليل إنتاج)، أو **أصح من المحاكي**. وكل قرار مؤقت موضع واحد في الكود، وكل صف أو بند هنا يحمل رقم بنده في ADR-023 §3.

## اختبارات Firebase الرسمية

`test/compat/storage/run.sh` (تفصيله في `test/compat/storage/README.md`) يشغّل اختبارات Storage التي تنشرها Firebase، بلا تعديل، على Aruma مبني من الفرع وعلى قاعدة تُنشأ وتُحذف، ويعيد تسجيلات `test/oracle/storage/emulator` عليه. وكل فرق مصنّف في `test/compat/storage/classified.json` بواحد من ثلاثة: **أ** خطأ في Aruma (أُصلح ومعه اختبار، أو أُدرج)، و**ب** فرق في ADR-023 §3 برقمه، و**ج** خطأ في المحاكي ومعه دليل أن الإنتاج يخالفه. والتشغيل محلي وحده: كل عملية Node تحت `test/experiments/netguard.cjs` فلا اتصال خارج الجهاز (لم يُرفض اتصال واحد)، والمتصفح لا يحل إلا loopback وأسماء الواجهة المحلية. مسار الإنتاج لكل منصة في S6.

وفي CI (`ci.yml`) تعمل هذه المجموعات وأخواتها تحت `test/ci/egress-guard.sh`: iptables يرفض كل حزمة ترسلها عمليات المجموعة إلى عنوان خارج الجهاز ويعدّها، ولا يُثبَّت شيء تحته، ولا يصل sudo إلى root (`no_new_privs`). وحدوده: الـ DNS يذهب إلى المحلّل المحلي `127.0.0.53` (systemd-resolved) الذي يرسل الاستعلام خارج الجهاز باسمه هو، فاستعلام الاسم لا يُعدّ خروجًا (تذكر الخطوة الأخيرة عدد حزم المنفذ 53 إليه)، وحراس المجموعات يرفضون أسماء Google قبل أي استعلام؛ وIPv6 يُفحص حين يكون للمشغّل مسار IPv6 وحده، وبدونه يفشل الاتصال عند التوجيه قبل القواعد فلا يُعدّ، ولا يخرج شيء.

| المجموعة | المصدر | الاختبارات | نجح | أ | ب | ج |
|---|---|---|---|---|---|---|
| js-node | firebase-js-sdk `firebase@13.0.0`، `packages/storage/test/integration` (vitest في Node) | 13 | 13 | 0 | 0 | 0 |
| js-browser | الملف نفسه في Chromium 1248 (vitest browser) | 13 | 13 | 0 | 0 | 0 |
| js-compat | `packages/storage-compat/test/integration` (mocha في Node) | 8 | 8 | 0 | 0 | 0 |
| ct | firebase-tools `v15.34.0`، `scripts/storage-emulator-integration/conformance/*.test.ts` غير `emulatorOnly` | 119 | 111 | 0 (12 أُصلحت) | 6 | 2 |
| **المجموع** | | **153** | **145** | **0** | **6** | **2** |

- **مجموعات js تعمل كما يعمل كود الإنتاج:** لا تستدعي `connectStorageEmulator`، فالمكتبة تكلم `firebasestorage.googleapis.com` وAuth يكلم `identitytoolkit.googleapis.com`. يحل `hosts.cjs` هذين الاسمين (وحدهما) في Node إلى واجهة TLS محلية، وتفعل قواعد المحلّل ذلك في Chromium، والواجهة تسلّم الطلب إلى Aruma بشكل التحويل وقت التشغيل (`/firebasestorage.googleapis.com/v0/...`، ADR-022 §1). القواعد `public/**` لمستخدم مسجّل، وCORS الـ bucket هو `cors.json` الموثّق، وبدونه لا يقرأ `getBytes` من متصفح على أصل آخر، في Firebase كما هنا.
- **اختبارات المطابقة وإعادة التسجيلات بوضع المحاكي**، عبر محوّل اختباري (`adapter.mjs`) يبدّل بدائل المحاكي الثابتة بما يحمله نشر حقيقي: `fake-api-key` بمفتاح المشروع، و`Bearer owner` ومكتبة Admin بلا اعتماد برمز حساب خدمة، و`/internal/reset` بحذف كائنات الـ bucket. ولا يخدم Aruma شيئًا من ذلك.
- **حدود العنوان الواحد:** كل الاختبارات من 127.0.0.1، مئات الرفعات في دقيقة، فالتشغيل يرفع حدود الرفع لكل عنوان. وبالحدود الافتراضية (`STORAGE_COMPAT_LIMITS=default`) العدد نفسه، 111 من 119، منذ صار بدء الـ admin لا يُحسب على العنوان (البند 81، مراجعة S3-D). وقبلها كان معظم `ct` يلقى رفض معدل البدء: على `/v0` 503 مع `Retry-After`، وعلى GCS 429.
- **`emulatorOnly`:** الأربعة تُشغَّل وتُعرض منفصلة خارج العدد: اثنان ينجحان، و`should not support the use of a rewriteToken` (400 هنا، 501 في المحاكي، البند 78) و`signed post policy uploads` (البند 93) يفشلان.
- **لم يُشغَّل:** اختبارات الوحدة في الحزمتين (بلا خادم)، و`storage-compat` في المتصفح: يشغّله upstream بـ karma وبناء webpack للمستودع كله، واختباراته الثمانية هي ملف Node نفسه.

### ما أصلحه S3-D (أ)

أول تشغيل لـ `ct` (بعد رفع الحدود): 20 فشلًا من 119. اثنا عشر منها أخطاء في Aruma أُصلحت، كل منها باختبار (الثلاثة الأخيرة بعد مراجعة S3-D):

| الإصلاح | الاختبارات | الموضع والاختبار |
|---|---|---|
| رد التنزيل المفكوك القصير كان يخرج بـ `Content-Length` (يضيفه net/http لرد يتسع في مخزنه)، والمفكوك لا يحمله | `gzip should serve gunzipped file by default` على `/v0` وعلى GCS (2) | `media.serve` يرسل سطر الحالة قبل البايتات؛ `TestShortTranscodingHasNoContentLength` على مستمع حقيقي |
| multipart بـ `Content-Type` ليس `multipart/related` بحد صالح: «Bad content type.» | `should return an error message when uploading a file with invalid content type` (`/v0`)، و`… on invalid content type` (GCS) (2) | `wire.ErrBadContentType`؛ `TestMultipartBadContentType`، و`TestUploadMultipart` |
| رفع GCS بلا `uploadType` كان 400؛ صار media، أو multipart بـ `X-Goog-Upload-Protocol` (البند 91) | `should set default metadata`، و`should default to media upload…`، و`should list objects in the provided bucket`، و`should respect X-Goog-Upload-Protocol header` (4) | `gcs.classifyUpload`؛ `TestClassify`، و`TestUploadMedia`، و`TestUploadMultipart` |
| قيمة `null` في metadata كائن GCS جديد كانت تُحذف؛ صارت `""` (البند 92) | `#copy() should set null custom metadata values to empty strings` (1) | `core.incomingObject`؛ `TestRewrite` |
| قيمة metadata منطقية أو عددية على GCS كانت 400؛ صارت نصها كما يكتبه `JSON.stringify` (البند 77) | `#setMetadata() should convert non-string fields under .metadata to strings` (1) | `gcs.metadataValue`؛ `TestDecodeAttrs`، و`TestPatchObject` |
| `Bearer` ليس Admin لمشروع الـ bucket على `/v0` كان 401؛ صار 403 (البند 67) | `should return a 403 for create_token` و`… for delete_token if auth header is invalid` (2) | `api.admin`؛ `TestRoutes`، و`TestStorageV0Metadata` |

ومن تسجيل الإنتاج (لا اختبار له في المجموعات): بدء رفع متقطع على bucket مجهول كان 404 بلا `X-Goog-Upload-Status`، والإنتاج يرد `final` (`bucket_probe.json`؛ وmultipart بلا حالة كما كان). `api.serve`؛ `TestUnknownBucketUpload`.

### ما بقي من `ct` (ب، ج)

| الاختبار | التصنيف | لماذا |
|---|---|---|
| `#getDownloadURL() serves the right content`، و`Headers should set default response headers on object download` | ب 28 | `attachment; filename*=utf-8''testFile` لكائن بلا قيمة مخزّنة؛ الاختبار يريد `filename*=testFile` بلا الـ charset الذي يوجبه RFC 8187 |
| `should handle resumable uploads with an empty buffer` | ب 88 | `.send({})` في supertest يجعل `finalize` يحمل البايتين `{}` بلا إزاحة؛ و`finalize` منفردًا جسمه فارغ وإلا 400 |
| `#copy() should copy the file to a different bucket` | ب 93 | bucket واحد لكل مشروع: `other-bucket` غير موجود، 404 |
| `#makePublic() should no-op`، و`… should not interfere with downloading of bytes via public URL` | ب 78 | `makePublic` يعدّل ACL الكائن: 501 |
| `#getMetadata() should return generated metadata for new upload`، و`#setMetadata() should allow overriding of default metadata` | ج | قائمة المفاتيح الدقيقة بلا `timeFinalized`، وهي في وصف واجهة GCS نفسها (`google.golang.org/api` storage/v1 `Object.TimeFinalized`، ويقرؤه `cloud.google.com/go/storage` 1.62.1 في `ObjectAttrs.Finalized`)؛ القائمة قائمة المحاكي |

### إعادة تسجيلات المحاكي

السيناريوهات الثلاثة عشر لـ `capture.mjs` على Aruma بالمكتبات والـ bucket والقواعد نفسها، ثم `captures.mjs` يقارن كل خطوة بتوأمها في `test/oracle/storage/emulator`: نتيجة المكتبة، والطلبات، والحالات، والترويسات التي تقرؤها العملاء، والحقول. بعد توحيد ما يختلف بين أي تشغيلين (معرّف التشغيل، والمنافذ، والمعرّفات، والأوقات، وترتيب المفاتيح، وترتيب الطلبات المتزامنة). **188 خطوة، 836 فرقًا من 64 نوعًا: أ 0، وب 24 نوعًا (293 فرقًا)، وج 40 نوعًا (543).** والقائمة الكاملة بأمثلتها في `captures-diff.txt` بعد التشغيل (`--verbose` لكل فرق).

| التصنيف | البند | الفرق | العدد |
|---|---|---|---|
| ب | 27 | `Access-Control-Allow-Origin: *` على كل رد `/v0`، والمحاكي يرد على `Origin` وحده | 136 |
| ب | 40 | الـ metadata الفارغة لا تُكتب في مورد `/v0` | 37 |
| ب | 22 | نص 403 «Permission denied.»، و`query` بعد الإنهاء يعيد الـ metadata | 29 |
| ب | 94 | ردود البدء والقطع على `/v0` بلا جسم، و`X-Goog-Upload-Size-Received` على كل رد قطعة | 24 |
| ب | 21 | `Cache-Control: private, max-age=0` على التنزيل بلا قيمة مخزّنة | 10 |
| ب | 86، 85، 23 | الجلسة المنتهية والملغاة والإنهاء الأقصر من المعلن | 10، 7، 5 |
| ب | 7 | قارئ Go بلا `WithJSONReads` يقرأ بمسار XML API | 9 |
| ب | 67، 26، 18، 45، 5، 59، 28 | الاعتماد المرفوض 401، والاسم في metadata وحده، والرمز لا يفتح الـ metadata، و`pageToken` المعتم، والرمز غير الصالح 400، والبادئة بلا `/`، والـ disposition الافتراضي | 7، 4، 3، 3، 3، 3، 3 |
| ج | — | `charset=UTF-8`، و`Cache-Control` على ردود JSON، وأخطاء JSON، وترويسات الكائن تُنسخ على ردود JSON، و`Content-Encoding` الفارغ، و`x-goog-stored-content-encoding`، و`timeFinalized`، وصفحات القائمة (`maxResults` يعد العناصر والبادئات معًا)، و`final` على رفض multipart، ونوع الجزء في multipart Go | 401 |
| ج | 34 | سطح GCS كاملًا: المتقطع لـ Node وGo، وPATCH وrewrite، و`buckets.get`، والشروط المسبقة 412، وما يتبعها | 57 |
| ج | 35 | `contentDisposition` الافتراضي `inline; filename*=utf-8''<اسم>` | 58 |
| ج | 31، 33، 32، 30 | bucket مجهول 404، والإزاحات تُفحص، ولا يزرع العميل رمزًا، والرمز غير الموقّع يُرفض | 9، 8، 7، 3 |

ودليل «ج» حيث لا بند: ردود الإنتاج في `production/bucket_probe.json` (JSON بـ `charset=UTF-8` و`Cache-Control` و`*` وأخطاء JSON، وبلا `X-Goog-Upload-Status` على multipart)، ووصف واجهة GCS ووثائقها (`timeFinalized`، و«Maximum combined number of entries in items[] and prefixes[]»، ونوع الجزء في multipart)، وما تقرؤه مكتبات Admin من ردود GCS (`x-goog-stored-content-encoding`، B §6.3).

## الفروق

القائمة الكاملة في ADR-023 §3: البنود 1 إلى 37 من S0، وما بعدها أضافته مهام S2 إلى S5 ثم S3-D (91 إلى 94)، مرقّمة مرة واحدة بترتيب الدمج. ولكل مجال قسم واحد أدناه بالبنود التي تمسه وموضعها في الكود.

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

### أداة `firebase`: الإدارة (S5-A)

ما ترسله `firebase deploy --only storage` و`firebase init storage` (C §1.3 و§1.4 و§1.5): Service Usage، و`defaultBucket`، وواجهة firebaserules (R1 إلى R8)، وكل نجاح 200 حرفيًا، والحذف `{}`. يختبرها `test/acceptance/storage_mgmt_test.go`: الطلبات بترتيب النشر الأول والثاني («already up to date»)، و`:test` على **كل** خطأ ترجمة أعادته Rules Test API في `test/oracle/rules/production/raw` (337 مصدرًا لا يترجم، و9 فوق حد الحجم): أسطر الأخطاء كما تطبعها الأداة (`[E] line:column - description`) مطابقة إلا أربعًا يعطي فيها المحرك خطأ نحو زائدًا («Unexpected ')'.» بعد خريطة مفتاحها عدد عشري، `hidane-conditions.json`)، و329 من 346 ردًا مطابقة بايتًا ببايت؛ والباقي فروق المحرك في `endOffset` وتحذيرات المتغير غير المستعمل والإصدار 1 وتحذيرات الأنواع (ADR-021 §2.1، `internal/platform/rules`)، لا في شكل الواجهة. و`tenancy/core/rules_s5a_test.go` للقوائم والحذف، ومنه حذف يسابق نشرًا.

62. **مؤقت: `:test` لا يشغّل `testSuite`.** مصدر لا يترجم يُرد بـ `issues[]` أولًا كالإنتاج، ومصدر يترجم مع `testCases` يُرفض 400 بدل أن يُرد كأنها نجحت. والأداة لا ترسل `testSuite`. `mgmt/rules.go` (`testRuleset`).
63. **مؤقت: خدمة الـ ruleset من تصريح `service` في مصدره:** Storage، أو Firestore إن صرّح `cloud.firestore`، واسم غيرهما يُترجم لـ Storage بتحذير المحرك ولا يمنح شيئًا. `mgmt/rules.go` (`compile`).
64. **مؤقت: الحالات التي لم تُقس:** حذف ruleset تشير إليه إصدارة 400 `FAILED_PRECONDITION` (ولو نُشرت الإصدارة أثناء الحذف)، و`PATCH` لإصدارة غير موجودة 404 فتنشئها الأداة بـ `POST`، و`POST` لإصدارة موجودة 409 `ALREADY_EXISTS`، و`filter` في القائمتين و`attachment_point` (قاعدة Firestore مسمّاة) 400 بدل التجاهل. `mgmt/rules.go`، و`tenancy/core/rules.go` (`DeleteRuleset`).
65. **مؤقت: الأشكال التي لم تُقس:** الـ Ruleset بلا `metadata.services`، والقائمة بلا `source`، والقائمة الفارغة `{}` كـ proto3، و`createTime` و`updateTime` بست خانات عشرية ثابتة (الأداة ترتب بمقارنة النص). و`defaultBucket` بـ `location` = `US-CENTRAL1` واسم بمعرّف المشروع، و`/v1beta/projects/{p}/buckets` باسم برقم المشروع (`extensions/provisioningHelper.ts:127-129`)، وService Usage `ENABLED` لكل اسم وعملية `:enable` منتهية. `mgmt/storage.go`، و`mgmt/rules.go` (`rulesTime`).
66. **قيد: جسم فوق 1 MiB على `:test` أو `rulesets`** يُرد برسالة الإنتاج للحجم 400 («Total size of sources must be smaller than 262144.»، `limits.json` `size_comment_1048576`) لا 413، فكل جسم بهذا الحجم مصدر فوق الحد. `mgmt/rules.go` (`decodeSource`).

### أداة `firebase` من طرف إلى طرف (S5-D)

firebase-tools 15.34.0 نفسها (مثبّتة في `test/acceptance/testdata/firebase-cli`) على خادم حي، يختبرها `TestStorageWithTheFirebaseCLI` في `test/acceptance/storage_cli_test.go` (`ARUMA_FIREBASE_CLI=1`). الأداة مضبوطة بالبيئة وحدها كما يضبطها المطوّر (C §5.1، وT23 في `docs/research/experiments.md`): `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_RULES_URL` و`FIREBASE_FIREBASESTORAGE_URL` و`FIREBASE_SERVICE_USAGE_URL` إلى واجهة https أمام الخادم، و`FIREBASE_TOKEN` رمز CLI، و`FIREBASE_SKIP_INFORMATIONAL_IAM=1`. وتعمل ببيت خاص بها، وتحت `test/experiments/netguard.cjs` (وsandbox-exec على macOS)، فأي اتصال خارج loopback يُرفض ويُفشل الاختبار. والواجهة تسجّل كل طلب، فيُفحص الترتيب أيضًا.

| الخطوة | ما يُفحص |
|---|---|
| `firebase init storage` بلا إصدارة | القالب بايتًا ببايت، و`"storage": {"rules": "storage.rules"}`، والطلبات: Service Usage باسم `127.0.0.1` (مضيف `FIREBASE_FIREBASESTORAGE_URL`، C §1.4)، ثم `defaultBucket`، ثم `releases` |
| `firebase deploy --only storage` | `:test`، و`releases`، و`POST rulesets`، و`PATCH` 404 ثم `POST releases`، وقراءة مجهولة على `/v0` تنقلب من 403 إلى 200 بعد النشر مباشرة |
| النشر الثاني بلا تغيير | «latest version of storage.rules already up to date, skipping upload...»، و`GET rulesets/{id}` و`PATCH` 200 بلا `POST rulesets` |
| قواعد مغيّرة | القراءة نفسها 403 بعد النشر مباشرة |
| `firebase init storage` بعد النشر | القواعد المنشورة بايتًا ببايت، و«Downloaded the existing Storage Security Rules from the Firebase console» |
| خطأ ترجمة | خروج 1 و`[E] 4:20 - Missing conditional expression after 'if'.`، ولا طلب بعد `:test`، والقواعد قبله باقية على `/v0` |
| بلا Storage | `init` و`deploy` يطبعان «Firebase Storage has not been set up on project 'cli-app'» بعد `defaultBucket` 404، ولا طلب لواجهة القواعد |

- الأداة تطبع تحذير `FIREBASE_TOKEN` المهمل في كل أمر؛ وهو الاعتماد الوحيد العامل (T23).
- `init` في هذا الإصدار يمر أيضًا بـ «Agent Skills Setup» ويكتب `.gitignore`، بلا طلب شبكة.
- لا بند جديد في ADR-023 §3.

### واجهة `/v0` (S3-A)

يختبرها `api/api_test.go` (`TestRoutes`: كل طلب ترسله المكتبات الثلاث يصل معالجه، وردود الموجّه بشكل الإنتاج 400 و404)، و`api/objects_test.go` (أجسام PATCH، وكل خطأ مسمّى بحالته)، و`test/acceptance/storage_v0_test.go` (كل عملية غير الرفع والتنزيل على خادم حقيقي، و`wiretest.AssertNoNull` على كل رد).

- **ترتيب الرد كالإنتاج** (`bucket_probe.json`): مسار لا يسمّي عملية 400 «Invalid HTTP method/URL pair.» أيًا كان الـ bucket، ثم bucket مجهول 404 «Not Found.» قبل أي اعتماد، ثم الاعتماد. ورابط جلسة الرفع يتجاهل `Authorization`.
- **CORS:** `*` على كل رد غير التنزيل، والأخطاء منه، بلا `Allow-Credentials`؛ و`nosniff` و`private, max-age=0` على كل رد JSON (E §1).

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 67 | `Firebase` برمز لا يُتحقق منه ومخطط آخر 401 (تجدّد المكتبات رمزها)؛ و`Bearer` ليس Admin لمشروع الـ bucket، ومنه `Bearer <idToken>` وحساب خدمة مشروع آخر، 403 «Permission denied.» قبل أي بحث عن كائن (مراجعة S3-D) | مؤقت | E §7، وCT (`firebase.endpoints.test.ts:662-667`، `:712-717`) | `api.principal`، `api.admin` |
| 68 | رمز CLI للمالك يتجاوز القواعد، ويُربط بالحساب فيُفحص عند إنهاء الرفع | مؤقت | ADR-022 §15 | `api.admin` |
| 69 | preflight على bucket مجهول 404، وعلى موجود 204؛ وCORS التنزيل من قواعد الـ bucket، و`Allow-Headers` يعيد الترويسات المطلوبة كـ GCS JSON | مؤقت | `bucket_probe.json`، ADR-022 §11 | `api.preflight`، `api.mediaCORS` |
| 70 | `upload_requests_per_ip` 503 مع `Retry-After`، بعدّ واحد لكل عنوان يشاركه سطح GCS؛ و`ErrTooManyUploads` 429، و`Retry-After` (بين 1 و60 ثانية) على الضغط العابر وحده، لا على `ErrUnavailable` | مؤقت | A §5.3، ADR-022 §12 | `ratelimit.InFlight`، `api.writeError` |
| 71 | `create_token` يغلب `delete_token` حين يجتمعان، وقيمته غير `true` 400 قبل فحص صلاحية Admin | مؤقت | `tokens.json` | `api.tokens` |
| 72 | `maxResults` غير عدد صحيح موجب 400، و`startOffset`/`endOffset` تُتجاهل | مؤقت | A §3.7 | `api.list` |
| 73 | جسم PATCH بقيمة غير نصية 400، وفوق الحد 413، والفارغ لا يغيّر إلا الـ metageneration | مؤقت | A §3.5 | `api.readPatch` |

### واجهة GCS JSON لـ Admin (S4-A)

تختبرها `internal/services/storage/gcs/*_test.go` فوق `core` حقيقية وPostgres، و`TestIsolationStorageServiceAccount` في `test/acceptance` برموز حقيقية من مفاتيح الحسابات. وجُرّبت مرة يدويًا بـ `@google-cloud/storage` 8.4.0 (`exists` و`getMetadata` و`setMetadata` و`copy` و`move` و`getFiles` و`setCorsConfiguration` و`delete`) وبـ `cloud.google.com/go/storage` 1.62.1 (`Attrs` و`Update` و`CopierFrom` و`Objects` و`Bucket.Update` و`Delete`): كلها كما في الإنتاج، و`exists()` يعيد `false` على 404، وGo يرى `ErrObjectNotExist` و`ErrBucketNotExist`. الرفع والوسائط في S4-B وS4-C.

74. **قيد: حسابات الخدمة وحدها.** `Authorization: Bearer aruma_sat_…` لحساب من مشروع الـ bucket؛ رمز CLI الشخصي أو رمز مستخدم 401 `authError` «Invalid Credentials»، وبلا رمز 401 `required`، وحساب مشروع آخر 403 `forbidden` بنص GCS («… does not have storage.objects.get access …»)، لا `NOT_FOUND`: استثناء Storage من ADR-010 S-11 (ADR-022 §14)، والرد واحد لكائن موجود وغائب. وطلب على جلسة رفع (`upload_id`) بلا اعتماد: المعرّف هو الصلاحية. `gcs/auth.go`.
75. **مؤقت: 404 قبل 401 و403.** الـ bucket يُحل أولًا، فغير الموجود 404 «The specified bucket does not exist.» لأي طالب، كما رد الإنتاج على المجهول (`production/bucket_probe.json`)؛ ثم الاعتماد، **قبل أي بحث عن كائن** (استثناء S-11، ADR-022 §14). `gcs/gcs.go` (`route`)، و`gcs/auth.go` (`admit`).
76. **مؤقت: كل شرط على الصف الذي تقع عليه العملية.** `sourceGeneration` و`ifSourceGenerationMatch` وأخواتها في rewrite تُفحص في `core.Rewrite` على صف المصدر الذي يُنسخ ملفه (ومفتاح الملف لا يُعاد استعماله، فهي تصدق على البايتات المنسوخة بالضبط)، و`ifMetagenerationMatch`/`NotMatch` على الـ bucket في معاملة `SetCORS` على صف مقفل، فمن تعديلين بالشرط نفسه يمر واحد والآخر 412. و`?generation=` على PATCH وDELETE يصير `ifGenerationMatch` يُفحص على الصف المقفل، والجيل غير الحالي 404 لا 412 (البند 10). `gcs/objects.go` (`pinned`)، و`core/insert.go`، و`core/cors.go`؛ و`TestRewriteSourceConditionsUnderReplacement` و`TestSetCORSConditions`.
77. **قيد: لا حقل يُتجاهل وهو يبدو مطبَّقًا.** في جسم PATCH وrewrite والرفع: `acl` و`temporaryHold` و`eventBasedHold` و`customTime` و`retention` و`contexts` و`kmsKeyName` و`customerEncryption` و`storageClass` غير `STANDARD` 501، وقيمها الفارغة (`null` و`false` و`[]`، كمورد قُرئ وأُعيد) تُتجاهل، وحقول المخرجات (`kind` و`generation` و`size` …) تُتجاهل. وقيمة مفتاح في `metadata` منطقية أو عددية تُخزَّن نصها كما يكتبه `JSON.stringify` (`true` ← `"true"`، و`-1` ← `"-1"`): المكتبة تكتب قيمها `string | boolean | number | null`، وCT (`gcs-js-sdk.test.ts:942-951`) يريد ذلك. والكائن والمصفوفة 400، وعلى `/v0` تبقى القيمة غير النصية 400 (البند 73) بلا دليل أن الإنتاج يحوّلها هناك (`gcs.metadataValue`). و`buckets.patch` بأي حقل غير `cors` 400، و`cors: null` يمسحها، وجسم بلا `cors` لا يغيّر شيئًا. `gcs/resource.go` (`decodeAttrs`)، و`gcs/buckets.go` (`decodeBucketPatch`).
78. **مؤقت: 501 وما حوله.** ما في واجهة GCS ولم يدخل v0.3 501 `notImplemented`: `copyTo` (المكتبات تنسخ بـ `rewriteTo`) و`compose` و`moveTo` و`restore` والـ ACL وIAM والإشعارات وقائمة الـ buckets وإنشاؤها وحذفها وتحديثها بـ PUT و`objects.update` بـ PUT وإنشاء كائن بلا رفع وترويسات CSEK و`predefinedAcl`. وما ليس في الواجهة أصلًا 404 «Not Found». و`rewriteToken` مُرسَل 400، فلا نصدر رموزًا (`done:true` دائمًا)، و`maxBytesRewrittenPerCall` يُتجاهل. وفي list: `versions` و`softDeleted` و`includeTrailingDelimiter` و`includeFoldersAsPrefixes` بقيمة `true`، و`matchGlob` غير فارغ، 400 (ADR-022 §13). `gcs/gcs.go`، و`gcs/objects.go`.
79. **مؤقت: ما لا تسجيل له.** مورد الـ bucket بموقع اسمي (`US`، `multi-region`)، و`timeCreated` إنشاء المشروع، و`updated` آخر تغيير لـ CORS، وبلا `generation`. وأسباب الأخطاء: 412 «At least one of the pre-conditions you specified did not hold.» `conditionNotMet` (نص GCS)، و413 `uploadTooLarge`، و429 `rateLimitExceeded` لكثرة الجلسات ولتجاوز `upload_requests_per_ip` (IPv6 لكل /64، و`X-Forwarded-For` من وكيل موثوق وحده؛ والعدّ واحد مع `/v0`، فكتابة معلّقة على أحدهما تُحسب على الآخر، `TestStorageWritesPerIPAcrossSurfaces`)، و410 للجلسة الملغاة، و503 `backendError`. و`Retry-After: 1` على الضغط العابر وحده (`upload_requests_per_ip` و`core.ErrOverloaded`، وبدء الرفع فوق معدله بانتظاره)، **ولا يُرسل على `core.ErrUnavailable`**: عميل يحترمه قد يُسقط حدود إعادته (gtm-session-fetcher)، فيعيد بلا نهاية. وكل رد JSON بـ `Cache-Control: no-cache, no-store, max-age=0, must-revalidate` كالإنتاج. `gcs/resource.go`، و`gcs/errors.go`.

### الرفع والوسائط على GCS (S4-B، S4-C)

تختبرها `internal/services/storage/gcs/{upload,download}_test.go` طلبًا طلبًا كما يرسلها Node 8.4.0 وGo 1.62.1، و`TestStorageGCSWithTheAdminSDKs` في `test/acceptance` (بـ `ARUMA_GCS_SDK=1`) بالمكتبتين نفسيهما عبر وكيل يسجّل كل تبادل: `save()` في طلب واحد (`bytes 0-*/*`) ومقطّعًا (308 و`Range`) و`resumable:false`، وGo بـ multipart وبالجلسة (200 مع `X-Http-Status-Code-Override: 308`، والجزء الأخير الفارغ `bytes */N`)، والـ checksums المختلفة 400 بلا كائن، و`ifGenerationMatch=0` على موجود 412، و`download()` بتحققه الافتراضي من CRC، وقراءات Go بـ `WithJSONReads` كاملة وبنطاق ومثبّتة على generation، و`ReaderObjectAttrs.CacheControl` يساوي `Attrs().CacheControl`.

80. **مؤقت: إلغاء الجلسة** بـ `DELETE` على رابطها 499 بلا جسم (وثيقة GCS: «the response contains a 499 status code»)، ومرة ثانية 499، وبعد الإنهاء 400، وطلب بعده على الجلسة 410. لا يرسله أي من العميلين (B §10.3). وطلب على الجلسة بلا `Content-Range` هو الكائن كله في طلب واحد، كـ `bytes 0-*/*`. `gcs/upload.go` (`resumeUpload`).
81. **مؤقت: `upload_starts_per_minute_per_ip` حد العملاء وحدهم** (مراجعة S3-D): بدء الـ admin، وكل بدء على GCS بحساب خدمة، وعلى `/v0` بحساب خدمة أو رمز CLI للمالك، يُحسب على `upload_starts_per_second` للعملية وحده (media وmultipart وresumable)؛ بالحد الافتراضي كان استيراد Admin من عنوان واحد يفشل بعد 60 بدءًا في الدقيقة، و`save()` في Node بلا شرط مسبق لا يعيد (`ct` بالحدود الافتراضية: 27 من 101). وتجاوز سقف العملية على GCS 429 `rateLimitExceeded` مع `Retry-After`. وبدء المستخدم والمجهول على `/v0` بعنوان العميل: من `X-Forwarded-For` لوكيل في `trusted_proxies` وحده، وIPv6 لكل /64 (`clientIP`)، والتجاوز 503 مع `Retry-After`. `gcs/upload.go` (`allowStart`)، و`api/upload.go` (`startAddr`)؛ و`TestUploadStartRate`، و`TestStorageStartRateSparesAdmins`.
82. **مؤقت: ما لا تسجيل له في الرفع.** جسم أبطأ من `min_bytes_per_second` على 30 ثانية 408، وجسم انقطعت قراءته 400، وتجاوز حد الجسم عند المدخل 413 `uploadTooLarge`. و`Content-Length` يخالف طول `Content-Range` 400 قبل حفظ شيء، وطلب `bytes */*` بجسم 400. و`crc32c` أو `md5Hash` في JSON يناقضه `X-Goog-Hash` في الطلب نفسه 400. و`X-Upload-Content-Length` حجم معلن، والزيادة عليه 400 وتنهي الجلسة. `gcs/upload.go`.
83. **قيد: `Retry-After` على الضغط الحقيقي وحده** في الرفع والتنزيل أيضًا (معدل البدء، و`upload_requests_per_ip`، و`core.ErrOverloaded`)، لا على 503 من عطل الخادم (`core.ErrUnavailable`: أرضية القرص، أو القاعدة، أو التزام لم يُحسم، أو إيقاف الخادم): المكتبات التي تحترمه تعيد بلا حد. القاعدة واحدة في `gcs/errors.go` (`writeError`، البند 79)، وتختبرها `TestUploadUnavailableHasNoRetryAfter`.

### الرفع على `/v0` (S3-B)

يختبرها `api/upload_test.go` (جسم JSON للرفع، وحالة كل رد على رابط الجلسة، وما يُرفض قبل `core`، والمهلة المتدرجة على خادم بمهلات مصغّرة)، و`test/acceptance/storage_upload_test.go` (إعادة طلبات الويب المسجّلة في `sdk_upload.json`، وأشكال Android وiOS من A §3.10-3.13، وكل رفض نهائي، ورفع 64 ثانية وslowloris على مستمع الخادم نفسه بمهلاته الحقيقية).

- **`X-Goog-Upload-Status`:** `final` مع كل رفض نهائي (403 و402 والحجم المخالف وmd5Hash و413 و401) ومع كل تكرار له و`query` بعده؛ و`active` مع 2xx لجلسة حية، ومع الفجوة (400 والحجم المستلَم)، ومع 503 وحدها؛ ولا شيء على جلسة مجهولة (404) ولا على طلب لا يُقرأ.
- **المهلة:** مهلتا القراءة والكتابة تتحركان معًا نافذة 30 ثانية كلما جاءت النافذة بـ `min_bytes_per_second` × 30 بايتًا، ومهلة الكتابة تُضبط نافذة أخرى قبل الرد مباشرة، فيصل رد رفع أطول من 60 ثانية بعد الـ commit.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 84 | جسم دون `min_bytes_per_second` في نافذة 30 ثانية يُقطع 408 بلا `X-Goog-Upload-Status`، وما وصل منه يُحفظ | مؤقت | ADR-022 §6.4، A §5.3 | `api.pacedBody`، `api.chunk` |
| 85 | قطعة أو إنهاء على جلسة ملغاة 400 مع `final`، و`query` و`cancel` عليها 200 مع `cancelled` | مؤقت | ADR-022 §6.1، E §3.1 | `api.writeSession`، `api.uploadSession` |
| 86 | قطعة على جلسة انتهت بنجاح لا تأتي ببايت بعد `received` تكرار يُعاد له المحفوظ (200 و`final` والـ metadata)؛ وببايت بعده 400 مع `final` كالمحاكي | مؤقت | ADR-022 §6.1، `raw_resumable.json` | `api.chunk`، `api.pastReceived` |
| 87 | نوع المحتوى في multipart من الـ JSON وحده و`?name=` يغلب اسم الـ JSON (كالمحاكي)؛ وقيمة `X-Goog-Upload-Protocol` أخرى 400؛ و`md5Hash` ليس base64 لستة عشر بايتًا 400 | مؤقت | `raw_upload.json` | `api.multipart`، `api.upload`، `api.parseResource` |
| 88 | `X-Goog-Upload-Offset` إلزامي إلا لـ `finalize` منفردًا بجسم فارغ، و`X-Goog-Upload-Content-Length` على القطع لا يُقرأ (المعلن عند `start` هو الحكم) | مؤقت | A §3.11 | `api.chunk` |

### التنزيل ومصفوفة المتصفحات (S3-C)

`alt=media` على `/v0` يفتح الكائن بـ `core.Open` (الرمز وحده، أو القواعد) ثم `media.Serve`. يختبره `test/acceptance/storage_download_test.go` (من يفتح البايتات، والترويسات وCache-Control بالوضع، وRange، ورفض العمّال وسكربتات أصل اللوحة، وCORS الـ bucket على الرد ورفضه والـ preflight، وgzip كما خُزّن أو مفكوكًا، وقنبلة تُقطع عند `object_bytes` وتُعدّ).

ومصفوفة المتصفحات (ADR-022 §10.6) في `test/e2e/browser-matrix/run.sh`: حالات F §2.4 على Chromium وFirefox وWebKit (Playwright 1.64.1)، 14 من 14 على كل محرك، ولكل حماية حالة ضابطة تثبت أن الفحص يرى فشلها. والنتائج التي تحسم ما علّقه ADR-022 على المصفوفة:

- **HTML وSVG تحت `sandbox`** لا ينفّذان سكربتًا تصفحًا مباشرًا في المحركات الثلاثة، و`window.origin === "null"`، ولا كعكة اللوحة ولا `localStorage`. وSVG يُرسم، وفي `<img>` من أصل آخر أيضًا.
- **الفيديو والصوت تحت `sandbox` و`media-src 'self'`** يُعرضان ويُقفز فيهما تصفحًا مباشرًا وفي `<video>`/`<audio>` من أصل آخر، ومع COEP `require-corp`، في الثلاثة. فلا حاجة لرفع `sandbox` عن الوسائط. وWebKit يطلب بـ `Range` ويأخذ 206.
- **PDF يُنزَّل** تصفحًا مباشرًا، ولا يُعرض في `<iframe>` ولا `<embed>` من أصل آخر (`X-Frame-Options: DENY`).
- **CORS:** bucket بلا إعداد يرفض `fetch` و`getBytes` و`getBlob` من أصل آخر، و`cors.json` الموثّق (`{origin: ["*"], method: ["GET"], maxAgeSeconds: 3600}`) يسمح بها ومعها الـ preflight. المكتبة هي `firebase` 13.1.0 (بناء CDN) بـ `connectStorageEmulator` للمضيف وحده، لأن مسار الإنتاج للويب في S6.
- **Service Worker** من أصل اللوحة يُرفض، وكذلك `<script>` من أصلها، ويُحمَّل `<script>` من أصل تطبيق. والرفض 403 وحده هو المانع، بالطفرة على Chromium وFirefox (2026-10-24): مع إيقاف `media.refused` يُسجَّل رابط التنزيل Service Worker على أصل Aruma في Firefox (`registered`) فتفشل الحالة 8، ومعه تنجح. والحالة الضابطة في Firefox تسجّل عاملًا من أصل الصفحات، لأن Firefox في Playwright لا يمرر سكربت العامل عبر `route`.
- **حدود المصفوفة:** H.264 غير موجود في Chromium الذي يوزعه Playwright، فيُفحص فيه WebM (VP9)؛ وWebKit يعلن VP9 ولا يفك منه إطارًا (من Aruma ومن خادم ملفات عادي سواء)، فيُفحص فيه MP4 وحده. وFirefox على Linux بلا جهاز صوت (مشغّل GitHub) يفك MP3 ويقفز فيه (`decodeAudioData` و`<audio>` مكتومًا)، لكن صفحة الوسائط تشغّل الأغنية بصوت قبل أن يكتمها الاختبار فيفشل العنصر بـ `MEDIA_ERR_DECODE` (3)، وفي الطرفية `NS_ERROR_DOM_MEDIA_MEDIASINK_ERR` (`OnMediaSinkAudioError`). وكذلك Opus في WebM وOgg وWAV سواء (Firefox 157، Playwright 1.64.1، صورة `playwright:v1.64.1-noble`، 2026-10-24): العطل في المخرج لا في فك الترميز، فلا يُستبدل MP3 ولا تُخفَّف الحالة، ويعطي CI المشغّلَ مخرجًا صامتًا (PulseAudio null sink) ومعه تنجح. والحالة 11 (قنبلة gzip) في اختبارات `media` والقبول لا في المتصفح، والحالة 12 (`file.download()` في Node) لمسار GCS في S4-C.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 89 | `?token=` الفارغ لا رمز، فتقرر القواعد | مؤقت | ADR-022 §13 | `api.download` |
| 90 | تنزيل قررته القواعد بلا `Authorization` يحمل `private` أيضًا | مؤقت | ADR-022 §10.5 | `api.download` |

### مسار الإنتاج على الويب (S6-W)

تبديل النطاق وقت البناء (ADR-022 §16.1) في `@aruma/web` (`sdk/web/aruma-web`، والتفصيل في README هناك)، مقيسًا في `test/e2e/web-storage`: المكتبة الرسمية في وضع الإنتاج، مبنية من npm، أمام Aruma حقيقي بالقواعد، في Chromium بحارس الشبكة. والعمليات هي الإحدى عشرة في S0-B، والنتيجة 11/11 بلا ⚠ في كل سطر، والرابط الموحّد في `<img>` من جذر Aruma، وكل طلب للمكتبة على `/v0` بـ `Authorization: Firebase`، ولا طلب بالبادئة.

| الأداة | النتيجة | ملاحظة |
|---|---|---|
| Vite 8.3.5، webpack 5.112.2، esbuild 0.28.2، Rollup 4.65.0، Rspack 1.7.12 | 11/11 | `run.sh`، ومعها Web Worker يرفع ويأخذ الرابط في الخمسة (`worker.plugins` في Vite). والبناء يفشل برسالة `@aruma/web` حين لا وحدة Storage وحين يُستورد SDK من CDN في الخمسة، وحين يُترك SDK خارج الحزمة (`ssr.external` في Vite، و`externals` في webpack)، وحين يحمل worker في Vite بلا الإضافة نطاق Google |
| SvelteKit 3.1.0 (adapter-node) | الخادم والمتصفح | الخادم تحت netguard يرسم الرابط الموحّد ولا يتصل إلا بـ Aruma |
| Vite 5.4.21 و6.4.5 و7.3.7 | `vite build` وخادم التطوير: 11/11 | `optimizeDeps.esbuildOptions.plugins` يعمل، فلا رفض لما قبل Vite 8 في Storage |
| Next.js 16.4.3 | webpack وTurbopack (`next build` و`next dev`): 11/11، والمكوّن على الخادم و`getServerSideProps` في Pages Router يرسمان الرابط الموحّد | `transpilePackages` يضيفه السطر، وبدونه يكلّم خادم Pages Router نطاق Google (مقيس). ومحمّل `turbopack.rules` بشرط المسار يصل إلى `node_modules`. ولا نهاية بناء في Turbopack: لا يفشل بناء لم يصل إلى المكتبة |
| Expo 58.0.19 (Metro 0.87.1) | تصدير الويب 11/11، وحزمة Android بلا نص نطاق Google، ومع react-native-svg-transformer 1.5.3 قبله | وبعده يفشل التصدير (`processModuleFilter`)، على الويب وAndroid |
| Flutter 3.44.2 web | 11/11 من Dart | `firebase-storage.js` 13.1.0 من npm مبدَّلًا مرة واحدة، و`flutterfire_ignore_scripts`. ولم يُطلب `firebase-storage.js` من gstatic |
| الاحتياط (`installStorage` و`useArumaStorage`) | 11/11 | من حزمة تبقي نطاق Google |
| Auth وحده (`install()` بلا Storage، SR3-06) | الرفع و`getMetadata` يفشلان، ولا طلب إلى `firebasestorage.googleapis.com` | `@firebase/storage` 0.14.6 يأخذ رمز الهوية من Auth التطبيق أيًّا كان نطاقه (`service.ts:266-277`) ويضعه `Authorization: Firebase` في كل طلب (`implementation/request.ts:237`)، فيرفض `install()` طلب المكتبة إلى نطاق Google (XHR بعد ترويساته، وfetch) برسالة واحدة في الكونسول، وتوقف إضافة Vite بلا `storage: true` البناء الذي فيه `@firebase/storage`. و`fetch` لرابط تنزيل (بلا رمز هوية) يمر |

- **لم يُقَس:** Rolldown منفردًا (قيس داخل Vite 8)، وwebpack في وضع المراقبة، وFirefox وWebKit، وmetro على جهاز. والبناء من npm وحده: لم يُقارن `firebase-storage.js` في npm بنسخة gstatic بايتًا ببايت (لا طلب إلى Google).
- **رابط Google بعد التبديل:** `ref(storage, link)` لرابط على `firebasestorage.googleapis.com` يرمي `storage/invalid-url` (مقيس على بناء Node المبدَّل)، كما يرفض Firebase رابطًا على مضيف غير مضيفه. والمخرج `toArumaUrl(link, url)` قبله؛ و`gs://` والرابط الموحّد يعملان.
- **قيود مقصودة:** عنوان Aruma لـ Storage جذر النطاق (بلا مسار) واسم مضيف أو IP ومنفذ وحسب، وNode 20.19 فأحدث، وبناء الخادم لا يفشل لغياب Storage، وتطبيق Flutter web التجريبي يوجّه خطوط المحرك بعيدًا عن `fonts.gstatic.com` (ليس من Storage).

### من اختبارات Firebase الرسمية (S3-D)

ما أضافه تشغيل `test/compat/storage` (القسم «اختبارات Firebase الرسمية» أعلاه): اختياران كانا في الكود بلا بند، وقيد ظهر للاختبارات، وفرق في ردود `/v0` لم يُكتب.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 91 | رفع GCS بلا `uploadType`: media، أو multipart إن قالت `X-Goog-Upload-Protocol: multipart`؛ وقيمة أخرى للترويسة بلا `uploadType` 400. ولا مكتبة رسمية تحذفه | مؤقت؛ ودليل مخالف: مرجع GCS JSON لـ `objects.insert` يذكر `uploadType` في «Required query parameters»، والسلوك باقٍ حتى يُقاس الإنتاج | CT (`gcs.endpoints.test.ts:158-173`، `:306-318`)، والمحاكي | `gcs.classifyUpload` |
| 92 | قيمة `null` في الـ metadata المخصّصة لكائن جديد عبر GCS (رفع أو rewrite) تُخزَّن `""`، و`null` في PATCH يحذف المفتاح؛ و`/v0` واللوحة يحذفانها في الكائن الجديد كما كانا | مؤقت: النسخ مقيس في CT، والرفع بالقياس | CT (`gcs-js-sdk.test.ts:637-670`) | `core.incomingObject` |
| 93 | bucket واحد لكل مشروع ولا XML API (ADR-023 §2): نسخ إلى bucket آخر 404 «The specified bucket does not exist.» كأي bucket غير موجود، ورفع POST policy على `/<bucket>` وقراءة `/<bucket>/<object>` لا تُخدمان | قيد | CT (`other-bucket`، و`signed post policy` الـ `emulatorOnly`)، و`admin_go.json` | `gcs/gcs.go`، `core.Bucket` |
| 94 | ردود البدء والقطع والاستعلام والإلغاء على `/v0` بلا جسم (المحاكي `OK` نصًا)، وكل رد قطعة أو إنهاء يحمل `X-Goog-Upload-Size-Received` لا الاستعلام وحده. لا مكتبة تقرأ الجسم، والترويسة معلومة زائدة | مؤقت | A §7، و`raw_resumable.json`، و`sdk_upload.json` | `api.start`، `api.writeSession` |
