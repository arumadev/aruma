# B: دروس التصميم للغة القواعد (CEL، أبحاث Firebase، محركات السياسات)

- **الحالة:** بحث للمرحلة S0، 2026-10-23. يغذّي ADR-021 (محرك القواعد) وADR-023 (النطاق). لا يقرر شيئًا وحده.
- **المنهج:** قراءة المصادر الأولية فقط حيث أمكن: مواصفة CEL وكود cel-go (`gh api` و`raw.githubusercontent.com`)، أوراق منشورة، وثائق Firebase وKubernetes وCedar وOPA، وملفات hidane وPyric المحلية تحت `.cache/research/src/`. لا خوادم ولا محاكيات ولا Firebase حقيقي في هذا المسار.
- **العلامات:** [V] قرأته في المصدر الأولي في هذه الجلسة. [S] مصدر ثانوي (تقرير صحفي، ملخص بحث). [U] لم أتحقق منه. [I] استنتاجي أنا.
- **مكمّل لا بديل:** A (`A-oracle-findings.md`) يجمع الأوراكل، وADR-021 يكتبه مسار آخر. هنا الدروس فقط، وفي آخر الملف التوصيات.
- **قيد جديد من مراجعة ADR-021:** أي حساب ينشر قواعد مشروعه، فكل حد في المحرك يجب أن يصمد أمام **قواعد معادية** (ruleset كتبه مهاجم)، لا أمام طلبات معادية فقط. القسم 4 مبني على هذا.

---

## 1. CEL: ما تقوله المواصفة، وأين يختلف Firebase

### 1.1 التقييم والأخطاء في المواصفة

المصدر: `google/cel-spec` `doc/langdef.md` (الفرع `master`، جُلب 2026-10-23).

| الحقيقة | النص | المصدر |
|---|---|---|
| التقييم حتمي | «a CEL expression will deterministically evaluate to either a value or an error» [V] | langdef.md:604-605 |
| ترتيب التقييم غير مضمون | «the order of evaluation among sub-expressions is not guaranteed ... it is not specified which ones» (أي الخطأ يُعاد حين يخطئ أكثر من فرع) [V] | langdef.md:629-633 |
| لا التقاط للأخطاء | «no way to catch or bypass errors, except for the short-circuiting behavior of the logical operators, and macros» [V] | langdef.md:677-679 |
| `&&` و`\|\|` تبادليان | إذا حسم أحد الطرفين النتيجة «the other operand may or may not be evaluated, and if that evaluation produces a runtime error, it will be ignored. This makes those operators commutative». والسبب: «to allow the boolean operators to be mapped to indexed queries, and align better with SQL semantics» [V] | langdef.md:686-693 |
| جدول الحقيقة | `error \|\| true → true`، `error \|\| false → error`، `error && true → error`، `error && false → false` [V] | langdef.md:1386-1404 |
| الشرطي ليس تبادليًا | `true ? error : value → error`، `false ? error : value → value` [V] | langdef.md:1417-1420 |
| الكلفة جزء من المواصفة | «the time and space cost of evaluation is an essential part of the specification of the language» [V] | langdef.md:788-792 |
| مصدر الانفجار الوحيد في المساحة | «The concatenation operator `_+_` is the only operator that dramatically increases the space complexity» (`x + x + ... + x` بكلفة `O(B * P^2)`). والماكرو هي الطريق الوحيد للسلوك الأُسّي [V] | langdef.md:919-946 |

### 1.2 القيم المجهولة والتقييم الجزئي في cel-go

