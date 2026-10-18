# الويب في وضع الإنتاج: تحويل طلبات Auth داخل التطبيق (@aruma/web)

- **الحالة:** تجربة مكتملة، 2026-10-18، فرع `p2-k-web-redirect`. وهي نظير `ios-production-redirect.md`.
- **السؤال:** هل تعمل مكتبة `firebase` الرسمية للويب مع Auth في Aruma **في وضعها العادي**، دون `connectAuthEmulator`، ودون أي سطر في كود التطبيق؟
- **الجواب: نعم.**
  - كل تدفقات اختبار الويب نجحت على firebase 11 و12 و13، بصفر طلب إلى Google.
  - وفي تطبيق التوافق طابقت 12/12 سيناريو Firebase، على http وعلى https.
- **المراجع:** ADR-013 (السطر الواحد والاستثناءات)، وADR-014، والحزمة `sdk/web/aruma-web`.

## لماذا

`connectAuthEmulator` يعمل، لكنه يضع المكتبة في وضع المحاكي:
- يضبط `appVerificationDisabledForTesting = true`.
- ويتخطّى `_validateOrigin` فحص أصل الصفحة في دخول Google وApple بالنافذة أو التحويل.

Aruma يفرض ذلك في الخادم (ADR-014)، لكن وضع الإنتاج يبقي فحوص المكتبة نفسها، ولا يحتاج سطرًا في الكود.

ما ثبت بقراءة `@firebase/auth` (1.10.8 في firebase 11، و1.13.6 في 12، و1.13.7 في 13):
- `registerAuth` يثبّت `apiHost` و`tokenApiHost` و`apiScheme` نصوصًا، ولا يقرؤها من ملف الإعداد.
- كل طلبات Auth تمر بـ `FetchProvider.fetch()`، ولا `XMLHttpRequest`.
- وفي بناء المتصفح (`dist/esm`) يُبحث عن `self.fetch` ثم `globalThis.fetch` **عند كل طلب**، فيصل إليها لفّ `fetch` قبل استيراد المكتبة أو بعده.
  - أما بناء Node (`dist/node-esm`) فيلتقط `fetch` عند الاستيراد (`FetchProvider.initialize(fetch, …)`).

## كيف تعمل

Aruma يخدم مسارات Google تحت نطاقه، فالتحويل تبديل للنطاق وحده:

| تطلب المكتبة | يذهب إلى |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<url>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<url>/securetoken.googleapis.com/<path>` |

1. **`install({ url })`** يلفّ `globalThis.fetch` مرة واحدة، ويُعيد ضبطه دون لفّ ثانٍ إن استُدعي مجددًا.
   - لا يحوّل إلا https على المنفذ الافتراضي إلى هذين النطاقين: نصًا كان الطلب، أو `URL`، أو `Request`.
   - يُبقي الطريقة والترويسات والجسم والإشارة. والـ `Request` يُعاد بناؤه بجسمه في الذاكرة، فلا حاجة إلى `duplex`.
   - كل طلب آخر يصل إلى `fetch` الأصلي بالكائنات نفسها.
2. **إضافة Vite** (`@aruma/web/vite`) تحقن `<script type="module">` أول عنصر في `<head>`.
   - يستورد وحدة افتراضية تستدعي `install`.
   - في `vite build` تصير أول وحدة في الحزمة، قبل كود التطبيق (تُحقّق في بناء تطبيق التوافق).
3. **وضع البناء** (`mode: 'build'`) يستبدل النصين `"identitytoolkit.googleapis.com"` و`"securetoken.googleapis.com"` داخل `@firebase/auth`.
   - ويستبدل `apiScheme` حين يكون Aruma على http محلي.
   - يعمل في `vite build` وفي مُحسّن الاعتماديات لخادم التطوير. ويحتاج Vite 8 (Rolldown)، ويرفض ما قبله.
   - `EID_FROM_APIHOST` يستخدم النص نفسه، فيبقى متسقًا.
   - **يفشل البناء** إن لم يجد النصين.
4. **`@aruma/web/auto`** يثبّت من `globalThis.__ARUMA__` أو `import.meta.env.VITE_ARUMA_URL`، لأدوات البناء الأخرى.

**الأمان** (كما في ArumaRedirect):
- https وحده، إلا للعناوين المحلية، أو مع `allowInsecure`.
- عنوان مرفوض يوقف البناء أو يرمي خطأ. وفي كل الأحوال تفشل طلبات Auth ولا تذهب إلى Google.
- ولا يُقبل نطاق Google نفسه عنوانًا لـ Aruma.
- دون عنوان لا يحدث شيء.
- السجل اختياري، ولا يحوي إلا الطريقة والنطاق والمسار.

## ما يضيفه المطوّر

| الأداة | الإضافة |
|---|---|
| Vite | `@aruma/web` + `plugins: [aruma({ url })]` في `vite.config` |
| webpack وغيرها | `@aruma/web` + `install({ url })` أول استيراد في المدخل |
| ملف الإعداد | `firebaseConfig` من Aruma كما هو |
| كود التطبيق | **لا شيء** |

## النتائج

**اختبارات Playwright في المستودع** (`test/e2e/web-auth`، خادم جديد لكل إصدار، وحارس يرفض كل طلب خارج الجهاز):

| الوضع | firebase 11.10.0 | firebase 12.19.0 | firebase 13.0.0 |
|---|---|---|---|
| `emulator` (كما كان) | 8/8 | 8/8 | 8/8 |
| `production` (لفّ fetch) | 9/9 | 9/9 | 9/9 |
| `production-build` (`vite build` + `vite preview`) | 8/8 | 8/8 | 8/8 |

- التدفقات:
  - البريد وكلمة المرور كاملة، والمجهول مع الربط، وcustom token.
  - والإبطال، ورموز الأخطاء، والتحقق من البريد، واستعادة كلمة المرور، وتغيير البريد.
- الاختبار التاسع في `production`: يزيل التحويل ويعيد تثبيته بعد أن استُوردت المكتبة واستُخدمت، فينجح الدخول المجهول التالي عبر Aruma.
- في الوضعين يُتحقّق أن المكتبة فعلًا في وضع الإنتاج:
  - `emulatorConfig` فارغ.
  - و`apiHost` هو نطاق Google في `production`، و`<aruma>/identitytoolkit.googleapis.com` في `production-build`.
- `production-build` نجح أيضًا على خادم التطوير (مُحسّن الاعتماديات)، بمنفذ مختلف في كل تشغيل.
- **شاهد سلبي:** دون الإضافة، يذهب أول طلب إلى `https://identitytoolkit.googleapis.com/v1/accounts:signUp`، فيرفضه الحارس ويفشل الاختبار.
- اختبارات الوحدة (`node:test`): 37 اختبارًا لقواعد التحويل، والأشكال الثلاثة، والمرور، والأمان، والتكرار، والإضافة.

