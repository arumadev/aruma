# ADR-023: نطاق الإصدار 0.3: لغة القواعد وCloud Storage for Firebase

- الحالة: معتمد
- التاريخ: 2026-10-24 (ومراجعات ADR-022 الثلاث أُدخلت هنا أيضًا)
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
| الويب | محوّل `unplugin` واحد في `@aruma/web` لـ Vite وRollup وwebpack وRspack وesbuild (وNext.js عبر webpack) يبدّل `DEFAULT_HOST`، ومعه `ssr.noExternal`، ويفشل البناء إن لم يُحوَّل `@firebase/storage` أو رأى استيرادًا من gstatic | `useArumaStorage(storage)` ولفّ XHR و`fetch` |
| Expo / React Native (firebase-js) | محوّل Metro | كالويب |
| Turbopack، وVite 5 إلى 7 | محمّلات Turbopack، و`optimizeDeps.esbuildOptions` لـ Vite القديم: **إن قاسها S6-W**، وإلا الاحتياط وحده | كالويب |
| Flutter web | خطوة بناء: `firebase-storage.js` المثبّت مبدَّلًا مرة واحدة تحت `web/`، و`flutterfire_ignore_scripts` (**تُقاس في S6-W**) | كالويب |
| Android | إضافة Gradle `dev.aruma.storage` تبدّل `PROD_BASE_URL` (بمضيف lowercase)، وتفشل مغلقة بثلاث طبقات | `aruma-redirect` لنطاق Storage، ويتحقق من `PROD_BASE_URL` عند الإقلاع |
| iOS (SwiftPM، الأساسي) | مرآة `arumadev/firebase-ios-sdk` بوسوم بأسماء الإصدارات الرسمية على commits مرقّعة (`ArumaURL`، و`port` اختياري)، بـ `swift package config set-mirror` | `ArumaRedirect` لنطاق Storage |
| iOS (CocoaPods) | مساعد `post_install` للإصدارات المجمّدة وحدها (Firebase أوقف النشر هناك في تشرين الأول 2026) | كالسابق |
| iOS (xcframework يدوي) | — | الاحتياط وحده |
| Flutter (الجوال) | الإضافة والمرآة الأصليتان، وحزمة Dart `aruma_storage` بـ `fromArumaUrl` لـ `refFromURL` | كالأصلية |
| Admin Node | `@aruma/admin-storage` (ومعه `getDownloadURL`) | — |
| Admin Go | `adminstorage` (وحدة مستقلة، عميل منفصل بـ `WithEndpoint` و`WithJSONReads`) | — |

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
| **نشر بلا انقطاع لنسخة فيها Storage** | كاتب واحد بقفل على `data_dir`، فالاستبدال إيقاف ثم تشغيل (ADR-022 §5.1) | مع تعدد الخوادم |
| **`data_dir` على NFS أو نظام ملفات مشترك** | `flock` لا يُعتمد عليه هناك (ADR-022 §5.1) | مع S3 |
| **تبديل وقت البناء لمستعملي xcframework اليدوي** | ثنائي جاهز لا يُرقَّع (ADR-022 §16.3) | لا خطة |

### 3. الفروق الموثّقة

كلها تُكتب في `docs/compat/storage-v0.3.md` (S7)، كل منها بمصدره وتصنيفه: **قيد** (أشد من Firebase عمدًا)، أو **مؤقت** (بلا دليل إنتاج، وقد نكون مختلفين)، أو **أصح من المحاكي** (نتبع الإنتاج أو الوثائق حيث يخطئ المحاكي).

**قيود (أشد من Firebase عمدًا):**

