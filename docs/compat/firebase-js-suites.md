# اختبارات Firebase الرسمية لـ Auth على Aruma (T23)

- **التاريخ:** 2026-10-20
- **ما هو:** اختبارات التكامل التي تنشرها Firebase مع مكتبتيها مفتوحتي المصدر، وهي مكتوبة لمحاكي Auth، تعمل على خادم Aruma مؤقت.
- **التشغيل:** [test/compat/firebase-js](../../test/compat/firebase-js/README.md)، بالأمر `test/compat/firebase-js/run.sh`. ليس في CI، والسبب في README.
- **الخادم:** Aruma من `a208be6`، على قاعدة مؤقتة `aruma_t23_*` ومنافذ 8200 إلى 8202. حماية تعداد البريد **مطفأة**، وهو إعداد المحاكي الذي تفترضه الاختبارات. وتشغيلها مفعّلةً في قسم مستقل أدناه.
- **الشبكة:** لم يخرج من الجهاز أي اتصال أثناء الاختبارات. حارس Node سجّل صفرًا، وChrome لا يحلّ إلا loopback.

## الإصدارات

| المكوّن | الإصدار |
|---|---|
| firebase-js-sdk | الوسم `firebase@13.0.0` (`@firebase/auth` 1.13.7) |
| firebase-admin-node | الوسم `v14.5.0` |
| Node | 24.21.0 |
| Go | 1.27.2 |
| Chrome | 155.0.8059.85، وchromedriver المطابق 155.0.8059.84 |

## الأرقام

| المجموعة | ما تشغّله | ناجح | فاشل | متخطّى |
|---|---|---|---|---|
| `js-node` | `test/integration/flows` في Node | 69 | 20 | 1 |
| `js-browser` | `test/integration/flows` في Chrome بلا واجهة (karma) | 69 | 36 | 20 |
| `js-webdriver` | `test/integration/webdriver` في Chrome عبر Selenium | 17 | 10 | 0 |
| `admin` | `test/integration/auth.spec.ts` في وضع المحاكي | 25 | 32 | 62 |
| **المجموع** | | **180** | **98** | **83** |

- **عدّ mocha:** فشل خطّاف `before each` أو `after each` يُعدّ فشلًا واحدًا، ولا تعمل الاختبارات التي يحرسها. ذُكر عدد هذه الاختبارات عند كل خطّاف.
- **قبل الإصلاحات:** على `d967198`، أي قبل الإصلاحين أدناه وقبل توقيع custom token في المحوّل، كانت حصيلة `js-node` 45 ناجحًا و44 فاشلًا.

## التصنيف

- **(أ)** خطأ في Aruma.
- **(ب)** ميزة غير مدعومة عمدًا. مرجعها `docs/guides/web-auth.md` و`docs/guides/admin-auth.md` («ما لا يعمل بعد» و«Not yet»)، وADR-004، و`docs/security/threat-model.md` («خارج النطاق»).
- **(ج)** سلوك يخص المحاكي وحده، وليس في الخدمة الحقيقية.
- **(د)** مسألة في بيئة الاختبار.

كل تصنيف تحقّق منه بقراءة الاختبار. ونُقل مع كل بند سبب فشله الفعلي في السجل.

## js-node وjs-browser

| الاختبارات | العدد (node / browser) | الصنف | السبب |
|---|---|---|---|
| `headless IdP`: 7 اختبارات و12 من `middleware` | 19 / 19 | ج، وب | الاختبار يمرّر لـ `GoogleAuthProvider.credential` و`FacebookAuthProvider` و`GithubAuthProvider` رمز هوية هو JSON غير موقّع (`{"sub": …, "email": …}`). المحاكي يقبله، وGoogle الحقيقي لا يقبل إلا رمزًا موقّعًا منه. يجيب Aruma `auth/operation-not-allowed` لأن google.com غير مفعّل في المشروع، و`auth/invalid-provider-id` لـ Facebook وGitHub، وهما غير مدعومين (ب). أما Google وApple الحقيقيان فتغطيهما W-18 وW-19 في [auth-p0-scenarios.md](auth-p0-scenarios.md) |
| `oob codes` › `flows beginning with sendSignInLinkToEmail`: خطّاف `before each` | 1 / 1 | ب | الدخول برابط البريد مؤجّل إلى v1.x، ويجيب `auth/operation-not-allowed`. لم تعمل الاختبارات الـ11 التي يحرسها |
| `phone auth`: 3 اختبارات، وخطّاف `with already-created user` (لم تعمل اختباراته الخمسة)، و12 من `middleware` | — / 16 | ب | الدخول بالهاتف مؤجّل، ويجيب `auth/operation-not-allowed`. مجموعة Node لا تشغّل ملف الهاتف |

