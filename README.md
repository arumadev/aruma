# Aruma

خادم ذاتي الاستضافة متوافق مع مكتبات Firebase الرسمية. تشغّله على خادمك، وتطبيقاتك على Web وiOS وAndroid وFlutter تعمل عليه بتغيير ملف الإعداد وحده.

> A self-hosted server compatible with the official Firebase SDKs: one Go binary and PostgreSQL. Apps keep their official SDKs and only swap their configuration file. Not affiliated with or endorsed by Google.

## الحالة

**المرحلة 1 من 7: المنصة.** هي الحاوية التي تُبنى فوقها الخدمات:
- حسابات أصحاب المشاريع ولوحة التحكم.
- المشاريع والتطبيقات لكل منصة.
- مفاتيح API وحسابات الخدمة.
- ملفات الإعداد التي تقبلها المكتبات الرسمية.
- Management API التي تحتاجها أداة `firebase`.

خدمات البيانات (Auth وقاعدة البيانات اللحظية وFirestore وStorage وFunctions) تأتي في المراحل القادمة، وأولها Auth في المرحلة 2.

| ما يعمل اليوم | كيف ثبت |
|---|---|
| ملفات إعداد Web وAndroid وiOS وFlutter وAdmin | المكتبات الرسمية تقبلها دون أي اتصال شبكة ([اختبارات الدخان](test/smoke)) |
| Management API (M-01 إلى M-18) | اختبارات القبول، وأداة `firebase` |
| حسابات الخدمة ورموزها | تبديل الرمز عبر `golang.org/x/oauth2`، المكتبة التي يستخدمها Admin SDK للغة Go |
| العزل بين المشاريع | ست حالات عزل في CI |

## التشغيل المحلي (بلا Docker)

