# ربط تطبيق ويب بمصادقة Aruma

يستخدم تطبيقك مكتبة `firebase` الرسمية كما هي، بملف الإعداد من Aruma. وللاتصال طريقتان:

| الطريقة | ما تضيفه | متى |
|---|---|---|
| **وضع الإنتاج (الموصى به)** | الحزمة `@aruma/web` وسطر في `vite.config`. **لا شيء في كود التطبيق** | دائمًا، وخصوصًا في الإنتاج |
| وضع المحاكي | سطر `connectAuthEmulator` بعد `getAuth()` (ADR-013) | التطوير، أو مشروع لا يمكن تعديل أداة بنائه |

## وضع الإنتاج (الموصى به)

تبقى المكتبة في وضعها العادي، كما مع Firebase تمامًا، وتحوّل `@aruma/web` طلبات Auth وحدها إلى خادمك.

1. **ثبّت الحزمة:** `npm install --save-dev @aruma/web` (من `sdk/web/aruma-web` في هذا المستودع حتى تُنشر).
2. **أضف السطر في `vite.config`:**

```js
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'https://aruma.example.com' })], // public_url لخادمك
});
```

3. **التطبيق لا يتغيّر:** `initializeApp(firebaseConfig)` بملف Aruma، ثم `getAuth(app)`، دون `connectAuthEmulator`.

**كيف يعمل:**
- Aruma يخدم مسارات Google نفسها تحت نطاقه (ADR-014)، فالتحويل تبديل للنطاق وحده:
  - `https://identitytoolkit.googleapis.com/<path>` ← `<url>/identitytoolkit.googleapis.com/<path>`.
  - `https://securetoken.googleapis.com/<path>` ← `<url>/securetoken.googleapis.com/<path>`.
- المكتبة ترسل كل طلبات Auth بـ `fetch`، وتبحث عنه عند كل طلب.
- فالإضافة تحقن قبل سكربتات التطبيق تثبيتًا يلفّ `fetch` مرة واحدة، ولا يغيّر إلا هذين النطاقين. وكل طلب آخر يمر كما هو.

**لماذا هو الموصى به:**
- `connectAuthEmulator` يضع المكتبة في وضع المحاكي، فتُسقط فحوصها هي:
  - التحقق من التطبيق: `appVerificationDisabledForTesting`.
  - وفحص أصل الصفحة لدخول Google وApple بالنافذة أو التحويل.
  - Aruma يفرض ذلك كله في الخادم (ADR-014)، لكن وضع الإنتاج يبقيها في العميل أيضًا.
- وفي وضع الإنتاج تذهب صفحات `authDomain` إلى `https://<authDomain>/__/auth/handler` و`/__/auth/iframe`، ويطلب `getProjectConfig`. وAruma يخدمها كلها.

**الأمان في الحزمة:**
- https وحده، إلا لـ `localhost` و`127.0.0.0/8` و`::1`، أو مع `allowInsecure` للتطوير.
- **تفشل مغلقة:** عنوان مرفوض يوقف البناء. وفي وقت التشغيل تفشل طلبات Auth، ولا تذهب إلى Google بكلمة مرور المستخدم.
- دون عنوان لا تفعل شيئًا.
- والسجل (مطفأ افتراضيًا) لا يحوي الاستعلام ولا الترويسات ولا الرموز ولا الأجسام.

**أدوات بناء أخرى (webpack وغيرها):** اجعل هذا أول استيراد في مدخل التطبيق:

```js
import { install } from '@aruma/web';
install({ url: 'https://aruma.example.com' });
```

**وضع البناء (`mode: 'build'`، Vite 8 فأحدث):**
- لا كود وقت التشغيل: تستبدل الإضافة عنواني Google النصيين داخل `@firebase/auth` أثناء البناء.
- ويفشل البناء إن لم تجدهما، كأن يغيّرهما إصدار جديد من Firebase.