### المتخطّى

تخطّاها الاختبار نفسه، لا Aruma.

| الاختبارات | العدد (node / browser) | الصنف | سبب التخطي |
|---|---|---|---|
| `oob codes` › `verifyBeforeUpdateEmail waits until flow completes` | 1 / 1 | د | معطّلة في المصدر (`xit`) لأن المحاكي لا يدعم تغيير البريد بعد التأكيد. أما Aruma فيدعمه، وتغطيه W-16 |
| `hosting link validation` | — / 5 | ب | تُتخطّى مع المحاكي. وهي رابط دخول بالبريد، بعضها في tenant، وكلاهما غير مدعوم |
| `password validation` › `validatePassword` | — / 5 | د | تُتخطّى مع المحاكي، لأنها تحتاج سياسة كلمة مرور مضبوطة في مشروع حقيقي. Aruma يخدم `passwordPolicy`، لكن هذه المجموعة لا تختبره |
| `phone auth with reCAPTCHA Enterprise ENFORCE mode` | — / 5 | ب | تُتخطّى مع المحاكي. وهي دخول بالهاتف |
| `Mfa enrollment using totp` و`sign-in for mfa-enrolled users` | — / 4 | ب | `describe.skip` في المصدر. وهي MFA |

## js-webdriver

ملفات `webdriver` الأربعة تعمل كلٌّ في عملية mocha مستقلة. في المصدر تتشارك جلسة متصفح واحدة، فلو فشل خطّاف في ملف النوافذ لتوقفت كل الملفات بعده.

| الملف | upstream | ناجح | فاشل | لم يعمل | الصنف والسبب |
|---|---|---|---|---|---|
| `anonymous` | 3 | 3 | 0 | 0 | — |
| `persistence` | 17 | 14 | 3 | 0 | **د.** اختبارات `persistence compatibility with legacy SDK` الثلاثة تحمّل SDK الإصدار 8 من `www.gstatic.com`، وChrome في هذا التشغيل لا يصل إلى غير loopback. فتفشل بـ `Cannot read properties of null (reading 'split')` قبل أي طلب إلى Aruma |
| `popup` | 13 | 0 | 4، وخطّاف `before each` | 9 | **ج.** كل اختبار ينتظر صفحة المحاكي `Auth Emulator IDP Login Widget` (`util/idp_page.ts`: زر «Add new account» وحقول البريد والاسم)، وهي مزوّد هوية وهمي في المحاكي. أما `/emulator/auth/handler` في Aruma فيحوّل إلى Google أو Apple الحقيقي. فتنتهي المهلة (20 ثانية) |
| `redirect` | 13 | 0 | خطّاف `after each` وخطّاف `after all` | 13 | **ج.** السبب نفسه في التحويل. انتهت مهلة الخطّاف بعد الاختبار الأول، فلم تعمل البقية |

## admin

### الفاشل (32)

