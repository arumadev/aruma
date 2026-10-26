# ADR-025: قاعدة المستندات: الاستعلامات والفهارس والمعاملات

- الحالة: مسودة
- التاريخ: 2026-10-26
- المرحلة: F0 من الإصدار 0.4 (قاعدة المستندات، Document Database)
- المراجع: ADR-024 (التخزين وترتيب الكتابات والأقفال، وأرقام مدير الأقفال)، ADR-026 (البث)، ADR-027 (النقل والـ protos)، ADR-028 (النطاق والتحقق)، ADR-029 (مصادقة Admin)، ADR-021 (محرك القواعد)، ADR-010، و:
  - الخطة المعتمدة `.cache/briefs/plan-v04-firestore.html` (اختصارها **PL**)، الأقسام 3 و4 و6.
  - أبحاث v0.4 في `.cache/briefs/research/` (خارج git، محفوظة مع الخطة): `firestore-composite-index-evidence.md` (**IX**)، و`firestore-clients.md` (**CL**، ومتطلباته «MUST n» في قسم «server MUST»)، و`firestore-official.md` (**OF**)، و`firestore-prior-art-and-verification.md` (**PA**)، و`firestore-scope-audit.md` (**SA**).
  - **تجربة الفهارس على الإنتاج** (اختصارها **الاختبار**): 105 أشكال استعلام على `projects/chatapp-8ae21/databases/(default)` (Standard، `concurrencyMode: PESSIMISTIC`)، كل شكل على مجموعة ممتلئة ومجموعة لم تُكتب قط، في 2026-10-26 بين 13:45:38Z و13:47:06Z، ومعها المحاكي v1.22.0. البيانات في `.cache/briefs/research/artifacts/composite-index-probe.json` و`composite-index-probe-emulator.json`، والسكربت `composite-index-probe.mjs` (IX §2، §3).
  - مراجعة الخطة المستقلة `.cache/briefs/reviews/v04-plan-review.md` (**RV**).
  - تجربة المتسلسل ومدير الأقفال في F0 `.cache/briefs/research/spike-sequencer.md` (**SP**)، وتسجيل التزاحم من الإنتاج `test/oracle/documents/corpus/f0-contention.json` (**RC**، 2026-10-26، ستة سيناريوهات × 3).
  - **تسجيلات F0 من الإنتاج** في `test/oracle/documents/corpus/` (`f0-find-nearest.json`، و`f0-explain.json`، و`f0-errors.json`، و`f0-missing-database.json`)، وتحليلها `.cache/briefs/research/f0-production-recordings.md` (**PR**). والفروق المقصودة في `test/oracle/documents/parity-exceptions.json`.

## السياق

الاستعلام في Firestore لا يُنفَّذ إلا من فهرس: «a linear scan over a range of a single secondary index... or a join of several such secondary indexes... with no in-memory sorting, filtering» (OF §1a). فما لا يخدمه فهرس يُرفض برابط لإنشائه. ويترتب على هذا ثلاثة أسئلة لم يحسمها شيء في المستودع بعد:

- **كيف نخدم الاستعلام على Postgres** دون أن يصير المخطِّط مصدر أخطاء صامتة؟ Kor جرّب مسارًا مرجعيًا ومخطِّطًا يرفض ما لا يخدمه بدقة، ووجد بإعادة الفحص خطأ مخطِّط في يومه الأول (PA §A1، `store/query.go:360-405`).
- **هل نفرض الفهارس المركّبة؟** المحاكي لا يفرضها («will execute any valid query»، IX §1.8). والاختبار على الإنتاج أعطى 60 مقبولًا و45 مرفوضًا، والحكم واحد في المجموعة الفارغة والممتلئة في الأشكال الـ 105 كلها، والمحاكي قبل 43 من الـ 45 (IX §2.2، §3). فالمحاكي أوراكل للنتائج لا للفهارس.
- **أي نموذج معاملات؟** الويب والجوال لا يستدعيان `BeginTransaction` أبدًا: قراءة ثم `Commit` بشروط و`verify` (CL §2.2). وNode يبدأ المعاملة داخل أول قراءة، وGo يستدعي `BeginTransaction` (CL §2.3، §2.4). والإنتاج لا يسمح لطلب طرف ثالث بمعاملة تشاؤمية: «3rd party auth requests are only allowed to create optimistic read-write transactions» (OF §2d، `common.proto`)، والسبب المذكور حرمان الخدمة (OF §1a). والمراجعة عدّت غياب هذا الحد مانعًا (RV، blocker الثاني).

تفاصيل الجداول وترتيب الكتابات ومدير الأقفال في ADR-024، والنقل في ADR-027، ومصادقة Admin في ADR-029. هنا الدلالة التي يراها العميل.

## القرار

### 1. مساران للاستعلام: مرجعي ومخطِّط، مع إعادة فحص

- **المسار المرجعي** (`query/reference.go`): يقرأ المرشحين بالمسار الأب أو معرّف المجموعة، ويرشّح ويرتّب في Go بالمقارن المرجعي (`value/compare.go`). هو **الحَكَم** في الاختبارات، ويخدم ما لا يخدمه غيره: القراءة بـ `read_time` (ADR-024)، و`allDescendants` بلا معرّف مجموعة (البند 6)، والوضع `off` (البند 4.4).
- **المخطِّط** (`query/plan.go`): يخدم الاستعلام من مدخلات الفهارس (مسح نطاق، أو دمج فهارس مساواة) **حين يقدر أن يخدمه بدقة**، ويرفض غير ذلك فيعود الاستعلام إلى المسار المرجعي. قاعدة Kor: «every rejected shape is a performance opportunity, never a correctness risk» (PA §A1، `plan.go:12-20`).
- **إعادة الفحص** (`query/recheck.go`): كل صف يعيده مسار الفهرس يُقيَّم من جديد بمطابق المسار المرجعي. **صف لا يطابق خطأ `INTERNAL` يُسجَّل ويُعدّ، لا صف يُحذف بصمت** (PA §A1، §B4). **وسلاسل المدخلات المتعادلة في بادئة المفتاح** (مفاتيح أطول من 2048 بايتًا، ADR-024 §1.4) تُرتَّب فيها بالمفتاح الكامل قبل أن يقطعها limit أو مؤشر أو صفحة.
- **عدّادات المسار** (`query/coverage.go`): `documents_query_path_total{path=reference|index|index_merged|index_contains}`. والاختبارات تثبت أن كل مسار جرى فعلًا، فلا ينجح مخطِّط يرفض كل شيء (PA §B4).
- **رفض المخطِّط لاستعلام قبله الفرض** (البند 4) يُعدّ ويُسجَّل: هو نقص أداء يُصلح، لأن الإنتاج يخدم كل ما يقبله من فهرس.
- **الحدود من الإنتاج:** OR و`in` حتى 30 فرعًا في DNF، و`not-in` حتى 10، و`not-in`/`!=` واحد في الاستعلام، وحقول عدم المساواة حتى 10، والمرشحات والترتيب وعمق الأب معًا حتى 100، والقيمة المفهرسة أول 1500 بايت (OF §2b). والترتيب الضمني كما في `query.proto`: حقول عدم المساواة الناقصة بترتيب أسمائها، ثم `__name__` باتجاه آخر ترتيب (OF §2b؛ CL §1.9).

