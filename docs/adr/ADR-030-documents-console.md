# ADR-030: قاعدة المستندات: واجهة اللوحة (console API)

- الحالة: مسودة
- التاريخ: 2026-10-28
- المرحلة: F7 من الإصدار 0.4 (قاعدة المستندات، Document Database)
- المراجع: ADR-022 §15 (لوحة Storage ونمطها)، ADR-010 (S-4، S-5)، ADR-021 §7 (القواعد في tenancy)، ADR-025 §3 و§4.5 (الفهارس وواجهة Admin)، ADR-028 §2 (TTL)، ADR-029 (تصنيف الطالب)، والكود: `internal/services/documents/console`، و`api.Call.Caller` في `internal/services/documents/api/service.go`.

## السياق

تحتاج لوحة F7 متصفح بيانات ومحرر قواعد وشاشة فهارس وTTL واستعمالًا، كما في صفحات Firestore في Firebase console. ولخدمة المستندات نواتها وطبقة خدمتها (`api.Service`) التي تخدم gRPC وREST وWebChannel. والقيد: **اللوحة لا تعيد كتابة شيء** من الترميز والكتابات والاستعلامات والقواعد والفهارس، و**لا يتغير أي خرج على أسلاك Firestore**.

## القرار

1. **المسارات تحت `/console/v1/projects/{project}/documents`** في الحزمة `internal/services/documents/console`، تُركَّب عبر `consolemount` كما تُركَّب مسارات Storage، بالسياسة نفسها (جلسة أو `aruma_pat_`، وCSRF على الكتابات)، وحد جسم 1 MiB، و`{project}` يُحل لمالكه وإلا 404. والخدمة معطّلة: مسار المعلومات يقول `enabled: false` والبقية `FAILED_PRECONDITION`.
2. **كل عملية عبر `api.Service`** بطالب مصادَق مسبقًا: الحقل الجديد `api.Call.Caller` يحمل `authn.Caller{Role: RoleAdmin, Principal: حساب اللوحة}`، فيتخطى `Resolve` وحده، ويمر بـ `Admit` (ملكية المشروع) كأي `aruma_pat_`. فالكتابة من اللوحة **كتابة Admin تتجاوز القواعد** كما في Firebase console، والأخطاء نصوص الإنتاج نفسها (تُحوَّل `wire.Error` إلى `google.rpc.Status` بالرمز والنص). ولا يتغير شيء لطالب لا يضبط الحقل.
3. **القيم بصيغة REST في Firestore** (`{"integerValue":"42"}` …) بـ `wire.MarshalREST` و`wire.UnmarshalREST`، فتعرض اللوحة كل نوع وتعدّله بدقة. والمسار يُمرَّر `?path=` نسبيًا إلى الجذر (`users/u1`)، لا مقطعًا في الرابط، كاسم الكائن في Storage.
   - `GET …/documents` المعلومات والحدود: `pageSize` (50، **تخمين**)، و`maxPageSize` (1000)، و`documentBytes` (1 MiB)، و`fieldDepth` (20)، و`requestBytes`، و`recursiveDeleteDocs`.
   - `GET …/collections?path=` معرّفات المجموعات تحت الجذر أو مستند، مصفّحة.
   - `GET …/documents?path=<مجموعة>` صفحة بترتيب المعرّف (ترتيب الاسم في ListDocuments) **مع المستندات الغائبة** التي لها مجموعات فرعية (`show_missing`)، معلَّمة `missing: true` بلا حقول. و`fields=false` للأسماء وحدها.
   - `POST …/documents?path=<مجموعة>` إنشاء بمعرّف أو بمعرّف آلي (CreateDocument)؛ والموجود `ALREADY_EXISTS`.
   - `GET|PATCH|DELETE …/document?path=<مستند>`: القراءة، والتعديل بـ UpdateDocument (بلا `updateMask` استبدال كامل؛ ومعه حقل مسمّى غائب عن `fields` يُحذف؛ و`exists` شرط مسبق اختياري)، والحذف (لا يحذف المجموعات الفرعية).
   - `POST …/documents:recursiveDelete` بـ `{"path"}` لمستند أو مجموعة: **طريقة `firebase firestore:delete -r` نفسها**: المستند أولًا، ثم استعلام kindless بـ `allDescendants` يختار `__name__` (ومجال الاسم بـ `MIN_ID` و`\0` للمجموعة، `collectionDescendantsQuery`)، وحذف بدفعات 250 في Commit. والطلب الواحد محدود بـ 5000 مستند ويرد `{"deleted", "done"}`، فتكرره اللوحة حتى `done`.
