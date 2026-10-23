# ADR-021: محرك قواعد Firestore وStorage: لغة واحدة مكتوبة يدويًا في `platform/rules`

- الحالة: معتمد
- التاريخ: 2026-10-23
- المرحلة: S0 من الإصدار 0.3 (القواعد وStorage)
- المراجع: [rules-layout.md](../research/rules-layout.md)، ADR-008، ADR-010، ADR-015، ADR-017، ADR-019، ADR-020، و:
  - وثائق Firebase (قُرئت في 2026-10-23): [rules-language](https://firebase.google.com/docs/rules/rules-language)، [rules-conditions](https://firebase.google.com/docs/firestore/security/rules-conditions)، [rules-structure](https://firebase.google.com/docs/firestore/security/rules-structure)، [rules-query](https://firebase.google.com/docs/firestore/security/rules-query)، [quotas § Security rules](https://firebase.google.com/docs/firestore/quotas#security_rules)، [Storage core-syntax](https://firebase.google.com/docs/storage/security/core-syntax)، [Storage rules-conditions](https://firebase.google.com/docs/storage/security/rules-conditions)، [مرجع Storage](https://firebase.google.com/docs/reference/security/storage)، [rules.String](https://firebase.google.com/docs/reference/rules/rules.String)، [manage-deploy](https://firebase.google.com/docs/rules/manage-deploy).
  - hidane (Apache-2.0/MIT، commit `abd4664` في 2026-10-19): `docs/adr/0005-rules-engine.md`، و`docs/parity-exceptions.md` (D008 وD020 وD059 وD060 وD113 وD114)، ووصف `crates/hidane-rules/src/eval/list.rs` و`eval/value.rs`. قُرئت أفكارًا، لا كودًا.
  - Pyric: [#150](https://github.com/davideast/pyric/issues/150) و[#151](https://github.com/davideast/pyric/issues/151) (محللان للغة واحدة انحرفا بصمت).
  - firebase-tools: [#6252](https://github.com/firebase/firebase-tools/issues/6252) (المحاكي يقيّم القاعدة مرتين، مفتوح منذ 2023)، و`src/rulesDeploy.ts` في 15.34.0 (صيغة أخطاء الترجمة).
  - cel-go: `cel.dev/cel-go` v0.32.0 (`github.com/cel-expr/cel-go`)، و`github.com/google/cel-go` صار مستودع توافق للقراءة فقط.
  - محرك قواعد RTDB عندنا: `internal/services/rtdb/rules`.

## السياق

v0.3 يحتاج لغة قواعد Firestore وStorage: Storage يستهلكها في S2، وFirestore في F5 (v0.4). هي لغة واحدة: `rules_version` و`service` و`match` و`allow` و`function` و`let`، وتعابير مبنية على CEL. ما يختلف بين الخدمتين هو المتغيرات (`request` و`resource`) ودوال القراءة (`get()` في Firestore، و`firestore.get()` في Storage). أما قواعد RTDB فلغة أخرى (JSON وتعابير شبيهة بـ JavaScript)، ومحركها بُني في R4.

ما يجعل القرار صعبًا:

- **اللغة ليست CEL، وليس لها نحو منشور.** قاس hidane محاكي Firestore الرسمي (v1.22.0) على 926 حالة:
  - **المعجم أضيق من CEL:** لا أُسس ولا hex في الأرقام، ولا نصوص raw. و130 من 578 حالة cel-spec لا تُترجم أصلًا.
  - **وأوسع منه:** `is`، ومسارات حرفية `/a/$(x)`، والشرائح `[i:j]`، وفهرسة النص، والحساب المختلط بين int وfloat.
  - **ودلالته غير دلالة CEL:** `matches` تطابق النص كله، و`[1] == [1.0]` خطأ مع أن `1 == 1.0` صحيح، و`1 in [1.0]` خطأ.
  - **والأنواع تُفحص وقت التقييم لا وقت الترجمة:** الدالة المجهولة تُترجم بتحذير، وتفشل حين تُقيَّم.
  - **ودلالة الخطأ هي دلالة CEL:** `error || true` يسمح، و`error && false` يرفض بـ false، و`error || false` يرفض. هذا غير مكتوب في الوثائق، لكن تسجيلات hidane (المحاكي) وPyric (الإنتاج) تؤكده.
- **الوثائق تتناقض في حد واحد على الأقل:** عمق الاستدعاء 20 في جدول الحدود وفي rules-language، و10 في نص rules-conditions لـ Firestore ولـ Storage. وعدد `let` عشرة في rules-language، و«أي عدد» في صفحة شروط Storage. يقيس S0-A هذه الحدود على الإنتاج.
- **قرار `list` في Firestore (list implication) أصعب جزء:** القواعد ليست مرشّحات، والاستعلام يُرفض إن أمكن أن يعيد مستندًا ترفضه القواعد. الوثائق لا تحدد التعابير التي تُحلَّل. ولا يُبنى قبل v0.4، لكن نموذج القيم الذي نختاره اليوم يجب ألا يمنعه.
- **قيود بنيتنا:** الخدمات لا تستورد بعضها (ADR-015). فاللغة المشتركة مكانها `internal/platform/`، و`firestore.get()` في قواعد Storage يمر بواجهة تربطها `internal/app`، كما في `idtoken`.
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
  - **العمليات مثبتة في مفسّره:** المساواة غير المتجانسة الحديثة في CEL تجعل `[1] == [1.0]` صحيحًا، والقواعد تقول خطأ. و`in` و`matches` والحساب المختلط مختلفة كذلك. وتغييرها يعني نسخة معدّلة (fork) من المفسّر. هذا ما أسقط النموذج C عند hidane.
  - **التقييم الجزئي في cel-go** (`PartialVars` والشجرة المتبقية) يبدو مفيدًا لـ `list`، لكنه لا يطابق نموذج المحاكي (البند 2.4). مثلًا: `n >= 1 && n <= 1` لا يستلزم `n == 1` عند المحاكي. فلا يوفر علينا شيئًا.
  - **الاعتمادات:** antlr4-go وprotobuf وgenproto وx/exp وx/text. وانتقلت الوحدة من `github.com/google/cel-go` إلى `cel.dev/cel-go` (المستودع القديم صار للتوافق فقط). خمس اعتمادات جديدة ومسار ينتقل، مقابل لا شيء.
  - **التشخيص:** نحتاج موضع كل خطأ (سطر:عمود) لـ `issues[]` التي تطبعها firebase-tools، وتتبعًا لكل تعبير لـ `:test` في S5. CEL لا يعطي موضعًا لأخطاء التقييم، ولا خطافات لتتبعها.
- **نحو واحد ومحرك واحد للخدمتين من اليوم الأول (درس Pyric):** لا محلل خاص بخدمة. الخدمة تختار اسم `service` وتقدّم متغيراتها وقراءاتها فقط (البند 3). واختبار يمرّر كل ملف من حالات الترجمة على الخدمتين: النحو لا يعرف الخدمة.
- **السلوك المرجعي:** الإنتاج أولًا، ثم المحاكي صندوقًا أسود. ولا نعيد أخطاء المحاكي المعروفة: التقييم المزدوج (#6252)، و`debug()` (غير موجودة في `:test` الإنتاج: «Function not found error: Name: [debug].»، hidane D113، فنتبع الإنتاج).

### 2. الحزمة وواجهتها: `internal/platform/rules`

اسم الحزمة `rules`. لا تتعارض مع `internal/services/rtdb/rules`، فلا حزمة تستورد الاثنتين. وتستورد Go القياسية فقط، ومن المنصة `platform/idtoken` لتبني `request.auth` في مكان واحد. وقاعدة archtest جديدة تفرض ذلك (rules-layout.md).

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
	Line, Col   int // 1-based; 0:0 for an issue about the whole file
	Offset, End int // from the start of the file, as Position.currentOffset/endOffset
	Severity    Severity // Error or Warning
	Description string
}
type Issues []Issue
func (is Issues) Err() error // nil when no issue is an error

func (rs *Ruleset) Version() int   // 1 or 2
func (rs *Ruleset) Service() Service
```

- **`Ruleset` لا يتغير بعد بنائه، وآمن للاستعمال المتزامن.**
- **وحدة الموضع:** العقد في الشجرة تحمل مواضع بالبايت. أما السطر والعمود والإزاحات في `Issue` فتُحسب عند إخراج الخطأ، بوحدة يحددها الأوراكل. وجد hidane أن المحاكي يعدّ code points. وما يعدّه الإنتاج يُقاس في S0-A، فإن خالف تتغير دالة التحويل وحدها.
- **ملف واحد:** `:test` في الإنتاج يرفض أكثر من ملف («Currently only one file source can be passed to firebase rules.»)، وfirebase-tools ترسل ملفًا واحدًا (hidane D114). فواجهة الترجمة تأخذ ملفًا واحدًا، وطبقة firebaserules في S5 تردّ رسالة الإنتاج على الأكثر.
- **ما يُرفض عند الترجمة (خطأ):**
  - أخطاء النحو.
  - `rules_version` غير `'1'` و`'2'`، و`service` لا يطابق الخدمة.
  - نص حرفي غير صالح يُمرَّر إلى `matches` أو `split` أو `replace` (hidane: الفحص الثابت الوحيد الذي يُفشل الترجمة عند المحاكي).
  - الاستدعاء الدوري بين الدوال (الحد 0).
  - تجاوز حدود البند 4 الثابتة.
- **ما يُحذَّر منه فقط:** الدالة المجهولة، والدالة غير المستعملة. وفي S1 لا ننقل كل تحذيرات الأنواع من المحاكي (2,145 سطرًا عند hidane). التحذير لا يغيّر قرارًا ولا يمنع نشرًا، ويُسجَّل هذا النقص في `docs/compat`.
- **الفحص على الإنتاج وحده:** تداخل طرق `allow` في الكتلة الواحدة، الذي تقول rules-language إنه خطأ، لا يُرفض حتى يثبته الأوراكل. رفض ملف قواعد يقبله Firebase يكسر النشر.

#### 2.2 التقييم: كل طلب

```go
func (rs *Ruleset) Evaluate(ctx context.Context, req *Request) (Decision, error)

type Request struct {
	Method    Method           // Get, List, Create, Update, Delete
	Path      []string         // the segments the match blocks see, from the service root
	Variables map[string]Value // "request" and "resource", built by the service
	Documents Documents        // get()/exists()/getAfter()/existsAfter()/firestore.*; nil: every lookup fails
	Budget    *Budget          // lookups shared by the operations of one request (a batch)
	Trace     TraceLevel       // TraceNone, TraceStatements; TraceExpressions reserved for :test (S5)
}

type Decision struct {
	Allow      bool
	Statements []StatementResult // each allow statement evaluated, in order: position, method, true/false/error and message
}
```

- **`error` من `Evaluate` عطل فقط:** سياق أُلغي، أو `Documents` أعاد خطأ غير `ErrNoDocuments` (قاعدة لا تجيب مثلًا). فتجيب الخدمة UNAVAILABLE ولا تسمح أبدًا. هذا تقسيم `idtoken` نفسه: الرفض قرار، والعطل خطأ.
- **القرار نفسه يأتي في `Decision`.** والرفض يصل العميل برسالة Firebase العامة، كـ «Missing or insufficient permissions.» في Firestore وما يسجّله S0 لـ Storage. أما `Statements` فللسجل، وللوحة، ولـ `:test`.

#### 2.3 نموذج القيم

قيمة القواعد نوع واحد `Value`. البنية الداخلية حرة لمن يكتبها في S1، والواجهة العامة ثابتة لأن الخدمات تبني بها `request` و`resource`:

- **الأنواع:** `Null` و`Bool` و`Int` (int64) و`Float` (float64) و`String` و`Bytes` و`List` و`Map` و`Set` و`MapDiff` و`Timestamp` و`Duration` و`LatLng` و`Path`.
- **ونوعان محجوزان لـ F5 من اليوم:** `Constraint` و`Unknown` (2.4).
- **دوال البناء:** `Null()` و`Bool(b)` و`Int(i)` و`Float(f)` و`String(s)` و`Bytes(b)` و`List(vs)` و`Map(m)` و`Timestamp(t)` و`Duration(d)` و`LatLng(lat, lng)` و`PathOf(segs)`، و`Auth(*idtoken.Claims)` التي تبني `request.auth` (`uid` و`token`) أو `null`.
  - `Auth` في المنصة لا في كل خدمة، فشكل `request.auth` واحد في Storage وFirestore. وأعداد الرمز الصحيحة (`exp` و`iat` و`auth_time`) تصير int والباقي float، ويتحقق الأوراكل من ذلك.
- **النصوص:** UTF-8 في الذاكرة. لكن الطول والفهرسة والشرائح بوحدات UTF-16، كما يعدّها Java (hidane).
- **الأعداد:** طفح int64 خطأ، والقسمة على صفر خطأ. والمساواة بين الأعداد تحوّل `1 == 1.0`، أما داخل القوائم والخرائط فالمساواة صارمة (`[1] == [1.0]` خطأ).

**الخطأ قيمة.** التقييم يعيد قيمة أو خطأ تقييم. والخطأ لا يوقف الشرط، بل ينتقل في الشجرة حتى يمتصه `&&` أو `||`:

| التعبير | النتيجة |
|---|---|
| `true \|\| x` و`false && x` | `true` و`false`، و`x` لا يُقيَّم (قصر الدائرة، كما في الوثائق) |
| `false \|\| x` و`true && x` | `x` (ويجب أن يكون منطقيًا، وإلا فخطأ) |
| `error \|\| x` | يُقيَّم `x`: إن كان `true` فالنتيجة `true`، وإلا فالخطأ الأول |
| `error && x` | يُقيَّم `x`: إن كان `false` فالنتيجة `false`، وإلا فالخطأ الأول |
| `!error`، و`error ? a : b`، والعمليات والدوال على خطأ | خطأ |
| شرط `allow` | يمنح فقط إن كانت قيمته `true` تمامًا. الخطأ والقيمة غير المنطقية لا يمنحان |

- **الأوراكل يحسم الحالات الطرفية:** `false && 32` تعطي `false` عند المحاكي (hidane). أما `true && 32` و`32 || true` فتعطي في جدولنا خطأ، ويتحقق الأوراكل منها.
- **`let` يُقيَّم بترتيبه عند دخول الدالة.** هذا ما تعنيه الوثائق بأن `let isAdmin = exists(...)` «enforces a lookup». والخطأ فيه يُحفظ قيمةً، ولا يُفشل الدالة إلا إن وصل إلى `return`.
- **وسائط الدالة تُقيَّم قبل الاستدعاء.**

#### 2.4 القيمة الجزئية حين تكون `resource.data` مجهولة: نحجز التصميم في S1 وننفّذه في F5

**القرار:** في S1 تُحجز ثلاثة أشياء، ولا يُبنى تقييم جزئي:

1. **نوعا القيمة** `Constraint` (ما يعرفه استعلام `list` عن حقل: أزواج مقارِن وقيمة، مثل `GT 0` و`NEQ 3` و`LIST_CONTAINS 'a'`) و`Unknown` (ما لا يُقيَّم أصلًا، كـ `request.path` في collection group). كلاهما في تعداد الأنواع.
2. **مقطع مسار مجهول** في `Request.Path`: معرّف المستند في `list`. فالمتغير الذي يأخذه يبقى بلا قيمة، وقراءته خطأ.
3. **قاعدة واحدة في المفسّر:** كل عملية أو دالة تتلقى `Constraint` أو `Unknown` تعيد في S1 خطأً بلا رسالة، أي رفضًا. فكل `switch` على النوع له فرع افتراضي يرفض، ولا يمكن أن يسمح المحرك بشيء لا يفهمه.

وفي F5 تُضاف المقارنات على `Constraint` في ملف خاص بها (`constraint.go`)، كما قاسها hidane: المقارنة صحيحة إن استلزمها أحد مرشحات الحقل وحده، وإلا فخطأ بلا رسالة، لا `false`. ويضيف ربط Firestore تفكيك الاستعلام إلى «طرق»: كل فرع `or`، وكل قيمة من `in` و`array-contains-any`. ثم تقيَّم كل طريقة بـ `Evaluate` عادي على `allow list`. فلا تتغير واجهة المحرك، بل تضاف دوال بناء.

**لماذا لا تقييم جزئي كامل الآن:**

- **نموذج المحاكي ليس شجرة متبقية.** قاسه hidane (#66) فوجده قيمًا مقيّدة لكل حقل مع قاعدة استلزام ضيقة: `n >= 1 && n <= 1` لا يستلزم `n == 1`، ومرشح لا يطابق شيئًا يستلزم `== true` و`== false` معًا. ومحلل رمزي عام (DNF واختبار الاحتواء) يقرر هذه الحالات بغير ما يقرره Firebase، فيكون أكثر تساهلًا أحيانًا. والتساهل ممنوع.
- **ما يحتاجه `list` لم يوجد بعد:** نموذج الاستعلام (F3) وترميز القيم (F1). وتصميم الاستلزام قبلهما يجمّد واجهة خاطئة.
- **Storage، المستهلك الوحيد في v0.3، لا يحتاجه.** `list` في Storage لا يحمل مرشحات على البيانات.
- **ما يُحجز اليوم يكفي ليبقى F5 إضافة لا إعادة كتابة.** الأنواع موجودة، والفرع الافتراضي يرفض، و`Evaluate` نفسه يُستدعى لكل طريقة. وإن أظهر الأوراكل في F5 شيئًا لم يقسه hidane، تُضاف أنواع قيمة أخرى بالطريقة نفسها.

### 3. ربط الخدمات (bindings): المنصة لا تعرف الخدمات

- **الخدمة تبني متغيراتها:** `request` و`resource` قيم `Map` عادية، تبنيها الخدمة بدوال البناء:
  - Storage في S2: `resource.name` و`bucket` و`size` و`contentType` و`metadata` و`timeCreated` وغيرها، و`request.resource` و`request.time` و`request.path`.
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
  // Documents reads Firestore documents for rules. path is the whole document path,
  // /databases/(default)/documents/...; after selects the state after the request's
  // pending writes (getAfter, existsAfter).
  type Documents interface {
  	Get(ctx context.Context, path []string, after bool) (doc Value, found bool, err error)
  }

  // ErrNoDocuments is what a Documents answers when Firestore cannot be read: an
  // evaluation error in the condition, never an outage.
  var ErrNoDocuments = errors.New("rules: Firestore documents are not available")

  // NoDocuments is the Documents Storage gets until v0.4: every lookup fails.
  type NoDocuments struct{}
  ```

  - **Firestore (F5):** تنفّذها على قراءتها، و`after` على طبقة الكتابات المعلّقة.
  - **Storage:** تتلقى `Documents` من `internal/app`. في v0.3 هي `rules.NoDocuments`، فكل `firestore.get()` خطأ تقييم، أي رفض ما لم يمتصه `||` كأي خطأ. وفي v0.4 تربط `app` خدمة Firestore بها، دون أن تستورد إحدى الخدمتين الأخرى (ADR-015).
  - **مسار خارج قاعدة البيانات:** مسار لا يبدأ بـ `/databases/(default)/documents/` يعيد خطأ تقييم. Storage لا يقرأ إلا القاعدة الافتراضية (الوثائق).
- **العدّ والذاكرة المؤقتة (`Budget`):**
  - مفتاح الذاكرة المؤقتة `(after, path)`. و`get` و`exists` على المسار نفسه وفي الحالة نفسها قراءة واحدة تُحسب مرة: «cached calls do not count».
  - **الحدود:**
    - Firestore: 10 لكل عملية، و20 للطلب كله في الدفعات والمعاملات والقراءات المتعددة.
    - Storage: مستندان من Firestore في التقييم الواحد («No more than two Firestore documents may be accessed in a single Rules evaluation»).
  - الخدمة تنشئ `Budget` واحدًا للطلب كله وتمرّره لكل عملية فيه، فيُحسب الحدّان معًا.
  - وهل المستند الذي تكتبه الدفعة نفسها مجاني لـ `get()`؟ عند المحاكي الجواب غير ثابت (hidane D020)، ويقرره F5 بالإنتاج.

### 4. الحدود: إعداد بقيم افتراضية

القيم تحت `rules.limits` في الإعداد، كما `rtdb.limits`. و`internal/app` يحوّلها إلى `rules.Limits`:

| الحد | الافتراضي | المصدر | يُفحص |
|---|---|---|---|
| عمق استدعاء الدوال | **من الأوراكل**، و20 حتى يقيسه S0-A | جدول الحدود وrules-language وhidane عن المحاكي: 20. نص rules-conditions لـ Firestore وStorage: 10 | تقييمًا |
| `let` في الدالة | 10 | جدول الحدود. وصفحة Storage تقول «أي عدد»، فيقيسه S0-A | ترجمةً |
| وسائط الدالة | 7 | جدول الحدود | ترجمةً |
| التعابير المقيَّمة في الطلب | 1000 | جدول الحدود. وحدة العدّ (كل عقدة تُقيَّم) تُطابَق بحالات `rules_expressions` من hidane والأوراكل | تقييمًا |
| قراءات المستندات | 10 للعملية، و20 للطلب، و2 لـ `firestore.*` من Storage | جدول الحدود ومرجع Storage | تقييمًا |
| عمق `match` المتداخلة | 10 | جدول الحدود | ترجمةً |
| مقاطع المسار في `match` المتداخلة | 100 | جدول الحدود | ترجمةً |
| متغيرات المسار في `match` المتداخلة | 20 | جدول الحدود | ترجمةً |
| حجم نص القواعد | 256 KiB (262,144 بايت). والحد الدقيق (256,000 أم 262,144) يقيسه S0-A | جدول الحدود | ترجمةً |
| كلفة `matches` و`split` و`replace` | طول النص بالبايت × عدد تعليمات البرنامج ≤ 2^26 | فكرة محرك RTDB نفسها (`regex.go`، `maxMatchCost`). حد خاص بنا | تقييمًا |
| عمق الشجرة (سلاسل `.a.b` و`[0][0]` والتعابير المتداخلة) | 2,000 | المحاكي يقبل نحو 6,500 ثم يعيد 500. وhidane يحد بـ 2,000 برسالة المترجم «Expression is too complex to evaluate safely.» (D060) | ترجمةً |
| عمل دوال المجموعات في الطلب (عناصر تُلمس) | 2^24 | حد خاص بنا، ليبقى زمن الطلب محدودًا على قوائم كبيرة في مستند حجمه 1 MiB | تقييمًا |

- **تجاوز حد التقييم يُنهي التقييم كله برفض.** لا يصير خطأً يمتصه `||`. الوثائق تقول: «Exceeding either limit results in a permission denied error». أما المحاكي فيجعله خطأً عاديًا (hidane). وبما أن الإنتاج غير مقيس بعد، نختار الأشد. المفسّر يميّز نوعين من الخطأ (`evalError` يُمتص، و`abort` يُنهي)، فإن أظهر الأوراكل غير ذلك تغيّر موضع واحد.
- **الحدّان الخاصان بنا (كلفة regex وعمل المجموعات)** يُوثّقان في `docs/compat` كقيود (limitation).
- **التعابير النمطية:**
  - **قواعد RE2 كما تقول الوثائق.** `regexp` في Go هو RE2، فلا ترجمة كما في RTDB. والمطابقة على النص كله (`^(?:…)$`).
  - **النص الحرفي يُترجم مرة عند الترجمة.** والنمط الذي يُبنى وقت التقييم يُخزَّن في ذاكرة محدودة لكل `Ruleset` (256 نمطًا).
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
   - **في الإصدار 1:** `{x=**}` يطابق مقطعًا واحدًا أو أكثر، ويجب أن يكون آخر مقطع في `match`.
   - **في الإصدار 2:** يطابق صفرًا أو أكثر، ويجوز في أي موضع، مع حد واحد لكل عبارة `match`. وقيمته `Path`، أما `{x}` فقيمته نص.
   - **في Storage:** `list` لا يُتاح إلا في الإصدار 2 (core-syntax). فعلى ملف من الإصدار 1 يرفضه ربط Storage، وهو يقرأ `rs.Version()`.
   - **ما يتبع الإصدار بلا تأكيد:** `let`، فالوثائق تقول «In rules version v2». يحدده الأوراكل.
6. **ترجمة مسارات `match`:**
   - عند الترجمة تُسطَّح كل كتلة فيها `allow` إلى نمط مقاطع كامل من جذر الخدمة. كل مقطع حرفي، أو `{x}`، أو `{x=**}`، ومعه موضع كل متغير.
   - المطابقة بالرجوع (backtracking)، وهو محدود: 100 مقطع، وعبارة `=**` واحدة لكل `match`.
   - إن طابقت كتلة بأكثر من ربط للمتغيرات (`=**` في كتلتين متداخلتين بالإصدار 2)، يُؤخذ أول ربط يطابق بترتيب «الأقصر أولًا». هذه حالة نادرة يتحقق منها الأوراكل.
   - المسارات الحرفية في الشروط (`/databases/$(database)/documents/x`) تُبنى قيم `Path`، و`$(…)` تُقيَّم نصًا.

### 6. الاختبار والقبول

- **الأوراكل** في `test/oracle/rules/`، يجمعه S0-A ويملك صيغته: حالات ترجمة وحالات تقييم. لكل ملف مصدره (الإنتاج عبر Rules Test API، أو المحاكي بإصداره، أو hidane، أو Pyric)، وتاريخه، وبصمة مدخلاته.
  - يقرأها `rulestest` في S1. وإن تغيرت الصيغة يتغير القارئ وحده.
  - حقول حالة التقييم: الخدمة، والمصدر، والطريقة، والمسار، و`auth`، و`resource`، و`request.resource`، والوقت، والمستندات التي تعيدها القراءات، والنتيجة `ALLOW`/`DENY`.
  - وحقول حالة الترجمة: نجاح أو فشل، و`issues[]`.
- **المجموعة المحجوزة:**
  - `test/oracle/rules/heldout/` لا يقرؤها بنّاؤو S1، ويُذكر ذلك في تكليف كل مهمة فرعية.
  - `TestHeldOut` يتخطاها إلا بـ `ARUMA_RULES_HELDOUT=1`. ويشغّلها المدمج مرة واحدة في نهاية S1، ويسجّل الرقم في `docs/compat/rules-v0.3.md`.
  - بعدها تنضم إلى الحالات العادية، ويُجمع لـ F5 جزء محجوز جديد لحالات `list`.
- **القبول: 98% أو أكثر** على مجموعة التطوير، وعلى المحجوزة كل على حدة.
  - حالة التقييم تطابق إن طابق القرار.
  - حالة الترجمة تطابق إن طابق النجاح أو الفشل، وموضع أول خطأ (سطر:عمود). ونص الرسالة يُقارن ويُبلَّغ عنه، لكنه لا يدخل النسبة.
  - **عند تعارض الإنتاج والمحاكي يُتبع الإنتاج،** وتُصنَّف حالة المحاكي فرقًا معروفًا.
  - **وكل حالة لا تطابق تُصنَّف** في `docs/compat/rules-v0.3.md`: خطأ عندنا يُصلح، أو فرق مقصود بسببه، أو خطأ في المحاكي.
  - **ولا يُقبل أي فرق نكون فيه أكثر تساهلًا من الإنتاج،** مهما كانت النسبة.
- **Fuzzing** على نمط RTDB، بزمن محدود لكل مدخل وبذور من حالات الأوراكل:
  - `FuzzParse`: لا ذعر (panic)، والمواضع داخل النص، ومهلة ثابتة.
  - `FuzzEval`: طلبات عشوائية على قواعد البذور. لا ذعر، وقرار حتمي (تقييمان متطابقان)، ومهلة ثابتة.
  - `FuzzRegex`: الكلفة لا تتجاوز الحد.
- **أداة مقارنة مع المحاكي:**
  - `tools/rules-diff` تولّد شروطًا من النحو وطلبات عشوائية ببذرة ثابتة. ترسلها إلى المحاكي الرسمي صندوقًا أسود وإلى المحرك، وتطبع أصغر حالة مختلفة.
  - تُشغَّل يدويًا وفي جدول CI الأسبوعي، لا مع `go test`.
  - على نمط أداة Kor، لكن مكتوبة لنا.
- **الاختبارات الجدولية في كل ملف، كعادة المستودع:**
  - جدول دلالة الخطأ أعلاه حالةً حالة.
  - وحدود كل نوع.
  - وUTF-16 بنص عربي وemoji.

### 7. أخطاء النشر وتخزين القواعد

- **صيغة الأخطاء:** `firebase deploy` تستدعي `POST /v1/projects/{p}:test` بالملف أولًا. ثم تطبع كل عنصر من `issues[]`:
  ```
  [E] 5:10 - <description>
  ```
  وتوقف النشر بـ «Compilation error(s) in <file>:» إن كان فيها خطأ، وتطبع التحذيرات دون أن تتوقف (`rulesDeploy.ts`).
  - شكل العنصر: `sourcePosition{fileName, line, column, currentOffset, endOffset}` و`description` و`severity` (`ERROR` أو `WARNING`).
  - `Issue` في البند 2.1 يطابقه حقلًا حقلًا. ونصوص `description` تُنسخ من الأوراكل.
  - **اللوحة (S5)** تعرض القائمة نفسها على أسطر المحرر، وترفض الحفظ إن كان فيها خطأ.
- **التخزين (يبنيه S2 أقلّه، ويكمله S5 بواجهة firebaserules):**
  - **المكان:** جدولان في `tenancy`، لا في خدمة. واجهة الإدارة (`internal/admin`) والخدمتان تستوردان `tenancy`، ولا تستوردان بعضها (ADR-015).
  - `rulesets(project_id, id, files jsonb, create_time)`:
    - غير قابل للتعديل.
    - الاسم `projects/{p}/rulesets/{uuid}`.
    - حتى 2,500 لكل مشروع، كحد Firebase (manage-deploy)، قابلة للضبط.
  - `releases(project_id, name, ruleset_id, create_time, update_time)`:
    - الأسماء: `cloud.firestore` و`firebase.storage/<bucket>`.
    - إصدار يشير إلى ruleset، والنشر تحديث للإشارة.
  - كلاهما بـ `project_id` وRLS (ADR-017).
  - المحمي لا يُحذف: ruleset تشير إليه إصدارة.
- **التخزين المؤقت:**
  - الـ ruleset لا يتغير، فيُترجم مرة لكل `id` ويُحفظ في ذاكرة محدودة (LRU).
  - إشارة الإصدارة تُحفظ في الذاكرة. وتُبطل داخل العملية عند النشر، بخادم واحد كما في ADR-019. وبتعدد الخوادم لاحقًا تمر بآلية `idtoken.Revocations` نفسها.
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
    - حد كلفة `matches` واختباره الزمني `TestMatchesIsBounded`.
    - أهداف fuzz بمهلة وبذور من الحالات المسجّلة.
    - ملفات golden بمصدرها وإصدارها.
    - مصدر بيانات في الذاكرة للاختبار (`MemorySource`، وهنا `MemoryDocuments`).
- **تقييم جزئي كامل في S1:** يخالف نموذج المحاكي المقيس، ويُبنى قبل نموذج الاستعلام الذي يحتاجه، ولا يستهلكه Storage (البند 2.4).
- **الخطأ يرفض الشرط كله فورًا (كما في محرك RTDB):** يخالف ما سجّله hidane من المحاكي وPyric من الإنتاج: `error || true` يسمح. وهذا الرفض يرفض قواعد يقبلها Firebase.
- **تقييم على مرحلتين كالمحاكي:** خطأ مسجّل (#6252)، يضاعف القراءات المحسوبة، ويغيّر رسالة الرفض.
- **حفظ الـ rulesets في كل خدمة:** واجهة firebaserules واحدة للخدمتين، ولا تستطيع الإدارة استيراد خدمة. و`tenancy` يملك ما يخص المشروع أصلًا.
- **تجاوز حد القراءات أو العمق خطأً يُمتص:** أكثر تساهلًا من نص الوثائق إن كان الإنتاج يرفض الطلب كله. نختار الأشد حتى يقاس.

## العواقب

- **محرك واحد** في `internal/platform/rules`، يستهلكه Storage في S2 وFirestore في F5، دون أن تعرفه أي خدمة من الداخل، ودون اعتماد خارجي جديد.
- **S1 مستقل عن Storage:** يُختبر بالأوراكل وحده. وتقسيمه إلى مهام متوازية بواجهات ثابتة في [rules-layout.md](../research/rules-layout.md).
- **قيود معروفة تُوثّق في `docs/compat/rules-v0.3.md`:**
  - التحذيرات أقل من المحاكي.
  - حدّا كلفة regex وعمل المجموعات.
  - النشر الفوري.
  - `debug()` غير موجودة.
  - وكل حالة أوراكل لا تطابق، بتصنيفها.
- **`firestore.get()` في قواعد Storage يرفض حتى v0.4،** إلا أن يمتص `||` الخطأ. وv0.4 يغيّر سطرًا في `internal/app` لا في Storage.
- **F5 إضافة:** دوال بناء `Constraint`، و`constraint.go`، وتفكيك الاستعلام في ربط Firestore. ولا تتغير `Evaluate` ولا نموذج القيم.
- **قيم غير محسومة بعد،** كلها في `Limits` أو في موضع واحد من الكود: عمق الاستدعاء، و`let` في Storage، ووحدة العمود، وامتصاص أخطاء الحدود، والحد الدقيق للحجم. فإن قاسها S0-A أو الأوراكل لاحقًا تغيّر الافتراضي، لا التصميم.
- **ما لم يُقرر هنا:** جداول `rulesets` و`releases` بتفاصيلها (S2 وS5)، وشكل `request` و`resource` في Storage (ADR-022 وS2)، وواجهة firebaserules (S5).