### 2. فهارس الحقل الواحد التلقائية، والتجاوزات والاستثناءات

- **التلقائي** (كوثيقة index-overview، IX §1.1؛ OF §2c):
  - لكل حقل ليس مصفوفة ولا خريطة: فهرس تصاعدي وتنازلي بنطاق `COLLECTION`.
  - الخريطة: كل حقل فرعي بالتكرار، وفهرسان للخريطة كاملة.
  - المصفوفة: فهرس `CONTAINS`، وتصاعدي وتنازلي للقيمة كاملة.
  - **نطاق `COLLECTION_GROUP` لا يُصان افتراضيًا**، فاستعلام collection-group بأي مرشح أو ترتيب على حقل يحتاج تجاوزًا (R12).
- **التجاوزات** (`fieldOverrides`) في `index/overrides.go`، مفتاحها `(database, collection group, field path)`:
  - `indexConfig.indexes[]` بـ `queryScope` و`order` أو `arrayConfig`. وبها يُفعَّل فهرس CG لحقل واحد (SA M12).
  - **الاستثناء** يعطّل فهرسًا تلقائيًا؛ و`*` استثناء على مستوى المجموعة؛ والافتراضي للقاعدة في `collectionGroups/__default__/fields/*` (IX §1.4، §1.7).
  - `ttlConfig` يُحفظ على التجاوز نفسه (SA M4)، والمنظّف في ADR-028.
- **حقل مستثنى يبقى قابلًا للفهرسة داخل فهرس مركّب** («can still be indexed as part of a manual index»، IX §1.4).
- **الحدود من الإنتاج** (IX §1.5): 40,000 مدخل فهرس للمستند، و7.5 KiB للمدخل، و8 MiB لمجموعها، و1500 بايت للقيمة (تُقص). وعدد التجاوزات من `documents.limits.field_overrides` (1000 افتراضيًا، رقم الإنتاج مع الفوترة). ونص الرفض عند تجاوز حد المستند يُسجَّل من الإنتاج في F3.

### 3. الفهارس المركّبة

- **مصدرها واجهة Admin وحدها** (البند 4.5)، فتصل من `firebase deploy --only firestore:indexes` عبر `FIRESTORE_URL` ومن لوحة Aruma (F7). ولا ملف إعداد ثانٍ.
- **الحقول** من 2 إلى 100، وآخرها `__name__` دائمًا. وإن غاب أُضيف باتجاه آخر حقل (IX §1.7، §4.3). وفيها حقل مصفوفة واحد على الأكثر (OF §2c).
- **الحالة:** `CREATING`، ثم `READY` بعد الملء، ببروتوكول ADR-024 §2.5: من لحظة الإنشاء يكتب كل commit مدخلات الفهرس (كتابة مزدوجة)، ثم يُملأ الموجود بدفعات بنطاقات المفتاح، ثم `READY` بعد آخر دفعة وحدها. والعملية التي يعيدها الإنشاء تبقى غير منتهية حتى `READY`. وهو أسرع من دقائق الإنتاج على البيانات الصغيرة، والشكل نفسه. **ولا يخدم المخطِّط فهرسًا `CREATING` أبدًا** (PA §A1، `ready_at`)، والاستعلام الذي يحتاجه يأخذ رد البند 4.2 (R21).
- **فهارس المتجهات** (`vectorConfig{dimension ≤ 2048, flat{}}`) تُقبل وتُحفظ وتُعاد كما أُرسلت، ولا يستعملها المخطِّط في v0.4.0. رفضها يُفشل نشر الملف كله (SA M5). ووجودها يحدد رد findNearest (البند 5).
- **العدد** من `documents.limits.composite_indexes` (1000 افتراضيًا). وتجاوزه يُرفض بنص الإنتاج، يُسجَّل في F3.

### 4. فرض الفهارس المركّبة: كما يفرض الإنتاج

#### 4.1 القاعدة المثبتة

التعريفات (IX §4.1):
- **شبه المساواة:** `==` و`in` و`array-contains` و`array-contains-any` و`IS_NULL`/`IS_NAN`.
- **عدم المساواة:** `<` و`<=` و`>` و`>=` و`!=` و`not-in` و`IS_NOT_NULL`/`IS_NOT_NAN`.
- **حقول الترتيب:** الترتيب الصريح، ثم الضمني على حقول عدم المساواة، ثم `__name__`.

الاستعلام بلا فهرس مركّب مقبول **إن تحقق أحد R1 إلى R4 في كل فرع من DNF**، وإلا احتاج فهرسًا مركّبًا واقترحه الخطأ. القواعد التالية كلها مثبتة على الإنتاج بالاختبار، وتُنفَّذ حرفًا في `index/enforce.go`:

