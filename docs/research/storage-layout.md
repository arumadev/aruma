# هيكل كود Cloud Storage for Firebase، وتقسيم S2 إلى S6

- **الحالة:** مقترح، ومراجَع (ثلاث مراجعات عدائية، 35 نتيجة أُدخلت كلها، 2026-10-24)، مع [ADR-022](../adr/ADR-022-storage-architecture.md) و[ADR-023](../adr/ADR-023-v03-scope.md)، 2026-10-24. يُحدَّث حيث يخالفه الكود، وكل ما لم يكن فيه يُعلَّم «لم يكن في المسودة»، كما في [rules-layout.md](rules-layout.md).
- **الأساس:**
  - ADR-022 (المعمارية) وADR-023 (النطاق).
  - ADR-015 (حدود الحزم وarchtest، وإضافة خدمة جديدة)، وADR-017 (`project_number` و`scopecheck` واختبارات العزل)، وADR-019 (كاتب واحد).
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
- **`core`:** تستورد `blob` و`queries` و`platform/rules` و`platform/crypto` و`platform/idtoken` و`tenancy` و`store`. ولا تستورد `api` ولا `gcs` ولا `console` ولا `media` ولا `platform/httpx`: القاعدة القائمة `internal/services/*/core` (`archtest_test.go:56`) تمنع `api` و`console` و`httpx` وحدها، فتُضاف `internal/services/*/gcs` و`internal/services/*/media` إلى قائمة المنع، فالنواة لا تعرف HTTP.
- **`media`:** تستورد `wire` وGo القياسية. لا `core`: تأخذ ما تخدمه قيمةً (`media.Item`) وقارئًا.
- **`api` و`gcs` و`console`:** تستورد `core` و`media` و`wire` و`platform/*`. **ولا يستورد أحدها الآخر:** قواعد منع جديدة `internal/services/*/api` (تمنع `gcs` و`console`)، و`…/gcs` (تمنع `api` و`console`)، و`…/console` (تمنع `api` و`gcs`)، و`…/media` (تمنع الثلاث و`core`). ولكل قاعدة حالة في `TestRulesThemselves`.
- **`internal/config` لا يستورد Storage** (`archtest_test.go:23`): `config.StorageLimits` نسخة حقلًا بحقل من `core.Limits`، والتحويل تحويل نوع، والفحص كله (ومعه الفحوص المتقاطعة، ADR-022 §17) في `config`، كما فعل S1-0 بـ `RulesLimits`.
- **`sdk/go/adminstorage`** وحدة Go مستقلة (`go.mod` خاص)، وقاعدة archtest `"sdk/go/adminstorage": {}` كـ `adminauth` (`archtest_test.go:47`).
- **Storage لا يستورد خدمة أخرى** (القاعدة القائمة `internal/services/*`)، و`admin/mgmt` لا يستورد Storage (القاعدة 3 في ADR-015).

## الشجرة

العمود الأخير هو المهمة (القسم الأخير). `0` هو commit العقد S2-0.

### `internal/services/storage/core/`: النواة

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | وصف الحزمة: bucket واحد لكل مشروع، والقرارات المؤقتة (ADR-023 §3) بموضعها في الكود | 0 |
| `api.go` | **العقد العام** (القسم «عقد S2-0»): `Service` **بكل حقوله الخاصة** (فلا تضيف مهمة حقلًا) و`New` و`Options` و`Principal` و`Bucket` و`Object` و`Patch` و`Preconditions` و`Checksums` و`UploadSpec` و`Chunk` و`UploadState` و`ListOptions` و`ListPage` و`CORSRule` و`Usage`، وتوقيعات كل الطرق | 0 |
| `helpers.go` | **مساعدات مشتركة تعمل من العقد** (لا تذعر)، ليتشاركها C وD وE بلا انتظار: `validateName` و`segments` و`applyDefaults` (ومنها `defaultDisposition`، **دالة الهروب الوحيدة** المطابقة لـ PY-RR) و`StripClientTokens` و`newDownloadToken` و`sealTokens`/`openTokens` (KEK، البيانات الإضافية `(project, name)`)، و`sealSessionTokens`/`openSessionTokens` (`(project, name, id_hash)`)، و`encodeAttrs`/`decodeAttrs` و`encodeSnapshot`/`decodeSnapshot` لصف الجلسة. ولها اختباراتها في العقد. تغييرها يمر بالمدمج | 0 |
| `durability.go` | **مساعد المعاملات الوحيد:** `begin` و`inTx` (`SET LOCAL synchronous_commit TO on`، و`COMMIT` تحت `WithoutCancel`)، تمر به كل معاملة Storage في C وD وE؛ و`warnUnsafeDurability` (`SHOW fsync`) يستدعيه `Boot`؛ و**`releaseObject`** (نقص `used_bytes` و`object_count` وصف `blob_gc`) يستعمله `Delete` في C والكتابة فوق موجود في D | 0 |
| `metrics.go` | المقاييس (ADR-022 §18) بأسمائها وتسمياتها، ولكل مقياس مالكه في تعليقه، و`Collectors()` يسجّلها `app` | 0 |
| `errors.go` | الأخطاء المسمّاة (`ErrNotFound` و`ErrBucketNotFound` و`ErrPermissionDenied` …، و`InvalidError{Reason, Message}` و`OffsetError{Received}`). كل واجهة تربطها بحالتها وجسمها. و`finalError`: قائمة الأخطاء النهائية وحدها (§6.1)، و`encodeError` يرفض غيرها | 0 |
| `limits.go` | `Limits` (ADR-022 §17) و`DefaultLimits()` و`Validate()` | 0 |
| `bucket.go` | `Bucket(ctx, name)`: الحل عبر `tenancy.ProjectForBucket`، و`ErrBucketNotFound` لغير `ACTIVE` | 0 |
| `names.go` | «مقطع فارغ» لاستبعاد `/v0` من القوائم، وحالات fuzz لـ `validateName` (في `helpers.go`) | C |
| `metadata.go` | الحقول القابلة للكتابة وحدودها (8 KiB للمخصّصة، 1 KiB ورفض CR/LF وما دون U+0020 للحقول التي تصير ترويسات)، وتطبيق `Patch` (`null` يحذف، ودمج مفتاحًا مفتاحًا، و`ClearMetadata`) | C |
| `objects.go` | `Stat` (ومعه توليد الرمز الذري لـ `/v0`) و`Open` (رمز يطابق، أو **رمز موجود لا يطابق = `ErrPermissionDenied` بلا قواعد**، أو القواعد؛ و`generation`) و`Update` و`Delete` | C |
| `tokens.go` | المقارنة بزمن ثابت بعد فتح الختم، و`CreateToken`/`DeleteToken` (حذف الأخير يولّد غيره)، وعرضها لـ GCS في `metadata.firebaseStorageDownloadTokens` وإخفاؤها من `/v0` | C |
| `list.go` | المشي على الفهرس بالقفز فوق البادئات (≤ `maxResults+1` بحثًا)، و`StartOffset`/`EndOffset`، واستبعاد ذوات المقطع الفارغ في `/v0` | C |
| `pagetoken.go` | `pageToken` معتم: HMAC بمفتاح الغرض الجديد `aruma/v1/storage-page-token` (`crypto.Keys.PageTokenMAC`، S2-0) على (المشروع، والـ bucket، والبادئة، والفاصل، وآخر اسم). غير صالح = `ErrInvalidPageToken` | C |
| `cors.go` | `CORS` و`SetCORS` (دلالة GCS `cors[]`، وفحص الحقول)، و`MatchCORS(origin, method)` الذي تستدعيه الواجهتان لـ `alt=media` | C |
| `rules.go` | **ربط القواعد** (ADR-022 §8): بناء `request` (`auth` و`path` و`time` و`resource` و`method` و`params` الفارغة) و`resource`، والمقاطع `b/<bucket>/o/…`، و`UnknownSegment` لـ list، والطريقة لكل عملية (الكتابة فوق موجود `create`)، ورفض list على الإصدار 1، و`Budget` لكل طلب، و`CompiledRelease("firebase.storage/default")` (الاسم المنطقي، ADR-022 §2)، و`ErrOverloaded` من `rules` | E |
| `rules_fields.go` | مفاتيح `request.resource` الخمسة عشر عند الإنشاء وعند PATCH وقيمها (`null` لـ `generation`/`metageneration`/`etag`/`storageClass` عند الإنشاء، وثباتها عند PATCH، وبلا `timeCreated`/`updated`)، ومفاتيح `resource` من `stdlib-realstorage-p2-native-object-fields.json`. ملف منفصل لأنه جدول يُطابَق بالأوراكل | E |
| `uploads.go` | آلة حالات الجلسة: **قفل لكل جلسة في الذاكرة** (خريطة بعدّاد مراجع) يأخذه كل طلب والإنهاء والإلغاء والانتهاء، ثم إعادة قراءة الحالة. `StartUpload` (فحص المعلن مقابل الحصة **بلا أي حجز**، وإنشاء ملف staging، وحدود الجلسات التي لا تعد الجلسات الفارغة، وللجلسات التي تصدرها اللوحة: عمر ساعة وحجم معلن إلزامي مربوط)، و`WriteUpload` (الإزاحة: إلحاق، أو تجاهل التداخل، أو `OffsetError`؛ والتدفق إلى staging مع `Sync` قبل تحديث `received` و`progressed_at`؛ وقص الملف إلى `received` بعد انهيار؛ وملف staging لا يُنشأ هنا أبدًا)، و`QueryUpload`، و`CancelUpload`، والإنهاء (الحجم المعلن، و`md5Hash` العميل، وحالة المستخدم الحية عبر `idtoken.Users`، وملكية حساب اللوحة للمشروع وسجل التدقيق لجلسات اللوحة، والقواعد بهوية `start`، و`blob_key` قبل النقل، ثم `commitObject`). **كل رفض نهائي يجعل الجلسة `final`** ويحفظ `final_status` و`final_body`، والإنهاء المكرر و`query` يعيدانهما | D |
| `insert.go` | `Insert` لرفع media وmultipart (جسم واحد): الكتابة المباشرة إلى مفتاح جديد، والقواعد، و`commitObject`. و**`Rewrite`** (من C إلى D، فلا ينتظر C الـ `commitObject`): ينسخ الملف إلى مفتاح جديد، ويُحسب على الحصة وأرضية القرص، وينسخ الرموز ويختمها لاسم الوجهة ما لم تُستبدل الـ metadata | D |
| `hashes.go` | `crc32c` (Castagnoli) و`md5` أثناء التدفق، وحفظ حالتهما مع الإزاحة (`encoding.BinaryMarshaler`) واستعادتها | D |
| `commit.go` | **`commitObject`**: المعاملة الوحيدة التي تجعل ملفًا مرئيًا (ADR-022 §5): بجمل `queries/commit.sql` المكتوبة في العقد: `InsertObject` (`DO NOTHING`، وهو وحده لـ `ifGenerationMatch=0`)، ثم عند التعارض `LockObject` (`FOR UPDATE`) والشروط على الصف المقفل و`ReplaceObject`، و`releaseObject` للقديم. **و`LockObject` بلا صف بعد تعارض `InsertObject` حذفٌ التزم بينهما: يُعاد `InsertObject` عددًا محدودًا من المرات، ولا يُرد 412** (`TestCommitStatementsRetryAfterConcurrentDelete`)، و`generation = GREATEST(now_µs, السابق+1)`، وصف `blob_gc` للقديم، وتحويل الحصة. `xid` قبل `COMMIT`، و`COMMIT` تحت `context.WithoutCancel`. **الحذف المتزامن للملف الجديد على أخطاء ما قبل `COMMIT` وحدها**، والملتبس يُحسم بـ `pg_xact_status` أو يُترك للمسح | D |
| `quota.go` | صف `usage` بـ upsert، والحجز لكل قطعة من البايتات الواصلة (`… WHERE used+reserved+$n <= quota`)، وخطوات 8 MiB حين لا يُعرف الطول، والرد والتحويل، و**كل الحجوزات في القاعدة** (`Insert` و`Rewrite` منها)، وسقف بايتات المجهولين (`anonymous_staged_fraction`)، و**عدّاد البايتات الجارية للعملية** مع `free − inflight − n ≥ min_free` فحصًا وإضافة ذريين (CAS) عند كل خطوة | D |
| `gc.go` | عامل `blob_gc` بعد `gc_grace`، وانتهاء الجلسات بالعمر (حذف staging و`blob_key` غير المشار إليه، وتحرير الحجز)، والانتهاء الخامل (`upload_idle_ttl`: يحرر `reserved − received`، وينهي الجلسة إن كان `received = 0`) | D |
| `boot.go` | **`Boot(ctx)` قبل فتح المستمع:** القفل على `data_dir` (`blob.Lock`)، وربط `INSTANCE` بصف `storage.instance` ورفض الإقلاع عند عدم التطابق، وقائمة المرشحين بالمشي على `p/` و`s/`، ومصالحة الحصة لكل المشاريع، وتحذير إن كان `fsync` في Postgres `off` | D |
| `sweep.go` | حذف المرشحين بلا إشارة، بجملة واحدة لكل دفعة (`objects.blob_key` و`staging_key`/`blob_key`)، في الخلفية بعد الإقلاع، والمسح الدوري (يبلّغ فقط) | D |
| `purge.go` | `PurgeProject`: staging، ثم `p/<n>/`، ثم الصفوف. idempotent وقابلة للاستئناف | D |
| `run.go` | `Run(ctx)` بعد فتح المستمع: حذف المرشحين، وGC، وانتهاء الجلسات، والمسح الدوري، حتى يُلغى `ctx` | D |
| `*_test.go` | لكل ملف مالكه. وأهمها: `uploads_test.go` (كل أشكال الإزاحة، والإنهاء المكرر، والانهيار بين `Sync` و`received`، وبين النقل والـ commit)، و`commit_test.go` (الترتيب، وكتابتان متزامنتان فوق الاسم نفسه بلا تكرار مفتاح ولا تسرب، و`ifGenerationMatch=0` المتزامن، و`COMMIT` الملتبس المحاكى لا يحذف ملفًا التزم)، و`quota_test.go` (مئة رفع متوازٍ لا تتجاوز الحصة، و`start` بمعلن 5 GiB لا يحجز شيئًا، وجلسات فارغة كثيرة من IP واحد تنتهي في 10 دقائق ولا تملأ الحدود، والمجهولون لا يتجاوزون خُمس الحصة، والمصالحة بعد انهيار)، و`boot_test.go` (نسخة ثانية على المجلد نفسه تنتظر القفل، ومجلد لقاعدة أخرى يرفض الإقلاع، وانهيار بين كتابة `INSTANCE` والصف يُتبنّى على مجلد فارغ ويُرفض على مجلد فيه بيانات، ومفتاح نُقل بعد الإقلاع لا يُحذف)، و`uploads_test.go` يضيف قطعتين متزامنتين على جلسة واحدة وقطعة بعد الإنهاء، و`rules_test.go` (جدول ADR-022 §8 حالةً حالة، ومنه الكتابة فوق موجود `create` و`resource` الموجود) | C، D، E |