1. **PDF يُنزَّل ولا يُعرض** في المتصفح (`sandbox` و`attachment` قسرًا)، وكل الأنواع تحت `sandbox`، فـ HTML وSVG لا ينفّذان سكربتًا تصفحًا مباشرًا (ADR-022 §10.2).
2. طلب تنزيل من Service Worker أو worker يُرفض 403، وكذلك تحميل ملف كسكربت أو أسلوب من أصل اللوحة نفسه (`Sec-Fetch-Site: same-origin`) (ADR-022 §10.1).
3. حدود Aruma: الحجم الأقصى للكائن، والحصة لكل مشروع شاملة staging، والمساحة الحرة الدنيا محجوزة، وحدود الجلسات، ومعدل البدء لكل IP، والطلبات المتزامنة لكل IP للرفع والكتابة وحدها (IPv6 لكل /64)، وأدنى معدل للطلب، والجلسة الفارغة تنتهي بعد 10 دقائق، والخاملة تُحرَّر حجوزاتها بعد ساعة، وبايتات جلسات المجهولين غير المنتهية حتى 20% من حصة المشروع، وجلسات رفع اللوحة بعمر ساعة وحجم معلن إلزامي (ADR-022 §6.5، §15، §17). وحدود محرك القواعد (ADR-021 §4).
4. اسم كائن فيه NUL يُرفض 400 (Postgres، F §4.1)، وGCS قد يقبله.
5. `maxResults` فوق 1000 يُقص إلى 1000، و`pageToken` غير صالح 400.
6. لا تقييم مبكر عند `start`: الرفض يأتي عند الإنهاء، بعد نقل البايتات (محدود بالحصة لكل قطعة).
7. Admin Node يحتاج `@aruma/admin-storage`، وAdmin Go يحتاج `adminstorage` بـ `WithJSONReads()`. وضع المحاكي (`STORAGE_EMULATOR_HOST`) مرفوض في الإنتاج.
8. ترويسات App Check وGMPID تُتجاهل.
9. النشر فوري (Firebase: «several minutes»)، كما في ADR-021 §7.
10. لا إصدارات: `?generation=` يُقبل إن طابق الإصدار الحالي، وغيره 404.
11. مستخدم حُذف أو عُطّل أو أُبطلت رموزه بعد `start` لا يُكمل رفعه: 401 مع `final` عند الإنهاء (ADR-022 §7).
12. نشر نسخة فيها Storage إيقاف ثم تشغيل (وفي Kubernetes `strategy: Recreate`، فـ `RollingUpdate` يعلق على القفل)، و`data_dir` محلي ومربوط بقاعدته (ADR-022 §5.1).

**مؤقت (بلا دليل إنتاج):**

13. **list يُقيَّم على أبناء البادئة** (`UnknownSegment`)، كما يقرر محرك المحاكي، لا على المسار نفسه كما في Test API (ADR-022 §8).
14. **`request.method`** مربوط باسم الطريقة، و**`request.params`** خريطة فارغة (مرجع firebaserules؛ المحاكي لا يمررهما، وحالات Pyric تمرر `method` صراحةً فلا تثبت ربط الإنتاج له).
15. `request.resource` في `get` و`list` و`delete`: `null` (المحاكي)؛ Test API تجعله غير معرّف.
16. `crc32c` في `/v0` بصيغة GCS (base64)، والمحاكي يكتبه عددًا عشريًا.
17. **`"metadata": null` في PATCH يمسح الـ metadata المخصّصة كلها** (المحاكي ودلالة GCS). وiOS قد يرسله حين يحدّث كائنًا جديدًا (A §8.1): طلب iOS الحقيقي يُسجَّل في S6-I.
18. الرمز لا يفتح الـ metadata بلا `alt=media` (المحاكي يفتحها).
19. **`token` موجود ولا يطابق: 403 بلا تقييم للقواعد** (الأشد؛ ملاحظة Pyric للإبطال أُخذت تحت قواعد مجهولة).
20. **`md5Hash` يرسله العميل على `/v0` ويختلف عن المحسوب: 400 ولا كائن، ومع `final` في المتقطع.**
21. `Cache-Control` الافتراضي على رابط الرمز `private, max-age=0`.
22. نص رسالة 403 «Permission denied.»، وجسم الإنهاء المكرر الناجح (نعيد الـ metadata)، و`query` على جلسة انتهت برفض يعيد الرفض المحفوظ.
23. على الإزاحة الخاطئة في الرفع المتقطع: 400 مع `active` والحجم المستلَم؛ وكل رفض نهائي آخر (402، والحجم المخالف للمعلن، و413، و401) مع `final`.
24. `Authorization` على قطع الرفع يُتجاهل: رابط الرفع هو الصلاحية.
25. الحصة: 402 على `/v0`، و403 `quotaExceeded` على GCS.
26. multipart باسم في `metadata.name` وحده يُقبل (المحاكي يرفضه).
27. CORS على ردود الـ API: `*`، والـ preflight 204 بالطرق والترويسات التي يرسلها العملاء.
28. `Content-Disposition` عند التنزيل لكائن بلا قيمة (رُفع عبر GCS): `attachment; filename*=utf-8''<اسم>`. ومجموعة الهروب في الافتراضي المخزّن مأخوذة من حالة منشورة واحدة.
29. ترويسة `ETag` على الوسائط من `generation` وحده (ثابتة للمحتوى)، وحقل `etag` في JSON يتغير مع PATCH.

