# إغلاق Auth: الإصدار 0.1

- **التاريخ:** 2026-10-20
- **الإصدار:** `v0.1.0`، أي `main` بعد دمج المراجعة الأمنية وقيود المفاتيح وT23.
- **الخلاصة:** Auth مطابق لـ Firebase على الويب وiOS وAndroid وNode، بكل طرق الاتصال. ولا طلب Auth يخرج إلى Google.

## ما يثبت ذلك

| الدليل | أين | النتيجة |
|---|---|---|
| سيناريوهات P0 | [auth-p0-scenarios.md](auth-p0-scenarios.md)، في CI | 27/27 |
| اختبارات Firebase الرسمية لـ Auth (T23) | [firebase-js-suites.md](firebase-js-suites.md) | 180 ناجحًا، وكل ما لم ينجح مصنّف بسببه. ثلاثة أخطاء في Aruma أُصلحت |
| المراجعة الأمنية | [../security/review-v0.1.md](../security/review-v0.1.md) | لا حرج ولا عالٍ. سبع مشاكل أُصلحت، منها اثنتان متوسطتان |
| تطبيق التوافق على الأجهزة | مستودع `aruma-conformance`، `results/ARUMA-REPORT.md` | الجدول أدناه |

## تطبيق التوافق: Firebase الحقيقي مقابل Aruma

تطبيق شات واحد بـ Flutter (iOS وAndroid) وVite (الويب) وNode (Admin)، و13 سيناريو Auth للعميل و10 لـ Admin.
كل خانة لـ Aruma تُقارن بنتيجة Firebase الحقيقي **على المنصة نفسها**، حقلًا بحقل.

| العميل | Firebase | Aruma: المحاكي | Aruma: الإنتاج (مكتبة التحويل) | Aruma: https |
|---|---|---|---|---|
| الويب | 12 ناجح | 12/12 مطابق | 12/12 مطابق | 12/12 مطابق |
| iOS (محاكي) | 12 ناجح | 12/12 مطابق | 12/12 مطابق | 12/12 مطابق |
| Android (محاكي API 33) | 12 ناجح | 12/12 مطابق | 12/12 مطابق، وفي release مصغّر بـ R8 | 12/12 مطابق |
| Node (Admin) | 10 ناجح | 10/10 مطابق | لا ينطبق | لا ينطبق |

- السيناريو المتخطّى في كل خانة: الدخول بحساب Google حقيقي، لأنه يحتاج حسابًا ونطاقًا حقيقيين.
- **`providerId` على Android:** Firebase نفسه يعيد `null` بعد الدخول بالبريد على Android، و`password` على iOS والويب. فرق منصة، وAruma يطابق كل منصة.
- مرجع Node من 2026-10-17. الأرقام الأخرى من 2026-10-20 على `main`.

## قيود مفاتيح API على الأجهزة

قيد صحيح يمرّ، وقيد خاطئ يُرفض بسبب Google نفسه. فالمكتبات الرسمية ترسل هوية التطبيق التي يعتمد عليها الفحص.

| المنصة | القيد | صحيح | خاطئ | ما يراه التطبيق |
|---|---|---|---|---|
| الويب | `allowedReferrers` | 12/12 | مرفوض كله | `auth/requests-from-referer-…-are-blocked.` |
| iOS | `allowedBundleIds` | 12/12 | مرفوض كله | `internal-error`، والرسالة الأصلية داخله |
| Android | الحزمة + SHA-1 | 12/12، في المحاكي والإنتاج | مرفوض كله | `FirebaseException` بكود `unknown`، والرسالة داخله |

## الاتصال بـ Google

- **طلبات Auth إلى Google: صفر**، على كل منصة وفي كل وضع. كل طلب وصل إلى Aruma.
- **iOS:** طلب واحد عند الإقلاع إلى `firebaseinstallations.googleapis.com`، من مكتبات Analytics وCrashlytics وRemote Config، حتى مع إيقاف جمع البيانات. ليس من Auth. موثّق في [ArumaRedirect](../../sdk/ios/ArumaRedirect/README.md).
- **الويب وAndroid وNode:** لا محاولة.
- Analytics وCrashlytics لم يرسلا شيئًا لأن تطبيق الاختبار يطفئهما. التطبيق الذي يبقيهما يتصل بـ Google.

**الحراس:**
- **في CI:** حارس الويب (Playwright) وحارس Node يُفشلان أي اختبار يخرج منه طلب.
- **يدويًا مع تطبيق التوافق:** حارس شبكة على الجهاز لـ iOS وAndroid.

## مكتبة Android

- **http إلى عنوان غير loopback** (مثل `10.0.2.2`) مسموح في التطبيق القابل للتصحيح (debuggable) وحده.
- **في release** يُرفض عند الإقلاع برسالة واضحة، إلا مع `ArumaAllowInsecureURL`.
- **https في release:** 12/12.

## ما لم يُختبر

- **هاتف حقيقي:** كل الأجهزة محاكيات.
- **الدخول بحساب Google وApple حقيقي:** يحتاج نطاقًا بشهادة https.
- **iOS وAndroid في CI:** تُختبر يدويًا اليوم، والتشغيل الآلي يحتاج Codemagic.

## خارج النطاق عمدًا

- **ميزات Auth غير المبنية:** الدخول بالهاتف، وMFA، والدخول برابط البريد، والمستأجرون (tenants)، وSAML/OIDC، وFacebook/GitHub.
- **Admin API:** `importUsers` وإعداد المشروع.
- **التفاصيل والأسباب:** [firebase-js-suites.md](firebase-js-suites.md).
