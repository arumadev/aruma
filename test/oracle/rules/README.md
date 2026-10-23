# مرجع لغة قواعد الأمان (Firestore وCloud Storage)

هذا المجلد هو «الحقيقة المرجعية» (oracle) للغة Security Rules التي يشترك فيها Firestore وCloud Storage.
بُني في المرحلة S0-A قبل أي كود للمحرّك، ليختبر به بُناة S1 محرّكَهم. ما فيه بيانات ووثائق فقط:
لا كود Go، وأدوات التسجيل في `tools/` مكتوبة بـ Node.

النتائج المستخلصة منه (الحدود بأرقامها، جدول دلالات الأخطاء، سلوك الدوال، الفروق بين المحاكي والإنتاج،
وما لم تستطع Rules Test API التعبير عنه) في
[docs/research/rules/A-oracle-findings.md](../../../docs/research/rules/A-oracle-findings.md).
والفروق بين المحاكي وRules Test API حالةً حالة في [parity-exceptions.md](parity-exceptions.md).

## المجموعة المحجوبة (held-out)

جزء من الحالات محجوب عن بُناة S1 حتى لا يدخل في بذور الـ fuzz ولا في الضبط، ولا يقرؤه إلا الاختبار الأخير
**S1-E**. هو ليس في هذا الفرع أصلًا: يعيش في الفرع **`oracle-rules-heldout`** تحت
`test/oracle/rules/heldout/`، ولا شيء من معرّفاته أو محتواه في ملفات هذا الفرع. فيه:

- قرابة خُمس حالاتنا المسجّلة عبر Rules Test API: كل حالة غير مثبّتة تُحجب إن وقعت قيمة
  `sha256("aruma-s0a-rules-heldout-v1:<topic>/<id>")` (أول أربعة بايتات قسمةً على 2^32) تحت 0.25
  (`heldOut` في `tools/oracle.mjs`). الحالات «المثبّتة» (`pin`) هي التي تستشهد بها وثيقة النتائج، فتبقى هنا.
- شروط hidane المحجوبة (`spikes/rules-eval/cases/rules-heldout.json`) ونتائج المحاكي لها، ونتائج
  Rules Test API لها.
- وحدات تعريف الحالات (`cases-src/*.mjs`) نفسها، لأنها تعرّف الحالات المحجوبة أيضًا. ملفات JSON هنا
  مكتفية بذاتها: في كل حالة القاعدة والطلب والمورد والنتيجة.

سُجّلت المجموعتان في طلبات API منفصلة، فلا يحوي أي طلب أو ruleset أو ردّ هنا حالةً محجوبة.

## الملفات

```
production/<topic>.json        حالاتنا: ما قرّرته Rules Test API (provenance: rules-test-api)
production/raw/<topic>.json    كل طلب أُرسل وردّه كما هو (بلا أي اعتماد أو token)
emulator/<topic>.json          الحالات نفسها على المحاكيات الرسمية (provenance: emulator)
parity-exceptions.md           كل حالة اختلف فيها المحاكي عن Rules Test API، وآثار Test API
imported/hidane/original/      ملفات hidane كما هي (انظر أدناه عن ملف النتائج)
imported/hidane/normalised/    نتائج المحاكي لشروط hidane بالمخطط الموحّد
imported/pyric/original/       ملاحظات Pyric وملفات الـ corpus التي سُجّلت منها، كما هي
imported/pyric/normalised/     الملاحظات نفسها بالمخطط الموحّد
tools/                         أدوات التسجيل والاستيراد والمقارنة
```

الموضوعات (`<topic>`) في `production/` و`emulator/`:

| الموضوع | الخدمة | ماذا يغطي |
|---|---|---|
| `encoding` | firestore | كيف تقرأ Test API الـ JSON: int وfloat، النصوص التي تشبه الوقت، `request.time`، `request.auth`، المورد الغائب |
| `errors` | firestore | انتقال الخطأ عبر `\|\|` و`&&` و`!` والشرط الثلاثي والدوال و`let`، المفاتيح الناقصة، أخطاء النوع، القسمة على صفر، فيض int |
| `limits` | firestore | عمق نداء الدوال، العودية، عدد `let`، تعقيد التعبير، حدّ الألف تعبير، حجم الـ ruleset، `get()`/`exists()` |
| `strings` | firestore | `size` وUnicode، `matches` (RE2)، `split`، `replace`، `lower`/`upper`، `trim`، الفهرسة، التحويلات |
| `math` | firestore | int وfloat، `math.*`، `hashing.*`، bytes، latlng |
| `time` | firestore | timestamp وduration: الحقول، البناء، الحساب، الوحدات |
| `collections` | firestore | list وset وmap و`diff` |
| `paths` | firestore | نوع path، `$(var)`، متغيرات match، `{x=**}` في rules_version 1 و2، تداخل match |
| `methods` | firestore | `read`/`write` مقابل `get`/`list`/`create`/`update`/`delete`، `request.method` |
| `storage` | storage | حقول `resource` و`request.resource`، claims، الطرق، `firestore.get/exists`، أساسيات اللغة |
| `storage-paths` | storage | `{name=**}` في 1 و2، صفر مقاطع، match متداخل، bucket |
| `followups` | firestore | جولة ثانية حسمت ما تركته الأولى غامضًا (crc32، `lower`، `split('')`، `{rest=**}` في v1…) |
| `hidane-conditions` | firestore | كل شرط من شروط hidane عبر Test API، لمقارنته بما قرّره المحاكي له |

## المخطط الموحّد `aruma.rules-oracle/v1`

كل ملف JSON من الملفات المطبّعة كائن فيه `schema` و`rulesets` (نصوص rulesets مشتركة بالاسم)،
و`resources` (موارد مشتركة بالاسم)، و`cases`. كل حالة:

| الحقل | المعنى |
|---|---|
| `id` | معرّف ثابت: `aruma/<topic>/<id>` أو `hidane/<name>` أو `pyric/<service>/<scenario>/<n>` |
| `service` | `firestore` أو `storage` |
| `source` | من سجّلها: `aruma` أو `hidane` أو `pyric` |
| `provenance` | على أي شيء سُجّلت: `deployed` (قواعد منشورة فعلًا وطلبات حقيقية)، `rules-test-api` (‏`projects.test`)، `emulator` |
| `recordedAt`، `version` | متى، وعلى أي إصدار (API، أو المحاكي وإصدار Java، أو commit المصدر) |
| `description` | وصف قصير |
| `expression` | الشرط، إن كانت الحالة شرطًا واحدًا |
| `ruleset` أو `rulesetRef` أو `rulesetGenerated` | الـ ruleset نصًّا، أو اسمه في `rulesets`، أو مولّده (`generate` في `tools/oracle.mjs`، لحالات الحجم). في `rulesets` لـ hidane قالب فيه `{{expr}}` يحلّ محلّه `expression` |
| `request` | الطلب بشكل Test API: `method`، `path`، `auth`، `time`، `resource` (أي `request.resource`)، `query` |
| `resource` أو `resourceRef` | المورد الموجود بشكل Test API (Firestore: ‏`{data: …}`؛ Storage: الكائن نفسه)، أو اسمه في `resources` |
| `functionMocks` | محاكاة `get()`/`exists()` (أو `firestore.get()`/`firestore.exists()` في Storage) بشكل Test API |
| `valueEncoding` | غائب = JSON كما تقرؤه Test API؛ `firestore-rest` = قيم Firestore REST المنمّطة (hidane) |
| `rawNumbers` | `[{from, to}]`: في نص الطلب المرسل كُتب الرقم `from` بالنص `to` (مثل `2.0`، وهو ما لا يكتبه `JSON.stringify`) |
| `expected` | النتيجة: `outcome` واحد من `allow` و`deny` و`error`، ومعه `reason` (`false` أو `no-matching-statement`) أو `errorKind` (`evaluation` أو `compile` أو `request`)، و`message` كما جاءت |
| `visited` | قيم التعبيرات العليا التي زارتها Test API (`visitedExpressions`) |
| `emulator` | `skip: <السبب>` إن لم يكن تشغيلها على المحاكي ممكنًا |