**أصح من المحاكي (نتبع الإنتاج أو الوثائق):**

30. رموز المستخدمين تُتحقق توقيعًا وصلاحية، و`uid` من `sub`، ولا كلمة سحرية (`Bearer owner`) (E §7).
31. bucket مجهول 404 بشكل الإنتاج (المحاكي ينشئه ضمنيًا ويرد 403).
32. رموز التنزيل: لا يزرعها العميل، واستبدالها أو حذفها من Admin يُبطل الرابط القديم (المحاكي: اتحاد) (E §5).
33. الإزاحات في الرفع المتقطع تُفحص (المحاكي يتجاهلها).
34. بروتوكول GCS المتقطع كاملًا (308 والـ override)، والشروط المسبقة 412، و`bucket.exists()`، وPATCH وrewrite تحت `/storage/v1` (المحاكي: 501 أو يتجاهلها) (E §8).
35. `contentDisposition` الافتراضي يُخزَّن `inline; filename*=utf-8''<اسم>` كما في Storage منشور (المحاكي: `inline`).
36. `etag` يتغير مع PATCH، و`generation` بالميكروثانية، و`x-goog-metageneration` بالتهجئة الصحيحة.
37. لا تقييم مزدوج للقواعد (ADR-021 §5.4).

**أُضيفت في S2-B (صيغ السلك، ADR-022 العواقب 16-17):**

38. **مؤقت:** `Range` غير صحيح الصيغة يُتجاهل (200 كاملًا)، و`bytes=-0` 416.
39. **قيد:** `multipart/related` جزءان بالضبط، أولهما `application/json` وبلا ترميز نقل، و`X-Goog-Upload-Command` بأحرف صغيرة، و`Content-Range` بأشكاله الستة ومعها `bytes N-(N-1)/N` (جزء Node الأخير الفارغ، يُقرأ `bytes */N`): غيرها 400. المكتبات الرسمية كلها داخل هذه الحدود: التسجيلات (`test/oracle/storage/emulator`)، وشكل Node الأخير من مراجعة S2-B (`@google-cloud/storage` 8.4.0 يرسل `bytes 0--1/0` لـ `save(Buffer.alloc(0), {chunkSize})`، `resumable-upload.js:619-623`).
40. **مؤقت:** الـ metadata المخصّصة الفارغة تُحذف من رد `/v0` (المحاكي يكتب `"metadata":{}`)، والمكتبات الثلاث تقرأ الحالتين «بلا metadata» (Android يفحص `has("metadata")` في `StorageMetadata.parseJSON`، وiOS `as? [String: String]`، A §4).

**أُضيفت في S2-F (خدمة البايتات، ADR-022 العواقب 18):**

41. **مؤقت:** `Cache-Control` على وسائط GCS بلا قيمة مخزّنة `private, max-age=0` (GCS لكائن غير عام)، فيقرؤه Go في `ReaderObjectAttrs.CacheControl`.
42. **قيد:** تنزيل قررته القواعد يحمل `private` دائمًا (يُحذف `public` و`s-maxage`، ويُضاف `private` حين يغيب، فلا يخزّنه مخبأ مشترك ولو كان المخزّن `must-revalidate`). و`Content-Type` مخزّن فيه فاصلة أو غير صالح يُخدم `application/octet-stream`.
43. **مؤقت:** 304 على `If-None-Match`/`If-Modified-Since` قبل `Range`، و`If-Range` بتاريخ يعيد الكائن كاملًا، وجسم 416 ونصه، وكائن مخزّن gzip ليس gzip يُخدم كما خُزّن.

**أضافها S2-C** (تفصيلها وموضعها في الكود في `docs/compat/storage-v0.3.md`):

