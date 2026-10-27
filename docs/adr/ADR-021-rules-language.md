# ADR-021: محرك قواعد Firestore وStorage: لغة واحدة مكتوبة يدويًا في `platform/rules`

- الحالة: معتمد، وعُدّل في 2026-10-24 بقيم الإنتاج المقيسة (القسم الأخير)
- التاريخ: 2026-10-23 (ومراجعتان عدائيتان في اليوم نفسه، أُدخلت نتائجهما)
- المرحلة: S0 من الإصدار 0.3 (القواعد وStorage)
- المراجع: [rules-layout.md](../research/rules-layout.md)، ADR-008، ADR-010، ADR-015، ADR-017، ADR-019، ADR-020، و:
  - وثائق Firebase (قُرئت في 2026-10-23): [rules-language](https://firebase.google.com/docs/rules/rules-language)، [rules-conditions](https://firebase.google.com/docs/firestore/security/rules-conditions)، [rules-structure](https://firebase.google.com/docs/firestore/security/rules-structure)، [rules-query](https://firebase.google.com/docs/firestore/security/rules-query)، [quotas § Security rules](https://firebase.google.com/docs/firestore/quotas#security_rules)، [Storage core-syntax](https://firebase.google.com/docs/storage/security/core-syntax)، [Storage rules-conditions](https://firebase.google.com/docs/storage/security/rules-conditions)، [مرجع Storage](https://firebase.google.com/docs/reference/security/storage)، [rules.String](https://firebase.google.com/docs/reference/rules/rules.String)، [rules.debug](https://firebase.google.com/docs/reference/rules/rules.debug)، [manage-deploy](https://firebase.google.com/docs/rules/manage-deploy).
  - hidane (Apache-2.0/MIT، commit `abd4664` في 2026-10-19): `docs/adr/0005-rules-engine.md`، و`docs/parity-exceptions.md` (D008 وD020 وD059 وD060 وD113 وD114)، و`crates/hidane/tests/fixtures/rules_compile.json`، ووصف `crates/hidane-rules/src/{lexer.rs,paths.rs,eval/list.rs,eval/value.rs,eval/interp.rs,eval/library.rs}`. قُرئت أفكارًا، لا كودًا.
  - Pyric: [#150](https://github.com/davideast/pyric/issues/150) و[#151](https://github.com/davideast/pyric/issues/151) (محللان للغة واحدة انحرفا بصمت)، وتسجيلاته في `packages/conformance/observations/` (Rules Test API، 2026-07 إلى 2026-10).
  - cel-spec v0.25.3 (`tests/simple/testdata/logic.textproto`).
  - firebase-tools: [#6252](https://github.com/firebase/firebase-tools/issues/6252) (المحاكي يقيّم القاعدة مرتين، مفتوح منذ 2023)، و`src/rulesDeploy.ts:313-332` في 15.34.0 (صيغة أخطاء الترجمة).
  - cel-go: `cel.dev/cel-go` v0.32.0 (`github.com/cel-expr/cel-go`)، و`github.com/google/cel-go` صار مستودع توافق للقراءة فقط.
  - في مشروعنا: محرك قواعد RTDB (`internal/services/rtdb/rules`، و`rtdb/core/database.go:39`)، و`internal/store/scopecheck`.

## السياق

v0.3 يحتاج لغة قواعد Firestore وStorage: Storage يستهلكها في S2، وFirestore في F5 (v0.4). هي لغة واحدة: `rules_version` و`service` و`match` و`allow` و`function` و`let`، وتعابير مبنية على CEL. ما يختلف بين الخدمتين هو المتغيرات (`request` و`resource`) ودوال القراءة (`get()` في Firestore، و`firestore.get()` في Storage). أما قواعد RTDB فلغة أخرى (JSON وتعابير شبيهة بـ JavaScript)، ومحركها بُني في R4.

ما يجعل القرار صعبًا:

- **اللغة ليست CEL، وليس لها نحو منشور.** قاس hidane محاكي Firestore الرسمي (v1.22.0) على 926 حالة:
  - **المعجم أضيق من CEL:** لا أُسس ولا hex في الأرقام، ولا نصوص raw. و130 من 578 حالة cel-spec لا تُترجم أصلًا.
  - **وأوسع منه:** `is`، ومسارات حرفية `/a/$(x)`، والشرائح `[i:j]`، وفهرسة النص، والحساب المختلط بين int وfloat.
  - **ودلالته غير دلالة CEL:** `matches` تطابق النص كله، و`[1] == [1.0]` خطأ مع أن `1 == 1.0` صحيح، و`1 in [1.0]` خطأ.
  - **والأنواع تُفحص وقت التقييم لا وقت الترجمة:** الدالة المجهولة تُترجم بتحذير، وتفشل حين تُقيَّم.
  - **ودلالة الخطأ هي دلالة CEL:** `error || true` يسمح، و`error && false` يرفض بـ false، و`error || false` يرفض. هذا غير مكتوب في الوثائق، لكن تسجيلات hidane (المحاكي) وPyric (Rules Test API) تؤكده.
- **الوثائق تتناقض في حد واحد على الأقل:** عمق الاستدعاء 20 في جدول الحدود وفي rules-language، و10 في نص rules-conditions لـ Firestore ولـ Storage. وعدد `let` عشرة في rules-language، و«أي عدد» في صفحة شروط Storage. يقيس S0-A هذه الحدود.
- **Rules Test API ليس الإنتاج المنشور.** في تسجيلات Pyric سمح `:test` بثلاثة مستندات مختلفة من `firestore.get()` في Storage، والإنتاج المنشور يرفض الثالث (`storage-rules/rules-storage-firestore-lookup-budget.json`). ورسائله على الطلب المجهول «Property auth is undefined on object». فهو أوراكل للغة، لا للحدود ولا للمتغيرات.
- **قرار `list` في Firestore (list implication) أصعب جزء:** القواعد ليست مرشّحات، والاستعلام يُرفض إن أمكن أن يعيد مستندًا ترفضه القواعد. الوثائق لا تحدد التعابير التي تُحلَّل. ولا يُبنى قبل v0.4، لكن نموذج القيم الذي نختاره اليوم يجب ألا يمنعه.
- **قيود بنيتنا:** الخدمات لا تستورد بعضها (ADR-015). فاللغة المشتركة مكانها `internal/platform/`، و`firestore.get()` في قواعد Storage يمر بواجهة تربطها `internal/app`، كما في `idtoken`. والعزل بين المشاريع بعمود `project_number` وفحص `scopecheck` واختبارات العزل، وRLS مؤجّل (ADR-017).
- **أي حساب يستطيع نشر قواعد لمشروعه.** فالقواعد مدخل عدائي: قاعدة تبني نصًا يتضاعف، أو نمط مطابقة متفجّر، يجب ألا تُسقط الخادم على المشاريع كلها.
- **تجربتان نتعلم منهما:**
  - **Pyric** كتب محللين للغة الواحدة، واحدًا لـ Firestore وآخر لـ Storage. انحرفا بصمت: محلل Storage رفض `rules_version`، ولم ينتبه أحد لأن حالات اختباره فيها العمى نفسه. وما زال ينقصه عشرة أشكال نحوية (#150). والمحرك المشترك عندهم مقترن بشكل `resource.data` في Firestore.
  - **المحاكي الرسمي** يقيّم كل شرط مرتين (#6252). في المرة الأولى تفشل `resource.data` والقراءات دائمًا. والقراءات تُحسب مرتين على حدَّي 10 و20.

## القرار

### 1. محرك مكتوب يدويًا بـ Go، بلا cel-go

محلل معجمي (lexer)، ثم محلل نحوي (parser) ينتج شجرة (AST)، ثم فاحص (checker)، ثم مفسّر يمشي على الشجرة (tree-walking). نكتبها كلها بأنفسنا، بلا اعتماد خارجي، وبمكتبة Go القياسية وحدها: `regexp` وهو RE2 كما تشترط الوثائق، و`crypto/sha256` و`crypto/md5` لـ `hashing`، و`unicode/utf16`.

- **الدليل المقيس** (hidane ADR 0005، أربعة نماذج على مجموعة واحدة فيها 106 حالة محجوزة كُتبت بعد النماذج ولم تُستعمل لتعديلها):

  | النموذج | المحجوزة (106) | الباقي (820) | كود |
  |---|---:|---:|---:|
  | A: يدوي كامل | **105** | 709 | 1,405 سطرًا |
  | B: مكتبة CEL موسّعة بواجهتها | 91 | 608 | 692 |
  | C: محلل A ومفسّر CEL | 98 | 687 | محلل A ومكتبة B |
  | CEL كما هي | 62 | 486 | 0 |

  - مكتبة CEL في hidane هي cel-rust، لا cel-go. لكن الحدود التي أسقطت B وC موجودة في cel-go بالشكل نفسه، فالدليل ينتقل نوعًا وإن لم ينتقل رقمًا. يفصّلها البند التالي.
- **لماذا لا cel-go تحديدًا:**
  - **محلله مولَّد بـ ANTLR** (`parser/gen/CEL.g4`). فلا يقرأ `is` ولا المسارات الحرفية ولا الشرائح، ولا `match` و`allow` و`function` أصلًا. ويقبل ما ترفضه القواعد، كالأُسس وhex والنصوص raw. فنكتب محللًا على أي حال، وهذه نصف المحرك.
  - **العمليات مثبتة في مفسّره:** المساواة غير المتجانسة الحديثة في CEL تجعل `[1] == [1.0]` صحيحًا، والقواعد تقول خطأ. وللقواعد أربع علاقات مساواة لا واحدة (2.3). و`in` و`matches` والحساب المختلط مختلفة كذلك. وتغييرها يعني نسخة معدّلة (fork) من المفسّر. هذا ما أسقط النموذج C عند hidane.
  - **التقييم الجزئي في cel-go** (`PartialVars` والشجرة المتبقية) يبدو مفيدًا لـ `list`، لكنه لا يطابق نموذج المحاكي (البند 2.4). مثلًا: `n >= 1 && n <= 1` لا يستلزم `n == 1` عند المحاكي. فلا يوفر علينا شيئًا.
  - **الاعتمادات:** antlr4-go وprotobuf وgenproto وx/exp وx/text. وانتقلت الوحدة من `github.com/google/cel-go` إلى `cel.dev/cel-go` (المستودع القديم صار للتوافق فقط). خمس اعتمادات جديدة ومسار ينتقل، مقابل لا شيء.
  - **التشخيص:** نحتاج موضع كل خطأ (سطر:عمود) لـ `issues[]` التي تطبعها firebase-tools، وتتبعًا لكل تعبير لـ `:test` في S5. CEL لا يعطي موضعًا لأخطاء التقييم، ولا خطافات لتتبعها.
- **نحو واحد ومحرك واحد للخدمتين من اليوم الأول (درس Pyric):** لا محلل خاص بخدمة. الخدمة تختار اسم `service` وتقدّم متغيراتها وقراءاتها فقط (البند 3). واختبار يمرّر كل ملف من حالات الترجمة على الخدمتين: النحو لا يعرف الخدمة.
- **السلوك المرجعي** بترتيب مصادر الأوراكل في البند 6: المنشور أولًا، ثم Rules Test API للغة وحدها، ثم المحاكي صندوقًا أسود. ولا نعيد أخطاء المحاكي المعروفة، كالتقييم المزدوج (#6252).
- **`debug()` دالة هوية:** تعيد وسيطها ولا تفعل شيئًا. المرجع يقول: «`debug()` has no effect in production». أما `:test` فيرفضها («Function not found error: Name: [debug].»، hidane D113)، وهذا من خصائص Test API لا من اللغة (البند 6).

### 2. الحزمة وواجهتها: `internal/platform/rules`

اسم الحزمة `rules`. لا تتعارض مع `internal/services/rtdb/rules`، فلا حزمة تستورد الاثنتين. **وتستورد Go القياسية وحدها**، لا شيء من الداخل، وقاعدة archtest تفرض ذلك كما تفرضه على `rtdb/rules` (rules-layout.md).

#### 2.1 الترجمة: وقت النشر

```go
// Compile parses and checks one rules file for a service. The issues hold every error and
// warning in source order; the ruleset is nil when any issue is an error.
func Compile(svc Service, f File, lim Limits) (*Ruleset, Issues)

type Service string // "cloud.firestore", "firebase.storage"
type File struct{ Name, Content string }

// Issue is one finding, as the Rules API's issues[] carries it.
type Issue struct {
	File        string
	Position    *Position // nil for an issue about the whole file: the wire form has no position
	Severity    Severity  // Error or Warning
	Description string
}
type Position struct {
	Line, Column             int // 1-based
	CurrentOffset, EndOffset int // from the start of the file
}
type Issues []Issue
func (is Issues) Err() error // nil when no issue is an error

func (rs *Ruleset) Version() int   // 1 or 2
func (rs *Ruleset) Service() Service
```

- **`Ruleset` لا يتغير بعد بنائه، وآمن للاستعمال المتزامن.** وذاكرة الأنماط التي يحملها (البند 4) محمية بقفل.
- **وحدة الموضع:** العقد في الشجرة تحمل مواضع بالبايت. أما السطر والعمود والإزاحات في `Issue` فتُحسب عند إخراج الخطأ، بوحدة يحددها الأوراكل. وجد hidane أن المحاكي يعدّ code points. وما يعدّه الإنتاج يُقاس في S0-A، فإن خالف تتغير دالة التحويل وحدها.
- **الخطأ على الملف كله** (كتحذير غياب `rules_version`) لا موضع له: في الصيغة على السلك يُحذف `line` و`column` والإزاحات، ويبقى `fileName` (hidane، حالة «no version»). أما firebase-tools فتطبع حينها `undefined` (`rulesDeploy.ts:313-315`)، فهذا من شأنها.
- **ملف واحد:** `:test` في الإنتاج يرفض أكثر من ملف («Currently only one file source can be passed to firebase rules.»)، وfirebase-tools ترسل ملفًا واحدًا (hidane D114). فواجهة الترجمة تأخذ ملفًا واحدًا، وطبقة firebaserules في S5 تردّ رسالة الإنتاج على الأكثر.
- **ما يُرفض عند الترجمة (خطأ):**
  - أخطاء النحو، ومنها ما يقرؤه المعجم مسارًا: `4/2` عدد ثم مقطع مسار `/2`، فهو خطأ (hidane `lexer.rs:615-617`، كالمحاكي). أما `4 / 2` فقسمة.
  - `rules_version` غير `'1'` و`'2'`، و`service` لا يطابق الخدمة.
  - `let` في الإصدار 1: «Let bindings are not allowed in rules_version="1"».
  - نص حرفي غير صالح يُمرَّر إلى `matches` أو `split` أو `replace` (hidane: الفحص الثابت الوحيد للتعابير الذي يُفشل الترجمة عند المحاكي).
  - الاستدعاء الدوري بين الدوال (الحد 0).
  - **عمق الاستدعاء يُفحص على مخطط الاستدعاءات عند الترجمة:** أطول سلسلة استدعاء أكثر من الحد خطأ، برسالة المحاكي «Maximum allowed call depth of 20 is reached for [f0->f1->…->f20] call stack.» (`rules_compile.json`). فلا يُفحص العمق وقت التقييم.
  - **تداخل التعابير** (الأقواس والعمليات والقوائم والثلاثي) بعمق 99 أو أكثر: «Expression is too complex to evaluate safely.». و98 يُترجم. في `rules_compile.json` عند hidane: «parentheses 99 deep» و«operators 99 long» (99 `&&`) يعيدان 400، و«parentheses 98 deep» يعيد 200. والإنتاج يترجم `&&` بـ 98 طرفًا.
  - نص حرفي لـ `matches` أو `split` أو `replace` يتجاوز برنامجه المترجم حد التعليمات (البند 4).
  - تجاوز بقية الحدود الثابتة في البند 4.
- **ما يُحذَّر منه فقط:** الدالة المجهولة، والدالة غير المستعملة. وفي S1 لا ننقل كل تحذيرات الأنواع من المحاكي (2,145 سطرًا عند hidane). التحذير لا يغيّر قرارًا ولا يمنع نشرًا، ويُسجَّل هذا النقص في `docs/compat`.
- **الفحص على الإنتاج وحده:** تداخل طرق `allow` في الكتلة الواحدة، الذي تقول rules-language إنه خطأ، لا يُرفض حتى يثبته الأوراكل. رفض ملف قواعد يقبله Firebase يكسر النشر.

#### 2.2 التقييم: كل طلب

```go
func (rs *Ruleset) Evaluate(ctx context.Context, req *Request) (Decision, error)

type Request struct {
	Project   int64            // the project whose documents lookups read
	Method    Method           // Get, List, Create, Update, Delete
	Path      []Segment        // what the match blocks see, from the service root
	Variables map[string]Value // "request" and "resource", built by the service
	Documents Documents        // required; rules.NoDocuments{} where Firestore cannot be read
	Budget    *Budget          // Firebase's lookup limits and cache, one per request, shared by a batch
	Inflight  *Inflight        // required; the process-wide cap on evaluation memory, made once by app
	Trace     TraceLevel       // TraceNone, TraceStatements; TraceExpressions reserved for :test (S5)
}

type Decision struct {
	Allow      bool
	Statements []StatementResult // each allow statement evaluated, in order: position, method, true/false/error and message
}
```

- **`error` من `Evaluate` عطل فقط:** سياق أُلغي، أو `Documents` أعاد خطأ غير `ErrNoDocuments` (قاعدة لا تجيب مثلًا)، أو امتلأ الحد العام لذاكرة التقييم (`ErrOverloaded`، البند 4)، أو طلب غير صالح (`Documents` أو `Budget` أو `Inflight` فارغ). فتجيب الخدمة UNAVAILABLE ولا تسمح أبدًا. هذا تقسيم `idtoken` نفسه: الرفض قرار، والعطل خطأ.
- **القرار نفسه يأتي في `Decision`.** والرفض يصل العميل برسالة Firebase العامة، كـ «Missing or insufficient permissions.» في Firestore وما يسجّله S0 لـ Storage. أما `Statements` فللسجل، وللوحة، ولـ `:test`.
- **`Evaluate` يفحص `ctx`** بين التعابير (كل 64 تعبيرًا) وقبل كل قراءة، فالطلب الذي يتركه عميله يتوقف.

#### 2.3 نموذج القيم

قيمة القواعد نوع واحد `Value`. البنية الداخلية حرة لمن يكتبها في S1، والواجهة العامة ثابتة لأن الخدمات تبني بها `request` و`resource`:

- **الأنواع:** `Null` و`Bool` و`Int` (int64) و`Float` (float64) و`String` و`Bytes` و`List` و`Map` و`Set` و`MapDiff` و`Timestamp` و`Duration` و`LatLng` و`Path`.
- **ونوعان محجوزان لـ F5 من اليوم:** `Constraint` و`Unknown` (2.4).
- **دوال البناء:** `Null()` و`Bool(b)` و`Int(i)` و`Float(f)` و`String(s)` و`Bytes(b)` و`List(vs)` و`Map(m)` و`Timestamp(t)` و`Duration(d)` و`LatLng(lat, lng)` و`PathOf(segs)`.
- **`Auth(uid string, token map[string]any) Value`** تبني `request.auth` (`uid` و`token`)، و`Null()` للطلب المجهول.
  - في المنصة لا في كل خدمة، فشكل `request.auth` واحد في Storage وFirestore. وتأخذ أنواعًا أولية لا `*idtoken.Claims`، فيمر بها الأوراكل و`:test` والخدمات على السواء، والحزمة لا تستورد شيئًا من الداخل.
  - أعداد الرمز الصحيحة (`exp` و`iat` و`auth_time`) تصير int والباقي float، ويتحقق الأوراكل من ذلك.
- **النصوص:** UTF-8 في الذاكرة. لكن الطول والفهرسة والشرائح بوحدات UTF-16، كما يعدّها Java (hidane).
- **الأعداد:** طفح int64 خطأ، والقسمة على صفر خطأ.

**أربع علاقات مساواة، لكل منها مستعملوها.** لا تُقارن الأعداد داخل الحاويات بـ `==` في Go على float64، بل بدوال صريحة لكل علاقة:

| العلاقة | مستعملوها | الأعداد | الدليل |
|---|---|---|---|
| E1: `==` و`!=` في أعلى التعبير | `==` و`!=` بين قيمتين غير حاويتين | بالقيمة بين int وfloat: `1 == 1.0` صحيح | hidane |
| E2: مساواة الحاويات | `==` بين قائمتين أو خريطتين، تكرارًا في العمق | النوع يجب أن يطابق (`[1] == [1.0]` خطأ)، و`±0` قيمتان: `[0.0] == [-0.0]` رفض، و`[-0.0] == [-0.0]` سماح | Pyric `rules-corpus/firestore/cross-type-operator-overloads.ts:137` وتسجيله. **ويخالفه hidane:** `value.rs:317` يقارن الأعداد بـ `==` في IEEE، فيجعل `±0` واحدًا. والتسجيل يغلب |
| E3: عضوية القائمة | `in` على قائمة، ودوال القائمة (`hasAll` و`hasAny` و`hasOnly` و`removeAll`…) | النوع يجب أن يطابق (`1 in [1.0]` خطأ، hidane)، و`±0` واحد في العنصر نفسه: `-0.0 in [0.0]` سماح. أما العنصر الذي هو حاوية فيُقارن بـ E2: `[0.0] in [[-0.0]]` رفض | Pyric `observations/firestore-rules/rules-firestore-list-methods-concat-removeall-toset.json` |
| E4: هوية المجموعة | `toSet()`، و`in` على مجموعة، و`hasAny` وأخواتها على مجموعة، و`difference` و`union` و`intersection`، و`==` بين مجموعتين، و`diff()` | بالقيمة بين الأنواع (`1 in [1.0].toSet()` سماح)، و`±0` عنصر واحد، وNaN يساوي NaN (`hasAny` يجد NaN) | hidane `value.rs` (`loose_eq`)، وPyric `set-algebra-difference-union-intersection` |

وNaN في E1 وE2 وE3، وكل حالة لم يغطها التسجيل، يحسمها الأوراكل.

**الخطأ قيمة.** التقييم يعيد قيمة أو خطأ تقييم. والخطأ لا يوقف الشرط، بل ينتقل في الشجرة حتى يمتصه `&&` أو `||`. **والطرف غير المنطقي في `&&` و`||` يُعامل كالخطأ تمامًا**، برسالة «Type error. Received: [int] Expected: [bool].» (cel-spec `logic.textproto:71-74` و`:139-142`، وhidane `short_circuit_type_right`، وPyric `strict-boolean-control-flow`):

| التعبير | النتيجة |
|---|---|
| `true \|\| x` و`false && x` | `true` و`false`، و`x` لا يُقيَّم (قصر الدائرة، كما في الوثائق) |
| `false \|\| x` و`true && x` | `x` إن كان منطقيًا، وإلا فخطأ نوع |
| `e \|\| x`، حيث `e` خطأ أو غير منطقي (`'horses' \|\| true`) | يُقيَّم `x`: إن كان `true` فالنتيجة `true`، وإلا فخطأ `e` |
| `e && x`، حيث `e` خطأ أو غير منطقي (`32 && false`) | يُقيَّم `x`: إن كان `false` فالنتيجة `false`، وإلا فخطأ `e` |
| `c ? a : b` | يُقيَّم `c`، ثم **الفرع المختار وحده** (فلا قراءات إلا فيه). و`c` غير المنطقي أو الخطأ خطأ |
| `!e`، والعمليات والدوال على خطأ | خطأ |
| شرط `allow` | يمنح فقط إن كانت قيمته `true` تمامًا. الخطأ والقيمة غير المنطقية لا يمنحان |

- **`let` ووسائط الدوال تُربط قيمةً أو خطأً، ولا تفشل إلا حين تُقرأ.** في Pyric (الإنتاج عبر `:test`) `ignoresArg(1/0)` يسمح. ويقول hidane (`interp.rs:586-589`) إن المحاكي يقيّم الوسائط (الأخير أولًا) ثم `let` بترتيبها قبل `return`، ولا يُحسب خطأ أحدها إلا إن استعملته الدالة. فالتقييم مبكر والخطأ مؤجل. والوثائق نفسها تقول إن `let isAdmin = exists(...)` «enforces a lookup».
- **ترتيب تقييم الوسائط** (الأخير أولًا عند المحاكي) لا يغيّر قرارًا، لكنه يغيّر أي القراءات يُحسب أولًا حين يقترب الطلب من حده. يتبعه S1 كما قاسه hidane، ويتحقق منه الأوراكل.

#### 2.4 القيمة الجزئية حين تكون `resource.data` مجهولة: نحجز التصميم في S1 وننفّذه في F5

**القرار:** في S1 تُحجز ثلاثة أشياء، ولا يُبنى تقييم جزئي:

1. **نوعا القيمة** `Constraint` (ما يعرفه استعلام `list` عن حقل: أزواج مقارِن وقيمة، مثل `GT 0` و`NEQ 3` و`LIST_CONTAINS 'a'`) و`Unknown` (ما لا يُقيَّم أصلًا، كـ `request.path` في collection group). كلاهما في تعداد الأنواع.
2. **مقطعا مسار مجهولان** في `Request.Path`، وكلاهما في نوع `Segment` من اليوم (hidane ADR 0005، و`paths.rs` `Part::UnknownRun`):
   - **`UnknownSegment`:** معرّف المستند في `list`. يأخذه `{x}` أو `{x=**}`، فيبقى المتغير بلا قيمة، وقراءته خطأ.
   - **`UnknownRun`:** سلسلة مقاطع مجهولة الطول قبل اسم المجموعة في collection group. لا يأخذها إلا `{x=**}`، وأي مقطع حرفي أو `{x}` أمامها لا يطابق.
3. **قاعدة واحدة في المفسّر:** كل عملية أو دالة تتلقى `Constraint` أو `Unknown`، أو تقرأ متغيرًا أخذ مقطعًا مجهولًا، تعيد في S1 خطأً بلا رسالة، أي رفضًا. فكل `switch` على النوع له فرع افتراضي يرفض، ولا يمكن أن يسمح المحرك بشيء لا يفهمه.

وفي F5 تُضاف المقارنات على `Constraint` في ملف خاص بها (`constraint.go`)، كما قاسها hidane: المقارنة صحيحة إن استلزمها أحد مرشحات الحقل وحده، وإلا فخطأ بلا رسالة، لا `false`. ويضيف ربط Firestore تفكيك الاستعلام إلى «طرق»: كل فرع `or`، وكل قيمة من `in` و`array-contains-any`. ثم تقيَّم كل طريقة بـ `Evaluate` عادي على `allow list`. فلا تتغير واجهة المحرك، بل تضاف دوال بناء.

**ما بُني في F5 (B1):** `constraint.go` يضيف أربع دوال بناء، ويصفها `doc.go` للربط:

- `Constraint(filters...)`، ومرشحاته `Filter{Op, Value}` بالمقارِنات `FilterEQ` و`FilterNE` و`FilterLT` و`FilterLE` و`FilterGT` و`FilterGE` و`FilterNotIn` و`FilterContains`. أما `FilterIn` و`FilterContainsAny` فلا يستلزمان شيئًا، لأن الربط يفككهما إلى طرق.
- `Unknown()`.
- `PartialMap(fields)` لـ `resource.data`، ولحقل خريطة يُرشَّح على أحد حقوله (`a.b`). قراءة حقل معروف تعطيه، وكل ما سواها خطأ بلا رسالة: حقل غير معروف، و`size` و`keys` و`in` و`==` و`is` و`get`.
- `Query(limit, offset)` لـ `request.query`. فيها `orderBy` وكل حد غائب `Unknown`.

**المقارنات:** `==` و`!=` و`<` و`<=` و`>` و`>=` تأخذ Constraint في أي جانب. و`b in c` يستلزمه `array-contains`. ويُستلزم الحكم حين تتفق فيه رتبة Firestore والقواعد على كل وثيقة يطابقها المرشح:

- الأعداد دون ±2^53، أو اللانهائية.
- النصوص حين تكون محارف `b` كلها دون U+D800: هنا يتفق UTF-8 وUTF-16.
- bytes وtimestamp.
- null وbool في `==` و`!=` وحدهما.
- المرشح الذي لا يطابق شيئًا (`< false` و`> true` و`< ''` و`< b''`) يستلزم كل مقارنة.

**ما يبقى خطأً لأنه غير مقيس، وهو الأشد:**

- المقارنة بين نوعين مختلفين.
- الحاويات وNaN.
- `<` و`<=` من مرشح `<` أو `<=` عددي، لأن NaN أول الأعداد في Firestore.
- الأصفار في المقارنة الصارمة، لأن ترتيب -0.0 غير مقيس.
- `c in list`، وكل دالة على الـ Constraint.

**الحماية:** القوائم والخرائط الحرفية ترفض القيمة المحجوزة، وكذلك المساواة والتجزئة في العمق.

**الاختبار:** `TestConstraintNeverAllowsMore` و`FuzzConstraint` يقارنان المحرك بنموذج لمطابقة Firestore يحسم كل شك ضده. فما يسمح به المحرك لا ترفضه وثيقة يطابقها المرشح.

**لماذا لا تقييم جزئي كامل الآن:**

- **نموذج المحاكي ليس شجرة متبقية.** قاسه hidane (#66) فوجده قيمًا مقيّدة لكل حقل مع قاعدة استلزام ضيقة: `n >= 1 && n <= 1` لا يستلزم `n == 1`، ومرشح لا يطابق شيئًا يستلزم `== true` و`== false` معًا. ومحلل رمزي عام (DNF واختبار الاحتواء) يقرر هذه الحالات بغير ما يقرره Firebase، فيكون أكثر تساهلًا أحيانًا. والتساهل ممنوع.
- **ما يحتاجه `list` لم يوجد بعد:** نموذج الاستعلام (F3) وترميز القيم (F1). وتصميم الاستلزام قبلهما يجمّد واجهة خاطئة.
- **Storage، المستهلك الوحيد في v0.3، لا يحتاجه.** `list` في Storage لا يحمل مرشحات على البيانات.
- **ما يُحجز اليوم يكفي ليبقى F5 إضافة لا إعادة كتابة.** الأنواع والمقطعان موجودة، والفرع الافتراضي يرفض، و`Evaluate` نفسه يُستدعى لكل طريقة. وإن أظهر الأوراكل في F5 شيئًا لم يقسه hidane، تُضاف أنواع قيمة أخرى بالطريقة نفسها.

### 3. ربط الخدمات (bindings): المنصة لا تعرف الخدمات

- **الخدمة تبني متغيراتها:** `request` و`resource` قيم `Map` عادية، تبنيها الخدمة بدوال البناء:
  - Storage في S2 (ADR-022): `resource.name` و`bucket` و`size` و`contentType` و`metadata` و`timeCreated` وغيرها، و`request.resource` و`request.time` و`request.path`. **و`request.method` و`request.params`**، لأن قواعد إنتاج حقيقية تقرؤهما. يحدد S2 قيمهما لكل عملية من الأوراكل.
  - Firestore في F5: `resource.data` و`__name__` و`id`، و`request.resource.data` و`request.query`.
  - المحرك لا يعرف شكل أيٍّ منها. وهذا ما تجنّبناه من Pyric، حيث اقترن المفسّر بشكل `resource.data` في Firestore.
- **مسار الطلب:** الخدمة تعطي المقاطع كما تراها كتل `match` من جذر الخدمة: `databases/(default)/documents/c/d` في Firestore، و`b/<bucket>/o/images/a.png` في Storage. واسم الكائن يُقسم على `/`.
- **الطريقة:** الخدمة تختار `Method` لكل عملية. مثلًا: هل الكتابة فوق ملف موجود `create` أم `update` في Storage؟ يحسمه S2 بالأوراكل.
- **دوال القراءة يعرفها المحرك نفسه** (ستّ دوال معرّفة جيدًا)، والخدمة تفعّل ما يخصها:

  | الخدمة | الدوال |
  |---|---|
  | `cloud.firestore` | `get` و`exists` و`getAfter` و`existsAfter` |
  | `firebase.storage` | `firestore.get` و`firestore.exists` |

  - دالة مفعّلة في خدمة أخرى (`get()` في Storage مثلًا) تُعامل كدالة مجهولة: تحذير عند الترجمة، وخطأ عند التقييم.
  - العدّ والذاكرة المؤقتة في المحرك، لا في كل خدمة. فقاعدة «المخزَّن لا يُحسب» واحدة في مكان واحد.
- **واجهة القراءة:**

  ```go
  // Documents reads Firestore documents for rules. project is the project the request is
  // for; path is the whole document path, /databases/(default)/documents/...; after selects
  // the state after the request's pending writes (getAfter, existsAfter).
  type Documents interface {
  	Get(ctx context.Context, project int64, path []string, after bool) (doc Value, found bool, err error)
  }

  // ErrNoDocuments is what a Documents answers when Firestore cannot be read: an
  // evaluation error in the condition, never an outage.
  var ErrNoDocuments = errors.New("rules: Firestore documents are not available")

  // NoDocuments is the Documents Storage gets until v0.4: every lookup fails.
  type NoDocuments struct{}
  ```

  - **المشروع صريح في كل قراءة،** كما في `idtoken.Verify(ctx, project, token)`. فلا يقرأ تنفيذ `Documents` مشروعًا من مكان آخر، ولا يمكن أن تعبر قراءة إلى مشروع غير مشروع الطلب.
  - **صيغة واحدة لـ «لا مستندات»:** `NoDocuments{}`. و`Documents` الفارغ (nil) طلب غير صالح، لا مرادف لها.
  - **Firestore (F5):** تنفّذها على قراءتها، و`after` على طبقة الكتابات المعلّقة.
  - **Storage:** تتلقى `Documents` من `internal/app`. في v0.3 هي `rules.NoDocuments{}`، فكل `firestore.get()` خطأ تقييم، أي رفض ما لم يمتصه `||` كأي خطأ. وفي v0.4 تربط `app` خدمة Firestore بها، دون أن تستورد إحدى الخدمتين الأخرى (ADR-015).
  - **مسار خارج قاعدة البيانات:** مسار لا يبدأ بـ `/databases/(default)/documents/` يعيد خطأ تقييم. Storage لا يقرأ إلا القاعدة الافتراضية (الوثائق).
- **العدّ والذاكرة المؤقتة (`Budget`):**
  - مفتاح الذاكرة المؤقتة `(after, path)`. و`get` و`exists` على المسار نفسه وفي الحالة نفسها قراءة واحدة تُحسب مرة: «cached calls do not count».
  - **الحدود:**
    - Firestore: 10 لكل عملية، و20 للطلب كله في الدفعات والمعاملات والقراءات المتعددة.
    - Storage: مستندان من Firestore في التقييم الواحد («No more than two Firestore documents may be accessed in a single Rules evaluation»). والإنتاج المنشور يرفض الثالث، أما `:test` فيسمح به (Pyric)، فالحد من الوثائق والمنشور.
  - الخدمة تنشئ `Budget` واحدًا للطلب كله وتمرّره لكل عملية فيه، فيُحسب الحدّان معًا.
  - **الدفعة تتشارك حدود Firebase وحدها،** أي القراءات وذاكرتها المؤقتة. أما حدود Aruma (العمل والتخصيص، البند 4) فلكل عملية: كل `Evaluate` يبدأ عدّاده من الصفر، تحت الحد العام للعملية كلها. فدفعة كبيرة مشروعة لا تكون عندنا أشد مما هي في Firebase.
  - وهل المستند الذي تكتبه الدفعة نفسها مجاني لـ `get()`؟ عند المحاكي الجواب غير ثابت (hidane D020)، ويقرره F5 بالمنشور.

### 4. الحدود: إعداد بقيم افتراضية

القيم تحت `rules.limits` في الإعداد، كما `rtdb.limits`. و`internal/app` يحوّلها إلى `rules.Limits`. وهي نوعان:

**حدود Firebase** (دلالية): قيمتها جزء من التوافق. الإعداد يقبل قيمة مساوية لـ Firebase أو أدنى، ويرفض الأعلى عند الإقلاع:

| الحد | الافتراضي | المصدر | يُفحص |
|---|---|---|---|
| عمق استدعاء الدوال | **من الأوراكل**، و20 حتى يقيسه S0-A | جدول الحدود وrules-language وhidane عن المحاكي: 20. نص rules-conditions لـ Firestore وStorage: 10 | ترجمةً، على مخطط الاستدعاءات |
| `let` في الدالة | 10، و`let` في الإصدار 1 خطأ | جدول الحدود. وصفحة Storage تقول «أي عدد»، فيقيسه S0-A | ترجمةً |
| وسائط الدالة | 7 | جدول الحدود | ترجمةً |
| تداخل التعابير (أقواس، عمليات، قوائم، ثلاثي) | **98، وهو أكبر عمق مسموح.** و99 أول ما يُرفض | `rules_compile.json` عند hidane (99 يعيد 400، و98 يعيد 200)، والإنتاج يترجم 98 | ترجمةً |
| عمق `match` المتداخلة | 10 | جدول الحدود | ترجمةً |
| مقاطع المسار في `match` المتداخلة | 100 | جدول الحدود | ترجمةً |
| متغيرات المسار في `match` المتداخلة | 20 | جدول الحدود | ترجمةً |
| حجم نص القواعد | 256 KiB (262,144 بايت). والحد الدقيق (256,000 أم 262,144) يقيسه S0-A | جدول الحدود | ترجمةً |
| التعابير المقيَّمة في الطلب | 1000 | جدول الحدود. وحدة العدّ (كل عقدة تُقيَّم) تُطابَق بحالات `rules_expressions` من hidane والأوراكل | تقييمًا |
| قراءات المستندات | 10 للعملية، و20 للطلب، و2 لـ `firestore.*` من Storage | جدول الحدود ومرجع Storage | تقييمًا |

**حدود Aruma** (حماية): لا مقابل لها في Firebase، تحمي الخادم من قواعد عدائية، وقابلة للضبط بحرية. وتُوثّق في `docs/compat` كقيود (limitation):

| الحد | الافتراضي | لماذا | يُفحص |
|---|---|---|---|
| سلاسل الأعضاء والفهارس (`.a.b…` و`[0][0]…`) | 2,000 | المحاكي يقبل نحو 6,500 ثم يعيد 500. وhidane يحد بـ 2,000 برسالة «Expression is too complex to evaluate safely.» (D060) | ترجمةً |
| العمل في التقييم الواحد | 2^26 وحدة | **عدّاد واحد للتقييم كله، لا لكل استدعاء:** في RTDB أخذ استدعاء واحد عند الحد 0.9 ثانية، ويتسع حد 1000 تعبير لنحو 300 استدعاء. يُحسب عليه: `matches` و`split` و`replace` (طول النص × تعليمات البرنامج، فكرة `maxMatchCost` في RTDB)، وترجمة الأنماط المبنية وقت التقييم، وكل عنصر تلمسه دوال المجموعات. **وهو لكل عملية** من الدفعة لا للدفعة كلها (البند 3) | تقييمًا |
| حجم القيمة الواحدة | 2 MiB للنص والبايتات، و2^20 عنصرًا للقائمة والخريطة والمجموعة | مستند Firestore حده 1 MiB، فقيمة أكبر من ضعفه لا تأتي إلا من قاعدة تبني نفسها (30 مضاعفة لنص داخل حد 1000 تعبير تبلغ غيغابايتات) | تقييمًا |
| التخصيص في التقييم الواحد | 16 MiB، لكل عملية | مجموع ما تبنيه العمليات والدوال من قيم جديدة، ومعه بايتات البرامج المترجمة للأنماط المبنية وقت التقييم. ما تمرّره الخدمة (`resource.data`) لا يُحسب | تقييمًا |
| **ذاكرة التقييم في العملية كلها** (`Inflight`) | 512 MiB | مجموع تخصيص كل التقييمات الجارية معًا. 16 MiB لكل تقييم لا تحد شيئًا إن تزامنت ألف منها. وهو نظير `rtdb.limits.inflight_bytes` (512 MiB، `internal/config/config.go:103-105` و`:145`). تحجز كل تقييم ما يخصصه عند تخصيصه، وتُرد عند انتهائه | تقييمًا |
| برنامج النمط الواحد | 10,000 تعليمة | قيس: نمط من 27 KB بتكرار `\pL{1000}` يُترجم إلى 3,000,002 تعليمة، و742 MB تخصيصًا، و149 MB تبقى، في 356 ms. فيُقاس البرنامج بـ `regexp/syntax.Parse` ثم `syntax.Compile` **قبل** `regexp.Compile` | ترجمةً للنص الحرفي، وتقييمًا للنمط المبني |
| مقاطع مسار الطلب | 1,024 | اسم كائن Storage حتى 1,024 بايت (نحو 512 مقطعًا)، ومسار Firestore أقصر | عند الدخول إلى `Evaluate` |
| ذاكرة الأنماط لكل `Ruleset` | 256 نمطًا و4 MiB من البرامج المقدّرة، بقفل | الـ `Ruleset` مشترك بين الطلبات المتزامنة، والنمط الواحد قد يبلغ نصف ميغابايت (نحو 50 بايتًا للتعليمة، من القياس أعلاه) | — |
| الـ rulesets المترجمة في الذاكرة | 128 MiB، LRU **بالوزن لا بالعدد** | وزن الـ ruleset تقدير بايتاته: النص، وبرامج أنماطه الحرفية، وسقف ذاكرة أنماطه (4 MiB). فألف ruleset كبير لا يتسع لها ما يتسع لألف صغير. البند 7 | — |

- **تجاوز حد التقييم يُنهي التقييم كله برفض.** لا يصير خطأً يمتصه `||`. الوثائق تقول: «Exceeding either limit results in a permission denied error». أما المحاكي فيجعله خطأً عاديًا (hidane). وبما أن المنشور غير مقيس بعد، نختار الأشد. المفسّر يميّز نوعين من الخطأ (`evalError` يُمتص، و`abort` يُنهي)، فإن أظهر الأوراكل غير ذلك في حدود Firebase تغيّر موضع واحد. أما حدود Aruma فتُنهي دائمًا.
- **استثناءان من «تُنهي برفض»:**
  - **النمط المبني وقت التقييم إن تجاوز حد التعليمات** خطأ تقييم (`evalError`)، كأي نمط غير صالح: رفض ما لم يمتصه `||`. فهو عيب في قيمة، لا في كلفة الطلب.
  - **امتلاء `Inflight`** ليس قرارًا عن القواعد بل ضغط على الخادم. فيعيد `Evaluate` الخطأ `ErrOverloaded`، وتجيب الخدمة UNAVAILABLE. لا رفض، ولا انتظار.
    - **لماذا لا رفض:** الرفض `PERMISSION_DENIED` نهائي عند المكتبات، فيبدو كأن قواعد المطوّر خاطئة. أما UNAVAILABLE فتعيد المكتبات المحاولة بتأخير متزايد.
    - **لماذا لا انتظار على semaphore:** الانتظار تحت الحِمل يكدّس الطلبات واتصالاتها وذاكرتها، ويؤخر الجميع بلا حد. و`rtdb.limits.inflight_bytes` يرفض الرسالة الجديدة ولا ينتظر، فنتبع النمط نفسه.
    - وفي الحالتين لا يسمح المحرك بشيء.
- **العمليات والدوال تسجّل عملها وتخصيصها على عدّاد التقييم** (`meter`، ومنه على `Inflight`) قبل أن تبني القيمة، لا بعدها.
- **التعابير النمطية:**
  - **قواعد RE2 كما تقول الوثائق.** `regexp` في Go هو RE2، فلا ترجمة كما في RTDB. والمطابقة على النص كله (`^(?:…)$`).
  - **حجم البرنامج يُقاس قبل الترجمة،** بـ `syntax.Parse` و`syntax.Compile`، ولا يُستدعى `regexp.Compile` على برنامج فوق الحد.
  - **النص الحرفي يُترجم مرة عند الترجمة،** وفوق الحد خطأ ترجمة.
  - **والنمط الذي يُبنى وقت التقييم** يُحسب عمله على عدّاد العمل، وبايتات برنامجه على عدّاد التخصيص، ويُخزَّن في ذاكرة الأنماط.
  - **صيغة الاستبدال في `replace`** (`$1` كما في Java) يحددها الأوراكل.

### 5. ترتيب التقييم

1. **المطابقة:** تُجمع كتل `match` التي يطابق مسارها الطلب كاملًا، بترتيب ورودها في الملف (عمقًا أولًا). ولا تتسلسل القواعد: كتلة تطابق جزئيًا لا تمنح شيئًا بنفسها.
2. **الطرق:** في كل كتلة تُؤخذ عبارات `allow` التي تشمل طريقة الطلب:
   - `read` تشمل `get` و`list`.
   - `write` تشمل `create` و`update` و`delete`.
   - `allow` بلا شرط تمنح.
3. **التقييم:** تُقيَّم العبارات بترتيبها، **وكل شرط مرة واحدة فقط**. أول `true` يسمح ويُوقف التقييم، فلا تُحسب قراءات ما بعده. وإن لم يمنح شيء فالطلب مرفوض («No matching allow statements» إن لم تطابق أي عبارة).
4. **لا تقييم مزدوج (#6252):**
   - `resource` و`request.resource` تبنيهما الخدمة كاملين قبل `Evaluate`. فلا مرور أول تفشل فيه `resource.data`.
   - اختبار وحدة يعدّ مرات تقييم كل شرط وكل قراءة بـ `Documents` عدّادة: كلها 1.
   - ويُسجَّل الفرق في `docs/compat` بأنه «أصح من المحاكي، مطابق للإنتاج».
5. **`rules_version`:**
   - بلا تصريح، الإصدار 1.
   - **في الإصدار 1:** `{x=**}` يطابق مقطعًا واحدًا أو أكثر، ويجب أن يكون آخر مقطع في `match`. و`let` خطأ ترجمة.
   - **في الإصدار 2:** يطابق صفرًا أو أكثر، ويجوز في أي موضع، مع حد واحد لكل عبارة `match`. وقيمته `Path`، أما `{x}` فقيمته نص.
   - **في Storage:** `list` لا يُتاح إلا في الإصدار 2 (core-syntax). فعلى ملف من الإصدار 1 يرفضه ربط Storage، وهو يقرأ `rs.Version()`.
6. **ترجمة مسارات `match`:**
   - عند الترجمة تُسطَّح كل كتلة فيها `allow` إلى نمط مقاطع كامل من جذر الخدمة. كل مقطع حرفي، أو `{x}`، أو `{x=**}`، ومعه موضع كل متغير.
   - **`match` متداخلة تحت كتلة عبارتُها نفسها فيها `=**` غير أخير لا تطابق شيئًا أبدًا.** مثلًا: `match /{p=**}/groups/{g} { match /members/{m} {…} }`. فالتسطيح يجعل نمطها «لا يطابق»، لا التسلسل. أما `=**` الأخير في الكتلة الأم فيترك المتداخلة تطابق على أي عمق.
     - الدليل: Pyric عبر `:test`، `hierarchical-match-cascade`: «nested match under a nested/root groups document» رفض، و«nested match after a final recursive wildcard at the root/at depth» سماح.
     - والتسطيح الساذج كان سيسمح في الحالة الأولى، أي أكثر تساهلًا من Firebase.
   - **المطابقة بالبرمجة الديناميكية** على (موضع في النمط، موضع في المسار): O(n·m) لكل نمط، لا رجوع (backtracking) يتفجّر حين تتعدد `=**` في الكتل المتداخلة وتطول أسماء Storage. ويُستخرج الربط بترتيب «الأقصر أولًا» لأول `=**` من الجدول نفسه. وطول المسار محدود في المحرك (البند 4).
   - إن طابقت كتلة بأكثر من ربط للمتغيرات (`=**` في كتلتين متداخلتين بالإصدار 2)، يُؤخذ الربط «الأقصر أولًا». هذه حالة نادرة يتحقق منها الأوراكل.
7. **المسارات في الشروط:**
   - المسار الحرفي (`/databases/$(database)/documents/x`) يُبنى قيمة `Path`.
   - المقاطع الحرفية ونص `path()` يُفك ترميزهما من URL كما يقرؤه `URLDecoder` في Java: `%XX`، و`+` مسافة كذلك (hidane `interp.rs:1084-1085`).
   - `$(x)` بقيمة نصية مقطع واحد. وبقيمة `Path` تُدرج مقاطعها كلها في موضعها (hidane `interp.rs:663-664` و`:1085`، و`library.rs:155`).

### 6. الاختبار والقبول

- **مصادر الأوراكل ثلاثة،** ولكل حالة مصدرها في حقل `provenance`:

  | المصدر | ما هو | ما يحسمه |
  |---|---|---|
  | `deployed` | قواعد منشورة على مشروع حقيقي، وطلب حقيقي من مكتبة Firebase | كل شيء، وهو الأعلى |
  | `rules-test-api` | `firebaserules.googleapis.com/v1/projects/{p}:test` | **دلالة اللغة وحدها:** القيم والعمليات والدوال والمطابقة والترجمة. لا حدود القراءات، ولا شكل المتغيرات (`request.auth` و`resource`) |
  | `emulator` | المحاكي الرسمي صندوقًا أسود، بإصداره | ما لم يسجله غيره |

  - **عند التعارض:** `deployed` يغلب الكل. و`rules-test-api` يغلب `emulator` في دلالة اللغة وحدها. وفي حدود القراءات والمتغيرات، إن لم يوجد `deployed`، فالوثائق ثم المحاكي، ويُعلَّم الحكم «غير مؤكد».
  - **خصائص Test API المعروفة** (تُسجَّل في `docs/compat`، ولا نحاكيها):
    - الطلب المجهول يعطي «Property auth is undefined on object» بدل `request.auth == null` (Pyric `hierarchical-match-cascade`).
    - `get()` المحاكاة (`functionMocks`) لا تحمل `id` ولا `__name__`.
    - حد `firestore.*` في Storage لا يُفرض: ثلاثة مستندات تُسمح (Pyric `rules-storage-firestore-lookup-budget`).
    - `debug()` غير موجودة («Function not found error: Name: [debug].»).
    - ملف مصدر واحد فقط.
- **ملفات الأوراكل** في `test/oracle/rules/`، يجمعها S0-A ويملك صيغتها: حالات ترجمة وحالات تقييم. لكل ملف مصدره بأحد الأنواع الثلاثة، وتاريخه، وإصداره، وبصمة مدخلاته.
  - يقرأها `rulestest` في S1. وإن تغيرت الصيغة يتغير القارئ وحده.
  - حقول حالة التقييم: الخدمة، والمصدر، والطريقة، والمسار، و`auth`، و`resource`، و`request.resource`، والوقت، والمستندات التي تعيدها القراءات، والنتيجة `ALLOW`/`DENY`.
  - وحقول حالة الترجمة: نجاح أو فشل، و`issues[]`.
  - **ويضيف S0-A حالات المطابقة المتداخلة** (البند 5.6): `match` تحت `=**` غير أخير (رفض)، وتحت `=**` أخير على أي عمق (سماح)، بالإصدارين.
- **المجموعة المحجوزة ليست على فرع S1 أصلًا:**
  - تعيش على فرع منفصل `oracle-rules-heldout` (يعدّه S0-A)، فلا يمكن أن يقرأها بنّاء في S1 ولو خطأً.
  - قارئ `rulestest` يتخطى أي مجلد اسمه `heldout/` بالبناء، واختبار يثبت ذلك. وبذور fuzz تستثنيه.
  - في S1-E يُدمج ذلك الفرع في فرع قياس، ويُكتب `heldout_test.go`، وتُشغَّل المحجوزة مرة واحدة، ويُسجَّل الرقم في `docs/compat/rules-v0.3.md` قبل أي إصلاح.
  - بعدها تنضم إلى الحالات العادية، ويُجمع لـ F5 جزء محجوز جديد لحالات `list`.
- **القبول: 98% أو أكثر** على مجموعة التطوير، وعلى المحجوزة كل على حدة، ويُبلَّغ عن كل مصدر وحده.
  - حالة التقييم تطابق إن طابق القرار.
  - حالة الترجمة تطابق إن طابق النجاح أو الفشل، وموضع أول خطأ (سطر:عمود). ونص الرسالة يُقارن ويُبلَّغ عنه، لكنه لا يدخل النسبة.
  - **وكل حالة لا تطابق تُصنَّف** في `docs/compat/rules-v0.3.md`: خطأ عندنا يُصلح، أو فرق مقصود بسببه، أو خطأ في المحاكي، أو خاصية من خصائص Test API.
  - **ولا يُقبل أي فرق نكون فيه أكثر تساهلًا من المصدر الأعلى،** مهما كانت النسبة.
- **Fuzzing** على نمط RTDB، بزمن محدود لكل مدخل وبذور من حالات الأوراكل (دون المحجوزة):
  - `FuzzParse`: لا ذعر (panic)، والمواضع داخل النص، ومهلة ثابتة.
  - `FuzzEval`: طلبات عشوائية على قواعد البذور. لا ذعر، وقرار حتمي (تقييمان متطابقان)، ومهلة ثابتة.
  - `FuzzRegex`: الكلفة لا تتجاوز الحد.
- **اختبارات الحماية، لكل حد من حدود Aruma اختبار زمني:**
  - `TestConcatIsBounded`: قاعدة تضاعف نصًا داخل حد التعابير تُرفض قبل أن تتجاوز حد القيمة أو التخصيص، وفي زمن محدود.
  - `TestMatchesIsBounded` (كما في RTDB)، ومعه اختبار التقييم كله: أكبر عدد من استدعاءات `matches` يتسع له حد التعابير ينتهي برفض في زمن محدود.
  - اختبار مطابقة المسار: 1,024 مقطعًا أمام كتل متداخلة كثيرة `=**`، في زمن محدود.
  - **`TestConcurrentEvaluate`** تحت `-race`: طلبات كثيرة متزامنة على `Ruleset` واحد، فيها أنماط تُبنى وقت التقييم.
  - **`TestInflightIsBounded`** بجانبه: تقييمات متزامنة يبلغ مجموعها حد `Inflight` تعيد `ErrOverloaded`، وما تحجزه يُرد كله بعد انتهائها، فيقبل التقييم التالي.
  - **`TestRegexProgramIsBounded`:** نمط `\pL{1000}` المكرر يُرفض حرفيًا عند الترجمة ومبنيًا عند التقييم، دون أن يُستدعى `regexp.Compile`، وفي زمن وتخصيص محدودين.
  - **دفعة كبيرة:** عمليات كثيرة، كل منها قريبة من حد العمل، تمر كلها (العدّاد لكل عملية)، والقراءة الحادية والعشرون في الدفعة ترفض (الحد مشترك).
- **أداة مقارنة مع المحاكي:**
  - `tools/rules-diff` تولّد شروطًا من النحو وطلبات عشوائية ببذرة ثابتة. ترسلها إلى المحاكي الرسمي صندوقًا أسود وإلى المحرك، وتطبع أصغر حالة مختلفة.
  - تُشغَّل يدويًا وفي جدول CI الأسبوعي، لا مع `go test`.
  - على نمط أداة Kor، لكن مكتوبة لنا.
- **الاختبارات الجدولية في كل ملف، كعادة المستودع:**
  - جدول دلالة الخطأ أعلاه حالةً حالة.
  - وجدول علاقات المساواة الأربع.
  - وحدود كل نوع.
  - وUTF-16 بنص عربي وemoji.

### 7. أخطاء النشر وتخزين القواعد

- **صيغة الأخطاء:** `firebase deploy` تستدعي `POST /v1/projects/{p}:test` بالملف أولًا. ثم تطبع كل عنصر من `issues[]`:
  ```
  [E] 5:10 - <description>
  ```
  وتوقف النشر بـ «Compilation error(s) in <file>:» إن كان فيها خطأ، وتطبع التحذيرات دون أن تتوقف (`rulesDeploy.ts`).
  - شكل العنصر: `sourcePosition{fileName, line, column, currentOffset, endOffset}` و`description` و`severity` (`ERROR` أو `WARNING`). والخطأ على الملف كله يحمل `fileName` وحده (2.1).
  - `Issue` في البند 2.1 يطابقه حقلًا حقلًا. ونصوص `description` تُنسخ من الأوراكل.
  - **اللوحة (S5)** تعرض القائمة نفسها على أسطر المحرر، وترفض الحفظ إن كان فيها خطأ.
- **التخزين (يبنيه S2 أقلّه، ويكمله S5 بواجهة firebaserules):**
  - **المكان:** جدولان في المخطط `control`، يملكهما `tenancy`، لا خدمة. واجهة الإدارة (`internal/admin`) والخدمتان تستوردان `tenancy`، ولا تستوردان بعضها (ADR-015).
  - **العزل كبقية جداول `control`، بلا RLS (ADR-017):**
    - كل جدول فيه `project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE`.
    - الجدولان في قائمة `TestQueriesAreProjectScoped` (`internal/tenancy/queries/scope_test.go`)، فكل استعلام عليهما يقيّد بـ `project_number` أو يعلن نطاقه (`scopecheck`).
    - واختبارات عزل في `test/acceptance`: مشروع لا يقرأ ruleset مشروع آخر ولا إصدارته، ولا ينشر عليهما، ولا يشير إصداره إلى ruleset غيره.
  - `control.rulesets(project_number, id, files jsonb, create_time)`:
    - غير قابل للتعديل.
    - الاسم `projects/{p}/rulesets/{uuid}`.
    - حتى 2,500 لكل مشروع، كحد Firebase (manage-deploy)، قابلة للضبط.
  - `control.rules_releases(project_number, name, ruleset_id, create_time, update_time)`:
    - الأسماء: `cloud.firestore` و`firebase.storage/<bucket>`.
    - إصدار يشير إلى ruleset من مشروعه نفسه، والنشر تحديث للإشارة.
  - المحمي لا يُحذف: ruleset تشير إليه إصدارة.
- **من يملك الترجمة والذاكرة المؤقتة: `tenancy/core`** (يستورد `platform/rules`، وهذا مسموح):
  - `CompiledRelease(ctx, project, name) (*rules.Ruleset, error)` تعيد الـ ruleset المترجم للإصدارة.
  - الذاكرة المؤقتة بمفتاح `(project, name)`، وLRU بالوزن تحت 128 MiB (البند 4): وزن كل ruleset تقدير بايتات نصه وبرامج أنماطه الحرفية وسقف ذاكرة أنماطه. والنشر عبر `tenancy` نفسه يبطل مدخل إصدارته، فلا حاجة لآلية إعلام بين الحزم. بخادم واحد كما في ADR-019، وتعدد الخوادم يحتاج ADR.
  - **إصدارة غير موجودة، أو ruleset مخزَّن لم يعد يُترجم** (حدود أو لغة تغيرت بين إصدارين من Aruma) **ترفض كل شيء**، كما في RTDB (`rtdb/core/database.go:39`: «nil when the stored rules do not parse, which denies all»)، ويُسجَّل تحذير.
  - المحاكي ينشر فورًا. أما Firebase فيقول إن النشر يأخذ «several minutes». نحن ننشر فورًا، وهذا لا يجعلنا أكثر تساهلًا.
- **القاعدة الافتراضية مقفلة (ADR-010):** المشروع الجديد يأخذ لـ Storage `allow read, write: if false;`، كوضع الإنتاج المقفل في Firebase. وحسابات الخدمة (Admin) تتجاوز القواعد كما في Firebase.

## البدائل المرفوضة

- **cel-go:**
  - النحو مولَّد ولا يقبل توسعة (`is`، والمسارات، والشرائح، و`match`/`allow`).
  - العمليات مثبتة وتخالف القواعد، فتعديلها نسخة معدّلة من المفسّر.
  - خمس اعتمادات جديدة، ووحدة غيّرت مسارها.
  - تقييمه الجزئي لا يطابق نموذج `list` عند المحاكي.
  - والنظير المقيس (cel-rust) طابق 91 من 106 مقابل 105 للمحرك اليدوي.
- **محللنا ومفسّر cel-go (النموذج C):** كل كود المحلل، ويبقى الفشل حيث تختلف العمليات: 98 من 106.
- **محلل مولَّد (ANTLR أو PEG) بدل اليدوي:** أداة بناء جديدة وكود مولَّد في المستودع. ورسائل الخطأ والمواضع تحتاج يدًا على أي حال، كما في محرك RTDB. واللغة صغيرة.
- **محرك لكل خدمة، أو محلل مشترك ومفسّر لكل خدمة:** هذا خطأ Pyric (#150): انحراف صامت بين محللين، ومفسّر يقترن بشكل بيانات خدمة. الاختلاف بين الخدمتين في المتغيرات والقراءات، وهي بيانات تُمرَّر لا لغة تُكتب.
- **إعادة استعمال محرك RTDB أو بعض حزمه:**
  - اللغة مختلفة: JSON وتعابير شبيهة بـ JavaScript، و`===`، وregex حرفي `/…/` بصيغة Firebase يُترجم إلى Go، ولقطات `data`/`newData`، وقواعد تتسلسل.
  - والأنواع فيه تُفحص عند التحميل، وهنا تُفحص وقت التقييم.
  - **ما يُنقل نمطًا لا كودًا:**
    - lexer يدوي يسمي ما لا تدعمه اللغة بالاسم.
    - مواضع بالبايت تُحوَّل إلى سطر:عمود عند الإخراج.
    - حد كلفة `matches` واختباره الزمني `TestMatchesIsBounded`، وقد صار هنا عدّادًا للتقييم كله، ومعه حد لحجم برنامج النمط.
    - أهداف fuzz بمهلة وبذور من الحالات المسجّلة.
    - ملفات golden بمصدرها وإصدارها.
    - مصدر بيانات في الذاكرة للاختبار (`MemorySource`، وهنا `MemoryDocuments`).
    - القواعد المخزّنة التي لا تُحمَّل ترفض كل شيء.
- **تقييم جزئي كامل في S1:** يخالف نموذج المحاكي المقيس، ويُبنى قبل نموذج الاستعلام الذي يحتاجه، ولا يستهلكه Storage (البند 2.4).
- **الخطأ يرفض الشرط كله فورًا (كما في محرك RTDB):** يخالف ما سجّله hidane من المحاكي وPyric عبر `:test`: `error || true` يسمح. وهذا الرفض يرفض قواعد يقبلها Firebase.
- **وسيط الدالة الخاطئ يُفشل الاستدعاء:** يرفض `ignoresArg(1/0)` الذي يسمح به الإنتاج.
- **تقييم على مرحلتين كالمحاكي:** خطأ مسجّل (#6252)، يضاعف القراءات المحسوبة، ويغيّر رسالة الرفض.
- **المطابقة بالرجوع:** أسيّة حين تتعدد `=**` في الكتل المتداخلة. والبرمجة الديناميكية تعطي النتيجة نفسها في زمن خطي في حجم الجدول.
- **حد كلفة لكل استدعاء `matches` وحده (كما في RTDB):** يتسع حد التعابير لمئات الاستدعاءات، فيصير الطلب الواحد مئات الثواني.
- **عدّادا العمل والتخصيص للدفعة كلها:** يجعلان دفعة كبيرة مشروعة ترفض عندنا وتمر في Firebase، أي أشد منه بلا سبب في القواعد. والحماية من التزامن يؤديها `Inflight`.
- **انتظار على semaphore حين يمتلئ `Inflight`، أو رفض:** الأول يكدّس الطلبات بلا حد، والثاني يبدو للمكتبات خطأً نهائيًا في القواعد. فالجواب UNAVAILABLE (البند 4).
- **حد الأنماط بطول النص لا بحجم البرنامج:** 27 KB تكفي لثلاثة ملايين تعليمة. الطول لا يقيس الكلفة.
- **حفظ الـ rulesets في كل خدمة:** واجهة firebaserules واحدة للخدمتين، ولا تستطيع الإدارة استيراد خدمة. و`tenancy` يملك ما يخص المشروع أصلًا.
- **RLS على جدولي القواعد:** مؤجّل لكل المشروع (ADR-017)، ولا سياسة RLS في أي migration. فالعزل هنا كبقية جداول `control`.
- **`Auth(*idtoken.Claims)`:** يجعل الحزمة تستورد `idtoken`، ولا يمر به الأوراكل ولا `:test` اللذان لا يملكان `Claims`.
- **تجاوز حد القراءات أو التعابير خطأً يُمتص:** أكثر تساهلًا من نص الوثائق إن كان المنشور يرفض الطلب كله. نختار الأشد حتى يقاس.

## العواقب

- **محرك واحد** في `internal/platform/rules`، يستهلكه Storage في S2 وFirestore في F5، دون أن تعرفه أي خدمة من الداخل، ودون اعتماد خارجي جديد.
- **S1 مستقل عن Storage:** يُختبر بالأوراكل وحده. وتقسيمه إلى مهام متوازية بواجهات ثابتة في [rules-layout.md](../research/rules-layout.md).
- **قواعد عدائية لا تُسقط الخادم:** عدّاد عمل وتخصيص لكل تقييم، وحد عام لذاكرة التقييمات المتزامنة، وحد للقيمة الواحدة ولبرنامج النمط الواحد، ومطابقة مسار خطية، وذاكرة rulesets بالوزن، وفحص `ctx`. لا شيء منها يسمح، ولكل منها اختبار.
- **قيود معروفة تُوثّق في `docs/compat/rules-v0.3.md`:**
  - التحذيرات أقل من المحاكي.
  - حدود Aruma (العمل، والتخصيص، و`Inflight`، وحجم القيمة، وبرنامج النمط، وطول المسار، وسلاسل الأعضاء).
  - النشر الفوري.
  - خصائص Test API التي لا نحاكيها.
  - وكل حالة أوراكل لا تطابق، بتصنيفها.
- **`firestore.get()` في قواعد Storage يرفض حتى v0.4،** إلا أن يمتص `||` الخطأ. وv0.4 يغيّر سطرًا في `internal/app` لا في Storage.
- **F5 إضافة:** دوال بناء `Constraint`، و`constraint.go`، وتفكيك الاستعلام في ربط Firestore. ولا تتغير `Evaluate` ولا نموذج القيم ولا نوع المقاطع.
- **قيم غير محسومة بعد،** كلها في `Limits` أو في موضع واحد من الكود: عمق الاستدعاء، و`let` في Storage، ووحدة العمود، وامتصاص أخطاء الحدود، والحد الدقيق للحجم، وNaN في علاقات المساواة الثلاث الأولى. فإن قاسها S0-A أو الأوراكل لاحقًا تغيّر الافتراضي، لا التصميم.
- **ما لم يُقرر هنا:** جداول `rulesets` و`releases` بتفاصيلها (S2 وS5)، وشكل `request` و`resource` في Storage بما فيه `request.method` و`request.params` (ADR-022 وS2)، وواجهة firebaserules (S5).

## تعديل 2026-10-24 (S1-E): قيم الإنتاج المقيسة

كُتب هذا القرار قبل أن يقيس S0-A الأوراكل، وقبل مراجعات S1-A وB وC وD ومسابيرها على Rules Test API
(`projects.test` على `chatapp-8ae21`، بلا نشر). ما يلي قيم قيست بعده، وهي تغلب ما يخالفها أعلاه. والمحرك
يطبّقها كلها، ونسبة التطابق في [rules-v0.3.md](../compat/rules-v0.3.md): ‏100% على Test API و99.9% على المحاكي
في مجموعة التطوير، و99.2% و99.6% على المحجوزة، بلا سماح زائد.

**حدود الترجمة:**

| ما قيس | القيمة | يغيّر |
|---|---|---|
| `let` في الدالة | 11 تُترجم، و12 خطأ «Maximum allowed variable count of 10 …». والخطأ عند ما يلي الـ `let` الثانية عشرة: الثالثة عشرة، أو تعبير `return` إن لم تكن (`lets_12` عند L4:177، و`lets_21` عند L4:170) | جدول §4: «10» يبقى اسم الحد، والمقبول 11 |
| تداخل التعابير، لكل بناء وحده | `&&`: ‏99 تُترجم و100 تُرفض. والأقواس: 98 و99. والثلاثي `?:`: ‏48 و49. **و`-` الأحادية لا تُعدّ في العمق** إلا على قوس (`-(x)` يُعدّ بقوسه)، **و`!` تُعدّ** | جدول §4: «98» كان للأقواس وحدها |
| الحرفي `1e3` | لا يُترجم | — |
| سلسلة الاستدعاءات | 21 دالة تُترجم، و22 خطأ ترجمة عند الثانية والعشرين يسمّي المكدّس. **وأي عودية خطأ ترجمة**، مباشرة أو في دورة، ولو لم تُستدعَ الدالة | جدول §4: «20 حتى يقيسه S0-A» |
| حجم المصدر | حجم رسالة `Source` في protobuf، `Source{files: [File{content: 1, name: 2}]}`، لا يزيد على 262,144 بايتًا. أي المحتوى والاسم و10 بايتات لاسم دون 128 بايتًا، و11 من 128 فأكثر لأن طول الاسم يُكتب في بايتين: `firestore.rules` يقبل 262,119 بايتًا من المحتوى ويرفض 262,120، وقاس S1-E أسماء من 1 و100 و127 و128 و129 و300 بايت فتحرّك الحد بطول الاسم، وببايت زائد من 128 | جدول §4: «256,000 أم 262,144». و«25 بايتًا» في `A-oracle-findings.md` كان سهوًا في الحساب: 262,119 + 15 + 10 = 262,144 |
| نمط حرفي غير صالح | في `matches()` و`split()` خطأ ترجمة، وفي `replace()` خطأ تقييم عند الاستدعاء يمتصه `\|\|` (مسبار S1-E: `'('` و`'(?=b)'` و`'(a)\1'`) | §4 «النص الحرفي يُترجم مرة»: يبقى، بلا خطأ ترجمة في `replace()` |
| عدد الوسائط | تحذير «Incorrect number of arguments supplied to function: …» عند الاسم، ثم الاستدعاء يفشل عند تقييمه: `debug(1, 2)` و`debug()` و`math.abs(1, 2)` و`'a'.size(1)` و`[1].size(1)` و`'a'.lower(1)` (مسبار S1-E). والمحرك يحذّر في هذه وحدها، والإنتاج في غيرها أيضًا | — |

**النحو:**

- **ترتيب العمليات** من الأشد ربطًا: اللاحقة (`a[i]` و`a()` و`a.f`)، ثم الأحادية (`!` و`-`)، ثم `* / %`، ثم `+ -`، ثم
  المقارنة (`< <= > >=`)، ثم `== !=`، ثم `is`، ثم `in`، ثم `&&`، ثم `||`، ثم `?:`. وهو غير ما تقوله الوثائق، والإنتاج
  يغلب.
- **الشرائح بحد محذوف** تُقرأ: `s[:2]` و`s[1:]`.
- **`\u` في حرفي البايتات** خطأ: «Invalid bytes literal b'ÿ'.».
- **`match` المتداخلة تحت `=**`:** في الإصدار 1 وبلا سطر إصدار، المتداخلة تحت كتلة فيها `=**` ميتة لا تطابق
  شيئًا، والملف يُترجم بتحذيرين لا خطأ («Ruleset uses old version …» و«Glob(**) resource rules should not have
  child resource rules.» عند `match` الأم)، ولو كان فيها `=**` ثانٍ. وفي الإصدار 2: تحت `=**` غير أخير ميتة،
  وتحت `=**` أخير تطابق على أي عمق، و`=**` ثانٍ في المتداخلة خطأ ترجمة. يكمّل §5.6.
- **ملف فيه خطأ نحو لا يُفحص:** الإنتاج يعطي أخطاء النحو وحدها، بلا خطأ `let` الإصدار 1 ولا العودية ولا تحذير
  الإصدار القديم.

**القيم والمكتبة:**

- **النصوص WTF-8:** وحدات UTF-16 كما في Java. نصف الزوج البديل يبقى كما هو لا U+FFFD (`'😀'[0]`)، ونصفان
  متجاوران يصيران الحرف (`'😀'[0] + '😀'[1] == '😀'`). و`toUtf8()` وhashing يكتبان النصف `?` كما يفعل
  `getBytes(UTF_8)`. والطول والفهرس والشريحة بوحدات UTF-16. يكمّل §2.3.
- **علاقة المساواة الرابعة (أعضاء المجموعة، `toSet()` و`in` عليها و`diff()`)** تقارن int بـ float بقيمتهما
  بالضبط: `2^53+1` ليس `2^53.0` فيها، مع أن `==` في أعلى التعبير يساوي بينهما. ويُحسم بها سؤال NaN: عضوان NaN
  عضو واحد.
- **`split()` و`replace()`** يمرّان على المطابقات كما يمر RE2J: مطابقة فارغة بعد مطابقة تُحسب، والخطوة بعد
  المطابقة الفارغة وحدة UTF-16. و`split()` يُسقط الأجزاء الفارغة في آخره.
- **فك ترميز المسار (`URLDecoder`):** `%` لا يتبعه رقمان ستعشريان خطأ تقييم بنص الإنتاج («Unable to decode … to
  url. Error: URLDecoder: …»)، والبايت غير الصالح في UTF-8 يصير U+FFFD وحده. يكمّل §5.7.
- **ترتيب الخريطة الحرفية:** `keys()` مرتبة، و`values()` بترتيب الكتابة، والمفتاح المكرر في موضعه الأول بقيمته الأخيرة.

**قرارات مؤقتة** (لم يقسها الإنتاج المنشور؛ مسجّلة في `docs/compat`، وتتغير في موضع واحد):

- **`debug()`** خطأ تقييم «Function not found error: Name: [debug].» كما في Test API، لا دالة هوية كما في المحاكي.
  وبعدد وسائط غير واحد تحذير الترجمة كذلك (مقيس على Test API، مسبار S1-E).
- **الأعداد في `request.auth.token`:** العدد الصحيح الذي يتسع له int64 يصير `int` ولو كُتب `2.0`، والباقي `float`.
  مصدرها Test API وحده.
- **`get()` لمستند غائب = `null`،** و`exists()` له `false`. المصدر مرجع `rules.firestore` والمحاكي، لا الإنتاج: و«الخطأ»
  الذي سجّله Pyric أثر من Test API (المستند فيها غير محاكى لا غائب)، ولا ملاحظة `deployed` تفصل.
- **ترتيب الخريطة في قيم الخدمة:** `values()` بترتيب المصدر (بيانات Storage الوصفية كما أرسلها الطلب). الدليل Test API
  وحده، وهي تأخذ `request` حرفيًّا (من آثارها المسجّلة)، فلا يثبت ما تفعله الخدمة. يبقى العقد بـ `rules.MapInOrder`
  (و`rules.Map` يرتّب)، وعلى S2 أن يعامل ترتيب البيانات الوصفية مؤقتًا حتى يقيسه تسجيل من المحاكي أو المنشور.

## تعديل 2026-10-24 (S7-B1): ما يحمله الـ ruleset وما تحمله الأخطاء

وجدت المراجعة الأمنية لـ v0.3 (`docs/security/review-v0.3.md`، SR3-02 إلى SR3-05) أن حدود §4 تحد كل شيء على حدة ولا
تحد مجموعه. فأضيف ما يلي، وكله من حدود Aruma:

| الحد | الافتراضي | لماذا | يُفحص |
|---|---|---|---|
| `regex_bytes`: البرامج المقدّرة لأنماط الملف الحرفية معًا | 4 MiB | مئة نمط حرفي، كل منها تحت حد التعليمات، في 10 KB من المصدر: 184 ms و206 MiB عند الترجمة، ووزن 170 MiB فوق ذاكرة الـ rulesets كلها، فيُترجم الـ ruleset من جديد مع كل طلب. **أكبر ما في الأوراكل 38,785 بايتًا** (`strings/size_ascii`)، أي واحد من مئة. والحد يتسع لنمطين بأقصى التعليمات | ترجمةً: خطأ عند حرفي النمط الذي يتجاوزه، ولا يُترجم ما بعده |
| وزن الـ ruleset الواحد (`Weight`) | ربع `compiled_rulesets_bytes`، أي 32 MiB | ما يُترجم يتسع له التخزين دائمًا، وتبقى فيه أربعة من أثقلها. **أثقل ما في الأوراكل 5,547,839 بايتًا** (`limits/size_functions_950`)، أي سدس الحصة. والإنتاج نفسه يرد 503 على `size_functions_2000` | ترجمةً: خطأ عن الملف كله |
| أسماء دورة الاستدعاء في رسالتها | 32 اسمًا ثم «and N more»، ومئة دورة في الملف، وكل دورة مرة عند الدالة التي رجع إليها البحث | 6,200 دالة كانت تعطي 6,200 خطأ و104 MiB من النص. والبحث صار خطيًا | ترجمةً |
| القيمة في رسالة خطأ التقييم | أول 128 بايتًا ثم `...` | كانت الرسالة تنسخ القيمة كلها (2 MiB) بلا حساب، والمعاملات تبقي خطأ كل عنصر: 250 إلى 522 MiB لطلب مجهول واحد | تقييمًا |

- **وزن النمط يحسب جداول فئاته** (مراجعة S7-B1، SR3-02-class-tables): `\pL` تعليمة واحدة و1,368 رونًا، يبقى جدولها
  في الصيغ الثلاث. وبحساب التعليمات وحدها كان 185 نمطًا من خمسين `\pL` في 44 KB يُترجم تحت `regex_bytes` والحصة،
  بوزن 24.9 MB، ويحمل 300 MiB. فصار الوزن يحسب لكل فئة في النمط المحلَّل 16 بايتًا لكل رون من سعة جدولها، قبل بناء
  أي برنامج. وذلك في ميزانية الملف، و`Weight`، وذاكرة الأنماط المبنية وقت التقييم، وعدّاد التقييم، وصيغ الأنصاف المنفردة.
- **صيغة `matches()` بلا برنامج one-pass** (المراجعة الثانية: onepass-alternation-merge وunicode-repeat-refused):
  الصيغة `(?:)^(?:p)$`، والمجموعة الفارغة لا تطابق إلا الفراغ، فالجواب كجواب `^(?:p)$`، والبحث مرسى كما كان. لكن البرنامج
  لا يبدأ بـ `^`، فلا يبني Go له برنامج one-pass، وهو الذي كان ينسخ جدول الفئة لكل تعليمة ويدمج فئات فروع البديل في كل
  منها: كان `^[\p{L} ]{1,100}$` يتسع ثلاث مرات في الملف، و`^[\p{L}\p{M}\p{N}\p{P}\p{Zs}]{1,500}$` لا مرة، وبديل من 28
  فئة يحمل 12 ضعف ما يُحسب. وبلا one-pass تشترك نسخ `x{n}` في جدول واحد (`InstRune` في `regexp/syntax`).
  **المقيس** (`TestClassTablesAreCounted`، Go 1.27): على `\pL` و`\PL` و`\p{Han}` و`[^\pL]` وفئات `(?i)` وتكرارها
  و`a{1000}` وبدائل الفئات (وحدها و`{5}` و`{8}`) والنمطين أعلاه، ملف بأقصى ما تقبله `regex_bytes` يحمل بعد الترجمة
  0.63 إلى 0.84 مما تحسبه الميزانيات، ومع صيغ الأنصاف المنفردة 1.00 من `Weight` على الأكثر، وذاكرة الأنماط المبنية
  0.75 من حسابها. وملف المراجعة يُرفض عند نمطه الثالث. ويتسع الملف لـ 56 من `^[\p{L} ]{1,100}$` و17 من نمط `{1,500}`،
  وعشرة أنماط للأسماء والعناوين بعدة كتابات تأخذ 18% من `regex_bytes` (`TestUnicodeValidationPatternsCompile`).
- **وزن الـ ruleset** يحسب النمط الحرفي خمس مرات: برنامجه، وصيغه للأنصاف البديلة المنفردة التي قد يحفظها لأربع كتل
  (`loneCacheBlocks`). والنمط المبني وقت التقييم لا يحفظ صيغه هذه، فلا تتجاوز ذاكرة أنماطه `regex_cache_bytes`.
  وحصة الشجرة تقدير (96 بايتًا للعقدة) لم يُقس بالطريقة نفسها.
- **`Validate` يرفض** `compiled_rulesets_bytes` أصغر من أربعة أمثال `source_bytes` و`regex_cache_bytes` وخمسة أمثال
  `regex_bytes` معًا، فلا تُضبط حدود لا يُترجم تحتها ملف تستعمل أنماطه حدها.
- **رسالة الخطأ تُحسب على العدّاد** حيث يوضع الخطأ في موضعه، كأي قيمة يبنيها التقييم. والمعاملات (`operands`) تبقي
  الخطأ الأول بترتيب المصدر وحده. والرسائل التي يقارنها الأوراكل قصيرة فلا تتغير: مجموعة التطوير 100% على Test API
  و99.9% على المحاكي، والمحجوزة 100% و100%، قبل التعديل وبعده. وبعد دمج S7-R: ‏87/87 من المنشور، و3643/3643 على
  Test API، و1985/1985 على المحاكي، بلا سماح زائد.
- **كل ترجمة تأخذ دورًا في `tenancy`، من نوعين لكل منهما `max(2, GOMAXPROCS/2)`:** أدوار الإدارة
  (`Service.CompileRules`): `:test` وإنشاء الـ ruleset في mgmt، والتحقق والنشر في الكونسول، وكان mgmt وحده يأخذها.
  وأدوار تحميل `CompiledRelease` التي تنتظرها الخدمات، فلا يقف التحميل خلف ترجمات الإدارة التي يرسلها مالك أي مشروع
  (مراجعة S7-B1، SR3-05-dataplane-starvation). ويترجم تحقق الكونسول الآن بالحدود المضبوطة لا بالافتراضية.