| الاختبارات | العدد | الصنف | السبب |
|---|---|---|---|
| `createUser()`: دون UID، وبـ UID، و`fails when the UID is already in use` | 3 | ب | بيانات المستخدم فيها `phoneNumber`، وأرقام الهاتف غير مدعومة بعد («Phone numbers are not supported yet») |
| `getUser()`، `getUserByEmail()`، `deleteUser()`، `updateUser()`، `revokeRefreshTokens()`، `setCustomUserClaims()` | 6 | ب (متتالية) | تعتمد على المستخدم الذي لم يُنشأ بسبب الهاتف. في التشغيل التشخيصي أدناه نجح الأربعة الأولى. وفشل الاثنان الأخيران لاحقًا بسبب `iss` (السطر التالي) |
| `createCustomToken()` ×2، `createSessionCookie()` ×3، `verifySessionCookie() … user disabled` | 6 | ب | الدخول نفسه ينجح. لكن `verifyIdToken` و`verifySessionCookie` في firebase-admin الرسمي يشترطان `iss = https://securetoken.google.com/<project>`. ورموز Aruma يصدرها `<public_url>/issuers/<project>` عمدًا (ADR-004 §1)، والتحقق منها بمكتبة `@aruma/admin-auth` بتبديل سطر الاستيراد. ويظهر الرفض في `creates a revocable session cookie` بالرمز `auth/argument-error` |
| `getUserByPhoneNumber()`، و`getUserByProviderUid() redirects to getUserByPhoneNumber` ×4 | 5 | ب | البحث بالهاتف |
| `getUserByProviderUid() redirects to getUserByEmail` ×4 | 4 | ب (متتالية) | تبحث عن مستخدم الاختبار الأول الذي لم يُنشأ. نجحت في التشغيل التشخيصي |
| `getUserByProviderUid() returns a user record with the matching provider id` ×4، و`importUsers() … multiple OAuth providers` | 5 | ب | تُنشئ المستخدم بـ `importUsers` (`accounts:batchCreate`). وهذه الدالة ليست في قائمة الدوال المدعومة في `docs/guides/admin-auth.md`، فلا يخدمها Aruma. والمكتبة تعرض الرد المجهول «An internal error has occurred» (انظر المقترح 1) |
| `getUsers()`: خطّاف `before all` | 1 | ب | ينشئ مستخدمين بأرقام هاتف، ثم واحدًا بـ `importUsers`. لم تعمل الاختبارات الخمسة التي يحرسها |
| `listUsers() returns up to the specified number of users` | 1 | ب | بعد الإصلاح الثاني أدناه يمرّ إنشاء المستخدمين. ثم يطلب الاختبار `passwordHash` و`passwordSalt`، وAruma لا يُخرج تجزئة كلمة المرور أبدًا. يرسل العنصر النائب `REDACTED`، وهو ما ترسله Firebase لمن لا يملك الصلاحية (`internal/services/auth/api/users.go`). وتوثّق firebase-admin أن الملح لا يأتي إلا مع SCRYPT، وAruma يجزّئ بغيره. في هذا التشغيل توقف الاختبار قبل ذلك على `pageToken` (المقترح 2) |
| `Link operations` › `generateSignInWithEmailLink()` | 1 | ب | رابط الدخول بالبريد مؤجّل |

### المتخطّى (62)

كلها تخطّاها الاختبار في وضع المحاكي («Not yet supported in Auth Emulator» أو «Not implemented»).

| الاختبارات | العدد | الصنف | ملاحظة |
|---|---|---|---|
| `Tenant management operations` | 26 | ب | tenants غير متاحة |
| `SAML configuration operations` و`OIDC configuration operations` | 13 | ب | المزوّدان الوحيدان Google وApple |
| `importUsers()` بخوارزميات التجزئة السبع، وبعوامل ثانية، وبأرقام هاتف خاطئة | 9 | ب | `importUsers` ليست في الدوال المدعومة. وفيها MFA والهاتف أيضًا |
| `Link operations`: custom domain (`linkDomain`)، وFirebase Dynamic Links | 6 | ب | نطاقات Hosting وDynamic Links ليست في Aruma، وبعضها رابط دخول بالبريد |
| `Link operations` › `generateVerifyAndChangeEmailLink()` | 2 | د | يخدمه Aruma (`VERIFY_AND_CHANGE_EMAIL` في `accounts:sendOobCode`)، والمجموعة لا تختبره. وثانيهما بنطاق مخصص (ب) |
| `Project config management operations` | 2 | ب | ليست في الدوال المدعومة |
| `createUser()` و`updateUser()` بعوامل ثانية | 2 | ب | MFA |
| `updateUser()`: ربط مزوّد اتحادي وفكّه | 2 | ب | ربط المزوّدين من Admin مؤجّل (`docs/research/phase2-admin-auth.md`، «Deferred») |

### التشغيل التشخيصي (خارج الأرقام)

