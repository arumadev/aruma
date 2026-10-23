# هيكل كود Cloud Storage for Firebase، وتقسيم S2 إلى S6

- **الحالة:** مقترح مع [ADR-022](../adr/ADR-022-storage-architecture.md) و[ADR-023](../adr/ADR-023-v03-scope.md)، 2026-10-24. يُحدَّث حيث يخالفه الكود، وكل ما لم يكن فيه يُعلَّم «لم يكن في المسودة»، كما في [rules-layout.md](rules-layout.md).
- **الأساس:**
  - ADR-022 (المعمارية) وADR-023 (النطاق).
  - ADR-015 (حدود الحزم وarchtest، وإضافة خدمة جديدة)، وADR-017 (`project_number` و`scopecheck` واختبارات العزل، بلا RLS)، وADR-019 (كاتب واحد).
  - ADR-021 و[rules-layout.md](rules-layout.md): عقد المحرك (`Compile` و`Evaluate` و`Request` و`Segment` و`NoDocuments` و`Inflight`)، و`tenancy/core.CompiledRelease`.
  - RTDB نمطًا: `internal/services/rtdb/{core,api,console,queries,wire}`، و`rtdb/api` في التحقق من اعتماد Admin (`Credentials` و`Projects`)، و`OnProjectPurged` (`internal/app/app.go:163`).
- **ما يتوقف على غيره:**
  - **S1 مدموج** قبل أن ينتهي S2-E (ربط القواعد). ما قبله يعمل على عقد S1-0 المدموج (`305f596`).
  - **أوراكل القواعد** (`origin/s0-rules-oracle`) يُدمج قبل S2-E، ففيه ملفات Pyric التي يطابقها ربط `request.resource` و`resource`.

## الصورة العامة

```
                  ┌──────────────────────── internal/services/storage ───────────────────────┐
 /v0/…  ─────────►│ api ─┐                                                                   │
 /firebasestorage │      ├──► core ──► queries (sqlc، storage.*)                            │
 .googleapis.com/ │ gcs ─┤     │  └──► blob (قرص محلي: p/<n>/<uuid>، s/<n>/<uuid>)          │
 /storage/v1/…    │      │     ├──► platform/rules (Evaluate، NoDocuments، Inflight)         │
 /upload/storage/…│      │     └──► tenancy (ProjectForBucket، CompiledRelease)              │
 /download/…      │      └──► media (ترويسات الخدمة الآمنة، Range، فك الضغط)                │
                  │ console ──► core          wire (صيغ السلك والمحللات، Go القياسية وحدها)  │
                  └─────────────────────────────────────────────────────────────────────────┘
 internal/admin/mgmt: firebaserules v1، وdefaultBucket، وService Usage (فوق tenancy، لا Storage)
 internal/app: يبني core بـ blob.Disk وrules.NoDocuments{} وInflight، ويسجّل PurgeProject، ويشغّل Run
```

**اتجاه الاعتماد** (archtest):

- **`internal/services/storage/blob` و`internal/services/storage/wire`:** Go القياسية وحدها. قاعدتان جديدتان `"internal/services/storage/blob": {}` و`"internal/services/storage/wire": {}`، كما `rtdb/rules` و`rtdb/wire` (`archtest_test.go:31-33`)، وحالات في `TestRulesThemselves`.
- **`core`:** تستورد `blob` و`queries` و`platform/rules` و`platform/crypto` و`tenancy` و`store`. ولا تستورد `api` ولا `gcs` ولا `console` ولا `media` ولا `platform/httpx`: القاعدة القائمة `internal/services/*/core` (`archtest_test.go:56`) تمنع الثلاث الأولى و`httpx`، وتُضاف `internal/services/*/media` إلى قائمة المنع، فالنواة لا تعرف HTTP.
- **`media`:** تستورد `wire` وGo القياسية. لا `core`: تأخذ ما تخدمه قيمةً (`media.Item`) وقارئًا.
- **`api` و`gcs` و`console`:** تستورد `core` و`media` و`wire` و`platform/*`. ولا يستورد أحدها الآخر (حالة في `TestRulesThemselves`).
- **Storage لا يستورد خدمة أخرى** (القاعدة القائمة `internal/services/*`)، و`admin/mgmt` لا يستورد Storage (القاعدة 3 في ADR-015).

## الشجرة

العمود الأخير هو المهمة (القسم الأخير). `0` هو commit العقد S2-0.