**مُختبَر:** `ARUMA_E2E_MODE=production test/e2e/web-auth/run.sh`، و`production-build` لوضع البناء.
كل التدفقات تنجح على firebase 11 و12 و13 بصفر طلب إلى Google، ومعها اختبار يثبّت الحزمة بعد استيراد المكتبة.
التفاصيل والمخاطر في `docs/research/web-production-redirect.md` وفي `sdk/web/aruma-web/README.md`.

## وضع المحاكي (للتطوير)

### الخطوات

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

### لماذا `connectAuthEmulator`؟

مكتبة Auth للويب تثبّت عنواني Google في كودها، ولا يغيّرهما أي حقل في ملف الإعداد. والطريق الموثّق الوحيد لتوجيهها إلى خادم آخر دون أداة البناء هو هذه الدالة. Aruma يخدم مسارات المصادقة بالشكل الذي تطلبه في هذا الوضع (ADR-014)، ويتحقق من كل شيء في الخادم، فلا يضعف الأمان بشيء:

- **الرموز:** يوقّعها Aruma بمفاتيح RS256 خاصة بكل مشروع، ولا يقبل `alg:none` ولا `Bearer owner` أبدًا.
- **reCAPTCHA:** يُعلن مطفأً، وتحمي حدود المعدل في الخادم من الإغراق.
- **CORS:** مفتوح لمسارات العميل وحدها، دون cookies. فالهوية هي مفتاح API والرمز، لا أصل الصفحة.

## تقييد مفتاح API

المفتاح ليس سرًا، لكن يمكنك أن تحد من أين يُقبل، من شاشة «مفاتيح API» في اللوحة. وAruma يطبّق القيود كما تطبّقها Google:

- **المواقع:** سطر لكل موقع، مثل `example.com/*` و`*.example.com/*`. لا تقوم `*` إلا مقام نطاق فرعي في أول المضيف، أو في آخر المسار. والمتصفح يرسل غالبًا أصل الصفحة وحده (`https://example.com/`) في الطلبات العابرة للمواقع، فقيد بمسار صفحة بعينها قد لا يطابق.
- **أضف مضيف Aruma أيضًا** (أو `authDomain`)، مثل `aruma.example.com/*`. فصفحات روابط البريد ونافذة الدخول بـ Google وApple تفحص المفتاح بعنوانها، كما تطلب Google إضافة `firebaseapp.com`.
- **عناوين IP:** للخوادم وحدها. إن كان أمام Aruma وكيل عكسي فاذكره في `trusted_proxies`، وإلا رأى Aruma عنوان الوكيل.
- **قيود الواجهات:** إن قيّدت المفتاح بواجهات (عبر واجهة اللوحة البرمجية) فاذكر `identitytoolkit.googleapis.com` و`securetoken.googleapis.com` معًا، وإلا فشل تجديد الرمز.

الطلب المرفوض يعود بـ 403، وتظهر في مكتبة الويب رسالة مثل `auth/requests-from-referer-https://other.example/-are-blocked.`.

## ما لا يعمل بعد

| الميزة | الحالة |
|---|---|
| الدخول بالهاتف، وMFA، ورابط الدخول بالبريد | يعيد `auth/operation-not-allowed`، كمشروع عطّل هذا المزوّد. مؤجّل إلى v1.x |
| Google وApple بالنافذة أو التحويل | تحمّل المكتبة ملفًا واحدًا من `apis.google.com`، وهو استثناء معلن في ADR-013 |
| tenants | غير متاحة. `tenantId` غير الفارغ يُرفض بـ `auth/invalid-tenant-id` |
| `signInWithRedirect` حين يكون التطبيق على موقع غير موقع Aruma | تعيد `getRedirectResult` قيمة `null` في المتصفحات التي تعزل تخزين الطرف الثالث، مثل Chrome. وهو القيد نفسه الذي توثّقه Firebase. الحل أن يكون Aruma أو `authDomain` على موقع التطبيق نفسه، أو استخدام `signInWithPopup` |

