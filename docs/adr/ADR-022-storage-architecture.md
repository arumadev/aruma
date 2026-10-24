# ADR-022: Cloud Storage for Firebase: المعمارية

- الحالة: معتمد
- التاريخ: 2026-10-24 (وثلاث مراجعات عدائية في اليوم نفسه: البناء لكل منصة، وأمانة البروتوكول، والمعمارية؛ 35 نتيجة أُدخلت كلها)
- المرحلة: S0 من الإصدار 0.3 (القواعد وStorage)
- المراجع: [storage-layout.md](../research/storage-layout.md)، ADR-023، ADR-008، ADR-010، ADR-015، ADR-017، ADR-019، ADR-021، و:
  - أبحاث S0 (كلها على main): [A-client-protocol.md](../research/storage/A-client-protocol.md) (اختصارها **A**)، و[B-admin-gcs-protocol.md](../research/storage/B-admin-gcs-protocol.md) (**B**)، و[C-management.md](../research/storage/C-management.md) (**C**)، و[D-emulator.md](../research/storage/D-emulator.md) (**D**)، و[E-production-vs-emulator.md](../research/storage/E-production-vs-emulator.md) (**E**)، و[F-architecture-lessons.md](../research/storage/F-architecture-lessons.md) (**F**)، و[storage-production-path.md](../research/storage-production-path.md) (**P**)، و[rules/B-design-lessons.md](../research/rules/B-design-lessons.md)، و[rules-layout.md](../research/rules-layout.md).
  - تسجيلات المحاكي: `test/oracle/storage/emulator/*.json` (firebase-tools 15.34.0، الجولة `s0c-20261023-emu`)، وتسجيل الإنتاج الوحيد `test/oracle/storage/production/bucket_probe.json`.
  - أوراكل القواعد (فرع `origin/s0-rules-oracle`، غير مدموج بعد): `docs/research/rules/A-oracle-findings.md`، و`test/oracle/rules/parity-exceptions.md`، وملاحظة Pyric على Storage منشور حقيقي `test/oracle/rules/imported/pyric/original/observations/storage-rules/stdlib-realstorage-request-resource-fields.json` (bucket `digame-mas.appspot.com`، 2026-09-27، SDK 12.13.0؛ اختصارها **PY-RR**).
  - وثائق Firebase (قُرئت 2026-10-24): [مرجع قواعد Storage](https://firebase.google.com/docs/reference/security/storage)، و[firebaserules `projects.test`](https://firebase.google.com/docs/reference/rules/rest/v1/projects/test)، و[download-files (web)](https://firebase.google.com/docs/storage/web/download-files)، و[core-syntax](https://firebase.google.com/docs/storage/security/core-syntax).
  - كود المشروع على `230339a`: `internal/server/server.go`، و`internal/app/app.go`، و`internal/tenancy/core/projects.go`، و`internal/platform/httpx/middleware.go`، و`sdk/`.

**قرار المالك الملزم (2026-10-24):** لا تسجيلات إنتاج لـ Storage (المشروع بلا Blaze، E §0). فأدلة «الإنتاج» هنا: الوثائق، وملاحظات Pyric على Storage حقيقي، واختبارات المطابقة في firebase-tools غير المعتمدة على القواعد (E: **CT**)، وكود المكتبات. وكل قرار لم يحسمه دليل إنتاج يُعلَّم «مؤقت» ويُسجَّل فرقًا محتملًا في `docs/compat/storage-v0.3.md` (ADR-023).

## السياق

Storage ثالث خدمة بيانات في Aruma، وأول خدمة تخدم **بايتات يرفعها المستخدم من نطاق اللوحة نفسه**. ما يجعله صعبًا:

- **بروتوكولان للرفع لا واحد،** ولكل منهما فروق بين المكتبات:
  - عملاء Firebase (`/v0`، ترويسات `X-Goog-Upload-*`): الويب يعلن الحجم عند `start`، وAndroid لا يعلنه أبدًا (A §3.10؛ `ResumableUploadStartRequest.java:43-45`)، وiOS يرسل قطعه بـ `PUT` **بلا أي مصادقة** (A §1.1؛ `GTMSessionUploadFetcher.m:1540-1573`)، ويرفع ملف `putFile` كله في طلب واحد (`uploadChunkSizeBytes = Int64.max`، P:187).
  - Admin عبر GCS JSON: Node ينتظر `308` حقيقيًا مع `Range` بين القطع، وGo يرسل `X-GUploader-No-308: yes` وينتظر `200` مع `X-Http-Status-Code-Override: 308` ويفشل على 308 الحقيقي (B §5.5، §5.7).
- **المحاكي ليس مرجعًا للإزاحات ولا للأمان:** يتجاهل `X-Goog-Upload-Offset` كليًا (D §2.1، `raw_resumable.json`)، ويقبل أي JWT بلا توقيع (D §4.4)، ويقبل رموز تنزيل يزرعها العميل (`tokens.json`).
- **رابط التنزيل صلاحية حاملة (bearer):** يفتح الملف دون القواعد (E §5)، وتبنيه المكتبة من نطاقها الثابت، فيتسرّب إلى Google افتراضيًا من `<img>` و`dart:io` وجلسات الخلفية (P، قسم «رابط التنزيل»). قرر المالك: تبديل النطاق **وقت البناء** في كل منصة.
- **القرص المحلي خطر صامت:** `gocloud.dev/blob/fileblob` لا يستدعي fsync إطلاقًا، وفيه ثغرتا عبور مسارات في 2026 (#3667، #3794 = GHSA-hj6x-2356-cmv5)، و`IfNotExist` غير ذري (#3809) (F §1.2).
- **الخدمة من أصل اللوحة:** الترويسات العامة `Cross-Origin-Resource-Policy: same-origin` و`Content-Security-Policy: default-src 'none'` (`server.go:140-141`) تكسر `<img src=downloadURL>` من أي تطبيق، ورفع ملف HTML يُخدم بلا حماية يصير XSS على أصل اللوحة (F §2.2).
- **العزل** كما في ADR-017: `project_number` وفحص `scopecheck` واختبارات العزل. **والمشاريع لا تُحذف صفوفها أبدًا:** الحذف الفعلي عبر hooks تسجّلها الخدمات (`projects.go:378-381`، `:433-437`؛ RTDB في `app.go:163`).
- **خادم واحد** يملك الكتابة، كما في 0.2 (ADR-019:34). لكن ADR-015:105 يقول «old instances serve until replaced» (وكذلك `server.go:170-172`)، فنسختان تتداخلان عند الاستبدال، ولا شيء اليوم يمنع نسختين من مشاركة مجلد بيانات واحد (البند 5.1).

## القرار

### 1. الحزم والحدود

خدمة جديدة `internal/services/storage` بحزم `core` و`blob` و`wire` و`media` و`api` (واجهة `/v0`) و`gcs` (واجهة GCS) و`console` و`queries`. الشجرة والملفات وتقسيم العمل في [storage-layout.md](../research/storage-layout.md). القواعد الملزمة هنا:

- **`core` لا تعرف HTTP ولا صيغة سلك.** تستورد `blob` و`queries` و`platform/rules` و`tenancy`، ولا تستورد `api` ولا `gcs` ولا `console` ولا `media` ولا `platform/httpx`. القاعدة القائمة `internal/services/*/core` (`archtest_test.go:56`) تمنع `api` و`console` و`httpx` وحدها، فتُضاف إليها `internal/services/*/gcs` و`internal/services/*/media`. **و`api` و`gcs` و`console` و`media` لا يستورد أحدها الآخر** (`media` تحت الثلاث فقط)، بقواعد منع جديدة لكل منها، ولكل قاعدة حالة في `TestRulesThemselves`.
- **`blob` و`wire` تستوردان Go القياسية وحدها،** وتُضافان إلى archtest كما `rtdb/rules` و`rtdb/wire` (`archtest_test.go:31-33`). فلا يصل اسم كائن ولا ترويسة إلى كود القرص إلا عبر `core`.
- **`api` و`gcs` واجهتان فوق `core` نفسها،** ويتشاركان `media` (خدمة البايتات بأمان، البند 10). Storage لا يستورد أي خدمة أخرى (ADR-015).
- **فحص الإعداد في `internal/config`:** archtest يسمح له باستيراد `internal/platform` وحده (`archtest_test.go:23`)، فلا يستدعي `core.Limits.Validate`. فكما فعل RTDB والقواعد: `config.StorageLimits` بحقول `core.Limits` نفسها، والتحويل بينهما تحويل نوع يفرض المترجم تطابقه، والفحص (ومعه الفحوص المتقاطعة في البند 17) في `config` عند التحميل.
- **`sdk/go/adminstorage` وحدة Go مستقلة** (`go.mod` خاص)، ولها قاعدة archtest فارغة كـ `sdk/go/adminauth` (`archtest_test.go:47`).
- **القواعد من `platform/rules` (ADR-021).** و`internal/app` يعطي Storage `rules.NoDocuments{}` و`Inflight` العملية (rules-layout.md:109). فكل `firestore.get()` في قواعد Storage خطأ تقييم حتى v0.4.
- **المسارات في `internal/server`** (جدول ADR-008):

  | البادئة | المالك | لماذا |
  |---|---|---|
  | `/v0/` | `api` | الشكل الموحّد لرابط التنزيل ومسار المكتبات بعد تبديل النطاق (البند 16)، وما يطلبه `getDownloadURL` في Admin (P، الشرط 2) |
  | `/firebasestorage.googleapis.com/v0/` | `api` | ما ترسله مكتبات التحويل وقت التشغيل، احتياطًا (P، الشرط 2) |
  | `/storage/v1/`، `/upload/storage/v1/`، `/download/storage/v1/` | `gcs` | Admin SDKs بـ `apiEndpoint`/`WithEndpoint` (B §10.1، P:282) |
  | `/v1/projects/`، `/v1alpha/projects/` | `mgmt` | firebaserules وService Usage وdefaultBucket لأداة `firebase` (C §5.1) |

  - **لا نخدم مسار XML** `/<bucket>/<object>` (يتعارض مع اللوحة على `/`)، ولا صيغة المحاكي `/b/...` بلا `/storage/v1` (لا يصل إليها إلا عميل بلا مصادقة، البند 14).
  - **حد الجسم:** مسارات Storage لا تمر بحد 1 MiB العام (`httpx/json.go:16`، `middleware.go:50`). تُركَّب بـ `CommonWithLimit` بحد `storage.limits.object_bytes` مضافًا إليه 1 MiB، والمعالج يفرض الحد الدقيق لكل طلب (البند 6.4).

### 2. الـ bucket والمشروع

- **bucket افتراضي واحد لكل مشروع في v0.3،** اسمه `<projectId>.<project_domain>` كما يعطيه `toProject` اليوم (`projects.go:49`) وكما تحمله ملفات الإعداد المولّدة (`configgen.go:53`، `:113`، `:143`). فلا إنشاء ولا حذف buckets (ADR-023).
- **الحل من الاسم إلى المشروع** في `tenancy` (`ProjectForBucket`): يُفصل اللاحق `.<project_domain>`، ويُبحث عن المشروع بمعرّفه، ولا يُقبل إلا مشروع `ACTIVE`. أي اسم آخر، أو مشروع محذوف، يُرد عليه **404 بشكل الإنتاج** `{"error":{"code":404,"message":"Not Found."}}` على `/v0` (E §1، `production/bucket_probe.json`)، لا 403 كالمحاكي الذي ينشئ الـ bucket ضمنيًا (D §7.2).
- **Storage مفعّل للنسخة كلها** بـ `storage.enabled` كما `rtdb.enabled`. حين يكون معطّلًا لا تُركَّب المسارات، و`defaultBucket` يرد 404 برسالة الأداة (C §1.3).
- **تغيير `project_domain` يغيّر أسماء الـ buckets:** البيانات مفتاحها `project_number` فلا تضيع، لكن الروابط المخزّنة تنكسر. يُوثَّق في دليل التشغيل.
- **والقواعد لا تضيع معه:** إصدارة Storage تُحفظ في `control.rules_releases` باسم منطقي ثابت (`firebase.storage/default`)، لا باسم الـ bucket. وواجهة firebaserules تحوّل `firebase.storage/<bucket>` من وإلى هذا الاسم بـ `ProjectForBucket` واسم الـ bucket الحالي. **ففي الردود (`listReleases` و`getRelease` و`createRelease` و`updateRelease`) يظهر الاسم دائمًا `projects/{p}/releases/firebase.storage/<الـ bucket الحالي>`، لا `default`:** firebase-tools يطابق مخرج القائمة بـ `startsWith` على `firebase.storage/<bucket>` (`gcp/rules.ts:33-39`، و`rulesDeploy.ts:108`)، فلو أعدنا `default` لما وجد الإصدارة ولرفع ruleset جديدًا في كل نشر. فتغيير النطاق يغيّر الاسم المعروض وحده، ولا تعود كل الطلبات إلى القاعدة المقفلة الافتراضية حتى يعيد المالك النشر. وطلب بإصدارة على bucket ليس لمشروعها 404.
- **وجداول القواعد تُحذف مع المشروع:** `PurgeExpired` يحذف التطبيقات وحسابات الخدمة والمفاتيح وحدها (`projects.go:406-414`). فيضاف `control.rules_releases` ثم `control.rulesets` إلى معاملته نفسها في `tenancy` (لا hook، فـ `tenancy` يملكهما).

### 3. الجداول والعزل والحذف

مخطط جديد `storage` (`internal/store/migrations/storage/00001_init.sql`، وسطره في `migrations.Schemas` و`sqlc.yaml` و`released.lock`، ADR-015). **كل جدول يخص مشروعًا** فيه `project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE` (ADR-017):

| الجدول | المفتاح | ما فيه |
|---|---|---|
| `storage.objects` | `(project_number, name)` | `name text COLLATE "C"`، و`generation bigint` (ميكروثانية، البند 12)، و`metageneration bigint`، و`blob_key text UNIQUE`، و`size`، و`md5 bytea`، و`crc32c bigint`، وحقول المحتوى الخمسة (`content_type`، `content_encoding`، `content_disposition`، `content_language`، `cache_control`، كلها `text` تقبل NULL)، و`metadata jsonb` (نص إلى نص)، و`download_tokens bytea` (**مختومة بـ KEK**، البند 9)، و`time_created`، و`updated` |
| `storage.upload_sessions` | `id_hash bytea` | المشروع، والاسم، والبروتوكول (`firebase`/`gcs`)، والحالة (`active`/`final`/`cancelled`)، والهوية المربوطة (البند 7)، والحجم المعلن (NULL إن لم يُعلن)، والـ metadata الواردة بعد التنقية، و`received`، و`staging_key`، و`blob_key` (يُملأ قبل نقل الملف، البند 5)، والشروط المسبقة وchecksums العميل (GCS)، ونتيجة الإنهاء (`final_status` و`final_body` و`result_generation`) لتكرار الإنهاء و`query`، و`reserved_bytes`، و`created_at`، و`progressed_at` (آخر بايت متين، للانتهاء الخامل، البند 6.5)، و`expires_at` |
| `storage.blob_gc` | `blob_key` | outbox الحذف: كل ملف لم يعد صف يشير إليه، يُدرج في معاملة الكتابة نفسها (F §1.3) |
| `storage.usage` | `project_number` | `used_bytes` و`reserved_bytes` و`object_count`، للحصة (البند 6.5) |
| `storage.buckets` | `project_number` | إعداد الـ bucket القابل للتعديل: `cors jsonb NOT NULL DEFAULT '[]'` (البند 11). صف واحد يُنشأ عند أول تعديل |
| `storage.instance` | `id uuid` | **صف واحد، لا يخص مشروعًا:** معرّف النسخة الذي يربط القاعدة بمجلد البيانات (البند 5.1). استعلاماته `-- scope: global` بسببها |

- **صف `storage.usage` يُنشأ بـ upsert** (`INSERT … ON CONFLICT (project_number) DO UPDATE`) في أول حجز، لا افتراضًا بوجوده: `UPDATE … WHERE` على صف غائب لا يطابق شيئًا، فيُرفض كل رفع.

- **الأسماء نص معتم:** `text COLLATE "C"` بلا تطبيع NFC ولا طي حالة، فالترتيب بالبايتات كترتيب GCS في القوائم، كما فعلنا في RTDB (F §4.1، `migrations/rtdb/00001_init.sql:26-31`).
- **`scopecheck`:** كل الجداول في قائمة `TestQueriesAreProjectScoped` في `internal/services/storage/queries/scope_test.go`. واستعلامات عامل GC والمسح عند الإقلاع والانتهاء الدوري للجلسات تعمل عبر المشاريع، فتُعلَّم `-- scope: global` بسببها المكتوب («عامل GC يعمل على مفاتيح ملفات، لا على بيانات مستخدمين»).
- **اختبارات العزل** في `test/acceptance/isolation_test.go` (القائمة في الهيكل): رابط رفع مشروع لا يُكمل رفعًا في مشروع آخر، ورمز تنزيل كائن لا يفتح كائنًا باسمه نفسه في مشروع آخر، وحساب خدمة مشروع لا يقرأ bucket غيره على GCS ولا يعيد كتابة (rewrite) إليه، وصفحة قائمة (`pageToken`) لا تُستعمل على bucket آخر، وruleset مشروع لا يُطلق لـ bucket غيره.
- **الحذف بـ hook لا بـ CASCADE:** صفوف المشاريع لا تُحذف (`projects.go:378-381`)، فيسجّل `internal/app` الدالة `storageSvc.PurgeProject` بـ `OnProjectPurged`، كما يفعل RTDB (`app.go:163`). الدالة:
  1. تحذف ملفات staging لجلسات المشروع (`s/<project_number>/`).
  2. تحذف كل الملفات تحت `p/<project_number>/`.
  3. تحذف صفوف المشروع من الجداول الخمسة.
  - **idempotent وقابلة للاستئناف:** `PurgeExpired` يعيدها في كل تشغيل حتى تنجح (`projects.go:382-383`)، والحذف بالبادئة يكمل من حيث توقف. والبادئة آمنة لأن `project_number` لا يُعاد استعماله.
  - المشروع المنتهي لا يقبل طلب Storage أصلًا (البند 2)، فلا رفع جديد يسابق الحذف. يُختبر ذلك.

### 4. تخزين البايتات: سائق قرص صغير خاص بنا

- **`blob.Store` واجهة صغيرة، وتنفيذها الوحيد في v0.3 سائق قرص محلي نكتبه** (نحو 300 سطر، Go القياسية وحدها):
  - `Create(key)` يفتح الملف بـ `O_CREATE|O_EXCL`، فلا كتابة فوق ملف قائم أبدًا.
  - `Commit()` يستدعي `f.Sync()` ثم `Sync` لمجلد الملف، ثم يغلق. الثاني ضروري: «Calling fsync() does not necessarily ensure that the entry in the directory containing the file has also reached disk» (fsync(2)، F §1.3).
  - `Open(key)` يعيد `io.ReadSeeker` بحجمه، و`Delete(key)` idempotent، و`Walk(prefix)` للمسح.
- **مفتاح الملف لا يُشتق من اسم الكائن أبدًا:** `p/<project_number>/<uuid>`، UUID جديد **لكل generation**. وstaging تحت `s/<project_number>/<uuid>`.
  - السائق يرفض أي مفتاح لا يطابق `^(p|s)/[0-9]+/[0-9a-f-]{36}$`. فعبور المسارات مستحيل بالبناء، لا بالهروب النصي الذي أخطأ فيه fileblob مرتين (#3667، #3794).
  - ولا تصادم بحالة الأحرف (APFS) ولا بمحارف ممنوعة (NTFS)، ولا يُكتب فوق ملف يقرؤه طلب جارٍ (F §4.1، S1).
  - على القرص يتفرع المجلد بأول حرفين من UUID (`p/<n>/ab/<uuid>`) حتى لا يكبر مجلد واحد. المفتاح المنطقي لا يتغير.
  - **كل مجلد جديد متين في أبيه:** نص fsync(2) أعلاه ينطبق على مدخل المجلد الجديد في أبيه أيضًا، وإلا قد يختفي مجلد شظية كاملًا بعد انقطاع الكهرباء والصفوف تشير إليه. فدالة واحدة `mkdirDurable` تنشئ الناقص من السلسلة وتستدعي fsync لأب كل مجلد أنشأته، حتى الجذر. وتمر بها `Create` و`Staging` و`Promote` في `p/` و`s/` معًا.
- **S3 ليس في v0.3** (ADR-023). الواجهة مصممة له: staging محلي دائمًا (قطع العملاء من 256 KiB أصغر من حد S3 الأدنى للجزء، 5 MiB، F §3.2)، و`Put` من ملف، و`Delete`، و`Open` بـ Seek. ومعه تأتي صفوف النية (البند 5).

### 5. ترتيب الكتابة وسلامة الانهيار

**الترتيب لكل كتابة تجعل ملفًا مرئيًا** (رفع، أو rewrite):

1. مفتاح جديد `p/<n>/<uuid>`. لا يشير إليه أي صف بعد.
2. الكتابة مباشرة إلى المفتاح النهائي، ثم `Commit()` (fsync للملف وللمجلد). أو نقل ملف staging إليه بـ `rename` على القرص نفسه، ثم fsync للمجلدين.
3. معاملة واحدة (`commitObject`): صف الكائن (generation جديد)، وللملف القديم إن وُجد صف في `storage.blob_gc`، وتحويل الحصة من محجوز إلى مستعمل. **والـ `COMMIT` نفسه يُرسل تحت `context.WithoutCancel`**: عميل يقطع الاتصال عند الإنهاء يلغي سياق الطلب، ولا يجوز أن يلغي `COMMIT` جاريًا. و`xid` المعاملة (`pg_current_xact_id()`) يُقرأ قبل `COMMIT`.
4. بعد الـ commit: عامل GC يحذف ملفات `blob_gc` ثم صفوفها. الحذف idempotent.
5. **مسار الخطأ يميّز ما قبل `COMMIT` مما بعده:**
   - **خطأ قبل إرسال `COMMIT`** (القواعد، والـ checksums، والشروط المسبقة، والقيود): يُحذف الملف الجديد متزامنًا قبل الرد، ويُعدّ الفشل إن لم ينجح الحذف.
   - **خطأ أثناء `COMMIT` أو بعده** (انقطاع الاتصال، أو مهلة): النتيجة **ملتبسة**، فقد التزمت المعاملة. لا يُحذف شيء قبل أن تُحسم بـ `pg_xact_status(xid)` على اتصال جديد: `committed` = نجاح (الرد 200)، و`aborted` = يُحذف الملف الجديد، وغير ذلك (لا اتصال) = يُترك الملف ويرد الطلب 503، والإنهاء التالي يحسمه (صف الجلسة يحمل `blob_key`)، والمسح عند الإقلاع يحذفه إن لم يشر إليه صف. وثائق Postgres: `pg_xact_status` وُجد «to determine whether their transaction committed or aborted after the application and database server become disconnected while a COMMIT is in progress».
   - **لماذا:** المعاملة التي التزمت تشير إلى الملف الجديد، ووضعت القديم في `blob_gc`. فحذف الجديد بعد التزام ملتبس يضيّع النسختين، والعميل قيل له إن الرفع فشل (مراجعة المعمارية).

- **`synchronous_commit=on` لكل اتصال، و`fsync` يبقى مفعّلًا:** الترتيب كله يفترض أن الـ commit متين عند عودته. ومع `off` «there can be a delay between when success is reported to the client and when the transaction is later guaranteed to be safe» (F §1.3). فيُضبط `synchronous_commit` لكل معاملة من معاملات Storage: `start`، وتحديث `received`، وكتابة `blob_key`، و`commitObject`، والحذف، وGC.
  - **تعديل (مراجعة S2-0، 2026-10-24):** كان القرار `synchronous_commit=on` في `RuntimeParams` لمجمّع الاتصالات (`store.Open`). أُسقط: PgBouncer بإعداده الافتراضي يرفض معامل بدء لا يعرفه، والإعداد كان يشمل كل الخدمات لا Storage وحده. فكل معاملة Storage تبدأ بـ `SET LOCAL synchronous_commit TO on` عبر مساعد واحد في `core` (`begin` و`inTx`، `durability.go`) تمر به مهام S2-C وD وE كلها. أما `fsync` فسياقه `sighup` ولا يُضبط لكل جلسة، وإطفاؤه «can result in unrecoverable data corruption»: الإقلاع يقرأ `SHOW fsync` ويحذّر في السجل إن كان `off`، ودليل التشغيل يشترطه. فلا ندّعي أننا مستقلون عن إعداد الخادم.
- **الحذف دائمًا بعد الـ commit،** فلا يرى قارئ صفًا ملفه محذوف. والقارئ الذي فتح ملف generation قديم يكمل عليه ولو حُذف، لأن حذف ملف مفتوح لا يمس بياناته على POSIX (unlink). وعامل GC ينتظر مهلة (`storage.gc_grace`، دقيقة افتراضيًا) تغطي ما بين قراءة الصف وفتح الملف. **وStorage في v0.3 يعمل على darwin وLinux وحدهما** (تعديل، مراجعة S2-A، 2026-10-24): الترتيب كله يفترض دلالات POSIX (unlink لملف مفتوح، وfsync للمجلدات، وflock)، فعلى أي نظام آخر يرفض `blob.NewDisk` الإقلاع برسالة صريحة (`ErrUnsupported`)، لا خطأً لاحقًا في منتصف العمل. وكان النص «على Windows يفشل حذف الملف المفتوح، فيعيده العامل لاحقًا»، وأُسقط.
- **الإنهاء قابل للاستئناف بعد انهيار:** قبل نقل staging يُكتب `blob_key` في صف الجلسة (معاملة صغيرة). فإن انهار الخادم بين النقل والـ commit، أو فشل `Promote` بعد `rename`، **يستدعي الإنهاء التالي `Promote` مرة أخرى قبل أي commit**: إن غاب ملف staging ووُجد ملف على `blob_key` (أو بقي الاسمان على الملف نفسه بعد انقطاع الكهرباء، فيُحذف اسم staging)، فالنقل حدث ومدخله قد لا يكون متينًا، فيزامن `Promote` الملف ومجلد المفتاح وما فوقه ومجلد staging، **ثم يعيد `blob.ErrPromotedEarlier` لا نجاحًا**: طبقة البايتات لا تعرف الهاش، فلا تعرف أن الملف ملف الجلسة. فيحسب الإنهاء crc32c وmd5 والحجم للملف على `blob_key` ويقارنها بما سجلته الجلسة، ولا يكمل الـ commit إلا إن طابقت (مراجعة S2-A، الجولة الثالثة). وملف مختلف على المفتاح وملف staging موجود: `ErrExist`، ولا يُمس أيهما. أما الثقة بملف موجود على `blob_key` بلا هذه المزامنة فتترك صفًا يشير إلى لا شيء بعد انقطاع الكهرباء، والمسح يحذف البايتات الباقية باسم staging (مراجعة S2-A). وإن وجد صف كائن يشير إلى `blob_key` نفسه (التزام ملتبس سبق)، فالنتيجة نجاح تُحفظ في الجلسة.
- **`commitObject` يقرأ الصف المستبدَل من الصف المقفل:** `INSERT … ON CONFLICT DO NOTHING RETURNING *`، وعند التعارض، في المعاملة نفسها، `SELECT … FOR UPDATE` على `(project_number, name)`، ثم فحص الشروط المسبقة عليه، ثم `UPDATE … SET generation = GREATEST($now_us, generation + 1) … RETURNING *`. ومن الصف المقفل مفتاح `blob_gc` والحجم القديم للحصة (`releaseObject`). تحت READ COMMITTED ينتظر `FOR UPDATE` كاتبًا متزامنًا ثم يقرأ نسخته الملتزمة، فيذهب إلى `blob_gc` المفتاح الذي استُبدل فعلًا، مرة واحدة (`TestCommitStatementsUnderConcurrentOverwrites`). وقراءة بلا قفل ثم upsert كانت تقرأ المفتاح القديم نفسه في الكتابتين (قيس في المراجعة).
  - **تعديل (مراجعة S2-0، 2026-10-24):** كان القرار `ON CONFLICT DO UPDATE … RETURNING old.blob_key, old.size` (Postgres 18). أُسقط: sqlc 1.31.1 لا يولّد `RETURNING old.*` (أُعيد إنتاجه). والجمل في `queries/commit.sql`.
  - **`ifGenerationMatch=0`:** فرع `DO NOTHING` وحده، وصفر صفوف = 412. فإنشاءان متزامنان لا ينجحان معًا: الثاني ينتظر الأول ثم لا يفعل شيئًا (`TestCommitStatementsCreateRace`).
  - **بقية الشروط المسبقة:** تُفحص على الصف المقفل نفسه، ثم الكتابة.

#### 5.1 مالك واحد لمجلد البيانات

دليل المسح عند الإقلاع («أقدم مني وبلا صف = يتيم») صحيح فقط إن لم يكتب غيرنا في المجلد. فيُفرض ذلك، لا يُفترض:

- **`storage.data_dir` إلزامي حين Storage مفعّل،** مسار مطلق على نظام ملفات محلي، يُنشأ إن غاب، ويُرفض إن لم يُكتب فيه. وNFS غير مدعوم (القفل أدناه لا يُعتمد عليه هناك): **`NewDisk` يرفضه** ومعه SMB وAFP وWebDAV (من `statfs`: `f_type` على Linux و`f_fstypename` على darwin)، برسالة تسمي نظام الملفات، ويُوثَّق.
- **قفل حصري:** `flock(LOCK_EX)` على `<data_dir>/LOCK` قبل أي عمل على المجلد، يُمسك طوال عمر العملية.
- **ربط المجلد بالقاعدة:** `<data_dir>/INSTANCE` فيه UUID، وصف `storage.instance` فيه الـ UUID نفسه. **عند أول إقلاع يُكتب الملف متينًا أولًا** (fsync للملف وللمجلد)، **ثم الصف.** فإن انهار الإقلاع بينهما، أو وُجد أحدهما وحده: **يُتبنّى** الموجود ويُكتب الآخر بقيمته **حين `p/` و`s/` فارغان** (لا بيانات يمكن أن تُحذف خطأً). وإلا يُرفض الإقلاع برسالة واضحة تسمي الملف والصف وما يُستعاد. وأي عدم تطابق (مجلد لقاعدة أخرى، أو قاعدة لمجلد آخر، أو أحدهما بلا الآخر) **يرفض الإقلاع** برسالة تقول ما يُستعاد. ففي نسختين على قاعدتين تتشاركان مجلدًا (`p/1/…` موجود في الاثنتين لأن أرقام المشاريع من تسلسل كل قاعدة) لا تحذف الثانية ملفات الأولى.
- **الاستبدال (قرار):** نسخة جديدة وStorage مفعّل **تنتظر القفل قبل أن تفتح أي مستمع**، وتكتب في السجل كل 10 ثوانٍ أنها تنتظر. فلا تراها موازنة الحمل جاهزة (لا تجيب أصلًا) حتى تخرج القديمة وتحرر القفل عند إغلاقها المنظم. أي أن نشر نسخة فيها Storage **إيقاف ثم تشغيل**، وانقطاعه بقدر إغلاق القديمة وإقلاع الجديدة. وما يقوله ADR-015 عن نسخ قديمة تخدم حتى تُستبدل يبقى صحيحًا للنسخ التي لا تشغّل Storage. وهذا تطبيق لـ ADR-019:34 (كاتب واحد)، لا استثناء منه. وتعدد الخوادم ADR جديد (البند 4).
- **دليل التشغيل:** `RollingUpdate` في Kubernetes يعلق على القفل (الجديدة تنتظر، والقديمة لا تُزال حتى تجهز الجديدة)، فالنسخ التي تشغّل Storage تحتاج `strategy: Recreate`. وكذلك أمثلة compose وsystemd: إيقاف القديمة ثم تشغيل الجديدة، لا تشغيلهما معًا.
- **قائمة المرشحين تُؤخذ قبل فتح المستمع:** بعد القفل يمشي الإقلاع على `p/` و`s/` ويحفظ المفاتيح، ثم يفتح المستمع. وكل مفتاح في القائمة سابق لأي كتابة في هذه العملية، والقفل يمنع غيرها، فلا يُعتمد على `mtime` أصلًا (قيس في المراجعة: `rename` يُبقي `mtime` ملف staging، فمفتاح نُقل بعد الإقلاع قد يبدو أقدم منه). **والتحقق من الإشارات بجملة واحدة لكل دفعة:** مفتاح بلا صف كائن (`objects.blob_key`) ولا جلسة (`staging_key` أو `blob_key`) يُحذف، في الخلفية بعد فتح المستمع.
- **وقبل فتح المستمع أيضًا تُصالح الحصة** (البند 6.5).

#### 5.2 المسح عند الإقلاع، لا صفوف نية، في v0.3

- **القرار:** كاتب واحد مفروض بالقفل (5.1)، فكل ملف تحت `p/` أو `s/` **موجود قبل أن تكتب العملية شيئًا** ولا يشير إليه صف كائن ولا `staging_key` ولا `blob_key` لجلسة حية هو يتيم بيقين: لا كاتب آخر في منتصف كتابته. فالمسح عند الإقلاع **يحذفه**، بقائمة المرشحين التي أُخذت قبل فتح المستمع (5.1)، ولا يمس غيرها (F §1.3).
- **المسح الدوري يبلّغ ولا يحذف** (مقياس وسجل): ملف بلا صف أحدث من الإقلاع، أو صف بلا ملف. حالاته غير مؤكدة، ودرس `rgw-orphan-list` (Ceph) و`volume.fsck` (SeaweedFS) أن أداة الحذف بلا يقين تحذف بيانات حية (F §1.1).
- **لماذا لا صفوف نية الآن:** تضيف معاملة قبل كل كتابة وعاملًا ثالثًا، ولا تشتري شيئًا على خادم واحد بقرص محلي. تلزم حين تتحقق إحدى ثلاث (F §1.3): أكثر من عملية تكتب، أو S3 تصير قائمته الكاملة عند الإقلاع مكلفة، أو تسرّب متراكم بين الإقلاعات. **S3 وتعدد الخوادم يأتيان معها بـ ADR** (المطالبة بـ `DELETE ... RETURNING`، والمنظّف يلتزم قبل حذف الملف، F §1.3).

### 6. الرفع: البروتوكولان على آلة حالات واحدة

`core` تعرف «جلسة رفع» واحدة: بدء، وقطع بإزاحة، واستعلام، وإلغاء، وإنهاء. و`api` و`gcs` تترجمان بروتوكوليهما إليها.

#### 6.1 بروتوكول العملاء (`/v0`، `X-Goog-Upload-*`)

- **النوع:** `X-Goog-Upload-Protocol: multipart|resumable`، أو وجود `X-Goog-Upload-Command`. معامل `uploadType` يُتجاهل (JS لا يرسله، A §1.5)، وكذلك ترتيب المعاملات (A §6.7). وبلا الاثنين: رفع `media` خام بـ `?name=` (CT `should default to media upload`، E §3.1).
- **`start` على `/o` وعلى `/o/{object}`** بـ `POST` و`PUT` (iOS يبدأ على مسار الكائن، A §1.2). الاسم من `?name=` ثم من المسار (A §6.8).
- **الحجم المعلن اختياري:** `X-Goog-Upload-Header-Content-Length` (الويب) أو `X-Goog-Upload-Content-Length` (iOS)، أو لا شيء (Android). والنوع من `contentType` في الجسم، ثم `X-Goog-Upload-Header-Content-Type` أو `X-Goog-Upload-Content-Type`، ثم `application/octet-stream` (A §7).
- **رد `start`:** 200، وجسم فارغ (iOS يشترطه، A §3.10)، و`X-Goog-Upload-Status: active`، و`X-Goog-Upload-URL` **مطلق من `public_url`** لا من ترويسة `Host` (D §7.2: المحاكي يبنيه من `Host`، فيخطئ خلف وكيل). شكله `https://<aruma>/v0/b/<bucket>/o?name=<enc>&upload_id=<id>&upload_protocol=resumable`. **ولا `X-Goog-Upload-Chunk-Granularity`** (iOS يقرّب به الإزاحة إلى أسفل، A §6.5).
  - المطلق شرط لا تحسين: قطع `putFile` في جلسة الخلفية على iOS لا تمر بأي URLProtocol، فتذهب حيث يقول الرابط (P، قسم iOS، «تجربتا الخلفية»).
- **طلب واحد على الجلسة في كل لحظة:** قفل في الذاكرة لكل جلسة (خريطة بعدّاد مراجع)، يُمسك عبر الإلحاق والـ `Sync` وتحديث `received`، ويأخذه الإنهاء والإلغاء والانتهاء أيضًا. وبعد أخذه تُقرأ حالة الجلسة من جديد. وملف staging يُنشأ عند `start` وحده، فطلب على جلسة ملغاة لا يعيد إنشاءه. السبب: Go يعيد الجزء كاملًا بالإزاحة نفسها (6.2)، وخلف وكيل يخزّن الطلب قد يتداخل الأصل وإعادته. و`Promote` بـ `rename` يُبقي الـ inode (قيس في المراجعة)، فقطعة متأخرة بمقبض مفتوح كانت ستلحق بايتاتها بملف الكائن خارج حجمه وهاشه وحصته. والقفل في الذاكرة يكفي لأن العملية واحدة بالقفل (5.1).
- **القطع:** `POST` أو `PUT` على رابط الجلسة. `X-Goog-Upload-Command` قائمة بفواصل ومسافات (`upload, finalize`)، و`finalize` منفردًا بجسم فارغ مقبول (Android، A §3.11). الجسم خام مهما كان `Content-Type`.
- **الإزاحة (لا نقلّد المحاكي):**
  - `offset == received`: إلحاق.
  - `offset < received`: البايتات المحفوظة لا يُكتب فوقها (دلالة GCS: «Cloud Storage ignores attempts to do so»، F §3.1)، فيُتجاهل التداخل ويُلحق الباقي.
  - `offset > received` (فجوة): **400** مع `X-Goog-Upload-Status: active` و`X-Goog-Upload-Size-Received` الحالي، حتى يرسل العميل `query` (E §3.2).
- **الردود:** القطعة الوسيطة **200** (لا 308 أبدًا، فهو فشل في المكتبات الثلاث، A §3.11) و`active` و`X-Goog-Upload-Size-Received` وجسم فارغ. والإنهاء 200 و`final` وmetadata JSON كاملة.
- **كل رفض نهائي للقطعة أو للإنهاء يجعل الجلسة `final`:** رفض القواعد (403)، والحصة (402)، والحجم الذي لا يطابق المعلن (400)، و`md5Hash` المختلف (400)، وتجاوز حد الطلب أو الكائن (413)، والهوية التي أُبطلت (401، البند 7). يُرد بالحالة ومعها `X-Goog-Upload-Status: final`، وتُحفظ الحالة وجسم الخطأ في صف الجلسة (`final_status` و`final_body`).
  - **السبب:** Android لا يوقف المهمة إلا على `final`، وأي رد آخر غير 2xx يرسل `query` ويعيد القطعة نحو دقيقة (`UploadTask.java:303-364`، و`maxChunkUploadRetry` 60 ثانية في `FirebaseStorage.java:56`). وiOS يعيد على 4xx غير 404 مع `active` حتى `maxUploadRetryInterval` (600 ثانية، `GTMSessionUploadFetcher.m:1667-1686`، `Storage.swift:298`)، والقطعة في `putFile` الملف كله. أما JS فيفشل فورًا على 400/402/403 (`task.ts:120-142`).
  - **`active` لحالتين فقط:** فجوة الإزاحة (400، أعلاه)، و503 (ضغط مؤقت تعيده المكتبات الثلاث).
- **الإنهاء المكرر و`query` على جلسة `final` يعيدان النتيجة المحفوظة نفسها:** بعد نجاح، 200 مع الـ metadata (لا جسم `OK` كالمحاكي، لأن iOS يقرأ الجسم، E §3.1)؛ وبعد رفض، الحالة وجسم الخطأ المحفوظان مع `final`. و`query` على جلسة `active` 200 مع الحالة والحجم دائمًا (GTM يفشل بدون `Size-Received`، `:1229-1236`). أما `query` على جلسة انتهت برفض فلا يرد 200: GTM يعدّ `final` في رد `query` اكتمالًا ويمرر جسمه للنجاح (`:1232-1242`)، فيفشل iOS في فك جسم فارغ برسالة خاطئة (`StorageUploadTask.swift:186-197`)، ويقرأ Android رمز 200 فيعطي `ERROR_UNKNOWN` (`UploadTask.java:392-395`). فإن ضاع رد الإنهاء المرفوض على الشبكة، يصل العميلَ رمزُ الرفض الصحيح.
- **`cancel`:** 200، ومرتين 200، وبعد `final` 400، ولجلسة مجهولة 404 (CT، E §3.1).
- **الإنهاء بحجم لا يطابق المعلن، حين أُعلن:** 400 مع `final`. وبلا إعلان، الحجم ما وصل (E §3.2).
- **`md5Hash` من العميل يُتحقق منه على `/v0` أيضًا:** هو حقل قابل للكتابة في JS («A Base64-encoded MD5 hash of the object being uploaded»، `public-types.ts:169-174`، ويرسله `toResourceString`، `metadata.ts:103`)، وiOS يضعه في جسم `start` متى ضُبط (`StorageMetadata.swift:117`). فإن اختلف عن المحسوب: **400 ولا كائن**، ومع `final` في الرفع المتقطع، كالبند 6.2. **مؤقت** (رد الإنتاج غير مقيس). و`crc32c` وبقية الحقول للقراءة فقط تُتجاهل (A:538)، ولا 409 أبدًا (البند 12).
- **multipart:** `multipart/related` يُقرأ تدفقًا بـ `mime/multipart.Reader`، جزء JSON بسقف 64 KiB ثم البايتات إلى القرص مباشرة، ويُقبل غياب CRLF بعد الحد الختامي (A §3.9، §6.6). لا `ParseMultipartForm` (F §3.5).
- **الترويسات بإملاء Google** (`X-Goog-Upload-URL` لا `X-Goog-Upload-Url`) بإسناد مباشر إلى الخريطة (A §6.2). والمكتبات المقيسة لا تفرّق (P، الشرط 9)، فهذا احتياط.

#### 6.2 بروتوكول GCS (Admin)

- **التوجيه:** وجود `upload_id` يعني عملية على جلسة قائمة **أيًّا كانت الطريقة وأيًّا كان `uploadType`**، لأن Go يرسل قطعه `POST` إلى رابط فيه `uploadType=resumable` (B §5.3، الفخ الذي أسقط المحاكي، B §9-B2).
- **`uploadType=multipart`** (Node بـ `resumable:false`، وGo تحت 16 MiB) و`media`، و`resumable` يبدأ جلسة ويرد 200 مع `Location` مطلق من `public_url`. **الـ metadata من جسم JSON في طلب البدء، مدموجًا مع `X-Upload-Content-Type`** (Node ينقل `contentType` إلى الترويسة ويرسل الجسم غالبًا `{}`، وGo يرسلها في الترويسة أيضًا، B §5.3، B:198-200، B:465). **والعميل هو من يتجاهل جسم الرد**، فنرده فارغًا.
- **`Content-Range`** بأشكاله الخمسة (B §10.2-5): `bytes a-b/*` وسيط، و`bytes a-b/T` و`bytes */T` نهاية (الأخيرة جزء فارغ)، و`bytes N-*/*` و`bytes N-*/T` «بقية الكائن في هذا الطلب» ويُغلق عند نهاية الجسم ولو كان الحجم صفرًا (Node `save()`، B §5.4)، و`bytes */*` بلا جسم استعلام.
- **الرد غير النهائي:**
  - مع `X-GUploader-No-308: yes` (Go): **200 و`X-Http-Status-Code-Override: 308`** و`Range`، بلا جسم JSON (B §5.7).
  - وإلا (Node): **308 مع `Range: bytes=0-<last>`**، بلا `Location` (fetch يتبعه، B §5.5). وبلا `Range` حين لا بايت محفوظًا.
- **سلامة الجزء:** ذري لـ Go (يتجاهل `Range` ويفترض قبول الجزء كله، B §5.7)، وتكراره بالإزاحة نفسها مقبول بلا أثر، والفجوة 400. ولـ Node في وضع الطلب الواحد تُحفظ البايتات تدريجيًا ويُبلَّغ عنها بدقة (B §5.4).
- **checksums:** الخادم يحسب `crc32c` (Castagnoli، `hash/crc32`) و`md5` أثناء التدفق، ويعيدهما في كل رد رفع: Node **يحذف الكائن** إن غاب `crc32c` أو اختلف (B §5.8). وإن أرسل العميل `crc32c` أو `md5Hash` في JSON أو في `X-Goog-Hash` واختلف: **400 ولا كائن** (B §5.8، `writer.go:45-47`).
- **الشروط المسبقة:** `ifGenerationMatch` (والصفر يعني «يجب ألا يوجد») و`ifGenerationNotMatch` و`ifMetagenerationMatch` و`ifMetagenerationNotMatch`، تُفحص داخل معاملة الإنهاء، والفشل **412** `conditionNotMet` (B §10.2-8؛ المحاكي يتجاهلها، E §8).

#### 6.3 staging

- **كل جلسة لها ملف staging محلي** `s/<n>/<uuid>`، للواجهتين، **ومع أي سائق بايتات لاحق** (F §3.2).
- **الحقيقة على القرص، والصف هو ما أُعلن للعميل:** طلب `upload` يلحق ما وصل، ثم `f.Sync()`، ثم يحدّث `received` في الصف. بعد انهيار بين الاثنين يكون طول الملف ≥ `received`، فيُقص إلى `received` عند الطلب التالي. فلا يُبلَّغ العميل أبدًا بإزاحة غير متينة (F §3.3).
- **الطلب المقطوع في منتصفه:** يُحفظ ما وصل (العميل سيسأل بـ `query`)، ولا يُعلن إلا ما فُرِّغ إلى القرص. وGo يعيد الجزء كاملًا بالإزاحة نفسها، فيُتجاهل المحفوظ منه (6.1).
- **الهاش بلا إعادة قراءة:** حالة `crc32c` و`md5` تُحفظ مع الإزاحة (`encoding.BinaryMarshaler` في هاشات المكتبة القياسية، F §3.3). وإن قُص الملف بعد انهيار، يُعاد حساب الهاش من الملف مرة.
- **العمر:** 7 أيام من البدء، قابل للضبط (`storage.limits.upload_session_ttl`). Android يعرض رابط الجلسة للتطبيق «valid for approximately one week» (`UploadTask.java:609-617`، A §3.13)، وGCS «active for up to a week» (F §3.1). والمنظّف يحذف الملف والصف معًا ويحرر الحصة.

#### 6.4 حدود كل طلب

- **لا حد ثابت للقطعة:** iOS يرسل ملف `putFile` كله في طلب واحد (P:187)، وAndroid حتى 32 MiB، والويب حتى 16 MiB (F §3.2). فحد الطلب = `min(object_bytes − received، المعلن − received إن أُعلن)` بـ `http.MaxBytesReader`، والجسم يتدفق إلى القرص بمخزن 32 KiB، ولا يُحمل في الذاكرة أبدًا.
- **المهلات متدرجة لا صفرية:** الخادم يبقي `ReadTimeout`/`WriteTimeout` 60 ثانية (`server.go:198-199`). معالج الرفع لا يصفّرها كما في WebSocket (`rtdb/api/ws.go:45-46`)، بل يمدّ **مهلتي القراءة والكتابة معًا** بعد كل قراءة ناجحة إلى الآن + 30 ثانية عبر `http.ResponseController`، **ويضبط مهلة الكتابة مرة أخرى قبل كتابة الرد مباشرة**، ويقطع الطلب إن نزل المعدل عن `storage.limits.min_bytes_per_second` (4 KiB/s) على نافذة 30 ثانية. فيكمل رفع بطيء متقدم، ويُقطع slowloris (F §3.5). ومد القراءة وحدها لا يكفي: قيس في المراجعة أن `WriteTimeout` ينتهي فيضيع رد طلب أطول من 60 ثانية بعد أن التزم الخادم (`extendWrite=false: EOF`، و`true: 200`)، فيرى العميل خطأً لكائن محفوظ.

#### 6.5 الحصص: محجوزة لكل قطعة

- **حصة بايتات لكل مشروع** (`storage.limits.project_bytes`، 5 GiB افتراضيًا، 0 بلا حد)، **تشمل staging**، وتُحجز **قبل كتابة كل قطعة**:
  `UPDATE storage.usage SET reserved_bytes = reserved_bytes + $n WHERE project_number = $1 AND used_bytes + reserved_bytes + $n <= $quota`. صفر صفوف = رفض القطعة قبل قراءة جسمها. و`$n` هو `Content-Length` إن عُرف، وإلا يُحجز تدفقًا بخطوات 8 MiB ويُرد الفائض عند انتهاء الطلب (Node يرسل chunked بلا طول، B §5.4). **وكل الحجوزات في القاعدة، `Insert` منها:** `Insert` يحجز في `storage.usage` كالقطعة، ويرده أو يحوّله في معاملته، ومصالحة الإقلاع (أدناه) تصفّر ما بقي بعد انهيار. فلا دفتر في الذاكرة يخالف القاعدة.
- **`start` لا يحجز شيئًا (قرار المسؤول):** يفحص فقط أن المعلن يتسع لما بقي من الحصة (`quota − used − reserved`)، وإلا 402 رفضًا مبكرًا. والحجز لكل قطعة، من البايتات التي تصل فعلًا. فلو حُجز المعلن كله لحجز طلب `start` واحد بلا هوية (`X-Goog-Upload-Header-Content-Length: 5368709120`) حصة المشروع كلها سبعة أيام، حتى تحت القواعد المقفلة، واسم الـ bucket عام في إعداد التطبيق. وحتى حجز 8 MiB عند كل `start` كان يكفي، مع 100 جلسة لكل IP، لاستنزاف 5 GiB من نحو سبعة عناوين، يتجدد كل ساعة (المراجعة الثانية). وبلا إعلان (Android) لا شيء يُفحص عند `start` (F §4.3).
- **جلسة فارغة لا تعيش:** جلسة `received = 0` تنتهي بعد `storage.limits.empty_session_ttl` (10 دقائق)، **ولا تُحسب في حدود الجلسات** لكل IP ولكل مشروع، فلا تملأ الحدودَ جلساتٌ لم ترسل بايتًا. وجلسة لم يتقدم فيها بايت متين منذ `upload_idle_ttl` (ساعة) تُحرَّر حجوزاتها غير المستعملة (`reserved − received`)، والبايتات المحفوظة تبقى محسوبة ما دامت الجلسة حية (7 أيام)، وتُعاد المحاولة بحجز جديد مع القطعة التالية، فيبقى استئناف Android بعد ساعات ممكنًا.
- **سقف لبايتات المجهولين:** البايتات المحفوظة (غير المنتهية) في جلسات بلا هوية محدودة لكل مشروع بـ `storage.limits.anonymous_staged_fraction` (20% من `project_bytes` افتراضيًا)، تُحجز مع الحصة نفسها وتُرفض بعدها بـ 402 و`final`. فمجهول لا يستنزف إلا خُمس الحصة، والمستخدمون المسجّلون يبقى لهم الباقي.
- **ما يبقى، ويُوثَّق:** لاستنزاف ذلك الخُمس يجب أن **يرسل المهاجم البايتات فعلًا** وأن يُبقي جلساته تتقدم، تحت حد المعدل لكل IP، وأدنى معدل للطلب، وحد الطلبات المتزامنة. وتنتهي جلساته بعمرها أو بخمولها، أو يلغيها المالك من اللوحة.
- يتحول المحجوز إلى مستعمل في معاملة الإنهاء، ويُحرَّر عند الإلغاء والانتهاء والفشل. بلا حجز، مئة رفع متوازٍ يمر كل منها من الفحص.
- **الحصة لا تتسرب بالانهيار:** قبل فتح المستمع (5.1) تُصالَح كل المشاريع: `reserved = Σ reserved_bytes` للجلسات الحية، و`used = Σ size` للكائنات، و`object_count`. وحجوزات `Insert` المعلّقة تصفَّر معها. و**`rewrite` يُحسب على الحصة** كأي كتابة. والانتهاء يحذف ملف staging و`blob_key` إن وُجد ولم يشر إليه صف.
- **أرضية القرص محجوزة لا مفحوصة فقط:** عدّاد بايتات جارية للعملية كلها (القرص شأن العملية لا القاعدة، فلا يدخل `usage`)، وكل خطوة حجز (قطعة، أو خطوة 8 MiB، أو `Insert`، أو `rewrite`) تشترط `free − inflight − n ≥ min_free_bytes`، وإلا 503. **والفحص والإضافة عملية ذرية واحدة** (حلقة CAS على `atomic.Int64`، أو قفل)، فطلبان متزامنان لا يمران كلاهما من فحص واحد. فحص البدء وحده لا يكفي: طلب واحد قد يبلغ 5 GiB، والطلبات المتزامنة تمر كلها من الفحص نفسه، والقرص مشترك مع WAL في Postgres («If the disk holding the WAL files grows full, database server panic and consequent shutdown might occur»، وثائق Postgres).
- **حجم أقصى للكائن** `storage.limits.object_bytes` (5 GiB افتراضيًا).
- **الرد:** على `/v0` **402** (JS: `storage/quota-exceeded`، iOS: `quotaExceeded`، A §5.2). وعلى GCS 403 بسبب `quotaExceeded` (مؤقت، لا دليل على رد GCS). وتُذكر في `docs/compat`.

### 7. رابط الرفع صلاحية مربوطة عند البدء

قطع iOS تصل بلا `Authorization` ولا App Check ولا GMPID (مقيس، P قسم iOS)، وبدء `save()` المتقطع في `@google-cloud/storage` يصل بلا مصادقة ما لم يُضبط `universeDomain` (P، قسم Admin Node). وفي GCS «This session URI acts as an authentication token» (F §3.1). فـ:

- **`upload_id` 128 بت عشوائية** (base64url). **يُخزَّن هاشه فقط** (`crypto.Keys.HashToken`، `internal/platform/crypto/tokens.go:38`)، ولا يُكتب في أي سجل (سجل الطلبات يكتب المسار بلا الاستعلام، `middleware.go:66-71`).
- **يُربط عند `start`** في صف الجلسة:
  - المشروع والـ bucket واسم الكائن. طلب بالمعرّف على اسم أو bucket آخر يُرفض 404.
  - **الهوية:** للعميل `uid` ونسخة من claims الرمز المحقق (تُبنى منها `request.auth` عند الإنهاء)، أو «مجهول». ولـ Admin معرّف حساب الخدمة.
  - الحجم المعلن ونوع المحتوى والـ metadata بعد التنقية (البند 9)، والشروط المسبقة وchecksums العميل.
  - `expires_at`.
- **القطع والاستعلام والإلغاء تُقبل بالمعرّف وحده،** و**`Authorization` إن جاء عليها يُتجاهل** (قرار المسؤول، 2026-10-24): رابط الرفع هو الصلاحية، والهوية سُجّلت عند `start`. فلا 401 ولا 403 غير نهائيين يدخلان Android وiOS في حلقة `query` (البند 6.1)، ولا يغيّر رمز على قطعة شيئًا. وهذا يخالف ما اقترحه P في الشرط 11 (رفض الهوية المخالفة)، عن قصد.
- **`start` نفسه يحتاج هوية صالحة إن حمل `Authorization`:** رمز غير صالح أو منتهٍ 401، كأي طلب. وبلا `Authorization` الهوية «مجهول»، وتقرر القواعد عند الإنهاء.
- **رمز المستخدم لا يُعاد فحص انتهائه عند الإنهاء** (ساعة عمر الرمز، والجلسة أيام). الهوية هي ما تحقق عند `start` (A §7).
- **لكن حالة المستخدم الحية تُفحص عند الإنهاء:** `idtoken` يتحقق عند كل طلب من «the user's live state (deleted, disabled, revoked)» (`idtoken.go:8-10`)، وRTDB ينهي الجلسات عند الإبطال. فعند الإنهاء يُسأل Auth عن `uid` الجلسة: محذوف، أو معطّل، أو `AuthTime` قبل `tokensValidAfter` (`Revocation.Revokes`، `idtoken.go:101-105`): **401 مع `final`**، ولا كائن. فلا يكمل مستخدم معطّل 50 جلسة من 5 GiB. ويحتاج ذلك واجهة صغيرة جديدة في `platform/idtoken` (`Users.State(ctx, project, uid)`) يربطها `app` بخدمة Auth، كما يربط `Verifier` (العقد S2-0).

### 8. القواعد: نقاط التقييم والربط

**القواعد تُقيَّم مرة واحدة في كل عملية، وحسابات الخدمة (Admin) تتجاوزها كما في Firebase** (ADR-021 §7). والطلب الذي يتعطل فيه التقييم (`ErrOverloaded`، أو عطل) يُرد 503 مع `Retry-After`، لا 403 ولا 500 (iOS لا يعيد على 500 ولا 429، A §5.3؛ ADR-021 §2.2).

| العملية | `Method` | المسار للـ `match` | `resource` | `request.resource` |
|---|---|---|---|---|
| GET metadata، و`alt=media` بلا رمز صالح | `get` | `b/<bucket>/o/<مقاطع الاسم>` | الكائن الموجود، أو `null` | `null` |
| list | `list` | `b/<bucket>/o/<مقاطع البادئة>/` + `UnknownSegment` | `null` | `null` |
| رفع (media، multipart، إنهاء resumable) | **`create` دائمًا، وفوق كائن موجود أيضًا** | الاسم | **الكائن الموجود** أو `null` | الكائن الجديد |
| PATCH (`updateMetadata`) | `update` | الاسم | الموجود | الموجود بعد تطبيق التعديل |
| DELETE | `delete` | الاسم | الموجود أو `null` | `null` |

- **الكتابة فوق كائن = `create`، والدليل من Storage منشور:** في PY-RR، رفع فوق كائن موجود: `verb-create: ALLOW` و`verb-update: DENY`، و`resource-existing: ALLOW` (`allow write: if resource.size == 5`، أي أن `resource` هو الموجود)، و`create-stored-and-new-size: ALLOW`. ويطابق الوثائق: `create` «Applies to writes to file contents»، و`update` لتعديل الـ metadata (core-syntax، E §3.3) والمحاكي (`rules.json`). فهذا البند المفتوح في E §10-3 محسوم بالمنشور.
- **الإنهاء يُقيَّم بهوية `start`** وبالقيم الفعلية (`request.resource.size` = ما وصل) و`request.time` = وقت الإنهاء. **ولا تقييم عند `start`:** Android لا يعلن الحجم، فقاعدة تقرأ `request.resource.size` بقيمة مخمّنة قد ترفض ما يقبله Firebase. والحماية المبكرة بالحد الأقصى والحصة لكل قطعة (F §3.3، S8). يُضاف تقييم مبكر فقط بدليل إنتاج، وفقط حين يُعرف كل مدخل تقرؤه القاعدة.
- **الصلاحية قبل الوجود:** القاعدة تُقيَّم أولًا. رفض = 403 ولو لم يوجد الكائن، وسماح لغير موجود = 404 (`rules.json`: B يقرأ غير موجود في مجلد A فيأخذ 403، D §7.1). وهذا لا يكشف وجود الأسماء.
- **list على «الأبناء»، كما يقرر محرك Google في المحاكي:** المسار هو البادئة **ومعها مقطع مجهول** (`UnknownSegment` المحجوز في ADR-021 §2.4 لهذا الغرض). فـ `match /p/{f} { allow list }` يسمح بقائمة `p/`، ولا يسمح بقائمة `p/a/` (`aruma/storage/read_list` و`aruma/storage-paths/list_on_folder` في parity-exceptions.md:45). Test API تطابق المسار نفسه، لكنها أوراكل للغة لا لشكل الطلب (ADR-021 §6). والمنشور غير مقيس: **مؤقت، ويُوثَّق**. و`{x}` الذي يأخذ المقطع المجهول بلا قيمة، وقراءته خطأ (رفض).
- **`list` على `rules_version` 1 يُرفض** (core-syntax، ADR-021 §5.5؛ المحاكي: «List operations are only allowed for rules_version='2'»، D §4.1).
- **`request.resource` عند الإنشاء**، المفاتيح الخمسة عشر التي سجّلها PY-RR (`keys-size-15: ALLOW` و13 و14 و16 رفض): `name` و`bucket` و`size` و`contentType` و`contentDisposition` و`contentEncoding` و`contentLanguage` و`cacheControl` و`metadata` و`md5Hash` و`crc32c`، و`generation` و`metageneration` و`etag` و`storageClass` **بقيمة `null`** (`generation-null` و`etag-null` و`storage-class-null`: ALLOW). **ولا `timeCreated` ولا `updated`** (`present-timeCreated`: DENY). وغير المضبوط: `contentLanguage` و`cacheControl` و`metadata` بقيمة `null`، و`contentEncoding` = `'identity'`، و`contentDisposition` = القيمة الافتراضية المخزّنة (البند 10.4) (`unset-*` في PY-RR).
- **`request.resource` عند PATCH:** المفاتيح نفسها بقيم الموجود بعد التعديل، و`generation` و`metageneration` و`etag` **دون تغيير** (`metageneration-next: DENY`)، و`storageClass = 'STANDARD'`، وبلا `timeCreated` ولا `updated` (PY-RR، `update`).
- **`resource` (الموجود):** حقول مرجع Storage بأنواعها: `size` و`generation` و`metageneration` int، و`timeCreated` و`updated` timestamp، و`metadata` خريطة (المرجع؛ `aruma/storage/resource_size` و`resource_time_created_is_ts` في A-oracle-findings §3). ومجموعة المفاتيح الدقيقة تُطابَق بملف Pyric المنشور `stdlib-realstorage-p2-native-object-fields.json` في S2.
- **`metadata` في `resource` و`request.resource` لا تحتوي `firebaseStorageDownloadTokens` أبدًا** (البند 9).
- **`request.auth`:** `rules.Auth(uid, token)` من رمز `internal/platform/idtoken` المحقق، و`uid` من `sub` لا من `user_id` كالمحاكي (E §7). و`null` للمجهول.
- **`request.path`** قيمة `Path` لمقاطع `b/<bucket>/o/<الاسم>` (`aruma/storage/request_path`)، وفي list مقاطع البادئة (كالمحاكي، D §4.2). **و`request.time`** وقت التقييم.
- **`request.method` و`request.params`:** مرجع firebaserules لحقل `TestCase.request`: «Minimally, all services support the following fields and types: … `method` string, `params` map<string, string>» (projects.test، قُرئ 2026-10-24). فيُربطان:
  - `request.method` = اسم الطريقة المقيَّمة (`'get'` و`'list'` و`'create'` و`'update'` و`'delete'`). وقاعدة Pyric `request.method == 'create'` تسمح عبر Test API (`metadata-verbs-and-arithmetic`). المحاكي لا يمررها (D §4.2، `aruma/storage/method_list`)، والوثائق تغلبه في المتغيرات (ADR-021 §6). **مؤقت، ويُوثَّق.**
  - `request.params` = **خريطة فارغة** في v0.3 لكل عملية. ما يضعه الإنتاج فيها غير معروف، والخريطة الفارغة تجعل قراءة أي مفتاح خطأً (رفضًا)، فلا نكون أكثر تساهلًا إلا في نفي الوجود (`!('x' in request.params)`)، ويُوثَّق.
- **القواعد من `tenancy.CompiledRelease(ctx, project, "firebase.storage/default")`** (الاسم المنطقي، البند 2). الإصدارة الغائبة أو الـ ruleset الذي لم يعد يُترجم يرفض كل شيء (ADR-021 §7). **والقاعدة الافتراضية لمشروع جديد مقفلة:** قالب `firebase init storage` حرفيًا `allow read, write: if false;` (C §2.1، ADR-010).
- **الحدود من ADR-021:** قراءتان لـ `firestore.*` في التقييم (لا أثر في v0.3)، و`Budget` واحد لكل طلب، و`Inflight` العملية.

### 9. رموز التنزيل

- **يولّدها الخادم وحده،** UUID v4:
  - **عند الإنشاء عبر `/v0`** (media وmultipart وresumable): رمز جديد دائمًا (CT: `downloadTokens` موجود بعد رفع خام، E §5).
  - **عند GET metadata مصرّح به على `/v0` لكائن بلا رموز:** يُنشأ رمز ويُحفظ ذريًا مع رفع `metageneration` (`UPDATE … WHERE cardinality(download_tokens) = 0 RETURNING`). الدليل منشور: Pyric `mintsTokenIntoMetadata: true` (E §5)، وCT `returns url pointing to the expected host` (`firebase-js-sdk.test.ts:451-462`). وبدونه يفشل `getDownloadURL` لكل ملف رفعه Admin (JS `storage/no-download-url`، A §1.14). ولا يُنشأ على طلب مرفوض، ولا على `alt=media`.
  - **عبر GCS (Admin) لا رمز عند الرفع** (Pyric `afterSave.hasDownloadTokens: false`، E §5)، ويضبطه Admin إن شاء.
- **العميل لا يزرع رمزًا ولا يحذفه:** المفتاح `firebaseStorageDownloadTokens` يُحذف من الـ metadata المخصّصة الواردة على `/v0` في multipart و`start` وPATCH، بقيمة أو بـ `null`، **قبل تقييم القواعد وقبل الحفظ** (E §5؛ المحاكي يقبل الزرع، `tokens.json`). وحقل `downloadTokens` في جسم PATCH يُتجاهل.
- **مخفية في ردود `/v0`:** حقل `downloadTokens` مستقل (نص بفواصل، A §4)، و`metadata` لا يحتوي المفتاح أبدًا (Pyric `metadataHasTokenKey: false`، E §5).
- **ظاهرة وقابلة للتعديل على GCS:** `metadata.firebaseStorageDownloadTokens` كما يراها GCS في الإنتاج (Pyric `afterSet`، E §5). Admin يستبدلها أو يحذفها، **فيُبطل الرابط القديم** (Pyric `revokedUrlStatus: 403`، E §5؛ اتحاد المحاكي انحراف). حذف آخر رمز من GCS لا يولّد غيره، والطلب التالي المصرّح به على `/v0` يولّده.
- **`?create_token=true` و`?delete_token=<t>` على `/v0` لـ Admin وحده** (`Bearer` حساب خدمة)، و403 لغيره، و400 لقيمة غير `true`، وحذف آخر رمز يولّد رمزًا جديدًا فورًا (CT، E §5).
- **الرمز يفتح `alt=media` وحده،** لا الـ metadata بلا `alt=media` (أضيق من المحاكي، E §5، مؤقت).
- **`token` موجود ولا يطابق: 403، بلا تقييم للقواعد** (قرار المسؤول: الأشد). الملاحظة المنشورة الوحيدة، Pyric `revokedUrlStatus: 403` (`admin-storage-metadata-download-url.json`)، أُخذت تحت قواعد مشروع مجهولة، فلا تفرّق بين «الرمز الخاطئ يُرفض» و«القواعد رفضت». والأشد لا يكون أكثر تساهلًا من أيهما. **مؤقت، ويُوثَّق** (ADR-023 §3).
- **مختومة في القاعدة:** ADR-010 S-15 يمنع أي رمز خام في القاعدة (فنسخة من القاعدة لا تحمل رموزًا). فقائمة رموز الكائن تُختم بـ `crypto.Keys.Seal` (الـ KEK، `envelope.go:27`)، والبيانات الإضافية `(project_number, name)`. تُفتح عند قراءة الـ metadata (لتظهر في `downloadTokens`)، وعند `alt=media` بعد البحث عن الكائن باسمه، ثم تُقارن بزمن ثابت (`crypto/subtle`). وrewrite يختمها من جديد لاسم الوجهة.
- **لا يُسجَّل الرمز**. والنسخ (rewrite) ينسخ الرموز ما لم تُستبدل الـ metadata (CT، E §5).

### 10. خدمة البايتات بأمان (`media`)

كل رد بايتات (`alt=media` على `/v0`، والوسائط على GCS، وتنزيل اللوحة) يمر بحزمة `media` واحدة.

#### 10.1 الترويسات

```
Content-Type: <المخزّن، أو application/octet-stream إن كان فارغًا>   (لا sniffing أبدًا)
X-Content-Type-Options: nosniff
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox   (لكل الأنواع، 10.2)
Cross-Origin-Resource-Policy: cross-origin
X-Frame-Options: DENY ، Referrer-Policy: no-referrer                       (العامة كما هي)
Content-Disposition: <10.4>
Cache-Control: <10.5>
ETag ، Last-Modified ، Accept-Ranges: bytes
x-goog-generation ، x-goog-metageneration ، x-goog-hash: crc32c=<b64>,md5=<b64> ، x-goog-stored-content-encoding
```

- **`Content-Type` مضبوط قبل `http.ServeContent` دائمًا،** وإلا خمّن Go النوع من الامتداد ثم من أول 512 بايت (`net/http/fs.go:222-225`، F §2.2-4): ملف `x.html` بلا نوع كان سيصير `text/html`.
- **`CORP: cross-origin` تجاوز لكل مسار** يكتبه معالج الوسائط فوق `same-origin` العامة (`server.go:140`)، كما تستبدل اللوحة CSP لكل صفحة. بدونه يُحجب `<img src=downloadURL>` من كل تطبيق على أصل آخر، وهو الاستعمال الأشيع (F §2.2-1). وهو ما يرسله مضيفا المحتوى عند Google وGitHub (`curl -I`، F §2.1). ردود JSON تبقى على الترويسات العامة.
- **`x-goog-hash` في ترويسة واحدة بلا مسافة بعد الفاصلة** (Node يقسم على `,` دون حذف المسافات، B §6.1)، و`x-goog-stored-content-encoding: identity|gzip` دائمًا، وإلا تخطى Node التحقق بصمت أو فشل (B §6.1، F §3.4). و`x-goog-metageneration` بالتهجئة الصحيحة (المحاكي يكتب `metadatageneration`، E §4).
- **لا ترويسة فارغة** (المحاكي يرسل `cache-control: ''`، E §4).
- **رفض العمّال:** طلب تنزيل فيه `Service-Worker: script`، أو `Sec-Fetch-Dest` = `serviceworker` أو `sharedworker` أو `worker`، يُرد **403**. ملف JS مرفوع يُسجَّل Service Worker على أصل اللوحة (بثغرة XSS في أي صفحة) يعترض كل تنزيلات الـ bucket ويبقى بعد إصلاح الثغرة (F §2.2-3).
- **ورفض التحميل كسكربت من أصل اللوحة نفسه (قرار المسؤول):** CSP اللوحة `script-src 'self'` و`style-src 'self'` (`internal/admin/web/web.go:152-161`)، وملف مرفوع بـ `text/javascript` يمر بـ nosniff، و`sandbox` في CSP الرد لا ينطبق على سكربت يُحمَّل موردًا فرعيًا. فأي حقن HTML في اللوحة كان سيصير تنفيذ سكربت كامل على أصلها (يُبطل ADR-010 S-9). لذلك طلب على مسار وسائط فيه `Sec-Fetch-Site: same-origin` و`Sec-Fetch-Dest` من `script` أو `style` أو `worker` أو `sharedworker` أو `serviceworker` أو `audioworklet` أو `paintworklet` أو `xslt` يُرد **403**.
  - التضمين من أصول التطبيقات (`cross-site` و`same-site`) لا يتأثر، ولا `<img>` و`<video>` من اللوحة نفسها (`image` و`video`).
  - **وتُضيَّق CSP اللوحة إلزامًا** إلى مصادر بمسار (`script-src` و`style-src` = `<origin>/_next/static/` بدل `'self'`) في S5-C، فلا يُحمَّل من أصل اللوحة سكربت ولا أسلوب إلا من أصولها المبنية، مهما أرسل المتصفح.
  - **على HTTP العادي لا تعمل القاعدة الأولى أبدًا:** المتصفحات لا ترسل `Sec-Fetch-*` إلى أصل ليس «potentially trustworthy» (Fetch Metadata §3)، فطلب السكربت من اللوحة على `http://` غير محلي لا يُميَّز من `<img>`، و`media` لا تستطيع رفضه. حمايته هناك CSP اللوحة المضيَّقة وحدها، ويكتب الخادم تحذيرًا عند الإقلاع حين Storage مفعّل و`public_url` بـ `http` على مضيف غير loopback (`internal/app`). وكذلك متصفح لا يرسل Fetch Metadata أصلًا. ولا يتأثر أي استعمال مشروع لرابط تنزيل.

#### 10.2 CSP حسب النوع: `sandbox` لكل نوع في v0.3

- **السياسة الواحدة:** `default-src 'none'; style-src 'unsafe-inline'; sandbox`، وهي سياسة `raw.githubusercontent.com` المقيسة على النص وPNG وSVG (F §2.1). `sandbox` بلا `allow-scripts` يجعل المستند بأصل معتم: لا سكربت، ولا كعكات اللوحة، ولا `localStorage` (F §2.2-2). وتطبَّق على HTML وSVG وXML والنص والصور وكل نوع مجهول.
- **الوسائط (`video/*` و`audio/*`)** تزيد `media-src 'self'`، وتبقى تحت `sandbox`. التضمين في `<video>`/`<audio>` من أصل آخر لا يتأثر بـ CSP الرد (مورد فرعي)، والتشغيل بالتصفح المباشر تحسمه مصفوفة المتصفحات (10.6). لا نرفع `sandbox` عن نوع قبل أن تثبت المصفوفة أمانه في المحركات الثلاثة.
- **PDF (`application/pdf`) يُخدم تحت `sandbox` ومعه `Content-Disposition: attachment` قسرًا.** عارض PDF في Chromium plugin، و`sandbox` يمنع الـ plugins، فيظهر عارض مكسور بلا `attachment` (F §2.3، Chromium `html_plugin_element.cc:879-891`). وGitHub لا يقدم سابقة: يخدم PDF كـ `application/octet-stream` (F §2.1). فالرابط في المتصفح **يُنزِّل الملف ولا يعرضه**. فرق موثّق في ADR-023، والعرض المضمَّن ينتظر المصفوفة وقرارًا لاحقًا.

#### 10.3 Range

- نطاق واحد `bytes=a-b` أو `bytes=a-` أو `bytes=-n`: **206** مع `Content-Range: bytes a-b/N`. Android يلحق جسم 200 بالملف عند الاستئناف دون فحص 206، فتجاهل `Range` يُفسد ملفه (A §1.11).
- **كائن فارغ مع أي `Range`: 200 بجسم فارغ، لا 416.** JS يرسل `Range: bytes=0-N` دائمًا مع `maxDownloadSizeBytes` ويقبل `[200, 206]` وحدهما (`requests.ts:227-230`)، والخادم «MAY ignore the Range header field» (RFC 9110 §14.2) (E §4).
- غير الفارغ: نطاق يبدأ بعد آخر بايت **416** مع `Content-Range: bytes */N`. ونطاقات متعددة: 200 بالكائن كاملًا (التجاهل مسموح، ولا مكتبة رسمية ترسلها، A §3.3). و`If-Range` لا يطابق `ETag` المحتوى (البند 12): 200 كاملًا.
- مع فك الضغط (10.7) يُتجاهل `Range` ويُرد 200 كاملًا، كما في GCS.

#### 10.4 `Content-Disposition`

- **القيمة المخزّنة إن وُجدت** (العميل يملك الحقل، فلا نعتمد عليه حماية، F §2.2-2). تُرفض عند الكتابة القيم التي فيها CR/LF أو محارف تحكم (البند 17).
- **الافتراضي عند الإنشاء عبر `/v0` يُخزَّن، كما في الإنتاج:** `inline; filename*=utf-8''<آخر مقطع من الاسم مرمَّزًا>`. الدليل PY-RR `storedObjects`: `plain.txt` يصير `inline; filename*=utf-8''plain.txt`، و`naïve 'q' 1+1%;x.txt` يصير `…na%C3%AFve%20%27q%27%201+1%25%3Bx.txt` في عرض GCS وعرض العميل معًا. **مجموعة الهروب تُطابَق بهذه الحالة حرفيًا** (`+` يبقى، و`'` يصير `%27`). والمحاكي يخزّن `inline` وحدها (D §2.2)، فنتبع المنشور.
- **عبر GCS لا قيمة افتراضية** (PY-RR: `gcs-json-api-seed` بلا `contentDisposition`). وعند التنزيل بلا قيمة مخزّنة: `attachment; filename*=utf-8''<آخر مقطع>` (CT `serves the right content` يتوقع `attachment`، E §4).
- **PDF:** `attachment` يحل محل المخزّن (10.2).
- **`contentEncoding`:** لا يُخزَّن افتراضيًا، ويظهر `identity` في ردود `/v0` وفي `request.resource`، ويغيب في GCS (PY-RR `storedObjects`، `unset-identity-contentEncoding`).

#### 10.5 `Cache-Control`

- **على تنزيلات `/v0` التي قررتها القواعد وحدها (الطلب فيه `Authorization`):** `public` يُستبدل بـ `private`، ويُحذف `s-maxage`. والقيمة المخزّنة يضبطها العميل نفسه، و`public` على رد طلب فيه `Authorization` يسمح لمخبأ مشترك بخزن كائن خاص وتقديمه لغير صاحبه (RFC 9111 §3.5، E §4). وبلا قيمة مخزّنة: `private, max-age=0`.
- **على رابط الرمز:** المخزّن كما هو، فالرابط نفسه هو الصلاحية. وبلا قيمة: `private, max-age=0` (مؤقت، الإنتاج غير مقيس).
- **ردود JSON:** `private, max-age=0`، كما رُصد على الإنتاج (E §1).
- **مسارات GCS** (`/storage/v1` و`/download/storage/v1`) **تخدم القيمة المخزّنة كما هي:** طلباتها تحمل `Bearer` دائمًا والقواعد لا تقررها، وGo يملأ `ReaderObjectAttrs.CacheControl` من الترويسة مباشرة (`http_client.go:1635`)، فالتبديل يجعله يخالف `Attrs().CacheControl` المخزّن.

#### 10.6 مصفوفة المتصفحات (شرط قبول S3)

Playwright على Chromium وFirefox وWebKit، بحالات F §2.4 الأربع عشرة. أهمها: HTML وSVG لا ينفّذان سكربتًا تصفحًا مباشرًا (`window.origin === "null"`)؛ و`<img>`/`<video>`/`<audio>` من أصل آخر تُحمَّل (ومع COEP `require-corp`)؛ و`fetch` من أصل آخر يفشل بلا CORS للـ bucket وينجح معه؛ وتسجيل SW يُرفض؛ و`<iframe>` محجوب؛ والقفز في MP4 على WebKit يعمل؛ وPDF يُنزَّل.

#### 10.7 فك الضغط (decompressive transcoding)

- **كائن مخزّن بـ `Content-Encoding: gzip`، وطلب بلا `Accept-Encoding: gzip`:** يُفك الضغط تدفقًا بـ `gzip.Reader` ويُرسل **بلا** `Content-Encoding` ولا `Content-Length`، ويُتجاهل `Range` (CT `gzip`، `firebase.endpoints.test.ts:481-514`، ليست `emulatorOnly`؛ ووثيقة GCS transcoding، E §4). ومع `Accept-Encoding: gzip` تُرسل البايتات كما خُزّنت مع `Content-Encoding: gzip`. و`Cache-Control: no-transform` المخزّن يمنع التحويل.
- **لا فك في الذاكرة** (المحاكي يستعمل `gunzipSync`، F §2.1): قنبلة 10 MB قد تنتفخ غيغابايتات. والمخرَج المفكوك محدود بـ `object_bytes`. تجاوزه يقطع الاتصال (الرأس أُرسل) ويُعدّ في مقياس.
- **لا ضغط ديناميكي** لأي رد (B §6.3).

### 11. CORS

- **سطح الـ API على `/v0`** (metadata، والقائمة، والرفع، والحذف، وPATCH، والـ preflight): **مفتوح لكل الأصول** بـ `Access-Control-Allow-Origin: *`، **بلا** `Allow-Credentials` (المصادقة بترويسة، لا بكعكة). الدليل: `*` مرصود على ردود الإنتاج في `/v0` (E §2، `bucket_probe.json`)، والرفع وقراءة الـ metadata من المتصفح يعملان في Firebase بلا إعداد CORS (الوثيقة تشترط الإعداد للتنزيل المباشر وحده). الترويسات المسموحة والمكشوفة كما في A §6.1 وP الشرط 5، و`Access-Control-Expose-Headers` يشمل `X-Goog-Upload-URL` و`X-Goog-Upload-Status` و`X-Goog-Upload-Size-Received` (JS يقرؤها، A §6.1).
- **`alt=media` يحكمه إعداد CORS للـ bucket، والافتراضي لا شيء:** «To download data directly in the browser, you must configure your Cloud Storage bucket for cross-origin access (CORS)» (download-files). فـ `getBytes` و`getBlob` و`fetch(downloadURL)` من أصل آخر تفشل حتى يضبط المالك القائمة، كما في Firebase. و`<img>`/`<video>` لا تحتاج CORS. السماح للكل (خيار E §2-2) **أكثر تساهلًا من bucket Firebase بلا إعداد، فهو مرفوض.**
- **الإعداد بدلالة GCS** (`cors[]`: `origin` و`method` و`responseHeader` و`maxAgeSeconds`)، في `storage.buckets.cors`، ويُضبط من:
  - اللوحة (إعدادات Storage).
  - `PATCH /storage/v1/b/<bucket>` بحقل `cors` وحده (`bucket.setCorsConfiguration()` في Node، و`gcloud storage buckets update --cors-file`). أي حقل آخر قابل للكتابة يُرد 400 «not supported»، لا تجاهلًا صامتًا.
- **GCS (`/storage/v1`):** بلا CORS، فعملاؤه خوادم.

### 12. صيغة الردود

- **لا JSON `null` في أي رد على `/v0`:** الحقل بلا قيمة يُحذف. Android يقرأ `null` نصًا `"null"` بـ `optString`، فيرسل `pageToken=null` ويبني رابطًا فيه `&token=null` (A §1.8؛ AOSP `JSONObject.java:579-583`). يُفرض بنوع ترميز واحد في `wire` (`omitempty` على كل حقل، وخرائط نص إلى نص)، واختبار يمر على كل رد مسجّل في اختبارات `/v0` ويرفض أي `null`.
- **الأوقات:** `2006-01-02T15:04:05.000Z` بالضبط، ثلاث خانات ولاحقة `Z`، على `/v0` وGCS. Android يُزيح الوقت بصمت مع كسر أطول ويُرجع 0 بلا كسر، وiOS يعيد `nil` (A §1.6). وPostgres يحفظ الميكروثانية، والتقريب عند الإخراج.
- **الأرقام نصوص:** `generation` و`metageneration` و`size` (و`objectSize` و`totalBytesRewritten` و`projectNumber` في GCS). وسوم `,string` في Go ترفض الأرقام (B §1.7).
- **`generation`** بالميكروثانية من الساعة، **ويزيد دائمًا للاسم نفسه** (`max(now_µs, السابق + 1)`) (Pyric `"1784647629568292"`، E §4). يتقدم مع كل كتابة للبايتات ويثبت مع PATCH، و`metageneration` يتقدم مع PATCH وتوليد الرمز (Pyric `metadata-update-relations`، E §4).
- **`crc32c`:** base64 لأربعة بايتات big-endian **في `/v0` وGCS معًا** (لا مكتبة `/v0` تقرؤه، A §4؛ صيغة الإنتاج في `/v0` غير مقيسة، مؤقت). و`md5Hash` base64.
- **`etag` في JSON:** نص معتم من `(generation, metageneration)`، **يتغير مع PATCH** (وثيقة GCS؛ المحاكي يثبّته، E §4).
- **ترويسة `ETag` على الوسائط من `generation` وحده،** أي من المحتوى: Android يعيد التنزيل من الصفر إن تغيّر `ETag` بين محاولتين (`FileDownloadTask.java:213-225`)، ويرمي 409 = `ERROR_INVALID_CHECKSUM` في `getStream` (`StreamDownloadTask.java:133-139`). ولو تبعت الـ metageneration لأفسد `updateMetadata` أو أول `getDownloadURL` (يولّد رمزًا ويرفع الـ metageneration) استئناف تنزيل جارٍ. وهو ما يقوله A:544: «`ETag` ثابت لمحتوى الكائن». و`If-Range` يقارن بهذه القيمة.
- **`metadata` خريطة نص إلى نص فقط:** iOS يُسقط الخريطة كلها إن كانت قيمة واحدة غير نصية (A §1.7).
- **الأخطاء:**
  - `/v0`: `{"error":{"code":<الحالة>,"message":"…"}}`. 404 «Not Found.» (E §1)، و403 «Permission denied.» (PY-RR `diagnostics`)، و400 «Invalid HTTP method/URL pair.» لمسار مجهول (E §1). JSON مضغوط بـ `Content-Type: application/json; charset=UTF-8`. ولا 409 أبدًا (Android يعرضه `invalid-checksum`، A §5.2)، ولا 308.
  - GCS: `{"error":{"code","message","errors":[{"domain":"global","reason","message"}]}}`، و`code` **رقم يساوي حالة HTTP** وإلا لا يعرف `exists()` في Node أن الحالة 404 (B §8).
  - الضغط المؤقت: 503 مع `Retry-After` محدود (iOS يعيد على 503 ويحترم `Retry-After`، ولا يعيد على 429 ولا 500، A §5.3؛ و`Retry-After` الدائم قد يعني إعادة بلا نهاية، فالقيمة محدودة).

### 13. القوائم

- **`/v0`:** `prefix` و`delimiter=/` و`maxResults` و`pageToken`. الرد `{"prefixes":["a/b/"],"items":[{"name":"…","bucket":"…"}],"nextPageToken":"…"}`:
  - `prefixes` و`items` حاضرتان ولو فارغتين (CT، E §6)، و`items` بحقلين نصيين فقط (iOS يشترط كل القيم نصوصًا، A §3.7).
  - **`nextPageToken` يُحذف في آخر صفحة،** فالنص الفارغ يدخل `listAll` في حلقة لا تنتهي على المنصات الثلاث، و`null` في Android (A §1.8).
  - الأسماء ذات المقطع الفارغ (`a//b`، `/x`) تُحذف من `/v0` (CT `should not include show invalid prefixes and items`، E §6)، وتبقى في GCS.
  - `maxResults` بين 1 و1000، والأكبر يُقص إلى 1000، والافتراضي 1000 (A §3.7؛ المحاكي بلا حد، E §6).
- **GCS:** المعاملات الفارغة (`delimiter=` و`prefix=` و`pageToken=` …) تُعامل كغائبة، و`versions=false` و`includeTrailingDelimiter=false` و`includeFoldersAsPrefixes=false` مقبولة، و`projection` و`fields` و`prettyPrint` و`alt=json` و`userProject` تُتجاهل (Go يرسلها دائمًا، B §4.2). و`startOffset`/`endOffset` مدعومان. و`matchGlob` وقيم `true` لتلك الأعلام 400 صريح (B §10.3).
- **`pageToken` معتم وموقّع:** HMAC بمفتاح جديد له غرض خاص يُشتق في `crypto.NewKeys` (`aruma/v1/storage-page-token`)، لأن «every purpose gets its own key» و`Keys` لا يحمل اليوم إلا `tokenMAC` و`csrfMAC` والـ KEK (`keys.go:16-41`). يُضاف في S2-0. والموقَّع `(project، bucket، prefix، delimiter، آخر اسم أُعيد)`. رمز غير صالح أو لطلب آخر: **400** (GCS؛ المحاكي يعيد من الأول، E §6). فلا يُستعمل رمز مشروع على مشروع آخر (البند 3).
- **البادئات تُرقَّم مع العناصر** بترتيب واحد بالبايتات، كما في GCS (المحاكي يعيدها كاملة في كل صفحة، E §6).
- **القائمة مشي على الفهرس، لا مسح للـ bucket:** مع `delimiter`، كل بادئة تُستخرج بقفزة فهرس واحدة إلى ما بعدها (`name >= prefix || delimiter-incremented`). فالصفحة الواحدة ≤ `maxResults + 1` بحثًا في الفهرس مهما كان حجم الـ bucket.

### 14. سطح GCS ومسار Admin

- **المصادقة إلزامية:** `Authorization: Bearer <access token>` لحساب خدمة أصدره Aruma (`/oauth2/v1/token`)، عبر `authn.Verifier` كما يفعل RTDB (`rtdb/api/api.go:41-44`). وحساب الخدمة مقيد بمشروعه (`Principal.ProjectNumber`)، فـ bucket مشروع آخر 403/404. **طلب GCS بلا مصادقة يُرفض**، ومنه وضع المحاكي (`STORAGE_EMULATOR_HOST`) في Node وGo الذي لا يرسل `Authorization` أبدًا (B §3، E §9). قبوله، أو قبوله من شبكة «موثوقة»، أكثر تساهلًا من Firebase (E §9، P الشرط 10).
- **ما يُخدم في v0.3** (B §10.1، بالمسار تحت `/storage/v1` وحده): `buckets.get` و`buckets.patch` (`cors` وحده)، و`objects.get` و`list` و`patch` و`delete`، و`rewriteTo` في طلب واحد يرد `done:true` (ضمن المشروع نفسه)، والرفع (`/upload/storage/v1`: media وmultipart وresumable)، والوسائط (`alt=media` تحت `/storage/v1` و`/download/storage/v1`). ودلالة PATCH: `null` على حقل يحذفه، و`metadata` تُدمج مفتاحًا مفتاحًا و`"metadata": null` يمسحها (B §4.3). و`?generation=N` (Go يثبّت به القراءة بعد انقطاع، B §6.2) يُقبل إن طابق الإصدار الحالي، وغيره 404، فلا إصدارات.
- **`@aruma/admin-storage` (Node، `sdk/node/admin-storage`)** على نمط `@aruma/admin-auth`:
  - تعيد `Storage` من `@google-cloud/storage` بـ `apiEndpoint` = أصل Aruma، و**`universeDomain`** = نطاق Aruma، و`useAuthWithCustomEndpoint: true`، و`authClient` يأخذ رمزه من `token_uri` Aruma. مع `universeDomain` وحده يحمل بدء `save()` المتقطع وقطعه `Bearer` (مقيس، P جدول Admin Node: «`Bearer` على الكل، والرفع المتقطع منه»).
  - **تصدّر `getDownloadURL(file)` خاصًا بها:** يطلب الـ metadata من `/v0` على أصل Aruma بالعميل المفوَّض، ويبني الرابط الموحّد (البند 16). `getDownloadURL` في firebase-admin يرسل رمز Aruma إلى `firebasestorage.googleapis.com` (مقيس، P).
  - **ترفض البدء إن وُجد `STORAGE_EMULATOR_HOST` أو `FIREBASE_STORAGE_EMULATOR_HOST`،** كما ترفض `@aruma/admin-auth` متغير محاكي Auth.
- **`adminstorage` (Go، `sdk/go/adminstorage`)** على نمط `adminauth`: `*storage.Client` من `cloud.google.com/go/storage` بـ `option.WithEndpoint("<aruma>/storage/v1/")` و`storage.WithJSONReads()` ومفتاح الحساب، **منفصلًا عن `firebase.App`** (7/7 بـ `Bearer`، P). و`WithEndpoint` على `firebase.NewApp` يكسر FCM (مقيس: 404 على `/storage/v1//projects/…/messages:send`، P:283) وFirestore. وترفض `STORAGE_EMULATOR_HOST`، وتصدّر `DownloadURL` بالشكل الموحّد. و`WithJSONReads()` إلزامي: القراءة الافتراضية XML (B §6.2) لا نخدمها (البند 1).

### 15. الإدارة

- **`firebase deploy --only storage` و`firebase init storage`** بمتغيرات الأداة على أصل Aruma، كلها https (http يرسل `Bearer owner`، C §1.5): `FIREBASE_RULES_URL` و`FIREBASE_FIREBASESTORAGE_URL` و`FIREBASE_SERVICE_USAGE_URL`، ومعها `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_SKIP_INFORMATIONAL_IAM=1` القائمة (docs/guides/rtdb.md:98-99).
- **firebaserules v1 في `internal/admin/mgmt`** (rules-layout.md:120) فوق `tenancy`: R1 إلى R8 في C §1.5. **كل نجاح 200 حرفيًا،** والحذف `{}` (الأداة تفحص `status === 200`، C §1.5). و`:test` يرد 200 مع `issues[]` لخطأ الترجمة (الأداة تطبع `[E] line:column`)، وكل عنصر بـ `createTime` RFC 3339، و`pageToken=` الفارغ مقبول، و`PATCH` على `releases/firebase.storage/<bucket>` (اسم فيه `/`) يحدّث أو يرد 404 فتنشئه الأداة بـ `POST`. الواجهة لا تعرف الخدمة: تقبل `cloud.firestore` أيضًا، ولا يستهلكها شيء قبل v0.4.
- **`GET /v1alpha/projects/{p}/defaultBucket`** يرد `{"name":"projects/{p}/defaultBucket","location":…,"bucket":{"name":"projects/{p}/buckets/<bucket>"}}`، والكائن `bucket` لا يغيب أبدًا (الأداة تقرأ `bucket.name`، C §1.3)، و404 برسالة الأداة حين Storage معطّل. **و`GET /v1/projects/{p}/services/{name}`** يرد `ENABLED` لأي اسم (C §1.4). و`/v1beta/projects/{p}/buckets` (Extensions) بالقائمة نفسها.
- **`getIamPolicy` للقواعد العابرة** لا يُخدم: الأداة تحوّل الخطأ تحذيرًا وتكمل النشر (C §1.6). و`firestore.get` يرفض حتى v0.4 على أي حال.
- **اللوحة** (`internal/services/storage/console` عبر `consolemount`، و`dashboard/src/features/storage`): متصفح الملفات (تصفح بالبادئات، ورفع، وتنزيل، وحذف، وتعديل الـ metadata، وإنشاء الرموز وإلغاؤها)، ومحرر القواعد (أخطاء `issues[]` على أسطره، ويرفض الحفظ بخطأ، ADR-021 §7)، وإعداد CORS، والاستعمال والحصة.
  - **تنزيل اللوحة بكعكة جلسة على أصل اللوحة،** فهو أخطر من رابط الرمز: `attachment` دائمًا، وترويسات `media` كاملة. والمعاينة للصور النقطية وحدها داخل `<img>`.
  - **الرفع من اللوحة جلسة رفع تصدرها اللوحة (قرار):** مسارات الكونسول تحت `httpx.Common` بحد 1 MiB (`server.go:76`، `:91`؛ `json.go:16`)، واللوحة بكعكة جلسة فلا تصل إلى مسارات `/v0` ولا GCS بمصادقتها. فطلب JSON صغير `POST /console/v1/projects/{p}/storage/uploads` (بكعكة الجلسة وCSRF) يبدأ جلسة رفع متقطع بهوية Admin (المالك، تتجاوز القواعد) ويعيد رابطها، ثم ترسل اللوحة القطع إلى رابط `/v0` نفسه ببروتوكول `X-Goog-Upload-*` وحدوده، بلا كعكة. فلا مسار كونسول بحد جسم كبير، ولا كعكة على مسار بايتات.
    - **نطاق الجلسة ضيق:** عمرها ساعة لا سبعة أيام، و**الحجم المعلن إلزامي ومربوط عند الإصدار** (الإنهاء بغيره 400 و`final`)، وفي `Principal` معرّف حساب اللوحة (`ConsoleAccount`). وعند الإنهاء يُفحص من جديد أن الحساب ما زال يملك المشروع (وإلا 403 و`final`)، ويُكتب الإصدار والإنهاء في سجل التدقيق. والبديل (فرع `CommonWithLimit` لمسارات الكونسول) يضع كعكة الجلسة على طلبات غيغابايتات، فرُفض.
  - إجراءات الكتابة من اللوحة تُكتب في سجل التدقيق كما في RTDB.
- **`storageBucket` في ملفات الإعداد موجود اليوم** (`configgen.go`، `mgmt/resources.go:27`، `adminSdkConfig`). لا تغيير.

### 16. مسار الإنتاج: رابط موحّد وتبديل النطاق وقت البناء

- **الرابط الموحّد الوحيد:** `https://<aruma-host>/v0/b/<bucket>/o/<path مرمَّز>?alt=media&token=<uuid>`، يخدمه Aruma من **جذر** نطاقه. هو شكل الإنتاج (Pyric `downloadURL`، E §5) على مضيف Aruma، ويُحلَّل في iOS والويب (بعد تبديل النطاق) (P، «شكل رابط التنزيل»). **ويجب أن تنتجه كل منصة حرفيًا:** بلا `:443` (16.3)، وبمضيف lowercase (16.2). **ولا يُستعمل الشكل المسبوق** `<aruma>/firebasestorage.googleapis.com/v0/…` في أي رابط: لا تفهمه أي منصة، ويفسده الويب بصمت (P:342، :354).
- **تبديل النطاق وقت البناء في كل منصة (قرار المالك)،** فتبني المكتبة نفسها الرابط الموحّد، ولا يتسرّب من أي حمّال (`<img>`، و`dart:io`، وCoil، وAVPlayer …). والتحويل وقت التشغيل يبقى **احتياطًا** لمن لم يضبط البناء. وكل تبديل **يفشل البناء بصوت عالٍ** إن لم يجد هدفه، لا ينجح بصمت.

#### 16.1 الويب

- **محوّل واحد بـ `unplugin`** يعمل في Vite وRollup وwebpack وRspack وesbuild، يبدّل تعريف `DEFAULT_HOST = 'firebasestorage.googleapis.com'` في `@firebase/storage` (بأي علامة اقتباس) إلى **نطاق Aruma وحده بلا بادئة**، و`_protocol = 'https'` حين Aruma على http. وهو تعريف واحد في كل ملف dist: `index.esm.js:27`، و`index.browser.cjs.js:29`، و`index.cjs.js:31` و`index.node.cjs.js:31`، و`node-esm/index.node.esm.js:27` (مراجعة البناء، 0.13.14 إلى 0.14.6). نمط إضافة Auth القائمة (`sdk/web/aruma-web/src/vite.js:24-39`).
  - **Next.js** عبر webpack. وTurbopack بمحمّلات `turbopack.rules` **فقط إن قاسها S6-W عاملة**، وإلا يُوثَّق أن Turbopack يأخذ الاحتياط وحده.
  - **Metro** (Expo وReact Native مع firebase-js) بمحوّل Metro خاص: Metro يحلّ `main` إلى `dist/index.node.cjs.js` حيث النص في `:31`.
  - **Vite 5 إلى 7** اختياري، بـ `optimizeDeps.esbuildOptions.plugins` بدل الرفض الحالي (`vite.js:91`)، **إن قاسه S6-W**.
- **لا نجاح صامت:** `vite.js:54` يعيد `null` حين لا وحدة رأى (`seen.modules === 0`)، ووضع `build` بلا كود وقت التشغيل (`:11`). ومع SSR تُخرج Vite الاعتماديات من التحويل افتراضيًا («Dependencies are 'externalized' from Vite's SSR transform module system by default»، دليل Vite SSR)، فتحمّل SvelteKit وNuxt وAstro `index.node.cjs.js` بلا تبديل. فـ:
  - وضع Storage يضبط `ssr.noExternal` لـ `@firebase/storage` و`firebase`.
  - **البناء يفشل** حين Storage مفعّل ولم تُحوَّل أي وحدة من `@firebase/storage`.
  - **البناء يفشل** إن رأى استيرادًا من `https://www.gstatic.com/firebasejs/…`: لا يمر بالمحوّل أبدًا.
- **الدليل، مصحَّحًا:** الـ 11/11 في P قيست على بناء CDN بُدّل فيه النص إلى **الشكل المسبوق** `"<host>/firebasestorage.googleapis.com"` (`test/experiments/storage-path/web/run.mjs:52`، P:112)، وهو الشكل الذي يمنعه هذا البند. **التبديل إلى النطاق وحده لم يُقَس في متصفح؛** قيس مُعدِّل `storage.host` في بناء Node وحده (P). فيقيسه S6-W.
- **الاحتياط:** `useArumaStorage(storage)` يضبط `storage.host` (واجهة داخلية في `storage.d.ts:211-212`، ليست في الأنواع العامة، ولكل نسخة `getStorage(app, bucket)` على حدة، وما سبقها يذهب إلى Google)، ولفّ `XMLHttpRequest.prototype.open` و`fetch` لنطاق Storage.
- **Flutter web: خطوة بناء (تُقاس في S6-W).** `firebase_core_web` يحقن `https://www.gstatic.com/firebasejs/$version/firebase-storage.js` بعد `initializeApp` في Dart (`firebase_core_web.dart:242`)، فسكربت قبل `flutter_bootstrap` لا يجد تطبيقًا ولا نسخة بعد. لكنه يتخطى الخدمات في `window.flutterfire_ignore_scripts` (`:118`، `:124`). فأداة في `@aruma/web` تجلب `firebase-storage.js` للإصدار المثبّت (`supportedFirebaseJsSdkVersion` = 13.1.0، `firebase_sdk_version.dart:9`)، وتبدّل النص بعلامتي الاقتباس المزدوجتين **مرة واحدة بالضبط وإلا تفشل**، وتكتبه تحت `web/`. ومقطع في `index.html` يضبط `flutterfire_ignore_scripts = ['storage']` و`window.firebase_storage = await import(…)`. والملف يستورد `firebase-app.js` من عنوان gstatic نفسه، فيتشارك نسخة التطبيق. وما لم يثبت هذا، يبقى لـ Flutter web الاحتياط وحده.

#### 16.2 Android

- **إضافة Gradle `dev.aruma.storage`** تبدّل النص الثابت `"https://firebasestorage.googleapis.com/v0"` في `<clinit>` الصنف `com.google.firebase.storage.network.NetworkRequest` إلى `"https://<aruma>/v0"`، بـ `AsmClassVisitorFactory` و`InstrumentationScope.ALL` في AGP.
  - **الدليل:** `PROD_BASE_URL` (`NetworkRequest.java:52`) يغذّي الطلبات (`:97-104`) والرابط (`StorageReferenceUri.java:38-46`) و`refFromURL` (`Util.java:86-101`) (P:325). وتحققت المراجعة بـ javap: النص في `NetworkRequest.class` وحده، والحقل `static final Uri` بلا ConstantValue، و`<clinit>` يفعل `ldc_w` ثم `Uri.parse` ثم `putstatic`، والمستعملون يقرؤونه بـ `getstatic` فلا يُضمَّن، وR8 يعمل بعد التحويل.
  - **النطاق بأحرف صغيرة:** الإضافة تكتب المضيف lowercase، لأن `Util.java:101` يقارن السلطة المحوّلة إلى lowercase بـ `baseUrl.getAuthority()` كما هو.
- **الفشل المغلق بثلاث طبقات:** واجهة instrumentation في AGP بلا خطّاف نهاية بناء (javap على gradle-api 9.4.1: `transformClassesWith` و`setAsmFramesComputationMode` و`getExcludes` وحدها)، والجرارات تُحوَّل artifact transforms مخزّنة، فعدّاد في الـ factory لا يُعتمد عليه، و`excludes` قد يُسقط الصنف. فـ:
  1. **الـ visitor يرمي** حين يزور `NetworkRequest` ولا يجد النص `ldc` مرة واحدة بالضبط.
  2. **مهمة تحقق منفصلة** على الأصناف النهائية (`ScopedArtifacts` بنطاق ALL، و`toGet` لـ CLASSES) تفشل إن لم تجد الثابت المبدَّل.
  3. **حارس وقت التشغيل:** `NetworkRequest.PROD_BASE_URL` عام، فيتحقق `aruma-redirect` عند الإقلاع من مضيفه، ويفشل مغلقًا (كما يفعل لـ Auth) إن كان مضيف Google ولا تحويل لـ Storage.
- **الاحتياط:** `aruma-redirect` يضيف `firebasestorage.googleapis.com` إلى نطاقاته بفشله المغلق (12/12 مقيسًا، P)، و`fromArumaUrl`.

#### 16.3 iOS

- **الرقعة:** `Storage.swift` يُعدَّل في `init` ليقرأ `ArumaURL` من `Info.plist` (المفتاح الذي تقرؤه `ArumaRedirect` أصلًا) بدل `host = "firebasestorage.googleapis.com"` و`scheme`. و**`var port: Int` يصير `Int?`**، `nil` حين لا منفذ صريح في `ArumaURL`: اليوم `port = 443` (`Storage.swift:287`، و`var port: Int` في `:308`، 12.19.2) يُكتب دائمًا في الرابط (`components.port = reference.storage.port`، `Internal/StorageGetDownloadURLTask.swift:62-64`، و`StorageUtils.swift:31`)، فينتج `https://<aruma>:443/v0/…` لا الشكل الموحّد. ومحلل الويب يشترط `/` بعد المضيف مباشرة (`^https?://${host}/${version}/b/`، `index.esm.js:318-320`)، وDart يرفضه كذلك.
  - **أربعة مواضع لا ثلاثة:** `Storage.swift:225` (`useEmulator`) و`:287` (`init`)، و`StorageGetDownloadURLTask.swift:64`، و`StorageUtils.swift:31`. واختبارات FirebaseStorage نفسها تثبّت المنفذ 443 (`StorageListTests.swift:66`، `:135`، `:171`).
  - **بلا `ArumaURL` (قرار المسؤول):** إن غاب المفتاح أو `Info.plist`، يبقى سلوك المكتبة الرسمي كما هو: المضيف `firebasestorage.googleapis.com`، والمنفذ 443، ومعه سطر سجل واحد صاخب. وفحص `ArumaRedirect` المغلق يمسك تلك الطلبات. فاختبارات المكتبة الرسمية تمر بلا تعديل.
  - **قبول S6-I:** اختبارات FirebaseStorage الرسمية تمر بلا `ArumaURL`، واختبار مضاف: `ArumaURL` بلا منفذ يعطي `url.port == nil`.
  - **الدليل:** `host` و`scheme` و`port` خصائص `internal` غير `@objc` تُضبط في `init` (`Storage.swift:285-287`، والتعريف `:306-308` في 12.19.2)، و`Storage.init` داخلي فلا وراثة، و`useEmulator` يفرض `http` (`:223`) فينزع iOS الرمز عن http غير loopback (`StorageTokenAuthorizer.swift:41-48`، A §2.3). وFirebaseStorage يُبنى من المصدر في SwiftPM (`Package.swift:1144`، `path: "FirebaseStorage/Sources"`) وCocoaPods (`FirebaseStorage.podspec:35-36`)، في firebase-ios-sdk 13.1.0 (P:326).
- **SwiftPM هو الطريق الأساسي:**
  - **مرآة `arumadev/firebase-ios-sdk` لا تحمل إلا commits مرقّعة، موسومة بأسماء الإصدارات الرسمية نفسها بالضبط** (`12.4.0` يشير إلى commit مرقّع، ولا وسوم غير مرقّعة)، تبنيها أداة من الوسم الرسمي ورقعة واحدة. فمتطلبات `from:` و`exact:` تُحلّ بلا تغيير.
  - **لماذا لا وسوم `<upstream>-aruma.N`:** أعاد المراجع إنتاجها بـ swift 6.1.2: `from: "12.4.0"` و`from: "12.4.0-aruma.1"` والمدى `"12.4.0"..<"13.0.0"` كلها تُحلّ إلى `12.4.0` غير المرقّع، و`exact: "12.4.0-aruma.1"` يفشل الحل إن احتاجت حزمة أخرى مدى. وFlutterFire يثبّت الرابط الرسمي بإصدار دقيق (`firebase_storage-14.0.1/ios/firebase_storage/Package.swift:22`، `exact: firebaseSdkVersion`).
  - **التسليم بـ `swift package config set-mirror`** من الرابط الرسمي إلى المرآة، فلا يتغير `Package.swift` في التطبيق ولا في FlutterFire.
  - **S6-I يقيس** أن Xcode و`flutter build ios` كليهما يحترمان المرآة. وإن لم يحترمها أحدهما يُكتب البديل: في Xcode، رابط المرآة مكان الرابط الرسمي (الوسوم بالأسماء نفسها فلا تتغير المتطلبات)، **ويقيس S6-I هل يرفض SwiftPM حزمتين بالهوية نفسها `firebase-ios-sdk`**: المرآة في التطبيق، والرابط الرسمي الذي يطلبه FlutterFire أو حزمة أخرى. فإن رفضها فلا يصلح هذا البديل إلا لتطبيق لا يجلب الرابط الرسمي من غيره، ويُكتب ذلك؛ وFlutter iOS، إن لم يحترمها، يأخذ الاحتياط وحده ويُوثَّق.
- **CocoaPods للإصدارات المجمّدة وحدها:** «Firebase will stop publishing new versions to CocoaPods in October 2026» (firebase.google.com/docs/ios/cocoapods-deprecation، حُدّثت 2026-10-22)، و«Updating Firebase on Unity or Flutter will automatically migrate» إلى SwiftPM. فمساعد `post_install` في `ArumaRedirect` يرقّع `Pods/FirebaseStorage/…/Storage.swift` ويفشل `pod install` إن لم يطابق السطر مرة واحدة، لآخر الإصدارات المنشورة هناك وحدها.
- **مستعملو Firebase.zip وxcframework (التثبيت اليدوي)** يأخذون الاحتياط وحده: ثنائي جاهز لا يُرقَّع.
- **الاحتياط:** `ArumaRedirect` يضيف النطاق (11/11، والرفع في الخلفية بالرابط المطلق، P)، و`fromArumaUrl`.

#### 16.4 Flutter

- **الجوال يرث الأصلية:** إضافة Gradle على Android، والمرآة (SwiftPM) على iOS. فيصير الرابط على Aruma ويعمل في `Image.network` (`dart:io`) بلا تحويل.
- **لكن `refFromURL` يُحلَّل في Dart على كل المنصات:** `_firebaseStorageHost = 'firebasestorage.googleapis.com'` (`firebase_storage-14.0.1/lib/src/utils.dart:24`)، وفرع `/v0` لهذا المضيف أو localhost/10.0.2.2 وحدهما (`:49-56`)، وغيرهما يسقط إلى نمط GCS فيعيد `null`، ثم `parts!` يرمي (`firebase_storage.dart:124`). فـ `refFromURL(await ref.getDownloadURL())` يرمي، وFirebase لا يرمي. **فحزمة Dart صغيرة (`aruma_storage`) فيها `fromArumaUrl`** تحوّل الرابط الموحّد إلى `gs://<bucket>/<path>` قبل `refFromURL` (S6-F).
- **الويب:** خطوة البناء في 16.1.

#### 16.5 Admin

`@aruma/admin-storage` و`adminstorage` يبنيان الرابط الموحّد بأنفسهما (البند 14).

#### 16.6 الرخصة والصيانة (Android وiOS)

- **الرخصة تسمح:** firebase-storage لـ Android منشور بـ Apache-2.0 (POM الإصدار 23.0.0: «The Apache Software License, Version 2.0»)، وfirebase-ios-sdk كذلك (A §0). وApache-2.0 §4(b) يشترط «prominent notices stating that You changed the files». **ويبقى القرار القانوني للمالك** (android-production-redirect.md:201-202، P:325)، وقد اتخذه.
- **الإشعار يبقى بعد التغليف:** استثناءات الموارد الافتراضية في AGP قد تُسقط أسماء `META-INF/NOTICE` الشائعة (لم يُتحقق منها في الوثائق)، فالإضافة تكتب مسارًا فريدًا `META-INF/aruma/firebase-storage-MODIFIED.txt`، ويتحقق S6-A من وجوده في الـ APK النهائي **وفي APK مشتق من AAB**. وفي iOS: README المرآة، وتعليق التعديل في رأس الملف المرقّع. والاثنان في `NOTICE`.
- **لا فك ثنائيات:** الإضافة تبدّل نصًا ثابتًا معروف الاسم والقيمة من المصدر المنشور (`NetworkRequest.java:52`)، ولا تقرأ كود Google لغرض آخر (قاعدة الـ brief 3).
- **الصيانة:** كل إصدار جديد من firebase-storage قد يغيّر الصنف أو النص، فيفشل البناء صراحةً لا بصمت. CI يبني تطبيق e2e على BoM مثبّت وعلى أحدث BoM أسبوعيًا (S7)، والمرآة تُبنى لكل وسم رسمي بأداة لا يدويًا.

#### 16.7 `refFromURL` عبر المنصات، لكل وضع

| الوضع | ما يُقبل | المصدر |
|---|---|---|
| الويب بعد التبديل وقت البناء | الرابط الموحّد، **وصيغ GCS عند Google** (`storage.googleapis.com/<bucket>/…`) أيضًا: `cloudStorageHost` يُحسب بمقارنة `host === DEFAULT_HOST`، والطرفان مبدّلان فتبقى صحيحة | `index.esm.js:322-324` |
| الويب بمُعدِّل `storage.host` وقت التشغيل | الرابط الموحّد وحده، وصيغ GCS مرفوضة | P (مقيس) |
| Android بعد الإضافة | الرابط الموحّد بمضيف lowercase، ورابط Google مرفوض | `Util.java:101` |
| iOS | أي مضيف، ومنه Google، ما دام `/v0/b/` أول المسار، و`fatalError` على خطأ تحليل أو bucket | `Internal/StoragePath.swift:86`، `Storage.swift:151`، `:156`، `:158` |
| Flutter | عبر `fromArumaUrl` وحده | 16.4 |

- **S6-X يمرر روابط iOS بفحص مسبق على نمط `fromArumaUrl`** (المضيف والـ bucket)، فلا يصل شيء إلى `fatalError`.
- ومصفوفة `refFromURL` عبر المنصات (Flutter منها) اختبار في S6-X (P:355): الرابط من كل منصة يُحلَّل على كل أخرى، ويعطي الـ bucket والمسار نفسيهما أو يفشل صراحةً.

### 17. الحدود وحماية الخدمة

كل حد قابل للضبط تحت `storage.limits`، كما `rtdb.limits`، ويصمد أمام **مشروع معادٍ** يختار أسماءه وقواعده (F، القيود):

| الحد | الافتراضي | لماذا |
|---|---|---|
| `object_bytes` | 5 GiB | حجم الكائن، وحد الطلب، وسقف فك الضغط (10.7) |
| `project_bytes` | 5 GiB (0 بلا حد) | الحصة شاملة staging، محجوزة لكل قطعة (6.5) |
| `min_free_bytes` | 2 GiB | محجوزة مع العدّاد الجاري للعملية عند كل خطوة حجز، وإلا 503 (6.5): القرص مشترك مع Postgres (F §4.3) |
| `empty_session_ttl` | 10 دقائق | جلسة لم تستلم بايتًا (6.5) |
| `upload_idle_ttl` | ساعة | 6.5 |
| `anonymous_staged_fraction` | 20% من `project_bytes` | بايتات جلسات المجهولين غير المنتهية (6.5) |
| `console_session_ttl` | ساعة | جلسات الرفع التي تصدرها اللوحة (15) |
| `metadata_bytes` | 8 KiB | مجموع مفاتيح الـ metadata المخصّصة وقيمها، كـ GCS (F §4.2) |
| `json_body_bytes` | 64 KiB | جسم PATCH و`start` وجزء JSON في multipart، قبل فك JSON |
| `header_field_bytes` | 1 KiB | `contentType` و`cacheControl` و`contentDisposition` و`contentEncoding` و`contentLanguage`، بلا CR/LF ولا محارف تحكم (تصير ترويسات، F §4.2) |
| اسم الكائن | 1..1024 بايت UTF-8 صالح | بلا CR ولا LF ولا NUL (Postgres لا يحفظ NUL في `text`)، وليس `.` ولا `..`، ولا يبدأ بـ `.well-known/acme-challenge/` (GCS، F §4.1). خطأ 400 |
| `upload_session_ttl` | 7 أيام | 6.3 |
| `upload_sessions_per_project` | 1,000 جلسة حية | ومعه حد لكل `uid` (50) ولكل IP (100). الجلسات بلا بايت لا تُحسب (6.5) |
| `upload_starts_per_minute_per_ip` | 60 لكل IP، عبر `internal/platform/ratelimit` | **لكل IP لا لكل مشروع:** حد لكل مشروع كان يسمح لمهاجم بحبس المستخدمين المشروعين. ومعه سقف عام مرتفع للعملية (`upload_starts_per_second`، 1,000) حماية للخادم وحده |
| `upload_requests_per_ip` | 64 طلبًا متزامنًا لكل IP | **لطلبات الرفع والكتابة وحدها** (البدء والقطع والإنهاء وmultipart وPATCH وDELETE)، **لا للتنزيل** (`alt=media`، وGET الـ metadata): صفحة واحدة فيها صور كثيرة من تطبيق واحد. كحد الاتصالات لكل IP في RTDB (`rtdb/api/api.go:86`) |

- **IPv6 يُعدّ لكل /64** في كل حد لكل IP: عنوان واحد لكل مضيف IPv6 لا يمنع شيئًا، فمضيف واحد يملك /64 كاملة.
- **الحدود لكل IP تعتمد على `trusted_proxies`:** خلف وكيل غير مضبوط تبدو كل الطلبات من IP الوكيل. فيُكتب تحذير واحد في السجل عند أول طلب يحمل `X-Forwarded-For` من نظير ليس في `trusted_proxies`.
- **الفحوص المتقاطعة في `config` عند التحميل:** `object_bytes ≤ project_bytes` حين الحصة محدودة، و`empty_session_ttl < upload_idle_ttl < upload_session_ttl`، و`console_session_ttl ≤ upload_session_ttl`، و`anonymous_staged_fraction` بين 0 و1، وكل حد موجب، و`data_dir` مسار مطلق.

| `min_bytes_per_second` | 4 KiB/s على 30 ثانية | 6.4 |
| `list_max_results` | 1,000 | 13 |
| `gc_grace` | دقيقة | 5 |

- **القواعد العدائية** محدودة بحدود ADR-021 §4، و`Inflight` واحد للعملية يشترك فيه Storage وواجهة `:test`.
- **التنزيل البطيء حد تشغيلي معروف:** عميل يقرأ بـ `min_bytes_per_second` تمامًا يمسك goroutine وملفًا مفتوحًا طوال تنزيل كائن ضخم، والتنزيلات لا تُحد لكل IP عمدًا (عملاء كثيرون خلف NAT واحد). من يحتاج سقفًا يضع أمام Aruma وكيلًا عكسيًا يحدّ الاتصالات.
- **لا معالجة على الخادم** (صور، ضغط) في v0.3، فلا قنابل إلا عبر فك الضغط، وهو محدود.
- **الأسماء تُفحص بـ fuzz** في `wire` (فك المسار من `EscapedPath()` مرة واحدة دون تقسيم قبل الفك، A §2.2؛ و`Content-Range`؛ و`X-Goog-Upload-Command`؛ وmultipart).

### 18. المراقبة

- **المقاييس** (`internal/platform/metrics`): الطلبات بالسطح (`v0`/`gcs`/`console`) والعملية والحالة، والبايتات الواردة والصادرة، والجلسات الحية وبايتات staging، ورفض الحصة والقرص، وقرارات القواعد (سماح، رفض، `ErrOverloaded`) وزمنها، وطول طابور GC وعمره، وما حذفه المسح عند الإقلاع وما بلّغ عنه المسح الدوري، وفك الضغط المقطوع، وفشل الحذف المتزامن، **والالتزامات الملتبسة** (5) وكيف حُسمت، وانتظار القفل عند الإقلاع (5.1)، والفرق الذي صالحته الحصة عند الإقلاع.
- **السجلات:** المسار والحالة كما في كل الخدمات، **بلا الاستعلام** (`middleware.go:66-71`)، فلا يُكتب `token` ولا `upload_id`. ولا `Authorization`. وتحذير عند كل إصدارة قواعد لا تُترجم (ADR-021 §7).
- **`/readyz`** لا يتغير. والقفل وقائمة المرشحين ومصالحة الحصة قبل فتح المستمع، وحذف المرشحين في الخلفية بعده.

### 19. App Check وGMPID

ترويستا `X-Firebase-AppCheck` و`X-Firebase-GMPID` **تُقبلان وتُتجاهلان** في v0.3 (قرار المالك؛ A §2.3: JS لا يرسل GMPID أبدًا، فلا تُشترطان). لا تطبيق لـ App Check، ويُوثَّق في `docs/compat`.

## البدائل المرفوضة

- **`gocloud.dev/blob/fileblob` للقرص:** بلا fsync (`fileblob.go:836-871`)، وعبور مسارات مرتين في 2026 (#3667، #3794)، و`.tmp` يتيمة (#3286)، و`rename` عبر الأجهزة (#3294)، و`IfNotExist` غير ذري (#3809) (F §1.2). سائقنا أصغر من ثمن هذه.
- **S3 عبر `s3blob` في v0.3:** اعتماد ثقيل، ويحتاج MinIO في الاختبار، ويجعل المسح عند الإقلاع قائمة LIST مدفوعة فيستلزم صفوف النية من اليوم الأول (F §1.3). والواجهة جاهزة له. ADR-023 يؤجله.
- **صفوف النية الآن:** معاملة قبل كل كتابة وعامل ثالث لا يشتري شيئًا على خادم واحد بقفل (البند 5).
- **المسح بمقارنة `mtime` بوقت الإقلاع:** `rename` يحفظ `mtime`، فالمقياس يخطئ. وبلا قفل يحذف ملفات نسخة أخرى (البند 5.1).
- **تداخل نسختين فيهما Storage أثناء الاستبدال:** كاتبان على مجلد واحد. الإيقاف ثم التشغيل بالقفل (5.1).
- **حجز الطول المعلن كله عند `start`:** طلب واحد بلا هوية يحجز حصة المشروع أيامًا (6.5).
- **رموز التنزيل نصًا في القاعدة:** ADR-010 S-15 (البند 9).
- **حذف الملف الجديد على أي خطأ في `commitObject`:** يضيّع النسختين بعد التزام ملتبس (البند 5).
- **ملف مؤقت ثم `rename` فوق المفتاح النهائي:** المفتاح الجديد لكل generation يجعل الكتابة فوق ملف قائم مستحيلة أصلًا، والقارئ القديم يكمل على ملفه.
- **مفتاح ملف من اسم الكائن:** عبور المسارات وتصادم الحالة (F §4.1).
- **تقليد المحاكي في الإزاحات** (يتجاهلها): إعادة المحاولة تفسد الملف (F §3.1).
- **تقييم القواعد عند `start`:** يرفض ما يقبله Firebase حين لا يُعلن الحجم (Android). والإنهاء هو الحكم (البند 8).
- **قبول Admin بلا مصادقة (وضع المحاكي) أو من شبكة «موثوقة»:** أكثر تساهلًا من Firebase (E §9).
- **`option.WithEndpoint` على `firebase.NewApp` في Go:** يكسر FCM (مقيس) وFirestore (P:283).
- **CORS مفتوح على `alt=media`:** أكثر تساهلًا من bucket بلا إعداد (البند 11).
- **رفع `sandbox` عن PDF أو الوسائط الآن:** لا سابقة مقيسة (GitHub يخدمهما octet-stream)، والقرار بعد المصفوفة (البند 10.2).
- **الاعتماد على `Content-Disposition: attachment` حماية:** العميل يملك الحقل، والإنتاج يخزّن `inline` افتراضيًا (البند 10.4).
- **نطاق تنزيل منفصل افتراضيًا:** يخالف ADR-008، والرابط يبنيه العميل من مضيف المكتبة. خيار لاحق (ADR-023).
- **رمز تنزيل من العميل، أو اتحاد الرموز كالمحاكي:** زرع رمز معروف، ولا إبطال (البند 9).
- **`pageToken` نصًا مكشوفًا كالمحاكي:** يعبر بين المشاريع، ويعيد من الأول عند الخطأ (البند 13).
- **إملاء رابط الرفع من `Host`:** يخطئ خلف وكيل، وقطع iOS في الخلفية تتبعه حرفيًا (البند 6.1).
- **iOS: swizzling أو KVC على `host`:** خصائص Swift `internal` غير `@objc`، فلا يصل إليها runtime Objective-C. **و`useEmulator`:** يفرض http (`Storage.swift:223`). **وترقيع الثنائي:** FirebaseStorage يُبنى من المصدر، فلا ثنائي يُرقَّع، وفك الثنائيات ممنوع أصلًا.
- **التحويل وقت التشغيل وحده:** يتسرّب الرابط إلى Google من `<img>` و`dart:io` وجلسات الخلفية (P، جدول التسريب). يبقى احتياطًا فقط.
- **وسوم المرآة `<upstream>-aruma.N`:** لا تختارها متطلبات SwiftPM العادية، ويفشل `exact:` مع أي مدى آخر (أعادها المراجع، 16.3).
- **CocoaPods طريقًا أساسيًا لـ iOS:** Firebase توقف عن النشر هناك في تشرين الأول 2026، وFlutter ينتقل إلى SwiftPM تلقائيًا (16.3).
- **Vite 8 وحده للويب:** «وقت البناء في كل منصة» كان سيصدق على Vite وحده، وwebpack وesbuild وMetro يأخذون الاحتياط (16.1).
- **العدّاد في الـ factory لفشل Android المغلق:** artifact transforms مخزّنة، ولا خطّاف نهاية في AGP (16.2).

## العواقب

- **Storage يقف على `core` واحدة** وواجهتين، ويُبنى بمهام متوازية بواجهات ثابتة (storage-layout.md).
- **قرص محلي وخادم واحد** في v0.3. S3 وتعدد الخوادم يحتاجان ADR جديدًا مع صفوف النية.
- **ترويسات الخدمة الآمنة على كل بايت** يخرج، بتجاوز لكل مسار للترويستين العامتين. **PDF يُنزَّل ولا يُعرض** في المتصفح، فرق موثّق.
- **نشر نسخة فيها Storage إيقاف ثم تشغيل:** القفل على `data_dir` يمنع تداخل نسختين، فانقطاع قصير عند كل استبدال (5.1).
- **قرارات مؤقتة بلا دليل إنتاج**، كلها في موضع واحد من الكود ومذكورة في `docs/compat/storage-v0.3.md` (ADR-023 §3)، فإن ظهر دليل يتغير الموضع لا التصميم:
  1. list على أبناء البادئة (`UnknownSegment`) (البند 8).
  2. `request.method` باسم الطريقة، و`request.params` خريطة فارغة (البند 8).
  3. `request.resource` = `null` في `get` و`list` و`delete` (البند 8).
  4. `crc32c` في `/v0` بصيغة base64 (البند 12).
  5. `"metadata": null` في PATCH يمسح الـ metadata المخصّصة (كالمحاكي ودلالة GCS؛ وطلب iOS الحقيقي يُسجَّل في S6-I، A §8.1).
  6. الرمز لا يفتح الـ metadata بلا `alt=media` (البند 9).
  7. **`token` موجود ولا يطابق: 403 بلا قواعد** (البند 9).
  8. **`md5Hash` من العميل على `/v0` يُتحقق منه، 400 و`final`** (البند 6.1).
  9. `Cache-Control` الافتراضي `private, max-age=0` على رابط الرمز (البند 10.5).
  10. الحصة: 402 على `/v0`، و403 `quotaExceeded` على GCS (البند 6.5).
  11. نص رسالة 403 «Permission denied.»، وجسم الإنهاء المكرر الناجح (البند 6.1).
  12. الإزاحة الخاطئة 400 مع `active`، والحجم المخالف للمعلن 400 مع `final` (البند 6.1).
  13. `Content-Disposition` عند التنزيل لكائن بلا قيمة: `attachment` (البند 10.4)، ومجموعة الهروب في الافتراضي المخزّن (حالة PY-RR واحدة، البند 10.4).
  14. `Authorization` على القطع يُتجاهل (البند 7).
  15. CORS `*` على ردود الـ API والـ preflight (البند 11).
  16. `Range` غير صحيح الصيغة (وحدة أخرى، أو `a-b` و`b < a`، أو رقم ناقص) يُتجاهل فيُرد 200 كاملًا، و`bytes=-0` على كائن غير فارغ 416 (البند 10.3؛ RFC 9110 §14.2 يسمح بالتجاهل، والإنتاج غير مقيس). في `wire.ParseRange` (S2-B).
  17. **صيغ السلك أشد من المقيس حيث لم يُقس الإنتاج** (S2-B): `multipart/related` جزءان بالضبط، أولهما `application/json`، وبلا `Content-Transfer-Encoding` غير `7bit`/`8bit`/`binary`، والحد حسب RFC 2046 (70 حرفًا)؛ و`X-Goog-Upload-Command` بأحرف صغيرة كما ترسلها المكتبات الثلاث؛ و`Content-Range` بأشكاله الستة وحدها. وكل خروج عنها 400. وكل الحالات في `wire` (`multipart.go` و`upload.go`).
  18. **خدمة البايتات حيث سكت هذا البند أو لم يُقس الإنتاج** (S2-F، كلها في `media`):
      - `Cache-Control` على GCS بلا قيمة مخزّنة `private, max-age=0`، كما يرسله GCS لكائن غير عام (البند 10.5).
      - على تنزيل قررته القواعد يُضاف `private` حين يغيب، لا يُستبدل `public` وحده: `must-revalidate` أيضًا يسمح لمخبأ مشترك بخزن رد طلب فيه `Authorization` (RFC 9111 §3.5).
      - تنزيل اللوحة `no-store`، ويبقى على `CORP: same-origin` العامة: يُطلب بكعكة الجلسة، ولا سبب لأصل آخر أن يضمّنه.
      - `attachment` القسري (PDF واللوحة) يُبقي معاملات القيمة المخزّنة (`filename`)، ويأخذ آخر مقطع من الاسم إن غابت.
      - 304 على `If-None-Match` (مقارنة ضعيفة) ثم `If-Modified-Since`، **قبل `Range`** (RFC 9110 §13.2.2)، فنسخة مخبأة 304 ولو كان النطاق 416. و`If-Range` بتاريخ يُرد 200 كاملًا (البند 10.3 يقارن بالـ ETag وحده).
      - `Content-Type` مخزّن فيه فاصلة، أو لا يقبله `mime.ParseMediaType` نوعًا/نوعًا فرعيًا، يُخدم `application/octet-stream`: Fetch يأخذ آخر نوع صالح في القائمة، فـ `text/plain,application/pdf` PDF عند المتصفح، وكان سيفلت من `attachment` القسري.
      - `gzip;q=0` صريحًا يغلب `*` في `Accept-Encoding` (RFC 9110 §12.5.3).
      - جسم 416 بشكل السطح: «The requested range cannot be satisfied.»، وفي GCS `reason` = `requestedRangeNotSatisfiable`.
      - الرد المفكوك بلا `Content-Length` ومعه `Accept-Ranges: none`، و`Vary: Accept-Encoding` على كل كائن مخزّن gzip، و`ETag` كما هو. وكائن مخزّن gzip ليس gzip يُخدم كما خُزّن بترميزه، لا 500.
      - مهلة الكتابة نافذة 30 ثانية تُمدّ كلما حملت `min_bytes_per_second × 30` بايتًا، فتنزيل أطول من دقيقة يكمل، وعميل لا يقرأ يُقطع (البند 17). و`Serve` يمدّها بنفسه. **وكل فشل بعد سطر الحالة يقطعه `Serve` نفسه:** يبلّغ `Limits.Cut` ثم `panic(http.ErrAbortHandler)`، فلا يُختم رد مقطوع كأنه كامل ولو عاد المعالج عاديًا. ولا يعيد خطأ إلا قبل أول بايت، وترويسات `w` كما كانت.
- **ما يُقاس في S6 قبل اعتماده** (وإلا يُكتب البديل): التبديل إلى النطاق وحده في متصفح حقيقي، وTurbopack، وVite 5 إلى 7، وخطوة بناء Flutter web، واحترام Xcode و`flutter build ios` للمرآة، وبقاء إشعار `META-INF/aruma/` في APK وفي APK من AAB. **وفي S2 وS4:** كلفة fsync على القرص المستهدف، و`universeDomain` على مفتاح فيه `universe_domain`.
- **قطع العلاقة مع Google في الإنتاج يحتاج تعديل كود Google** على Android وiOS، بإذن الرخصة وإشعاراتها، وصيانة تفشل بصوت عالٍ مع كل إصدار جديد. ومستعملو xcframework اليدوي وما لا يمر بالمحوّل يأخذون الاحتياط وحده.
- **`firestore.get()` في قواعد Storage يرفض حتى v0.4**، وv0.4 يغيّر سطرًا في `internal/app`.
