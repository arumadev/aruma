# توافق لغة قواعد الأمان (Firestore وCloud Storage): الإصدار 0.3

- **الحالة:** هيكل كتبه S1-D، 2026-10-24، قبل دمج المحرك (S1-A وB وC). الأرقام يملؤها S1-E بعد الدمج، والمحجوزة
  يقيسها S1-E مرة واحدة قبل أي إصلاح، ويكمله S7 بالقيود والنشر.
- **القرار:** [ADR-021](../adr/ADR-021-rules-language.md)، والقبول في §6: ‏98% أو أكثر على مجموعة التطوير، وعلى
  المحجوزة كلٌّ على حدة، لكل مصدر وحده، ولا فرق واحد نكون فيه أكثر تساهلًا من المصدر الأعلى.
- **المرجع:** [test/oracle/rules/](../../test/oracle/rules/README.md) (S0-A)، ونتائجه في
  [A-oracle-findings.md](../research/rules/A-oracle-findings.md)، والفروق بين المحاكي وTest API في
  [parity-exceptions.md](../../test/oracle/rules/parity-exceptions.md).

## المصادر

المصادر ثلاثة بترتيب الحجية، ولكل حالة مصدرها في `provenance`. عند التعارض يغلب `deployed` الكل، ويغلب
`rules-test-api` المحاكي في دلالة اللغة وحدها.

| المصدر (`provenance`) | ما هو | من سجّله | الحالات (التطوير) | ما يحسمه |
|---|---|---|---|---|
| `deployed` | قواعد منشورة على Storage حقيقي وطلبات من مكتبة Firebase | Pyric | 274 | كل شيء. لا طلب مسجّلًا فيها يُعاد على المحرك وحده: تُقاس مع ربط Storage في S2 |
| `rules-test-api` | ‏`firebaserules.googleapis.com/v1/projects/chatapp-8ae21:test` | نحن (2034)، وPyric (1649) | 3683 | دلالة اللغة: القيم والعمليات والدوال والمطابقة والترجمة |
| `emulator` | `cloud-firestore-emulator` v1.22.0 و`cloud-storage-rules-runtime` v1.1.3، صندوقين أسودين | نحن (689، حالاتنا نفسها)، وhidane (1345) | 2034 | ما لم يسجّله غيره |

- **القارئ:** `internal/platform/rules/rulestest` يقرأ الملفات كلها في نوع واحد، ويسجّل لكل ملف مصدره وتاريخه
  وإصداره وبصمته (SHA-256). لا يدخل أي مجلد اسمه `heldout`، ويرفض ملفًا يقول إنه من المحجوزة
  (`TestLoadSkipsHeldOut`).
- **الربط:** كل حالة تُعطى للمحرك كما رآها مصدرها، حتى يكون الفرق فرقَ اللغة لا فرقَ الربط:
  - `rules-test-api`: ‏`request` و`resource` خريطتان حرفيتان، والحقل غير المرسل غائب، و`null` قيمة. والـ JSON يُقرأ
    كما تقرؤه Test API (انظر «آثار Test API»).
  - `emulator`: كما بناه مسجّل المحاكي والمحاكي نفسه: `request.auth` بـ `null` لطلب بلا مستخدم، والمستند بـ `id`
    و`__name__`، و`request` في Storage بلا `method`، والأرقام كما يقرؤها JavaScript.
  - قيم hidane بصيغة Firestore REST المنمّطة.
- **ما لا يُعدّ:** يُشغَّل ويُطبع، لكنه خارج النسبة: القيم التي ليست قرارًا (`fact`)، وما لم يشغّله مصدره، والطلب
  الذي رفضته Test API نفسها (إلا رفض الحجم، فهو رفض ترجمة)، وآثار Test API وأخطاء المحاكي أدناه. القائمة
  حالةً حالة يطبعها `TestOracle`، وقواعدها في `rulestest/known.go`.

## كيف يُقاس

```sh
go test ./internal/platform/rules -run 'TestOracle|TestGrammar' -v     # النسبة لكل مصدر، والفروق
ARUMA_RULES_ORACLE=report go test ./internal/platform/rules -run TestOracle -v   # تقرير بلا فشل
go test ./internal/platform/rules -run '^$' -fuzz FuzzParse -fuzztime 10m      # وFuzzEvaluate وFuzzRegex
go run ./tools/rules-diff -seed 1 -n 1000                                   # مقارنة بالمحاكي ببذرة
```