### `internal/services/storage/blob/`: البايتات (Go القياسية وحدها)

| الملف | ما فيه | المهمة |
|---|---|---|
| `blob.go` | **العقد:** `Store` و`Writer` و`Reader` و`Staging` و`Lock`، و`ObjectKey(project)` و`StagingKey(project)` (UUID من `crypto/rand`)، و`ValidKey` (`^(p|s)/[0-9]+/[0-9a-f-]{36}$`) | 0 |
| `lock.go` | `AcquireLock(root, wait)`: `flock(LOCK_EX)` على `<root>/LOCK` بانتظار يُسجَّل كل 10 ثوانٍ، و`ReadInstance`/`WriteInstance` لملف `INSTANCE` (كتابة متينة) | A |
| `disk.go` | `NewDisk(root)`: التفرع `p/<n>/<ab>/<uuid>`، و`Create` بـ `O_EXCL`، و`Commit` (fsync للملف ثم للمجلد ثم إغلاق)، و`Open` (`io.ReadSeekCloser` وحجمه)، و`Delete` idempotent، و`Promote(staging, key)` (`rename` على القرص نفسه ثم fsync للمجلدين)، و**`mkdirDurable`** (fsync لأب كل مجلد ينشئه حتى الجذر، لـ `p/` و`s/` و`Promote`)، ورفض أي مفتاح غير صالح قبل لمس القرص | A |
| `staging.go` | ملف الجلسة: `CreateStaging` عند `start` وحده (`O_EXCL`)، و`OpenStaging` للقطع لا ينشئ أبدًا، و`Truncate(n)`، و`Append(r, limit)` يعيد ما كُتب، و`Sync`، و`Size` | A |
| `walk.go` | `Walk(prefix, fn(key, modTime))` بترتيب ثابت، و`DeletePrefix(prefix)` قابل للاستئناف (لـ `PurgeProject`)، و`FreeBytes()` (`syscall.Statfs`، ومع Windows بديل أو «غير مدعوم» صريح) | A |
| `*_test.go` | رفض المفاتيح الغريبة (`..`، و`/`، وحالة الأحرف، وNUL)، و`O_EXCL` يمنع الكتابة فوق موجود، و`Promote` بين مجلدين، و`Walk` على شجرة كبيرة، و`FuzzKey` | A |