### `internal/services/storage/core/`: النواة

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | وصف الحزمة: bucket واحد لكل مشروع، والقرارات المؤقتة (ADR-023 §3) بموضعها في الكود | 0 |
| `api.go` | **العقد العام** (القسم «عقد S2-0»): `Service` و`New` و`Options` و`Principal` و`Bucket` و`Object` و`Patch` و`Preconditions` و`Checksums` و`UploadSpec` و`Chunk` و`UploadState` و`ListOptions` و`ListPage` و`CORSRule` و`Usage`، وتوقيعات كل الطرق | 0 |
| `errors.go` | الأخطاء المسمّاة (`ErrNotFound` و`ErrBucketNotFound` و`ErrPermissionDenied` …، و`InvalidError{Reason, Message}` و`OffsetError{Received}`). كل واجهة تربطها بحالتها وجسمها | 0 |
| `limits.go` | `Limits` (ADR-022 §17) و`DefaultLimits()` و`Validate()` | 0 |
| `bucket.go` | `Bucket(ctx, name)`: الحل عبر `tenancy.ProjectForBucket`، و`ErrBucketNotFound` لغير `ACTIVE` | 0 |
| `names.go` | فحص اسم الكائن: UTF-8 صالح، 1..1024 بايت، بلا CR/LF/NUL، ليس `.`/`..`، ولا `.well-known/acme-challenge/` (ADR-022 §17). وتقسيم الاسم إلى مقاطع للقواعد، و«مقطع فارغ» لاستبعاد `/v0` من القوائم | C |
| `metadata.go` | الحقول القابلة للكتابة وحدودها (8 KiB للمخصّصة، 1 KiB ورفض CR/LF وما دون U+0020 للحقول التي تصير ترويسات)، و**حذف `firebaseStorageDownloadTokens` من metadata العميل** (`StripClientTokens`)، وتطبيق `Patch` (`null` يحذف، ودمج مفتاحًا مفتاحًا، و`ClearMetadata`)، والقيم الافتراضية عند الإنشاء: `contentDisposition` = `inline; filename*=utf-8''<هروب PY-RR>` لـ `/v0` وحده، و`contentType` = `application/octet-stream` | C |
| `objects.go` | `Stat` (ومعه توليد الرمز الذري لـ `/v0`) و`Open` (الرمز أو القواعد، و`generation`) و`Update` و`Delete` و`Rewrite` (ينسخ الرموز ما لم تُستبدل الـ metadata، ويكتب عبر `commitObject`) | C |
| `tokens.go` | توليد UUID، والمقارنة بزمن ثابت، و`CreateToken`/`DeleteToken` (حذف الأخير يولّد غيره)، وعرضها لـ GCS في `metadata.firebaseStorageDownloadTokens` وإخفاؤها من `/v0` | C |
| `list.go` | المشي على الفهرس بالقفز فوق البادئات (≤ `maxResults+1` بحثًا)، و`StartOffset`/`EndOffset`، واستبعاد ذوات المقطع الفارغ في `/v0` | C |
| `pagetoken.go` | `pageToken` معتم: HMAC بمفتاح من `crypto.Keys` على (المشروع، والـ bucket، والبادئة، والفاصل، وآخر اسم). غير صالح = `ErrInvalidPageToken` | C |
| `cors.go` | `CORS` و`SetCORS` (دلالة GCS `cors[]`، وفحص الحقول)، و`MatchCORS(origin, method)` الذي تستدعيه الواجهتان لـ `alt=media` | C |
| `rules.go` | **ربط القواعد** (ADR-022 §8): بناء `request` (`auth` و`path` و`time` و`resource` و`method` و`params` الفارغة) و`resource`، والمقاطع `b/<bucket>/o/…`، و`UnknownSegment` لـ list، والطريقة لكل عملية (الكتابة فوق موجود `create`)، ورفض list على الإصدار 1، و`Budget` لكل طلب، و`CompiledRelease("firebase.storage/<bucket>")`، و`ErrOverloaded` من `rules` | E |
| `rules_fields.go` | مفاتيح `request.resource` الخمسة عشر عند الإنشاء وعند PATCH وقيمها (`null` لـ `generation`/`metageneration`/`etag`/`storageClass` عند الإنشاء، وثباتها عند PATCH، وبلا `timeCreated`/`updated`)، ومفاتيح `resource` من `stdlib-realstorage-p2-native-object-fields.json`. ملف منفصل لأنه جدول يُطابَق بالأوراكل | E |
| `uploads.go` | آلة حالات الجلسة: `StartUpload` (حجز الحجم المعلن، وحدود الجلسات)، و`WriteUpload` (الإزاحة: إلحاق، أو تجاهل التداخل، أو `OffsetError`؛ والتدفق إلى staging مع `Sync` قبل تحديث `received`؛ وقص الملف إلى `received` بعد انهيار)، و`QueryUpload`، و`CancelUpload`، والإنهاء (الحجم المعلن، والـ checksums، والقواعد بهوية `start`، و`blob_key` قبل النقل، ثم `commitObject`)، والإنهاء المكرر يعيد النتيجة المحفوظة | D |
| `insert.go` | `Insert` لرفع media وmultipart (جسم واحد): الكتابة المباشرة إلى مفتاح جديد، والقواعد، و`commitObject` | D |
| `hashes.go` | `crc32c` (Castagnoli) و`md5` أثناء التدفق، وحفظ حالتهما مع الإزاحة (`encoding.BinaryMarshaler`) واستعادتها | D |
| `commit.go` | **`commitObject`**: المعاملة الوحيدة التي تجعل ملفًا مرئيًا: `SET LOCAL synchronous_commit TO on`، والشروط المسبقة، وصف الكائن (`generation` = `max(now_µs, السابق+1)`)، وصف `blob_gc` للقديم، وتحويل الحصة. ومسار الخطأ يحذف الملف الجديد متزامنًا | D |
| `quota.go` | الحجز لكل قطعة (`UPDATE … WHERE used+reserved+$n <= quota`)، وخطوات 8 MiB حين لا يُعرف الطول، والرد والتحويل، والمساحة الحرة الدنيا | D |
| `gc.go` | عامل `blob_gc` بعد `gc_grace`، وانتهاء الجلسات (حذف staging وتحرير الحجز) | D |
| `sweep.go` | المسح عند الإقلاع (يحذف ما هو أقدم من الإقلاع بلا صف كائن ولا `staging_key`/`blob_key` لجلسة حية)، والمسح الدوري (يبلّغ فقط) | D |
| `purge.go` | `PurgeProject`: staging، ثم `p/<n>/`، ثم الصفوف. idempotent وقابلة للاستئناف | D |
| `run.go` | `Run(ctx)`: المسح عند الإقلاع في الخلفية، وGC، وانتهاء الجلسات، والمسح الدوري، حتى يُلغى `ctx` | D |
| `*_test.go` | لكل ملف مالكه. وأهمها: `uploads_test.go` (كل أشكال الإزاحة، والإنهاء المكرر، والانهيار بين `Sync` و`received`، وبين النقل والـ commit)، و`commit_test.go` (الترتيب، و`synchronous_commit`، والشروط المسبقة تحت تزامن)، و`quota_test.go` (مئة رفع متوازٍ لا تتجاوز الحصة)، و`sweep_test.go` (لا يحذف أحدث من الإقلاع ولا ما تشير إليه جلسة)، و`rules_test.go` (جدول ADR-022 §8 حالةً حالة، ومنه الكتابة فوق موجود `create` و`resource` الموجود) | C، D، E |

### `internal/services/storage/blob/`: البايتات (Go القياسية وحدها)

| الملف | ما فيه | المهمة |
|---|---|---|
| `blob.go` | **العقد:** `Store` و`Writer` و`Reader` و`Staging`، و`ObjectKey(project)` و`StagingKey(project)` (UUID من `crypto/rand`)، و`ValidKey` (`^(p|s)/[0-9]+/[0-9a-f-]{36}$`) | 0 |
| `disk.go` | `NewDisk(root)`: التفرع `p/<n>/<ab>/<uuid>`، و`Create` بـ `O_EXCL`، و`Commit` (fsync للملف ثم للمجلد ثم إغلاق)، و`Open` (`io.ReadSeekCloser` وحجمه)، و`Delete` idempotent، و`Promote(staging, key)` (`rename` على القرص نفسه ثم fsync للمجلدين)، ورفض أي مفتاح غير صالح قبل لمس القرص | A |
| `staging.go` | ملف الجلسة: فتح أو إنشاء، و`Truncate(n)`، و`Append(r, limit)` يعيد ما كُتب، و`Sync`، و`Size` | A |
| `walk.go` | `Walk(prefix, fn(key, modTime))` بترتيب ثابت، و`DeletePrefix(prefix)` قابل للاستئناف (لـ `PurgeProject`)، و`FreeBytes()` (`syscall.Statfs`، ومع Windows بديل أو «غير مدعوم» صريح) | A |
| `*_test.go` | رفض المفاتيح الغريبة (`..`، و`/`، وحالة الأحرف، وNUL)، و`O_EXCL` يمنع الكتابة فوق موجود، و`Promote` بين مجلدين، و`Walk` على شجرة كبيرة، و`FuzzKey` | A |

