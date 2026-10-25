---
title: البدء السريع
description: ابنِ اللوحة، شغّل خادم Aruma محليًا، واضبطه للإنتاج وأدره من سطر الأوامر.
---

المتطلبات: Go 1.26 أو أحدث، وPostgreSQL 18 (مثلًا عبر [DBngin](https://dbngin.com) أو [Postgres.app](https://postgresapp.com))، وNode 24 لبناء اللوحة.

```bash
make dashboard   # يبني اللوحة ويضمّنها في الثنائي (مرة واحدة)
make dev         # ينشئ .env بمفتاح عشوائي عند أول تشغيل، ثم القاعدة والـ migrations، ثم الخادم
```

افتح <http://localhost:8080>. أول تشغيل يطبع في السجل رمز إعداد لمرة واحدة (`code`). أدخله في صفحة الإعداد لإنشاء حسابك الأول.

إن كانت أدوات Postgres خارج `PATH`، فاضبط `ARUMA_PG_BIN`، مثلًا `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin`.

## إعداد الخادم

ثلاثة إعدادات إلزامية، والباقي له قيم افتراضية.

| المتغير | المعنى |
|---|---|
| `ARUMA_DATABASE_URL` | عنوان اتصال PostgreSQL |
| `ARUMA_PUBLIC_URL` | العنوان الذي تصل منه المكتبات، مثل `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | 32 بايت عشوائية بـ base64 (`openssl rand -base64 32`). منه تُشتق كل المفاتيح |

الإعداد ملف YAML اختياري (`--config` أو `ARUMA_CONFIG`) ومتغيرات `ARUMA_*` تتقدّم عليه. المرجع الكامل في [الإعداد](/aruma/en/docs/reference/configuration/) (بالإنجليزية)، و[`aruma.example.yaml`](https://github.com/arumadev/aruma/blob/main/aruma.example.yaml) يعرض كل مفتاح.

## التشغيل والصيانة

```bash
aruma migrate up|status                 # الخادم لا ينفّذ DDL أبدًا، ويرفض الإقلاع على مخطط قديم
aruma serve
aruma accounts create --email you@example.com
aruma backup --out aruma.dump           # pg_dump بالصيغة المخصّصة
aruma restore --in aruma.dump --yes     # بعد إيقاف الخادم، وفي معاملة واحدة
aruma config check
```

## من سطر الأوامر

أنشئ رمز CLI من اللوحة (حسابي ← رموز CLI)، ثم:

```bash
arumactl login --server https://aruma.example.com
arumactl projects create chat-app --name "Chat App"
arumactl apps create --project chat-app --platform android --package com.example.chat
arumactl apps sdkconfig --project chat-app <APP_ID> --out .
arumactl sa create --project chat-app backend
arumactl sa keys create --project chat-app backend@chat-app.sa.aruma.example.com --out key.json
```

أداة `firebase` الرسمية تعمل أيضًا عبر المتغيرات `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_TOKEN`. التفاصيل في [docs/research/experiments.md](https://github.com/arumadev/aruma/blob/main/docs/research/experiments.md).

## اربط تطبيقك

نزّل ملف إعداد التطبيق من اللوحة وضعه في تطبيقك بدل ملف Firebase. وحيث تحتاج المنصة، أضف سطر البناء الواحد الموصوف في [`sdk/`](https://github.com/arumadev/aruma/tree/main/sdk). وللقاعدة اللحظية انظر [الدليل](/aruma/docs/guides/rtdb/).
