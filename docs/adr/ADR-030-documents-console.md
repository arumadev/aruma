# ADR-030: قاعدة المستندات: واجهة اللوحة (console API)

- الحالة: مسودة
- التاريخ: 2026-10-28
- المرحلة: F7 من الإصدار 0.4 (قاعدة المستندات، Document Database)
- المراجع: ADR-022 §15 (لوحة Storage ونمطها)، ADR-010 (S-4، S-5)، ADR-021 §7 (القواعد في tenancy)، ADR-025 §3 و§4.5 (الفهارس وواجهة Admin)، ADR-028 §2 (TTL)، ADR-029 (تصنيف الطالب)، والكود: `internal/services/documents/console`، و`api.Call.Caller` في `internal/services/documents/api/service.go`.

## السياق

تحتاج لوحة F7 متصفح بيانات ومحرر قواعد وشاشة فهارس وTTL واستعمالًا، كما في صفحات Firestore في Firebase console. ولخدمة المستندات نواتها وطبقة خدمتها (`api.Service`) التي تخدم gRPC وREST وWebChannel. والقيد: **اللوحة لا تعيد كتابة شيء** من الترميز والكتابات والاستعلامات والقواعد والفهارس، و**لا يتغير أي خرج على أسلاك Firestore**.

## القرار

1. **المسارات تحت `/console/v1/projects/{project}/documents`** في الحزمة `internal/services/documents/console`، تُركَّب عبر `consolemount` كما تُركَّب مسارات Storage، بالسياسة نفسها (جلسة أو `aruma_pat_`، وCSRF على الكتابات)، وحد جسم 1 MiB، و`{project}` يُحل لمالكه وإلا 404. والخدمة معطّلة: مسار المعلومات يقول `enabled: false` والبقية `FAILED_PRECONDITION`.
2. **كل عملية عبر `api.Service`** بطالب مصادَق مسبقًا: الحقل الجديد `api.Call.Caller` يحمل `authn.Caller{Role: RoleAdmin, Principal: حساب اللوحة}`، فيتخطى `Resolve` وحده، ويمر بـ `Admit` (ملكية المشروع) كأي `aruma_pat_`. فالكتابة من اللوحة **كتابة Admin تتجاوز القواعد** كما في Firebase console، والأخطاء نصوص الإنتاج نفسها (تُحوَّل `wire.Error` إلى `google.rpc.Status` بالرمز والنص). ولا يتغير شيء لطالب لا يضبط الحقل. ولا يضبطه إلا `internal/services/documents/console`، يفرض ذلك `internal/archtest` (`TestCallerOnlyFromConsole`).
3. **القيم بصيغة REST في Firestore** (`{"integerValue":"42"}` …) بـ `wire.MarshalREST` و`wire.UnmarshalREST`، فتعرض اللوحة كل نوع وتعدّله بدقة. والمسار يُمرَّر `?path=` نسبيًا إلى الجذر (`users/u1`)، لا مقطعًا في الرابط، كاسم الكائن في Storage.
   - `GET …/documents` المعلومات والحدود: `pageSize` (50، **تخمين**)، و`maxPageSize` (1000)، و`documentBytes` (1 MiB)، و`fieldDepth` (20)، و`requestBytes`، و`recursiveDeleteDocs`.
   - `GET …/collections?path=` معرّفات المجموعات تحت الجذر أو مستند، مصفّحة.
   - `GET …/documents?path=<مجموعة>` صفحة بترتيب المعرّف (ترتيب الاسم في ListDocuments) **مع المستندات الغائبة** التي لها مجموعات فرعية (`show_missing`)، معلَّمة `missing: true` بلا حقول. و`fields=false` للأسماء وحدها.
   - `POST …/documents?path=<مجموعة>` إنشاء بمعرّف أو بمعرّف آلي (CreateDocument)؛ والموجود `ALREADY_EXISTS`.
   - `GET|PATCH|DELETE …/document?path=<مستند>`: القراءة، والتعديل بـ UpdateDocument (بلا `updateMask` استبدال كامل؛ ومعه حقل مسمّى غائب عن `fields` يُحذف؛ و`exists` شرط مسبق اختياري)، والحذف (لا يحذف المجموعات الفرعية).
   - `POST …/documents:recursiveDelete` بـ `{"path"}` لمستند أو مجموعة: **طريقة `firebase firestore:delete -r` نفسها**: المستند أولًا، ثم استعلام kindless بـ `allDescendants` يختار `__name__` (ومجال الاسم بـ `MIN_ID` و`\0` للمجموعة، `collectionDescendantsQuery`)، وحذف بدفعات 250 في Commit. والطلب الواحد محدود بـ 5000 مستند ويرد `{"deleted", "done"}`، فتكرره اللوحة حتى `done`.