| # | القاعدة | الحكم | أمثلة من الاختبار |
|---|---|---|---|
| R1 | شبه مساواة فقط، على أي عدد من الحقول (دمج الفهارس)، بلا ترتيب أو بـ `__name__` وحده بأي اتجاه | مقبول | eq3، in_in، ac_in، map_sub_eq_eq، eq2_ord_name_desc |
| R2 | كل المرشحات والترتيب على حقل واحد (أي عامل، ومنه `!=` و`not-in` والنطاق من الجهتين)، و`__name__` بالاتجاه الافتراضي | مقبول | range1_both، neq، notin، in_ord_same_desc |
| R3 | بلا مرشح، وترتيب على حقل واحد بأي اتجاه (ومعه limit أو مؤشر) | مقبول | ord_a_desc، cursor_single |
| R4 | الإسقاط وoffset وlimit لا تغيّر الحكم | — | select_eq، offset_eq |
| R5 | `__name__` باتجاه غير الافتراضي بعد ترتيب أو بلا مرشح | مركّب | name_desc، range1_name_desc، ord_d_desc_name_asc |
| R6 | شبه مساواة على X مع ترتيب أو عدم مساواة على Y، **ولو ثُبّت Y بـ `==`** | مركّب (X, Y) | eq1_ord_other، eq_eq_ord_eqfield، ac_ord |
| R7 | عدم مساواة على حقلين أو أكثر | مركّب، القالب 2 | range2 |
| R8 | عدم مساواة على X وأول ترتيب على Y≠X. لا `INVALID_ARGUMENT`: قاعدة «أول ترتيب على حقل عدم المساواة» زالت من الخادم | مركّب (Y, X)، القالب 2 | range1_ord_other_first |
| R9 | عدم مساواة على X مع `==` على Y، أو ترتيب X ثم Y | مركّب | neq_eq، notin_eq، range1_ord_other |
| R10 | ترتيب على حقلين أو أكثر، بأي اتجاهات | مركّب لكل نمط اتجاهات | ord_a_d، ord_a_desc_d |
| R11 | OR: كل فرع يحقق R1 إلى R3 **مع ترتيب الاستعلام كاملًا**، فعدم المساواة في فرع تفرض ترتيبها على الفروع كلها | مركّب، والقالب 3 إن نقص أكثر من فهرس | or_range، or_ord (مرفوض)؛ or2، count_or (مقبول) |
| R12 | collection-group: بلا مرشح ولا ترتيب (أو `__name__` تصاعدي) مقبول، ومنه count. وأي مرشح أو ترتيب على حقل يحتاج فهرس CG لحقل واحد (القالب 4)، والمساواة مع ترتيب مركّب CG | — | cg_all، cg_count؛ cg_eq، cg_range، cg_eq_ord، cg_or |
| R13 | `count()` يتبع استعلامه. و`sum`/`avg(F)` يضيفان F حقلًا تصاعديًا بعد المرشحات والترتيب | — | sum_eq، sum2_all، multi_agg؛ sum_range_same مقبول |

ومثبت أيضًا (IX §4.1): الحكم مستقل عن البيانات وعن وجود المجموعة (R14، 105 من 105)؛ وlimitToLast يصل معكوس الترتيب من العميل فيأخذ حكم الاستعلام المعكوس، وقد يحتاج فهرسًا تنازليًا لا يحتاجه الأصل (R17؛ CL §1.9).

#### 4.2 الأمان في اتجاه واحد

- **لا نرفض أبدًا ما يقبله الإنتاج.** القاعدة R1 إلى R13 هي النواة الصلبة، تُنفَّذ بلا تقريب.
- **ما لم يُثبت يُقبل ويُسجَّل** (IX §4.2، البند 1):
  - **R18، دمج المركّبات المعلنة** (موثّق، لم يُختبر لأن إنشاء الفهارس كان ممنوعًا): يُقبل كل دمج لفهارس معلنة تشترك في لاحقة الترتيب المطلوبة.
  - **R19، اختيار الفهرس:** يُعدّ الفهرس المعلن خادمًا إن غطّت حقوله مجموعة المساواة ثم تسلسل الترتيب.
  - **R20، الاستثناءات:** استعلام يحتاج فهرسًا تلقائيًا عطّله استثناء يرفضه الإنتاج بنص لم يُسجَّل، فنقبله ونسجّله.
  - **R21، فهرس `CREATING`:** يقع عندنا أيضًا (البند 3). لا يخدمه المخطِّط أبدًا. ورد الإنتاج على استعلام يحتاج فهرسًا قيد البناء لم يُسجَّل، فحتى يُسجَّل يُرد خطأ الفهرس الناقص بالقالب 1، مؤقتًا، مع صف `index-still-building` في `parity-exceptions.json`. ويُسجَّل مع R18 إلى R22 في المشروع التجريبي.
  - **R22، القواعد المسمّاة:** شكل الرابط مجهول، ولا قواعد مسمّاة في v0.4.0 (ADR-028).
- **القبول غير المؤكد** يكتب سطر سجل `index_enforcement_uncertain` فيه شكل الاستعلام والقاعدة، ويرفع عدّادًا باسمها.
- **إعادة اختبار R18 إلى R22** في مشروع تجريبي يُسمح فيه بإنشاء الفهارس، قبل الإصدار (IX §4.2 البند 5؛ ADR-028).

#### 4.3 الخطأ بالبايت

