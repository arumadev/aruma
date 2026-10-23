# هيكل كود لغة قواعد Firestore وStorage

- **الحالة:** معتمد مع [ADR-021](../adr/ADR-021-rules-language.md)، 2026-10-23، بعد مراجعتين عدائيتين. يُحدَّث حيث يخالفه الكود، وكل ما لم يكن فيه يُعلَّم «لم يكن في المسودة»، كما في [rtdb-layout.md](rtdb-layout.md).
- **الأساس:**
  - ADR-021 (المحرك وواجهته).
  - ADR-015 (حدود الحزم وarchtest)، وADR-017 (العزل بـ `project_number` و`scopecheck`، بلا RLS).
  - طريقة `internal/platform/idtoken`: عقد في المنصة تربطه `internal/app`.
  - محرك قواعد RTDB (`internal/services/rtdb/rules`): نمطًا لا كودًا.
- **ما يتوقف على غيره:**
  - صيغة ملفات الأوراكل في `test/oracle/rules/`، ويملكها S0-A. يقرؤها `rulestest` وحده، فإن تغيرت تغيّر القارئ.
  - المجموعة المحجوزة على فرع `oracle-rules-heldout`، يعدّه S0-A، ولا يُدمج في فرع S1.
  - شكل `request` و`resource` في Storage، ومنه `request.method` و`request.params`: ADR-022 وS2.

## الصورة العامة

```
                        ┌──────────────── internal/platform/rules ────────────────┐
 Storage (S2) ──┐       │ Compile(svc, file, limits) ──► lexer ► parser ► AST      │
 Firestore (F5) ┼─────► │                                  ► check ► match paths   │
 tenancy/core ──┘       │                                  ► *Ruleset (ثابت)       │
 (CompiledRelease،      │ (*Ruleset).Evaluate(ctx, *Request) ──► decide            │
  و:test في S5)         │        ├─ eval: الخطأ قيمة، && || ?: let، عدّ التعابير    │
                        │        ├─ library: الأنواع ودوالها، regex                │
                        │        ├─ meter: العمل والتخصيص لكل تقييم، تحت Inflight   │
                        │        └─ lookup: Documents + Budget (عدّ وذاكرة مؤقتة)   │
                        └──────────────────────────────────────────────────────────┘
 الخدمة تقدّم: Project، وMethod، والمقاطع، و"request" و"resource" قيمًا، وDocuments، وBudget،
 وInflight الواحد للعملية كلها الذي ينشئه internal/app.
 الحزمة لا تستورد إلا Go القياسية. ولا خدمة ولا حزمة داخلية تُستورد فيها.
 internal/app يعطي Storage الكائن rules.NoDocuments{} في v0.3، وقراءات Firestore في v0.4.
```

**اتجاه الاعتماد:**

- **`internal/platform/rules`:** لا يستورد شيئًا من الداخل. يفرضه archtest كما يفرضه على `rtdb/rules` (`"internal/platform/rules": {}`).
- **`internal/platform/rules/rulestest`:** يستورد `rules`. وهو للاختبارات و`tools/` بالعرف وحده: archtest لا يميز ملفات `_test.go`، فلا ندّعي أنه يفرضه.
- **`internal/tenancy/core`:** يستورد `platform/rules` ليترجم الإصدارات ويحفظها (ADR-021 §7). وهذا مسموح بقاعدة `tenancy` الحالية.
- **`internal/services/storage/...` و`internal/services/firestore/...`:** تستورد `platform/rules` و`tenancy`، وهذا مسموح أصلًا بقاعدة `services/*`.
- **`internal/admin/mgmt`:** واجهة firebaserules في S5. تستورد `platform/rules` لـ `:test`، و`tenancy` للتخزين.

## الشجرة

العمود الأخير هو المهمة الفرعية في S1 (القسم الأخير). `0` هو commit العقد الذي يسبق الجميع.

### `internal/platform/rules/`: اللغة