4. **القواعد عبر tenancy** كـ `firebase deploy --only firestore:rules`: `GET|PUT …/rules`، و`POST …/rules:validate` (بلا حفظ، `issues[]` بسطر وعمود 1-based كما في firebaserules)، وسجل `GET …/rulesets` (الأحدث أولًا، قواعد Firestore وحدها، والحالي معلَّم) و`GET …/rulesets/{id}` بالمصدر. ولهذا حمل `tcore.Ruleset` حقل `Service` (لا يظهر في firebaserules API). وtenancy يدقّق الحفظ والنشر بنفسه.
   - **ساحة القواعد (Rules Playground)** `POST …/rules:simulate` بـ `{"source"?, "method", "path", "auth"?, "data"?}`: `method` من get وcreate وupdate وdelete (لا list، كما في console)، و`path` مستند نسبي إلى الجذر، و`source` نص المحرر ولو لم يُنشر (غيابه: المنشور، ولا منشور: المغلق الافتراضي). و`auth` إن وُجد يبني `request.auth` كـ "Authentication payload" المسجَّل: `uid`، و`token` فيه `sub` و`aud` (المشروع) و`email` و`email_verified` و`phone_number` و`name` ولو فارغة، و`firebase.sign_in_provider` (`provider`، وافتراضه `google.com`). و`data` حقول `request.resource` بصيغة REST للإنشاء والتعديل. يحكم بـ `api.Service.SimulateRules` بمحرك القواعد نفسه وكما يُحكم طلب مستخدم حقيقي: المستند المخزَّن `resource`، و`get()`/`exists()` تقرأ مستندات المشروع نفسه في آخر commit (و`getAfter()` في الكتابة ترى المستند كما تتركه)، بميزانية البحث وحدود التقييم نفسها، **ولا يكتب شيئًا ولا يُعدّ في الاستعمال**. ويرد `{"allowed", "statements", "issues"}`: كل عبارة allow قُيِّمت بسطرها وعمودها وطرقها ونتيجتها (`true`/`false`/`error` برسالتها)، أي ما سمح أو لماذا لم يسمح شيء (لا شجرة التعبير الكاملة التي يعرضها console: `TraceExpressions` محجوز). والمصدر الذي لا يُترجم `INVALID_ARGUMENT` بمشكلاته كـ `PUT …/rules`. والطلب لا يكشف ما لا يقرؤه الطالب أصلًا: يُقبل بمسار `admitAs` للمدير وحده (مالك المشروع كبقية المسارات)، وبحث القواعد مقيَّد بمشروع الطلب وقاعدته (`lookups.Get`).
5. **الفهارس بطرق واجهة Admin**، فحالاتها وعملياتها ما يراه `firebase deploy`، بصيغة REST: `GET|POST …/indexes` و`DELETE …/indexes/{group}/{id}` للمركّبة (CREATING/READY)؛ و`GET|PUT|DELETE …/fields?group=&field=` للاستثناءات (`indexConfig.usesAncestorConfig=false`، والمسح يعيد الحقل إلى إعداد سلفه)؛ و`GET|PUT|DELETE …/ttl?group=&field=` لسياسات TTL (CREATING ثم ACTIVE)؛ و`GET …/operations`.
6. **التدقيق** كما في Storage: كل كتابة نجحت تُكتب (`documents.document.create|update|delete`، `documents.recursive_delete` بعددها (ويُكتب كذلك إن توقف الحذف بعد دفعات نُفذت: `done: false` ورمز الخطأ `error`)، `documents.index.create|delete`، `documents.field.update|clear`، `documents.ttl.update|clear`)، وفشل السجل `INTERNAL` والتغيير قائم.
7. **الحزمة تقف على `api`** لا على نقل: archtest يمنعها من grpc وrest وwebchannel وlisten.
8. **الاستعمال:** عدّادات لكل مشروع في `core.Usage`، في الذاكرة بقفل قراءة وأعداد ذرية، لا يكتب أي طلب صفًا: تُضاف كل 10 ثوانٍ إلى `documents.usage_hours` (ترحيل 00004، صف لكل مشروع وساعة) **بجملة واحدة** (`unnest`)، ومرة أخيرة عند الإيقاف (في `Documents.Close` بعد أن يصرّف الخادم طلباته، لا عند انتهاء سياق التشغيل) فتبقى بعد إعادة التشغيل، وما فشلت إضافته يبقى للتالية. وتُحذف الساعات الأقدم من 31 يومًا (مرة في الساعة)، وتُحذف مع المشروع المحذوف، وينسى `Usage.Forget` قبلها ما في الذاكرة من أعداده ومقاييسه فلا تعيدها دفعة لاحقة. ويُعدّ في طبقة الخدمة فيشمل gRPC وREST وWebChannel وكتابات اللوحة (كما يقول console "Includes Firebase console usage"):
   - **القراءات** كما يفوتر Firestore: Get واحدة (والمستند الغائب واحدة)، وBatchGet لكل مستند مطلوب، وصفحة ListDocuments ومستعلم RunQuery بعدد المستندات، وواحدة لما لا نتيجة له، وكل `document_change` يُرسل في Listen، وكل `get()`/`exists()` تقرؤه القواعد من المخزن. ولا تُعدّ التجميعات (كما يقول console).
   - **الكتابات والحذف**: كل كتابة نجحت بنوعها (التحقق verify لا يُعدّ)، في Commit وBatchWrite وWrite stream وCreate/Update/DeleteDocument.
   - **الذروات**: أهداف Listen المفتوحة (snapshot listeners) وتدفقات Listen المفتوحة (active connections)، أعلى قيمة في الساعة، وتبدأ كل ساعة بما بقي مفتوحًا.
   - **قرارات القواعد**: allow وdeny وerror (ما لم تستطع القواعد الحكم فيه، UNAVAILABLE) لكل حكم على مستند أو استعلام. والـ Commit الذي تجرحه معاملة أقدم فيعيد طلب أقفاله يُحكم عليه من جديد، ولا يُعدّ (قرارًا ولا قراءة `get()`) إلا ما قالته المحاولة الأخيرة (`core.CountUsage`).

   و`GET …/usage?period=60m|24h|7d|30d` (افتراضه 24h) يرد `totals` (المجاميع، وأعلى ذروة) و`hours[]` صفًا لكل ساعة، لمشروع الطالب وحده بعد `admitAs`، ومنها ما لم يُضف بعد. والفترة تبدأ من بداية ساعتها الأولى (فـ 60m الساعة الجارية والسابقة)، واللوحة تجمع الساعات أيامًا إن شاءت.

