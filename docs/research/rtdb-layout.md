# هيكل كود Realtime Database

- **الحالة:** مسودة، 2026-10-20. تصبح ADR بعد قرارات [الخطة](rtdb-plan.md). لم يُكتب منها سطر كود بعد.
- **الأساس:** تخطيط الخدمات في [ADR-015](../adr/ADR-015-services-layout.md)، وطريقة خدمة Auth الحالية (`internal/services/auth`)، والمدخل الواحد في [ADR-008](../adr/ADR-008-single-entry.md).
- **ما يتوقف على قرار:**
  - شكل `databaseURL` (Q1). الهيكل هنا مبني على التوصية: النطاق الفرعي، والاسم من أول جزء في `Host`.
  - التخزين (Q2). الهيكل مبني على التوصية: صف لكل ورقة. وإن تغيّر القرار تتغيّر ملفات `store` وحدها.

## الصورة العامة

```
                 ┌──────────────────────────── internal/services/rtdb ─────────────────────────────┐
 SDK (wss) ───►  │ api/ws  ─► api/conn ─► api/dispatch ─┐                                           │
 SDK (https) ─►  │ api/rest ────────────────────────────┼─► core (Service)                           │
 الكونسول ────►  │ console/ ────────────────────────────┘     ├─ registry ─► Database (كاتب لكل قاعدة) │
                 │                                             │     ├─ rules (هل مسموح؟)              │
                 │ wire/ (إطارات ورسائل البروتوكول 5)           │     ├─ store (Postgres)              │
                 │                                             │     └─ listeners ─► دفعات للجلسات     │
                 └─────────────────────────────────────────────┴──────────────────────────────────────┘
 الاعتماد على خارج الخدمة: platform/idtoken (عقد رمز Auth)، platform/authn (حسابات الخدمة)،
 tenancy (المشاريع)، store/migrations/rtdb (المخطط). ولا استيراد لخدمة Auth نفسها (archtest).
```

**اتجاه الاعتماد داخل الخدمة** (يفرضه archtest كما في ADR-015):
- **`wire`:** لا يستورد شيئًا من الخدمة. Go القياسية فقط.
- **`rules`:** لا يستورد شيئًا من الخدمة. يقرأ البيانات عبر واجهة لقطة يعرّفها هو.
- **`core`:** يستورد `rules` و`queries`. ولا يستورد `api` ولا `console` ولا `platform/httpx` (القاعدة 2 في ADR-015).
- **`api` و`console`:** يستوردان `core` و`wire`.

## الشجرة

