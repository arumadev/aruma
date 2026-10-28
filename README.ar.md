<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/banner-dark.svg">
  <img alt="Aruma: احتفظ بمكتبات Firebase، وامتلك خادمك" src="docs/assets/banner-light.svg" width="100%">
</picture>

<br>

**خادم ذاتي الاستضافة يتحدث مع مكتبات Firebase الرسمية.**
ملف تنفيذي واحد بلغة Go وقاعدة PostgreSQL. تطبيقاتك تبقى على المكتبات الرسمية وكودك لا يتغيّر.

<br>

[![License](https://img.shields.io/badge/license-Apache%202.0-1446D8?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26+-0FD8C6?style=flat-square&logo=go&logoColor=white)](go.mod)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-1446D8?style=flat-square&logo=postgresql&logoColor=white)](#البدء-السريع)
[![Status](https://img.shields.io/badge/status-pre--1.0-0A7D73?style=flat-square)](#الحالة)

[English](README.md) · [العربية](README.ar.md)

[البدء السريع](#البدء-السريع) · [الحالة](#الحالة) · [كيف يعمل](#كيف-يعمل) · [الإعداد](docs/configuration.md) · [الوثائق](#الوثائق) · [المساهمة](CONTRIBUTING.md)

</div>

<div dir="rtl">

<br>

## لماذا Aruma

يقدّم Firebase مكتبات عميل ممتازة وخادمًا مُدارًا لا تستطيع مغادرته. Aruma تُبقي الأولى وتستبدل الثاني.

- **تبقى المكتبات الرسمية.** مكتبات الويب وiOS وAndroid وFlutter ومكتبات Admin تعمل بوضعها الإنتاجي المعتاد، دون وضع المحاكي. تبدّل ملف الإعداد، وفي بعض المنصات تضيف سطرًا واحدًا إلى إعداد البناء (انظر [`sdk/`](sdk)). كود تطبيقك نفسه لا يتغيّر.
- **بياناتك عندك.** شغّله على خادم افتراضي رخيص أو على خوادمك أو حيث تعمل PostgreSQL. ملف تنفيذي واحد وقاعدة واحدة، بلا فوترة على كل عملية.
- **مفتوح المصدر.** Apache 2.0. اقرأ الكود، شغّل الاختبارات، وأرسل تعديلك.
- **مبني ليُتحقَّق منه.** يُقارَن السلوك بـ Firebase الحقيقي وبالمحاكيات الرسمية، وتُشغَّل اختبارات Firebase الرسمية على Aruma. الأرقام في [الحالة](#الحالة).

> Aruma غير تابعة لـ Google ولا معتمدة منها. Firebase علامة تجارية لـ Google LLC.

## الحالة

Aruma قبل الإصدار 1.0 وتحت تطوير نشط. الخدمات تصل واحدة بعد أخرى، وتُغلَق كل واحدة بتقرير توافق قبل أن تبدأ التي بعدها.

| الخدمة | الحالة | الدليل |
|---|---|---|
| **المنصة**: المشاريع والتطبيقات ومفاتيح API وحسابات الخدمة واللوحة وManagement API | متاحة | اختبارات القبول، واختبارات العزل بين المشاريع في CI، وأداة `firebase` تعمل عليها |
| **المصادقة (Auth)** | صدرت في [v0.1.0](docs/compat/auth-v0.1.md) | 27 من 27 سيناريو P0، و180 اختبار Auth رسميًا ناجحًا من Firebase JS، والتحقق على Web وiOS وAndroid وNode |
| **قاعدة البيانات اللحظية (RTDB)** | صدرت في [v0.2.0](docs/compat/rtdb-v0.2.md)، وتُفعَّل بـ `ARUMA_RTDB_ENABLED` | 332 من 332 اختبارًا رسميًا لـ RTDB على `databaseURL` بنمط الإنتاج |
| **قواعد الأمان (Security Rules)** و**Storage** | صدرت في [v0.3.0](docs/releases/v0.3.0.md) ([storage](docs/compat/storage-v0.3.md)، [rules](docs/compat/rules-v0.3.md)) | 145 من 153 اختبار Storage رسميًا، وكل ما بقي فرق موثّق؛ ومحرك القواعد يوافق الإنتاج والمحاكي في كل حالة معدودة من أوراكله؛ ومسارات الإنتاج على الويب وAndroid وiOS وFlutter |
| **قاعدة المستندات (متوافقة مع Firestore)** | نسخة مرشحة [v0.4.0-rc.1](docs/releases/v0.4.0-rc.1.md)، وتُفعَّل بـ `ARUMA_DOCUMENTS_ENABLED` ([التوافق](docs/compat/documents-v0.4.md)) | إعادة تشغيل تسجيلات من الإنتاج في كل اختبار، واختبارات الويب على Chromium وFirefox وWebKit، وfirebase-tools ومكتبات Admin؛ ومجموعتا Android وiOS الرسميتان لم تُشغَّلا بعد |
| **الإشعارات وApp Check وRemote Config وFunctions** | مخطَّطة بعد الخدمات الأساسية | |

كل فرق عن Firebase موثَّق بمصدره وتصنيفه في تقارير التوافق تحت [`docs/compat`](docs/compat) و[القرارات المعمارية](docs/adr).

## كيف يعمل

1. أنشئ مشروعًا وتطبيقًا من اللوحة أو بـ `arumactl`.
2. نزّل ملف إعداد التطبيق (`google-services.json` أو `GoogleService-Info.plist` أو ما يقابلهما للويب وFlutter).
3. ضعه في تطبيقك بدل ملف Firebase. كود تطبيقك لا يتغيّر. وحيث تحتاج المنصة، أضف سطر البناء الواحد الموصوف في [`sdk/`](sdk).

العزل بين المشاريع تفرضه PostgreSQL بأمان مستوى الصف ويُختبر في CI. التصميم في [ADR-008](docs/adr/ADR-008-single-entry.md) و[ADR-015](docs/adr/ADR-015-services-layout.md) و[ADR-017](docs/adr/ADR-017-postgres-rls.md).

## اللوحة

كل خادم Aruma يأتي بلوحة تحكم مدمجة في الملف التنفيذي نفسه. أنشئ المشاريع والتطبيقات، وأدر المستخدمين، وعدّل القواعد، وتصفّح البيانات والملفات، ونزّل ملفات الإعداد التي تحتاجها تطبيقاتك.

</div>

<p align="center">
  <img src="docs/assets/console-auth.png" alt="لوحة Aruma: المصادقة وقائمة مستخدمي المشروع" width="100%">
</p>

<table>
  <tr>
    <td width="50%"><img src="docs/assets/console-rtdb.png" alt="لوحة Aruma: قواعد قاعدة البيانات اللحظية وعنوان databaseURL"></td>
    <td width="50%"><img src="docs/assets/console-storage.png" alt="لوحة Aruma: استخدام Cloud Storage"></td>
  </tr>
</table>

<div dir="rtl">

اللوحة بالإنجليزية والعربية (من اليمين إلى اليسار).

## البدء السريع

المتطلبات: Go 1.26 أو أحدث، وPostgreSQL 18 (مثلًا عبر [DBngin](https://dbngin.com) أو [Postgres.app](https://postgresapp.com))، وNode 24 لبناء اللوحة.

</div>

```bash
make dashboard   # يبني اللوحة ويضمّنها في الثنائي (مرة واحدة)
make dev         # ينشئ .env بمفتاح عشوائي عند أول تشغيل، ثم القاعدة والـ migrations، ثم الخادم
```

<div dir="rtl">

افتح <http://localhost:8080>. أول تشغيل يطبع في السجل رمز إعداد لمرة واحدة (`code`). أدخله في صفحة الإعداد لإنشاء حسابك الأول.

إن كانت أدوات Postgres خارج `PATH`، فاضبط `ARUMA_PG_BIN`، مثلًا `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin`.

### إعداد الخادم

ثلاثة إعدادات إلزامية، والباقي له قيم افتراضية.

| المتغير | المعنى |
|---|---|
| `ARUMA_DATABASE_URL` | عنوان اتصال PostgreSQL |
| `ARUMA_PUBLIC_URL` | العنوان الذي تصل منه المكتبات، مثل `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | 32 بايت عشوائية بـ base64 (`openssl rand -base64 32`). منه تُشتق كل المفاتيح |

الإعداد ملف YAML اختياري (`--config` أو `ARUMA_CONFIG`) ومتغيرات `ARUMA_*` تتقدّم عليه. المرجع الكامل في [docs/configuration.md](docs/configuration.md)، و[`aruma.example.yaml`](aruma.example.yaml) يعرض كل مفتاح.

### التشغيل والصيانة

</div>

```bash
aruma migrate up|status                 # الخادم لا ينفّذ DDL أبدًا، ويرفض الإقلاع على مخطط قديم
aruma serve
aruma accounts create --email you@example.com
aruma backup --out aruma.dump           # pg_dump بالصيغة المخصّصة
aruma restore --in aruma.dump --yes     # بعد إيقاف الخادم، وفي معاملة واحدة
aruma config check
```

<div dir="rtl">

### من سطر الأوامر

أنشئ رمز CLI من اللوحة (حسابي ← رموز CLI)، ثم:

</div>

```bash
arumactl login --server https://aruma.example.com
arumactl projects create chat-app --name "Chat App"
arumactl apps create --project chat-app --platform android --package com.example.chat
arumactl apps sdkconfig --project chat-app <APP_ID> --out .
arumactl sa create --project chat-app backend
arumactl sa keys create --project chat-app backend@chat-app.sa.aruma.example.com --out key.json
```

<div dir="rtl">

أداة `firebase` الرسمية تعمل أيضًا عبر المتغيرات `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_TOKEN`. التفاصيل في [docs/research/experiments.md](docs/research/experiments.md).

## بنية المستودع

| المسار | المحتوى |
|---|---|
| `cmd/aruma`، `cmd/arumactl` | الخادم وأداة المطوّر |
| `internal/server` | المدخل العام الواحد: التوجيه بالمسار وترويسات الأمان |
| `internal/services` | `auth` و`rtdb` و`storage` |
| `internal/admin` | خلفية اللوحة وManagement API ورموز OAuth |
| `internal/tenancy` | منطق المنصة وبوابة الملكية والاستعلامات المولَّدة (sqlc) |
| `internal/store` | الاتصال والمعاملات والـ migrations والنسخ الاحتياطي |
| `internal/platform` | اللبنات المشتركة: الأخطاء والمصادقة والتشفير وHTTP والسجلات والمقاييس وتحديد المعدل |
| `dashboard/` | اللوحة: Next.js بتصدير ثابت، عربي RTL افتراضيًا |
| `sdk/` | حزم صغيرة توجّه المكتبات الرسمية إلى Aruma: Web وiOS وAndroid وGo وNode |
| `test/` | اختبارات القبول والعزل والتوافق والمرجع والدخان |
| `docs/` | القرارات المعمارية وتقارير التوافق والمراجعات الأمنية والبحث |

قاعدة الاعتماد بين الحزم يفرضها اختبار في CI.

## الاختبارات

</div>

```bash
make test    # الوحدات والقبول والعزل (يحتاج Postgres محليًا، وكل اختبار يأخذ قاعدة جديدة)
make lint    # gofmt وvet وقواعد الاعتماد
make smoke   # المكتبات الرسمية على ملفات حقيقية: web وadmin-node وadmin-go وandroid وios وflutter
```

<div dir="rtl">

## الأمان

- لا حساب افتراضي. أول حساب يحتاج رمزًا لمرة واحدة من السجل، والتسجيل مغلق افتراضيًا.
- كلمات المرور بـ argon2id. والرموز تُخزَّن HMAC فقط، والمفتاح الخاص لحساب الخدمة لا يُخزَّن أبدًا.
- ما لا تملكه يرجع `NOT_FOUND`.

القائمة الكاملة واختباراتها في [ADR-010](docs/adr/ADR-010-secure-defaults.md) و[نموذج التهديد](docs/security/threat-model.md). للإبلاغ عن ثغرة اقرأ [SECURITY.md](SECURITY.md).

## الوثائق

- [القرارات المعمارية](docs/adr/README.md)
- [تقارير التوافق](docs/compat)
- [الأدلة](docs/guides): مصادقة الويب ومصادقة Admin وقاعدة البيانات اللحظية وتشغيلها
- [المراجعات الأمنية](docs/security)
- [قياسات الأداء](docs/performance)
- [ملاحظات البحث](docs/research)

## المساهمة

نرحّب بالـ Issues وطلبات الدمج. ابدأ من [CONTRIBUTING.md](CONTRIBUTING.md).

## الترخيص

Apache License 2.0، انظر [LICENSE](LICENSE) و[NOTICE](NOTICE). Firebase علامة تجارية لـ Google LLC، وAruma غير تابعة لـ Google ولا معتمدة منها.

</div>