### `internal/services/storage/wire/`: صيغ السلك (Go القياسية وحدها)

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | المصادر: A وB وD وE، وقاعدة «لا `null` على `/v0`» | 0 |
| `firebase.go` | `FirebaseObject` (كل حقل نص بـ `omitempty`، و`downloadTokens` نص بفواصل، و`metadata` خريطة نص إلى نص)، و`FirebaseList` (`prefixes` و`items` بلا `omitempty`، و`nextPageToken` بـ `omitempty`)، و`FirebaseError` | 0 ثم B |
| `gcs.go` | `GCSObject` (`kind` و`id` و`selfLink` و`mediaLink`، والأرقام نصوص)، و`GCSObjects` و`GCSBucket` (ومعه `cors`) و`GCSRewrite` و`GCSError` (`code` رقم = الحالة، و`errors[]`) | 0 ثم B |
| `format.go` | `Time(t)` (`2006-01-02T15:04:05.000Z`)، و`Int(n)` نصًا، و`CRC32C(v)` (base64 لأربعة بايتات BE مع الحشو)، و`MD5(sum)`، و`ETag(gen, metagen)` لحقل JSON، و`MediaETag(gen)` لترويسة الوسائط (المحتوى وحده)، و`HashHeader(crc, md5)` (ترويسة واحدة بلا مسافة). ولا هروب لـ `Content-Disposition` هنا: الدالة الوحيدة في `core/helpers.go` | B |
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
| `headers.go` | جدول ADR-022 §10.1 و§10.2: `nosniff`، وCSP واحدة بـ `sandbox` (والوسائط تزيد `media-src 'self'`)، و`CORP: cross-origin` فوق العامة، وPDF `attachment`، و`Cache-Control` (`public` إلى `private` **في `/v0` حين قررته القواعد وحدها**، والمخزّن كما هو على GCS، و`private, max-age=0` افتراضيًا)، و`ETag` المحتوى، ولا ترويسة فارغة | F |
| `serve.go` | `Content-Type` قبل أي كتابة، وRange (`wire.ParseRange`، والفارغ 200)، و`If-Range` على `ETag` المحتوى، والمهلة المتدرجة للكتابة عبر `ResponseController`، ورفض `Service-Worker: script` و`Sec-Fetch-Dest` العمّال 403، **ورفض `Sec-Fetch-Site: same-origin` مع وجهات السكربت والأسلوب والعمّال والـ worklets و`xslt` 403** | F |
| `gunzip.go` | فك الضغط تدفقًا (`gzip.Reader`) حين المخزّن gzip والطلب بلا `Accept-Encoding: gzip` ولا `no-transform`، وسقف `object_bytes` للمخرَج، وقطع الاتصال عند التجاوز | F |
| `*_test.go` | الترويسات لكل نوع (HTML وSVG وPNG وPDF وMP4 وغير المعروف)، وجدول `Sec-Fetch-Site`×`Sec-Fetch-Dest`، وملف `x.html` بلا نوع يخرج `octet-stream`، وRange على الفارغ وغير الفارغ والمتعدد، وقنبلة gzip بذاكرة ثابتة | F |

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
| `upload.go` | تحديد النوع (multipart، resumable، media)، و`start` على `/o` و`/o/{object}` بـ `POST`/`PUT`، و`X-Goog-Upload-URL` مطلق من `PublicURL`، والقطع و`query` و`cancel` و`finalize` بـ `POST`/`PUT` على رابط الجلسة **و`Authorization` عليها يُتجاهل**، و`X-Goog-Upload-Status: final` على كل رفض نهائي و`active` على الفجوة و503 وحدهما، والمهلة المتدرجة **للقراءة والكتابة معًا** (ومهلة الكتابة تُضبط ثانية قبل الرد) وأدنى معدل، وحد الطلبات المتزامنة لكل IP (IPv6 لكل /64) | S3-B |
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
| `upload.go` | `uploadType` media وmultipart وresumable (الـ metadata من جسم JSON مدموجًا مع `X-Upload-Content-Type`)، و`upload_id` يغلب الطريقة و`uploadType`، و`Content-Range` بأشكاله، و308 مع `Range` لـ Node، و200 مع `X-Http-Status-Code-Override: 308` لـ `X-GUploader-No-308`، و`X-Goog-Hash` والـ checksums في JSON، والشروط المسبقة | S4-B |
| `download.go` | `alt=media` تحت `/storage/v1` و`/download/storage/v1` عبر `media.Serve` (و`x-goog-*` كلها) | S4-C |

### `internal/services/storage/console/`: اللوحة

| الملف | ما فيه | المهمة |
|---|---|---|
| `console.go` | `Routes(svc)` لـ `consolemount`: التصفح بالبادئات، و**الرفع بجلسة تصدرها اللوحة** (`POST /console/v1/projects/{p}/storage/uploads`، JSON صغير تحت حد 1 MiB، يبدأ جلسة متقطعة بهوية Admin ويعيد رابط `/v0`؛ والقطع تذهب إلى `/v0` بلا كعكة)، والتنزيل (`media.Serve` بـ `Mode.Console`: `attachment` دائمًا)، والحذف، وتعديل الـ metadata، والرموز، وCORS، والاستعمال. والقواعد عبر `tenancy` (قراءة الإصدارة، والتحقق، والنشر). وسجل التدقيق لكل كتابة | S5-B |

### `internal/services/storage/queries/`

| الملف | ما فيه | المهمة |
|---|---|---|
| `objects.sql` | الكائنات، والقائمة بالقفز، والرموز، وCORS | 0 (المخطط والهياكل) ثم C |
| `uploads.sql`، `usage.sql`، `gc.sql` | الجلسات، والحصة، وoutbox الحذف، والمسح. استعلامات العامل والمسح `-- scope: global` بسببها | 0 ثم D |
| `commit.sql` | جمل `commitObject` و`releaseObject`: `InsertObject` و`LockObject` و`ReplaceObject` و`QueueBlobGC` و`ReleaseUsage` (بلا `RETURNING old.*`، الذي لا يولّده sqlc 1.31.1) | 0، ويستعملها C وD |
| `scope_test.go` | **الجداول الخمسة الخاصة بالمشاريع في `TestQueriesAreProjectScoped`**، و`storage.instance` باستعلامات `-- scope: global` | 0 |
| `*.sql.gen.go`، `db.gen.go`، `models.gen.go` | مولّدة بـ sqlc، تُودَع مع كل تغيير | كل من يغيّر SQL |

### خارج الحزمة: ما يُضاف أو يتغير