- **ما هو:** نسخة مؤقتة من `auth.spec.ts`، حُذفت منها أرقام الهاتف من البيانات المشتركة وحدها. أُعيد الملف الأصلي بعدها.
- **الغرض:** رؤية ما تفعله الاختبارات التي يوقفها الهاتف بعد ذلك، بحثًا عن أخطاء مخفية.
- **النتيجة:** 36 ناجحًا و21 فاشلًا و62 متخطًّى.
  - نجحت `createUser` و`getUser` و`getUserByEmail` و`getUserByProviderUid → getUserByEmail` و`updateUser` و`deleteUser`.
  - الباقي كله أسباب مصنّفة أعلاه: `iss` (ب)، والهاتف (ب)، و`importUsers` (ب)، والتجزئة (ب)، ورابط الدخول (ب).
  - لم يظهر خطأ (أ) جديد.

## مع حماية تعداد البريد مفعّلة

- **ما شُغّل:** `ENUMERATION_PROTECTION=on`، وهو الافتراضي في Aruma وفي مشاريع Firebase الجديدة. على الخادم نفسه، لـ `js-node` و`js-browser` و`admin`.
- **النتيجة:** 64/25/1 و64/41/20 و25/32/62.
- **الفرق:** خمسة اختبارات في كل بيئة فقط، وكلها (د): إعداد لا تفترضه الاختبارات. وكل رد منها هو ما توثّقه Firebase لمشروع فيه الحماية:
  - **`allows the user to delete the account`:** الدخول بعد الحذف يعطي `auth/invalid-credential`، لا `auth/user-not-found`.
  - **`account can be upgraded by setting email and password`** و**`account can have email / password attached`:** `updateEmail` ممنوع مع الحماية، ويجيب «Please verify the new email before changing email».
  - **`can be used to verify email`** و**`can be used to initiate password reset`:** `fetchSignInMethodsForEmail` يعيد قائمة فارغة.
- **admin:** الأعداد نفسها (25/32/62)، لأن Admin API لا تمسّه الحماية.

## ما يفعله المحوّل لتعمل الاختبارات

هذه بدائل بيئة الاختبار، وكلها في [adapter/](../../test/compat/firebase-js/adapter). Aruma نفسه لا يخدم شيئًا منها (ADR-014).

- **مفتاح API:** مفتاح الاختبارات الثابت (`emulator-api-key`، `fake-key`) يُستبدل بمفتاح الويب الحقيقي للمشروع.
- **`Bearer owner`:** يُستبدل برمز CLI لصاحب المشروع.
- **custom token غير موقّع:** المحاكي وحده يقبل JSON أو JWT بـ `alg: none` (ج). يوقّعه المحوّل بمفتاح حساب خدمة للمشروع، كما يوقّعه `createCustomToken`، بالـ uid وclaims نفسها. فتختبر اختبارات custom token والجلسات Aruma فعلًا. وقبل هذا البديل كانت 23 من اختبارات `custom auth` تفشل في كل بيئة.
- **عنوان العميل:** يتغير مع كل `reset`، فتطبَّق حدود المعدل لكل اختبار لا للمجموعة كلها.
- **`/emulator/v1/...`:** `DELETE accounts` و`oobCodes` و`verificationCodes` و`config` تُخدم من Console API وAdmin API وصندوق البريد التجريبي.

## الإصلاحات

| الالتزام | الخطأ | الاختبار |
|---|---|---|
| `d967198` (من WIP `bb146fc`) | `RecaptchaVerifier` يطلب `recaptchaParams` قبل أي طلب هاتف. لم يكن له رد، فرأى المتصفح `auth/network-request-failed` بدل `auth/operation-not-allowed`. صار المسار يجيب الرفض نفسه مع CORS، وكذلك `getRecaptchaParam` في v3 | `TestAuthDeferredPhoneRecaptchaParams` |
| `6b7b172` | `updateProfile({photoURL: 'photo-url'})` رُفض بـ `INVALID_PHOTO_URL`، وهو رمز لا تعرفه مكتبة الويب ولا firebase-admin. المحاكي يخزّن أي نص، واختبار `email.test.ts` مكتوب ليعمل على الخدمة الحقيقية أيضًا. بقي حدّ Aruma: 2048 بايتًا. فشل به `allows the user to update profile` في `email` و`custom`، في البيئتين | `TestAuthPhotoURLIsAnyString`، وصفّا `accounts:update` في `TestAuthErrorTables` |
| `a208be6` | `createUser({uid, password})` دون بريد رُفض بـ `MISSING_EMAIL`. firebase-admin تنشئ به مستخدمي `listUsers()` في اختبارها. صار Admin API يحفظ التجزئة، ويظهر مزوّد `password` حين يصير للمستخدم بريد. نقاط العميل لم تتغير | `TestAuthAdminPasswordWithoutEmail` |