- **`TestOracle`:** كل حالة تطوير عبر `Compile` ثم `Evaluate`. حالة التقييم تطابق إن طابق القرار (سماح أو لا)،
  وحالة الترجمة إن طابق النجاح أو الفشل وموضع أول خطأ. ويُبلَّغ بجانبها عن النتيجة الأدق (رفض أم خطأ، ونص
  الرسالة) دون أن تدخل النسبة. يفشل تحت 98% لأي مصدر، أو عند أي سماح رفضه المصدر ومن فوقه.
- **`TestGrammar`:** كل ملف قواعد مختلف (1936 ملفًا) يُترجم وحده. وخطأ النحو يقع في الموضع نفسه حين يُسمّي
  إعلان الخدمة الخدمةَ الأخرى، فالنحو واحد للخدمتين.
- **Fuzz:** بذور من حالات التطوير وحدها، ومهلة ثانيتين لكل ترجمة وتقييم.
- **`tools/rules-diff`:** شروط مولّدة من النحو وطلبات عشوائية ببذرة، على المحاكي وعلى المحرك، وكل فرق يُقلَّص إلى
  أصغر شرط يبقى فيه. يُشغَّل يدويًا وفي جدول أسبوعي (S7)، لا مع `go test`.

## نسبة التطابق

*يملؤها S1-E بعد دمج A وB وC، بالجدول الذي يطبعه `TestOracle` كما هو. قبل الدمج النسبة صفر لكل مصدر:
`Compile` هو عقد S1-0 الذي يذعر.*

### مجموعة التطوير

| provenance | source | cases | excluded | counted | agree | ratio | finer | permissive |
|---|---|---|---|---|---|---|---|---|
| deployed | pyric | 274 | 274 | 0 | | | | |
| rules-test-api | aruma | 2034 | 15 | 2019 | | | | |
| rules-test-api | pyric | 1649 | 39 | 1610 | | | | |
| emulator | aruma | 689 | 49 | 640 | | | | |
| emulator | hidane | 1345 | 0 | 1345 | | | | |

- `finer`: من المعدودة، ما طابقت نتيجته الأدق أيضًا. `permissive`: سماح رفضه المصدر ومن فوقه؛ يجب أن يكون صفرًا.
- حالات الترجمة وحدها (`TestGrammar`): *يملؤها S1-E.*

### المجموعة المحجوزة

*يقيسها S1-E مرة واحدة على فرع القياس الذي يُدمج فيه `oracle-rules-heldout`، وتُسجَّل هنا قبل أي إصلاح، لكل
مصدر وحده.*

### `rules-diff`

*البذرة وعدد الحالات والفروق بعد التقليص: يملؤها S1-E، ثم الجدول الأسبوعي في S7.*

## الفروق المعروفة

كل حالة لا تطابق تُصنَّف هنا في واحد من أربعة: **خطأ عندنا** يُصلح، أو **فرق مقصود** بسببه، أو **خطأ في
المحاكي**، أو **أثر من Test API**. *القائمة حالةً حالة يكتبها S1-E.*

### أخطاء المحاكي، وطريقته في الربط

