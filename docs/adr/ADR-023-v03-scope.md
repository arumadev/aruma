# ADR-023: نطاق الإصدار 0.3: لغة القواعد وCloud Storage for Firebase

- الحالة: مقترح (ينتظر المراجعة العدائية)
- التاريخ: 2026-10-24
- المرحلة: S0 من الإصدار 0.3
- المراجع: ADR-021 (محرك القواعد)، ADR-022 (معمارية Storage)، [storage-layout.md](../research/storage-layout.md)، [rules-layout.md](../research/rules-layout.md)، وأبحاث Storage A إلى F و[storage-production-path.md](../research/storage-production-path.md) (الاختصارات كما في ADR-022)، وخطة v0.3/v0.4 التي اعتمدها المالك (`.cache/briefs/plan-v03-v04.html`، الأقسام 1 إلى 4)، وقرارات المالك في 2026-10-23 و2026-10-24.

## السياق

ADR-020 حدّ 0.2 بما يحتاجه تطبيق RTDB حقيقي، وترك النادر. ونحتاج الحد نفسه لـ 0.3، لثلاثة أسباب:

- **سطح GCS واسع** (ACL وIAM وإصدارات وcompose وإشعارات وروابط موقّعة)، ومكتبات Firebase لا تستعمل منه إلا قليلًا، وAdmin SDKs تستعمل أكثر قليلًا (B §10).
- **قرارات المالك تُخرج أشياء صراحةً:** الروابط الموقّعة، وقواعد Firestore المسمّاة، وأدوات النقل من Firebase إلى Aruma بعد الإطلاق؛ وترويسات App Check تُتجاهل؛ ولا تسجيلات إنتاج لـ Storage.
- **ما لم يحسمه دليل إنتاج** يجب أن يُكتب قبل البناء، لا أن يُكتشف بعده (القاعدة: لا نكون أكثر تساهلًا من Firebase، وكل فرق يُوثَّق).

## القرار

### 1. ما يشحنه 0.3

**القواعد (ADR-021):**

- محرك اللغة في `internal/platform/rules` لـ `firebase.storage` و`cloud.firestore` معًا (Firestore يستهلكه في v0.4).
- تخزين الـ rulesets والإصدارات في `tenancy` (`control.rulesets` و`control.rules_releases`)، و`CompiledRelease` بذاكرة LRU بالوزن.
- واجهة firebaserules v1 كاملة لأداة `firebase`: `:test` و`rulesets` (إنشاء، وقراءة، وقائمة، وحذف) و`releases` (إنشاء، وتحديث، وقائمة) (C §1.5، R1 إلى R8).
- قاعدة Storage الافتراضية المقفلة لكل مشروع (`allow read, write: if false;`).
- `firestore.get()`/`firestore.exists()` في قواعد Storage: تُترجم، وتُقيَّم خطأً (رفضًا) حتى v0.4.

**Storage: bucket افتراضي واحد لكل مشروع** (`<projectId>.<project_domain>`، ADR-022 §2)، على قرص محلي وخادم واحد.

**واجهة العملاء `/v0`** (ومعها `/firebasestorage.googleapis.com/v0` احتياطًا):

| العملية | ما يُدعم |
|---|---|
| getMetadata | كاملة، ومعها توليد رمز التنزيل عند أول قراءة مصرّح بها لكائن بلا رمز |
| التنزيل `alt=media` | بالقواعد أو بالرمز، وRange بنطاق واحد، وفك ضغط gzip تدفقًا، وترويسات الخدمة الآمنة (ADR-022 §10) |
| getDownloadURL | عبر getMetadata والرابط الموحّد |
| updateMetadata | `PATCH`، و`PUT` مع `X-HTTP-Method-Override: PATCH` (Android) |
| delete | كاملة |
| list / listAll | `prefix` و`delimiter` و`maxResults` و`pageToken` معتم موقّع |
| الرفع | media، وmultipart، وresumable (`start` على `/o` و`/o/{object}`، وقطع `POST`/`PUT`، و`query`، و`cancel`، و`finalize`)، بحجم معلن أو بلا إعلان |
| رموز التنزيل لـ Admin | `?create_token=true` و`?delete_token=` |