### `internal/services/storage/wire/`: صيغ السلك (Go القياسية وحدها)

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | المصادر: A وB وD وE، وقاعدة «لا `null` على `/v0`» | 0 |
| `firebase.go` | `FirebaseObject` (كل حقل نص بـ `omitempty`، و`downloadTokens` نص بفواصل، و`metadata` خريطة نص إلى نص)، و`FirebaseList` (`prefixes` و`items` بلا `omitempty`، و`nextPageToken` بـ `omitempty`)، و`FirebaseError` | 0 ثم B |
| `gcs.go` | `GCSObject` (`kind` و`id` و`selfLink` و`mediaLink`، والأرقام نصوص)، و`GCSObjects` و`GCSBucket` (ومعه `cors`) و`GCSRewrite` و`GCSError` (`code` رقم = الحالة، و`errors[]`) | 0 ثم B |
| `format.go` | `Time(t)` (`2006-01-02T15:04:05.000Z`)، و`Int(n)` نصًا، و`CRC32C(v)` (base64 لأربعة بايتات BE مع الحشو)، و`MD5(sum)`، و`ETag(gen, metagen)`، و`HashHeader(crc, md5)` (ترويسة واحدة بلا مسافة)، و`ContentDispositionDefault(name)` بمجموعة الهروب المطابقة لـ PY-RR | B |
| `headers.go` | أسماء الترويسات بإملاء Google (`X-Goog-Upload-URL` …)، و`Set(h, name, v)` بالإسناد المباشر إلى الخريطة | 0 |
| `path.go` | `SplitObjectPath(escaped)`: من `EscapedPath()`، يقسم إلى `b/<bucket>/o/<object>` **قبل** الفك، ويفك الاسم مرة واحدة، ويرفض الترميز الفاسد. والشكل المسبوق `/firebasestorage.googleapis.com` | B |
| `upload.go` | `ParseUploadCommand` (قائمة بفواصل ومسافات)، و`ParseContentRange` (الأشكال الخمسة في B §10.2-5)، و`DeclaredLength(h)` (الويب وiOS، أو -1)، و`DeclaredType(h)` | B |
| `multipart.go` | `NewRelatedReader(body, contentType, maxJSON)`: جزء JSON بسقف، ثم قارئ البايتات تدفقًا، ويقبل غياب CRLF بعد الحد الختامي | B |
| `range.go` | `ParseRange(h, size)`: نطاق واحد، والفارغ يتجاهل، وغير المرضي 416، والمتعدد يتجاهل | B |
| `nonull_test.go` | `AssertNoNull(t, body)` يُصدَّر لاختبارات `api` عبر حزمة اختبار (`wire/wiretest`) | B |
| `fuzz_test.go` | `FuzzSplitObjectPath` و`FuzzContentRange` و`FuzzUploadCommand` و`FuzzRelated` و`FuzzRange`: لا ذعر، وحدود الذاكرة | B |

### `internal/services/storage/media/`: خدمة البايتات بأمان

| الملف | ما فيه | المهمة |
|---|---|---|
| `media.go` | **العقد:** `Item` (النوع والترميز و`Content-Disposition` و`Cache-Control` والاسم والحجم والـ ETag و`Updated` و`Generation`/`Metageneration` والهاشان)، و`Mode` (`RulesDecided` و`Token` و`Console`)، و`Serve(w, r, it, body io.ReadSeeker, m Mode, lim Limits)` | 0 |
| `headers.go` | جدول ADR-022 §10.1 و§10.2: `nosniff`، وCSP واحدة بـ `sandbox` (والوسائط تزيد `media-src 'self'`)، و`CORP: cross-origin` فوق العامة، وPDF `attachment`، و`Cache-Control` (`public` إلى `private` حين قررته القواعد، و`private, max-age=0` افتراضيًا)، ولا ترويسة فارغة | F |
| `serve.go` | `Content-Type` قبل أي كتابة، وRange (`wire.ParseRange`، والفارغ 200)، و`If-Range`، والمهلة المتدرجة للكتابة عبر `ResponseController`، ورفض `Service-Worker: script` و`Sec-Fetch-Dest` العمّال 403 | F |
| `gunzip.go` | فك الضغط تدفقًا (`gzip.Reader`) حين المخزّن gzip والطلب بلا `Accept-Encoding: gzip` ولا `no-transform`، وسقف `object_bytes` للمخرَج، وقطع الاتصال عند التجاوز | F |
| `*_test.go` | الترويسات لكل نوع (HTML وSVG وPNG وPDF وMP4 وغير المعروف)، وملف `x.html` بلا نوع يخرج `octet-stream`، وRange على الفارغ وغير الفارغ والمتعدد، وقنبلة gzip بذاكرة ثابتة | F |

### `internal/services/storage/api/`: Firebase REST (`/v0`)

| الملف | ما فيه | المهمة |
|---|---|---|
| `api.go` | `New(svc, Options{PublicURL, Credentials authn.Verifier, IDTokens, CORS…}, log)` و`Handler()`، والتوجيه على `EscapedPath()` لـ `/v0` و`/firebasestorage.googleapis.com/v0`، ومسار مجهول 400 «Invalid HTTP method/URL pair.» | 0 ثم S3-A |
| `auth.go` | `Authorization: Firebase <idToken>` (عبر `idtoken`، و401 للرمز غير الصالح)، و`Bearer <access token>` لـ Admin (`authn`، مقيد بمشروعه)، وغيرهما مجهول. وتجاهل App Check وGMPID | S3-A |
| `errors.go` | ربط أخطاء `core` بـ `{"error":{"code","message"}}`: 404 «Not Found.»، و403 «Permission denied.»، و402 للحصة، و503 مع `Retry-After` محدود، ولا 409 ولا 308 | S3-A |
| `cors.go` | CORS مفتوح (`*`) لكل ما ليس `alt=media`، والترويسات المسموحة والمكشوفة (A §6.1)، والـ preflight 204. و`alt=media` بـ `core.MatchCORS` | S3-A |
| `objects.go` | GET metadata (مع التوليد)، وPATCH و`PUT`+`X-HTTP-Method-Override: PATCH`، وDELETE (204)، و`create_token`/`delete_token` | S3-A |
| `list.go` | القائمة بشكل `/v0` | S3-A |
| `resource.go` | من `core.Object` إلى `wire.FirebaseObject` (`contentEncoding` = `identity` حين غاب، والرموز في `downloadTokens` لا في `metadata`) | S3-A |
| `upload.go` | تحديد النوع (multipart، resumable، media)، و`start` على `/o` و`/o/{object}` بـ `POST`/`PUT`، و`X-Goog-Upload-URL` مطلق من `PublicURL`، والقطع و`query` و`cancel` و`finalize` بـ `POST`/`PUT` على رابط الجلسة، والمهلة المتدرجة للقراءة وأدنى معدل | S3-B |
| `multipart.go` | multipart عبر `wire.NewRelatedReader` إلى `core.Insert` | S3-B |
| `download.go` | `alt=media`: الرمز أو القواعد عبر `core.Open`، ثم `media.Serve` | S3-C |
| `*_test.go` | جداول على `httptest` فوق `core` حقيقية وPostgres (`storetest`)، و`wiretest.AssertNoNull` على كل رد | S3-A إلى C |