4. **القواعد عبر tenancy** كـ `firebase deploy --only firestore:rules`: `GET|PUT …/rules`، و`POST …/rules:validate` (بلا حفظ، `issues[]` بسطر وعمود 1-based كما في firebaserules)، وسجل `GET …/rulesets` (الأحدث أولًا، قواعد Firestore وحدها، والحالي معلَّم) و`GET …/rulesets/{id}` بالمصدر. ولهذا حمل `tcore.Ruleset` حقل `Service` (لا يظهر في firebaserules API). وtenancy يدقّق الحفظ والنشر بنفسه.
5. **الفهارس بطرق واجهة Admin**، فحالاتها وعملياتها ما يراه `firebase deploy`، بصيغة REST: `GET|POST …/indexes` و`DELETE …/indexes/{group}/{id}` للمركّبة (CREATING/READY)؛ و`GET|PUT|DELETE …/fields?group=&field=` للاستثناءات (`indexConfig.usesAncestorConfig=false`، والمسح يعيد الحقل إلى إعداد سلفه)؛ و`GET|PUT|DELETE …/ttl?group=&field=` لسياسات TTL (CREATING ثم ACTIVE)؛ و`GET …/operations`.
6. **التدقيق** كما في Storage: كل كتابة نجحت تُكتب (`documents.document.create|update|delete`، `documents.recursive_delete` بعددها، `documents.index.create|delete`، `documents.field.update|clear`، `documents.ttl.update|clear`)، وفشل السجل `INTERNAL` والتغيير قائم.
7. **الحزمة تقف على `api`** لا على نقل: archtest يمنعها من grpc وrest وwebchannel وlisten.
8. **الاستعمال:** لا عدّادات لكل مشروع اليوم (مقاييس Prometheus على مستوى العملية، بالطريقة)، فـ `GET …/usage` يرد `available: false` ولا يعرض حركة مشاريع أخرى. ولا نبني نظام مقاييس هنا.

## تخمينات لم تُسجَّل من الإنتاج (R7 يسجّلها)

حجم الصفحة الافتراضي (50)؛ وأن console يرتب بالمعرّف ويعرض الغائب بخط مائل؛ ودلالة الحذف التكراري في console (نتبع firebase-tools)؛ ونصوص التحقق من القواعد في console (نعيد نصوص المترجم)؛ وأن تعديل console مستندًا حُذف أثناء التحرير يعيد إنشاءه (لا نضع شرطًا ما لم تطلبه اللوحة).

## البدائل المرفوضة

- **الكتابة إلى النواة مباشرة:** تتجاوز التحقق والحدود ونصوص الأخطاء في `api`، وتكرر منطقه.
- **تمرير ترويسة Authorization إلى `api`:** لا تصلح لكعكة الجلسة، و`aruma_pat_` يُطلب عبر TLS (ADR-029 §3.7).
- **مقاطع المسار للمستندات:** المعرّفات قد تحمل `:` و`%` وغيرهما؛ `?path=` يصلها كما كُتبت.

## العواقب

- مستند قرب حد 1 MiB لا يُكتب من اللوحة (JSON أكبر من حد الجسم 1 MiB). يُقبل حتى يحتاجه أحد.
- سجل القواعد قد يرد صفحة أقصر من `pageSize` لأن قواعد Storage تُستبعد منها.
- الحذف التكراري ليس ذريًا، كما في firebase-tools: ما حُذف قبل خطأ يبقى محذوفًا.
