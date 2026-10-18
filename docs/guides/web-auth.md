# ربط تطبيق ويب بمصادقة Aruma

يستخدم تطبيقك مكتبة `firebase` الرسمية كما هي. يكفي ملف الإعداد من Aruma، وسطر واحد بعد `getAuth()` (ADR-013).

## الخطوات

1. **أنشئ تطبيق ويب** في المشروع من اللوحة، وانسخ ملف الإعداد (`firebaseConfig`).
2. **فعّل طرق الدخول** التي تريدها من شاشة «المصادقة ← طرق الدخول». البريد وكلمة المرور مفعّل افتراضيًا، والدخول المجهول معطّل كما في Firebase.
3. **أضف نطاق تطبيقك** إلى «النطاقات المصرّح بها» إن كنت ستستخدم روابط البريد أو دخول Google وApple. `localhost` مصرّح به افتراضيًا.
4. **أضف السطر:**

```js
import { initializeApp } from "firebase/app";
import { getAuth, connectAuthEmulator } from "firebase/auth";

const app = initializeApp(firebaseConfig); // من Aruma، دون تعديل
const auth = getAuth(app);
connectAuthEmulator(auth, "https://aruma.example.com", { disableWarnings: true }); // السطر الوحيد
```

ضع عنوان خادم Aruma العام (`public_url`) مكان `https://aruma.example.com`. وبعدها تعمل دوال المكتبة كما هي: `createUserWithEmailAndPassword` و`signInWithEmailAndPassword` و`signInAnonymously` و`linkWithCredential` و`signInWithCustomToken` و`updateProfile` و`updatePassword` و`deleteUser` و`onAuthStateChanged`، والتجديد التلقائي للرمز.

## لماذا `connectAuthEmulator`؟

مكتبة Auth للويب تثبّت عنواني Google في كودها، ولا يغيّرهما أي حقل في ملف الإعداد. والطريق الموثّق الوحيد لتوجيهها إلى خادم آخر هو هذه الدالة. Aruma يخدم مسارات المصادقة بالشكل الذي تطلبه في هذا الوضع (ADR-014)، ويتحقق من كل شيء في الخادم، فلا يضعف الأمان بشيء:

- **الرموز:** يوقّعها Aruma بمفاتيح RS256 خاصة بكل مشروع، ولا يقبل `alg:none` ولا `Bearer owner` أبدًا.
- **reCAPTCHA:** يُعلن مطفأً، وتحمي حدود المعدل في الخادم من الإغراق.
- **CORS:** مفتوح لمسارات العميل وحدها، دون cookies. فالهوية هي مفتاح API والرمز، لا أصل الصفحة.

## ما لا يعمل بعد

| الميزة | الحالة |
|---|---|
| الدخول بالهاتف، وMFA، ورابط الدخول بالبريد | يعيد `auth/operation-not-allowed`، كمشروع عطّل هذا المزوّد. مؤجّل إلى v1.x |
| Google وApple بالنافذة أو التحويل | تحمّل المكتبة ملفًا واحدًا من `apis.google.com`، وهو استثناء معلن في ADR-013 |
| tenants | غير متاحة. `tenantId` غير الفارغ يُرفض بـ `auth/invalid-tenant-id` |

## التحقق في خادمك

خادمك يتحقق من رموز المستخدمين بمكتبة Aruma للتحقق، بتبديل سطر الاستيراد وحده. التفاصيل في [admin-auth.md](admin-auth.md).

---

# Connecting a web app to Aruma Auth

Your app keeps the official `firebase` package. It needs Aruma's config file and one line after `getAuth()` (ADR-013):

```js
const auth = getAuth(app);
connectAuthEmulator(auth, "https://aruma.example.com", { disableWarnings: true }); // the only line
```

Use your server's `public_url`. Enable the sign-in methods you need in the dashboard (email/password is on by default, anonymous is off as in Firebase), and add your app's domain to the authorised domains for email links and Google/Apple sign-in.

`connectAuthEmulator` is the only documented way to point the web Auth SDK at another server: the Google hosts are fixed in its code. Aruma serves the paths that mode calls (ADR-014) and enforces everything server-side: per-project RS256 keys, never `alg:none` or `Bearer owner`, reCAPTCHA reported off with server rate limits instead, and CORS open on client routes only, without cookies.

Not yet supported: phone, MFA and email-link sign-in answer `auth/operation-not-allowed` (v1.x); Google/Apple popup and redirect load one script from `apis.google.com` (declared exception in ADR-013); tenants are refused with `auth/invalid-tenant-id`.

To verify tokens on your server, swap one import line for Aruma's verification library: see [admin-auth.md](admin-auth.md).
