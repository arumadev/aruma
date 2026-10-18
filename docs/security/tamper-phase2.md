# سيناريوهات العبث: المرحلة 2 (T24)

- **التاريخ:** 2026-10-18
- **معيار الخروج:** 22 سيناريو، كلها في CI، بنسبة 100% (X-2، X-3).
- **الاختبار:** `TestAuthTamper` في [test/acceptance/auth_tamper_test.go](../../test/acceptance/auth_tamper_test.go). لكل سيناريو اختبار فرعي باسمه ورقمه. أبقِ الأسماء هنا وهناك متطابقة.
- **التشغيل:**
  - الخادم وحده: `go test -run TestAuthTamper ./test/acceptance/`.
  - مع المكتبتين: `test/smoke/run.sh tamper-libs`.

## المتحققات الثلاثة

كل رمز دخول مزوَّر يُعرض على المتحققات الثلاثة:

1. **الخادم:** `accounts:lookup`، ومعه التجديد حين يكون في السيناريو رمز تجديد.
2. **مكتبة Node** (`@aruma/admin-auth`): `verifyIdToken(token, true)`.
3. **مكتبة Go** (`adminauth`): `VerifyIDTokenAndCheckRevoked`.

### كيف تُشغَّل المكتبتان

اخترنا عملية مساعدة صغيرة لكل مكتبة، داخل شجرة المكتبة نفسها:

- [sdk/node/admin-auth/test/tamper-verify.js](../../sdk/node/admin-auth/test/tamper-verify.js)
- [sdk/go/adminauth/internal/tamperverify](../../sdk/go/adminauth/internal/tamperverify/main.go)

تقرأ كل عملية سطر JSON لكل رمز، وتجيب بالقبول أو بالرفض. ويربطها [auth_verifiers_test.go](../../test/acceptance/auth_verifiers_test.go).

**لماذا لا نضع الفحوص في اختبارات المكتبتين؟** التزوير يحتاج مفتاح التوقيع الخاص بالمشروع. وهذا المفتاح لا يُستخرج إلا من قاعدة البيانات بالمفتاح الرئيسي، وهو متاح في بيئة اختبار Go وحدها. فالتزوير يبقى في Go، والتحقق يجري بكود المكتبتين كما نُشر دون نسخه.

**التشغيل:**

- تشارك المكتبتان حين يُضبط `ARUMA_VERIFY_NODE` و`ARUMA_VERIFY_GO`.
- الهدف `tamper-libs` في `test/smoke/run.sh` يبنيهما ويضبطهما، ويعمل في مهمة smoke في CI.
- `go test ./...` العادي يتخطى فحص المكتبتين مع ذكر السبب.

## طريقة التزوير

- السيناريوهات التي تختبر فحص الادعاءات (المُصدِر، الجمهور، الانتهاء) توقّع بمفتاح المشروع الحقيقي، بعد فك تشفيره من قاعدة البيانات. فلا يكون الرفض بسبب التوقيع وحده.
- الاختبار الضابط `00` يثبت أن المتحققات الثلاثة تقبل الرمز الأصلي، وتقبل إعادة توقيع حمولته دون تغيير.

## الفهرس

الحالة: **ناجح** يعمل في CI. **معلّق** مكتوب كاملًا على العقد الموثّق، ويُتخطى بـ `t.Skip` مع اسم الميزة.