العمود الأخير هو المرحلة التي يُكتب فيها الملف (R1 إلى R7 في [الخطة](rtdb-plan.md#المراحل)).

### `internal/services/rtdb/wire/`: بروتوكول Firebase الإصدار 5، بلا شبكة ولا قاعدة

| الملف | ما فيه | المرحلة |
|---|---|---|
| `doc.go` | وصف الحزمة: البروتوكول غير موثّق، والمرجع كود المكتبات بأرقام commit | R1 |
| `frames.go` | تجميع الرسالة المقسّمة: إطار العدد ثم الأجزاء. وتقسيم الرسالة الكبيرة بوحدات UTF-16 على حدود الحروف، بحد 16384. وتجاهل إطار `0`. وسقف إعادة التجميع بالبايت | R1 |
| `envelope.go` | الأغلفة: `{t:"c"}` للتحكم، و`{t:"d"}` للبيانات، والطلب `{r,a,b}`، والرد `{r,b:{s,d}}`، والدفعة `{a,b}` | R1 |
| `control.go` | رسائل التحكم: المصافحة `h` بحقولها، و`s` للإيقاف، و`r` لإعادة التوجيه، و`e`، والرد `o` على `p` | R1 |
| `actions.go` | ثوابت الأوامر (`q n p m g o om oc auth gauth unauth appcheck unappcheck s`)، وفك جسم كل أمر إلى نوع Go | R1 |
| `query.go` | كائن الاستعلام (`sp sn sin ep en ein l vf i`) من وإلى `core.Query` | R3 |
| `push.go` | بناء الدفعات `d` و`m` مع `p` و`d` دائمًا، و`t` للمستعلم، و`c` و`ac` | R3 |
| `status.go` | نصوص الحالة: `ok`، `permission_denied`، `datastale`، `no_index`، `too_big`، `invalid_token`، `expired_token`، `unavailable` | R1 |
| `frames_test.go` وأخواته | اختبارات جدولية، منها نص عربي وemoji على حد الإطار | R1 |
| `testdata/golden/*.json` | إطارات مسجّلة من المحاكي الرسمي 4.11.2. لكل ملف: المصدر (محاكي أو إنتاج)، والإصدار، وبصمة SHA-256 | R1 إلى R3 |

### `internal/services/rtdb/core/`: نموذج البيانات والكاتب، مستقل عن طريقة الاتصال

| الملف | ما فيه | المرحلة |
|---|---|---|
| `service.go` | `Service`: نقطة الدخول التي يستدعيها `api` و`console`. فيها `Get` و`Set` و`Update` و`Listen` و`Unlisten` و`OnDisconnect` و`Rules` | R2 |
| `path.go` | نوع `Path`: التطبيع (`/a/b` و`a/b`)، والأحرف المحظورة `. # $ [ ] /` وأحرف التحكم، وطول المفتاح 768 بايت، والعمق 32 | R2 |
| `node.go` | الشجرة في الذاكرة: ورقة أو أبناء، والأولوية، وصيغة التصدير (`.value` و`.priority`)، و`{}` و`null` حذف، وقاعدة المصفوفات في REST | R2 |
| `order.go` | الترتيب: المفاتيح الشبيهة بالأعداد أولًا، وترتيب الأنواع (null ثم false ثم true ثم الأرقام ثم النصوص ثم الكائنات)، والترتيب بالأولوية | R2 |
| `hash.go` | بصمة Firebase للعقدة: SHA-1 ثم base64 على الصيغة القياسية، ومنها الأرقام بصيغة IEEE-754 hex. نفسها قيمة `h` وقيمة ETag | R2 |
| `servervalues.go` | `{".sv":"timestamp"}` و`{".sv":{"increment":n}}`، تُحل داخل الكاتب وقت التطبيق | R2 |
| `write.go` | أوامر الكتابة: الكتابة، والتحديث متعدد المسارات (ذري)، والكتابة المشروطة بالبصمة (`datastale`)، والتحقق من الحدود | R2 |
| `database.go` | **الكاتب:** ممثل واحد لكل قاعدة. صندوق وارد، وتطبيق بالترتيب، وجمع عدة أوامر في معاملة، والتوزيع بعد اكتمال الحفظ فقط | R3 |
| `registry.go` | خريطة الاسم ← الكاتب: تشغيل عند أول طلب، وإيقاف بعد خمول، وحدود كل مشروع | R3 |
| `listeners.go` | شجرة المستمعين بالمسار، والوسوم، ونوافذ الاستعلامات المستمعة (ما دخل وما خرج) | R3 |
| `query.go` | تقييم الاستعلام: نوع الترتيب، والبداية والنهاية والحد. يحدد هل يحتاج `.indexOn`، ويرتّب عند الطلب بلا فهرس دائم | R3 |
| `events.go` | من تغيير محفوظ إلى دفعات: `d` لمسار واحد، و`m` لعدة أبناء، ولكل مستمع وسمه | R3 |
| `session.go` | حالة الجلسة بلا شبكة: هوية المستخدم، والاستماعات، وترتيب الردود (بيانات الاستماع قبل تأكيده، ودفعة الكتابة قبل تأكيدها) | R3 |
| `disconnect.go` | أوامر onDisconnect لكل جلسة، تُنفَّذ بالترتيب عند الانقطاع عبر الكاتب نفسه | R3 |
| `principal.go` | متغير `auth` في القواعد: المستخدم (uid والمطالبات)، أو المسؤول، أو المجهول، و`auth_variable_override` | R4 |
| `revoke.go` | قطع البث: عند تعطيل المستخدم أو إلغاء رموزه، يُرسل `ac` وتُلغى الاستماعات بـ `c` | R4 |
| `limits.go` | `Limits`: العمق، والمفتاح، وحجم الكتابة، والاتصالات والمستمعون لكل مشروع، والكتابة في الدقيقة، وحجم طابور الخروج بالبايت | R2 |
| `errors.go` | أخطاء الخدمة، وتحويلها إلى حالات البروتوكول وأكواد REST | R2 |
| `store.go` | واجهة التخزين التي يستعملها الكاتب: قراءة شجرة فرعية، وتطبيق دفعة كتابات في معاملة، وقراءة القواعد وحفظها | R2 |
| `store_pg.go` | تنفيذها على Postgres عبر `queries`. صف لكل ورقة، ومسار `COLLATE "C"`، واستعلام نطاق على البادئة | R2 |

### `internal/services/rtdb/rules/`: لغة القواعد، نكتبها بأنفسنا

| الملف | ما فيه | المرحلة |
|---|---|---|
| `ruleset.go` | قراءة ملف القواعد (JSON) إلى شجرة: `.read` و`.write` و`.validate` و`.indexOn` و`$متغيرات`، مع حد الحجم وأخطاء بمواضعها | R4 |
| `lexer.go` و`parser.go` و`ast.go` | محلّل التعابير، يدويًا بأسلوب Pratt. قواعد الصياغة مأخوذة فكرةً من Bolt (Apache-2.0)، لا كودًا | R4 |
| `eval.go` | التقييم: الأنواع، والعمليات، والمقارنة، والخطأ أثناء التقييم يعني رفضًا | R4 |
| `methods.go` | دوال اللقطة (`val` و`child` و`parent` و`exists` و`hasChild` و`hasChildren` و`isString`…) ودوال النص (`length` و`contains` و`beginsWith` و`matches`…) | R4 |
| `regex.go` | ترجمة `/…/` في القواعد إلى `regexp` في Go، ورفض ما لا يدعمه Firebase | R4 |
| `check.go` | التشغيل: `CanRead` و`CanWrite` بتسلسل القواعد عبر الشجرة، و`.validate` على الأبناء المكتوبين فقط، و`newData` تراكب فوق البيانات الحالية | R4 |
| `snapshot.go` | واجهة اللقطة التي يقرأ بها المحرك `data` و`newData` و`root`، وينفّذها `core` | R4 |
| `testdata/targaryen/fixtures.json` و`LICENSE` | 186 حالة سُجّلت مقابل Firebase، مع نص رخصة ISC | R4 |

### `internal/services/rtdb/queries/`: sqlc، كما في Auth

| الملف | ما فيه | المرحلة |
|---|---|---|
| `nodes.sql` | قراءة شجرة فرعية بنطاق البادئة، وكتابة الأوراق، وحذف شجرة فرعية | R2 |
| `databases.sql` | القاعدة لكل مشروع: إنشاؤها، والقواعد، والإعدادات | R2 |
| `*.gen.go` | ملفات sqlc المولّدة | R2 |
| `scope_test.go` | كل استعلام مقيّد بالقاعدة، كما في Auth | R2 |

### `internal/services/rtdb/api/`: الطرف العام

| الملف | ما فيه | المرحلة |
|---|---|---|
| `api.go` | `Handler`: اسم القاعدة من أول جزء في `Host` تحت `rtdb_domain`، ومن `?ns=` للتطوير المحلي، ورفض الطلب إن اختلفا. وتوزيع المسارات: `/.ws` و`/<path>.json` و`/.settings/rules.json`. و`/.lp` يرجع 404 موثّقًا | R1 |
| `ws.go` | الترقية إلى WebSocket بمكتبة coder/websocket، وفحص Origin (رفض المختلف وقبول الغائب)، وإرسال المصافحة، وتتبع الاتصالات لإغلاقها عند إيقاف الخادم | R1 |
| `conn.go` | حلقة القراءة وحلقة الكتابة، وطابور خروج محدود بالبايت يُغلق المتباطئ، ومهلة خمول 120 ثانية، وإرسال `s` قبل الإيقاف | R1 |
| `dispatch.go` | كل أمر إلى استدعاء في `core`، وكل نتيجة إلى رد بالحالة الصحيحة | R1 إلى R3 |
| `auth.go` | `auth` و`gauth` و`unauth`: رمز المستخدم عبر `idtoken.Verifier`، ورمز حساب الخدمة (Admin) عبر `authn.Verifier`. و`appcheck` يُقبل ويُتجاهل | R4 |
| `rest.go` | `GET` و`PUT` و`POST` و`PATCH` و`DELETE` على `.json`، و`print` و`shallow` و`format=export`، وETag و`if-match` (412)، وأخطاء JSON بصيغة Firebase | R5 |
| `rest_query.go` | `orderBy` و`limitToFirst` و`limitToLast` و`startAt` و`endAt` و`equalTo`، و400 «Index not defined» | R5 |
| `rest_auth.go` | `auth=` و`access_token=` و`Authorization: Bearer`، و`auth_variable_override` لمكتبات Admin | R5 |
| `tlsask.go` | نقطة `ask` لشهادات Caddy عند الطلب: 200 إن كان الاسم مشروعًا موجودًا. **تُخدم على العنوان الخاص فقط** | R6 |

### `internal/services/rtdb/console/`: شاشات اللوحة، عبر `consolemount` كما في Auth

| الملف | ما فيه | المرحلة |
|---|---|---|
| `console.go` | `Routes(svc)`: تسجيل المسارات بسياسة `anyCred` | R6 |
| `data.go` | تصفح الشجرة صفحةً صفحة (أبناء مباشرون، مثل `shallow`)، وتعديل قيمة، وحذف | R6 |
| `rules.go` | قراءة القواعد وحفظها، وأخطاء الصياغة بمواضعها | R6 |

### خارج الخدمة: ما يتغيّر أو يُضاف

| المكان | التغيير | المرحلة |
|---|---|---|
| `internal/platform/idtoken/` **جديد** | العقد الذي وعد به ADR-015: واجهة `Verifier` تتحقق من رمز مستخدم Auth لمشروع (ومنه فحص الإلغاء)، وواجهة `Revocations` تُعلم بتعطيل مستخدم أو إلغاء رموزه. Auth ينفّذهما، وRTDB يستعملهما، و`internal/app` يربطهما | R4 |
| `internal/services/auth/core/` | تنفيذ `idtoken.Verifier` و`idtoken.Revocations`، دون أن تعرف Auth بوجود RTDB | R4 |
| `internal/store/migrations/rtdb/00001_init.sql` **جديد** | المخطط `rtdb`: جدول `databases` (لكل مشروع: الاسم والقواعد ورقم الترتيب)، وجدول `nodes` (المسار `COLLATE "C"` والقيمة والأولوية، والمفتاح `(database, path)`) | R2 |
| `internal/store/migrations/migrations.go` و`released.lock` و`sqlc.yaml` | إضافة القسم `rtdb` كما يقول ADR-015، «إضافة خدمة جديدة» | R2 |
| `internal/server/server.go` | حقل `Handlers.RTDB`. التوجيه بالـ `Host` قبل المسار: كل طلب إلى `*.<rtdb_domain>` يذهب إلى RTDB، و`/.ws` على النطاق الرئيسي مع `?ns=` للتطوير. واستثناء اتصالات WebSocket من مهلة 60 ثانية ومن حد الجسم 1 MB، مع إغلاقها المنظم عند الإيقاف | R1 |
| `internal/config/config.go` | قسم `rtdb`: `domain`، والحدود الافتراضية، وعنوان `ask` الخاص | R1 |
| `internal/tenancy/core/projects.go` | صيغة `databaseURL` الجديدة: `https://<project>-default-rtdb.<rtdb_domain>` بدل `?ns=`. وتحديث `internal/configgen/testdata` | R6 |
| `internal/app/app.go` | بناء خدمة RTDB، وربطها بـ `idtoken` من Auth، وإضافة `console.Routes` إلى `Mounts`، وتحديث `testdata/console_routes.txt` | R1 ثم R6 |
| `internal/archtest/` | لا تعديل للقواعد الحالية، فهي تغطي `services/*`. وتُضاف قاعدة: `wire` و`rules` لا يستوردان شيئًا من الخدمة | R1 |
| `go.mod` | `github.com/coder/websocket` (رخصة ISC). وهو الاعتماد الجديد الوحيد | R1 |
| `docs/adr/ADR-008-single-entry.md` | تعديل: النطاق الفرعي لـ RTDB صار جزءًا من المدخل | R0 |

### اللوحة: `dashboard/src/features/database/`

| الملف | ما فيه | المرحلة |
|---|---|---|
| `DatabaseSection.tsx` | قسم Realtime Database في صفحة المشروع، ومعه `databaseURL` جاهزًا للنسخ | R6 |
| `DataScreen.tsx` و`.module.css` | عارض الشجرة: يفتح العقد عند الطلب، ويعدّل ويحذف | R6 |
| `NodeEditor.tsx` | تعديل قيمة أو إضافة ابن، مع التحقق من المفتاح والحدود قبل الإرسال | R6 |
| `RulesScreen.tsx` و`.module.css` | محرر القواعد، وأخطاء الصياغة بأرقام الأسطر | R6 |
| `dashboard/src/lib/api/console.ts` و`types.ts` | دوال الطلب وأنواعها للمسارات الجديدة | R6 |
| `*.test.tsx` واختبارات e2e في `dashboard/e2e` | كما في شاشات Auth | R6 |

### الاختبارات والأدوات

| المكان | ما فيه | المرحلة |
|---|---|---|
| `internal/services/rtdb/*/..._test.go` | اختبارات الوحدات: الإطارات، والترتيب، والبصمة (بقيم مأخوذة من كود المكتبة)، والاستعلام، والقواعد بحالات targaryen | R1 إلى R4 |
| `test/acceptance/rtdb_*_test.go` | على خادم حقيقي: المصافحة، والكتابة والاستماع، والترتيب بين جلستين، وonDisconnect، والقواعد، وقطع البث، وREST، و**العزل بين مشروعين** | R1 إلى R5 |
| `test/compat/firebase-js/` | تشغيل `@firebase/database-compat` و`exp/integration` من commit مثبَّت. أساسًا في وضع الإنتاج عبر `config/project.json` بنطاق فرعي وTLS، ووضع المحاكي للمقارنة. وملف نتائج مصنَّفة كما في Auth | R3 ثم R7 |
| `test/e2e/android-rtdb/` و`ios-rtdb/` و`web-rtdb/` | على غرار `android-auth` و`ios-auth`: المكتبة الرسمية في وضع الإنتاج، مع الحارس. ويستعمل `server.sh` المشترك | R7 |
| `test/smoke/admin-node/` و`admin-go/` | حالات RTDB: Node عبر WebSocket، وGo عبر REST، مع أشكال `databaseURL` | R5 |
| `tools/rtdb-chaos/` **جديد** | أداة القتل والاستعادة: كتابات ثم SIGKILL، ثم التحقق من كل كتابة مؤكَّدة. ببذرة تعيد الفشل نفسه | R7 |
| `tools/bench/` | سيناريوهات RTDB: اتصالات متزامنة، وتوزيع على المستمعين، وكتابات في الثانية | R7 |
| `.github/workflows/ci.yml` و`devices.yml` | تشغيل اختبارات Google في CI، وأجهزة RTDB في الجدول الأسبوعي | R7 |

### التوثيق

| الملف | ما فيه | المرحلة |
|---|---|---|
| `docs/adr/ADR-018…` | قرارات R0: `databaseURL` والشهادات، والتخزين، والكاتب الواحد والترتيب، ونطاق القواعد | R0 |
| `docs/compat/rtdb-v0.2.md` | ما يعمل وما لا يعمل، ونتائج اختبارات Google، والاختلافات المقصودة عن Firebase | R7 |
| `docs/guides/rtdb.md` | دليل المطوّر: الإعداد، و`databaseURL`، والقواعد، والتطوير المحلي | R7 |
| دليل التشغيل | سجل DNS العام، وCaddy مع `on_demand_tls` و`ask`، ومهلة الوكيل | R6 |
| `docs/security/review-v0.2.md` | المراجعة الأمنية قبل الوسم | R7 |

## قرارات صغيرة داخل الهيكل

- **لماذا `wire` منفصل عن `api`؟** ليُختبر البروتوكول بالإطارات المسجّلة دون شبكة، ولأن REST لا يحتاجه.
- **لماذا `session.go` في `core` و`conn.go` في `api`؟** ترتيب الردود قاعدة في البروتوكول نفسه، فمكانها النواة. أما المقابس والمهلات فطريقة الاتصال.
- **لماذا `rules` حزمة مستقلة؟** فيها أكبر عدد من الحالات الصعبة، وتُختبر وحدها بحالات targaryen. ويمكن أن تفيد Firestore لاحقًا في المحلّل وحده، أما الدلالة فمختلفة.
- **لماذا عقد `idtoken` في `platform`؟** archtest يمنع خدمةً من استيراد أخرى. وAuth تبقى لا تعرف RTDB، و`internal/app` وحدها تربطهما، كما في ADR-015.
- **لماذا لا `tokens/` في RTDB؟** RTDB لا يصدر رموزًا، يتحقق منها فقط.
- **لا كود منسوخ:** flakbase وfirebase-server وLark مراجع أفكار، وbolt فكرة الصياغة، وtargaryen بيانات اختبار برخصتها.
