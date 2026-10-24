# توافق Cloud Storage for Firebase: الإصدار 0.3

- **الحالة:** هيكل بدأه S2-B، 2026-10-24، بفروق صيغ السلك. يكمله S3-D بنتائج مجموعة Storage في firebase-js-sdk وإعادة تسجيلات المحاكي، وS7 بأرقامه (storage-layout.md).
- **المرجع السلوكي:** الإنتاج حيث رُصد (`test/oracle/storage/production`، وملاحظات Pyric على Storage حقيقي)، ثم المحاكي الرسمي من firebase-tools 15.34.0 (`test/oracle/storage/emulator`)، ثم مجموعات Google. والفروق كلها بتصنيفها في ADR-023 §3، وهذه الوثيقة تذكر لكل منها مصدره وموضعه في الكود.

## صيغ السلك (`internal/services/storage/wire`)

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

## خدمة البايتات (`internal/services/storage/media`)

تختبرها `headers_test.go` (كل نوع على كل وضع)، و`fetch_test.go` (جدول `Sec-Fetch-Site` × `Sec-Fetch-Dest` كاملًا)، و`range_test.go`، و`gunzip_test.go` (قنبلة 1 GiB بذاكرة ثابتة)، و`http_test.go` (خادم حقيقي: الهاشان كما يحللهما Go وNode، والقطع، ومهلة الكتابة). مصفوفة المتصفحات في S3-C.

| # (ADR-023 §3) | الفرق | التصنيف | المصدر | الموضع |
|---|---|---|---|---|
| 1 | كل الأنواع تحت `sandbox`، وPDF `attachment` قسرًا | قيد | ADR-022 §10.2 | `media.setHeaders`، `media.disposition` |
| 2 | 403 لطلبات العمّال (`Service-Worker`، و`Sec-Fetch-Dest` worker وsharedworker وserviceworker) من أي موقع، ولوجهات السكربت والأسلوب والـ worklets و`xslt` من `same-origin` | قيد | ADR-022 §10.1 | `media.refused` |
| 21 | `Cache-Control` الافتراضي `private, max-age=0` على رابط الرمز | مؤقت | ADR-022 §10.5 | `media.cacheControl` |
| 28 | بلا `Content-Disposition` مخزّنة: `attachment; filename*=utf-8''<آخر مقطع>`، بهروب `core.defaultDisposition` نفسه | مؤقت | CT، PY-RR | `media.filenameParam` |
| 29 | `If-Range` يقارن بـ `ETag` المحتوى (من `generation`) مقارنة قوية | مؤقت | ADR-022 §12 | `media.ifRangeHolds` |
| 41 | `Cache-Control` على GCS بلا قيمة: `private, max-age=0` | مؤقت | GCS لكائن غير عام | `media.cacheControl` |
| 42 | تنزيل القواعد يحمل `private` دائمًا | قيد | RFC 9111 §3.5 | `media.cacheControl` |
| 43 | 304، و`If-Range` بتاريخ 200، وجسم 416، وgzip غير صالح يُخدم كما خُزّن | مؤقت | RFC 9110 §13 | `media.notModified`، `media.writeError`، `media.gzipHeaderOK` |

- **فك الضغط:** بلا `Accept-Encoding: gzip` (أو `gzip;q=0`) يُرسل المفكوك بلا `Content-Encoding` ولا `Content-Length`، ويُتجاهل `Range`، وتبقى `x-goog-stored-content-encoding: gzip` وهاشا المخزّن، فلا يتحقق Go (`uncompressedByServer`) ولا Node (`safeToValidate`) من هاش على غير ما خُزّن. و`no-transform` يمنعه. والمخرج محدود بـ `object_bytes`، وتجاوزه `media.ErrDecompressedTooLarge`.
- **القطع:** كل خطأ يعيده `Serve` يعني ردًا ناقصًا، والمعالج يعدّه ثم `panic(http.ErrAbortHandler)`، فيرى العميل اتصالًا مقطوعًا لا جسمًا قصيرًا مختومًا (`TestCutIsSeenByTheClient`).