| المكان | التغيير | المهمة |
|---|---|---|
| `internal/store/migrations/storage/00001_init.sql` **جديد** | `storage.objects` (و`blob_key` فريد، و`download_tokens bytea` مختومة) و`upload_sessions` (ومعه `final_body` و`progressed_at`) و`blob_gc` و`usage` و`buckets` و`storage.instance` (صف واحد، ليس لمشروع) (ADR-022 §3)، وفي كل جدول مشروع `project_number … REFERENCES control.projects … ON DELETE CASCADE`، والأسماء `COLLATE "C"`. وسطر في `migrations.Schemas` ونمط `embed` و`sqlc.yaml`، ثم `-update` لـ `released.lock` | 0 |
| `internal/store/migrations/control/` (migration جديد) | `control.rulesets` و`control.rules_releases` كما في rules-layout.md:115 | E |
| `internal/tenancy/core/storage.go` **جديد** | `ProjectForBucket(ctx, bucket) (Project, error)` (اللاحق `.<project_domain>`، و`ACTIVE` وحده) | 0 |
| `internal/tenancy/core/rules.go` و`internal/tenancy/queries/` | حفظ الـ rulesets ونشر الإصدارات، و`CompiledRelease` بـ LRU بالوزن، وإصدارة Storage باسمها المنطقي `firebase.storage/default` (والتحويل من `firebase.storage/<bucket>` وإليه، **والردود باسم الـ bucket الحالي دائمًا** لأن firebase-tools يطابق القائمة بـ `startsWith`)، والإصدارة الافتراضية المقفلة عند أول قراءة لمشروع بلا إصدارة، والجدولان في `scope_test.go` (rules-layout.md:115-117)، **وحذفهما في معاملة `PurgeExpired`** (`projects.go:406-414`). **التوقيعات في العقد:** `CreateRuleset` و`GetRelease` و`PublishRelease` و`CompiledRelease` (E)، و`GetRuleset` و`ListRulesets` و`DeleteRuleset` و`ListReleases` (S5-A)، والنوعان `Ruleset` و`Release`، و`Options.RulesLimits` | E، S5-A |
| `internal/archtest/archtest_test.go` | قاعدتا `blob` و`wire`، و`gcs` و`media` في قائمة منع `core`، والمنع المتبادل بين `api` و`gcs` و`console` و`media`، و`sdk/go/adminstorage`، وحالات `TestRulesThemselves` | 0 |
| `internal/config/config.go` | قسم `storage`: `enabled` و`data_dir` (**إلزامي ومطلق حين Storage مفعّل**) و`limits` (`config.StorageLimits`، ADR-022 §17) و`gc_grace`، ومتغيرات `ARUMA_STORAGE_*`، والفحص والفحوص المتقاطعة في `config` عند التحميل | 0 |
| `internal/platform/crypto/keys.go` | غرض جديد `aruma/v1/storage-page-token` و`PageTokenMAC`، واختباره | 0 |
| `internal/platform/idtoken/idtoken.go` | واجهة `Users` (`State(ctx, project, uid)`: موجود، ومعطّل، و`tokensValidAfter`) | 0، وتنفيذها في `internal/services/auth/core` في S2-D |
| `internal/server/server.go` | `Handlers.Storage` و`Handlers.GCS`، والبادئات (ADR-022 §1)، وتركيبهما بـ `CommonWithLimit` بحد `object_bytes + 1 MiB`، و`/v1/` و`/v1alpha/` لـ `Mgmt`. وحالات في اختبار جدول البادئات | 0 |
| `internal/app/app.go` | `core.New` بـ `blob.NewDisk(cfg.Storage.DataDir)` و`rules.NoDocuments{}` و`rulesInflight` و`svc` (tenancy) و`idtoken.Users` من Auth، و`svc.OnProjectPurged(storageSvc.PurgeProject)`، و**`storageSvc.Boot(ctx)` قبل فتح المستمع** في `Serve`، و`go storageSvc.Run(ctx)` بعده، وتركيب `api` و`gcs` و`console.Routes`. **كله في S2-0** (بأجسام تذعر)، فلا تلمسه المهام بعدها إلا عبر المدمج | 0 |
| `internal/admin/mgmt/rules.go` **جديد** | firebaserules v1 (R1 إلى R8، C §1.5) فوق `tenancy`، و`:test` بـ `rules.Compile` و`Inflight` نفسه | S5-A |
| `internal/admin/mgmt/storage.go` **جديد** | `GET /v1alpha/projects/{p}/defaultBucket`، و`GET /v1beta/projects/{p}/buckets`، و`GET /v1/projects/{p}/services/{name}` (`ENABLED`) و`:enable` | S5-A |
| `internal/app/testdata/console_routes.txt` | مسارات الكونسول الجديدة، بـ `-update` ومراجعة الفرق | S5-B |
| `dashboard/src/features/storage/` **جديد** | الملفات (والرفع ببروتوكول `/v0` إلى رابط الجلسة التي تصدرها اللوحة)، ومحرر القواعد (أخطاء `issues[]` على الأسطر)، والإعدادات (CORS)، والاستعمال | S5-C |
| `internal/admin/web/web.go` | **تضييق CSP اللوحة** إلى مصادر بمسار (`<origin>/_next/static/`) حيث أمكن، والثمن مكتوب (ADR-022 §10.1) | S5-C |
| `test/acceptance/storage_*_test.go` **جديد** | فوق خادم حقيقي وPostgres: كل عملية `/v0` وGCS، والانهيار المحاكى (قتل العملية بين الخطوات في `tools/storage-chaos`، اختياري في S7) | S3، S4 |
| `test/acceptance/isolation_storage_*_test.go` | حالات Storage (ADR-022 §3)، **ملف لكل مالك**: `isolation_storage_test.go` (العقد: bucket لكل مشروع)، و`_s2c_` (رمز التنزيل و`pageToken`)، و`_s2d_` (رابط الرفع)، و`_s2e_` (الـ ruleset)، و`_s4a_` (حساب الخدمة وrewrite) | 0، C، D، E، S4-A |
| `test/acceptance/harness_test.go` | `startStorage`: `Boot` ثم `Run` ثم `Close` كما يفعل `Serve`، والاختبار يُتخطى باسم المالك حتى يكتب S2-D `Boot` (`storage_boot_test.go`) | 0 |
| `test/compat/storage/` **جديد** | مجموعة Storage في firebase-js-sdk عبر حاضنة `test/compat/firebase-js`، واختبارات المطابقة في firebase-tools (غير `emulatorOnly`)، وإعادة `test/oracle/storage/emulator/*.json` على Aruma مع جدول الفروق | S3-D |
| `test/smoke/admin-node`، `test/smoke/admin-go` | سيناريو Storage بـ `@aruma/admin-storage` و`adminstorage` (الرفع بأنواعه، والتنزيل بتحقق CRC، و`getDownloadURL`، والنسخ) | S4-D |
| `test/e2e/{web,android,ios,flutter}-storage/` **جديد** | تطبيقات بالمكتبات الرسمية في وضع الإنتاج، وحارس شبكة يرفض كل طلب إلى Google (نمط `test/experiments/storage-path`)، ومصفوفة `refFromURL` | S6 |
| `test/e2e/browser-matrix/` **جديد** | Playwright على المحركات الثلاثة، بحالات F §2.4 | S3-C |
| `sdk/web/aruma-web/src/{unplugin.js,vite.js,metro.js,flutter-web.js,storage.js}` | محوّل `unplugin` واحد (Vite وRollup وwebpack وRspack وesbuild) يبدّل `DEFAULT_HOST` مع `ssr.noExternal` وفشل البناء الصاخب، ومحوّل Metro، وأداة Flutter web، و`useArumaStorage(storage)` ولفّ XHR و`fetch`. ويحل `storage.js` محل `storage-spike.js` | S6-W |
| `sdk/android/aruma-storage-gradle/` **جديد** | إضافة Gradle `dev.aruma.storage` (ADR-022 §16.2): الـ visitor الذي يرمي، ومهمة التحقق على الأصناف النهائية، والمضيف lowercase، وإشعار `META-INF/aruma/firebase-storage-MODIFIED.txt` | S6-A |
| `sdk/android/aruma-redirect/` | نطاق Storage في النطاقات المحوَّلة افتراضيًا (يحل محل `setStorageSpike`)، والتحقق من مضيف `NetworkRequest.PROD_BASE_URL` عند الإقلاع، و`ArumaUrls.fromArumaUrl` | S6-A |
| `sdk/ios/ArumaRedirect/` | نطاق Storage افتراضيًا، و`fromArumaUrl` (والفحص المسبق قبل `reference(forURL:)`)، و`Scripts/aruma_storage.rb` (مساعد `post_install` لإصدارات CocoaPods المجمّدة) | S6-I |
| `sdk/flutter/aruma_storage/` **جديد** | حزمة Dart: `fromArumaUrl` (الرابط الموحّد إلى `gs://<bucket>/<path>` قبل `refFromURL`) | S6-F |
| `sdk/ios/firebase-storage-patch/` **جديد** | `storage-host.patch` (`ArumaURL`، و`var port: Int?`)، و`make-mirror.sh <upstream-tag>` الذي يبني commit مرقّعًا **ويوسمه باسم الإصدار الرسمي نفسه** في المرآة (لا وسوم غير مرقّعة)، ويشغّل اختبارات FirebaseStorage، وREADME للمرآة بإشعار Apache-2.0 وأمر `swift package config set-mirror` | S6-I |
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
- **كل كتابة تجعل ملفًا مرئيًا في D:** `commitObject` و`Insert` والإنهاء و**`Rewrite`**، فلا ينتظر C شيئًا من D. والمساعدات التي يحتاجها C وD وE معًا (الأسماء، والمقاطع، والقيم الافتراضية، وتنقية الرموز، وتوليدها وختمها) **تعمل من العقد** في `helpers.go`، كما عملت `position` من عقد S1-0، فلا ينتظر D ولا E شيئًا من C.
- **دالة هروب واحدة** لـ `Content-Disposition` الافتراضي، في `core/helpers.go`.
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
	CreateStaging(key string) (Staging, error)          // at start only
	OpenStaging(key string) (Staging, error)            // chunks: never creates
	Walk(prefix string, fn func(key string, modTime time.Time) error) error
	DeletePrefix(prefix string) error                   // resumable
	FreeBytes() (int64, error)
}
type Writer interface { io.Writer; Commit() error; Abort() error } // Commit: fsync file and directory
type Reader interface { io.ReadSeekCloser; Size() int64 }
type Staging interface { Append(r io.Reader, limit int64) (int64, error); Truncate(n int64) error; Sync() error; Size() (int64, error); Close() error }
func ValidKey(key string) bool
func ObjectKey(project int64) string   // p/<n>/<uuid>
func StagingKey(project int64) string  // s/<n>/<uuid>
func NewDisk(root string) (*Disk, error)

// Lock is held for the life of the process (ADR-022 §5.1).
type Lock interface{ Instance() (string, bool, error); SetInstance(id string) error; Release() error }
func AcquireLock(ctx context.Context, root string, log func(waited time.Duration)) (Lock, error) // lock.go: flock(LOCK_EX) on <root>/LOCK
```

```go
// package core: types complete in S2-0; method bodies panic with their owner
type Principal struct {
	Kind  PrincipalKind  // Anonymous, User, Admin
	UID   string         // User
	Token map[string]any // User: the verified ID token's claims, for request.auth
	AuthTime, IssuedAt time.Time // User: auth_time and iat, for idtoken.UserState.Admits at finalize
	ServiceAccount string // Admin: for audit
	ConsoleAccount string // Admin through the dashboard: re-checked at finalize, audited
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
	IP       netip.Addr  // the sessions-per-IP limit; the zero Addr counts against none
}
type Chunk struct {
	Offset   int64
	Body     io.Reader
	Length   int64 // -1 when unknown (chunked)
	Finalize bool
	Total    int64 // -1 when unknown
	Want     Checksums // this request's claim (Go's X-Goog-Hash on the last one); merged at finalize
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
	Users     idtoken.Users     // live user state at finalize
	DataDir   string            // storage.data_dir, which Boot locks
	GCGrace   time.Duration     // storage.gc_grace; 0 is one minute
	Metrics   *Metrics          // nil: unregistered collectors
	Now       func() time.Time
}

// Service holds every private field the tasks need from S2-0 on: pool, store, lock, keys,
// per-session mutexes, the in-flight byte counter, the boot candidate list, limits and logger.
type Service struct{ /* complete in S2-0 */ }