44. مؤقت: CORS على `alt=media` يطابق الأصل حرفيًا أو `*`، بلا أنماط داخله. والطريقة حرفيًا أو `*` (أي طريقة، وثيقة GCS).
45. مؤقت: `pageToken` مربوط أيضًا بـ `startOffset` و`endOffset` والسطح.
46. مؤقت: `delete_token` لرمز ليس للكائن يرفع الـ metageneration وحده (المحاكي).
47. قيد: الرموز تُحسب في `metadata_bytes` كما يحملها GCS.
48. مؤقت: `"metadata": null` على GCS يمسح الرموز معها (دلالة GCS).
49. قيد: مفاتيح الـ metadata المخصّصة بلا محارف تحكم، ولا NUL في قيمها، ولا DEL في الحقول التي تصير ترويسات.
50. مؤقت: على `/v0` تُحذف البادئة التي فيها مقطع فارغ قبل فاصلها الأخير، وتبقى `a/bar/` من `a/bar//` (CT). وما يُحذف يُحسب على الصفحة، فقد تأتي صفحة ناقصة أو فارغة ومعها `nextPageToken`.

**أضافها S2-D** (تفصيلها وموضعها في الكود في `docs/compat/storage-v0.3.md`):

51. مؤقت: كل شرط مسبق غير `ifGenerationMatch=0` على كائن غير موجود 412.
52. مؤقت: الكتابة فوق موجود، أو الإنشاء بعد حذف، لا ترث رموز التنزيل.
53. مؤقت: قطعة تتجاوز الحجم المعلن أو الكلي في `Content-Range` 400 و`final` (لا 413)، وجسم أقصر من طوله المعلن 400 مع `active`.
54. قيد: جسم مجهول الطول يحجز الحصة بخطوات 8 MiB تُنصَّف حتى 64 KiB قرب الحد، فتحت التزامن قرب الحصة قد يُرفض رفع كان سيتسع.
55. مؤقت: على GCS، شرط مسبق يفشل عند إنهاء الرفع المتقطع نهائي (412 محفوظ يُعاد، والبايتات وحجزها تُحرَّر فورًا).
56. قيد: الجلسات الفارغة تُحسب عند `start` في حدّي IP و`uid`، وأول بايت متين لجلسة يُفحص من جديد على الحدود الثلاثة، وتجاوزها عنده 402 و`final`.

**أضافها S2-E** (ربط القواعد، ADR-022 العواقب 19-20؛ تفصيلها في `docs/compat/storage-v0.3.md`):

57. **مؤقت: ترتيب `metadata` في `resource` و`request.resource` بالمفتاح** (`rules.Map`)، فـ `values()` مرتبة كـ `keys()`. Test API وحدها تحفظ ترتيب الطلب، وهي تأخذ `request` حرفيًا، وjsonb لا يحفظ ترتيبًا، ولا قياس من المحاكي ولا المنشور. فلا تخزين يحفظ الترتيب على استنتاج. `core/rules_fields.go` (`metadataValue`).
58. **مؤقت: `resource` سبعة عشر مفتاحًا:** الخمسة عشر التي قاسها PY-RR في `request.resource`، و`timeCreated` و`updated` timestamp. والحقل غير المضبوط `null`، و`contentEncoding` غير المضبوط `'identity'` كما قاسه PY-RR في `request.resource` (عند الإنشاء والتعديل)، و`storageClass` = `'STANDARD'`. مجموعة المفاتيح الدقيقة في `resource` لم تُقس. `core/rules_fields.go` (`existing`).
59. **مؤقت: list ببادئة لا تنتهي بـ `/` يُقرَّر على مجلدها** (ما قبل آخر `/`) ومقطع مجهول، فلا تسمح قاعدة `p/a/{x}` بقائمة `p/a` التي تعيد `p/ab`. المكتبات ترسل `<مسار>/` أو البادئة الفارغة دائمًا. `core/rules.go` (`rulePaths`).
60. **قيد: اسم بمقاطع أكثر من `request_segments` للـ ruleset** (1024 افتراضيًا مع `b/<bucket>/o`) **يُرفض 403**، لا 503 تعيد المكتبات عليه بلا نهاية. `core/rules.go` (`ruleRequest`، و`Ruleset.RequestSegments`).
61. **قيد: مصدر قواعد فيه NUL يُرفض 400** (jsonb لا يحفظه). وعدد الـ rulesets لكل مشروع `rules.limits.rulesets_per_project` (2,500 افتراضيًا، ولا يتجاوز حد Firebase، ADR-021 §7). `tenancy/core/rules.go`.

**أضافها S4-A** (أساس GCS JSON؛ تفصيلها في `docs/compat/storage-v0.3.md`):

