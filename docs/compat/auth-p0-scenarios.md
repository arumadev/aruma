# سيناريوهات Auth ذات الأولوية P0 (T22)

- **التاريخ:** 2026-10-18
- **المصدر:** نطاق خطة المرحلة 2: البريد وكلمة المرور، والدخول المجهول، وcustom token، وإجراءات البريد، وGoogle وApple على الويب، والتجديد، والحساب، والإبطال، وAdmin.
- **العدد:** 27 سيناريو: 19 للويب و8 لـ Admin.

## ما يغطيها

- **e2e:** [test/e2e/web-auth](../../test/e2e/web-auth)، وتُشغَّل بـ `test/e2e/web-auth/run.sh`.
  - تطبيق Vite على حزمة `firebase` الرسمية، بإصداراتها الثلاثة 11 و12 و13.
  - لا يأخذ إلا إعداد الويب من Management API، والسطر الواحد (ADR-013).
  - حارس شبكة يُفشل أي اختبار يخرج منه طلب من الجهاز.
- **acceptance:** [test/acceptance](../../test/acceptance)، عبر HTTP على خادم حقيقي.
  - جداول الأخطاء: `TestAuthErrorTables`.
  - تطابق v3: `TestAuthV3Parity`.
  - العبث: `TestAuthTamper`.

الحالة: **ناجح** يعمل في CI. **معلّق** ينتظر فرعًا آخر عند الدمج.

## الويب

| # | السيناريو | الاختبار | الحالة |
|---|---|---|---|
| W-01 | إنشاء حساب ببريد وكلمة مرور | e2e `email and password: …`؛ `TestAuthEmailPasswordLifecycle` | ناجح |
| W-02 | الخروج | e2e `email and password: …` | ناجح |
| W-03 | الدخول ببريد وكلمة مرور | e2e `email and password: …`؛ `TestAuthEmailPasswordLifecycle` | ناجح |
| W-04 | التجديد القسري `getIdToken(true)` ومُصدِر الرمز | e2e `email and password: …` | ناجح |
| W-05 | `onAuthStateChanged` عبر الدورة كلها | e2e `email and password: …`، `revocation …` | ناجح |
| W-06 | `updateProfile`: الوضع والمسح | e2e `email and password: …`؛ `TestAuthEmailPasswordLifecycle` | ناجح |
| W-07 | `updatePassword` بعد دخول حديث (`reauthenticateWithCredential`) | e2e `email and password: …`؛ `TestAuthPasswordChangeRightAfterSignIn` | ناجح |
| W-08 | طلب دخول حديث (`requires-recent-login`) | `TestAuthRecentLoginRequired`؛ `TestAuthErrorTables` (update وdelete) | ناجح |
| W-09 | الدخول المجهول | e2e `anonymous, then linked …`؛ `TestAuthAnonymousUpgradeKeepsUID` | ناجح |
| W-10 | ربط المجهول ببريد وكلمة مرور مع بقاء uid (AC-A2) | e2e `anonymous, then linked …` | ناجح |
| W-11 | `signInWithCustomToken` برمز من `createCustomToken` الرسمي (AC-A5) | e2e `custom token from the official createCustomToken`؛ `TestAuthCustomToken` | ناجح |
| W-12 | `deleteUser` | e2e `email and password: …` | ناجح |
| W-13 | الإخراج عند الإبطال (`auth/user-token-expired`) | e2e `revocation signs the user out …`؛ `TestAuthTamper/08` | ناجح |
| W-14 | رموز الأخطاء `auth/*` في التطبيق (AC-A3) | e2e `error codes reach the app …`؛ `TestAuthErrorTables` | ناجح |
| W-15 | `fetchSignInMethodsForEmail` مع الحماية وبدونها (AC-A4) | `TestAuthErrorTables` (createAuthUri) | ناجح |
| W-16 | تأكيد البريد: `sendEmailVerification` ثم `applyActionCode` (AC-A6) | جدول sendOobCode؛ `TestAuthTamper/11` | معلّق: إجراءات البريد |
| W-17 | إعادة تعيين كلمة المرور: `sendPasswordResetEmail` ثم `confirmPasswordReset` (AC-A7) | جدولا sendOobCode وresetPassword؛ `TestAuthTamper/11`، `/12` | معلّق: إجراءات البريد |
| W-18 | Google بالنافذة والتحويل والربط وإعادة المصادقة (AC-A8) | `TestAuthTamper/14`، `/15` | معلّق: Google وApple |
| W-19 | Apple مع nonce (AC-A9) | `TestAuthTamper/16` | معلّق: Google وApple |

## Admin

| # | السيناريو | الاختبار | الحالة |
|---|---|---|---|
| A-01 | `verifyIdToken` بمكتبة Aruma في Node وGo يقبل الرمز الصحيح ويرفض المزوَّر (AC-A10) | خطّافا `nodeLibVerify` و`goLibVerify` في `TestAuthTamper/01`–`/08` | معلّق: مكتبتا التحقق |
| A-02 | إبطال ثم تجديد ثم `checkRevoked`: `TOKEN_EXPIRED` ثم `id-token-revoked` (AC-A11) | `TestAuthTamper/08` (الخادم ناجح) | معلّق: مكتبتا التحقق |
| A-03 | createUser وgetUser وgetUserByEmail وupdateUser وdeleteUser بأشكال UserRecord وأخطائها (AC-A14) | — | معلّق: Admin API |
| A-04 | listUsers بترتيب ثابت وnextPageToken | — | معلّق: Admin API |
| A-05 | custom claims تظهر بعد التجديد، ورفض المحجوز وما فوق 1000 (AC-A13) | `TestAuthErrorTables` (ادعاءات custom token)؛ `TestAuthCustomToken` | معلّق جزئيًا: Admin API |
| A-06 | revokeRefreshTokens | `TestAuthTamper/08` (طريق كلمة المرور) | معلّق: Admin API |
| A-07 | تعطيل المستخدم وإعادة تفعيله: الدخول والتجديد مرفوضان (AC-A12) | `TestAuthDisabledUser`؛ `TestAuthErrorTables` | ناجح على الخادم، وطريق Admin معلّق |
| A-08 | جلسة الكوكي: إنشاء وتحقق وإبطال و`INVALID_DURATION` (AC-A20) | — | معلّق: Admin API |

## الحصيلة

- **مغطّاة الآن:** 15 من 27. وهي W-01 إلى W-15.
- **معلّقة على فروع أخرى:** 12.
  - البريد: W-16 وW-17.
  - Google وApple: W-18 وW-19.
  - Admin ومكتبتا التحقق: A-01 إلى A-08. وA-07 ناجح على الخادم.