func (s *Service) Limits() Limits                                                                            // 0
func (s *Service) Now() time.Time                                                                            // 0
func (s *Service) Bucket(ctx context.Context, name string) (Bucket, error)                                  // 0
func (s *Service) Stat(ctx context.Context, b Bucket, name string, p Principal, sf Surface, gen int64) (Object, error) // C
func (s *Service) Open(ctx context.Context, b Bucket, name string, p Principal, token string, gen int64) (Object, blob.Reader, error) // C
func (s *Service) List(ctx context.Context, b Bucket, p Principal, o ListOptions) (ListPage, error)          // C
func (s *Service) Update(ctx context.Context, b Bucket, name string, p Principal, sf Surface, u Patch, pre Preconditions) (Object, error) // C
func (s *Service) Delete(ctx context.Context, b Bucket, name string, p Principal, pre Preconditions) error   // C
func (s *Service) Rewrite(ctx context.Context, src Bucket, srcName string, dst Bucket, dstName string, p Principal, attrs *Patch, pre Preconditions) (Object, error) // D
func (s *Service) CreateToken(ctx context.Context, b Bucket, name string, p Principal) (Object, error)       // C
func (s *Service) DeleteToken(ctx context.Context, b Bucket, name string, p Principal, token string) (Object, error) // C
func (s *Service) CORS(ctx context.Context, b Bucket) ([]CORSRule, error)                                   // C
func (s *Service) SetCORS(ctx context.Context, b Bucket, p Principal, rules []CORSRule) (BucketInfo, error) // C
func (s *Service) BucketInfo(ctx context.Context, b Bucket) (BucketInfo, error)                              // C: metageneration, created, updated, CORS
func (s *Service) MatchCORS(rules []CORSRule, origin, method string) (allow bool, rule CORSRule)            // C
func (s *Service) Usage(ctx context.Context, b Bucket) (Usage, error)                                      // D
func (s *Service) Insert(ctx context.Context, b Bucket, p Principal, spec UploadSpec, body io.Reader, length int64) (Object, error) // D
func (s *Service) StartUpload(ctx context.Context, b Bucket, p Principal, spec UploadSpec) (id string, err error) // D
func (s *Service) WriteUpload(ctx context.Context, b Bucket, name, id string, p *Principal, c Chunk) (UploadState, error) // D; p nil when unauthenticated
func (s *Service) QueryUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error)         // D
func (s *Service) CancelUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error)        // D
func (s *Service) PurgeProject(ctx context.Context, project int64) error                                   // D
func (s *Service) Boot(ctx context.Context) error   // D: before the listener opens: lock, instance marker, candidates, quota reconcile
func (s *Service) Run(ctx context.Context) error    // D: after: candidate deletion, GC, expiry, reports
func (s *Service) Close() error                     // D: releases the lock once Server.Run has drained
func (s *Service) AllowUploadStart(ip netip.Addr) (bool, time.Duration) // 0: per IP, then the process, for both surfaces
func (s *Service) Metrics() *Metrics                // 0

// inside the package
func (s *Service) commitObject(ctx context.Context, w objectWrite) (Object, error) // D: the only transaction that makes a blob visible
func (s *Service) authorize(ctx context.Context, b Bucket, p Principal, op ruleOp) error // E: Admin bypasses; ErrPermissionDenied or ErrOverloaded