### `internal/services/storage/gcs/`: GCS JSON (Admin)

| الملف | ما فيه | المهمة |
|---|---|---|
| `gcs.go` | `New(svc, Options{PublicURL, Credentials, Projects}, log)` و`Handler()`، والتوجيه تحت `/storage/v1` و`/upload/storage/v1` و`/download/storage/v1`، وتجاهل `alt=json` و`prettyPrint` و`projection` و`fields` و`userProject`، والمعاملات الفارغة غائبة | 0 ثم S4-A |
| `auth.go` | `Bearer` حساب خدمة إلزامي (`authn.Verifier`)، والمشروع من الـ bucket يطابق `Principal.ProjectNumber`، والجلسات بالمعرّف وحده | S4-A |
| `errors.go` | `wire.GCSError` بـ `reason` (`notFound` و`conditionNotMet` و`invalid` و`forbidden` و`required` و`quotaExceeded`)، و501 لما أُجّل (ACL و`compose` …) | S4-A |
| `objects.go` | `objects.get/list/patch/delete`، و`?generation=` للإصدار الحالي وحده، و`rewriteTo` (`done:true`) | S4-A |
| `buckets.go` | `buckets.get` و`buckets.patch` (`cors` وحده، وغيره 400) | S4-A |
| `resource.go` | من `core.Object` إلى `wire.GCSObject` (`metadata.firebaseStorageDownloadTokens` ظاهرة) | S4-A |
| `upload.go` | `uploadType` media وmultipart وresumable، و`upload_id` يغلب الطريقة و`uploadType`، و`Content-Range` بأشكاله، و308 مع `Range` لـ Node، و200 مع `X-Http-Status-Code-Override: 308` لـ `X-GUploader-No-308`، و`X-Goog-Hash` والـ checksums في JSON، والشروط المسبقة | S4-B |
| `download.go` | `alt=media` تحت `/storage/v1` و`/download/storage/v1` عبر `media.Serve` (و`x-goog-*` كلها) | S4-C |

### `internal/services/storage/console/`: اللوحة

| الملف | ما فيه | المهمة |
|---|---|---|
| `console.go` | `Routes(svc)` لـ `consolemount`: التصفح بالبادئات، والرفع (multipart عبر `core.Insert`)، والتنزيل (`media.Serve` بـ `Mode.Console`: `attachment` دائمًا)، والحذف، وتعديل الـ metadata، والرموز، وCORS، والاستعمال. والقواعد عبر `tenancy` (قراءة الإصدارة، والتحقق، والنشر). وسجل التدقيق لكل كتابة | S5-B |

### `internal/services/storage/queries/`

| الملف | ما فيه | المهمة |
|---|---|---|
| `objects.sql` | الكائنات، والقائمة بالقفز، والرموز، وCORS | 0 (المخطط والهياكل) ثم C |
| `uploads.sql`، `usage.sql`، `gc.sql` | الجلسات، والحصة، وoutbox الحذف، والمسح. استعلامات العامل والمسح `-- scope: global` بسببها | 0 ثم D |
| `scope_test.go` | **الجداول الخمسة في `TestQueriesAreProjectScoped`** | 0 |
| `*.sql.gen.go`، `db.gen.go`، `models.gen.go` | مولّدة بـ sqlc، تُودَع مع كل تغيير | كل من يغيّر SQL |

### خارج الحزمة: ما يُضاف أو يتغير

