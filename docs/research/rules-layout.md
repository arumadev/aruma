# هيكل كود لغة قواعد Firestore وStorage

- **الحالة:** معتمد مع [ADR-021](../adr/ADR-021-rules-language.md)، 2026-10-23. يُحدَّث حيث يخالفه الكود، وكل ما لم يكن فيه يُعلَّم «لم يكن في المسودة»، كما في [rtdb-layout.md](rtdb-layout.md).
- **الأساس:**
  - ADR-021 (المحرك وواجهته).
  - ADR-015 (حدود الحزم وarchtest).
  - طريقة `internal/platform/idtoken`: عقد في المنصة تربطه `internal/app`.
  - محرك قواعد RTDB (`internal/services/rtdb/rules`): نمطًا لا كودًا.
- **ما يتوقف على غيره:**
  - صيغة ملفات الأوراكل في `test/oracle/rules/`، ويملكها S0-A. يقرؤها `rulestest` وحده، فإن تغيرت تغيّر القارئ.
  - شكل `request` و`resource` في Storage: ADR-022 وS2.

## الصورة العامة

```
                        ┌──────────────── internal/platform/rules ────────────────┐
 Storage (S2) ──┐       │ Compile(svc, file, limits) ──► lexer ► parser ► AST      │
 Firestore (F5) ┼─────► │                                  ► check ► match paths   │
 firebaserules ─┘       │                                  ► *Ruleset (ثابت)       │
  (S5، :test)           │ (*Ruleset).Evaluate(ctx, *Request) ──► decide            │
                        │        ├─ eval: الخطأ قيمة، && || ?: let، العمق والعدّ    │
                        │        ├─ library: الأنواع ودوالها، regex بحد كلفة       │
                        │        └─ lookup: Documents + Budget (عدّ وذاكرة مؤقتة)   │
                        └──────────────────────────────────────────────────────────┘
 الخدمة تقدّم: Method، والمقاطع، و"request" و"resource" قيمًا، وDocuments، وBudget.
 الحزمة لا تستورد إلا Go القياسية وplatform/idtoken (لـ rules.Auth). ولا خدمة تُستورد فيها.
 internal/app يعطي Storage الكائن rules.NoDocuments{} في v0.3، وقراءات Firestore في v0.4.
```

**اتجاه الاعتماد** (يفرضه archtest):

- **`internal/platform/rules`:** يستورد `internal/platform/idtoken` فقط من الداخل.
- **`internal/platform/rules/rulestest`:** يستورد `rules`، ولا يُستورد إلا من ملفات `_test.go` ومن `tools/`.
- **`internal/services/storage/...` و`internal/services/firestore/...`:** تستورد `platform/rules`، وهذا مسموح أصلًا بقاعدة `services/*`.
- **`internal/admin/mgmt`:** واجهة firebaserules في S5. تستورد `platform/rules` لـ `:test` والترجمة، و`tenancy` للتخزين.

## الشجرة

العمود الأخير هو المهمة الفرعية في S1 (القسم الأخير). `0` هو commit العقد الذي يسبق الجميع.