**واجهة GCS JSON لـ Admin SDKs** (تحت `/storage/v1` و`/upload/storage/v1` و`/download/storage/v1`، بـ `Bearer` حساب خدمة إلزامي): `buckets.get`، و`buckets.patch` لحقل `cors` وحده، و`objects.get/list/patch/delete`، و`rewriteTo` في طلب واحد (`copy` و`move` في Node، و`CopierFrom` في Go)، والرفع media وmultipart وresumable (308 لـ Node، و200 مع `X-Http-Status-Code-Override` لـ Go)، والوسائط JSON، والشروط المسبقة `if(Meta)generation(Not)Match`، والتحقق من `crc32c`/`md5Hash`.

**الإدارة:**

- `firebase deploy --only storage` و`firebase init storage` بمتغيرات الأداة على أصل Aruma (ADR-022 §15)، ومعها `defaultBucket` وService Usage.
- اللوحة: متصفح الملفات، ومحرر قواعد Storage، وإعداد CORS للـ bucket، والاستعمال والحصة.
- إعداد CORS للـ bucket بدلالة GCS، يحكم `alt=media` وحده، والافتراضي لا شيء.

**مسار الإنتاج لكل منصة** (ADR-022 §16)، بالرابط الموحّد `https://<aruma>/v0/b/<bucket>/o/<path>?alt=media&token=…`:

| المنصة | الأساسي (وقت البناء) | الاحتياط (وقت التشغيل) |
|---|---|---|
| الويب (Vite 8) | `@aruma/web/vite` يبدّل `DEFAULT_HOST` في `@firebase/storage` | `useArumaStorage(storage)` ولفّ XHR و`fetch` |
| الويب بلا Vite، وFlutter web | — | الاحتياط نفسه |
| Android | إضافة Gradle `dev.aruma.storage` تبدّل `PROD_BASE_URL` | `aruma-redirect` لنطاق Storage |
| iOS | رقعة `Storage.swift` تقرأ `ArumaURL`: مساعد Podfile، ومرآة SwiftPM | `ArumaRedirect` لنطاق Storage |
| Flutter (الجوال) | الإضافة والمساعد الأصليان | كالأصلية |
| Admin Node | `@aruma/admin-storage` (ومعه `getDownloadURL`) | — |
| Admin Go | `adminstorage` (عميل منفصل بـ `WithEndpoint` و`WithJSONReads`) | — |

**التحقق** (S7): مجموعة اختبارات Storage في firebase-js-sdk على Aruma عبر `test/compat/firebase-js`، واختبارات المطابقة في firebase-tools غير الموسومة `emulatorOnly` وغير المعتمدة على قواعدها (E §0: CT)، وإعادة تسجيلات `test/oracle/storage/emulator` على Aruma مع تصنيف كل فرق، وe2e على Android وiOS والويب بحارس شبكة يرفض أي طلب إلى Google، ومصفوفة المتصفحات (ADR-022 §10.6)، وعتبة أوراكل القواعد (98%، ADR-021 §6).

### 2. ما يُؤجَّل