| المكان | التغيير | المهمة |
|---|---|---|
| `internal/store/migrations/storage/00001_init.sql` **جديد** | `storage.objects` و`upload_sessions` و`blob_gc` و`usage` و`buckets` (ADR-022 §3)، وفي كل منها `project_number … REFERENCES control.projects … ON DELETE CASCADE`، والأسماء `COLLATE "C"`. وسطر في `migrations.Schemas` ونمط `embed` و`sqlc.yaml`، ثم `-update` لـ `released.lock` | 0 |
| `internal/store/migrations/control/` (migration جديد) | `control.rulesets` و`control.rules_releases` كما في rules-layout.md:115 | E |
| `internal/tenancy/core/storage.go` **جديد** | `ProjectForBucket(ctx, bucket) (Project, error)` (اللاحق `.<project_domain>`، و`ACTIVE` وحده) | 0 |
| `internal/tenancy/core/rules.go` و`internal/tenancy/queries/` | حفظ الـ rulesets ونشر الإصدارات، و`CompiledRelease` بـ LRU بالوزن، والإصدارة الافتراضية المقفلة لـ `firebase.storage/<bucket>` عند أول قراءة لمشروع بلا إصدارة، والجدولان في `scope_test.go` (rules-layout.md:115-117) | E |
| `internal/archtest/archtest_test.go` | قاعدتا `blob` و`wire`، و`internal/services/*/media` في قائمة منع `core`، وحالات `TestRulesThemselves` | 0 |
| `internal/config/config.go` | قسم `storage`: `enabled` و`data_dir` و`limits` (ADR-022 §17) و`gc_grace`، ومتغيرات `ARUMA_STORAGE_*`، و`Limits.Validate` عند التحميل | 0 |
| `internal/server/server.go` | `Handlers.Storage` و`Handlers.GCS`، والبادئات (ADR-022 §1)، وتركيبهما بـ `CommonWithLimit` بحد `object_bytes + 1 MiB`، و`/v1/` و`/v1alpha/` لـ `Mgmt`. وحالات في اختبار جدول البادئات | 0 |
| `internal/app/app.go` | `core.New` بـ `blob.NewDisk(cfg.Storage.DataDir)` و`rules.NoDocuments{}` و`rulesInflight` و`svc` (tenancy)، و`svc.OnProjectPurged(storageSvc.PurgeProject)`، و`go storageSvc.Run(ctx)`، وتركيب `api` و`gcs` و`console.Routes` | 0 ثم D وS3 وS4 وS5 |
| `internal/admin/mgmt/rules.go` **جديد** | firebaserules v1 (R1 إلى R8، C §1.5) فوق `tenancy`، و`:test` بـ `rules.Compile` و`Inflight` نفسه | S5-A |
| `internal/admin/mgmt/storage.go` **جديد** | `GET /v1alpha/projects/{p}/defaultBucket`، و`GET /v1beta/projects/{p}/buckets`، و`GET /v1/projects/{p}/services/{name}` (`ENABLED`) و`:enable` | S5-A |
| `internal/app/testdata/console_routes.txt` | مسارات الكونسول الجديدة، بـ `-update` ومراجعة الفرق | S5-B |
| `dashboard/src/features/storage/` **جديد** | الملفات، ومحرر القواعد (أخطاء `issues[]` على الأسطر)، والإعدادات (CORS)، والاستعمال | S5-C |
| `test/acceptance/storage_*_test.go` **جديد** | فوق خادم حقيقي وPostgres: كل عملية `/v0` وGCS، والانهيار المحاكى (قتل العملية بين الخطوات في `tools/storage-chaos`، اختياري في S7) | S3، S4 |
| `test/acceptance/isolation_test.go` | حالات Storage (ADR-022 §3): رابط رفع عبر مشروعين، ورمز تنزيل، وحساب خدمة على bucket آخر وrewrite إليه، و`pageToken`، وruleset | D، C، S4-A، E |
| `test/compat/storage/` **جديد** | مجموعة Storage في firebase-js-sdk عبر حاضنة `test/compat/firebase-js`، واختبارات المطابقة في firebase-tools (غير `emulatorOnly`)، وإعادة `test/oracle/storage/emulator/*.json` على Aruma مع جدول الفروق | S3-D |
| `test/smoke/admin-node`، `test/smoke/admin-go` | سيناريو Storage بـ `@aruma/admin-storage` و`adminstorage` (الرفع بأنواعه، والتنزيل بتحقق CRC، و`getDownloadURL`، والنسخ) | S4-D |
| `test/e2e/{web,android,ios}-storage/` **جديد** | تطبيقات بالمكتبات الرسمية في وضع الإنتاج، وحارس شبكة يرفض كل طلب إلى Google (نمط `test/experiments/storage-path`)، ومصفوفة `refFromURL` | S6 |
| `test/e2e/browser-matrix/` **جديد** | Playwright على المحركات الثلاثة، بحالات F §2.4 | S3-C |
| `sdk/web/aruma-web/src/{vite.js,storage.js}` | وضع `build` يبدّل `DEFAULT_HOST` في `@firebase/storage`، و`useArumaStorage(storage)`، ولفّ XHR و`fetch` لنطاق Storage. ويحل `storage.js` محل `storage-spike.js` | S6-W |
| `sdk/android/aruma-storage-gradle/` **جديد** | إضافة Gradle `dev.aruma.storage` (ADR-022 §16) | S6-A |
| `sdk/android/aruma-redirect/` | نطاق Storage في النطاقات المحوَّلة افتراضيًا (يحل محل `setStorageSpike`)، و`ArumaUrls.fromArumaUrl` | S6-A |
| `sdk/ios/ArumaRedirect/` | نطاق Storage افتراضيًا، و`fromArumaUrl`، و`Scripts/aruma_storage.rb` (مساعد `post_install`) | S6-I |
| `sdk/ios/firebase-storage-patch/` **جديد** | `storage-host.patch`، و`make-mirror.sh <upstream-tag>` الذي يبني وسم المرآة `<tag>-aruma.N` ويشغّل اختبارات FirebaseStorage، وREADME للمرآة وإشعار Apache-2.0 | S6-I |
| `sdk/node/admin-storage/` **جديد** | `@aruma/admin-storage` (ADR-022 §14) | S4-D |
| `sdk/go/adminstorage/` **جديد** | `adminstorage` (ADR-022 §14) | S4-D |
| `docs/compat/storage-v0.3.md` **جديد** | فروق ADR-023 §3 بمصادرها، ونتائج `test/compat/storage` | S3-D ثم S7 |
| `docs/guides/storage.md` **جديد** | الإعداد، والقواعد، والنشر، ومسار الإنتاج لكل منصة، والإشعار القانوني لـ Android وiOS | S6-X ثم S7 |
| `NOTICE` | ذكر firebase-ios-sdk وfirebase-android-sdk (Apache-2.0) للرقعة والإضافة | S6-A، S6-I |

## قرارات صغيرة داخل الهيكل

- **`blob` داخل الخدمة لا في `platform`:** لا مستهلك آخر اليوم. وإن احتاجه Hosting لاحقًا ينتقل بـ ADR.
- **`media` منفصلة عن `api`:** الواجهتان واللوحة تخدم كلها بايتات، والترويسات قرار أمني واحد يجب ألا يُكتب ثلاث مرات.
- **`wire` بلا `core`:** الصيغ قيم بسيطة، والتحويل من `core.Object` في كل واجهة (`resource.go`)، لأن `/v0` وGCS يختلفان في الرموز و`contentEncoding` والأرقام.
- **`rules_fields.go` جدول منفصل:** مفاتيح `request.resource` و`resource` تُطابَق بأوراكل منشور، وتتغير وحدها إن ظهرت ملاحظة جديدة.
- **`commitObject` في D ويستدعيه C:** المعاملة الوحيدة التي تجعل ملفًا مرئيًا في مكان واحد. والعقد يعطيها توقيعًا يذعر حتى تُدمج D.
- **لا واجهة `Service` مجردة بين `api` و`core`:** كما في RTDB، الاختبارات فوق `core` حقيقية وPostgres (`storetest`)، لا مزيفات.

## عقد S2-0 (يسبق الجميع)

يكتبه المدمج ويُدمج في فرع S2 قبل أن تبدأ المهام. ما فيه ثابت حتى نهاية S6، وتغييره يمر بالمدمج. **ولا تحتاج مهمة كود مهمة أخرى لتُترجم.** والدوال التي لم تُكتب تذعر برسالة اسم مالكها.

