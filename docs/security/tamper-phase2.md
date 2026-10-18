# سيناريوهات العبث: المرحلة 2 (T24)

- **التاريخ:** 2026-10-18
- **معيار الخروج:** 22 سيناريو، كلها في CI، بنسبة 100% (X-2، X-3).
- **الاختبار:** `TestAuthTamper` في [test/acceptance/auth_tamper_test.go](../../test/acceptance/auth_tamper_test.go). لكل سيناريو اختبار فرعي باسمه ورقمه. أبقِ الأسماء هنا وهناك متطابقة.
- **التشغيل:** `go test -run TestAuthTamper ./test/acceptance/`

## المتحققات الثلاثة

كل رمز دخول مزوَّر يُعرض على المتحققات الثلاثة:

1. **الخادم:** `accounts:lookup`، ومعه التجديد حين يكون في السيناريو رمز تجديد.
2. **مكتبة Node** (`@aruma/admin-auth`): خطّاف `nodeLibVerify`.
3. **مكتبة Go:** خطّاف `goLibVerify`.

الخطّافان فارغان حتى تُدمج المكتبتان، فيُتخطى اختبارهما مع ذكر السبب. يُوصَلان عند الدمج بـ `verifyIdToken(token, true)`.

## طريقة التزوير

السيناريوهات التي تختبر فحص الادعاءات (المُصدِر، الجمهور، الانتهاء) توقّع بمفتاح المشروع الحقيقي، بعد فك تشفيره من قاعدة البيانات. فلا يكون الرفض بسبب التوقيع وحده. ويثبت الاختبار الضابط `00` أن إعادة توقيع الحمولة نفسها دون تغيير تُقبل.

## الفهرس

الحالة: **ناجح** يعمل الآن في CI. **معلّق** مكتوب كاملًا على العقد الموثّق، ويُتخطى بـ `t.Skip` مع اسم الميزة، ويُفعَّل عند الدمج بحذف السطر.

