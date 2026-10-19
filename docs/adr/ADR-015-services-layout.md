# ADR-015: تخطيط الخدمات وحدودها

- الحالة: معتمد
- التاريخ: 2026-10-20
- المرحلة: 2، قبل إغلاق Auth وبدء Realtime Database
- المراجع: ADR-008، ADR-009، ADR-010، ADR-014

## السياق

انتهت المرحلة 2 بخدمة بيانات واحدة هي Auth، والخدمات التالية RTDB ثم Firestore وStorage. قبل أن تأتي، ظهرت مشكلتان في التخطيط:

1. **معالجات شاشات Auth في الكونسول تعيش خارج الخدمة.** أربعة ملفات (784 سطرًا: `auth.go` و`auth_users.go` و`auth_providers.go` و`auth_email.go`) في حزمة الكونسول المشتركة `internal/admin/console`. فالكونسول يعتمد على خدمة Auth، وكل خدمة قادمة ستضيف ملفاتها هناك.
2. **الأسماء مربكة.** `internal/identity` تعني خدمة Auth. وفي اللوحة `features/auth` هو دخول صاحب الحساب نفسه، و`features/identity` هو شاشات خدمة Auth.

وظهرت معهما فجوتان في الـ migrations:

- **لا شيء يمنع تعديل migration صدر.** ملف عُدّل بعد أن طُبّق على قاعدة أحد المشغّلين يترك قاعدته مختلفة عن قاعدة جديدة بالرقم نفسه، ولا يظهر الفرق إلا عند ترقية لاحقة.
- **مخطط أحدث من الثنائي يُقبل (P11).** `SchemaStatus.Pending` يفحص `Current < Latest` فقط. فثنائي أقدم على قاعدة رحّلها ثنائي أحدث: الخادم يعمل على مخطط لم يُكتب له، و`aruma migrate status` يطبع «up to date»، و`aruma migrate up` ينجح دون أن يفعل شيئًا.

هذا نقل وإعادة تسمية، **بلا تغيير في السلوك**: كل مسار وكل بايت في الرد وكل رمز حالة وسياسة اعتماد وفحص CSRF يبقى كما هو.

## القرار

### التخطيط

```
internal/
├── app/              يربط الخدمات بالكونسول، وهو المكان الوحيد الذي يعرف الطرفين
├── server/           دون تغيير
├── tenancy/          دون تغيير (المنصة، لا خدمة Firebase)
├── services/
│   └── auth/         كانت internal/identity
│       ├── api/  core/  tokens/  queries/   نُقلت كما هي
│       └── console/  من admin/console/auth*.go
│           ├── console.go    (كان auth.go)
│           ├── users.go      (كان auth_users.go)
│           ├── providers.go  (كان auth_providers.go)
│           └── email.go      (كان auth_email.go)
├── admin/
│   ├── console/      معالجات المنصة وحدها، وتركيب مسارات الخدمات
│   └── mgmt/ oauth/ render/ web/   دون تغيير
├── platform/
│   └── consolemount/ جديد: النوع Mount الذي تسجّل به الخدمة مسارات شاشاتها
├── store/
│   └── migrations/   تبقى هنا، ومعها released.lock
└── archtest/         قواعد جديدة
```

- أسماء الحزم لا تتغير: `api` و`core` و`tokens` و`queries`، والجديدة `console`.
- في اللوحة، على فرع منفصل: `features/auth` صارت `features/session` (دخول صاحب الحساب وتسجيله والإعداد الأول)، و`features/identity` صارت `features/auth` (شاشات خدمة Auth).

### نقطة الربط: `consolemount.Mount`

```go
type Mount struct {
	Handle  func(p authn.Policy, method, pattern string, h http.HandlerFunc)
	Project func(r *http.Request) (ProjectRef, error)
	Fail    func(w http.ResponseWriter, r *http.Request, err error)
}
```

- **الكونسول يحتفظ بكل ما هو مشترك:** الاعتماد، وحماية CSRF، والتوجيه، وقائمة المسارات (`Routes()`) التي يمر عليها اختبار «كل مسار غير عام يطلب اعتمادًا» (ADR-010 S-4).
  - `Handle` هي دالة التسجيل نفسها التي يستعملها الكونسول لمساراته.
  - `Project` تحل `{project}` لصاحب الطلب، كما كان `authProject`.
  - `Fail` تكتب الخطأ بصيغة `google.rpc.Status` بسجل الكونسول نفسه.