```go
// package blob (S2-A replaces the bodies)
type Store interface {
	Create(key string) (Writer, error)                  // O_EXCL: never over an existing file
	Open(key string) (Reader, error)
	Delete(key string) error                            // idempotent
	Promote(staging, key string) error                  // staging file → object key, durable on return
	Staging(key string) (Staging, error)                // open or create
	Walk(prefix string, fn func(key string, modTime time.Time) error) error
	DeletePrefix(prefix string) error                   // resumable
	FreeBytes() (int64, error)
}
type Writer interface { io.Writer; Commit() error; Abort() error } // Commit: fsync file and directory
type Reader interface { io.ReadSeekCloser; Size() int64 }
type Staging interface { Append(r io.Reader, limit int64) (int64, error); Truncate(n int64) error; Sync() error; Size() (int64, error); Close() error }
func ObjectKey(project int64) string   // p/<n>/<uuid>
func StagingKey(project int64) string  // s/<n>/<uuid>
func NewDisk(root string) (*Disk, error)
```

```go
// package core: types complete in S2-0; method bodies panic with their owner
type Principal struct {
	Kind  PrincipalKind  // Anonymous, User, Admin
	UID   string         // User
	Token map[string]any // User: the verified ID token's claims, for request.auth
	ServiceAccount string // Admin: for audit
}
type Bucket struct{ Project int64; Name string }
type Object struct {
	Bucket, Name                    string
	Generation, Metageneration      int64
	Size                            int64
	MD5                             [16]byte
	CRC32C                          uint32
	ContentType, ContentEncoding, ContentDisposition, ContentLanguage, CacheControl string // "" = unset
	Metadata                        map[string]string // never holds firebaseStorageDownloadTokens
	DownloadTokens                  []string
	Created, Updated                time.Time
}
type Patch struct {
	Fields        map[string]*string  // contentType, cacheControl, …: nil deletes
	Metadata      map[string]*string  // nil deletes the key
	ClearMetadata bool                // "metadata": null
	Tokens        *[]string           // GCS only: firebaseStorageDownloadTokens set by Admin
}
type Preconditions struct{ IfGenerationMatch, IfGenerationNotMatch, IfMetagenerationMatch, IfMetagenerationNotMatch *int64 }
type Checksums struct{ CRC32C *uint32; MD5 *[16]byte } // what the client claims
type Surface uint8 // SurfaceFirebase, SurfaceGCS, SurfaceConsole: defaults, tokens, rules
type UploadSpec struct {
	Name     string
	Attrs    Patch       // after StripClientTokens on SurfaceFirebase
	Declared int64       // -1 when not declared
	Surface  Surface
	Pre      Preconditions
	Want     Checksums
}
type Chunk struct {
	Offset   int64
	Body     io.Reader
	Length   int64 // -1 when unknown (chunked)
	Finalize bool
	Total    int64 // -1 when unknown
}
type UploadStatus uint8 // UploadActive, UploadFinal, UploadCancelled
type UploadState struct {
	Status   UploadStatus
	Received int64
	Object   *Object // Final and succeeded
	Err      error   // Final and failed: the same error on every repeat
}
type ListOptions struct{ Prefix, Delimiter, PageToken, StartOffset, EndOffset string; MaxResults int; Surface Surface }
type ListPage struct{ Prefixes []string; Objects []Object; NextPageToken string }
type CORSRule struct{ Origins, Methods, ResponseHeaders []string; MaxAgeSeconds int }
type Usage struct{ UsedBytes, ReservedBytes, Objects int64; QuotaBytes int64 }

func New(ctx context.Context, pool *pgxpool.Pool, store blob.Store, opts Options, log *slog.Logger) (*Service, error)
type Options struct {
	Limits    Limits
	Keys      *crypto.Keys      // page tokens, upload id hashes
	Projects  Projects          // tenancy: ProjectForBucket, CompiledRelease
	Documents rules.Documents   // rules.NoDocuments{} until v0.4
	Inflight  *rules.Inflight
	Now       func() time.Time
}

func (s *Service) Bucket(ctx context.Context, name string) (Bucket, error)                                  // 0
func (s *Service) Stat(ctx context.Context, b Bucket, name string, p Principal, sf Surface, gen int64) (Object, error) // C
func (s *Service) Open(ctx context.Context, b Bucket, name string, p Principal, token string, gen int64) (Object, blob.Reader, error) // C
func (s *Service) List(ctx context.Context, b Bucket, p Principal, o ListOptions) (ListPage, error)          // C
func (s *Service) Update(ctx context.Context, b Bucket, name string, p Principal, sf Surface, u Patch, pre Preconditions) (Object, error) // C
func (s *Service) Delete(ctx context.Context, b Bucket, name string, p Principal, pre Preconditions) error   // C
func (s *Service) Rewrite(ctx context.Context, src Bucket, srcName string, dst Bucket, dstName string, p Principal, attrs *Patch, pre Preconditions) (Object, error) // C
func (s *Service) CreateToken(ctx context.Context, b Bucket, name string, p Principal) (Object, error)       // C
func (s *Service) DeleteToken(ctx context.Context, b Bucket, name string, p Principal, token string) (Object, error) // C
func (s *Service) CORS(ctx context.Context, b Bucket) ([]CORSRule, error)                                   // C
func (s *Service) SetCORS(ctx context.Context, b Bucket, p Principal, rules []CORSRule) error               // C
func (s *Service) MatchCORS(rules []CORSRule, origin, method string) (allow bool, rule CORSRule)            // C
func (s *Service) Usage(ctx context.Context, b Bucket) (Usage, error)                                      // D
func (s *Service) Insert(ctx context.Context, b Bucket, p Principal, spec UploadSpec, body io.Reader, length int64) (Object, error) // D
func (s *Service) StartUpload(ctx context.Context, b Bucket, p Principal, spec UploadSpec) (id string, err error) // D
func (s *Service) WriteUpload(ctx context.Context, b Bucket, name, id string, p *Principal, c Chunk) (UploadState, error) // D; p nil when unauthenticated
func (s *Service) QueryUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error)         // D
func (s *Service) CancelUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error)        // D
func (s *Service) PurgeProject(ctx context.Context, project int64) error                                   // D
func (s *Service) Run(ctx context.Context) error                                                         // D

// inside the package
func (s *Service) commitObject(ctx context.Context, w objectWrite) (Object, error) // D: the only transaction that makes a blob visible
func (s *Service) authorize(ctx context.Context, b Bucket, p Principal, op ruleOp) error // E: Admin bypasses; ErrPermissionDenied or ErrOverloaded
type ruleOp struct { Method rules.Method; Name string; List bool; Resource, Incoming *Object; Patch *Patch; Surface Surface }
```