## ما سجّله R7 من الإنتاج، وما بقي تخمينًا

سجّل R7 (`.cache/briefs/research/f7-production-console.md`، خارج git، أشكال الطلبات وحدها) وطابقناه في:
- **المجموعات الفرعية:** console يطلب `listCollectionIds` بـ `pageSize` 300، فهو افتراضنا (`collectionsPageSize`).
- **الإنشاء** تحقق `exists: false` ثم كتابة، فإنشاء موجود يفشل: هو CreateDocument عندنا (`ALREADY_EXISTS`).
- **التعديل** بشرط `updateTime` الذي قرأه المحرر وقناع حقول بأسماء بين backticks: يقبل `PATCH …/document` الحقل `updateTime` (أو `exists`، لا كليهما)، والتعديل فوق قراءة قديمة `FAILED_PRECONDITION`.
- **الحذف التكراري** لمستند ولمجموعة بنقطة واحدة في console (`?recursive=true`، داخلية): نقطتنا الواحدة كذلك، بطريقة firebase-tools لأن طريقة console الداخلية غير معروفة.
- **سجل القواعد** مع علامة المنشور، والتحقق بـ `:test` ورسائل المحلل بسطرها: كما هنا، والرسائل رسائل مترجمنا لا رسائل ANTLR في الإنتاج (فرق مقبول، يُسجَّل في المحرر).
- **الاستثناء** يسرد الفهارس المفعّلة وحدها بقناع `indexConfig`: هو `PUT …/fields`.

وبقي تخمينًا: حجم صفحة المستندات (50؛ console يقرؤها عبر Listen بلا حجم مسجَّل)؛ وعرض الغائب بخط مائل (لم يُمارَس)؛ وحالات TTL (لم تُنشأ سياسة)، وإزاحة TTL (`offset`) في Google Cloud console لا نخدمها. وفي الاستعمال: قواعد الفوترة من توثيق Firestore لا من تسجيل (قراءة المستند الغائب، والحد الأدنى للاستعلام، وعدّ Listen)، وحذف TTL لا يُعدّ بعد، ودقة 60m ساعة لا دقيقة كما في Cloud Monitoring. وفي الساحة: شكل الطلب من واجهة console المسجَّلة لا من شبكتها (لم يُضغط Run).

## البدائل المرفوضة

- **الكتابة إلى النواة مباشرة:** تتجاوز التحقق والحدود ونصوص الأخطاء في `api`، وتكرر منطقه.
- **تمرير ترويسة Authorization إلى `api`:** لا تصلح لكعكة الجلسة، و`aruma_pat_` يُطلب عبر TLS (ADR-029 §3.7).
- **مقاطع المسار للمستندات:** المعرّفات قد تحمل `:` و`%` وغيرهما؛ `?path=` يصلها كما كُتبت.

## العواقب

- مستند قرب حد 1 MiB لا يُكتب من اللوحة (JSON أكبر من حد الجسم 1 MiB). يُقبل حتى يحتاجه أحد.
- سجل القواعد قد يرد صفحة أقصر من `pageSize` لأن قواعد Storage تُستبعد منها.
- الحذف التكراري ليس ذريًا، كما في firebase-tools: ما حُذف قبل خطأ يبقى محذوفًا.