| الملف | ما فيه | المهمة |
|---|---|---|
| `doc.go` | وصف الحزمة: لغة واحدة لـ `cloud.firestore` و`firebase.storage`، والمراجع (الوثائق، وhidane ADR 0005، والأوراكل بمصادره الثلاثة)، وما لا تدعمه (تحذيرات الأنواع الكاملة) | 0 |
| `api.go` | الواجهة العامة: `Service` و`File` و`Compile` و`Ruleset` (`Version` و`Service` و`Evaluate`) و`Method` و`Segment` (حرفي، و`UnknownSegment`، و`UnknownRun`) و`Request` و`Decision` و`TraceLevel` و`ErrOverloaded`. في commit العقد تذعر `Compile` و`Evaluate` برسالة «S1-C» | 0 ثم C |
| `limits.go` | `Limits` بنوعيه في ADR-021 §4، و`DefaultLimits()`، و`Validate()` التي ترفض حدًا من حدود Firebase أعلى من قيمة Firebase، وتترك حدود Aruma حرة | 0 |
| `issues.go` | `Issue` و`Position` (فارغ للخطأ على الملف كله) و`Severity` و`Issues` و`Err()`، ونصوص الرسائل الثابتة. ودالة `position` التي تحوّل الموضع بالبايت إلى سطر:عمود وإزاحات (code points حتى يُقاس الإنتاج)، وهي في العقد تذعر برسالة «S1-A» | 0 ثم A |
| `ast.go` | عقد الشجرة، ولكل عقدة `span{start, end int}` بالبايت، **ونوع `token`** (العمليات وعلامات الترقيم) الذي يتشاركه المحلل والمفسّر والمكتبة: `file` (الإصدار، والخدمة، والكتل)، و`match` (مقاطع النمط)، و`allow` (الطرق، والشرط أو لا شرط)، و`function` (الوسائط، و`let`، و`return`)، والتعابير (حرفي، ومعرّف، وعضو، وفهرس، وشريحة، واستدعاء، وأحادي، وثنائي، و`is`، و`in`، وثلاثي، وقائمة، وخريطة، ومسار حرفي بأجزاء `$(…)`) | 0 |
| `errors.go` | `evalError{msg, at}` الذي يمتصه `&&` و`\|\|`، و`abort{msg}` الذي ينهي التقييم برفض | 0 |
| `lexer.go` | الرموز كما قاسها hidane: لا أُسس ولا hex، والنصوص بعلامتي التنصيص والهروب المسموح فقط، و`b'…'`، و`--` و`++` رمزان، والتعليقات `//` و`/* */`، و`/seg` مقطع مسار حتى بعد عدد (`4/2` عدد ثم `/2`). وما ليس من اللغة يُسمّى بالاسم كما في lexer محرك RTDB | A |
| `parser.go` | نزول متكرر (recursive descent)، وأسبقية التعابير بجدول الوثائق: `a[i] a() a.f`، ثم `! -`، ثم `* / %`، ثم `+ -`، ثم `< <= > >=`، ثم `in`، ثم `is`، ثم `== !=`، ثم `&&`، ثم `\|\|`، ثم `?:`. ومعه `rules_version` والرأس `service` والكتل. يجمع كل الأخطاء، ويتعافى عند `;` و`}`. و**تداخل التعابير** (أقواس وعمليات وقوائم وثلاثي) بعمق 99 أو أكثر خطأ «Expression is too complex to evaluate safely.»، و98 يُترجم، وسلاسل الأعضاء والفهارس فوق 2,000 كذلك. في العقد: `parse` تذعر برسالة «S1-A» | 0 ثم A |
| `check.go` | الفحص: النطاقات المعجمية للدوال والمتغيرات، وربط كل معرّف واستدعاء بموضعه، وكشف الاستدعاء الدوري، و**عمق الاستدعاء على مخطط الاستدعاءات** برسالة «Maximum allowed call depth of 20 is reached for [f0->…] call stack.»، و`let` في الإصدار 1 خطأ، والحدود الثابتة (`let` والوسائط وعمق `match` ومقاطعها ومتغيراتها والحجم)، والتحذيرات (الدالة المجهولة وغير المستعملة)، والدوال المفعّلة لكل خدمة. في العقد: `check` تذعر برسالة «S1-C» | 0 ثم C |
| `match.go` | ترجمة كتل `match` إلى أنماط مقاطع مسطّحة (حرفي، و`{x}`، و`{x=**}`) بدلالة الإصدار 1 أو 2. **المتداخلة تحت عبارة فيها `=**` غير أخير لا تطابق أبدًا**، وتحت `=**` أخير تطابق على أي عمق. والمطابقة **بالبرمجة الديناميكية** على (موضع النمط، موضع المسار)، O(n·m)، والربط «الأقصر أولًا» يُستخرج من الجدول. و`{x}` نص، و`{x=**}` قيمة `Path`. و`UnknownSegment` يأخذه `{x}` و`{x=**}`، و`UnknownRun` لا يأخذه إلا `{x=**}`، والمتغير الذي أخذ أحدهما بلا قيمة | C |
| `value.go` | `Value` و`Kind`، ومنها `KindConstraint` و`KindUnknown` المحجوزان. ودوال البناء العامة (`Null` و`Bool` و`Int` و`Float` و`String` و`Bytes` و`List` و`Map` و`Timestamp` و`Duration` و`LatLng` و`PathOf`) والقراءة. واسم النوع في الرسائل ولـ `is` | 0 ثم B |
| `auth.go` | `Auth(uid string, token map[string]any) Value`: `request.auth` بـ `uid` و`token`، والأعداد الصحيحة في الرمز int. يمر بها الأوراكل و`:test` والخدمات | B |
| `equal.go` | **أربع علاقات مساواة، دالة لكل منها، بلا `==` على float64 داخل الحاويات** (ADR-021 §2.3): E1 للـ `==` في أعلى التعبير (`1 == 1.0`)، وE2 للحاويات (النوع يطابق، و`±0` قيمتان)، وE3 لعضوية القائمة ودوالها (النوع يطابق، و`±0` واحد)، وE4 للمجموعات و`diff` (بالقيمة بين الأنواع، و`±0` واحد، وNaN يساوي NaN) | B |
| `ops.go` | العمليات: `+ - * / %` مع الحساب المختلط، وطفح int64، والقسمة على صفر، والمقارنة لكل نوع، و`in` (E3 للقائمة وE4 للمجموعة)، و`is`، والفهرسة والشرائح بوحدات UTF-16. كل قيمة جديدة تُسجَّل على `meter` قبل بنائها (حجمها وتخصيصها). وكل عملية على نوع لا تعرفه، ومنه المحجوزان، تعيد خطأ. في العقد: `binary` و`unary` تذعران برسالة «S1-B» | 0 ثم B |
| `strings.go` | دوال النص: `size` و`lower` و`upper` و`trim` و`split` و`replace` و`matches` و`toUtf8` و`hasPrefix`…، والطول والفهرسة بـ UTF-16، والتخصيص على `meter` | B |
| `collections.go` | دوال `List` و`Map` و`Set` و`MapDiff`: `hasAll` و`hasAny` و`hasOnly` و`toSet` و`join` و`concat` و`removeAll` و`keys` و`values` و`get` و`diff` و`affectedKeys` و`difference` و`union` و`intersection`…، بتطبيق خطي أو `n log n`، وكل عنصر يُلمس يُحسب على عمل `meter` | B |
| `time.go` | `timestamp` و`duration`: `timestamp.date` و`timestamp.value` و`duration.value` و`duration.time` والدوال على القيم، وحدود السنوات 1 إلى 9999 و10,000 سنة | B |
| `path.go` | قيم `Path`: البناء من الحرفي ومن `path()` بفك ترميز URL للمقاطع، و`$(x)` النصي مقطع واحد و`$(p)` بقيمة `Path` يُدرج مقاطعها كلها، و`bind`، والفهرسة، والمقارنة | B |
| `regex.go` | RE2 من `regexp` على النص كله. **حجم البرنامج يُقاس قبل الترجمة** (`syntax.Parse` ثم `syntax.Compile`)، وفوق 10,000 تعليمة لا يُستدعى `regexp.Compile`: النص الحرفي خطأ ترجمة، والنمط المبني خطأ تقييم. وكلفة كل مطابقة (طول × تعليمات) على عمل `meter`، وترجمة الأنماط المبنية على عمله، وبايتات برامجها على تخصيصه. وذاكرة أنماط لكل `Ruleset` (256 نمطًا و4 MiB) **بقفل**، وصيغة الاستبدال في `replace` كما يحددها الأوراكل. في العقد: `compileLiteralRegex` تذعر برسالة «S1-B» | 0 ثم B |
| `library.go` | جدولا التوزيع: الدوال على كل نوع، والدوال العامة ومساحات الأسماء (`int()` و`float()` و`string()` و`math.*` و`hashing.*` و`latlng.value`). و`debug()` اسم يعرفه الفاحص، لكن استدعاءه خطأ «Function not found error: Name: [debug].» كما في Test API، المصدر الوحيد المقيس من الإنتاج. وهذا مؤقت حتى يُقاس المنشور ([rules-library-provisional.md](../compat/rules-library-provisional.md))، **لم يكن في المسودة**: كانت دالة هوية. يسجّل فيهما B، ويستدعيهما المفسّر عبر `callMethod` و`callFunction`. في العقد: كلتاهما تذعر برسالة «S1-B» | 0 ثم B |
| `meter.go` | `meter`: ما تحتاجه المكتبة من التقييم الجاري، **ويُنشأ جديدًا لكل `Evaluate`** (لكل عملية من الدفعة). `work(n)` و`alloc(bytes)` و`value(size, elems)` تعيد `abort` عند التجاوز. و`alloc` يحجز أيضًا من `Inflight`. و`Inflight` نفسه هنا: `NewInflight(bytes)`، وحجز ذري يعيد `ErrOverloaded` عند الامتلاء، ورد كل ما حجزه التقييم عند انتهائه | 0 |
| `eval.go` | المفسّر: الخطأ قيمة، والطرف غير المنطقي في `&&` و`\|\|` كالخطأ، و`?:` يقيّم الفرع المختار وحده، والوسائط و`let` تُقيَّم مبكرًا وتُربط قيمةً أو خطأً ولا تفشل إلا حين تُقرأ، وعدّ التعابير، وفحص `ctx` كل 64 تعبيرًا، و`abort` يُنهي | C |
| `lookup.go` | `Documents` (`Get(ctx, project, path, after)`) و`ErrNoDocuments` و`NoDocuments`، و`Budget` (حدود Firebase للقراءات وحدها: حد الطلب، وحد العملية، والذاكرة المؤقتة بمفتاح `(after, path)`. تتشاركه عمليات الدفعة). في العقد: الأنواع كاملة. والدوال `get` و`exists` و`getAfter` و`existsAfter` و`firestore.get` و`firestore.exists` وتفعيلها لكل خدمة، ورفض المسار خارج `/databases/(default)/documents/` | 0 ثم C |
| `decide.go` | `(*Ruleset).Evaluate`: رفض طلب غير صالح (`Documents` أو `Budget` أو `Inflight` فارغ، أو مسار أطول من 1,024 مقطعًا)، و`meter` جديد للتقييم يرد حجزه من `Inflight` في `defer`، والكتل المطابقة بترتيب الملف، والعبارات التي تشمل الطريقة (`read` = `get`+`list`، و`write` = `create`+`update`+`delete`)، وكل شرط مرة واحدة، وأول `true` يُوقف، و`No matching allow statements` | C |
| `trace.go` | `StatementResult` (الموضع، والطريقة، والنتيجة، والرسالة) في العقد. و`TraceExpressions` محجوز لتقرير `:test` في S5 | 0 ثم C |
| `constraint.go` | **F5، لا S1.** المقارنات على `Constraint` كما قاسها hidane (#66). في S1 لا يوجد هذا الملف، والفرع الافتراضي في `ops.go` يرفض | F5 |

#### الاختبارات في الحزمة نفسها

| الملف | ما فيه | المهمة |
|---|---|---|
| `lexer_test.go` و`parser_test.go` | جداول: كل شكل نحوي، ورفض المعجم الذي قاسه hidane، و`4/2` مقابل `4 / 2`، وتداخل 99 مقابل 98، ومواضع الأخطاء، ونص عربي وemoji في النصوص والتعليقات وأثرها في العمود | A |
| `issues_test.go` | تحويل الموضع: أسطر CRLF، وحروف خارج BMP، والإزاحات، والخطأ على الملف كله بلا موضع | A |
| `value_test.go` و`equal_test.go` و`ops_test.go` و`library_test.go` و`regex_test.go` | جداول لكل نوع ودالة: علاقات المساواة الأربع بحالات Pyric وhidane (`[0.0] == [-0.0]` و`-0.0 in [0.0]` و`1 in [1.0]` و`1 in [1.0].toSet()` وNaN في المجموعة)، والطفح، وUTF-16، و`$(p)` بقيمة `Path`، و`debug()` هوية | B |
| `bounds_test.go` | **حدود Aruma على المكتبة، بزمن محدود:** `TestConcatIsBounded` (نص يتضاعف يُرفض قبل حد القيمة أو التخصيص)، و`TestMatchesIsBounded` كما في RTDB، و`TestRegexProgramIsBounded` (نمط `\pL{1000}` المكرر يُرفض قبل `regexp.Compile`، حرفيًا ومبنيًا، بزمن وتخصيص محدودين)، وقوائم كبيرة في `hasAll` و`toSet` | B |
| `check_test.go` و`match_test.go` | النطاقات، والدوري، وعمق الاستدعاء على المخطط برسالته، و`let` في الإصدار 1، ودلالة `=**` في الإصدارين، و**المتداخلة تحت `=**` غير أخير (رفض) وتحت `=**` أخير (سماح على أي عمق)**، والمقطعان المجهولان، والربط عند تعدد المطابقة، و`TestMatchIsLinear` (1,024 مقطعًا أمام كتل متداخلة كثيرة `=**`، بزمن محدود) | C |
| `eval_test.go` | جدول دلالة الخطأ في ADR-021 حالةً حالة (ومنه `'horses' \|\| true` و`32 && false`)، و`?:` لا يقرأ في الفرع غير المختار، و`ignoresArg(1/0)` يسمح، و`let` الخاطئ غير المقروء، وعدّ التعابير، و`abort` مقابل `evalError` | C |
| `decide_test.go` | الترتيب، و`read`/`write`، و**التقييم مرة واحدة** (عدّاد على كل شرط و`Documents` عدّادة: كلها 1)، والكتل التي لا تتسلسل، و`list` في Storage على الإصدار 1، والطلب غير الصالح، و**التقييم كله محدود الزمن** (أكثر ما يتسع له حد التعابير من `matches` ودوال المجموعات ينتهي برفض)، و**الدفعة الكبيرة** (عمليات كثيرة كل منها قرب حد العمل تمر، والقراءة الحادية والعشرون فيها ترفض) | C |
| `concurrent_test.go` | `TestConcurrentEvaluate` تحت `-race`: طلبات كثيرة متزامنة على `Ruleset` واحد، فيها أنماط تُبنى وقت التقييم فتمر بذاكرة الأنماط. و`TestInflightIsBounded`: تقييمات متزامنة تبلغ حد `Inflight` تعيد `ErrOverloaded`، وكل ما حُجز يُرد بعدها فيقبل التقييم التالي | C |
| `lookup_test.go` | الحدود 10/20/2، والمخزَّن لا يُحسب، و`get` و`exists` على المسار نفسه قراءة واحدة، و`NoDocuments` خطأ يمتصه `\|\|`، وعطل `Documents` خطأ من `Evaluate`، والمشروع يصل `Documents` كما في الطلب | C |
| `grammar_test.go` | **ضد انحراف Pyric:** كل ملف من حالات الترجمة يُترجم للخدمتين بالنتيجة النحوية نفسها. مكتوب على الواجهة العامة، فيمر بعد الدمج | D |
| `fuzz_test.go` | `FuzzParse` و`FuzzEval` و`FuzzRegex`، بمهلة لكل مدخل وبذور من حالات الأوراكل دون المحجوزة، على نمط `rtdb/rules/fuzz_test.go` | D |
| `oracle_test.go` | حالات التطوير من `test/oracle/rules/` عبر `rulestest`: النسبة لكل مصدر (`deployed` و`rules-test-api` و`emulator`) وفئة، والفشل تحت 98% أو عند أي سماح يرفضه المصدر الأعلى، وقائمة الفروق المعروفة باسم الحالة وسببها، ومنها خصائص Test API | D |
| `heldout_test.go` | **يُكتب في S1-E لا قبله،** على فرع القياس الذي يُدمج فيه `oracle-rules-heldout` | E |
| `testdata/` | حالاتنا الصغيرة الخاصة بالوحدات (`*.rules` مع الأخطاء المتوقعة)، وبذور fuzz (`testdata/fuzz/...`). وحالات الأوراكل لا تُنسخ هنا: مكانها `test/oracle/rules/` | A إلى D |

### `internal/platform/rules/rulestest/`: أدوات الاختبار المشتركة

| الملف | ما فيه | المهمة |
|---|---|---|
| `fixtures.go` | قارئ صيغة S0-A: المصدر (`deployed` أو `rules-test-api` أو `emulator`) والتاريخ والإصدار والبصمة لكل ملف. حالات الترجمة (الملف، والنجاح، و`issues[]`)، وحالات التقييم (الخدمة، والمصدر، والطريقة، والمسار، و`auth`، و`resource`، و`request.resource`، والوقت، والمستندات، و`ALLOW`/`DENY`). ويحوّل الحالة إلى `rules.Request` بربط بسيط للخدمتين خاص بالاختبار. **يتخطى أي مجلد اسمه `heldout` بالبناء**، و`fixtures_test.go` يثبت ذلك بمجلد مؤقت | D |
| `documents.go` | `MemoryDocuments`: مستندات في الذاكرة لكل مشروع، تعدّ كل قراءة (لاختبار «مرة واحدة» والحدود وأن المشروع الصحيح يصل) | D |
| `report.go` | جدول النتيجة لكل مصدر وفئة، وأول الفروق، بصيغة تُلصق في `docs/compat` | D |

### خارج الحزمة: ما يُضاف أو يتغير

| المكان | التغيير | المرحلة |
|---|---|---|
| `internal/archtest/archtest_test.go` | قاعدة: `"internal/platform/rules": {}`، فلا يستورد شيئًا من الداخل. وحالات في `TestRulesThemselves`: `platform/rules` لا يستورد `idtoken` ولا خدمة ولا `tenancy` ولا `store`، وStorage و`tenancy/core` يستوردانه | S1-0 |
| `internal/config/config.go` | قسم `rules.limits` بحقول `rules.Limits`، وقيم ADR-021 الافتراضية، ومتغيرات `ARUMA_RULES_*` كما في `rtdb.limits`. ومنها `inflight_bytes` (512 MiB) و`compiled_rulesets_bytes` (128 MiB). والإقلاع يرفض حدًا من حدود Firebase أعلى من قيمة Firebase (`Limits.Validate`) | S1-0 |
| `internal/app/app.go` | تحويل `config.Rules.Limits` إلى `rules.Limits`، وإنشاء `rules.Inflight` واحد للعملية كلها. ولا مستهلك في S1. وفي S2: `tenancy` يأخذ الحدود، وStorage يأخذ `Inflight` و`Documents: rules.NoDocuments{}`، وفي S5 واجهة `:test` تأخذ `Inflight` نفسه. وفي v0.4: قراءات Firestore | S1-0 ثم S2 |
| `test/oracle/rules/` | ملفات الأوراكل بمصادرها الثلاثة، ومنها حالات المطابقة المتداخلة تحت `=**`. يملكه S0-A | S0-A |
| فرع `oracle-rules-heldout` | المجموعة المحجوزة، تحت `test/oracle/rules/heldout/`. لا يُدمج في فرع S1 | S0-A |
| `NOTICE` | ذكر hidane وPyric لأي حالات أُخذت منهما. يملكه S0-A | S0-A |
| `tools/rules-diff/` **جديد** | مقارنة مع المحاكي الرسمي صندوقًا أسود: شروط من النحو، وطلبات عشوائية ببذرة، وتقليص إلى أصغر حالة مختلفة. لا يُشغَّل مع `go test` | S1-D |
| `.github/workflows/devices.yml` أو جدول أسبوعي | تشغيل `tools/rules-diff` ببذرة جديدة كل أسبوع | S7 |
| `internal/store/migrations/control/` (migration جديد) | `control.rulesets` و`control.rules_releases`، وفي كل منهما `project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE`، وإشارة الإصدارة إلى ruleset من مشروعها نفسه. بلا RLS (ADR-017). وسطر في `released.lock` | S2 |
| `internal/tenancy/queries/` | الاستعلامات، و**الجدولان في قائمة `TestQueriesAreProjectScoped`** (`scope_test.go`) | S2 |
| `internal/tenancy/core/rules.go` | حفظ ruleset ونشر إصدارة، والقاعدة المقفلة الافتراضية لـ Storage، و`CompiledRelease(ctx, project, name)` مع ذاكرة LRU **بالوزن** تحت 128 MiB (وزن كل ruleset تقدير بايتات نصه وبرامج أنماطه الحرفية وسقف ذاكرة أنماطه)، يبطلها النشر نفسه. والإصدارة الغائبة أو الـ ruleset الذي لم يعد يُترجم ترفض كل شيء، مع تحذير في السجل | S2 ثم S5 |
| `test/acceptance/isolation_test.go` | حالات العزل للقواعد: مشروع لا يقرأ rulesets مشروع آخر ولا إصداراته، ولا ينشر عليها، ولا يشير بإصدارته إلى ruleset غيره | S2 ثم S5 |
| `internal/services/storage/core/rules.go` | ربط Storage: بناء `request` (ومنه `method` و`params`) و`resource`، والطريقة لكل عملية، والمقاطع `b/<bucket>/o/...`، ورفض `list` على الإصدار 1، والقواعد من `tenancy.CompiledRelease` | S2 |
| `internal/admin/mgmt/rules.go` | firebaserules: `:test` و`rulesets` و`releases` بالأشكال التي ترسلها firebase-tools، فوق `tenancy` | S5 |
| `docs/compat/rules-v0.3.md` **جديد** | النسبة لكل مصدر، والمحجوزة وحدها، وكل فرق مصنّف، والقيود (التحذيرات، وحدود Aruma، والنشر الفوري)، وخصائص Test API التي لا نحاكيها | S1-D ثم S7 |

## قرارات صغيرة داخل الهيكل

- **حزمة واحدة لا حزم فرعية للمحلل والقيم:**
  - المفسّر يحتاج داخل الشجرة والقيم، وتصديرها لحزم فرعية يجعلها واجهة عامة بلا مستهلك.
  - التوازي يأتي من ملفات منفصلة يملك كل منها مهمة، فوق عقد ثابت يُدمج أولًا.
  - و`rulestest` وحدها منفصلة لأن الخدمات والأدوات تستعملها.
- **الاسم `rules` لا `firestorerules`:** اللغة لخدمتين. والتعارض مع `rtdb/rules` اسمي فقط، فلا حزمة تستورد الاثنتين.
- **دوال القراءة في المحرك لا في الخدمة:** حتى لا يُكتب العدّ والذاكرة المؤقتة وقاعدة «المخزَّن لا يُحسب» مرتين.
- **`Auth` في المنصة بأنواع أولية:** حتى يكون `request.auth` واحدًا في الخدمتين وفي الأوراكل و`:test`، دون أن تستورد الحزمة `idtoken`.
- **عدّاد واحد للتقييم لا لكل استدعاء:** حد التعابير يسمح بمئات الاستدعاءات، فالحد على كل استدعاء وحده لا يحد التقييم.
- **عدّاد العمل والتخصيص لكل عملية لا للدفعة، وفوقهما `Inflight` للعملية كلها:** الدفعة تتشارك حدود Firebase وحدها، فلا تكون دفعة مشروعة أشد عندنا. والتزامن يحده `Inflight`.
- **الذاكرة المؤقتة للإصدارات في `tenancy` لا في الخدمات:** `tenancy` يملك النشر، فيبطل ذاكرته بنفسه بلا آلية إعلام. والخدمتان تقرآن منه.
- **لا ملف `constraint.go` في S1:** الحجز في الأنواع والمقاطع وفي الفرع الافتراضي يكفي، وملف فارغ يوهم بأن شيئًا بُني.
- **لا نسخ كود:** hidane (Rust) وPyric (TypeScript) وcel-go مراجع أفكار. ومحرك RTDB نمط. أما الحالات المأخوذة فبرخصها في `NOTICE`.

## تقسيم S1: commit عقد، ثم أربع مهام متوازية، ثم الدمج

### S1-0: العقد (يسبق الجميع)

يكتبه المدمج ويُدمج في فرع S1 قبل أن تبدأ المهام. ما فيه ثابت حتى نهاية S1، وتغييره يمر بالمدمج. **ولا تحتاج أي مهمة كود مهمة أخرى لتُترجم:**

- **الأنواع كاملة:** `doc.go`، و`api.go` (الواجهة العامة، ومنها `Segment` و`ErrOverloaded`)، و`limits.go`، و`issues.go` (`Issue` و`Position` و`Severity`)، و`ast.go` كاملًا ومعه `token`، و`errors.go` (`evalError` و`abort`)، و`value.go` (الأنواع ودوال البناء والقراءة)، و`meter.go` (`meter` و`Inflight` كاملين، فهما صغيران ويستعملهما B وC معًا)، و`lookup.go` (`Documents` و`ErrNoDocuments` و`NoDocuments` و`Budget`)، و`trace.go` (`StatementResult`).
- **دوال تذعر، كل منها في ملف المهمة التي تستبدلها،** فلا يلمس ملفًا إلا مالكه:

  ```go
  // parser.go and issues.go (S1-A)
  func parse(name, src string, lim Limits) (*file, []Issue) { panic("S1-A: parse") }
  func position(src string, at span) Position // provisional, working: code points, "\n" ends a line

  // ops.go, library.go, regex.go, path.go and auth.go (S1-B)
  func binary(m *meter, op token, x, y Value) (Value, error)                  { panic("S1-B: binary") }
  func unary(m *meter, op token, x Value) (Value, error)                      { panic("S1-B: unary") }
  func selectField(m *meter, x Value, name string) (Value, error)             { panic("S1-B: selectField") }
  func selectIndex(m *meter, x, i Value) (Value, error)                       { panic("S1-B: selectIndex") }
  func selectSlice(m *meter, x Value, lo, hi *Value) (Value, error)           { panic("S1-B: selectSlice") }
  func typeIs(x Value, typ string) (Value, error)                             { panic("S1-B: typeIs") }
  func callMethod(m *meter, recv Value, name string, args []Value) (Value, error) { panic("S1-B: callMethod") }
  func callFunction(m *meter, name string, args []Value) (Value, error)       { panic("S1-B: callFunction") }
  func compileLiteralRegex(rx *regexCache, pattern string) error              { panic("S1-B: compileLiteralRegex") }
  func isLibraryFunction(name string) bool                                    // working: the rules.* list
  func isLibraryNamespace(name string) bool                                   // working
  func (rx *regexCache) literalBytes() int64                                  // working: 0 until B keeps programs
  func buildPath(m *meter, parts []pathPart) (Value, error)                   { panic("S1-B: buildPath") }
  func Auth(uid string, token map[string]any) Value                           { panic("S1-B: Auth") }

  // api.go, check.go and decide.go (S1-C)
  func Compile(svc Service, f File, lim Limits) (*Ruleset, Issues)              { panic("S1-C: Compile") }
  func check(f *file, svc Service, lim Limits) (*Ruleset, []Issue)              { panic("S1-C: check") }
  func (rs *Ruleset) Evaluate(ctx context.Context, req *Request) (Decision, error) { panic("S1-C: Evaluate") }
  ```

  والخطأ الذي تعيده دوال B إما `*evalError` (يُمتص) أو `*abort` (يُنهي)، ولا شيء غيرهما.
- **وفيه:** قاعدة archtest، وقسم الإعداد مع `Limits.Validate`، والتحويل في `app`.

#### قرارات commit العقد (لم تكن في المسودة)

حسمها S1-0 حيث سكت الهيكل أو تعارض، وكلها في الكود:

- **دوال تذعر زائدة على القائمة، كلها في ملفات B:** `selectField` و`selectIndex` و`selectSlice` و`typeIs` في `ops.go`، و`buildPath` مع `pathPart` في `path.go`، و`Auth` في `auth.go`. الهيكل يجعل `a.f` و`a[i]` و`a[i:j]` و`is` وبناء المسار الحرفي من B، ولم يعطها توقيعًا، فكان C سيكتب نسخًا خاصة منها. أما `in` فعملية ثنائية (`tokIn`) تمر بـ `binary`.
- **`position(src, at span)` لا `position(src, off int)`، وتعمل ولا تذعر:** `Position` يحمل `CurrentOffset` و`EndOffset`، فيحتاج طرفي المدى. وفي العقد نسخة مؤقتة تعدّ code points و`\n` نهاية السطر، حتى لا تنتظر اختبارات C مواضعَ A. يستبدلها A (CRLF، ووحدة الإنتاج) ويملك اختبارها.
- **أسماء المكتبة للفاحص:** `isLibraryFunction` و`isLibraryNamespace` في `library.go` تعملان من الآن بقائمة مرجع rules.* (`int` و`float` و`string` و`path` و`debug`، و`math.*` و`hashing.*` و`latlng.value` و`timestamp.*` و`duration.*`)، فيربط C الاستدعاءات قبل دمج B. ويبقيها B مطابقة لـ `callFunction`. ودوال القراءة ليست فيها: هي لـ `lookup.go`.
- **`(*Ruleset).Weight()`** في `api.go`: النص، وبرامج الأنماط الحرفية (`regexCache.literalBytes()`، صفر حتى يحفظها B)، وسقف ذاكرة الأنماط. يقرؤه LRU في `tenancy`.
- **`compileLiteralRegex(rx *regexCache, pattern)`:** يأخذ ذاكرة أنماط الـ ruleset ليحفظ فيها برنامج النمط الحرفي (وزن الـ ruleset يحسب برامج أنماطه الحرفية). ونوع `regexCache` و`newRegexCache` (فارغة لا تذعر) في `regex.go` يملؤهما B. و`meter` يحمل `regexes`، فتصل `callMethod` إلى الذاكرة دون `Ruleset`.
- **أسماء عقد التعابير بلاحقة `Expr`** (`binaryExpr` و`unaryExpr` و`callExpr`…)، لأن `binary` و`unary` أسماء الدوال في القائمة. وأنواع الاستدعاء `callsUser` و`callsFunction` و`callsMethod` و`callsLookup` للسبب نفسه. وحقول الربط التي يملؤها الفاحص (`identExpr.kind/slot` و`callExpr.kind/user/name`) في `ast.go` من الآن، فلا يلمسه C.
- **`token` للعمليات وعلامات الترقيم وحدها:** الحرفيات والمعرّفات ومقاطع المسار أصناف معجمية يعرّفها A في `lexer.go`. والكلمات المحجوزة (`service` و`match` و`allow`…) معرّفات عند المعجم، يميزها المحلل بنصها.
- **`MethodGet` و`MethodList`… لا `Get` و`List`:** `List` دالة بناء القيمة. وكذلك `SeverityError` و`SeverityWarning`.
- **`abort{msg, err}`:** `err` هو العطل خلفه (`ErrOverloaded`، أو عطل `Documents`، أو `ctx`)، و`nil` لحد من حدود القواعد. فتبقى أخطاء المكتبة `*evalError` أو `*abort` وحدهما، ويعيد `Evaluate` العطل بـ `errors.As`.
- **`meter.value(bytes, elems)` يفحص حجم القيمة ولا يحسب شيئًا،** و`alloc` وحده يحسب التخصيص ويحجز من `Inflight` بالبايت (CAS، بلا دفعات). ويُفحص حد التقييم قبل `Inflight`: الأول رفض، والثاني `ErrOverloaded`.
- **`Budget` بلا حدود:** `NewBudget()` يعدّ قراءات الطلب ويحمل الذاكرة المؤقتة فقط، و`Evaluate` يأخذ الحدود من الـ ruleset نفسه بـ `lookupLimits(rs.service, &rs.lim)`، فلا يعطي `Budget` صُنع لخدمة حدودَ خدمة أخرى. وعدّ العملية الواحدة عند التقييم. وفي Storage حد الطلب = حد العملية = 2، وخدمة مجهولة صفر. والفاشل لا يُخزَّن.
- **`Value`:** الصفر ليس قيمة (`kindInvalid`)، فكل عملية عليه خطأ. و`==` عليه لا يُترجم (`_ [0]func()`)، لأنه يذعر على قائمتين: المساواة دوال `equal.go`. و`AsDuration` يرفض ما لا تتسع له `time.Duration` حتى النانوثانية. والتمثيل `kind` و`x any` (يغيّره B إن شاء)، و`duration` ثوانٍ ونانوثوانٍ لأن ±10,000 سنة لا تتسع لها `time.Duration`. وأسماء الأنواع في `Kind.String()` مبدئية، يطابقها B بالأوراكل.
- **`StatementResult{Position, Methods []string, Outcome, Message}`:** الطرق كما كُتبت في العبارة (`read, write`)، و`Outcome` ثلاث قيم: `false` و`true` و`error`.
- **`Segment`:** `Literal(name)` و`Literals(names...)` و`Name() (string, bool)`، والصفر هو الحرفي الفارغ.
- **الإعداد:** `config.RulesLimits` نسخة من `rules.Limits` حقلًا بحقل بوسوم yaml، فالتحويل `rules.Limits(c.Rules.Limits)` تحويل نوع يفرض المترجم تطابقه، والقيم الافتراضية `RulesLimits(rules.DefaultLimits())`. يُفحص عند التحميل بـ `Validate`، و`Config.RulesEngineLimits()` يعيده لـ `app`. ورسائل الرفض بأسماء المفاتيح (`rules.limits.call_depth 21 is above Firebase's 20…`).
- **`Validate`** يرفض أيضًا `inflight_bytes` أقل من `alloc_bytes` (كل تقييم كان سيعيد `ErrOverloaded` أبدًا)، و`value_bytes` أكبر من `alloc_bytes`.
- **archtest:** `TestStandardLibraryOnly` يرفض أي استيراد في `internal/platform/rules` أول عنصر في مساره فيه نقطة، لأن `TestDependencyRules` لا يرى إلا استيرادات الوحدة نفسها.
- **اختبارات العقد** في `contract_test.go` و`limits_test.go` و`meter_test.go`. وحالات `value_test.go` التي تبني القيم من تمثيلها (حدود `AsDuration`) وضعها العقد هناك لأنها لـ B، يغيّرها مع التمثيل.

### المهام المتوازية

| المهمة | تملك | تعتمد على | تنتهي حين |
|---|---|---|---|
| **S1-A: النحو** | `lexer.go` و`parser.go` و`position` في `issues.go`، واختباراتها | العقد فقط | اختباراتها الجدولية تمر: كل شكل نحوي، ورفض المعجم الذي قاسه hidane، و`4/2`، وتداخل 98 يُترجم و99 يُرفض، ومواضع الأخطاء. ولا تقرأ الأوراكل: `rulestest` في D وحده يقرؤه، ومطابقة حالاته في S1-E |
| **S1-B: القيم والمكتبة** | تنفيذ `value.go`، و`auth.go` و`equal.go` و`ops.go` و`strings.go` و`collections.go` و`time.go` و`path.go` و`regex.go` و`library.go`، واختباراتها ومنها `bounds_test.go` | العقد فقط. تختبر بالقيم مباشرة دون محلل | كل دالة في مرجع rules.* لها حالة جدولية، وعلاقات المساواة الأربع مغطاة بحالات Pyric وhidane، و`TestConcatIsBounded` و`TestMatchesIsBounded` و`TestRegexProgramIsBounded` تمر |
| **S1-C: الترجمة والتقييم** | `check.go` و`match.go` و`eval.go` و`decide.go`، والدوال في `lookup.go` و`trace.go`، وتنفيذ `api.go`، واختباراتها | العقد فقط. اختباراتها تبني الأشجار يدويًا، وتقتصر حتى دمج B على ما لا يمر بـ `binary` ولا بالمكتبة: الحرفيات و`&&` و`\|\|` و`?:` و`let` والدوال والقراءات والمطابقة. **ولا نسخة خاصة من `binary`** | جدول دلالة الخطأ، والتقييم مرة واحدة، والحدود، والإصداران، والمطابقة المتداخلة و`TestMatchIsLinear`، كلها تمر بأشجار مبنية يدويًا. ثم المقارنات والمحلل بعد دمج A وB |
| **S1-D: الأوراكل والأدوات** | `rulestest/` و`oracle_test.go` و`grammar_test.go` و`fuzz_test.go` و`tools/rules-diff/` وهيكل `docs/compat/rules-v0.3.md` | الواجهة العامة في العقد فقط | القارئ يقرأ كل ملفات S0-A ويتخطى `heldout/` باختبار، والتقرير يطبع النسبة لكل مصدر (صفرًا قبل اكتمال A وB وC)، و`rules-diff` يعمل على المحاكي محليًا ببذرة |

- **التوازي حقيقي:**
  - كل مهمة تملك ملفاتها، فلا تعارض في الدمج.
  - A وB لا يعرف أحدهما الآخر.
  - C يبدأ بأشجار يدوية على ما يملكه هو.
  - D يرى الواجهة العامة وحدها.
- **المجموعة المحجوزة ليست على فرع S1،** فلا يقرؤها أحد في S1 ولو أراد.

### S1-E: الدمج والقياس (بعد المهام)

1. **دمج** A ثم B ثم C ثم D في فرع S1، وتشغيل `oracle_test.go` و`grammar_test.go`، وإصلاح الفروق حتى 98% على مجموعة التطوير بلا أي سماح زائد على المصدر الأعلى. ومنها حالات الترجمة النحوية: نجاحًا وموضعَ أول خطأ. و`FuzzParse` عشر دقائق بلا ذعر.
2. **القياس على المحجوزة مرة واحدة:** فرع قياس من فرع S1 يُدمج فيه `oracle-rules-heldout`، ويُكتب فيه `heldout_test.go`. يُسجَّل الرقم قبل أي إصلاح، ثم الإصلاح، ثم تنضم المحجوزة إلى الحالات العادية.
3. **استبدال الافتراضيات المقيسة:** حين يصل قياس S0-A تُحدَّث في `DefaultLimits` وفي ADR-021: عمق الاستدعاء، و`let` في Storage، ووحدة العمود، وامتصاص أخطاء الحدود، والحد الدقيق للحجم، وNaN في علاقات المساواة.
4. **قبل الإغلاق:** `make lint` و`make test` و`go test -race` للحزمة، وfuzz عشر دقائق لكل هدف، و`docs/compat/rules-v0.3.md` بالأرقام.