- **REST:** HTTP 400، والجسم `{"error":{"code":400,"message":"…","status":"FAILED_PRECONDITION"}}`، **ملفوفًا في مصفوفة** على الطرق المتدفقة (`runQuery` و`runAggregationQuery`)، كما سُجّل (IX §2.3) وكما تعتمد عليه مكتبة lite (OF §2c، JS PR #6712).
- **gRPC:** الرمز 9 `FAILED_PRECONDITION` بالنص نفسه. هذا استنتاج (الاختبار كان REST وحده)، ويُسجَّل عبر gRPC في F3.
- **Listen:** الهدف الذي يحتاج فهرسًا يُرفض بـ `REMOVE` مع `cause` بالحالة نفسها، ولا يُغلق البث (ADR-026؛ MUST 6).
- **القوالب الخمسة كما سُجّلت حرفًا** (IX §2.3):
  1. `The query requires an index. You can create it here: <URL>`
  2. القالب 1، ثم ` .\nThe query contains range and inequality filters on multiple fields, please refer to the documentation for index selection best practices: https://cloud.google.com/firestore/docs/query-data/multiple-range-fields.` (بمسافة قبل النقطة وسطر جديد حرفي؛ مع R7 وR8 وحدهما).
  3. `The query requires multiple indexes. You can create them here:<URL>` (بلا مسافة بعد `here:`، و`create_index=` مكرر لكل فهرس ناقص).
  4. `The query requires a COLLECTION_GROUP_ASC index for collection <cg> and field <f>. You can create it here: <URL>`، ومعه `COLLECTION_GROUP_DESC` و`COLLECTION_GROUP_CONTAINS`، والرابط بـ `create_exemption=`.
  5. المركّب بنطاق CG بالقالب 1 و`create_composite` بنطاق `COLLECTION_GROUP`.
- **الرابط:** `https://console.firebase.google.com/v1/r/project/{projectId}/firestore/indexes?{create_composite|create_exemption|create_index}={payload}`.
- **الحمولة:** base64 بأبجدية URL-safe بلا حشو (الأبجديتان متطابقتان في كل ما سُجّل)، لـ protobuf بأرقام حقول `google.firestore.admin.v1.Index`:
  - `1` الاسم: `projects/{p}/databases/(default)/collectionGroups/{cg}/indexes/_`، أو `…/fields/{field}` للاستثناء.
  - `2` النطاق: 1 `COLLECTION`، و2 `COLLECTION_GROUP`.
  - `3` الحقول: `1` المسار، و`2` الاتجاه (1 تصاعدي، 2 تنازلي) أو `3` `array_config` (1 `CONTAINS`).
- **ترتيب الحقول المقترحة** (R16): المساواة وarray-contains، ثم الترتيب الصريح بترتيبه واتجاهه، ثم بقية حقول عدم المساواة والتجميع **مرتبة بالمسار**، ثم `__name__` باتجاه آخر حقل. وترتيب حقول المساواة فيما بينها غير مثبت (ترتيب الاستعلام والترتيب المعجمي تطابقا في كل ما سُجّل)، فنتبع ترتيب الاستعلام. والحكم لا يتأثر به، والرابط وحده قد يختلف.
- **الرابط يشير إلى لوحة Firebase** ولا ينشئ شيئًا في Aruma. نُبقي شكله حرفًا لأن المكتبات والمستخدمين والأدوات تطابقه وتحلّله، **ولا نضيف إليه تلميحًا خاصًا بنا** (IX §4.2 البند 3).

#### 4.4 أوضاع الفرض

- **`enforce | warn | off`، لكل قاعدة مستندات:**
  - `enforce` (الافتراضي): كما سبق.
  - `warn`: يخدم الاستعلام، ويكتب سطر سجل ومقياسًا يسمّيان الفهرس المقترح بالضبط. هو مخرج الانتقال حين تُنشأ فهارس التطبيق من روابط اللوحة ولم تُصدَّر قط (IX §4.2 الخطر a).
  - `off`: للتطوير المحلي كالمحاكي.
- **الافتراضي** من `documents.index_enforcement`، والتجاوز لكل قاعدة في صف `databases` (ADR-024)، يُغيَّر من اللوحة. ولا يظهر في مورد `Database` على واجهة Admin، لأن الإنتاج لا يعرفه.

#### 4.5 واجهة Admin التي يطلبها firebase-tools

`firebase-tools` يوجّه كل نداء إلى `FIRESTORE_URL` (`lib/api.js:61`). وما يطلبه للنشر والتصدير (IX §1.7، §4.3؛ SA M1، M4، M5)، تحت `/v1`:

| الخطوة | النداء | ما نرده |
|---|---|---|
| نوع القاعدة (قبل القواعد والفهارس) | `GET /projects/{p}/databases/{db}` | `{type:"FIRESTORE_NATIVE", databaseEdition:"STANDARD", concurrencyMode:"PESSIMISTIC", …}` لـ `(default)`؛ وغيرها 404 `NOT_FOUND` بنص واجهة Admin المسجّل «Project '{p}' or database '{db}' does not exist.» (`f0-missing-database.json:263`؛ ADR-027 §2.2) |
| سرد المركّبة | `GET …/databases/{db}/collectionGroups/-/indexes` | `{indexes:[{name, queryScope, fields:[…, __name__], state:"READY"}]}`، والاسم `projects/{p}/databases/{db}/collectionGroups/{cg}/indexes/{id}` تحلّله الأداة |
| سرد التجاوزات | `GET …/collectionGroups/-/fields?filter=indexConfig.usesAncestorConfig=false OR ttlConfig:*` | `{fields:[…]}` ومعها `__default__/fields/*` كالإنتاج (الأداة تسقطه) |
| إنشاء مركّب | `POST …/collectionGroups/{cg}/indexes` بـ `{fields, queryScope}` | `google.longrunning.Operation` بـ `IndexOperationMetadata`، غير منتهية حتى يصير الفهرس `READY` (البند 3). والمواصفة المطابقة لموجود لا تتكرر |
| حذف مركّب | `DELETE /{index.name}` (مع `--force` أو بعد سؤال) | عملية منتهية |
| تجاوز | `PATCH …/collectionGroups/{cg}/fields/{fieldPath}` بـ `indexConfig` و`ttlConfig` اختياريًا، و`?updateMask=indexConfig` حين يغيب `ttl` | عملية؛ والمسار قد يكون بين علامتي backtick |
| إزالة تجاوز | `PATCH /{field.name}` بجسم `{}` | يعود الحقل إلى إعداد أصله |
| العمليات | `GET …/operations/{id}` | العملية وحالتها |

مصادقة هذه الواجهة في ADR-029، وتفاصيل `databases.list` والقاعدة غير الموجودة في ADR-028.

### 5. التجميعات، وfindNearest وexplain

- **`RunAggregationQuery`** بـ `count` (ومعه `up_to`) و`sum` و`avg` (OF §2b):
  - من 1 إلى 5 تجميعات، وأسماؤها فريدة وتُعاد كما أُرسلت (الويب يرسل `aggregate_0…`)، **ونتيجة واحدة بالضبط** (CL §2.5؛ MUST 17).
  - `sum` يعطي int64 ما لم يفض فيعطي double، والفارغ 0. و`avg` double دائمًا، والفارغ `null`. ومدخل NaN يعطي NaN. والقيم غير العددية تُتجاهل في `sum`/`avg` وتُعدّ في `count`. والتجميع على عدة حقول يشمل المستندات التي فيها كلها.
  - المهلة 60 ثانية ثم `DEADLINE_EXCEEDED` كالإنتاج.
  - حكم الفهرس R13. والتجميع داخل المعاملة وبـ `read_time` كالاستعلام (البند 7).
  - لا تجميع في Listen («aggregations... are not supported»، OF §1b).
- **`find_nearest`** في `RunQuery` و`RunAggregationQuery`، بحسب فهارس المتجهات المحفوظة (البند 3):
  - **بلا فهرس متجه مطابق** (مجموعة الاستعلام ونطاقه، وحقل المتجه، وبُعد متجه الاستعلام، وحقول المساواة السابقة له): **رد الإنتاج حرفًا**، كما سُجّل على REST وgRPC بالنص نفسه: HTTP 400 `FAILED_PRECONDITION` (gRPC 9)، ملفوفًا في مصفوفة على REST، والنص:
    `Missing vector index configuration. Please create the required index with the following gcloud command: gcloud firestore indexes composite create --project={p} --collection-group={cg} --query-scope=COLLECTION[ --field-config=order=ASCENDING,field-path={k}…] --field-config=vector-config='{"dimension":"{n}","flat": "{}"}',field-path={f}`
    بلا مسافة بعد `"dimension":"{n}",` وبمسافة بعد `"flat":`. والحكم من شكل الاستعلام وحده (المجموعة التي لم تُكتب تعطي النص نفسه باسمها)، والمقياس وخيارا المسافة لا يظهران فيه، وكل حقل مساواة سابق يضيف `--field-config=order=ASCENDING,field-path=k` قبل إعداد المتجه (`f0-find-nearest.json:7`، و`:74`، و`:144`، و`:213`؛ PR §b). ونطاق collection-group لم يُسجَّل، فنصه مؤقت.
  - **بفهرس متجه مطابق:** `UNIMPLEMENTED` (HTTP 501) ونص يسمّيه («findNearest (vector search) is not supported by this server»). الإنتاج يخدمه، فهو قيد مسجّل في `parity-exceptions.json` (`find-nearest-with-vector-index`)، وتنفيذه بمسح دقيق في v0.4.x (SA §4.4).
  - **هدف Listen به** يُرفض `REMOVE` مع `cause`، لأن البحث المتجهي لا يدعم المستمعين (SA §4.4؛ ADR-026 §1.3). ورمز الإنتاج ونصه لم يُسجَّلا بعد (ADR-028 §7.6).
  - وقيمة المتجه وفهارسه مدعومة من v0.4.0 (البند 3؛ ADR-028).
- **`explain_options` يُرفض صراحة في v0.4.0** بـ `UNIMPLEMENTED` (HTTP 501) ونص يسمّيه، في `RunQuery` و`RunAggregationQuery`، و`{}` كـ `analyze:false`. تجاهله يُسقط `explain()` في Node بـ «No explain results» (SA M10، `reference/query.js:878-886`). **والإنتاج يدعمه على Standard** (`f0-explain.json`؛ PR §c):
  - `analyze:false` (أو `{}`): عنصر واحد فيه `explainMetrics.planSummary` وحده، بلا مستند ولا `readTime` (`:5`، `:144`)؛
  - `analyze:true`: المستندات كالعادة، و`explainMetrics` (`planSummary` و`executionStats`، والأعداد نصوص، و`debugStats` من نوع `Struct`) على رد آخر مستند (`:55`، و`:505` عبر gRPC)؛
  - التجميع: `planSummary` وحده، أو النتيجة و`explainMetrics` و`readTime` في عنصر واحد (`:253`، `:311`)؛
  - استعلام يحتاج فهرسًا: خطأ الفهرس الناقص المعتاد، بلا مقاييس (`:397`).

  فالرفض قيد مسجّل في `parity-exceptions.json` (`explain-rejected`)، والتسجيل هو ما يُطابَق حين يأتي Query Explain في v0.4.x (ADR-028 §3). **ويُفحص الفهرس قبل رفض explain**، فالاستعلام الذي يحتاج فهرسًا يأخذ خطأ الإنتاج نفسه (`:397`).

### 6. `allDescendants` بلا معرّف مجموعة، و`showMissing`، وPartitionQuery

- **الاستعلام بلا معرّف مجموعة** (`from=[{allDescendants:true}]` بلا `collection_id`، ومعه مرشحات نطاق على `__name__` وإسقاط `__name__`): يخدمه المسار المرجعي بنطاق بادئة على اسم المستند تحت الأب. عليه يقوم `recursiveDelete` في Node وPython وJava، و`firebase firestore:delete -r`، وحذف المجموعة من اللوحة (SA M2، `delete.ts:170-270`، `recursive-delete.js:197-213`). وحكم الفهرس فيه لم يُختبر، فمرشحات `__name__` وحدها مقبولة، وغيرها يُقبل ويُسجَّل (البند 4.2). **ولا يكون هدف Listen:** الإنتاج يرفض الاستعلام على كل المجموعات هدفًا لحظيًا (PA §A2، hidane D117)، فيُرد `REMOVE` مع `cause` (ADR-026).
- **`ListDocuments` مع `show_missing=true`:** المستند «المفقود» (غير موجود وتحته مستندات) يُعاد باسمه وحده، بلا `create_time` ولا `update_time`. ومع `collection_id` فارغ يسرد كل المجموعات تحت الأب. **تجاهله يُسقط شجرات كاملة بصمت**، وAdmin Node وGo يرسلانه دائمًا مع قناع فارغ (SA M3). ويقوم على عمود المسار الأب في جدول المستندات (ADR-024)، وعليه أيضًا `ListCollectionIds`.
- **`PartitionQuery`:** في v0.4.0 نتيجة فارغة، وهي صحيحة بنص `firestore.proto:927-929` («An empty result may indicate that the query has too few results to be partitioned»)، ثم تقسيم حقيقي بعيّنات من `__name__` (PL، F3). والتحقق من الطلب كما يشترط الـ proto: استعلام collection-group، وترتيب `__name__` تصاعدي وحده، وبلا مرشحات.

### 7. المعاملات: النموذج من هوية الطالب، لا من نوع المكتبة

#### 7.1 من الطالب

الطالب يُصنَّف عند المصادقة (ADR-029؛ `core/principal.go`):
- **مستخدم نهائي:** رمز Firebase ID أو بلا مصادقة.
- **حساب خدمة أو مدير:** حساب خدمة المشروع، ورمز مالك المشروع، و`Bearer owner` حين يفعّله `documents.dev_owner_token` على خادم loopback وحده (ADR-029 §6).

والنموذج يُختار من هذا التصنيف وحده.

#### 7.2 المتفائل: للمستخدمين النهائيين

- **معاملات الويب والجوال** هي `BatchGetDocuments` بلا معاملة، ثم `Commit` بلا معرّف معاملة (CL §2.2). فدلالتها دلالة `Commit`:
  - المستند المقروء ثم المكتوب يحمل `current_document.update_time` (أو `exists:false`)، والمكتوب دون قراءة `exists:true`.
  - **المقروء غير المكتوب يصل كتابة `verify`**، وهي شرط بلا تغيير. `Write.verify` (الحقل 5) غائب عن googleapis وموجود في proto مكتبة JS (PA §A1)، ودمج الـ protos في ADR-027.
  - شرط يفشل يُفشل الـ commit كله ذريًا: `FAILED_PRECONDITION` لـ `update_time` قديم، و`ALREADY_EXISTS` لـ `exists:false` على موجود، و`NOT_FOUND` لـ `exists:true` على غائب (MUST 15). والعملاء يعيدون المحاولة على الثلاثة (CL §2.2). والنصوص تُسجَّل في F2.
  - `update_time` في الشرط بدقة الميكروثانية (OF §2d).
- **مستخدم نهائي يطلب معاملة read_write** (`BeginTransaction` أو `new_transaction`) **يجب أن يحدد `concurrency_mode: OPTIMISTIC`، وإلا يُرفض.** فلا يقدر مستخدم أن يقفل مستندات 270 ثانية.
  - **المصدران يختلفان:** googleapis تقول «only allowed to create optimistic read-write transactions»، والنسخة المشحونة من `common.proto` (`@google-cloud/firestore` 9.3.1، السطران 61-62) تقول «Firestore does not allow 3rd party auth requests to create read-write. transactions.». فإن رفض الإنتاج كل معاملة read_write من طرف ثالث، كان قبول `OPTIMISTIC` أكثر تساهلًا منه.
  - **فالقرار مؤقت** حتى يُسجَّل الطلبان (العادي و`OPTIMISTIC`) من مستخدم اختبار يُنشئه المالك (`test/oracle/documents/README.md`، «Pending owner action»؛ ADR-028 §7.6). ومعهما اختبار عقد.
- **المعاملة المتفائلة على الخادم** (لمستخدم بـ `OPTIMISTIC`، أو لحساب خدمة طلبه صراحة) لا تأخذ أقفالًا: تسجل نسخة كل مستند تقرؤه ونتيجة كل استعلام. وعند `Commit` يُتحقق منها تحت أقفال عابرة في مدير الأقفال وأقفال صفوف الـ commit، وبـ `update_seq` لا بـ `update_time`، وأي انحراف `ABORTED`. وآلية التحقق في ADR-024 §6.4.

#### 7.3 التشاؤمي: لحسابات الخدمة والمدير وحدهم

- **هو نموذج القاعدة الافتراضي** (`concurrencyMode: PESSIMISTIC` في Standard، OF §2d، والاختبار §2.1). وهو ما تتوقعه Admin SDKs التي تستعمل المعاملات المدمجة.
- **الأقفال** (تفاصيلها في ADR-024 §6.1):
  - القراءة في معاملة read_write تأخذ قفلًا مشتركًا على المستند، موجودًا كان أو غائبًا.
  - **الاستعلام و`ListDocuments` داخل المعاملة يقفلان مجالهما، فيُمنع الـ phantom** (PL §4): استعلام المجموعة يقفل `(المسار الأب، معرّف المجموعة)`، أي تلك المجموعة وحدها؛ واستعلام collection-group يقفل معرّف المجموعة في القاعدة كلها؛ والاستعلام بلا معرّف مجموعة و`ListDocuments` بمعرّف فارغ يقفلان القاعدة.
  - **هذا أخشن من الإنتاج:** Spanner يقفل النطاقات التي يمسحها الاستعلام (OF §1a)، وAruma تقفل المجموعة كلها. فاستعلام `where x > 5` في معاملة Admin على `chats/A/messages` يوقف كتابة أي مستند في `chats/A/messages` حتى نهاية المعاملة أو مهلة الانتظار، لا كتابات `chats/B/messages`. وهو قيد في `parity-exceptions.json` (`coarse-query-locks`)، **يمس معاملات حسابات الخدمة والمدير التشاؤمية التي تستعلم وحدها**: معاملات المستخدمين النهائيين متفائلة لا تأخذ أقفالًا وهي مفتوحة (البند 7.2). ونموذج المحاكي، قفل معرّف المجموعة في كل مجموعة لكل استعلام (hidane، PA §A2)، أخشن منه.
  - **يُسجَّل في F2 من الإنتاج:** T1 تستعلم `c/A/sub` في معاملة، وW تكتب `c/B/sub/x` بلا معاملة، ثم مستندًا في `c/A/sub` خارج نطاق الاستعلام: هل تنتظر W؟ والنتيجة تحدّث صف القيد.
  - الكتابة عند `Commit` تأخذ قفلًا حصريًا. وكل كتابة أخرى، ومنها commit المستخدمين النهائيين بلا معاملة، تنتظر القفل.
  - الأقفال بترتيب الوصول (FIFO): «Transactions acquire their locks in chronological order» (OF §2d).
- **انتهاء الانتظار** يُنهي المعاملة المنتظرة بـ `ABORTED` ونص الإنتاج «Too much contention on these documents. Please try again.» (OF §2d؛ RC، s6). وGo لا يعيد إلا على `ABORTED` (CL §2.4).
- **الجمود يُحل بـ wound-wait، ومهلة الانتظار حدّ احتياطي** (ADR-024 §6.2):
  - المعاملة الأقدم التي تطلب قفلًا تُنهي الأحدث التي تمسكه ولم تبدأ الـ commit، بـ `ABORTED` ونص الإنتاج «Aborted due to cross-transaction contention. This occurs when multiple transactions attempt to access the same data, requiring Firestore to abort at least one in order to enforce serializability.». والأحدث تنتظر الأقدم.
  - **العمر يبدأ عند أول طلب قفل، لا عند البدء.**
  - **مؤكَّد بتسجيل f0-contention:** الأقدم تجرح الأحدث (s1، s3a)، والأحدث تنتظر (s2)، والمعاملة التي لم تقرأ شيئًا تُعدّ الأحدث (s3b)، كل منها 3 من 3 (RC؛ ADR-024 §6.2).
  - **الحل بالمهلة وحدها انهار في التجربة:** 6 commits في الثانية على 8 مستندات ساخنة وعلى عدّاد واحد، وتجويع أكثر من 3 ثوانٍ، مقابل 272–690 في الثانية بـ wound-wait (SP §3.4؛ ADR-024 §6.5).
- **مدير الأقفال داخل العملية** (`core/lockmgr.go`)، ولا اتصال Postgres يُحجز بين طلبين، و`FOR UPDATE` داخل معاملة الـ commit الأخيرة وحدها (RV، lock-design). تصميمه وأرقامه في ADR-024 §6.3: مهلة الانتظار 20 ثانية كالإنتاج (RC، s6)، وسقف 1,000 معاملة مفتوحة **لكل قاعدة** (تجاوزه `ABORTED`)، و10,000 قفل للمعاملة الواحدة (تجاوزه `ABORTED`) (SP §5).
- **`retry_transaction`** يُقبل، والمعاملة المعادة ترث عمر محاولتها الأولى، فتصير الأقدم مع الوقت ولا تُجوَّع (ADR-024 §6.2؛ SP §3.4). وسلوك الإنتاج فيه لم يُسجَّل بعد، وهذا قرارنا.

#### 7.4 القراءة فقط و`read_time`

- **`read_only{read_time?}`**، بـ `BeginTransaction` أو `new_transaction`: بلا أقفال، وكل قراءاتها من لقطة واحدة: `read_time` إن أُعطي، وإلا وقت آخر commit عند البدء (المتسلسل، ADR-024). ولا يعيدها العملاء (CL §2.3، §2.4).
- **`read_time`** بدقة الميكروثانية وضمن الساعة الماضية («within the past one hour»، OF §2d). وأقدم منها `FAILED_PRECONDITION`، بنص لم يُسجَّل في F0 فيُسجَّل في F2 (ADR-028 §7.6). ونافذة 7 أيام بالدقيقة الكاملة (PITR) تأتي مع مقبض الاحتفاظ في v0.4.x (ADR-028).
- **Node بـ `readOnly` و`readTime`** لا يبدأ معاملة، ويمرر `read_time` على كل قراءة (CL §2.3). فكل RPC قراءة يقبل `read_time` بالشروط نفسها.
- **الاستعلام والتجميع بـ `read_time` قديم** بالمسار المرجعي على جدول النسخ بسقف صفوف، وما يتجاوزه `FAILED_PRECONDITION` موثّق (PL §4، ADR-024).

#### 7.5 المعرّف والمهلات

- **`new_transaction`** يعيد `transaction` في أول رد، وإلا رفض Node كل القراءات المنتظرة بـ «Transaction ID was missing from server response» (CL §2.3؛ MUST 16-17).
- **المعرّف** معتم، **موثَّق بـ HMAC** بمفتاح مشتق من المفتاح الرئيس، فيبقى قابلًا للتحقق بعد إعادة التشغيل. يغطي الـ HMAC: رقم الصيغة، و**nonce الإقلاع** (جديد لكل عملية)، ورقم المعاملة، ووقت إصدارها، والمشروع والقاعدة والطالب (ADR-024 §6.2).
- **المهلات من الإنتاج:** 270 ثانية من البدء، و60 ثانية خمولًا منذ آخر RPC في المعاملة (OF §2a). **والخمول لا يُنهي المعاملة وحده:** في الإنتاج التزم commit بعد 50 و65 و70 ثانية خمولًا بلا منافس (RC، s5)، فبعد الستين تصير المعاملة قابلة للجرح من أي منتظر (ADR-024 §6.3). المنتهية تُحرَّر أقفالها.
- **الرد على معرّف معاملة، بحسب حاله** (على Commit وكل قراءة وRollback):

  | الحال | الرد | المصدر |
  |---|---|---|
  | مزوَّر أو تالف (الـ HMAC لا يطابق)، ومنه معرّف مشروع أو قاعدة أو طالب آخر | `INVALID_ARGUMENT` (HTTP 400، gRPC 3) «Invalid transaction.» | الإنتاج للمعرّف الذي لم يوجد قط، على commit وGET وrunQuery وrollback (`f0-errors.json:3597-3730`). فلا يكشف وجود معاملة في مستأجر آخر (PA §B8) |
  | أصيل ومجهول: صدر قبل إعادة تشغيل (nonce إقلاع آخر)، أو أُسقط مع فقد قفل النسخة (ADR-024 §7) | `ABORTED` (HTTP 409، gRPC 10) «The referenced transaction has expired or is no longer valid.» | قرارنا: الإنتاج لا يُعاد تشغيله. والنص نص الإنتاج لما بعد الـ commit. Go وNode يعيدان على `ABORTED`، فالمعاملة تُعاد بعمر جديد. قيد في `parity-exceptions.json` (`transaction-lost-on-restart`) |
  | أصيل ومنتهٍ (بعد 270 ثانية من وقت إصداره، أو كنسه المدير) | `ABORTED` بالنص نفسه | **مؤقت:** رد الإنتاج على الـ 270 ثانية لم يُسجَّل، ويُسجَّل في F2. والنص يحوي «transaction has expired» أيضًا، فيطابق ما يعيد عليه Node (MUST 18) |
  | أصيل التُزم أو رُدّ | `ABORTED` بالنص نفسه | الإنتاج بعد الـ commit (`f0-errors.json:3812`؛ RC، s5، 5 من 5) |

  و`Rollback` استثناء في الصفوف الثلاثة الأخيرة: يقبل التكرار والمنتهي والمجهول الأصيل دون خطأ (البند التالي).
- **`Rollback`** يحرر الأقفال، ويقبل التكرار ومعرّفًا منتهيًا دون خطأ، لأن Node يرسله بلا ضمان (CL §2.3).
- **لا حد لعدد الكتابات في الـ commit** منذ 2023-03-29، والطلب حتى 10 MiB، و500 تحويل للمستند في الـ commit (OF §2a).

### 8. القواعد قبل F5

من F1 إلى F4 لا قواعد `cloud.firestore` بعد. **فالرفض افتراضي لكل من ليس حساب خدمة أو مديرًا،** و`documents.dev_open_rules` يفتح القاعدة للتطوير المحلي **حين `public_url` و`listen` على loopback وحده**، كما `rtdb.dev_open_rules` (`aruma.example.yaml:31-33`؛ RV، minor `rules-default-before-f5`). وفي F5 تحل القواعد محل ذلك (ADR-021).

## البدائل المرفوضة

- **خدمة كل استعلام كالمحاكي:** يخفي الفهرس الناقص، فيعمل التطبيق على Aruma ويفشل على Firebase، ويترك مسحًا بلا فهرس يجري على Postgres حيث يرفضه الإنتاج (IX §4.2). وتوصية PA §A4 البند 5 بالفرض «off by default, matching the emulator» سبقت الاختبار، والاختبار حسمها.
- **الفرض الافتراضي `off` مع `enforce` اختياري:** أكثر تساهلًا من الإنتاج افتراضيًا. وخطر الانتقال الذي يدفع إليه (فهارس صُنعت من روابط اللوحة) يعالجه `warn` واستيراد الفهارس من المشروع الحي (ADR-028)، لا تعطيل الفرض للجميع.
- **رفض ما لم يُثبت (R18 إلى R22) احتياطًا:** يرفض ما قد يقبله الإنتاج، وهو الاتجاه الذي يكسر تطبيقًا منقولًا.
- **مخطِّط واحد بلا مسار مرجعي:** لا شيء يحكم عليه، وأخطاؤه تصير نتائج خاطئة بصمت.
- **التشاؤمي لكل من يطلبه:** أي مستخدم برمز صالح، أو مجهول، يقفل مستندات ساخنة 270 ثانية ويكرر. أكثر تساهلًا من الإنتاج (OF §2d، §1a؛ RV).
- **المتفائل للجميع كـ Kor:** يغيّر سلوكًا تراه اختبارات Admin (انتظار القفل ثم `ABORTED`، PA §B7)، ويقارن بـ `update_time` فيرث خلل Kor (PA §A1).
- **حل الجمود بمهلة الانتظار وحدها:** 6 commits في الثانية على المستندات الساخنة وتجويع (SP §3.4)، والإنتاج نفسه يجرح (RC؛ ADR-024 §6.5).
- **أقفال `pg_advisory_xact_lock` في معاملة Postgres مفتوحة عبر الطلبات:** تحجز اتصالًا حتى 270 ثانية لكل معاملة، فتستنزف المجمّع المشترك مع Auth وRTDB وStorage (RV، lock-design).
- **تجاهل `find_nearest` و`explain_options`:** نتائج خاطئة بصمت، أو انهيار Node.
- **`UNIMPLEMENTED` لكل findNearest:** يرد 501 حيث يرد الإنتاج نص الفهرس الناقص، والتطبيق بلا فهرس متجه يرى غير ما يراه على Firebase (البند 5).
- **الملء داخل معاملة الإنشاء** (المسودة السابقة): يسابق الـ commits الجارية فيصير الفهرس `READY` ناقصًا، وإعادة الفحص لا تمسك المدخل الغائب (البند 3؛ ADR-024 §2.5).
- **قفل معرّف المجموعة لكل استعلام، كالمحاكي:** معاملة Admin تستعلم `chats/A/messages` كانت توقف كتابات كل `*/messages` حتى 20 ثانية ثم `ABORTED`. ونموذج المحاكي ليس الإنتاج (البند 7.3).
- **المعرّف المجهول كالمنتهي دائمًا** (المسودة السابقة): يخالف الإنتاج في المزوَّر والغريب، ويخفي أن نص المجهول عند الإنتاج لا يعيد عليه Node (البند 7.5).

## العواقب

- **مخرج F3** (PL §6): جدول الأشكال الـ 105 يطابق الإنتاج حرفًا، حكمًا ونصًا ورابطًا، من ملف مسجّل يُعاد في CI بلا اتصال (ADR-028)؛ والعلاقات الثابتة تمر بعدّادات المسار؛ وfirebase-tools ينشر الفهارس ويحذف تكراريًا.
- **التطبيق المنقول يحتاج فهارسه:** إن صُنعت من روابط لوحة Firebase ولم تُصدَّر، يُصدّرها `firebase firestore:indexes --project <src>` من المشروع الحي، أو يعمل بـ `warn` حتى ذلك (IX §4.3). يُكتب في دليل الانتقال.
- **رابط الخطأ يفتح لوحة Firebase** لا لوحة Aruma. يُوثَّق قيدًا مقصودًا في `docs/compat/documents-v0.4.md`.
- **القبول غير المؤكد** قد يقبل ما يرفضه الإنتاج، فتطبيق بُني على Aruma قد ينقصه فهرس على Firebase. سطر السجل والمقياس يكشفانه.
- **كتابة المستخدمين النهائيين تنتظر أقفال معاملات Admin** حتى مهلة الانتظار، كما في الإنتاج. والمهلة وسقف المعاملات في ADR-024 §6.3.
- **سُجّل في F0:** findNearest بلا فهرس، وexplain، والمعرّف المجهول، والمعاملة بعد التزامها (البندان 5 و7.5). **ويبقى قبل كتابة الكود الذي يرده:** رفض المعاملة من مستخدم نهائي، والمعاملة المنتهية بعد 270 ثانية، و`read_time` القديم والمستقبلي، ومدى قفل الاستعلام في المعاملة، والفهرس قيد البناء، وخطأ الفهرس عبر gRPC (ADR-028 §7.6).
- **أربعة قيود جديدة** في `parity-exceptions.json`: `explain-rejected`، و`find-nearest-with-vector-index`، و`coarse-query-locks`، و`transaction-lost-on-restart`، ومعها صف مؤقت `index-still-building`.