- README في cel-go يشرح «Partial State»: حين يكون المتغير غير معروف يُعاد `unknown`، والعاملان المنطقيان التبادليان يرفعان فرص النجاح: `<x> || true → true`، `<x> && false → false`، `<x> && <y> → <x, y>`. ثم `interpreter.Prune` يولّد «residual expression» من حالة التقييم (`OptTrackState`) [V] (`google/cel-go` README.md:169-215، commit `6d32ef54`).
- خيارات البرنامج [V] (`cel/options.go` عند الوسم `v0.29.0`): `OptPartialEval` «enables the evaluation of a partial state» (السطور 701-707)، `OptExhaustiveEval` يلغي القصر (691-692)، `CostLimit` «exit early with a "runtime cost limit exceeded" error ... indicative of CPU usage, not memory usage» (765-772)، و`InterruptCheckFrequency` لفحص الإلغاء داخل الحلقات (729-734). و`Env.EstimateCost` يقدّر كلفة تعبير مفحوص الأنواع ستاتيكيًا (`cel/env.go`:807-809)، و`Env.ResidualAst` يبني الشجرة المتبقية (785).
- **ترتيب الأولوية بين المجهول والخطأ** [V]: في `evalAnd.Exec` يُعاد `unknown` قبل `error` إن وُجدا معًا (`interpreter/interpretable.go`:390-419 عند `v0.29.0`). أي `<unknown> && <error>` يعطي unknown لا خطأ.
- ملاحظة صيانة [V]: فرع `main` في `google/cel-go` صار غلافًا (`go.mod` يستورد `cel.dev/cel-go v0.32.0`، وملفات `interpreter/` و`cel/` مولّدة كـ alias). أي اعتماد مستقبلي على cel-go يتبع وحدة جديدة؛ هذا يقوّي قرار hidane وخطتنا بعدم الربط بها [I].

### 1.3 أين يختلف Firebase Rules عن CEL

ملخص ADR 0005 في hidane (`hidane/docs/adr/0005-rules-engine.md`، Apache-2.0)، مسجّل من المحاكي v1.22.0 بلغة JVM إنجليزية:

| الاختلاف | الدليل |
|---|---|
| 130 من 578 حالة cel-spec لا تُترجم لأسباب معجمية: لا أُسس ولا hex في الأرقام، لا raw/triple strings، `--` رمز [V] | ADR 0005، جدول «What the official emulator does» |
| الدالة المجهولة تُترجم بتحذير وتفشل وقت التقييم؛ الأنواع تُفحص وقت التشغيل، والفاحص الستاتيكي يحذّر فقط، إلا regex حرفي لا يُترجم [V] | المصدر نفسه |
| `matches` مطابقة كاملة؛ `[1] == [1.0]` خطأ مع أن `1 == 1.0` صحيح؛ `1 in [1.0]` خطأ [V] | المصدر نفسه |
| امتصاص الأخطاء على طريقة CEL تمامًا: `error \|\| true` سماح، `error && false` false [V] | المصدر نفسه |
| **في الإنتاج:** Pyric سجّل من Rules Test API لقواعد Storage أن `&&` يمتص تبادليًا («Storage && absorbs commutatively like Firestore»)، وأن الخطأ يُبلَّغ عن **الطرف الأيسر** («two erroring operands of == → DENY, reported as the left one»، «two erroring method arguments → DENY, reported as the first») [V] | `pyric/packages/conformance/observations/storage-rules/rules-storage-error-absorption-and-direction.json` (observedAt 2026-09-28، SDK 12.13.0) |
| **ميزانية التعبيرات على مستوى الطلب لا الشرط:** «an error, then a true rule past the expression limit → DENY, reported as the error» [V] | الملف نفسه |
| **التقييم كسول من اليسار:** «lazy OR: true left branch skips unmocked lookup and allows»، و«short-circuited third distinct lookup does not deny» [V] | `.../storage-rules/rules-storage-firestore-lookup-budget.json` (2026-07-21) |

**ما يترتب [I]:** المواصفة تسمح بأي ترتيب، والإنتاج يقيّم من اليسار ويقصر عند الطرف الحاسم ويبلّغ عن الخطأ الأيسر. وما دام هناك عدّاد تعبيرات (1000) وعدّاد استدعاءات `get/exists`، فالترتيب **مرئي** للمستخدم: يغيّر هل نصل إلى الحد أم لا، وأي رسالة تظهر. إذن «تبادلي» صفة للنتيجة فقط، والترتيب الفعلي يجب أن يطابق الإنتاج: يسار أولًا، ولا يُقيَّم الأيمن إن حسم الأيسر.

### 1.4 هل نحتاج «residual» على طريقة cel-go؟