- **أخطاء `core`** في `errors.go` كاملة من العقد، و`errors.Is` عليها في الواجهات. ولا تُضاف حالة جديدة إلا عبر المدمج.
- **`authorize` في العقد ترفض كل شيء لغير Admin** (`ErrPermissionDenied`) حتى تُدمج E. فتعمل اختبارات C وD بـ Admin، أو بـ `authorizeHook` اختباري في الحزمة.
- **`wire` و`media`:** الأنواع والتوقيعات كاملة في العقد (الجداول أعلاه)، والأجسام تذعر.
- **`api` و`gcs`:** `New` و`Handler()` في العقد، و`Handler` يرد 501 لكل مسار حتى تُدمج S3 وS4. والتركيب في `server` و`app` يعمل من العقد.
- **وفيه:** المخطط، وsqlc بهياكل الجداول، وقاعدتا archtest، وقسم الإعداد، و`ProjectForBucket`، والتركيب في `app` (`OnProjectPurged` و`Run` يذعران حتى D).

## المهام

### S2: النواة (بعد S2-0، متوازية)

| المهمة | تملك | تعتمد على | تنتهي حين |
|---|---|---|---|
| **S2-A: البايتات** | `blob/` كلها | العقد فقط | `Create` لا يكتب فوق موجود، و`Commit` يستدعي fsync للملف وللمجلد (اختبار بغلاف `syscall` أو بفحص التسلسل)، و`Promote` متين، والمفاتيح الغريبة ترفض قبل لمس القرص، و`FuzzKey` عشر دقائق بلا ذعر |
| **S2-B: صيغ السلك** | `wire/` كلها | العقد فقط | كل نوع يطابق أمثلة A §4 وB §7 والتسجيلات، و`ContentDispositionDefault` يطابق حالتي PY-RR حرفيًا، وأهداف fuzz الخمسة تعمل عشر دقائق لكل منها بلا ذعر وبذاكرة محدودة |
| **S2-C: الكائنات** | `names.go` و`metadata.go` و`objects.go` و`tokens.go` و`list.go` و`pagetoken.go` و`cors.go`، و`queries/objects.sql`، واختباراتها، وحالات العزل للرموز و`pageToken` | العقد. و`commitObject` لـ `Rewrite` بعد دمج D | جداول كل عملية، والتوليد الذري للرمز تحت تزامن (رمز واحد مهما تزامنت القراءات)، والقائمة بالقفز ≤ `maxResults+1` بحثًا على مليون اسم، و`pageToken` مشروع على مشروع آخر 400 |
| **S2-D: الرفع والكتابة والحياة** | `uploads.go` و`insert.go` و`hashes.go` و`commit.go` و`quota.go` و`gc.go` و`sweep.go` و`purge.go` و`run.go`، و`queries/{uploads,usage,gc}.sql`، واختباراتها، وحالات العزل لرابط الرفع | العقد، و`blob` الحقيقي بعد دمج A (قبله `blob` في الذاكرة داخل الاختبار) | كل أشكال الإزاحة والإنهاء المكرر، والانهيار المحاكى بين كل خطوتين من ترتيب الكتابة يترك حالة يصلحها الطلب التالي أو المسح، ومئة رفع متوازٍ لا تتجاوز الحصة، و`PurgeProject` مرتين متتاليتين ومقطوعة في المنتصف تنتهي نظيفة |
| **S2-E: ربط القواعد** | `rules.go` و`rules_fields.go` و`authorize`، وmigration `control` وعمل `tenancy` على القواعد (rules-layout.md:115-118)، واختباراتها، وحالات العزل للقواعد | العقد، و**S1 مدموجًا**، وملفات Pyric من `origin/s0-rules-oracle` | جدول ADR-022 §8 حالةً حالة، و`overwrite/verb-create` و`resource-existing` و`keys-size-15` وأخواتها من PY-RR تمر عبر ربطنا، وlist بـ `UnknownSegment` يطابق `aruma/storage/read_list` و`list_on_folder` للمحاكي، والإصدارة الغائبة ترفض، و`ErrOverloaded` يصير 503 لا 403 |
| **S2-F: خدمة البايتات** | `media/` كلها | العقد و`wire` (أنواعه في العقد) | جداول الترويسات لكل نوع، وRange، وقنبلة gzip بذاكرة ثابتة. ومصفوفة المتصفحات في S3-C |

- **S2 يُغلق** بدمج A ثم B ثم F ثم D ثم C ثم E، و`make lint` و`make test` و`go test -race ./internal/services/storage/...`.

### S3: Firebase REST `/v0` (يبدأ بعد S2-0، وينتهي بعد S2)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S3-A: الأساس** | `api.go` و`auth.go` و`errors.go` و`cors.go` و`objects.go` و`list.go` و`resource.go`، و`test/acceptance/storage_v0_test.go` | كل عملية غير الرفع والتنزيل بأشكال A §7، و`AssertNoNull` على كل رد، وbucket مجهول 404 بشكل الإنتاج، ومسار مجهول 400 |
| **S3-B: الرفع** | `upload.go` و`multipart.go`، و`test/acceptance/storage_upload_test.go` | البروتوكول كما في A §7 لكل منصة (طلبات الويب وAndroid وiOS المسجّلة في `test/oracle/storage/emulator/sdk_upload.json` و`raw_resumable.json` مع تصحيح ما يخالف فيه المحاكي الإنتاج)، والرابط المطلق من `public_url` خلف وكيل، وقطع بلا مصادقة، و`Authorization` مخالف 403، والمهلة المتدرجة وslowloris |
| **S3-C: التنزيل والمتصفحات** | `download.go`، و`test/e2e/browser-matrix/` | الرمز والقواعد، وCORS الـ bucket على `alt=media`، والمصفوفة على المحركات الثلاثة بحالات F §2.4 |
| **S3-D: التوافق** | `test/compat/storage/`، وهيكل `docs/compat/storage-v0.3.md` | مجموعة Storage في firebase-js-sdk، واختبارات المطابقة غير `emulatorOnly`، وإعادة تسجيلات المحاكي: كل فرق مصنّف (خطأ عندنا، أو فرق ADR-023 §3، أو خطأ في المحاكي) |