- **الخدمة تعلن مساراتها وسياستها:** `console.Routes(svc)` تُرجع دالة تركيب، وتسجّل مسارات Auth كلها بسياسة `anyCred` (الجلسة أو رمز CLI) كما كانت.
- **الترتيب:** `Options.Mounts` تُستدعى بعد مسارات الكونسول وبترتيبها، في الموضع الذي كانت تُسجَّل فيه مسارات Auth، فلم يتغير ترتيب `Routes()`.
- **التثبيت:** `TestConsoleRouteTable` في `internal/app` يقارن جدول المسارات (الطريقة، والنمط، وعام أم لا) بملف `internal/app/testdata/console_routes.txt` المأخوذ من `main` قبل النقل. التغيير المقصود يُقبل بـ `-update` ويظهر في مراجعة الفرق.

### قواعد archtest

1. **كل خدمة طبقة مستقلة.** المفتاح `internal/services/*`، والنجمة مقطع واحد من المسار. فالخدمة تستورد نفسها، و`tenancy` و`store` و`events` و`platform` و`config`، ولا تستورد خدمة أخرى. والخدمة الجديدة تدخل القاعدة دون تعديل الجدول.
2. **نواة الخدمة تحت واجهاتها.** `internal/services/*/core` لا تستورد `api` ولا `console` من أي خدمة، ولا `platform/httpx`. فالنواة نفسها تخدم REST والكونسول وأي بروتوكول لاحق.
   - `net/http` يبقى مسموحًا: `core/errors.go` يربط الأخطاء برموز الحالة، و`idp*.go` يتصل بـ Google وApple.
   - الآلية قائمة منع (`denied`) فوق جدول السماح، بالنجمة نفسها.
3. **الإدارة لا تعرف الخدمات.** `internal/admin/...` لا تستورد `internal/services/...`. `internal/app` وحدها تربطهما.
4. **`platform/consolemount`** مثل كل `platform`: لا تستورد إلا `platform`.

ولكل قاعدة حالات في `TestRulesThemselves` تثبت أنها ترفض المخالفة.

### تجميد الـ migrations: `released.lock`

- الملف `internal/store/migrations/released.lock` فيه سطر لكل migration صدر: `<schema>/<file> sha256:<hex>`، والبصمة على بايتات الملف كما هي في المستودع.
- الاختبار `TestReleasedMigrations` يفشل في ثلاث حالات:
  - ملف مسجّل تغيّر محتواه، أو حُذف.
  - ملف migration موجود وليس في القفل.
  - ورسالته تقول ما يفعله المطوّر: تغيير المخطط ملف جديد برقم جديد، ولا يُعدَّل ملف صدر.
- **إضافة ملف جديد:**
  ```
  go test ./internal/store/migrations -run TestReleasedMigrations -update
  ```
  - `-update` يضيف الأسطر الناقصة فقط، ولا يعيد كتابة سطر موجود ولا يحذفه.
  - الـ migration الذي ما زال يُكتب على فرع لم يصدر بعد: لتعديله يُحذف سطره ويُشغَّل `-update` من جديد.
- **الدمج في baseline** لاحقًا قرار مقصود: commit يعيد كتابة القفل يدويًا، ومعه ADR. الأداة لا تفعل ذلك.

### مخطط أحدث من الثنائي

- **سلوك goose v3.29.0** (قُرئ في الكود وجُرّب على قاعدة حقيقية): `Provider.Up` يسأل `HasPending` أولًا، وهذا يحسب ما يُطبَّق بـ `gooseutil.UpVersions` (`internal/gooseutil/resolve.go`). الدالة لا تنظر إلا إلى إصدارات الملفات الناقصة من القاعدة، وتتجاهل إصدارات القاعدة التي لا ملف لها. فعلى قاعدة فيها `auth` 4 والملفات حتى 3: `HasPending = false`، و`Up` يُرجع `nil, nil`.
- **القرار:** `SchemaStatus.Ahead()` (`Current > Latest`) والخطأ `ErrSchemaNewer`، ورسالته:
  `database schema is newer than this binary (auth at 4 of 3): run the matching or a newer aruma, or restore a backup`