- hidane قاس نموذج `list` في المحاكي (#66): الاستعلام يُقسَّم إلى «ways»، وفي كل way يصير الحقل إمّا قيمة (حين يبقى `EQ` واحد) أو **constraint** من أزواج (`GT 0`، `NEQ 3`). والمقارنة مع constraint «is true when one of its filters alone implies it, else an error without a message ... never false» [V] (ADR 0005، قسم «`list` as the official emulator decides it»).
- أي أن المحاكي **لا يبني تعبيرًا متبقيًا**؛ هو يضيف نوع قيمة جديدًا (constraint) يعطي `true` أو خطأ، ثم يكمل التقييم العادي بامتصاص الأخطاء [V، عن المحاكي فقط؛ الإنتاج غير مقاس، U].
- قواعد Storage لا تحتاج list implication أصلًا: `list` في Storage تُقيَّم على مسار البادئة، وصفحة شروط Storage لا تذكر list ولا تصف `resource` فيه [V، firebase.google.com/docs/storage/security/rules-conditions]، وسلوكه الدقيق [U] يحسمه مسار الأوراكل A.

**ما يترتب [I]:** فكرة «المفسّر يعيد قيمة أو خطأ أو شرطًا متبقيًا» في الخطة صحيحة الاتجاه، لكن الشكل الأقرب للدليل هو **قيمة من نوع constraint** داخل نموذج القيم، لا residual AST مع `Prune`. وفي v0.3 لا يُبنى منها شيء: يكفي أن يكون نوع القيمة قابلًا للتوسعة (interface أو tagged union) فلا يحتاج F5 إعادة كتابة.

---

## 2. أبحاث سوء إعداد Firebase وما تعنيه لنا

### 2.1 القياسات المنشورة

| الدراسة | ما وجدته | الحالة |
|---|---|---|
| Zuo, Lin, Zhang، «Why Does Your Data Leak? Uncovering the Data Leakage in Cloud from Mobile Apps»، IEEE S&P 2019 (LeakScope) | الأسباب الجذرية: «lack of authentication, misuse of various keys ... or misconfiguration of user permissions in authorization»، وأدوات تحليل آلي للتطبيقات | [S] (الملخص عبر البحث؛ PDF في par.nsf.gov لم يُفتح: رفض الاتصال) |
| Appthority، تقرير «HospitalGown/Firebase» 2018 | 2,271 قاعدة مكشوفة خلف أكثر من 3,000 تطبيق، أكثر من 100 مليون سجل | [S] تقرير صناعي |
| Comparitech (Bob Diachenko)، أيار 2020 | 515,735 تطبيق Android، منها 155,066 تستعمل Firebase، و11,730 قاعدة مكشوفة؛ تقدير 4.8% من تطبيقات Firebase | [S] (تغطية BitDefender وغيرها؛ صفحة المصدر 404 اليوم) |
| Demissie, Ranise (FBK)، «Assessing the Effectiveness of the Shared Responsibility Model for Cloud Databases: the Case of Google's Firebase»، IEEE SMDS 2021 | أكثر من 50 ألف تطبيق: 763 بقاعدة مقروءة للعامة (~1 مليار تنزيل) و530/536 بقاعدة قابلة للكتابة للعامة (~630 مليون)؛ أداة إضافة Android Studio تفحص الإعداد | [S] (الـ preprint في cris.fbk.eu يرد 403 على الجلب الآلي) |
| Chattr ثم مسح 5 ملايين نطاق، آذار 2024 | **التسجيل كمستخدم جديد أعطى صلاحيات كاملة** على قاعدة Chattr؛ ثم 916 موقعًا، ~124.6 مليون سجل، منها ~20 مليون كلمة مرور؛ 24% فقط أصلحوا بعد التبليغ | [S] (SecurityWeek، The Register 2024-03-18) |

**ملاحظة منهجية [I]:** كل هذه القياسات تقيس RTDB/Firestore بالمسح الخارجي (`/.json` وما شابه). لم أجد دراسة أكاديمية تقيس قواعد Storage تحديدًا، ولا أداة تحقق رمزي (SMT) منشورة لقواعد Firebase. البحث في «formal verification / symbolic analysis Firebase security rules» لم يُرجع شيئًا [V: نتيجة البحث فارغة]. أقرب عمل صناعي: Zelkova في AWS لسياسات IAM (لم أقرأه في هذه الجلسة، [U]). وأداة مجتمعية `therturk35/firestore-rules-audit` (فحص ستاتيكي بثماني قواعد، SARIF) [S، لم أقرأ كودها].

### 2.2 الأنماط الخطرة كما توثّقها Firebase نفسها

من «Avoid insecure rules» و«Fix insecure rules» [V]:
- **وصول مفتوح:** Firestore `allow read, write: if true;`، Storage `allow read, write;`، RTDB `".read": true`.
- **أي مستخدم مسجّل:** `allow read, write: if request.auth != null;`. وهذا بالضبط نمط Chattr: التسجيل مفتوح فالشرط لا يحمي شيئًا [I].
- **بريد غير موثّق:** `request.auth.email.endsWith('@example.com')` دون `request.auth.email_verified`.
- **RTDB:** القاعدة الأعمق لا تسحب صلاحية منحها أب.
- Firebase يرسل «alert» حين تكون القواعد غير آمنة، ولا توثّق الصفحة قناته ولا معياره [V: الصفحة تقول «If you receive an alert ...» فقط].

### 2.3 القوالب الافتراضية في firebase-tools (v15.34.0 المحلي)

- Storage: `templates/init/storage/storage.rules`:6-12 يغلق كل شيء: `match /{allPaths=**} { allow read, write: if false; }` [V].
- Firestore: `templates/init/firestore/firestore.rules`:5-15 «test mode» بانتهاء: `allow read, write: if request.time < timestamp.date({{IN_30_DAYS}});` مع تعليق يشرح أن كل الطلبات تُرفض بعد 30 يومًا [V].
- دليل MCP في firebase-tools يقترح نسخة أضيق: `request.auth != null && request.time < timestamp.date(y, m, d)` (`src/mcp/resources/guides/init_firestore.ts`:51) [V].

### 2.4 ماذا يعني هذا للوحة التحكم ومحرر القواعد [I]

القاعدة الحاكمة: **التحذير لا يغيّر الدلالة.** ما يقبله Firebase نقبله، ونضيف تشخيصًا فوقه:

| التشخيص | الشرط (على الـ AST بعد الترجمة) | الشدة المقترحة |
|---|---|---|
| وصول عام كامل | `allow` على `read` أو `write` (أو تفرعاتهما) بشرط غائب أو ثابت `true`، تحت مسار يلتقط كل شيء (`{x=**}`) | خطأ مرئي في المحرر + شارة حمراء في لوحة المشروع (لا يمنع النشر) |
| «أي مسجّل» | الشرط الوحيد `request.auth != null` (أو `.uid != null`) على مسار لا يربط wildcard بـ `request.auth.uid` | تحذير، ونص يذكر أن التسجيل مفتوح افتراضيًا في Auth |
| بريد دون تحقق | `request.auth.token.email` مع `endsWith/matches` دون `email_verified` في الطرف نفسه من `&&` | تحذير |
| انتهاء test mode | مقارنة `request.time < timestamp.date(...)` بتاريخ: ماضٍ ← «القاعدة ترفض كل شيء منذ ...»، قريب (≤ 7 أيام) ← عدّ تنازلي | معلومة/تحذير |
| رفع بلا حد حجم (Storage) | `allow write/create` لا يذكر `request.resource.size` في أي فرع | تحذير (خطر امتلاء القرص/الحصة) |
| قراءة عامة لمسار يقبل أي contentType | `allow read: if true` ومعه `write` لا يقيّد `request.resource.contentType` | معلومة: الحماية الفعلية في ترويسات التنزيل (ADR-022) |

- **الافتراضي في Aruma:** قواعد Storage الجديدة مغلقة كقالب firebase-tools (ADR-010). إن قدّمت اللوحة «test mode» فهو قالب Firestore نفسه بتاريخ انتهاء (30 يومًا)، لا `if true` أبديًا، ومعه الشارة والعدّ التنازلي. ونطبّق حارس `dev_open_rules` الموجود (لا تشغيل مفتوح على غير loopback إلا بخيار صريح).
- **التشخيصات تُختبر** بجدول حالات، ولكل تشخيص حالة سلبية (قاعدة تشبهه وآمنة) حتى لا يصير المحرر مزعجًا.

---

## 3. دروس محركات السياسات على المسار الساخن

### 3.1 OPA/Rego

- **التقييم الجزئي:** هدفه «to convert non-linear-time policies into linear-time policies»، و«Optimization applies partial evaluation to precompute known values» [V] (openpolicyagent.org/docs/policy-performance). وواجهة Compile/data filtering تحوّل السياسة إلى شرط SQL `WHERE`، وOPA «returns the filter — it does not enforce it» [S، عبر docs/filtering].
- **الفرق الجوهري عن Firebase [I]:** OPA يحوّل السياسة إلى **مرشح** يطبّقه الاستعلام. Firebase يقول «rules are not filters»: يرفض الاستعلام كله إن أمكن أن يعيد مستندًا تمنعه القاعدة. فتقنية OPA لا تنتقل كما هي إلى F5؛ ما ينتقل هو فكرة «احسب المعروف مسبقًا» عند الترجمة (constant folding، ترجمة regex الحرفي مرة واحدة).
- **الحتمية داخل التقييم:** `time.now_ns` في OPA «set once at the start of the evaluation»، وRegal يحذّر من استدعائه مرتين [S، openpolicyagent.org/projects/regal/rules/bugs/time-now-ns-twice]. المقابل عندنا: `request.time` يُثبَّت مرة لكل طلب ويُمرَّر، لا `time.Now()` داخل المفسّر.
- **الفهرسة:** «The more effective the indexing is the fewer rules need to be evaluated» [V]. المقابل [I]: مطابقة `match` تُبنى مرة لكل ruleset (شجرة مقاطع ثابتة + wildcards)، لا مسح خطي لكل الكتل في كل طلب.

### 3.2 Cedar (AWS)

- اللغة مصممة «ergonomic, fast, safe, and analyzable»، وتسمح بـ«sound and complete logical encoding, which enables precise policy analysis» [V] (Cutler وآخرون، arXiv 2403.04651؛ نُشرت في OOPSLA 2024 [S]).
- **منهج البناء (verification-guided development):** نموذج تنفيذي مكتوب في Lean مع براهين، ثم **differential random testing** يقارن كود Rust الإنتاجي بالنموذج، وproperty-based testing لما لم يُنمذج. النتيجة: «found and fixed 4 bugs in Cedar's policy validator» أثناء البراهين، و«DRT and PBT helped us find and fix 21 additional bugs» [V] (Disselkoen وآخرون، «How We Built Cedar: A Verification-Guided Approach»، arXiv 2407.01688).
- **دلالات القرار:** «no request is authorized ... unless there is a specific permit policy»، و`forbid` يغلب؛ والسياسة التي تخطئ «does not factor into the authorization response; it is skipped» مع ذكرها في diagnostics [V] (docs.cedarpolicy.com/auth/authorization.html). وهذا يطابق Firebase في الأثر: خطأ في `allow` = لا سماح من هذا السطر، والسطور الأخرى ما زالت تُقيَّم (Pyric: «an error, then a true rule in the same block → ALLOW») [V].
- **ما ينتقل [I]:** لا نكتب براهين Lean، لكن نأخذ البنية: (1) **أوراكل** خارجي هو النموذج (الإنتاج عبر Rules Test API ثم المحاكي، لا نموذج نكتبه نحن)، (2) **مولّد عشوائي** لتعابير وقواعد صحيحة نحويًا يُقارن ناتجها بالأوراكل المسجّل، (3) **خصائص** (properties) لا تحتاج أوراكل، أهمها في 3.4.

### 3.3 Zanzibar (Google)

- الورقة: Pang وآخرون، USENIX ATC 2019. «trillions of access control lists and millions of authorization requests per second»، p95 أقل من 10 ms، توافر أعلى من 99.999% [V] (research.google، وPDF atc19-pang).
- **الاتساق:** مشكلة «new enemy» تُحل بـ zookie يحمل لقطة زمنية: «a check is always evaluated at a consistent snapshot no earlier than the given zookie» [V] (§2.2، §2.4).
- **الكاش:** «We avoid reusing results evaluated from a different snapshot by encoding snapshot timestamps in cache keys»، مع تقريب الطوابع الزمنية لزيادة الإصابة؛ و«lock table» لمنع «cache stampede»: «Among requests sharing the same cache key only one request will begin processing» [V] (§3.2.5).
- **ما ينتقل [I]:**
  - لا نخزّن **قرارات** بين الطلبات: مدخلات القرار (`request.time`، الرمز، `resource`، الطلب) تتغير كل مرة، ولا شيء عندنا بحجم Zanzibar يبرر ذلك.
  - نخزّن **الترجمة**: الـ ruleset المترجم لكل release، مبادَل ذريًا عند النشر، ومع `singleflight` (فكرة lock table) حتى لا يترجمه ألف طلب متزامن بعد النشر.
  - `firestore.get()` (من v0.4) يقرأ كل الوثائق في **لقطة واحدة** لكل تقييم، ويخزّنها داخل التقييم فقط (Firebase: المكرر لا يُحسب، وPyric يؤكد «three repeated calls to one document allow») [V].

### 3.4 Kubernetes: ميزانية الكلفة لـ CEL على مسار حرج

- «Cost units are ... deterministic; for any given CEL expression and input data, evaluation ... will always result in the same cost». المقارنة كلفتها 1، الـ list literal ‏40، و`matches` تقريبًا `length(regexString)*length(inputString)` لأسوأ حالة في RE2 [V] (kubernetes.io/docs/reference/using-api/cel، «Resource constraints»).
- **ميزانيتان:** runtime budget يوقف التقييم بخطأ، وميزانية **لكل عملية** تجمع كل التعابير (per-validation)؛ وقدير ستاتيكي يرفض حفظ تعبير أسوأ حالته باهظة [V].
- **ما ينتقل [I]:** ميزانية Firebase (1000 تعبير، 10/20 `get`) هي نفسها «per-validation budget» بلغة Kubernetes، وPyric أكد أنها لكل الطلب. أما **رفض الـ ruleset عند النشر بسبب كلفة تقديرية** فلا ننقله: Firebase لا يرفض بالكلفة، فنصبح أشد منه عند النشر ونكسر قواعد صالحة. التقدير الستاتيكي يصلح **تحذيرًا** في المحرر فقط.
- وهذا ما فعلناه في RTDB: `maxMatchCost = 1 << 26` = طول النص × عدد تعليمات برنامج regex، لأن 10 MB أمام نمط كبير حبس الكاتب 59 ثانية [V] (`internal/services/rtdb/rules/regex.go`:70-120).

### 3.5 خصائص تُختبر بلا أوراكل (metamorphic) [I]

1. **التبادل:** لكل `a && b` و`a || b` في حالة اختبار، قرار السماح لـ `b && a` و`b || a` هو نفسه (الرسالة قد تختلف، والعدّاد قد يختلف قرب الحد؛ فتُستثنى الحالات القريبة من 1000).
2. **إزالة السطور الخاطئة:** حذف `allow ...: if false` لا يغيّر أي قرار.
3. **إعادة ترتيب كتل `match`** وسطور `allow` لا يغيّر القرار (Firebase: أي سطر صحيح يكفي) إلا عند حدود الميزانية.
4. **الحتمية:** تقييم الطلب نفسه مرتين (و`GOMAXPROCS` مختلف) يعطي القرار والرسالة نفسيهما. وهذا يكشف تكرار التقييم المزدوج (#6252) لو تسرّب.
5. **الترجمة ثابتة:** `print(parse(src))` ثم `parse` مرة أخرى يعطي AST مساويًا (round-trip) — مفيد لمحرر اللوحة.

---

## 4. قواعد معادية: ما يجب أن يصمد أمامه المحرك

كل ما يلي [I] إلا ما عليه مصدر. المبدأ: كل حد إمّا يطابق حد Firebase الموثّق، أو أوسع منه بكثير ومسجّل في `docs/compat` كفرق، ولا يكون أبدًا «بلا حد».

| الهجوم بالقواعد | الأثر لو لم يُحد | الحد المقترح |
|---|---|---|
| ملف قواعد ضخم | ذاكرة ووقت ترجمة | 256 KB كحد Firebase (الخطة، القسم 1) |
| تعشيش عميق `((((...))))` أو `!!!!...` في محلل recursive descent | Go: «If any goroutine exceeds this limit while growing its stack, the program crashes» (افتراضي 1 GB) [V، `runtime/debug/garbage.go`:105-110]. الانهيار يُسقط **كل المشاريع**، وrecover لا يلتقطه | حد عمق في المحلل كـ `maxExprDepth = 200` في محلل RTDB (`rtdb/rules/parser.go`:8-9) [V]، ومثله لعمق `match` المتداخل |
| مضاعفة النصوص: `let a = s + s;` ثم `let b = a + a;` ... عبر دوال بعمق استدعاء 10/20 | كل `let` يضاعف، فـ 10 `let` × 20 مستوى ≈ 2^200 × حجم المدخل: OOM للعملية كلها. المواصفة تسمّي `+` المصدر الوحيد لانفجار المساحة [V، langdef.md:928-930] | ميزانية **بايتات** لكل تقييم (مجموع أحجام القيم المبنية) بجانب عدّاد التعابير؛ تجاوزها = خطأ (= رفض) |
| `matches()` بنمط ديناميكي من بيانات الطلب | ترجمة regex في كل طلب، وبرنامج كبير × نص كبير | سقف لطول النمط الديناميكي، كاش LRU محدود للأنماط المترجمة، و`maxMatchCost` نفسه لكل استدعاء |
| `split`/`replace`/`join`/`toSet`/`hasAll` على قوائم كبيرة | O(n·m) لو نُفّذت ساذجة | تنفيذ بخرائط (O(n+m))، واحتسابها في ميزانية الكلفة بحسب الحجم |
| `hashing.sha256` على نص كبير | وقت CPU | يُحسب في الميزانية بحسب البايتات |
| عدد `match` و`function` كبير | ذاكرة الترجمة لكل مشروع | يغطيه حد 256 KB؛ والـ ruleset المترجم يُخزَّن لكل release نشط فقط |
| `firestore.get()` من Storage | قراءات خارج الخدمة | حتى v0.4 يعيد خطأ (= رفض). بعده: الوثائق تقول «No more than two Firestore documents» [V، storage rules-conditions]، لكن Pyric سجّل عبر Rules Test API مع mocks أن ثلاثة مستندات مختلفة سُمح بها («three distinct documents deny => ALLOW») [V، ملف lookup-budget]؛ يحسمه الأوراكل قبل v0.4 |
| تقييم بطيء متعمّد ضمن الميزانية | يحجز goroutine الطلب فقط (لا كاتب مشترك في Storage كما في RTDB) | context بمهلة لكل تقييم، يُفحص كل N خطوة (كـ `InterruptCheckFrequency` في cel-go) |

وكل حد مما سبق يأتي باختبار fuzz: `testing.F` على المحلل (لا panic، ولا وقت فوق سقف لأي مدخل ≤ 256 KB)، وعلى المفسّر بقواعد مولّدة (لا panic، والميزانية تحترم دائمًا).

---

## 5. التوصيات لـ ADR-021 وADR-023

| # | القرار المقترح | الدليل | الثقة |
|---|---|---|---|
| R1 | **مفسّر يدوي** في `internal/platform/` (لا cel-go)، ونظام قيم فيه نوع `error` كقيمة وقابل للتوسعة بنوع `constraint` لاحقًا | hidane ADR 0005 (105/106 مقابل 91 لـ cel-rust)؛ اختلافات المعجم والمساواة والدوال (1.3)؛ cel-go صار غلافًا لوحدة جديدة (1.2) | عالية |
| R2 | **امتصاص CEL التبادلي للنتيجة، مع ترتيب تقييم ثابت يسار-أولًا وقصر عند الطرف الحاسم**، والإبلاغ عن الخطأ الأيسر/الأول | langdef.md:686-693؛ Pyric إنتاج (error-absorption، lazy lookups) | عالية |
| R3 | **عدّاد تعابير واحد لكل طلب** (1000) يمر عبر كل السطور والكتل، لا لكل شرط؛ وكل شرط يُقيَّم مرة واحدة (لا #6252) | Pyric إنتاج: «past the expression limit → DENY»؛ Kubernetes per-validation budget | عالية |
| R4 | لا residual AST ولا `Prune` في v0.3. نوع القيمة يسمح بإضافة `constraint` في F5 على نموذج المحاكي الذي قاسه hidane | ADR 0005 #66؛ Storage لا يحتاج list implication | متوسطة (نموذج list مقاس على المحاكي لا الإنتاج) |
| R5 | **حدود صامدة أمام قواعد معادية:** 256 KB، عمق المحلل، عمق الاستدعاء (قابل للضبط، 10 أو 20 حتى يحسمه الأوراكل)، 10 `let`، 1000 تعبير، **ميزانية بايتات** للقيم المبنية، `maxMatchCost` وسقف وكاش للأنماط الديناميكية، ومهلة context | القسم 4؛ `runtime/debug` (انهيار المكدس قاتل)؛ langdef §Performance؛ RTDB regex.go | عالية للمبدأ، متوسطة للأرقام الجديدة (البايتات، طول النمط) حتى تُقاس |
| R6 | لا رفض للقواعد عند النشر بسبب كلفة تقديرية؛ التقدير الستاتيكي تحذير في المحرر فقط | Kubernetes يرفض، Firebase لا (لا مصدر يذكر رفضًا بالكلفة)؛ «لا نكون أشد من Firebase في القبول» عكس «لا نكون أكثر تساهلًا» في القرار | متوسطة |
| R7 | `request.time` وكل «الآن» يُثبَّت مرة لكل طلب؛ لا مصدر وقت داخل المفسّر | OPA time.now_ns؛ CEL «deterministically evaluate» | عالية |
| R8 | كاش للـ ruleset **المترجم** لكل release مع `singleflight`، ولا كاش للقرارات بين الطلبات؛ `get/exists` (v0.4) بلقطة واحدة وكاش داخل التقييم | Zanzibar §3.2.5؛ Firebase «cached calls not counted» | عالية |
| R9 | **منهج اختبار على طريقة Cedar:** (أ) مقارنة تفاضلية بمدونة الأوراكل (الإنتاج أولًا)، (ب) مولّد قواعد عشوائي يُشغَّل ضد المحاكي صندوقًا أسود ويُسجَّل، (ج) خصائص metamorphic (3.5)، (د) fuzz للمحلل والمفسّر، (هـ) جزء محجوز لا يُرى أثناء البناء | Cedar: 4 + 21 خللًا بالبراهين وDRT/PBT؛ hidane held-out | عالية |
| R10 | **تشخيصات المحرر** (2.4) دون تغيير الدلالة؛ وقواعد Storage الافتراضية `if false`، و«test mode» إن وُجد بتاريخ انتهاء 30 يومًا وعدّ تنازلي | قوالب firebase-tools؛ Chattr؛ FBK/Comparitech؛ ADR-010 | عالية |
| R11 (ADR-023) | خارج v0.3: residual/list implication، `firestore.get` الفعلي، التحقق الرمزي (SMT) من القواعد. داخل v0.3: التشخيصات الست في 2.4 | الأدبيات لا تقدم أداة تحقق رمزي جاهزة لقواعد Firebase (2.1) | متوسطة |

### ما يخالف الخطة أو يدققها

1. **«شرط متبقٍ» (الخطة، ADR-021):** الدليل يشير إلى قيمة `constraint` لا residual expression. الاتجاه صحيح والشكل يتغير (R1، R4).
2. **«تقييم كل شرط مرة واحدة» غير كافٍ:** الميزانية لكل الطلب، والترتيب مرئي عبرها؛ يجب تثبيت ترتيب يسار-أولًا مع القصر، وإلا اختلف القرار قرب الحدود عن الإنتاج (R2، R3).
3. **حدود الخطة (عمق 20، 10 let، 1000، 256 KB) لا تكفي أمام قواعد معادية:** ينقصها حد للتعشيش في المحلل وميزانية بايتات للقيم (R5). بدونهما ruleset واحد يُسقط الخادم لكل المشاريع.
4. **`firestore.get` من Storage:** الخطة تذكر 10/20؛ صفحة Storage تقول مستندين، وتسجيل Pyric يخالف الصفحة. لا أثر على v0.3 (خطأ = رفض)، لكنه سؤال أوراكل قبل v0.4.
5. **عزل المشاريع:** ADR-017 أجّل RLS؛ أي جدول للقواعد (rulesets، releases) يحمل `project_number` ويمر بـ `scopecheck` واختبارات العزل، لا RLS كما تقول الخطة في القسم 4.