// helpers.go: working in S2-0, shared by C, D and E
func validateName(name string) error
func segments(name string) []string
func applyDefaults(o *Object, sf Surface)             // contentType, and defaultDisposition on SurfaceFirebase
func defaultDisposition(name string) string           // the one escaping function (PY-RR)
func StripClientTokens(md map[string]*string)
func newDownloadToken() string
func (s *Service) sealTokens(project int64, name string, tokens []string) ([]byte, error)
func (s *Service) openTokens(project int64, name string, sealed []byte) ([]string, error)
type ruleOp struct { Method rules.Method; Name string; List bool; Resource, Incoming *Object; Patch *Patch; Surface Surface }
```

- **أخطاء `core`** في `errors.go` كاملة من العقد، و`errors.Is` عليها في الواجهات. ولا تُضاف حالة جديدة إلا عبر المدمج.
- **`authorize` في العقد ترفض كل شيء لغير Admin** (`ErrPermissionDenied`) حتى تُدمج E. فتعمل اختبارات C وD بـ Admin، أو بـ `authorizeHook` اختباري في الحزمة.
- **`wire` و`media`:** الأنواع والتوقيعات كاملة في العقد (الجداول أعلاه)، والأجسام تذعر.
- **`api` و`gcs`:** `New` و`Handler()` في العقد، و`Handler` يرد 501 لكل مسار حتى تُدمج S3 وS4. والتركيب في `server` و`app` يعمل من العقد.
- **وفيه:** المخطط، وsqlc بهياكل الجداول، وقواعد archtest كلها، وقسم الإعداد بفحوصه، و`ProjectForBucket`، وغرض المفتاح الجديد في `keys.go`، وواجهة `idtoken.Users`، ومساعد المعاملات `begin`/`inTx` (لا `RuntimeParams`، انظر القرارات أدناه)، والتركيب في `app` و`server` كاملًا (`Boot` و`Run` و`OnProjectPurged` تذعر حتى D). فلا تلمس مهمة بعد العقد `app.go` ولا `server.go` إلا عبر المدمج.

#### قرارات commit العقد (لم تكن في المسودة)

حسمها S2-0 حيث سكت الهيكل أو تعارض، وكلها في الكود (والاختبار `core/contract_test.go` يثبّت توقيعات `Service` وطرقه المصدَّرة وحقول الأنواع):

- **`gc_grace` في `storage.gc_grace` و`Options.GCGrace`، لا في `Limits`:** ADR-022 §5 والهيكل يضعانه في القسم، وجدول §17 تحت `storage.limits`. أخذنا الأولين: هو مهلة تشغيل لا حد يصمد أمام مشروع معادٍ.
- **أسماء الحدود التي لم يسمّها §17:** `upload_sessions_per_user` (50) و`upload_sessions_per_ip` (100) بجانب `upload_sessions_per_project`، و`upload_starts_per_second` (1,000)، و`list_max_results` لا يتجاوز 1000. والحدود على الطلبات (المعدلات لكل IP، والطلبات المتزامنة، وأدنى معدل) في `core.Limits` أيضًا ليبقى `storage.limits` نوعًا واحدًا، وتفرضها الواجهتان.
- **`Principal.AuthTime` و`IssuedAt`، و`idtoken.UserState.Admits(authTime, issuedAt)`:** فحص الحالة الحية عند الإنهاء يحتاجهما بترتيب `Verifier` نفسه (محذوف، ثم معطّل، ثم مُبطل أو رمز أقدم من المستخدم). و`UserState` فيها `Created` لهذا. وتنفيذ `Users()` في `auth/core/userstate.go`، و`State` يذعر حتى S2-D. لا مستأجرين في v0.3، فلا `tenant` في التوقيع.
- **`UploadSpec.IP` وعمود `client_ip`:** حد الجلسات لكل IP يحتاج العنوان عند `start`، ولا موضع له في التوقيع.
- **`final_status` و`final_body` يحفظان رمز خطأ `core` ورسالته، لا حالة HTTP:** `core` لا تعرف HTTP. و`encodeError`/`decodeError` في `errors.go` تعملان من العقد، وكل واجهة تربط الخطأ بحالتها. وأُضيفت `ErrUnauthenticated` (401 عند الإنهاء) و`ErrUploadCancelled` و`ErrTooManyUploads` و`ErrUnavailable` إلى الأخطاء المسمّاة.
- **الأخطاء النهائية قائمة مغلقة (`finalError`، مراجعة S2-0):** القواعد 403، والحصة 402، والحجم المخالف 400، و`md5` المخالف 400، والحد 413، والهوية التي سقطت 401، والجلسة الملغاة. و`encodeError` يرفض غيرها، فلا يصير 503 ولا `ErrOverloaded` ولا «غير موجود» `final` أبدًا. والنجاح يحفظ لقطة الكائن الملتزم (`result_object`، والرموز مختومة في `download_tokens` للجلسة)، فيعيد التكرار ما أرسله الرد الأول ولو كُتب فوق الكائن أو حُذف.
- **`synchronous_commit` لكل معاملة Storage، لا للمجمّع (مراجعة S2-0):** `store.Open` عاد كما كان، فـ PgBouncer الافتراضي يرفض معامل البدء، والإعداد كان يشمل كل الخدمات. والمساعد الوحيد `begin`/`inTx` في `durability.go`، وتحذير `fsync` في `warnUnsafeDurability`. ADR-022 §5 معدَّل.
- **لا `RETURNING old.*` (مراجعة S2-0):** sqlc 1.31.1 لا يولّده. فـ `commitObject` = `InsertObject` (`DO NOTHING`) ثم `LockObject` (`FOR UPDATE`) ثم `ReplaceObject`، والجمل مكتوبة ومولّدة في العقد، ومعها اختبار الكتابتين المتزامنتين وسباق `ifGenerationMatch=0`. ADR-022 §5 معدَّل.
- **قيود المخطط (مراجعة S2-0):** كل مفتاح ملف في صف يسمّي مشروع الصف (`split_part(key, '/', 2) = project_number::text` على `objects` و`upload_sessions` و`blob_gc`)، ولا `firebaseStorageDownloadTokens` في `objects.metadata` ولا في `attrs` الجلسة، ولجلسة `final` نتيجة واحدة بالضبط. والرموز التي يضبطها Admin عند البدء في `upload_sessions.download_tokens` مختومة.
- **الفهارس والانتهاء (مراجعة S2-0):** حد الجلسات لكل IP **لكل مشروع**، بفهرس `(project_number, client_ip) WHERE state='active'`، و`(progressed_at) WHERE state='active'` للانتهاء الخامل. والجلسة الفارغة: `expires_at = start + empty_session_ttl`، ويُمدّ إلى عمر الجلسة عند أول بايت متين، فيخدم فهرس `expires_at` الانتهاءين.
- **سقف الجسم:** `object_bytes` ≤ 5 TiB و`json_body_bytes` ≤ 1 MiB، و`Limits.BodyCap()` = المجموع و64 KiB للحدود (والنسخة نفسها في `config.StorageLimits`)، فلا فيض.
- **`storage.*` لا يُفحص وStorage معطّل،** و`/v1/projects/` و`/v1alpha/projects/` لا تُركَّب إلا مع Storage (حتى يحتاجها Firestore)، فخادم بلا Storage يوجّه كما كان على main (`TestStorageRouting`).
- **التركيب كامل لـ E وS5 (مراجعة S2-0):** `tenancy.Options.RulesLimits`، و`mgmt.Options{RulesLimits, Inflight}`، و`console.Options{PublicURL, ProjectDomain, Rules, Audit}` (و`console.Rules` واجهة على `tenancy`)، وتوقيعات الـ rulesets والإصدارات في `tenancy/core/rules.go` تذعر بأسماء مالكيها. فلا تلمس مهمة `app.go` ولا `mgmt.go` ولا `tenancy/core/service.go`.
- **`BucketInfo` و`SetCORS` يعيدان مورد الـ bucket** (الـ metageneration والوقتان وCORS) لـ `buckets.get/patch`.
- **`Options.DataDir`** يعطي `Boot` مجلد القفل بلا تحويل نوع، و`Close` (D) يحرر القفل بعد أن يصرف `Server.Run` طلباته، وهو في `app.go` و`startStorage`.
- **`AllowUploadStart(ip)`:** محدِّد واحد للعملية ولكل IP (`upload_starts_per_minute_per_ip` ثم `upload_starts_per_second`) تستعمله الواجهتان واللوحة.
- **`storage/core` لا تستورد `storage/wire`** (قاعدة archtest خاصة بـ Storage).
- **الجولة الثالثة:** `mgmt.routes()` يستدعي `rulesRoutes` و`storageRoutes` حين Storage مفعّل، وهما فارغتان في `mgmt/rules.go` و`mgmt/storage.go` لـ S5-A، فلا يلمس S5-A `mgmt.go`. **والشروط المسبقة لـ GCS وحده:** `checkPre` يرد `ErrInvalidArgument` على `Pre` غير فارغ من سطح آخر، في `StartUpload` و`Insert` و`Update` و`Delete` (و`Delete` أخذ `Surface`)، قبل أي عمل، لأن 412 يبقي الجلسة `active` فيدخل Android وiOS في حلقة. **و`s.reads` للقراءة وحدها** (كان `s.q`): كل كتابة في `inTx` أو `begin`، و`TestWritesOnlyInTransactions` (فحص AST) يفشل على جملة كتابة أو `FOR UPDATE` خارج معاملة، أو `queries.New` على المجمّع، أو `pool.Exec`. **و`result_object` لا يحمل `firebaseStorageDownloadTokens` في `metadata`** (قيد في المخطط).
- **`download_tokens` بقيمة NULL حين لا رموز:** هي `bytea` مختومة، فشرط «بلا رموز» في §9 يصير `download_tokens IS NULL` لا `cardinality(...) = 0`.
- **أعمدة زائدة على §3:** `upload_sessions.surface` (`firebase`/`gcs`/`console`) بدل `protocol`، لأن جلسة اللوحة تختلف في عمرها وحجمها المعلن وإعادة فحص الحساب؛ و`hash_state` لحالة الهاشين مع `received` (§6.3)؛ و`usage.anonymous_reserved_bytes` لسقف المجهولين؛ و`buckets.metageneration` و`updated` لمورد GCS؛ و`instance.one_row` لفرض الصف الواحد.
- **الاستعلامات في العقد قراءة واحدة لكل جدول** (`GetObject` و`GetBucketSettings` و`GetUploadSession` و`GetUsage` و`GetInstance`): sqlc وscopecheck يحتاجان استعلامًا في كل ملف، ويضيف C وD الباقي.
- **`core.Projects`** فيها `ProjectForBucket` و`GetProject` (إعادة فحص حساب اللوحة) و`CompiledRelease` و`Audit`. فأُضيف إلى `tenancy` `Audit` عاملًا (سجل التدقيق لكتابات Storage)، و`CompiledRelease` في `tenancy/core/rules.go` يذعر حتى S2-E.
- **`applyDefaults` يضع `defaultDisposition` على `SurfaceConsole` أيضًا:** جلسة اللوحة ترفع عبر `/v0` (§15).
- **`media.Mode` فيها `GCS` رابعة،** لأن §10.5 يخدم على GCS القيمة المخزّنة كما هي ومعها `x-goog-*`. و`Serve` يعيد `error` يقول لماذا توقف، للمقاييس.
- **قاعدة archtest للكونسول خاصة بـ Storage** (`internal/services/storage/console`): `auth/console` يستورد `auth/api` (`users.go:10`)، فقاعدة `*/console` العامة كانت تكسره. وقواعد `*/api` و`*/gcs` و`*/media` عامة كما في الهيكل.
- **`AcquireLock` في `lock.go`** (ملف A) لا في `blob.go`، و`NewDisk` يعمل من العقد (مسار مطلق فقط)، ويكمله A بالإنشاء وفحص الكتابة. ومساعدان عاملان: `ProjectPrefix` و`StagingPrefix`.
- **`wire` في العقد:** `ObjectPath` (ناتج `SplitObjectPath`، ومعه `Rest` لـ `rewriteTo`)، و`UploadCommands` (مجموعة بتّات)، و`ContentRange` مع `Query()` و`Final()` العاملتين، و`RelatedReader`، و`Range`، وثوابت البادئات وأسماء الترويسات، و`Time(time.Time)`.
- **`server`:** البادئات `/v1/projects/` و`/v1alpha/projects/` (أضيق، كما في جدول §1)، والتوجيه إلى Storage بالبادئة وحدها بلا `ServeMux`، فلا يُنظَّف مسار فيه اسم كائن ولا يُعاد توجيهه. و`mgmt.Options.Storage` جاهز لـ S5-A.
- **`Run` يبدأ بجانب `Server.Run`** (قبله مباشرة في goroutine)، لا بعد فتح المستمع بالضبط: لا خطّاف في `server` لذلك، ولا ضرر، فالمرشحون أُخذوا في `Boot`.
- **المقاييس في العقد** (§18، `core/metrics.go`): أسماؤها وتسمياتها ثابتة، ولكل مقياس مالكه في تعليقه (D للجلسات والحصة والقرص وGC والمسح والالتزامات الملتبسة والقفل، وE للقواعد، وS3/S4 للبايتات وفك الضغط المقطوع)، ويسجّلها `app` على سجل الخادم، وتبقى صفرًا حتى يبلّغ مالكها.
- **`ctx` في `core.New`** لا يُستعمل اليوم، و`app.New` يمرر `context.Background()`.

#### قرارات S2-E (لم تكن في المسودة)

- **`prepareRules` قبل كل قرار، وقبل أي معاملة:** `ctx, err := s.prepareRules(ctx, b, p)`، ثم `authorize` بهذا الـ `ctx`. و`authorize` لا يقرأ الإصدارة أبدًا: بلا العلامة يسجّل خطأ ويرد `ErrUnavailable`، مع `authorizeHook` أيضًا، فتكشف اختبارات C وD النداء المنسي. فلا تأخذ قرارًا تحت قفل صف اتصالًا ثانيًا من المجمّع (وإلا تقف الكتابات حين يمتلئ). على C في `Stat` و`Open` و`List` و`Update` و`Delete` والرموز، وعلى D في `Insert` والإنهاء و`Rewrite` (للـ bucketين). وAdmin يأخذ العلامة بلا قراءة. `TestDecidingUnderALockNeedsNoConnection` (مجمّع من اتصالين) و`TestAuthorizeWithoutPrepareRulesFails`. وتحميل الإصدارة المشترك يجري تحت `WithoutCancel` بمهلة 10 ثوانٍ، وكل منتظر يتوقف بسياقه.
- **ذاكرة `CompiledRelease` معلّقة بمفتاح ضعيف على `*tenancy.Service`** في `tenancy/core/rules.go`، لأن حقول `Service` من العقد (`service.go`). تزول مع الخدمة (`runtime.AddCleanup`).
- **`etag` في القواعد** يُحسب في `core/rules_fields.go` (`objectETag`) بصيغة `wire.ETag` نفسها (S2-B)، فـ `core` لا تستورد `wire`. وأي تغيير في إحداهما يغيّر الأخرى.
- **الإصدارة الغائبة لا تُكتب صفًا:** `CompiledRelease` يعيد القاعدة المقفلة المترجمة مرة واحدة، و`GetRelease` يرد NOT_FOUND، فتكتب firebase-tools القالب (C §2.2).
- **`PublishRelease`** يقبل اسم الـ ruleset كاملًا (بمعرّف المشروع أو رقمه) أو الـ uuid وحده، ويرفض ruleset خدمة أخرى لإصدارتَي Storage و`cloud.firestore`. و`firebase.storage/default` من الخارج NOT_FOUND كأي bucket آخر.

## المهام

### S2: النواة (بعد S2-0، متوازية)

| المهمة | تملك | تعتمد على | تنتهي حين |
|---|---|---|---|
| **S2-A: البايتات** | `blob/` كلها | العقد فقط | `Create` لا يكتب فوق موجود، و`Commit` يستدعي fsync للملف وللمجلد (اختبار بغلاف `syscall` أو بفحص التسلسل)، و`mkdirDurable` يزامن أب كل مجلد جديد، و`Promote` متين، و`OpenStaging` لا ينشئ، والقفل يمنع عملية ثانية ويحرره الخروج، والمفاتيح الغريبة ترفض قبل لمس القرص، و`FuzzKey` عشر دقائق بلا ذعر، وكلفة fsync مقيسة في `docs/research/storage/baseline.md` |
| **S2-B: صيغ السلك** | `wire/` كلها | العقد فقط | كل نوع يطابق أمثلة A §4 وB §7 والتسجيلات، و`MediaETag` لا يتغير مع الـ metageneration، وأهداف fuzz الخمسة تعمل عشر دقائق لكل منها بلا ذعر وبذاكرة محدودة |
| **S2-C: الكائنات** | `names.go` و`metadata.go` و`objects.go` و`tokens.go` و`list.go` و`pagetoken.go` و`cors.go`، و`queries/objects.sql`، واختباراتها، وحالات العزل للرموز و`pageToken` | العقد فقط | جداول كل عملية، والتوليد الذري للرمز تحت تزامن (رمز واحد مهما تزامنت القراءات)، والرموز مختومة في القاعدة (لا نص خام في `pg_dump`)، والرمز الخاطئ 403، والقائمة بالقفز ≤ `maxResults+1` بحثًا على مليون اسم، و`pageToken` مشروع على مشروع آخر 400 |
| **S2-D: الرفع والكتابة والحياة** | `uploads.go` و`insert.go` (ومعه `Rewrite`) و`hashes.go` و`commit.go` و`quota.go` و`gc.go` و`boot.go` و`sweep.go` و`purge.go` و`run.go`، و`queries/{uploads,usage,gc}.sql`، وتنفيذ `idtoken.Users` في `internal/services/auth/core`، واختباراتها، وحالات العزل لرابط الرفع | العقد، و`blob` الحقيقي بعد دمج A (قبله `blob` في الذاكرة داخل الاختبار، و`boot_test.go` بعد A) | كل أشكال الإزاحة، وكل رفض نهائي `final` يُعاد على الإنهاء و`query`، والانهيار المحاكى بين كل خطوتين من ترتيب الكتابة يترك حالة يصلحها الطلب التالي أو المسح، و`COMMIT` الملتبس لا يضيّع نسخة، وقطعتان متزامنتان على جلسة لا تتداخلان، ومئة رفع متوازٍ لا تتجاوز الحصة، و`start` بمعلن كبير بلا هوية لا يحجز شيئًا، والمجهولون محدودون بخُمس الحصة، وجلسة لوحة بعد إبعاد الحساب عن المشروع لا تُنهى، والمصالحة بعد انهيار، ومستخدم معطّل لا يُنهي، و`PurgeProject` مرتين متتاليتين ومقطوعة في المنتصف تنتهي نظيفة |
| **S2-E: ربط القواعد** | `rules.go` و`rules_fields.go` و`authorize`، وmigration `control` وعمل `tenancy` على القواعد (rules-layout.md:115-118) ومعه الاسم المنطقي للإصدارة وحذف الجدولين في `PurgeExpired`، واختباراتها، وحالات العزل للقواعد | العقد، و**S1 مدموجًا**، وملفات Pyric من `origin/s0-rules-oracle` | جدول ADR-022 §8 حالةً حالة، و`overwrite/verb-create` و`resource-existing` و`keys-size-15` وأخواتها من PY-RR تمر عبر ربطنا، وlist بـ `UnknownSegment` يطابق `aruma/storage/read_list` و`list_on_folder` للمحاكي، والإصدارة الغائبة ترفض، و`ErrOverloaded` يصير 503 لا 403، وتغيير `project_domain` لا يمس القواعد المنشورة، ومشروع مُطهَّر بلا rulesets |
| **S2-F: خدمة البايتات** | `media/` كلها | العقد و`wire` (أنواعه في العقد) | جداول الترويسات لكل نوع، و`Cache-Control` لكل سطح، وجدول `Sec-Fetch-Site`×`Sec-Fetch-Dest`، وRange، وقنبلة gzip بذاكرة ثابتة. ومصفوفة المتصفحات في S3-C |

- **لا انتظار دائري:** C وE لا يعتمدان على D، وD لا يعتمد على C ولا E (يختبر بـ Admin أو `authorizeHook`)، وA وB وF على العقد وحده.
- **S2 يُغلق** بدمج A ثم B ثم F ثم C ثم D ثم E، و`make lint` و`make test` و`go test -race ./internal/services/storage/...`.

### S3: Firebase REST `/v0` (يبدأ بعد S2-0، وينتهي بعد S2)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S3-A: الأساس** | `api.go` و`auth.go` و`errors.go` و`cors.go` و`objects.go` و`list.go` و`resource.go`، و`test/acceptance/storage_v0_test.go` | كل عملية غير الرفع والتنزيل بأشكال A §7، و`AssertNoNull` على كل رد، وbucket مجهول 404 بشكل الإنتاج، ومسار مجهول 400 |
| **S3-B: الرفع** | `upload.go` و`multipart.go`، و`test/acceptance/storage_upload_test.go` | البروتوكول كما في A §7 لكل منصة (طلبات الويب وAndroid وiOS المسجّلة في `test/oracle/storage/emulator/sdk_upload.json` و`raw_resumable.json` مع تصحيح ما يخالف فيه المحاكي الإنتاج)، والرابط المطلق من `public_url` خلف وكيل، وقطع بلا مصادقة و`Authorization` عليها يُتجاهل، و`final` على كل رفض نهائي (Android لا يعيد القطعة، وiOS لا يدخل حلقة `query`)، و`md5Hash` المختلف 400، ورفع أطول من 60 ثانية يصل رده، وslowloris |
| **S3-C: التنزيل والمتصفحات** | `download.go`، و`test/e2e/browser-matrix/` | الرمز والقواعد، وCORS الـ bucket على `alt=media`، والمصفوفة على المحركات الثلاثة بحالات F §2.4 |
| **S3-D: التوافق** | `test/compat/storage/`، وهيكل `docs/compat/storage-v0.3.md` | مجموعة Storage في firebase-js-sdk، واختبارات المطابقة غير `emulatorOnly`، وإعادة تسجيلات المحاكي: كل فرق مصنّف (خطأ عندنا، أو فرق ADR-023 §3، أو خطأ في المحاكي) |

### S4: GCS JSON وAdmin (يبدأ بعد S2-0، وينتهي بعد S2)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S4-A: الأساس** | `gcs.go` و`auth.go` و`errors.go` و`objects.go` و`buckets.go` و`resource.go`، وحالات العزل لحساب الخدمة | B §10.1 (عدا الرفع والوسائط)، وطلب بلا `Bearer` 401، وحساب مشروع على bucket آخر 403، و`code` في الخطأ رقم = الحالة، و`exists()` في Node يعيد `false` على 404 |
| **S4-B: الرفع** | `upload.go` | الـ metadata من جسم البدء و`X-Upload-Content-Type` معًا، وNode `save()` (الطلب الواحد `bytes 0-*/*`، والمقطّع بـ 308 و`Range`، و`resumable:false`)، وGo (multipart تحت 16 MiB، وresumable بالـ override، والجزء الأخير الفارغ)، والـ checksums المختلفة 400 بلا كائن، و`ifGenerationMatch=0` على موجود 412 |
| **S4-C: الوسائط** | `download.go` | `file.download()` في Node بتحقق CRC الافتراضي، وGo بـ `WithJSONReads` مع `generation`، و`x-goog-*` كلها، و`Attrs.CacheControl` في Go يساوي المخزّن |
| **S4-D: الحزمتان** | `sdk/node/admin-storage/` و`sdk/go/adminstorage/`، وسيناريو Storage في `test/smoke/admin-{node,go}` | `universeDomain` يحمل `Bearer` على بدء `save()` وقطعه (يُعاد قياسه على مفتاح فيه `universe_domain`، P)، و`getDownloadURL` الخاص يعيد الرابط الموحّد، والحزمتان ترفضان متغيرات المحاكي، وحارس الشبكة لا يرى طلبًا إلى Google |

### S5: الإدارة واللوحة (يبدأ بعد S2-0، وS5-A بعد S2-E)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S5-A: واجهات الأداة** | `internal/admin/mgmt/rules.go` و`storage.go`، واختباراتهما | كل طلب في C §1.5 و§1.3 و§1.4 بالحالة 200 حرفيًا وأشكاله، و`:test` بـ `issues[]` على الخطأ |
| **S5-B: واجهة اللوحة** | `internal/services/storage/console/`، و`console_routes.txt` | كل مسار يطلب اعتمادًا (ADR-010 S-4) ويمر بـ CSRF، ومسار إصدار جلسة الرفع يعيد رابط `/v0` بهوية Admin وحساب اللوحة، بعمر ساعة وحجم معلن إلزامي، ويُكتب الإصدار والإنهاء في سجل التدقيق، ولا مسار كونسول يقبل جسمًا فوق 1 MiB، والتنزيل `attachment` دائمًا بترويسات `media` |
| **S5-C: الواجهة** | `dashboard/src/features/storage/`، وتضييق CSP في `internal/admin/web/web.go` | الملفات (تصفح، ورفع متقطع كبير بجلسة تصدرها اللوحة وقطع إلى `/v0`، وحذف، وmetadata، ورموز)، ومحرر القواعد بأخطائه على الأسطر، وCORS، والاستعمال. وe2e في Playwright |
| **S5-D: أداة `firebase`** | e2e في `test/acceptance` أو `test/e2e/cli-storage` | `firebase init storage` يكتب القالب أو القواعد المنشورة، و`firebase deploy --only storage` ينشر وتطبَّق القواعد فورًا على `/v0`، والنشر الثاني بلا تغيير «already up to date»، وخطأ ترجمة يطبع `[E] line:column` |

### S6: مسار الإنتاج لكل منصة (يبدأ بعد S3-B على خادم يعمل)

| المهمة | تملك | تنتهي حين |
|---|---|---|
| **S6-W: الويب** | `sdk/web/aruma-web` (`unplugin` ومحوّل Metro وأداة Flutter web و`useArumaStorage` والاحتياط)، و`test/e2e/web-storage/` | **التبديل إلى النطاق وحده مقيس في متصفح** على بناء npm حقيقي بـ Vite 8 وwebpack وesbuild: 11/11 بلا ⚠، و`<img>` بالرابط الموحّد. وتطبيق SSR (SvelteKit) يُحوَّل، والبناء يفشل حين لا وحدة حُوّلت وحين يُرى استيراد gstatic. وMetro على تطبيق Expo. **ويُقاس ويُكتب ما يعمل:** Turbopack، وVite 5 إلى 7، وخطوة Flutter web (`firebase-storage.js` المثبّت مبدَّلًا مرة واحدة، و`flutterfire_ignore_scripts`). وما لا يعمل يأخذ الاحتياط موثّقًا |
| **S6-A: Android** | `sdk/android/aruma-storage-gradle/`، وتعديل `aruma-redirect`، و`test/e2e/android-storage/`، و`NOTICE` | تطبيق e2e على AVD `aruma-a34` (وحده عليه): 12/12 بلا ⚠، والرابط يساوي الشكل الموحّد حرفيًا (والمضيف lowercase ولو كُتب بأحرف كبيرة)، وiptables لا يرفض حزمة واحدة. **والفشل المغلق بطبقاته الثلاث:** الـ visitor يرمي على نص مفقود، ومهمة التحقق تفشل حين يُستثنى الصنف بـ `excludes`، وحارس `aruma-redirect` يوقف تطبيقًا بلا الإضافة ولا تحويل. والإشعار في `META-INF/aruma/firebase-storage-MODIFIED.txt` موجود في الـ APK النهائي وفي APK مشتق من AAB (`bundletool`) |
| **S6-I: iOS** | `sdk/ios/ArumaRedirect` (النطاق، و`fromArumaUrl`، و`aruma_storage.rb`)، و`sdk/ios/firebase-storage-patch/`، و`test/e2e/ios-storage/`، و`NOTICE` | **SwiftPM أولًا:** تطبيق e2e على المرآة بـ `set-mirror` و`from:`/`exact:` بالإصدار الرسمي: 11/11 بلا ⚠، و**`downloadURL` يساوي الشكل الموحّد حرفيًا** (بلا `:443`)، و**اختبارات FirebaseStorage الرسمية تمر بلا `ArumaURL`** (سلوك الأصل وسطر سجل، و`ArumaRedirect` يمسك الطلبات)، واختبار مضاف أن `ArumaURL` بلا منفذ يعطي `url.port == nil`، والرقعة تغيّر المواضع الأربعة (`Storage.swift:225` و`:287`، و`StorageGetDownloadURLTask.swift:64`، و`StorageUtils.swift:31`)، والرفع في الخلفية (`putFile` بجلسة خلفية) إلى Aruma، و`make-mirror.sh` يبني commit مرقّعًا بوسم الإصدار الرسمي ويمر باختبارات FirebaseStorage. **ويقيس أن Xcode و`flutter build ios` يحترمان المرآة**، وإلا يكتب البديل (رابط المرآة مكان الرسمي في Xcode، والاحتياط لـ Flutter iOS)، **ويقيس هل يرفض SwiftPM المرآة بهوية `firebase-ios-sdk` بجانب الرابط الرسمي بالهوية نفسها** (FlutterFire). وتطبيق CocoaPods على آخر إصدار منشور هناك، و`pod install` يفشل على سطر مفقود. **ويُسجَّل طلب `updateMetadata` الحقيقي من iOS** (هل يرسل `"metadata": null`، A §8.1، ADR-023 §3-17) |
| **S6-F: Flutter** | `sdk/flutter/aruma_storage/`، و`test/e2e/flutter-storage/` | بعد S6-A وS6-I (الجهاز نفسه): Android بالإضافة وiOS بالمرآة (SwiftPM)، و`Image.network(getDownloadURL())` لا يذهب إلى Google (كان يتسرّب في P)، و`refFromURL(fromArumaUrl(await ref.getDownloadURL()))` يعطي المرجع نفسه (بدونها يرمي، `utils.dart:24`، `:49-56`) |
| **S6-X: عبر المنصات** | مصفوفة `refFromURL` **لكل وضع** (ADR-022 §16.7: الويب بالتبديل وبالمُعدِّل، وAndroid، وiOS، وFlutter): الرابط من كل منصة يُحلَّل على كل أخرى، و`docs/guides/storage.md` (قسم الإنتاج والإشعار القانوني) | المصفوفة تعطي الـ bucket والمسار نفسيهما أو تفشل صراحةً، وروابط iOS تمر بفحص `fromArumaUrl` المسبق فلا يصل شيء إلى `fatalError` (`Storage.swift:151`، `:156`، `:158`) |

- **الأجهزة:** وكيل واحد على `aruma-a34` وعلى المحاكي في وقت واحد (قاعدة الـ brief 7). فـ S6-A ثم S6-F على Android، وS6-I قبل S6-F على iOS. وS6-W بلا جهاز.

### S7: التحقق والإصدار (خارج هذا الهيكل)

مجموعات Google بالوضعين، وe2e كلها بحارس الشبكة، والمصفوفة، وأوراكل القواعد (98%)، و`docs/compat/storage-v0.3.md` بأرقامه، و`docs/security/review-v0.3.md`، ومراجعة أمنية لخدمة البايتات والرفع.

## ما يُقاس في البناء ولم يُقس هنا

- كلفة fsync لكل قطعة على القرص المستهدف (S2-A يقيسها ويضعها في `docs/research/storage/baseline.md`؛ إن ثقلت تُجمَّع القطع الصغيرة قبل `Sync` دون أن يُعلن ما لم يُفرَّغ).
- بدء `save()` المتقطع بـ `universeDomain` على مفتاح فيه `universe_domain` وعلى نطاق فرعي (S4-D، P).
- التبديل إلى النطاق وحده في متصفح حقيقي (قيس قبلها الشكل المسبوق على بناء CDN معدَّل يدويًا، ومُعدِّل `host` في Node وحده، P)، وTurbopack، وVite 5 إلى 7، وخطوة Flutter web (S6-W).
- إضافة Gradle وطبقاتها الثلاث، وبقاء الإشعار في APK وفي APK من AAB (S6-A).
- احترام Xcode و`flutter build ios` للمرآة عبر `set-mirror`، والرقعة بـ `port` اختياري (S6-I).
- `pg_xact_status` بعد انقطاع محاكى أثناء `COMMIT` (S2-D)، و`strategy: Recreate` في مثال Kubernetes وإيقاف ثم تشغيل في أمثلة compose وsystemd (`docs/guides/storage.md`، S6-X)، وسلوك مجلد البيانات وfsync للمجلدات على ext4 وXFS (لا جهاز Linux في المراجعة؛ CI على Linux، S2-A).
