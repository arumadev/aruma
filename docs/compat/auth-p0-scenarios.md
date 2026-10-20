# سيناريوهات Auth ذات الأولوية P0 (T22)

- **التاريخ:** 2026-10-18
- **المصدر:** نطاق خطة المرحلة 2: البريد وكلمة المرور، والدخول المجهول، وcustom token، وإجراءات البريد، وGoogle وApple على الويب، والتجديد، والحساب، والإبطال، وAdmin.
- **العدد:** 27 سيناريو: 19 للويب و8 لـ Admin.

## ما يغطيها

- **e2e:** [test/e2e/web-auth](../../test/e2e/web-auth)، وتُشغَّل بـ `test/e2e/web-auth/run.sh`.
  - تطبيق Vite على حزمة `firebase` الرسمية، بإصداراتها الثلاثة 11 و12 و13.
  - لا يأخذ إلا إعداد الويب من Management API، والسطر الواحد (ADR-013).
  - حارس شبكة يُفشل أي اختبار يخرج منه طلب من الجهاز.
  - كل إصدار رئيسي يعمل على خادم جديد خاص به.
- **e2e على الأجهزة:** [test/e2e/android-auth](../../test/e2e/android-auth) و[test/e2e/ios-auth](../../test/e2e/ios-auth).
  - Firebase الرسمي لـ Android وiOS في وضع الإنتاج، عبر مكتبتي التحويل، بحارس شبكة.
  - تغطي W-01 إلى W-04، وW-06 وW-07 وW-09 وW-12 وW-14 وW-17.
  - تعمل آليًا: Android أسبوعيًا في GitHub Actions، وiOS مرتين في الأسبوع في Codemagic. التفاصيل في [auth-v0.1.md](auth-v0.1.md).
- **acceptance:** [test/acceptance](../../test/acceptance)، عبر HTTP على خادم حقيقي.
  - جداول الأخطاء: `TestAuthErrorTables`.
  - تطابق v3: `TestAuthV3Parity`.
  - العبث: `TestAuthTamper`.
  - البريد: `auth_email_test.go`.
  - Admin API: `auth_admin_test.go`.
- **مكتبتا التحقق:** الاختبارات الحية في [sdk/go/adminauth](../../sdk/go/adminauth/live_test.go) و[sdk/node/admin-auth](../../sdk/node/admin-auth/test/live.test.js)، بجانب firebase-admin الرسمي. والهدف `tamper-libs` يعرض عليهما الرموز المزوَّرة.

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
| W-16 | تأكيد البريد: `sendEmailVerification` ثم `checkActionCode` و`applyActionCode`، وتغيير البريد بـ `verifyBeforeUpdateEmail` (AC-A6) | e2e `email verification through the dev mailbox`، `email change after verifying the new address`؛ `TestAuthEmailVerificationThroughDevMailbox`، `TestAuthChangeAndRecoverEmail`؛ جدول sendOobCode؛ `TestAuthTamper/11`، `/13` | ناجح |
| W-17 | إعادة تعيين كلمة المرور: `sendPasswordResetEmail` ثم `verifyPasswordResetCode` و`confirmPasswordReset` (AC-A7) | e2e `password reset through the dev mailbox`؛ `TestAuthPasswordReset`؛ جدولا sendOobCode وresetPassword؛ `TestAuthTamper/11`، `/12`، `/22` | ناجح |
| W-18 | Google بالنافذة والتحويل والرمز المباشر، والربط وإعادة المصادقة (AC-A8) | `TestAuthIdPGooglePopup`، `TestAuthIdPGoogleRedirect`، `TestAuthIdPDirectIDToken`، `TestAuthIdPLinkingAndTakeover`، `TestAuthIdPAccountExistsAndReauth`، `TestAuthIdPAnonymousUpgrade`؛ `TestAuthV3Parity` (verifyAssertion)؛ `TestAuthTamper/14`، `/15` | ناجح على HTTP بمزوّد مزيّف |
| W-19 | Apple مع nonce بـ form_post والرمز المباشر، وإبطال رمز Apple عند الحذف (AC-A9) | `TestAuthIdPAppleFormPostAndRevoke`، `TestAuthIdPDirectIDToken`؛ `TestAuthTamper/16` | ناجح على HTTP بمزوّد مزيّف |

