# ADR-014: استضافة نقاط Auth

- الحالة: معتمد
- التاريخ: 2026-10-18
- المرحلة: 2
- المراجع: [docs/research/phase2-web-sdk-auth.md](../research/phase2-web-sdk-auth.md)، [docs/research/phase2-identity-toolkit.md](../research/phase2-identity-toolkit.md)، ADR-008، ADR-010، ADR-013

## السياق

بعد `connectAuthEmulator(auth, url)` ترسل مكتبة الويب طلباتها إلى:
- `<url>/identitytoolkit.googleapis.com/v1/...`
- `<url>/securetoken.googleapis.com/v1/token`

وتهمل أي مسار في `url`، فلا بد أن تُخدم هذه النقاط على جذر النطاق. وiOS يرسل إلى `<host>/www.googleapis.com/identitytoolkit/v3/relyingparty/...`.

وهذه النقاط يناديها المتصفح من نطاق تطبيق المطوّر، أي من أصل آخر.

## القرار

### المسارات (تُضاف إلى جدول ADR-008)

| المسار | الغرض | الاعتماد |
|---|---|---|
| `/identitytoolkit.googleapis.com/v1/accounts:*`، `/v1/projects`، `/v2/*` | نقاط العميل | مفتاح API في `?key=` |
| `/securetoken.googleapis.com/v1/token` | التجديد | مفتاح API |
| `/www.googleapis.com/identitytoolkit/v3/relyingparty/*` | iOS (v3)، طبقة تحويل على النواة نفسها | مفتاح API |
| `/identitytoolkit.googleapis.com/v1/projects/{p}/accounts*` و`:createSessionCookie` | الإدارة | رمز حساب خدمة، أو رمز CLI لصاحب المشروع |
| `/emulator/auth/handler`، `/emulator/auth/iframe` | دخول Google وApple في وضع المحاكي | — |
| `/__/auth/handler`، `/__/auth/iframe`، `/__/auth/action`، `/__/firebase/init.json` | صفحات `authDomain` | — |
| `/issuers/{project}/...`، `/session-issuers/{project}/...` | الاكتشاف وJWKS (ADR-004) | عام |

- مسارات `/emulator/*` الأخرى، مثل حذف كل الحسابات وإعداد المحاكي، **لا تُسجَّل أبدًا**.

### CORS

- **مسارات العميل وJWKS والاكتشاف:** CORS مفتوح.
  - `Access-Control-Allow-Origin: *`، دون `Allow-Credentials`، فلا cookies.
  - الترويسات المسموحة: `content-type` و`x-client-version` و`x-firebase-client` و`x-firebase-gmpid` و`x-firebase-appcheck`.
  - الهوية فيها هي الرمز في الجسم أو مفتاح API، لا الأصل.
- **اللوحة وManagement API ونقاط الإدارة:** يبقى CORS مغلقًا كما في ADR-010 S-8. يُعدَّل نص S-8 ليحدد نطاقه بهذا.

### ما تُسقطه المكتبة في وضع المحاكي، يفرضه الخادم

- **النطاقات المصرّح بها:** تُفحص في الخادم لـ `redirectUrl` و`requestUri` و`continueUrl`.
- **رمز `state`:** مربوط بـ `sessionId`، ولمرة واحدة.
- **reCAPTCHA:** يُعلن `OFF`. والحماية من الإغراق بحدود معدل في الخادم.

### المفاتيح ومشروع الطلب

- مفتاح API يحدد المشروع، عبر `LookupAPIKey` من المرحلة 1.
- رمز الدخول أو رمز التجديد يجب أن يكون من **المشروع نفسه**، وإلا يُرفض بالرسالة نفسها لرمز غير صالح.

## البدائل المرفوضة

- **نطاق فرعي لكل خدمة** (`identitytoolkit.<domain>`): يخالف ADR-008، ولا تحتاجه المكتبة.
- **CORS مغلق لنقاط العميل:** يكسر كل تطبيق ويب على نطاق مختلف، وهو الحالة العادية.

## العواقب

- **صياغة ADR-010 S-8:** تصبح «CORS مغلق للوحة وManagement API ونقاط الإدارة».
- **أسماء نطاقات Google داخل مسارات Aruma:** هي مسارات فقط، ولا تعني أن الخادم يتظاهر بأنه Google. والوثائق توضح ذلك.