| # | السيناريو | الاختبار الفرعي (`TestAuthTamper/...`) | النتيجة المطلوبة | الحالة |
|---|---|---|---|---|
| 1 | رمز بلا توقيع | `01 unsigned token` | يرفضه المتحققات الثلاثة؛ الخادم بـ `INVALID_ID_TOKEN` | ناجح |
| 2 | `alg: none` | `02 alg none` | رفض، مع التوقيع الأصلي ودونه | ناجح |
| 3 | خلط الخوارزميات | `03 algorithm confusion` | رفض في كل الحالات. الحالات: HS256 بالمفتاح العام، RS256 بمفتاح آخر، `kid` لمشروع آخر، `kid` لمفتاح جلسات الكوكي، بلا `kid`، `jku`/`x5u` | ناجح |
| 4 | تعديل الحمولة: تبديل uid، تمديد exp، تغيير aud، إضافة ادعاء | `04 modified payload` | رفض | ناجح |
| 5 | رمز المشروع A على المشروع B | `05 project A token on project B` | رفض، والتجديد `INVALID_REFRESH_TOKEN` | ناجح |
| 6 | مُصدِر Google (`https://securetoken.google.com/<id>`) | `06 Google issuer` | رفض رغم صحة التوقيع | ناجح |
| 7 | رمز منتهٍ، أو صادر في المستقبل | `07 expired token` | رفض في المتحققات الثلاثة. الخادم يرفض حتى بعد ثانية واحدة، والمكتبتان بعد هامش 300 ثانية الذي يسمح به firebase-admin-go | ناجح |
| 8 | رمز مُبطَل بعد تغيير كلمة المرور | `08 revoked token after a password change` | `TOKEN_EXPIRED` للدخول والتجديد، و`id-token-revoked` في المكتبتين، والجلسة الجديدة سليمة | ناجح |
| 9 | custom token من حساب خدمة لمشروع آخر | `09 custom token from another project's service account` | `CREDENTIAL_MISMATCH`، ولا يُنشأ مستخدم | ناجح |
| 10 | custom token موقّع بمفتاح حساب خدمة محذوف | `10 custom token signed with a deleted service account key` | `INVALID_CUSTOM_TOKEN`، حتى لرمز صُنع قبل الحذف | ناجح |
| 11 | رمز بريد مستخدَم | `11 used email code` | `INVALID_OOB_CODE`، والصفحة لا تعرض نموذجًا، وكل نوع رمز لا يصلح لغيره | ناجح |
| 12 | رمز بريد منتهٍ | `12 expired email code` | `EXPIRED_OOB_CODE`، والصفحة لا تعرض نموذجًا، وكلمة المرور لا تتغير | ناجح |
| 13 | continueUrl إلى نطاق غير مصرّح | `13 continueUrl to an unauthorised domain` | `UNAUTHORIZED_DOMAIN` أو `INVALID_CONTINUE_URI` دون إرسال بريد. وصفحة الإجراء، ولو عُدّل الرابط يدويًا، لا تقود إلا إلى العنوان المحفوظ مع الرمز | ناجح |
| 14 | إعادة استخدام state | `14 replayed state` | `INVALID_IDP_RESPONSE` للاستخدام الثاني، عبر النافذة المنبثقة وعبر createAuthUri. وصفحة المعالج لا تعرف الـ state بعد استخدامه | ناجح |
| 15 | تبديل state بين جلستين، أو تعديله، أو sessionId ناقص | `15 swapped state` | `INVALID_IDP_RESPONSE`. والمحاولة تحرق الجلسة التي سمّتها، فلا يُعاد التخمين | ناجح |
| 16 | nonce خاطئ أو ناقص أو مُجزّأ بدل الخام | `16 wrong nonce` | `MISSING_OR_INVALID_NONCE`، في تدفق الرمز (code) وفي رموز Apple وGoogle المباشرة | ناجح |
| 17 | حقول الإدارة من العميل | `17 admin-only fields from a client` | تُهمل ولا تُطبَّق أبدًا. update وlookup وdelete تعمل على صاحب الرمز فقط، و`returnOobLink` يُرفض بـ 403 `INSUFFICIENT_PERMISSION`. الحقول: `localId` لمستخدم آخر، `customAttributes`، `disabled`، `emailVerified`، `validSince`، `passwordHash` وغيرها | ناجح |
| 18 | `Bearer owner` على نقاط الإدارة | `18 Bearer owner on admin endpoints` | 401 أو 403 على كل مسار في Admin API، دون CORS ولا بيانات. و401 في Management API والكونسول | ناجح |
| 19 | مفتاح API، أو رمز دخول مستخدم، على نقاط الإدارة | `19 API key on admin endpoints` | 401 أو 403. وكضابط: رمز حساب الخدمة يقرأ المستخدم | ناجح |
| 20 | تفريغ القاعدة والسجل: صفر سر خام | `20 database and log dump hold no raw secret` | لا يظهر أي سر نصًا ولا hex. الأسرار: كلمات المرور، رموز التجديد والدخول، custom token، المفاتيح الخاصة، المفتاح الرئيسي، رمز CLI، رموز البريد، كلمة مرور SMTP، سر عميل Google، ورموز Apple (التجديد والوصول والدخول) | ناجح |
| 21 | حدود المعدل على الدخول | `21 rate limit on sign-in` | التأخير بعد محاولات خاطئة يشمل كلمة المرور الصحيحة. وحد لإنشاء الحسابات، وحد للطلبات لكل عنوان | ناجح |
| 22 | حدود المعدل على إرسال البريد | `22 rate limit on email sending` | بعد 5 رسائل للمستلم نفسه: `RESET_PASSWORD_EXCEED_LIMIT`. وبعد كثرة الرسائل من العنوان نفسه: `TOO_MANY_ATTEMPTS_TRY_LATER` | ناجح |

## الحصيلة

- **22 من 22 ناجحة.**
- السيناريوهات 14 إلى 16 تعمل على مزوّدَي Google وApple المزيّفَين في [auth_idp_test.go](../../test/acceptance/auth_idp_test.go). يصل إليهما الخادم عبر خيار الاختبار `app.WithIdPEndpoints` وحده. و`idpDriver` غلاف رقيق فوق `idpEnv` هناك.
- في السيناريوهات 1 إلى 8 تعمل فحوص المكتبتين في `tamper-libs`.
- **ما لا يُختبر محليًا:** Google وApple الحقيقيان. يحتاجان بيانات اعتماد حقيقية، وApple يحتاج نطاق https عامًا. الخطوات اليدوية في [phase2-idp.md](../research/phase2-idp.md) §7.

## نتائج وجدها الاختبار

- **حساب معطَّل بكلمة مرور صحيحة:**
  - كان يُجاب بـ `INVALID_LOGIN_CREDENTIALS` تحت الحماية، خلافًا لتعليق الكود وللحماية الموثقة.
  - أُصلح في `0c5fe3d`.
- **دقة الإبطال:**
  - كانت رموز التجديد تُقارن بالميكروثانية.
  - هذا يُخرج مستخدم الويب بعد `reauthenticateWithCredential` ثم `updatePassword` في الثانية نفسها.
  - أُصلح في `96e76e0`، والقرار مسجّل في ADR-004.
- **هامش الانتهاء في مكتبة Go:**
  - تقبل رمزًا انتهى قبل أقل من 300 ثانية، كما يفعل firebase-admin-go الرسمي. وهذا مطابقة مقصودة، لا خلل.
  - الخادم لا يسمح بأي هامش.