## Admin

| # | السيناريو | الاختبار | الحالة |
|---|---|---|---|
| A-01 | `verifyIdToken` بمكتبة Aruma في Node وGo يقبل الرمز الصحيح ويرفض المزوَّر (AC-A10) | `TestLiveVerifyIDToken`؛ Node `verifyIdToken accepts a valid token …`؛ `tamper-libs` (`TestAuthTamper/00`–`/08`) | ناجح |
| A-02 | إبطال ثم تجديد ثم `checkRevoked`: `TOKEN_EXPIRED` ثم `id-token-revoked` (AC-A11) | `TestAuthAdminRevokeAndDisable`؛ `TestLiveRevoke`؛ Node `revoke, then refresh fails …`؛ `TestAuthTamper/08` | ناجح |
| A-03 | createUser وgetUser وgetUserByEmail وupdateUser وdeleteUser بأشكال UserRecord وأخطائها (AC-A14) | `TestAuthAdminUserManagement`؛ `TestLiveUserManagement`؛ Node `user management returns official UserRecords …` | ناجح |
| A-04 | listUsers بترتيب ثابت وnextPageToken | `TestAuthAdminUserManagement` (batchGet)؛ `TestLiveUserManagement` (`Users`)؛ Node `listUsers` | ناجح |
| A-05 | custom claims تظهر بعد التجديد، ورفض المحجوز وما فوق 1000 (AC-A13) | `TestAuthAdminCustomClaims`؛ `TestLiveCustomClaims`؛ Node `custom claims reach the token …`؛ `TestAuthErrorTables` | ناجح |
| A-06 | revokeRefreshTokens | `TestAuthAdminRevokeAndDisable`؛ `TestLiveRevoke`؛ Node `revoke, then refresh fails …` | ناجح |
| A-07 | تعطيل المستخدم وإعادة تفعيله: الدخول والتجديد مرفوضان (AC-A12) | `TestAuthAdminRevokeAndDisable`؛ `TestLiveDisable`؛ Node `a disabled user is refused …`؛ `TestAuthDisabledUser` | ناجح |
| A-08 | جلسة الكوكي: إنشاء وتحقق وإبطال و`INVALID_DURATION` (AC-A20) | `TestAuthAdminSessionCookie`؛ `TestLiveSessionCookie`؛ Node `session cookies: …` | ناجح |

## فحص المتصفح الاختياري لـ Google وApple

- **الأمر:** `go test -tags browser -run TestAuthIdPBrowser -v ./test/acceptance/`، بعد `npm ci` في `test/experiments/idp-browser`.
- **ما يفعله:** يشغّل firebase@13 الرسمي في Chromium على المزوّدَين المزيّفَين.
- **لماذا هو خارج CI:** يحتاج الشبكة إلى `apis.google.com`، وهو استثناء ADR-013 المعلن للنافذة والتحويل.
- **تشغيل 2026-10-18 على هذا الجهاز:**
  - `same-site` نجح في المرات الثلاث.
  - `cross-site` و`cross-site-partitioned` تعثّرا متقطعًا (فشل 3 من 4، و2 من 3). العطل في الخطوة الأولى دائمًا: أول نافذة Google لا تُرجع نتيجة. أما Apple والتحويل والربط وإعادة المصادقة فتنجح في كل مرة.
  - سجل الإطار يُظهر أن المكتبة تجيب حدث النافذة الأول بـ `status: ERROR`، أي لم يجد مستهلكًا، فيضيع الحدث.
  - هذا في ترحيل الصفحة والإطار (`internal/services/auth/api/idp_pages.go`)، لا في REST. وهو مسجّل للمراجعة.

## الحصيلة

- **مغطّاة:** 27 من 27.
  - الويب: W-01 إلى W-19.
  - Admin: A-01 إلى A-08.
- **حدود W-18 وW-19:**
  - تُختبر على HTTP بمزوّدَين مزيّفَين.
  - Google وApple الحقيقيان يحتاجان بيانات اعتماد، وApple يحتاج نطاق https عامًا. الخطوات اليدوية في [phase2-idp.md](../research/phase2-idp.md) §7.
  - فحص المتصفح أعلاه اختياري، وفيه تعثّر متقطع عبر المواقع.