| الفرق | الحالات | ما نفعله |
|---|---|---|
| حدّ القراءات في محاكي Firestore يعدّ تقييمَي الشرط معًا (firebase-tools #6252) | `aruma/limits/exists_get_same_6` | نتبع Test API والوثائق: القراءة نفسها لا تُعدّ مرتين |
| محاكي Storage لا يطبّق حدّ القراءات: ثلاثة مستندات و11 تُسمح، وStorage المنشور يرفض الثالث | `aruma/storage/fs_lookups_3`، `aruma/storage/fs_lookups_11` | خارج النسبة، والحدّ تثبته حالات المرساة (`rulestest.Anchors`): 10 تُسمح و11 تُرفض في Firestore، و2 تُسمح و3 تُرفض في Storage |
| `list` في Storage: المحاكي يطابق أبناء المسار لا المسار نفسه | `aruma/storage/read_list`، `aruma/storage/method_list`، `aruma/storage-paths/list_on_folder` | لم يُحسم على المنشور؛ S2 |

### فروق مقصودة

*يكتبها S1-E وS7: حدود Aruma (ADR-021 §4) حيث تمنع ما يقبله Firebase، والتحذيرات التي لا نصدرها، والنشر الفوري.*

## آثار Test API

سلوك لـ Test API نفسها لا للغة: لا نحاكيه في المحرك. حالاتها في `rules-test-api` إمّا يحاكيها الربط كما أُرسلت
(فتبقى في النسبة)، وإمّا تخرج منها بسببها.

| الأثر | الحالات | في النسبة؟ |
|---|---|---|
| تأخذ `request` حرفيًّا: بلا `auth` يكون `request.auth == null` خطأً «Property auth is undefined on object»؛ وكذلك `time` و`query` و`resource` | `aruma/encoding/auth_absent`، `aruma/storage/auth_absent`، `aruma/encoding/time_unset_is_timestamp`، Pyric `hierarchical-match-cascade` | نعم: الربط يحذف الحقل كما حذفه الطلب |
| تأخذ `resource` حرفيًّا: بلا `resource` يكون `resource == null` خطأً، ولا `id` ولا `__name__` فيه | `aruma/encoding/resource_absent_is_null`، `aruma/encoding/resource_name`، `aruma/hidane-conditions/rules/138`…`149` | نعم |
| الأرقام: الصحيح منها int ولو كُتب `2.0` أو `1e2`، وما فوق 2^53 يمر بـ double | `aruma/encoding/json_2_0_is_int`، `aruma/encoding/json_1e2_is_int`، `aruma/encoding/json_2pow53_plus1_is_int` | نعم: الربط يقرؤها كذلك |
| نص بصيغة RFC 3339 يصير timestamp، ونص يبدأ بـ `/databases/` يصير path، و`{latitude, longitude}` يصير latlng | `aruma/encoding/json_ts_string_is_timestamp`، `aruma/hidane-conditions/rules/11`، `145`، `150` | نعم |
| نتيجة `get()` المحاكاة بلا `id` ولا `__name__` | `aruma/storage/fs_get_id`، `aruma/storage/fs_get_name` | لا: الإنتاج يسمّي المستند |
| `get()`/`exists()` لمستند غير محاكى «Function not found»، والمحاكاة بنتيجة `undefined` «Service call error» | `aruma/limits/get_unmocked`، `aruma/limits/get_mock_null` | نعم: الربط يجعلهما `ErrNoDocuments`، وهو خطأ تقييم |
| لا حدّ لعدد القراءات المحاكاة: 50 مستندًا في Firestore، و3 و11 في Storage | `aruma/limits/exists_11`…`exists_50`، `aruma/limits/get_11`، `aruma/storage/fs_lookups_3`، `aruma/storage/fs_lookups_11`، `pyric/storage/firestore-lookup-budget/6` | لا: نطبّق 10/20 و2 كما في الوثائق وStorage المنشور (`pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/three`) |
| `debug()` غير موجودة | `aruma/errors/debug_function`، `aruma/storage/debug` | لا: المحاكي والإنتاج فيهما `debug()` |
| تقبل الطريقة `write` في الطلب | `aruma/methods/method_write_verb` | لا: لا طلب حقيقي طريقته `write` |
| ترفض طلبًا لا قواعد: وقت بغير صيغته، ومسار Storage ينتهي بـ `/`، و`503` لـ ruleset كبير | `aruma/encoding/time_bad_format`، `aruma/storage-paths/list_on_prefix`، `aruma/limits/size_functions_2000` | لا |
| حجم المصدر يُقاس على رسالة الطلب (المحتوى والاسم و25 بايتًا) | `aruma/followups/size_comment_262119`، `aruma/followups/size_comment_262120` | نعم: رفض الحجم رفض ترجمة |
| `request.auth.uid` أي نوع، ومسار Firestore لا يُشترط أن يسمّي مستندًا | `aruma/encoding/auth_uid_int`، `aruma/followups/nested_under_recursive_deep` | نعم: لغة لا ربط |
| ملف مصدر واحد لكل طلب | Pyric `stdlib-storage-p0-source-boundaries` | لا: قواعده لم تُسجَّل |

## القيود

*يكتبها S7: التحذيرات التي لا نصدرها (ADR-021 §2.1)، وحدود Aruma بقيمها، والنشر الفوري، و`list` في Firestore
(F5).*