62. **قيد: سطح GCS لحسابات الخدمة وحدها** (`aruma_sat_`): رمز CLI الشخصي ورمز المستخدم 401 `authError`، كما بلا رمز 401 `required`. وحساب مشروع على bucket غيره 403 `forbidden` بنص GCS، لا `NOT_FOUND`: استثناء Storage من ADR-010 S-11 (ADR-022 §14)، والرفض قبل أي بحث عن كائن. `gcs/auth.go`.
63. **مؤقت: الـ bucket يُحل قبل الاعتماد،** فـ bucket غير موجود 404 لأي طالب، كما رد الإنتاج على المجهول (`bucket_probe.json`)، والموجود 401 بلا رمز و403 لحساب مشروع آخر (استثناء S-11، ADR-022 §14)، وكل ذلك قبل أي بحث عن كائن. `gcs/gcs.go` (`route`)، و`gcs/auth.go` (`admit`).
64. **مؤقت: الشروط المسبقة كلها على الصف الذي تقع عليه العملية** (بعد مراجعة S4-A): `sourceGeneration` و`ifSource*` في rewrite على صف المصدر الذي يُنسخ ملفه (`core.SourceConditions`)، و`ifMetagenerationMatch`/`NotMatch` على صف إعدادات الـ bucket المقفل في معاملة `SetCORS` (`core.BucketConditions`)، و`?generation=` على PATCH وDELETE يصير `ifGenerationMatch` على الصف المقفل؛ والجيل غير الحالي 404 لا 412. `gcs/objects.go` (`pinned`)، و`core/insert.go` (`Rewrite`)، و`core/cors.go` (`SetCORS`).
65. **قيد: حقول الكائن المؤجلة في الجسم 501** (`acl` و`temporaryHold` و`eventBasedHold` و`customTime` و`retention` و`contexts` و`kmsKeyName` و`customerEncryption` و`storageClass` غير `STANDARD`)، وقيمها الفارغة تُتجاهل؛ وقيمة `metadata` غير نصية 400. و`buckets.patch` بغير `cors` 400، و`ifGenerationMatch` على الـ bucket 400. `gcs/resource.go` (`decodeAttrs`)، و`gcs/buckets.go`.
66. **مؤقت: ما ليس في v0.3 من الواجهة 501** (`copyTo` و`compose` و`moveTo` والـ ACL وIAM والإشعارات وقائمة الـ buckets وإنشاؤها وحذفها و`objects.update` بـ PUT وCSEK)، و`rewriteToken` مُرسَل 400 (لا نصدر رموزًا)، وما لا تعرفه الواجهة 404. `gcs/gcs.go`.
67. **مؤقت: مورد الـ bucket** موقعه اسمي (`US`، `multi-region`)، و`timeCreated` إنشاء المشروع. وأسباب الأخطاء حيث لا تسجيل: 413 `uploadTooLarge`، و429 `rateLimitExceeded` لكثرة الجلسات ولتجاوز `upload_requests_per_ip`، و410 للجلسة الملغاة. و`Retry-After` على الضغط العابر وحده (تجاوز `upload_requests_per_ip`، و`core.ErrOverloaded`، وبدء رفع فوق معدله)، ولا يُرسل أبدًا على 503 الخادم نفسه (`core.ErrUnavailable`). `gcs/resource.go`، `gcs/errors.go`.

**أضافها S4-BC** (الرفع والوسائط على GCS؛ تفصيلها في `docs/compat/storage-v0.3.md`):

68. **مؤقت: `DELETE` على رابط الجلسة يلغيها بـ 499** (وثيقة GCS)، وطلب على الجلسة بلا `Content-Range` هو الكائن كله. `gcs/upload.go`.
69. **مؤقت: بدء الرفع على GCS يُحسب على `upload_starts_per_minute_per_ip`** بعنوان العميل (`trusted_proxies`، كحد الطلبات لكل IP)، والتجاوز 429 `rateLimitExceeded` مع `Retry-After`. `gcs/upload.go`.
70. **مؤقت: أسباب رفض الرفع حيث لا تسجيل:** البطء تحت `min_bytes_per_second` 408، والجسم المنقطع 400، و`Content-Length` يخالف `Content-Range` 400، وتناقض checksum بين JSON و`X-Goog-Hash` 400. `gcs/upload.go`.
71. **قيد: لا `Retry-After` على 503 من عطل الخادم** (`core.ErrUnavailable`) في الرفع والتنزيل، بل على الضغط وحده. `gcs/errors.go` (`writeError`).

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