| البند | لماذا | متى |
|---|---|---|
| **الروابط الموقّعة** (`getSignedUrl`، V4) وسياسة POST | قرار المالك. وهي XML API (B §4.7) | بعد الإطلاق |
| **XML API** (`/<bucket>/<object>`) | يتعارض مع اللوحة على `/` (ADR-008)، و`adminstorage` يفرض `WithJSONReads()` | مع الروابط الموقّعة |
| **S3 وغيره من المخازن** | الواجهة جاهزة، وصفوف النية تأتي معه (ADR-022 §4، §5) | ADR جديد |
| **تعدد الخوادم** | كاتب واحد (ADR-019) | ADR جديد |
| **أكثر من bucket لكل مشروع**، وإنشاء الـ buckets وحذفها | bucket افتراضي واحد يكفي تطبيقات Firebase المعتادة | لاحقًا |
| **`firestore.get()` الفعلي في قواعد Storage** | Firestore في v0.4 | v0.4 |
| **قواعد Firestore المسمّاة وقواعد البيانات المسمّاة** | قرار المالك | بعد الإطلاق |
| **أدوات النقل من Firebase إلى Aruma** | قرار المالك | بعد الإطلاق |
| **تطبيق App Check** | قرار المالك: الترويسة تُقبل وتُتجاهل | لاحقًا |
| **ACL و`makePublic` وIAM والإشعارات وHMAC وCSEK/KMS** | لا تستعملها مكتبات Firebase. تُرد 501 بشكل GCS | لاحقًا |
| **الإصدارات وsoft delete و`restore` و`compose` و`moveTo`** | كذلك (B §10.3) | لاحقًا |
| **`rewriteToken` متعدد الطلبات**، و`matchGlob`، و`fields`/`projection` | نرد `done:true` دائمًا، و`matchGlob` 400 صريح | لاحقًا |
| **قواعد دورة الحياة (lifecycle) والاحتفاظ (retention)** | — | لاحقًا |
| **مشغّلات Functions وأحداث Storage** | لا Functions في Aruma | مع Functions |
| **نطاق تنزيل منفصل** (`storage.download_origin`) | ADR-008. والترويسات تحمي اليوم (ADR-022 §10) | تحسين اختياري |
| **عرض PDF في المتصفح** (بلا `sandbox`) | ينتظر مصفوفة المتصفحات وقرارًا (ADR-022 §10.2) | 0.3.x |
| **تقييم القواعد عند `start`** | يحتاج دليل إنتاج ومعرفة كل مدخل (ADR-022 §8) | بدليل |
| **حد «كتابة واحدة في الثانية لكل كائن»** و429 GCS | ليس صلاحية، وغيابه لا يجعلنا أكثر تساهلًا (F §4.3) | لا خطة |
| **معالجة الصور، وExtensions (resize)** | — | لا خطة |
| **Admin SDKs لـ Python وJava و.NET، وUnity وC++** | لم تُقَس (P، «ما لم يُقَس»). وPython Admin غير مدعوم أصلًا في RTDB | لاحقًا |
| **`Bearer <idToken>` على `/v0`** | غير مقيس في الإنتاج، والمكتبات ترسل `Firebase <idToken>` (E §7) | بدليل |

### 3. الفروق الموثّقة

كلها تُكتب في `docs/compat/storage-v0.3.md` (S7)، كل منها بمصدره وتصنيفه: **قيد** (أشد من Firebase عمدًا)، أو **مؤقت** (بلا دليل إنتاج، وقد نكون مختلفين)، أو **أصح من المحاكي** (نتبع الإنتاج أو الوثائق حيث يخطئ المحاكي).

**قيود (أشد من Firebase عمدًا):**

1. **PDF يُنزَّل ولا يُعرض** في المتصفح (`sandbox` و`attachment` قسرًا)، وكل الأنواع تحت `sandbox`، فـ HTML وSVG لا ينفّذان سكربتًا تصفحًا مباشرًا (ADR-022 §10.2).
2. طلب تنزيل من Service Worker أو worker يُرفض 403 (ADR-022 §10.1).
3. حدود Aruma: الحجم الأقصى للكائن، والحصة لكل مشروع شاملة staging، والمساحة الحرة الدنيا، وحدود الجلسات والمعدل، وأدنى معدل للطلب (ADR-022 §17). وحدود محرك القواعد (ADR-021 §4).
4. اسم كائن فيه NUL يُرفض 400 (Postgres، F §4.1)، وGCS قد يقبله.
5. `maxResults` فوق 1000 يُقص إلى 1000، و`pageToken` غير صالح 400.
6. لا تقييم مبكر عند `start`: الرفض يأتي عند الإنهاء، بعد نقل البايتات (محدود بالحصة لكل قطعة).
7. Admin Node يحتاج `@aruma/admin-storage`، وAdmin Go يحتاج `adminstorage` بـ `WithJSONReads()`. وضع المحاكي (`STORAGE_EMULATOR_HOST`) مرفوض في الإنتاج.
8. ترويسات App Check وGMPID تُتجاهل.
9. النشر فوري (Firebase: «several minutes»)، كما في ADR-021 §7.
10. لا إصدارات: `?generation=` يُقبل إن طابق الإصدار الحالي، وغيره 404.

**مؤقت (بلا دليل إنتاج):**