## مقترحات لم تُنفَّذ

1. **ردّ واضح لدوال Admin غير المخدومة.**
   - **المشكلة:** `importUsers` (`accounts:batchCreate`) وغيرها من مسارات Admin غير المدعومة، مثل `accounts:query` والإعداد وtenants وإعدادات SAML وOIDC، لا يجيبها Aruma برمز تعرفه المكتبة. فتعرض المكتبة «An internal error has occurred».
   - **المقترح:** تسجيلها في `internal/services/auth/api/admin.go` بالرد `OPERATION_NOT_ALLOWED`، كما تُجاب الطرق المؤجلة في `client.go`. firebase-admin تحوّله إلى `auth/operation-not-allowed`.
   - **مع ذلك:** إضافة `importUsers` إلى «Not yet» في `docs/guides/admin-auth.md`.
   - **لماذا لم يُنفَّذ:** يغيّر جدول المسارات (ADR-008 وADR-014)، وهو قرار نطاق لا إصلاح خطأ.
2. **`nextPageToken` لصفحة ممتلئة في `accounts:batchGet`.**
   - **الفرق:** المحاكي يعيد الرمز متى امتلأت الصفحة، ولو لم يبق بعدها أحد (`batchGet` في firebase-tools). وAruma لا يعيده إلا إن بقي مستخدم.
   - **الأثر:** `listUsers(2, uids[0])` يتوقع رمزًا بعد آخر مستخدمَين.
   - **ما لم يُتحقق منه:** سلوك الخدمة الحقيقية، لأنه يحتاج طلبًا إلى مشروع حقيقي خارج حدود هذه المهمة. والسلوكان تقرؤهما مكتبات Admin صحيحًا.
   - **المقترح:** مطابقة المحاكي، أي إعادة الرمز للصفحة الممتلئة. كلفته طلب صفحة فارغة أخيرة. لم يُنفَّذ لأنه غير مثبت أنه سلوك Firebase.
3. **روابط البريد في مكتبات `@aruma/admin-auth`.**
   - **الوضع:** نجح هنا `generatePasswordResetLink` و`generateEmailVerificationLink`، لأن الخادم يخدم `accounts:sendOobCode` مع `returnOobLink` (`adminSendOobCode` في `api/admin.go`). لكن `ArumaAuth` في `sdk/node/admin-auth` لا يعرض هذه الدوال، ولذلك يذكرها `docs/guides/admin-auth.md` في «Not yet».
   - **المقترح:** إضافتها إلى مكتبتي Node وGo، ثم حذفها من «Not yet». المؤجل منها حينئذ رابط الدخول وحده.
   - **لماذا لم يُنفَّذ:** واجهة جديدة في المكتبات، وليست خطأً في الخادم.

## الحصيلة

- **فاشل ومتخطٍّ:** 181، صُنّفت كلها.
  - **(أ):** 3 أخطاء، أُصلحت كلها، ولكل منها اختبار في `test/acceptance`.
  - **(ب):** الهاتف وMFA ورابط الدخول بالبريد وtenants وSAML وOIDC وFacebook وGitHub و`importUsers` وإعداد المشروع، والتحقق بالمكتبة الرسمية بدل `@aruma/admin-auth` (ADR-004)، وتجزئة كلمة المرور.
  - **(ج):** رموز IdP غير الموقّعة، وصفحة IdP الوهمية في المحاكي.
  - **(د):** SDK الإصدار 8 من gstatic، واختبارات عطّلها المصدر أو تخطّاها في وضع المحاكي، وإعداد الحماية.
- **أخطاء (أ) متبقية:** لم يبق خطأ معروف في نطاق Auth المعلن.