### S4: GCS JSON وAdmin (يبدأ بعد S2-0، وينتهي بعد S2)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S4-A: الأساس** | `gcs.go` و`auth.go` و`errors.go` و`objects.go` و`buckets.go` و`resource.go`، وحالات العزل لحساب الخدمة | B §10.1 (عدا الرفع والوسائط)، وطلب بلا `Bearer` 401، وحساب مشروع على bucket آخر 403، و`code` في الخطأ رقم = الحالة، و`exists()` في Node يعيد `false` على 404 |
| **S4-B: الرفع** | `upload.go` | Node `save()` (الطلب الواحد `bytes 0-*/*`، والمقطّع بـ 308 و`Range`، و`resumable:false`)، وGo (multipart تحت 16 MiB، وresumable بالـ override، والجزء الأخير الفارغ)، والـ checksums المختلفة 400 بلا كائن، و`ifGenerationMatch=0` على موجود 412 |
| **S4-C: الوسائط** | `download.go` | `file.download()` في Node بتحقق CRC الافتراضي، وGo بـ `WithJSONReads` مع `generation`، و`x-goog-*` كلها |
| **S4-D: الحزمتان** | `sdk/node/admin-storage/` و`sdk/go/adminstorage/`، وسيناريو Storage في `test/smoke/admin-{node,go}` | `universeDomain` يحمل `Bearer` على بدء `save()` وقطعه (يُعاد قياسه على مفتاح فيه `universe_domain`، P)، و`getDownloadURL` الخاص يعيد الرابط الموحّد، والحزمتان ترفضان متغيرات المحاكي، وحارس الشبكة لا يرى طلبًا إلى Google |

### S5: الإدارة واللوحة (يبدأ بعد S2-0، وS5-A بعد S2-E)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S5-A: واجهات الأداة** | `internal/admin/mgmt/rules.go` و`storage.go`، واختباراتهما | كل طلب في C §1.5 و§1.3 و§1.4 بالحالة 200 حرفيًا وأشكاله، و`:test` بـ `issues[]` على الخطأ |
| **S5-B: واجهة اللوحة** | `internal/services/storage/console/`، و`console_routes.txt` | كل مسار يطلب اعتمادًا (ADR-010 S-4) ويمر بـ CSRF، والتنزيل `attachment` دائمًا بترويسات `media`، وسجل التدقيق |
| **S5-C: الواجهة** | `dashboard/src/features/storage/` | الملفات (تصفح، ورفع متقطع كبير، وحذف، وmetadata، ورموز)، ومحرر القواعد بأخطائه على الأسطر، وCORS، والاستعمال. وe2e في Playwright |
| **S5-D: أداة `firebase`** | e2e في `test/acceptance` أو `test/e2e/cli-storage` | `firebase init storage` يكتب القالب أو القواعد المنشورة، و`firebase deploy --only storage` ينشر وتطبَّق القواعد فورًا على `/v0`، والنشر الثاني بلا تغيير «already up to date»، وخطأ ترجمة يطبع `[E] line:column` |

### S6: مسار الإنتاج لكل منصة (يبدأ بعد S3-B على خادم يعمل)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S6-W: الويب** | `sdk/web/aruma-web` (وضع `build` لـ Storage، و`useArumaStorage`، والاحتياط)، و`test/e2e/web-storage/` | على بناء npm حقيقي بـ Vite 8: 11/11 عمليات P بلا ⚠، و`getDownloadURL` بالرابط الموحّد يعمل في `<img>`، والبناء يفشل إن لم يجد `DEFAULT_HOST`. وبلا Vite: `useArumaStorage` والاحتياط. وFlutter web بسكربت `install` |
| **S6-A: Android** | `sdk/android/aruma-storage-gradle/`، وتعديل `aruma-redirect`، و`test/e2e/android-storage/`، و`NOTICE` | تطبيق e2e على AVD `aruma-a34` (وحده عليه): 12/12 بلا ⚠، والرابط موحّد، وiptables لا يرفض حزمة واحدة، والبناء يفشل على صنف أو نص مفقود، وإشعار التعديل في `META-INF`. ومعه بناء بلا الإضافة يعمل بالاحتياط |
| **S6-I: iOS** | `sdk/ios/ArumaRedirect` (النطاق، و`aruma_storage.rb`)، و`sdk/ios/firebase-storage-patch/`، و`test/e2e/ios-storage/`، و`NOTICE` | تطبيق e2e بـ CocoaPods وآخر بـ SwiftPM على المرآة: 11/11 بلا ⚠، والرفع في الخلفية (`putFile` بجلسة خلفية) إلى Aruma، و`pod install` يفشل على سطر مفقود، و`make-mirror.sh` يبني وسمًا من الوسم الرسمي ويمر باختبارات FirebaseStorage. **ويُسجَّل طلب `updateMetadata` الحقيقي من iOS** (هل يرسل `"metadata": null`، A §8.1، ADR-023 §3-15) |
| **S6-F: Flutter** | `test/e2e/flutter-storage/` | بعد S6-A وS6-I (الجهاز نفسه): Android وiOS بالإضافة والمساعد، و`Image.network(getDownloadURL())` لا يذهب إلى Google (كان يتسرّب في P) |
| **S6-X: عبر المنصات** | مصفوفة `refFromURL` (الرابط من كل منصة يُحلَّل على كل أخرى)، و`docs/guides/storage.md` (قسم الإنتاج والإشعار القانوني) | المصفوفة تعطي الـ bucket والمسار نفسيهما أو تفشل صراحةً، ولا يُنهي تطبيق iOS (`fatalError`، P:344) |

- **الأجهزة:** وكيل واحد على `aruma-a34` وعلى المحاكي في وقت واحد (قاعدة الـ brief 7). فـ S6-A ثم S6-F على Android، وS6-I قبل S6-F على iOS. وS6-W بلا جهاز.

### S7: التحقق والإصدار (خارج هذا الهيكل)

مجموعات Google بالوضعين، وe2e كلها بحارس الشبكة، والمصفوفة، وأوراكل القواعد (98%)، و`docs/compat/storage-v0.3.md` بأرقامه، و`docs/security/review-v0.3.md`، ومراجعة أمنية لخدمة البايتات والرفع.

## ما يُقاس في البناء ولم يُقس هنا

- كلفة fsync لكل قطعة على القرص المستهدف (S2-A يقيسها ويضعها في `docs/research/storage/baseline.md`؛ إن ثقلت تُجمَّع القطع الصغيرة قبل `Sync` دون أن يُعلن ما لم يُفرَّغ).
- بدء `save()` المتقطع بـ `universeDomain` على مفتاح فيه `universe_domain` وعلى نطاق فرعي (S4-D، P).
- إضافة Vite على بناء npm حقيقي، والإضافة لـ Gradle، والرقعة والمرآة لـ iOS (S6؛ قيس قبلها بناء CDN معدَّل يدويًا وحده، P).
- مرآة SwiftPM عبر `swift package config set-mirror`، بديلًا عن تغيير رابط الحزمة في التطبيق (S6-I يقيسه ويوثّق ما يعمل في Xcode).