`error` يعني أن القرار رفضٌ سببه خطأ تقييم (أو ruleset لا يُترجم، أو طلب رفضته الـ API نفسها). `deny`
رفض لأن الشرط `false` أو لأن لا عبارة `allow` تطابق. ملاحظة: Test API تُرجع الرسالة نفسها لخطأ في
أي عبارة، فالمرجع هو `outcome`؛ الرسائل وأرقام الأسطر للقراءة والتشخيص.

## كيف أُنتج كل ملف

### `production/` (مصدرنا، provenance `rules-test-api`)

- `tools/record-production.mjs` يأخذ وحدات الموضوعات (في الفرع المحجوب)، ويقسم كل موضوع إلى مجموعتي
  التطوير والمحجوبة، ويجمع شروط كل مجموعة في rulesets من 60 حالة (match لكل حالة:
  `match /c_<id>/{doc}`)، ويرسل كل ruleset مع حالاته في طلب واحد إلى
  `POST https://firebaserules.googleapis.com/v1/projects/chatapp-8ae21:test`. الحالة التي لا يُترجم شرطها
  تُخرج من الحزمة وتُسجّل وحدها؛ وحالات الحدود والحجم وقواعد match تُسجّل وحدها من البداية.
- `projects.test` يقيّم المصدر المعطى دون نشره ودون قراءة أي بيانات: **لم تُنشر أي قواعد** على المشروع.
- الـ token: من تسجيل دخول firebase CLI على هذا الجهاز، عبر وحدة `lib/auth.js` في firebase-tools نفسها
  (`tools/rulesapi.mjs`). لا يُطبع ولا يُكتب في أي ملف، وطلبات `production/raw/` لا تحويه.
- كل حالة تُرسل بـ `expectation: ALLOW`؛ `SUCCESS` = ‏`allow`. ما سواه: `error` إن جاء
  `errorPosition` أو رسالة `Error:`، وإلا `deny`.
- القيم الافتراضية: `request.auth` هو `alice` بـ claims ثابتة (`ALICE` في `tools/oracle.mjs`)،
  `request.time` هو `2026-01-15T10:20:30.123Z`، والمورد هو `DOC` (Firestore) أو `OBJECT` (Storage)، إلا إن
  حدّدت الحالة غير ذلك. Test API تأخذ `request` و`resource` حرفيًّا: الحقل غير المرسل غير معرّف في
  القواعد، فالطلب بلا مستخدم يُرسل `auth: null` والمستند الغائب `resource: null`.
- التاريخ: 2026-10-23/24. الإصدار: `firebaserules.googleapis.com v1 projects.test` (لا تذكر الـ API إصدار
  محرّكها).

### `emulator/` (provenance `emulator`)

`tools/record-emulator.mjs` يشغّل حالات `production/` نفسها على المحاكيات الرسمية كصناديق سوداء (لم يُفتح
أي JAR):

- Firestore: `cloud-firestore-emulator-v1.22.0.jar` على Java 21 (`--host 127.0.0.1 --port 8840`، مشروع
  `demo-oracle`). تُحمّل القواعد بـ `PUT /emulator/v1/projects/demo-oracle:securityRules`، وتُكتب مستندات
  الحالة (المورد وأهداف `get()`/`exists()`) بصفة المالك، ثم يُرسل الطلب عبر REST بـ ID token غير موقّع
  يحمل `auth` الحالة. لا يمكن عبر REST اختيار `request.time` ولا إرسال `request.query`، فتلك الحالات تُتخطّى.
- Storage: `cloud-storage-rules-runtime-v1.1.3.jar` على Java 21، عبر بروتوكول stdin/stdout نفسه الذي
  يستعمله محاكي Storage في firebase-tools 13.23.0 (`tools/storagert.mjs`)، والمتغيرات (`request`،
  `resource`) مبنية كما يبنيها firebase-tools، بقيم الحالة.
- `tools/compare.mjs` يكتب جداول [parity-exceptions.md](parity-exceptions.md).

### `imported/hidane/` (‏github.com/hidane-dev/hidane، ‏MIT أو Apache-2.0؛ نستعمله بـ Apache-2.0)

