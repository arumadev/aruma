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
| 39 | `multipart/related` جزءان بالضبط، أولهما `application/json`، بلا ترميز نقل؛ و`X-Goog-Upload-Command` بأحرف صغيرة؛ و`Content-Range` بأشكاله الستة: غيرها 400 | قيد | التسجيلات كلها داخلها | `wire.NewRelatedReader`، `wire.ParseUploadCommand`، `wire.ParseContentRange` |
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
| 28 | بلا `Content-Disposition` مخزّنة: `attachment; filename*=utf-8''<آخر مقطع>`، بهروب `core.defaultDisposition` نفسه | مؤقت | CT، PY-RR | `media.filenameParam` |
| 29 | `If-Range` يقارن بـ `ETag` المحتوى (من `generation`) مقارنة قوية | مؤقت | ADR-022 §12 | `media.ifRangeHolds` |
| 52 | `Cache-Control` على GCS بلا قيمة: `private, max-age=0` | مؤقت | GCS لكائن غير عام | `media.cacheControl` |
| 53 | تنزيل القواعد يحمل `private` دائمًا؛ و`Content-Type` فيه فاصلة أو غير صالح يُخدم `octet-stream` | قيد | RFC 9111 §3.5، Fetch «extract a MIME type» | `media.cacheControl`، `media.contentType` |
| 54 | 304 قبل `Range` (فلا 416 لنسخة مخبأة)، و`If-Range` بتاريخ 200، وجسم 416، وgzip غير صالح يُخدم كما خُزّن | مؤقت | RFC 9110 §13 | `media.notModified`، `media.writeError`، `media.gzipHeaderOK` |

- **فك الضغط:** بلا `Accept-Encoding` يقبل gzip (و`gzip;q=0` صريحًا يغلب `*`) يُرسل المفكوك بلا `Content-Encoding` ولا `Content-Length`، ويُتجاهل `Range`، وتبقى `x-goog-stored-content-encoding: gzip` وهاشا المخزّن، فلا يتحقق Go (`uncompressedByServer`) ولا Node (`safeToValidate`) من هاش على غير ما خُزّن. و`no-transform` يمنعه. والمخرج محدود بـ `object_bytes`، وتجاوزه `media.ErrDecompressedTooLarge`.
- **القطع:** كل فشل بعد سطر الحالة يقطعه `Serve` نفسه: يبلّغ `Limits.Cut` (للمقاييس) ثم `panic(http.ErrAbortHandler)`، فيرى العميل اتصالًا مقطوعًا لا جسمًا قصيرًا مختومًا، ولو عاد المعالج عاديًا (`TestCutIsSeenByTheClient`). ولا يعيد خطأ إلا قبل أول بايت.

### الكائنات والقوائم والرموز وCORS (S2-C)

38. **مؤقت: CORS يطابق الأصل حرفيًا أو `*`، والطريقة حرفيًا أو `*`.** و`*` في الطرق أي طريقة، كما تقول وثيقة GCS (`json_api/v1/buckets`، `cors[].method[]`: «'*' is permitted in the list of methods, and means 'any method'»). ولا أنماط داخل الأصل (`https://*.example.com`)، ولا طي لحالة الأحرف: لم نجد دليلًا على ما يقبله GCS غير ذلك، فلا نكون أوسع منه. وأول قاعدة تطابق تقرر. `core/cors.go` (`MatchCORS`).
39. **مؤقت: `pageToken` مربوط بالقائمة كلها:** المشروع والـ bucket والبادئة والفاصل و`startOffset` و`endOffset` والسطح، لا الخمسة الأولى وحدها (ADR-022 §13). فرمز صفحة على قائمة بإزاحة أخرى أو من سطح آخر 400. وحجم الصفحة يتغير بين الصفحات بلا رفض. `core/pagetoken.go`.
40. **مؤقت: `delete_token` لرمز ليس للكائن يرفع الـ metageneration وحده** ولا يُرد بخطأ، كما يفعل المحاكي (`tokens.json`). `core/tokens.go`.
41. **قيد: الرموز تُحسب في `metadata_bytes`** (8 KiB) كما يحملها GCS في الـ metadata المخصّصة تحت `firebaseStorageDownloadTokens`. فـ `create_token` يُرفض 400 حين تتجاوز الرموز والـ metadata معًا الحد (قرابة مئتي رمز لكائن بلا metadata). `core/metadata.go` (`metadataSize`).
42. **مؤقت: `"metadata": null` على GCS يمسح الرموز أيضًا**، لأنها في الـ metadata المخصّصة هناك (دلالة GCS)، فيُبطل الرابط القديم، والقراءة التالية المصرّح بها على `/v0` تولّد رمزًا. وعلى `/v0` يمسح الـ metadata المخصّصة وحدها (البند 17). `core/metadata.go` (`surfacePatch`).
43. **قيد: مفاتيح الـ metadata المخصّصة غير فارغة وبلا محارف تحكم، ولا NUL في قيمها** (jsonb لا يحفظه)، و`DEL` مرفوض مع ما دون U+0020 في الحقول الخمسة التي تصير ترويسات (ومنها TAB). القيم تقبل السطر الجديد. `core/metadata.go` (`checkPatch`).
44. **مؤقت: البادئة بمقطع فارغ على `/v0`:** تُحذف البادئة التي فيها مقطع فارغ قبل الفاصل الأخير (`listAll//`)، وتبقى `listAll/bar/` من `listAll/bar//` (CT `should not include show invalid prefixes and items`). وبادئة طلب تبدأ بـ `/` أو فيها `//` قائمة فارغة. **وما يُحذف يُحسب على الصفحة** كما لو أُعيد، فالصفحة لا تفحص أكثر من `maxResults` مدخلًا ولا تتجاوز `maxResults+1` بحثًا، وقد تأتي ناقصة أو فارغة ومعها `nextPageToken` (والمكتبات تتابع الرمز). `core/names.go` (`prefixHasEmptySegment`)، و`core/list.go`.