| # | السيناريو | الاختبار الفرعي (`TestAuthTamper/...`) | النتيجة المطلوبة | الحالة |
|---|---|---|---|---|
| 1 | رمز بلا توقيع | `01 unsigned token` | `INVALID_ID_TOKEN` في المتحققات الثلاثة | ناجح (المكتبتان معلّقتان) |
| 2 | `alg: none` | `02 alg none` | `INVALID_ID_TOKEN`، مع التوقيع الأصلي ودونه | ناجح (المكتبتان معلّقتان) |
| 3 | خلط الخوارزميات: HS256 بالمفتاح العام، RS256 بمفتاح آخر، `kid` لمشروع آخر، `kid` لمفتاح جلسات الكوكي، بلا `kid`، `jku`/`x5u` | `03 algorithm confusion` | `INVALID_ID_TOKEN` | ناجح (المكتبتان معلّقتان) |
| 4 | تعديل الحمولة: تبديل uid، تمديد exp، تغيير aud، إضافة ادعاء | `04 modified payload` | `INVALID_ID_TOKEN` | ناجح (المكتبتان معلّقتان) |
| 5 | رمز المشروع A على المشروع B | `05 project A token on project B` | `INVALID_ID_TOKEN`، والتجديد `INVALID_REFRESH_TOKEN` | ناجح (المكتبتان معلّقتان) |
| 6 | مُصدِر Google (`https://securetoken.google.com/<id>`) | `06 Google issuer` | `INVALID_ID_TOKEN` رغم صحة التوقيع | ناجح (المكتبتان معلّقتان) |
| 7 | رمز منتهٍ، أو صادر في المستقبل | `07 expired token` | `INVALID_ID_TOKEN` | ناجح (المكتبتان معلّقتان) |
| 8 | رمز مُبطَل بعد تغيير كلمة المرور | `08 revoked token after a password change` | `TOKEN_EXPIRED` للدخول والتجديد، والجلسة الجديدة سليمة | ناجح (المكتبتان معلّقتان) |
| 9 | custom token من حساب خدمة لمشروع آخر | `09 custom token from another project's service account` | `CREDENTIAL_MISMATCH`، ولا يُنشأ مستخدم | ناجح |
| 10 | custom token موقّع بمفتاح حساب خدمة محذوف | `10 custom token signed with a deleted service account key` | `INVALID_CUSTOM_TOKEN`، حتى لرمز صُنع قبل الحذف | ناجح |
| 11 | رمز بريد مستخدَم | `11 used email code` | `INVALID_OOB_CODE`، وكل نوع رمز لا يصلح لغيره | معلّق: إجراءات البريد |
| 12 | رمز بريد منتهٍ | `12 expired email code` | `EXPIRED_OOB_CODE`، وكلمة المرور لا تتغير | معلّق: إجراءات البريد |
| 13 | continueUrl إلى نطاق غير مصرّح | `13 continueUrl to an unauthorised domain` | `UNAUTHORIZED_DOMAIN`، وصفحة الإجراء لا تربط به | معلّق: إجراءات البريد |
| 14 | إعادة استخدام state | `14 replayed state` | رفض الاستخدام الثاني | معلّق: دخول Google وApple |
| 15 | تبديل state بين جلستين، أو تعديله | `15 swapped state` | رفض | معلّق: دخول Google وApple |
| 16 | nonce خاطئ أو ناقص | `16 wrong nonce` | `MISSING_OR_INVALID_NONCE` | معلّق: دخول Google وApple |
| 17 | حقول الإدارة من العميل: `localId` لمستخدم آخر، `customAttributes`، `disabled`، `emailVerified`، `validSince`، `passwordHash` وغيرها | `17 admin-only fields from a client` | تُهمل ولا تُطبَّق أبدًا. update وlookup وdelete تعمل على صاحب الرمز فقط | ناجح (جزء `returnOobLink` معلّق) |
| 18 | `Bearer owner` على نقاط الإدارة | `18 Bearer owner on admin endpoints` | لا 2xx ولا بيانات مستخدم، و401 في Management API والكونسول | ناجح (فحص 401/403 الدقيق معلّق: Admin API) |
| 19 | مفتاح API، أو رمز دخول مستخدم، على نقاط الإدارة | `19 API key on admin endpoints` | لا 2xx ولا بيانات مستخدم | ناجح (الفحص الدقيق والضابط بحساب الخدمة معلّقان: Admin API) |
| 20 | تفريغ القاعدة والسجل: صفر سر خام | `20 database and log dump hold no raw secret` | لا كلمة مرور ولا رمز تجديد ولا رمز دخول ولا custom token ولا مفتاح خاص ولا مفتاح رئيسي ولا رمز CLI، نصًا أو hex | ناجح (رموز البريد وكلمة مرور SMTP معلّقة) |
| 21 | حدود المعدل على الدخول | `21 rate limit on sign-in` | التأخير بعد محاولات خاطئة حتى لكلمة المرور الصحيحة، وحد إنشاء الحسابات وحد الطلبات لكل عنوان | ناجح |
| 22 | حدود المعدل على إرسال البريد | `22 rate limit on email sending` | `TOO_MANY_ATTEMPTS_TRY_LATER` أو `RESET_PASSWORD_EXCEED_LIMIT` | معلّق: إجراءات البريد |

**الحصيلة:** 15 ناجحًا، و7 معلّقة على فروع أخرى (البريد 4، Google وApple 3). وفحوص المكتبتين في السيناريوهات 1 إلى 8 معلّقة على مكتبتي التحقق.

## عند الدمج

1. **إجراءات البريد:** وصّل `mailboxCode` بصندوق البريد التجريبي، واحذف `t.Skip(pendingEmail)`.
2. **Google وApple:** نفّذ `idpDriver` بمزوّد تجريبي، وعيّن `idp`، واحذف `t.Skip(pendingIdP)`.
3. **Admin API:** احذف `t.Skip(pendingAdmin)`.
4. **المكتبتان:** عيّن `nodeLibVerify` و`goLibVerify`.

## نتائج وجدها الاختبار

- **حساب معطَّل بكلمة مرور صحيحة:** كان يُجاب بـ `INVALID_LOGIN_CREDENTIALS` تحت الحماية، خلافًا لتعليق الكود وللحماية الموثقة. أُصلح في `0c5fe3d`.
- **دقة الإبطال:** كانت رموز التجديد تُقارن بالميكروثانية. هذا يُخرج مستخدم الويب بعد `reauthenticateWithCredential` ثم `updatePassword` في الثانية نفسها. أُصلح في `96e76e0`، والقرار مسجّل في [phase2-identity-toolkit.md §3.14](../research/phase2-identity-toolkit.md).