اختبارات Playwright التي تستدعي `signInWithPopup`: شغّلها بـ `channel: 'chromium'` أو بمتصفح Chrome، لا بالـ headless shell الافتراضي. فهو يجمّد الإطار الخفي العابر للمواقع أحيانًا عند فتح النافذة، فلا تكتمل النافذة الأولى. التفاصيل في `docs/research/phase2-idp.md`.

## التحقق في خادمك

خادمك يتحقق من رموز المستخدمين بمكتبة Aruma للتحقق، بتبديل سطر الاستيراد وحده. التفاصيل في [admin-auth.md](admin-auth.md).

---

# Connecting a web app to Aruma Auth

Your app keeps the official `firebase` package and Aruma's config file.

**Production mode (recommended):** no change in the app's code. Add `@aruma/web` and one line in `vite.config`: `plugins: [aruma({ url: 'https://aruma.example.com' })]` (`import aruma from '@aruma/web/vite'`). The SDK stays in its normal production mode, with its own origin and app checks, and the plugin rewrites only `identitytoolkit.googleapis.com` and `securetoken.googleapis.com` onto `<url>/<host>/<path>`. It is https-only except for loopback, fails closed (a refused URL stops the build, and Auth requests fail instead of reaching Google), and logs no query, header, token or body. Other bundlers: call `install({ url })` from `@aruma/web` first in the entry. `mode: 'build'` (Vite 8+) rewrites the host literals at build time instead. See `sdk/web/aruma-web/README.md`.

**Emulator mode (development):** one line after `getAuth()` (ADR-013):

```js
const auth = getAuth(app);
connectAuthEmulator(auth, "https://aruma.example.com", { disableWarnings: true }); // the only line
```

Use your server's `public_url`. Enable the sign-in methods you need in the dashboard (email/password is on by default, anonymous is off as in Firebase), and add your app's domain to the authorised domains for email links and Google/Apple sign-in.

`connectAuthEmulator` is the only documented way to point the web Auth SDK at another server: the Google hosts are fixed in its code. Aruma serves the paths that mode calls (ADR-014) and enforces everything server-side: per-project RS256 keys, never `alg:none` or `Bearer owner`, reCAPTCHA reported off with server rate limits instead, and CORS open on client routes only, without cookies.

API key restrictions set on the dashboard's API keys screen are enforced as Google enforces them. Websites match the `Referer` header with Google's wildcard rules (`example.com/*`, `*.example.com/*`; a `*` only for a subdomain or at the end of the path); also list Aruma's host or `authDomain`, since the email action and sign-in pages check the key from their own URL, as Google requires `firebaseapp.com`. IP addresses match the client address resolved through `trusted_proxies` only. A key restricted to APIs must list both `identitytoolkit.googleapis.com` and `securetoken.googleapis.com`, or token refresh fails. A refused call gets 403 `PERMISSION_DENIED`, which the web SDK reports as e.g. `auth/requests-from-referer-https://other.example/-are-blocked.`.

Not yet supported: phone, MFA and email-link sign-in answer `auth/operation-not-allowed` (v1.x); Google/Apple popup and redirect load one script from `apis.google.com` (declared exception in ADR-013); tenants are refused with `auth/invalid-tenant-id`. `signInWithRedirect` from an app on a different site than Aruma returns `null` from `getRedirectResult` in browsers that partition third-party storage (Chrome), the limitation Firebase documents too: serve Aruma or the custom `authDomain` on the app's site, or use `signInWithPopup`. Playwright tests that drive `signInWithPopup` should launch with `channel: 'chromium'` (or Chrome), not the default headless shell, which sometimes freezes the cross-site relay iframe when the popup opens (`docs/research/phase2-idp.md`).

To verify tokens on your server, swap one import line for Aruma's verification library: see [admin-auth.md](admin-auth.md).