- يُرفض في ثلاثة مواضع:
  - **`CheckCurrent`** (إقلاع الخادم): يفحص «أحدث» قبل «متأخر».
  - **`Migrate`**: يقرأ الحالة على اتصال خاص به، ويرفض قبل تشغيل goose لأي قسم.
  - **`aruma migrate status`**: يطبع `newer than this binary` بدل `up to date`.
- العودة إلى ثنائي أقدم تكون بنسخة احتياطية، كما في ADR-009 (للأمام فقط في الإنتاج).

### إضافة خدمة جديدة

1. مجلد `internal/services/<name>/` فيه `api/` و`core/` و`queries/`، و`console/` إن كانت للخدمة شاشات.
2. **المخطط:** مجلد `internal/store/migrations/<name>/`، وسطر في `migrations.Schemas` ونمط `embed`، وكتلة في `sqlc.yaml`، ثم `-update` لـ `released.lock`.
3. **المسارات العامة:** حقل في `server.Handlers` وبادئات مساراته في `internal/server` (جدول ADR-008)، والمعالج يُبنى في `internal/app`.
4. **شاشات الكونسول:** `console.Routes(svc)` تُضاف إلى `Options.Mounts` في `internal/app`، ثم `-update` لملف المسارات ومراجعة فرقه.
5. لا يحتاج archtest تعديلًا.

## ما لم يُفعل عمدًا، ومتى يُضاف

- **واجهة `Service` مشتركة، وعقد `idtoken` للتحقق من رمز Auth في خدمة أخرى:** مع RTDB، أول خدمة ثانية تحتاجهما. يُصمَّمان حينها على مستهلكين حقيقيين، لا على مستهلك واحد.
- **فصل البريد عن `core`:** فقط إن كبرت `core` حتى تصعب قراءتها.
- **`tenancy`:** تبقى مكانها. هي المنصة (الحسابات والمشاريع والمفاتيح)، لا خدمة Firebase.
- **الـ migrations:** تبقى في `store`. `embed` واحد، وقائمة أقسام مرتبة واحدة، ومرحِّل واحد. والخدمة تملك مجلد قسمها باسمها.
- **إعادة تسمية `api` و`core`، وتقسيم `dashboard/src/lib/api/console.ts`:** لا حاجة الآن.
- **RLS، وgRPC أو h2c، والمهام الخلفية:** كل منها بـ ADR خاص عند أول حاجة حقيقية. والمهام الخلفية بـ `river` كما في ADR-009.

## البدائل المرفوضة

- **مجلد فرعي لكل خدمة داخل `admin/console`:** يُبقي الإدارة معتمدة على كل خدمة، وهو ما يمنعه archtest الآن.
- **تسجيل الخدمات بـ `init()` في سجل عام:** يخفي الترتيب، فيصبح ترتيب `Routes()` معتمدًا على ترتيب الاستيراد.
- **تمرير `*console.API` إلى الخدمة:** يجعل الخدمة تستورد الإدارة، عكس الاتجاه.
- **بصمات الـ migrations في جدول في القاعدة:** goose لا يحفظها، والفحص وقت التشغيل متأخر. الاختبار يمسك التعديل في CI قبل أن يصدر.

## العواقب

- `internal/admin/console` لا يعرف خدمة Auth، ولا أي خدمة بعدها.
- أي تغيير في جدول مسارات الكونسول يظهر في فرق `console_routes.txt`.
- كل migration جديد يحتاج سطره في `released.lock`، وإلا يفشل CI.
- ثنائي أقدم من القاعدة لا يعمل ولا يرحّل. المشغّل يشغّل الإصدار المطابق أو أحدث، أو يستعيد نسخة احتياطية.
- **نهايات الأسطر:** البصمة على البايتات. فمن يسحب المستودع بتحويل CRLF (`core.autocrlf` على Windows) يفشل عنده الاختبار. المستودع بنهايات LF.