### `internal/platform/rules/`: اللغة

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | وصف الحزمة: لغة واحدة لـ `cloud.firestore` و`firebase.storage`، والمراجع (الوثائق، وhidane ADR 0005، والأوراكل)، وما لا تدعمه (`debug()`، وتحذيرات الأنواع الكاملة) | 0 |
| `api.go` | الواجهة العامة: `Service` و`File` و`Compile` و`Ruleset` (`Version` و`Service` و`Evaluate`) و`Method` و`Request` و`Decision` و`StatementResult` و`TraceLevel`. في commit العقد تعيد `Compile` و`Evaluate` «not implemented» | 0 ثم C |
| `limits.go` | `Limits` بحقول البند 4 في ADR-021، و`DefaultLimits()`، والتحقق من القيم | 0 |
| `issues.go` | `Issue` و`Severity` و`Issues` و`Err()`، وتحويل الموضع بالبايت إلى سطر:عمود وإزاحات بالوحدة التي يحددها الأوراكل (code points حتى يُقاس الإنتاج). ونصوص الرسائل الثابتة | 0 ثم A |
| `ast.go` | عقد الشجرة، ولكل عقدة `span{start, end int}` بالبايت: `file` (الإصدار، والخدمة، والكتل)، و`match` (مقاطع النمط)، و`allow` (الطرق، والشرط أو لا شرط)، و`function` (الوسائط، و`let`، و`return`)، والتعابير (حرفي، ومعرّف، وعضو، وفهرس، وشريحة، واستدعاء، وأحادي، وثنائي، و`is`، و`in`، وثلاثي، وقائمة، وخريطة، ومسار حرفي بأجزاء `$(…)`) | 0 |
| `lexer.go` | الرموز كما قاسها hidane: لا أُسس ولا hex، والنصوص بعلامتي التنصيص والهروب المسموح فقط، و`b'…'`، و`--` و`++` رمزان، والتعليقات `//` و`/* */`. وما ليس من اللغة يُسمّى بالاسم كما في lexer محرك RTDB | A |
| `parser.go` | نزول متكرر (recursive descent)، وأسبقية التعابير بجدول الوثائق: `a[i] a() a.f`، ثم `! -`، ثم `* / %`، ثم `+ -`، ثم `< <= > >=`، ثم `in`، ثم `is`، ثم `== !=`، ثم `&&`، ثم `\|\|`، ثم `?:`. ومعه `rules_version` والرأس `service` والكتل. يجمع كل الأخطاء، ويتعافى عند `;` و`}`. وعمق الشجرة محدود (2,000) | A |
| `check.go` | الفحص: النطاقات المعجمية للدوال والمتغيرات، وربط كل معرّف واستدعاء بموضعه، وكشف الاستدعاء الدوري، والحدود الثابتة (`let` والوسائط وعمق `match` ومقاطعها ومتغيراتها والحجم)، ونص `matches`/`split`/`replace` الحرفي يُترجم مرة أو يُرفض، والتحذيرات (الدالة المجهولة وغير المستعملة)، والدوال المفعّلة لكل خدمة | C |
| `match.go` | ترجمة كتل `match` إلى أنماط مقاطع مسطّحة (حرفي، و`{x}`، و`{x=**}`) بدلالة الإصدار 1 أو 2، والمطابقة بالرجوع المحدود (الأقصر أولًا)، وربط المتغيرات (`{x}` نص، و`{x=**}` قيمة `Path`)، والمقطع المجهول المحجوز لـ F5 | C |
| `value.go` | `Value` و`Kind`، ومنها `KindConstraint` و`KindUnknown` المحجوزان. ودوال البناء العامة (`Null` و`Bool` و`Int` و`Float` و`String` و`Bytes` و`List` و`Map` و`Timestamp` و`Duration` و`LatLng` و`PathOf`) والقراءة. واسم النوع في الرسائل ولـ `is` | 0 ثم B |
| `auth.go` | `Auth(*idtoken.Claims) Value`: `request.auth` بـ `uid` و`token`، والأعداد الصحيحة في الرمز int | B |
| `equal.go` | المساواة: العددية بين int وfloat في `==`، والصارمة داخل القوائم والخرائط ولـ `in`، وتلك التي تستعملها المجموعات و`diff` (hidane `value.rs`) | B |
| `ops.go` | العمليات: `+ - * / %` مع الحساب المختلط، وطفح int64، والقسمة على صفر، والمقارنة لكل نوع، و`in`، و`is`، والفهرسة والشرائح بوحدات UTF-16. كل عملية على نوع لا تعرفه، ومنه المحجوزان، تعيد خطأ | B |
| `strings.go` | دوال النص: `size` و`lower` و`upper` و`trim` و`split` و`replace` و`matches` و`toUtf8` و`hasPrefix`…، والطول والفهرسة بـ UTF-16 | B |
| `collections.go` | دوال `List` و`Map` و`Set` و`MapDiff`: `hasAll` و`hasAny` و`hasOnly` و`toSet` و`join` و`concat` و`removeAll` و`keys` و`values` و`get` و`diff` و`affectedKeys`…، بتطبيق خطي أو `n log n` يُحسب على حد العمل | B |
| `time.go` | `timestamp` و`duration`: `timestamp.date` و`timestamp.value` و`duration.value` و`duration.time` والدوال على القيم، وحدود السنوات 1 إلى 9999 و10,000 سنة | B |
| `path.go` | قيم `Path`: البناء من الحرفي و`$(…)` ومن `path()`، و`bind`، والفهرسة، والمقارنة | B |
| `regex.go` | RE2 من `regexp` على النص كله، وحد الكلفة (طول × تعليمات ≤ 2^26، فكرة `maxMatchCost` في RTDB)، وذاكرة أنماط محدودة لكل `Ruleset` (256)، وصيغة الاستبدال في `replace` كما يحددها الأوراكل | B |
| `library.go` | جدولا التوزيع: الدوال على كل نوع (`methods[Kind][name]`)، والدوال العامة ومساحات الأسماء (`int()` و`float()` و`string()` و`math.*` و`hashing.*` و`latlng.value`). يسجّل فيهما B، ويستدعيهما المفسّر عبر `callMethod` و`callFunction` اللتين يحدد العقد توقيعهما | 0 ثم B |
| `meter.go` | `meter`: ما تحتاجه دوال المكتبة من التقييم الجاري. حد العمل (`charge(n)`)، وذاكرة الأنماط، والحدود. معرَّف في العقد كي يكتب B وC بالتوازي | 0 |
| `eval.go` | المفسّر: الخطأ قيمة (`evalError`) وجدول `&&` و`\|\|` و`?:` في ADR-021، و`let` بترتيبه، والوسائط قبل الاستدعاء، وعمق الاستدعاء، وعدّ التعابير، والتمييز بين `evalError` الذي يُمتص و`abort` الذي يُنهي التقييم برفض | C |
| `lookup.go` | `Documents` و`ErrNoDocuments` و`NoDocuments`، و`Budget` (حد الطلب، وحد العملية، والذاكرة المؤقتة بمفتاح `(after, path)`)، والدوال `get` و`exists` و`getAfter` و`existsAfter` و`firestore.get` و`firestore.exists` وتفعيلها لكل خدمة، ورفض المسار خارج `/databases/(default)/documents/` | C |
| `decide.go` | `(*Ruleset).Evaluate`: الكتل المطابقة بترتيب الملف، والعبارات التي تشمل الطريقة (`read` = `get`+`list`، و`write` = `create`+`update`+`delete`)، وكل شرط مرة واحدة، وأول `true` يُوقف، وما يحتاجه `Decision.Statements`، و`No matching allow statements` | C |
| `trace.go` | `StatementResult` (الموضع، والطريقة، والنتيجة، والرسالة)، و`TraceExpressions` محجوز لتقرير `:test` في S5 | C |
| `constraint.go` | **F5، لا S1.** المقارنات على `Constraint` كما قاسها hidane (#66). في S1 لا يوجد هذا الملف، والفرع الافتراضي في `ops.go` يرفض | F5 |

#### الاختبارات في الحزمة نفسها

| الملف | ما فيه | المهمة |
|---|---|---|
| `lexer_test.go` و`parser_test.go` | جداول: كل شكل نحوي، ورفض المعجم الذي قاسه hidane، ومواضع الأخطاء، ونص عربي وemoji في النصوص والتعليقات وأثرها في العمود | A |
| `issues_test.go` | تحويل الموضع: أسطر CRLF، وحروف خارج BMP، والإزاحات | A |
| `value_test.go` و`ops_test.go` و`library_test.go` و`regex_test.go` | جداول لكل نوع ودالة: المساواة الصارمة والعددية، والطفح، وUTF-16، و`TestMatchesIsBounded` بالحد الزمني نفسه في RTDB | B |
| `check_test.go` و`match_test.go` | النطاقات، والدوري، والحدود الثابتة، ودلالة `=**` في الإصدارين، والربط عند تعدد المطابقة | C |
| `eval_test.go` | جدول دلالة الخطأ في ADR-021 حالةً حالة، و`let`، والعمق، وعدّ التعابير، و`abort` مقابل `evalError` | C |
| `decide_test.go` | الترتيب، و`read`/`write`، و**التقييم مرة واحدة** (عدّاد على كل شرط و`Documents` عدّادة: كلها 1)، والكتل التي لا تتسلسل، و`list` في Storage على الإصدار 1 | C |
| `lookup_test.go` | الحدود 10/20/2، والمخزَّن لا يُحسب، و`get` و`exists` على المسار نفسه قراءة واحدة، و`NoDocuments` خطأ يمتصه `\|\|`، وعطل `Documents` خطأ من `Evaluate` | C |
| `grammar_test.go` | **ضد انحراف Pyric:** كل ملف من حالات الترجمة يُترجم للخدمتين بالنتيجة النحوية نفسها | A |
| `fuzz_test.go` | `FuzzParse` و`FuzzEval` و`FuzzRegex`، بمهلة لكل مدخل وبذور من حالات الأوراكل، على نمط `rtdb/rules/fuzz_test.go` | D |
| `oracle_test.go` | حالات التطوير من `test/oracle/rules/` عبر `rulestest`: النسبة لكل مصدر وفئة، والفشل تحت 98% أو عند أي سماح يرفضه الإنتاج، وقائمة الفروق المعروفة باسم الحالة وسببها | D |
| `heldout_test.go` | `TestHeldOut`: يتخطى إلا بـ `ARUMA_RULES_HELDOUT=1`، ويقرأ `test/oracle/rules/heldout/` | D |
| `testdata/` | حالاتنا الصغيرة الخاصة بالوحدات (`*.rules` مع الأخطاء المتوقعة)، وبذور fuzz (`testdata/fuzz/...`). وحالات الأوراكل لا تُنسخ هنا: مكانها `test/oracle/rules/` | A إلى D |

### `internal/platform/rules/rulestest/`: أدوات الاختبار المشتركة

| الملف | ما فيه | المهمة |
|---|---|---|
| `fixtures.go` | قارئ صيغة S0-A: المصدر والتاريخ والإصدار والبصمة لكل ملف. حالات الترجمة (الملف، والنجاح، و`issues[]`)، وحالات التقييم (الخدمة، والمصدر، والطريقة، والمسار، و`auth`، و`resource`، و`request.resource`، والوقت، والمستندات، و`ALLOW`/`DENY`). ويحوّل الحالة إلى `rules.Request` بربط بسيط للخدمتين خاص بالاختبار | D |
| `documents.go` | `MemoryDocuments`: مستندات في الذاكرة، تعدّ كل قراءة (لاختبار «مرة واحدة» والحدود) | D |
| `report.go` | جدول النتيجة لكل مصدر وفئة، وأول الفروق، بصيغة تُلصق في `docs/compat` | D |

### خارج الحزمة: ما يُضاف أو يتغير

| المكان | التغيير | المرحلة |
|---|---|---|
| `internal/archtest/archtest_test.go` | قاعدة: `internal/platform/rules` لا يستورد من الداخل إلا `internal/platform/idtoken`. وحالات في `TestRulesThemselves`: `platform/rules` لا يستورد خدمة ولا `tenancy` ولا `store`، وStorage يستورده | S1-0 |
| `internal/config/config.go` | قسم `rules.limits` بحقول `rules.Limits`، وقيم ADR-021 الافتراضية، ومتغيرات `ARUMA_RULES_*` كما في `rtdb.limits` | S1-0 |
| `internal/app/app.go` | تحويل `config.Rules.Limits` إلى `rules.Limits`. ولا مستهلك في S1. وفي S2: Storage يأخذ الحدود و`Documents: rules.NoDocuments{}`. وفي v0.4: قراءات Firestore | S1-0 ثم S2 |
| `test/oracle/rules/` | ملفات الأوراكل ومصادرها، و`heldout/`. يملكه S0-A | S0-A |
| `NOTICE` | ذكر hidane وPyric لأي حالات أُخذت منهما. يملكه S0-A | S0-A |
| `tools/rules-diff/` **جديد** | مقارنة مع المحاكي الرسمي صندوقًا أسود: شروط من النحو، وطلبات عشوائية ببذرة، وتقليص إلى أصغر حالة مختلفة. لا يُشغَّل مع `go test` | S1-D |
| `.github/workflows/devices.yml` أو جدول أسبوعي | تشغيل `tools/rules-diff` ببذرة جديدة كل أسبوع | S7 |
| `internal/tenancy` و`internal/store/migrations/control/` | `rulesets` و`releases` بـ `project_id` وRLS (ADR-021 §7)، والقاعدة المقفلة الافتراضية لـ Storage | S2 ثم S5 |
| `internal/services/storage/core/rules.go` | ربط Storage: بناء `request` و`resource`، والطريقة لكل عملية، والمقاطع `b/<bucket>/o/...`، ورفض `list` على الإصدار 1 | S2 |
| `internal/admin/mgmt/rules.go` | firebaserules: `:test` و`rulesets` و`releases` بالأشكال التي ترسلها firebase-tools | S5 |
| `docs/compat/rules-v0.3.md` **جديد** | النسبة على كل مجموعة، والمحجوزة وحدها، وكل فرق مصنّف، والقيود (التحذيرات، والحدان الخاصان بنا، والنشر الفوري، و`debug()`) | S1-D ثم S7 |

## قرارات صغيرة داخل الهيكل

- **حزمة واحدة لا حزم فرعية للمحلل والقيم:**
  - المفسّر يحتاج داخل الشجرة والقيم، وتصديرها لحزم فرعية يجعلها واجهة عامة بلا مستهلك.
  - التوازي يأتي من ملفات منفصلة يملك كل منها مهمة، فوق عقد ثابت يُدمج أولًا.
  - و`rulestest` وحدها منفصلة لأن الخدمات والأدوات تستعملها.
- **الاسم `rules` لا `firestorerules`:** اللغة لخدمتين. والتعارض مع `rtdb/rules` اسمي فقط، فلا حزمة تستورد الاثنتين.
- **دوال القراءة في المحرك لا في الخدمة:** حتى لا يُكتب العدّ والذاكرة المؤقتة وقاعدة «المخزَّن لا يُحسب» مرتين.
- **`Auth` في المنصة:** حتى يكون `request.auth` واحدًا في الخدمتين. ولهذا وحده تستورد الحزمة `idtoken`.
- **لا ملف `constraint.go` في S1:** الحجز في الأنواع وفي الفرع الافتراضي يكفي، وملف فارغ يوهم بأن شيئًا بُني.
- **لا نسخ كود:** hidane (Rust) وPyric (TypeScript) وcel-go مراجع أفكار. ومحرك RTDB نمط. أما الحالات المأخوذة فبرخصها في `NOTICE`.

## تقسيم S1: commit عقد، ثم أربع مهام متوازية، ثم الدمج

### S1-0: العقد (يسبق الجميع، صغير)

يكتبه المدمج ويُدمج في فرع S1 قبل أن تبدأ المهام. ما فيه ثابت حتى نهاية S1، وتغييره يمر بالمدمج:

- `doc.go`، و`api.go` (الواجهة العامة بأجسام «not implemented»)، و`limits.go`، و`issues.go` (الأنواع فقط)، و`ast.go` كاملًا، و`value.go` (الأنواع ودوال البناء والقراءة)، و`meter.go`.
- **التوقيعات الداخلية التي تلتقي عندها المهام:**

  ```go
  // A: the front end.
  func parse(name, src string, lim Limits) (*file, []Issue)
  func position(src string, off int) (line, col, offset int)

  // B: the library, called by the evaluator.
  type evalError struct{ msg string; at int } // absorbed by && and ||
  type abort struct{ msg string }            // a limit: ends the evaluation, denies
  func callMethod(m *meter, recv Value, name string, args []Value) (Value, error)
  func callFunction(m *meter, name string, args []Value) (Value, error)
  func binary(m *meter, op token, x, y Value) (Value, error)
  func unary(op token, x Value) (Value, error)
  func compileLiteralRegex(pattern string) error // check.go rejects a literal that fails

  // C: compile and evaluate, on top of A and B.
  func check(f *file, svc Service, lim Limits) (*Ruleset, []Issue)
  ```

- **وفيه:** قاعدة archtest، وقسم الإعداد، والتحويل في `app`.

### المهام المتوازية

| المهمة | تملك | تعتمد على | تنتهي حين |
|---|---|---|---|
| **S1-A: النحو** | `lexer.go` و`parser.go` وتنفيذ `issues.go`، واختباراتها، و`grammar_test.go` | العقد فقط | كل حالات الترجمة النحوية في الأوراكل تطابق نجاحًا وموضعَ أول خطأ، و`FuzzParse` يعمل 10 دقائق بلا ذعر (بالتعاون مع D) |
| **S1-B: القيم والمكتبة** | `value.go` (التنفيذ) و`auth.go` و`equal.go` و`ops.go` و`strings.go` و`collections.go` و`time.go` و`path.go` و`regex.go` و`library.go`، واختباراتها | العقد فقط. تختبر بالقيم مباشرة دون محلل | كل دالة في مرجع rules.* لها حالة جدولية، والفروق التي قاسها hidane مغطاة، و`TestMatchesIsBounded` يمر |
| **S1-C: الترجمة والتقييم** | `check.go` و`match.go` و`eval.go` و`lookup.go` و`decide.go` و`trace.go`، و`api.go` (التنفيذ)، واختباراتها | العقد. وحتى يصل A وB: شجرة تُبنى يدويًا في الاختبار، ونسخة صغيرة من `binary` للأعداد والمنطق | جدول دلالة الخطأ، والتقييم مرة واحدة، والحدود، والإصداران، كلها تمر بأشجار مبنية يدويًا، ثم بالمحلل بعد دمج A |
| **S1-D: الأوراكل والأدوات** | `rulestest/` و`oracle_test.go` و`heldout_test.go` و`fuzz_test.go` و`tools/rules-diff/` وهيكل `docs/compat/rules-v0.3.md` | الواجهة العامة في العقد فقط | القارئ يقرأ كل ملفات S0-A، والتقرير يطبع النسبة لكل مصدر (صفرًا قبل اكتمال A وB وC)، و`rules-diff` يعمل على المحاكي محليًا ببذرة |

- **التوازي حقيقي:**
  - كل مهمة تملك ملفاتها، فلا تعارض في الدمج.
  - A وB لا يعرف أحدهما الآخر.
  - C يبدأ بأشجار يدوية.
  - D يرى الواجهة العامة وحدها.
- **ما لا يقرؤه أحد في S1:** `test/oracle/rules/heldout/`. يُذكر في تكليف كل مهمة، كحدود المجلدات في التكليف العام.

### S1-E: الدمج والقياس (بعد المهام)

1. **دمج** A ثم B ثم C ثم D في فرع S1، وتشغيل `oracle_test.go`، وإصلاح الفروق حتى 98% على مجموعة التطوير بلا أي سماح زائد.
2. **تشغيل المحجوزة مرة واحدة** بـ `ARUMA_RULES_HELDOUT=1`، وتسجيل الرقم قبل أي إصلاح، ثم الإصلاح، ثم ضمها إلى الحالات العادية.
3. **استبدال الافتراضيات المقيسة:** حين يصل قياس S0-A تُحدَّث في `DefaultLimits` وفي ADR-021: عمق الاستدعاء، و`let` في Storage، ووحدة العمود، وامتصاص أخطاء الحدود، والحد الدقيق للحجم.
4. **قبل الإغلاق:** `make lint` و`make test`، وfuzz عشر دقائق لكل هدف، و`docs/compat/rules-v0.3.md` بالأرقام.