- commit ‏`abd4664b0d610781eb3f0fec45642e7bc9953533`. استورده `tools/import-hidane.mjs`.
- `original/results/rules-conditions-official-v1.22.0.development.json`: ملف hidane
  `results/rules-conditions-official-v1.22.0.json` (ما قرّره محاكي Firestore الرسمي v1.22.0 لكل شرط) كما
  هو، بعد إخراج أسطر `heldout/*` منه إلى الفرع المحجوب (سطر لكل نتيجة، فالباقي بايتات الأصل نفسها،
  مع تصحيح الفاصلة الأخيرة فقط). سجّلته hidane في 2026-10-11 بمحاكي تحت لغة إنجليزية.
- `original/cases/rules.json` و`cel-spec.json` كما هما. `cel-spec.json` مأخوذ من
  google/cel-spec (‏v0.25.3، ‏Apache-2.0) كما يذكر الملف نفسه.
- `original/tools/conditions.py` و`rules_conditions.py`: الأداتان اللتان أنتجتا الملف، للأثر.
- `normalised/rules-conditions-emulator.json`: النتائج بالمخطط الموحّد (provenance `emulator`، المستند
  بقيم REST المنمّطة).
- `production/hidane-conditions.json` (عندنا): الشروط نفسها عبر Test API. المستند بأقرب JSON: `ts` نص
  RFC 3339 (تقرؤه Test API timestamp)، و`big` الرقم `9223372036854775807`؛ أما `one_f` و`geo` و`ref` و`by`
  فلا تُعبّر عنها Test API بنوعها، فالحالات التي تقرؤها عليها `note: comparable: false`.

### `imported/pyric/` (‏github.com/davideast/pyric، ‏Apache-2.0)

- commit ‏`756d23efae7e0147db820b7ce9c1672ba2a6d8a6`. استورده `tools/import-pyric.mjs`.
- `original/observations/{firestore-rules,storage-rules}/` كما هي، و`original/rules-corpus/` ملفات
  السيناريوهات التي سُجّلت منها (TypeScript، كما هي)، و`original/src/rules-math-cases.ts` الذي تستورده.
- `normalised/{firestore,storage}-rules.json`: كل حالة من الـ corpus مع الحكم الذي سجّلته الملاحظة لها،
  والطلب بشكل Test API كما يبنيه Pyric. `rules-*` سُجّلت عبر Test API (provenance `rules-test-api`).
  `stdlib-realstorage-*` سُجّلت على Storage حقيقي بقواعد منشورة (`deployed`)؛ قواعدها يبنيها مشغّل Pyric
  فهي في `rulesets` حين تحفظها الملاحظة وإلا تُشير `rulesetNote` إلى المشغّل. القيم التي ليست قرارًا
  (علاقات بين حقول، أزمنة) مسجّلة بـ `outcome: fact`.
- بيانات تعريفية: لا رسائل بريد حقيقية ولا مفاتيح. بقيت معرّفات مشاريع Pyric (`agentive-de`،
  `digame-mas`، `genkit-idx`) وأسماء الـ buckets ومعرّفات التشغيل في الرسائل كما هي، فهي تسميات فقط.

إشعارات الترخيص في [NOTICE](../../../NOTICE).

## إعادة التسجيل

يحتاج Node 24، وfirebase CLI مسجّل الدخول، وJava 21 للمحاكيات. على هذا الجهاز تفشل اتصالات Node إلى
Google عبر IPv6، فيلزم:

```sh
export NODE_OPTIONS="--no-network-family-autoselection --dns-result-order=ipv4first"
cd test/oracle/rules/tools

# الحالات (وحدات cases-src في الفرع oracle-rules-heldout):
node record-production.mjs --dev ../production --heldout <heldout>/production <cases-src>/*.mjs hidane-conditions.mjs

# المحاكيات (محاكي Firestore يعمل على 127.0.0.1:8840):
node record-emulator.mjs --firestore 127.0.0.1:8840 --out ../emulator ../production/<topic>.json …
node compare.mjs ../emulator ../production

# الاستيراد:
node import-hidane.mjs <hidane checkout> <commit> <heldout dir>
node import-pyric.mjs <pyric checkout> <commit>
```

موضوع سُجّل سابقًا (ملفه في `--dev` موجود) يُتخطّى؛ احذف ملفه لتسجيله من جديد.