**تطبيق التوافق** (`aruma-conformance`، الهدف الجديد `aruma-prod` للويب، firebase 13.0.0، قاعدة جديدة `aruma_webprod`):

| | http (Chromium) | https (Firefox) |
|---|---|---|
| سيناريوهات Auth مقابل Firebase | 12 متطابق، 0 مختلف | 12 متطابق، 0 مختلف |
| عناوين Auth إنتاجية طلبها SDK وحوّلتها الحزمة | 70 | 70 |
| وصلت إلى Aruma (Playwright وسجل الخادم، أو وكيل TLS) | 70 | 70 |
| طلبات إلى Google أو خارج الجهاز | 0 | 0 |

التوزيع في كل تشغيل:
- `accounts:lookup` 25، و`signInWithPassword` 15، و`signUp` 13، و`delete` 11.
- و`sendOobCode` 2، و`update` 2، و`securetoken …/v1/token` 2.

**https:**
- وكيل TLS محلي على `https://localhost:8443`، و`public_url` للخادم هو العنوان نفسه.
- شهادة `localhost` من CA مؤقت حُذف مفتاحه بعد الإصدار.
- الـ CA مُضاف إلى مخزن NSS في ملف Firefox جديد، فالتحقق عادي كامل، دون `ignoreHTTPSErrors`.
- وملف دون الـ CA رفض الشهادة نفسها.
- لماذا Firefox:
  - Chromium على macOS لا يثق إلا بـ Keychain النظام، وتعديله خارج حدود الاختبار.
  - وعلى Linux يكفي `~/.pki/nssdb` للمستخدم بالطريقة نفسها.
- في الإنتاج تكفي شهادة عامة عادية لنطاق Aruma.

## المخاطر

1. **تغيّر SDK:**
   - إن انتقلت المكتبة إلى نطاق جديد، يمر طلبها دون تحويل إلى Google بمفتاح Aruma، فيفشل لأن المفتاح غير معروف.
   - **لكن كلمة المرور تصل إلى Google حينها.** وكذلك إن تركت `fetch` إلى آلية أخرى.
   - العلاج: تثبيت إصدار Firebase، وتشغيل `production` في CI قبل كل ترقية (مضاف إلى CI مع هذا الفرع).
   - وفي وضع البناء يفشل البناء بدل ذلك.
2. **مكتبة تستبدل `fetch` دون أن تستدعي السابق،** بعد التثبيت، تُسقط التحويل. أما المكتبات التي تلفّه، كأدوات المراقبة، فلا تضر.
3. **SSR في Node:** بناء Node يلتقط `fetch` عند الاستيراد. فالتثبيت قبل استيراد `firebase/auth`، أو وضع البناء.
4. **دخول Google وApple بالنافذة أو التحويل** ما زال يحمّل `apis.google.com/js/api.js`، وهو الاستثناء المعلن في ADR-013.
   - ولم يُختبر هنا في وضع الإنتاج، فاختباره خارج النطاق الآن.
   - صفحات `/__/auth/handler` و`/__/auth/iframe` و`getProjectConfig` يخدمها Aruma.
5. **CSP:** الإضافة تحقن `<script type="module">` مضمّنًا في `index.html` أثناء التطوير. وفي `vite build` يُدمج في حزمة المدخل، فلا سكربت مضمّن.

## التوصية

- **اعتماد `@aruma/web` طريقًا موصى به للويب في الإنتاج:**
  - يحقق «صفر كود»: سطر في `vite.config` وحزمة.
  - ويُبقي فحوص المكتبة التي يُسقطها وضع المحاكي.
- **يبقى `connectAuthEmulator` خيارًا للتطوير،** ولمن لا يستطيع تعديل أداة البناء.
- **يُحدَّث ADR-013** بقرار من صاحب المشروع:
  - الويب يُدعم بالحزمة في الإنتاج، والسطر الواحد في الكود يصير البديل.
  - ونشر الحزمة على npm.
- **لا تُعمَّم الآلية على Firestore وRTDB:** لهما إعداد `host` موثّق وقرارات في مراحلهما.