المتطلبات:
- Go 1.26 أو أحدث.
- PostgreSQL 18، مثلًا عبر [DBngin](https://dbngin.com) أو Postgres.app.
- Node 24، لبناء اللوحة فقط.

```sh
make dashboard   # يبني اللوحة ويضمّنها في الثنائي (مرة واحدة)
make dev         # ينشئ .env بمفتاح عشوائي عند أول تشغيل، ثم القاعدة والـ migrations، ثم الخادم
```

- افتح http://localhost:8080.
- أول تشغيل يطبع في السجل رمز إعداد لمرة واحدة (`"code"`). أدخله في صفحة الإعداد لإنشاء حسابك الأول.
- إن كانت أدوات Postgres خارج `PATH`، فاضبط `ARUMA_PG_BIN`، مثلًا `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin`.

## الإعداد

ملف YAML اختياري (`--config` أو `ARUMA_CONFIG`)، ومتغيرات `ARUMA_*` تتقدّم عليه.

| المتغير | إلزامي | الافتراضي | المعنى |
|---|---|---|---|
| `ARUMA_DATABASE_URL` | نعم | | عنوان Postgres |
| `ARUMA_PUBLIC_URL` | نعم | | العنوان الذي تصل منه المكتبات، مثل `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | نعم | | 32 بايت عشوائية بـ base64 (`openssl rand -base64 32`). منه تُشتق كل المفاتيح |
| `ARUMA_LISTEN` | | `:8080` | المدخل الواحد العام |
| `ARUMA_METRICS_LISTEN` | | `127.0.0.1:9464` | عنوان خاص لـ `/metrics`. القيمة `-` تعطّله |
| `ARUMA_SIGNUP_OPEN` | | `false` | السماح بإنشاء حسابات من اللوحة |
| `ARUMA_PROJECT_DOMAIN` | | مضيف `public_url` | أساس `authDomain` وأسماء الحاويات وبريد حسابات الخدمة |
| `ARUMA_TRUSTED_PROXIES` | | | عناوين CIDR التي تُصدَّق منها `X-Forwarded-For` |
| `ARUMA_SESSION_IDLE_TIMEOUT` / `ARUMA_SESSION_ABSOLUTE_TIMEOUT` | | `30m` / `8h` | عمر جلسة اللوحة |
| `ARUMA_MAIL_DEV_MAILBOX` | | `true` إن كان `public_url` على loopback، وإلا `false` | صندوق بريد التطوير: رسائل Auth للمشاريع التي بلا SMTP تُحفظ في اللوحة بدل إرسالها. خادم SMTP يُضبط لكل مشروع من اللوحة |
| `ARUMA_RTDB_ENABLED` | | `false` | تشغيل قاعدة البيانات اللحظية (Realtime Database) |
| `ARUMA_RTDB_DOMAIN` | | | النطاق الأب لمضيفات القواعد: `https://<project_id>-default-rtdb.<domain>`. يحتاج سجل DNS عامًا وشهادة ووكيلًا يحفظ المضيف، انظر [دليل التشغيل](docs/guides/rtdb-operator.md). بلا قيمة تُخدم القواعد بـ `?ns=` على `public_url`، للتطوير المحلي فقط |
| `ARUMA_MAIL_SMTP_ALLOW_PRIVATE` | | `true` إن كان `public_url` على loopback، وإلا `false` | السماح بخادم SMTP على عنوان loopback أو خاص أو link-local (مثل Postfix على الجهاز نفسه). مغلق، لا تصل إعدادات SMTP في اللوحة إلى شبكة الخادم الداخلية ولا إلى عنوان metadata السحابي. فعّله إن كان مرحّل البريد محليًا |

## أوامر المشغّل: `aruma`

```sh
aruma migrate up|status                    # الخادم لا ينفّذ DDL أبدًا، ويرفض الإقلاع على مخطط قديم
aruma serve
aruma accounts create --email you@example.com   # كلمة المرور من الطرفية دون عرض، أو من stdin
aruma accounts disable|enable|reset-password --email you@example.com
aruma backup --out aruma.dump              # pg_dump بالصيغة المخصّصة
aruma restore --in aruma.dump --yes        # بعد إيقاف الخادم، وفي معاملة واحدة
aruma config check
```

## أداة المطوّر: `arumactl`

أنشئ رمز CLI من اللوحة (حسابي ← رموز CLI)، ثم:

```sh
arumactl login --server https://aruma.example.com
arumactl projects create chat-app --name "Chat App"
arumactl apps create --project chat-app --platform android --package com.example.chat
arumactl apps sdkconfig --project chat-app <APP_ID> --out .
arumactl sa create --project chat-app backend
arumactl sa keys create --project chat-app backend@chat-app.sa.aruma.example.com --out key.json
```

أداة `firebase` الرسمية تعمل أيضًا عبر المتغيرات `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_TOKEN`. التفاصيل في [docs/research/experiments.md](docs/research/experiments.md).

## البنية

```
cmd/aruma, cmd/arumactl     الثنائيان
internal/server             المدخل الواحد: التوجيه بالمسار، وترويسات الأمان، ولا CORS
internal/admin              console (واجهة اللوحة)، وmgmt (Management API)، وoauth (الرموز)، وweb (اللوحة المضمّنة)
internal/tenancy            core (منطق المنصة وبوابة الملكية)، وqueries (sqlc)
internal/configgen          مولّدات ملفات الإعداد (نقية)
internal/platform           apierr، وauthn، وcrypto، وhttpx، وids، وlog، وmetrics، وratelimit
internal/store              الاتصال، والمعاملات، والـ migrations (قسم لكل خدمة)، والنسخ الاحتياطي
dashboard/                  Next.js بتصدير ثابت، عربي RTL افتراضيًا
test/acceptance, test/smoke حالات القبول والعزل، واختبارات الدخان على المكتبات الرسمية
```

القرارات المعمارية مكتوبة في [docs/adr](docs/adr):
- [ADR-007](docs/adr/ADR-007-projects-and-keys.md): المعرّفات.
- [ADR-008](docs/adr/ADR-008-single-entry.md): المدخل الواحد.
- [ADR-009](docs/adr/ADR-009-data-tooling.md): أدوات البيانات.
- [ADR-010](docs/adr/ADR-010-secure-defaults.md): الافتراضيات الآمنة.

وقاعدة الاعتماد بين الحزم يفرضها اختبار في CI.

## الاختبارات

```sh
make test        # الوحدات والقبول والعزل (يحتاج Postgres محليًا، وكل اختبار يأخذ قاعدة جديدة)
make lint        # gofmt وvet وقواعد الاعتماد
make smoke       # المكتبات الرسمية على ملفات حقيقية: web وadmin-node وadmin-go وandroid وios وflutter
```

## الأمان

- لا حساب افتراضي. أول حساب يحتاج رمزًا لمرة واحدة من السجل، والتسجيل مغلق افتراضيًا.
- كلمات المرور بـ argon2id. والرموز تُخزَّن HMAC فقط، والمفتاح الخاص لحساب الخدمة لا يُخزَّن أبدًا.
- ما لا تملكه يرجع `NOT_FOUND`.
- القائمة الكاملة واختباراتها في [ADR-010](docs/adr/ADR-010-secure-defaults.md) و[نموذج التهديد](docs/security/threat-model.md).

## الترخيص

[Apache License 2.0](LICENSE). Firebase علامة تجارية لـ Google LLC، وAruma غير تابع لـ Google ولا معتمد منها.