11. **list يُقيَّم على أبناء البادئة** (`UnknownSegment`)، كما يقرر محرك المحاكي، لا على المسار نفسه كما في Test API (ADR-022 §8).
12. **`request.method`** مربوط باسم الطريقة، و**`request.params`** خريطة فارغة (مرجع firebaserules؛ المحاكي لا يمررهما).
13. `request.resource` في `get` و`list` و`delete`: `null` (المحاكي)؛ Test API تجعله غير معرّف.
14. `crc32c` في `/v0` بصيغة GCS (base64)، والمحاكي يكتبه عددًا عشريًا.
15. **`"metadata": null` في PATCH يمسح الـ metadata المخصّصة كلها** (المحاكي ودلالة GCS). وiOS قد يرسله حين يحدّث كائنًا جديدًا (A §8.1): طلب iOS الحقيقي يُسجَّل في S6.
16. الرمز لا يفتح الـ metadata بلا `alt=media` (المحاكي يفتحها).
17. `Cache-Control` الافتراضي على رابط الرمز `private, max-age=0`.
18. نص رسالة 403 «Permission denied.»، وجسم الإنهاء المكرر الناجح (نعيد الـ metadata).
19. على الإزاحة الخاطئة في الرفع المتقطع: 400 مع الحالة والحجم المستلَم؛ والإنهاء بحجم لا يطابق المعلن: 400.
20. حصة GCS: 403 `quotaExceeded`.
21. multipart باسم في `metadata.name` وحده يُقبل (المحاكي يرفضه).
22. CORS على ردود الـ API: `*`، والـ preflight 204 بالطرق والترويسات التي يرسلها العملاء.
23. `Content-Disposition` عند التنزيل لكائن بلا قيمة (رُفع عبر GCS): `attachment; filename*=utf-8''<اسم>`.

**أصح من المحاكي (نتبع الإنتاج أو الوثائق):**

24. رموز المستخدمين تُتحقق توقيعًا وصلاحية، و`uid` من `sub`، ولا كلمة سحرية (`Bearer owner`) (E §7).
25. bucket مجهول 404 بشكل الإنتاج (المحاكي ينشئه ضمنيًا ويرد 403).
26. رموز التنزيل: لا يزرعها العميل، واستبدالها أو حذفها من Admin يُبطل الرابط القديم (المحاكي: اتحاد) (E §5).
27. الإزاحات في الرفع المتقطع تُفحص (المحاكي يتجاهلها).
28. بروتوكول GCS المتقطع كاملًا (308 والـ override)، والشروط المسبقة 412، و`bucket.exists()`، وPATCH وrewrite تحت `/storage/v1` (المحاكي: 501 أو يتجاهلها) (E §8).
29. `contentDisposition` الافتراضي يُخزَّن `inline; filename*=utf-8''<اسم>` كما في Storage منشور (المحاكي: `inline`).
30. `etag` يتغير مع PATCH، و`generation` بالميكروثانية، و`x-goog-metageneration` بالتهجئة الصحيحة.
31. لا تقييم مزدوج للقواعد (ADR-021 §5.4).

**خصائص Test API** التي لا نحاكيها (ADR-021 §6) تبقى في `docs/compat/rules-v0.3.md`.

## البدائل المرفوضة

- **سطح GCS كامل في 0.3:** أسابيع لميزات لا تستعملها مكتبات Firebase، وكل ميزة سطح هجوم جديد.
- **تأجيل GCS كله وترك Admin على وضع المحاكي:** وضع المحاكي بلا مصادقة، وقبوله أكثر تساهلًا من Firebase (E §9).
- **تأجيل مسار الإنتاج للجوال إلى ما بعد 0.3:** رابط التنزيل صلاحية تتسرّب إلى Google افتراضيًا (P). شحن Storage دونه شحن تسريب.
- **S3 في 0.3:** ADR-022 §4.
- **عدّ قرارات المحاكي إنتاجًا:** المحاكي يخطئ في الأمان والإزاحات (D §6)، وبعض اختبارات المطابقة تعتمد على قواعد لا تُنشر على الإنتاج (E §0: CT-r).

## العواقب

- **0.3 يكفي تطبيق Firebase معتادًا** يرفع وينزّل ويعرض ويسرد ويحذف ملفات من الويب وAndroid وiOS وFlutter، ويديره Admin من Node وGo، وينشر قواعده بأداة `firebase`.
- **التطبيق الذي يعتمد على ما أُجّل** (روابط موقّعة، إصدارات، buckets متعددة، Functions) يعرف ذلك من `docs/compat/storage-v0.3.md` قبل أن يبدأ.
- **كل بند «مؤقت» موضع واحد في الكود،** يتغير إن ظهر دليل إنتاج، دون تغيير التصميم.
- **الإطلاق يحتاج** أن تمر S7: مجموعات Google، وe2e بلا طلب واحد إلى Google، ومصفوفة المتصفحات، وأوراكل القواعد.
