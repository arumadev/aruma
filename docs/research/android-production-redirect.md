# Android في وضع الإنتاج: أين تخرج طلبات Auth، وكيف تُحوَّل

- **الحالة:** اكتشاف مكتمل (المراحل 0 إلى 3)، 2026-10-18. **المكتبة مبنية:** `sdk/android/aruma-redirect` (راجع "كيف تستخدمها" أدناه).
- **السؤال:** مكتبة Firebase الرسمية لـ Android **في وضعها العادي** (دون `useEmulator`): من أي عملية تخرج طلبات Auth، وبأي واجهة، وإلى أي عناوين؟ وأي طبقة تصلح لتحويلها إلى Aruma؟
- **الجواب:**
  - الطلبات تخرج من **عملية التطبيق نفسها**، لا من Play services.
  - تمر كلها بـ **`java.net.URL.openConnection`** (أي `HttpURLConnection`).
  - العناوين هي عائلات iOS نفسها: `www.googleapis.com/identitytoolkit/v3/relyingparty/*` و`securetoken.googleapis.com/v1/token`.
  - **`URL.setURLStreamHandlerFactory`** (واجهة عامة) يحوّلها كلها: إثبات مؤقت طابق وضع المحاكي في 12/12 سيناريو، بصفر طلب Auth إلى Google.
- **المراجع:** ADR-013 وADR-014، و`ios-production-redirect.md` و`web-production-redirect.md`. والتطبيق: `aruma-conformance` (فرع `aruma-target`).

## المنهج والحدود

- **مراقبة وقت التشغيل فقط، على حدود واجهات المنصة العامة:**
  - `java.net.URL.openConnection`، و`java.net.Socket.connect`، و`java.net.InetAddress.getAllByName`، و`android.net.Network.openConnection`.
  - و`getaddrinfo` و`connect` من libc (لأي نقل أصلي مثل Cronet أو gRPC).
  - و`okhttp3.OkHttpClient.newCall` و`org.chromium.net.CronetEngine$Builder.build` إن وُجدا.
- **لم يُفكّ ولم يُقرأ أي كود** لـ Firebase أو Play services. أسماء الأصناف أدناه من مكدّسات الاستدعاء كما ظهرت وقت التشغيل.
- **السجل لا يحوي أجسامًا ولا ترويسات.** والاستعلام يُسجَّل بأسماء مفاتيحه فقط: `?key=<redacted>`.
- **لا اتصال بـ Google:**
  - على الجهاز سلسلة iptables `ARUMA_GUARD` (`adb root`) ترفض كل خروج إلا إلى loopback و`10.0.2.2`، أي خادم Aruma. وخيار المحاكي `-dns-server` لم يمنع DNS.
  - وخطّاف Frida في التطبيق يرمي `IOException` لأي نطاق Google بعد تسجيله، قبل حلّ الاسم.
  - ملاحظة: Play services على الجهاز اتصلت بـ Google عند إقلاع المحاكي، قبل تثبيت الحارس وقبل تثبيت التطبيق. لم يكن فيها شيء من Firebase ولا من بيانات الاختبار.

**البيئة:**

| | |
|---|---|
| الجهاز | AVD جديد `aruma-a34`، صورة `android-33/google_apis/arm64-v8a` (صورة API 34 المثبّتة فارغة)، فيها Play services، وحساب Google غير مسجّل |
| المكتبة | firebase-auth 25.0.0، من firebase_auth 7 في Flutter |
| الخادم | Aruma من `phase2`، قاعدة جديدة `aruma_android` (حُذفت)، على `127.0.0.1:8094` |
| `public_url` | `http://10.0.2.2:8094`: المضيف كما يراه المحاكي، دون `adb reverse`، ومُصدِر الرموز هو العنوان الذي يطلبه الجهاز |
| Frida | 17.23.3، و`frida-java-bridge` 7.0.13 (Frida 17 لا يضمّن Java في السكربتات) |

## المرحلة 1: وضع المحاكي

`useAuthEmulator('10.0.2.2', 8094)` بملف `google-services.json` من Aruma. وhttp مسموح في بناء debug وحده (`network_security_config`).

- **11/12 نجحت.** ومقارنةً بـ Firebase iOS (لا مرجع Firebase لـ Android، والقيم المرصودة لا تخص منصة): 10 متطابق، 2 مختلف.
- **فرق 1 خطأ في Aruma:** `getOobConfirmationCode` يرسل `"requestType": 1`، أي رقم enum بصيغة proto3 JSON، لا `"PASSWORD_RESET"`.
  - حقل Aruma نصّي، فيرد `INVALID_JSON`.
  - الشكل كما رآه وكيل يسجّل الأسماء والأنواع فقط: `requestType:number=1 email:string androidInstallApp:boolean canHandleCodeInApp:boolean clientType:string`.
  - **الإصلاح:** قبول الرقم والاسم معًا في كل حقول enum في `internal/identity/api` (proto3 JSON يقبل الاثنين).
- **فرق 2 غير محسوم:** `additionalUserInfo.providerId` بعد الدخول بالبريد هو `null` على Android، و`password` على iOS والويب.
  - قد يكون فرق منصة أو حقلًا ناقصًا في رد `verifyPassword`. يحسمه تشغيل واحد على Firebase الحقيقي لـ Android.
- **حتى في وضع المحاكي** يستخدم SDK Android مسارات v3: `http://10.0.2.2:8094/www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser`.

## المرحلة 2: الاكتشاف في وضع الإنتاج

هدف `aruma-prod-discovery`: ملف `google-services.json` من Aruma ولا `useAuthEmulator`.
- التطبيق يُشغَّل بـ Frida في وضع spawn، والخطاطيف محمّلة قبل أي كود.
- وعمليات Play services الأربع مراقَبة (`com.google.android.gms` و`.persistent` و`.unstable` و`com.google.process.gservices`).
- قائمة العمليات قبل التشغيل وبعده: لم تظهر عملية Google جديدة غير هذه.

### (أ) العملية: التطبيق، لا Play services

```
proc=com.aruma.conformance.flutter_chat  thread="Firebase Blocking Thread #2"
URL.openConnection https://www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser?key=<redacted>  -> blocked (12 مرة)
```

- في عمليات Play services الأربع: **صفر** طلب إلى identitytoolkit أو securetoken أو أي عنوان Firebase.
- والطلب كله في عملية التطبيق: من خيط منفّذ Firebase إلى `URL.openConnection` ثم `Socket.connect`، دون أي عملية أخرى بينهما.

### (ب) الواجهة: `HttpURLConnection` عبر `URL.openConnection`

المكدّس (أعلى 12 إطارًا، كما سُجّل):

```
java.net.URL.openConnection
com.google.android.gms.internal.firebase-auth-api.zzajj.zzb
com.google.android.gms.internal.firebase-auth-api.zzaip.zzr
com.google.android.gms.internal.firebase-auth-api.zzaga.zzm
com.google.android.gms.internal.firebase-auth-api.zzaia.zzf
com.google.android.gms.internal.firebase-auth-api.zzagf.zzc
com.google.android.gms.internal.firebase-auth-api.zzajd.run
java.util.concurrent.ThreadPoolExecutor.runWorker
java.util.concurrent.ThreadPoolExecutor$Worker.run
com.google.firebase.concurrent.CustomThreadFactory.lambda$newThread$0$...
com.google.firebase.concurrent.CustomThreadFactory$$ExternalSyntheticLambda0.run
java.lang.Thread.run
```

- الاسم `com.google.android.gms.internal.firebase-auth-api` هو حزمة مكتبة firebase-auth **داخل APK التطبيق**، لا تطبيق Play services. وكل نقاط الدخول تمر بالإطار الأول نفسه (`zzajj.zzb` ← `URL.openConnection`).
- **النقل تحته:** `com.android.okhttp` الداخلي في المنصة. الاتصال المحوَّل صنفه `com.android.okhttp.internal.huc.HttpURLConnectionImpl`:

  ```
  java.net.Socket.connect 10.0.2.2:8094
  com.android.okhttp.internal.Platform.connectSocket
  com.android.okhttp.internal.io.RealConnection.connect
  ...
  com.android.okhttp.internal.huc.HttpURLConnectionImpl.getOutputStream
  ```

- `okhttp3`: غير موجود في التطبيق. وCronet: صنفه العام موجود في عمليات Play services وحدها، وليس في مسار Auth. ولم يرَ libc أي اتصال من التطبيق غير `10.0.2.2`.

### (ج) العناوين

من تشغيل الإنتاج المحوَّل (كل الطلبات نجحت فظهرت كل التدفقات): **72 طلبًا، كلها في عائلتين**:

| العنوان الإنتاجي | العدد |
|---|---|
| `www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo` | 28 |
| `.../relyingparty/verifyPassword` | 15 |
| `.../relyingparty/signupNewUser` | 13 |
| `.../relyingparty/deleteAccount` | 11 |
| `.../relyingparty/setAccountInfo` | 2 |
| `.../relyingparty/getOobConfirmationCode` | 1 |
| `securetoken.googleapis.com/v1/token` | 2 |

- لا `identitytoolkit.googleapis.com/v1` في هذه التدفقات (بخلاف الويب). والنمط نفسه في iOS، وعدده هناك 73: طلب oob ثانٍ لم يصله Android لأن الأول فشل بالخطأ أعلاه.
- كلها https على المنفذ الافتراضي، ومفتاح API في الاستعلام `key=`.

### (د) طبقة التحويل: `URL.setURLStreamHandlerFactory` تصلح

إثباتان مؤقتان بـ Frida، على APK الإنتاج نفسه، **لا مكتبة**:

| الإثبات | ما يفعله | Auth المحوَّل | وصل إلى Aruma | النتيجة |
|---|---|---|---|---|
| تحويل قبل `openConnection` | خطّاف على `URL.openConnection` يبدّل العنوان | 72/72 | 72 | 11/12، ومطابق لوضع المحاكي 12/12 |
| **`URL.setURLStreamHandlerFactory`** | **لا خطّاف أصلًا**: يسجّل factory لـ https فقط عند الإقلاع، كما سيفعل ContentProvider | 72/72 | 72 | 11/12، ومطابق لوضع المحاكي 12/12 |

- الفشل الوحيد هو خطأ `requestType` في Aruma نفسه. ولا طلب Auth وصل إلى Google.
- **طريقة الـ factory:**
  - `createURLStreamHandler("https")` يعيد معالجًا يحوّل العائلات الثلاث إلى `<ArumaURL>/<host>/<path>?<query>`، ويعيد `null` لغير https.
  - كل عنوان https آخر يُعاد إلى معالج المنصة: `new URL(contextUrl, spec)`، و`contextUrl` عنوان أُنشئ قبل التثبيت فيحمل معالج المنصة. كل هذا واجهات عامة.
- المكتبة قبلت `HttpURLConnection` عاديًا (http إلى Aruma المحلي) مكان `HttpsURLConnection`: لم يحدث `ClassCastException`. ومع Aruma على https في الإنتاج يعود النوع https أصلًا.
- مكان التثبيت المقترح: ContentProvider، فهو يعمل قبل `Application.onCreate`. وFirebase تبني `URL` لكل طلب، فيكفي أن يسبق التثبيت أول طلب Auth.

### (هـ) عناوين Google الأخرى أثناء التدفقات

- **في التطبيق:**
  - لا شيء غير طلبات Auth، وقراءة `jar:file:…/base.apk!/firebase-auth.properties` (رقم الإصدار، محلي).
  - لم يظهر `firebaseinstallations.googleapis.com`، بخلاف iOS.
  - Analytics وCrashlytics مُطفأة في التشغيل بمفاتيح المانيفست، كما في iOS. وهما يرسلان إلى Google وحدها، فهما خارج النطاق.
- **في Play services**، خلفية لا صلة لها بـ Auth، وتوقيتها بعد انتهاء السيناريوهات. وكلها رفضها الحارس:
  - `fonts.gstatic.com` عبر `URL.openConnection`.
  - و`connect` إلى `2001:4860:4860::8888:53` (DNS).
- **لم تُجرَّب** تدفقات قد تمر بـ Play services: الهاتف (Play Integrity/SafetyNet وreCAPTCHA)، ودخول Google (Credential Manager)، وApp Check، و`getRecaptchaConfig`.

## كيف تستخدمها

`sdk/android/aruma-redirect`: مكتبة Java بلا اعتماديات، minSdk 24، 28 اختبار وحدة على JVM (`./gradlew :aruma-redirect:testDebugUnitTest`).
تفاصيلها في `sdk/android/aruma-redirect/README.md`. الخطوات الدنيا لمطوّر حقيقي:

1. `./gradlew :aruma-redirect:publishToMavenLocal` من `sdk/android/aruma-redirect` (مؤقتًا حتى يُنشر الـ AAR).
2. في التطبيق: `mavenLocal()` في المستودعات، و`implementation("dev.aruma:aruma-redirect:0.1.0")`.
   في Flutter: الاعتمادية في `android/app/build.gradle(.kts)`، والمستودع في `android/build.gradle(.kts)`.
3. في `AndroidManifest.xml` داخل `<application>`: `<meta-data android:name="ArumaURL" android:value="https://aruma.example.com"/>`.
4. `google-services.json` من Aruma، ولا `useAuthEmulator`. لا كود.

- **التثبيت:** `ContentProvider` (`initOrder` 1000) يقرأ `ArumaURL` قبل `Application.onCreate`. وبديله في الكود `ArumaRedirect.install(url)`.
- **المعالج الافتراضي:** عند التثبيت يُنشأ `URL` https قبل تسجيل الـ factory فيحمل معالج المنصة، ثم يُبنى كل اتصال غير Auth (وكل اتصال محوَّل) بـ `new URL(thatUrl, spec).openConnection()`، فيعود معالج المنصة نفسه.
  واجهات Java عامة فقط: لا انعكاس، ولا أصناف `com.android.okhttp`. والـ factory يعيد `null` لغير https فيبقى http على الافتراضي.
- **http:** مقبول دون إعداد لـ `localhost` و`127.0.0.0/8` و`::1` و`10.0.2.2` و`10.0.3.2`، وغيرها بـ meta-data `ArumaAllowInsecureURL=true` (للتطوير). ويلزم `networkSecurityConfig` للـ cleartext في debug.
- **الفشل مغلق:** `ArumaURL` غير صالح، أو factory موجود مسبقًا: `ArumaRedirectException` عند الإقلاع. وطلب Auth غير قابل للتحويل الآمن (منفذ غير 443، بيانات اعتماد، `..`) يُرفض بـ `IOException`.
- **التحقق (تشغيل واحد، 2026-10-18):** تطبيق التوافق، هدف `aruma-prod` لـ Android (مكتبة حقيقية + `ArumaURL`، بلا `useAuthEmulator`)، على AVD `aruma-a34` (API 33)، 73 طلب Auth كلها وصلت إلى Aruma، **12/12 ناجحة**.
  الفرق الوحيد عن Firebase iOS: `providerId` هو `null` (لا `password`)، كما في وضع المحاكي. وتفاصيلها في `aruma-conformance/results/ARUMA-REPORT.md`.
  https لم يُجرَّب (يحتاج خادمًا بعنوان https وCA في مخزن المحاكي).

## المرحلة 3: التوصية

### الآلية: مكتبة `aruma-redirect` لـ Android

| | |
|---|---|
| الطبقة | `URL.setURLStreamHandlerFactory` بمعالج https يحوّل العائلات الثلاث، ويمرّر الباقي لمعالج المنصة |
| التثبيت | `ContentProvider` في مانيفست المكتبة (`exported=false`، و`initOrder` أعلى من 100، قيمة FirebaseInitProvider في المانيفست المدموج، فيعمل قبلها) |
| الإعداد | `<meta-data android:name="ArumaURL" android:value="https://aruma.example.com"/>` في `<application>` التطبيق |
| ما يضيفه المطوّر | اعتمادية Gradle + سطر meta-data + `google-services.json` من Aruma. **لا كود** |
| Flutter | الاعتمادية في `android/app/build.gradle`، أو إضافة pub بلا واجهة Dart تضيف الـ AAR. وسطر meta-data في `AndroidManifest.xml` |
| الأمان | كـ ArumaRedirect: https وحده إلا للعناوين المحلية، والسجل اختياري بلا استعلام ولا أجسام، و**الفشل مغلق** |

**الإيجابيات:**
- واجهة Java عامة منذ JDK 1.0، لا انعكاس (reflection)، ولا تعديل لكود Google، ولا خطّاف.
- تغطي العملية كلها، وتعمل في Kotlin وJava وFlutter.
- تطابق آلية iOS والويب: تبديل النطاق وحده، وAruma يخدم المسارات نفسها (ADR-014).

**السلبيات:**
- **factory واحد لكل عملية:** المحاولة الثانية ترمي `Error`.
  - نادر في التطبيقات الحديثة، لكن بعض المكتبات القديمة تثبّت factory (مثل `ObsoleteUrlFactory` في okhttp-urlconnection).
  - العلاج: الفشل المغلق. إن تعذّر التثبيت ووُجد `ArumaURL`، يتوقف التطبيق عند الإقلاع برسالة واضحة، فلا تذهب كلمة مرور إلى Google.
- **مربوطة بالنقل الحالي:** لو انتقلت firebase-auth إلى OkHttp أو Cronet أو gRPC، لن يراها الـ factory.
  - الطلب حينها يذهب إلى Google بمفتاح Aruma فيُرفض، **لكن كلمة المرور تصل إلى Google**.
  - العلاج: تثبيت إصدار Firebase، واختبار التوافق في CI على كل ترقية بحارس شبكة كهذا.
- العناوين المعروفة تفصيل داخلي، وقد تتغير. العلاج: مطابقة واسعة لنطاقات Auth، والفشل المغلق لأي نطاق Auth غير معروف.

### الخطورة والمراجعة

- **Google Play:** واجهات عامة، ولا سلوك مقيّد. على المطوّر أن يصرّح في Data safety بأن البيانات تذهب إلى خادمه، وهذا صحيح أصلًا.
- **R8/ProGuard:** المكتبة تحتاج قواعد keep لصنفيها فقط، ولا يُمسّ كود Firebase. لم يُجرَّب بناء release.
- **حدود التجربة:**
  - محاكي واحد بـ API 33، ولم يُجرَّب جهاز حقيقي ولا إصدارات Android أخرى.
  - التدفقات: البريد والمجهول وتحديث الرمز والملف الشخصي. ولم تُجرَّب تدفقات Play services المذكورة في (هـ).
- **Firestore وRTDB (لاحقًا):** Firestore على Android يستخدم gRPC، وRTDB يستخدم WebSocket، فلا يمرّان بـ `URLStreamHandler`. لكل منهما قرار مستقل.

### البدائل (لو ظهر لاحقًا أن طلبًا يخرج من Play services أو بنقل آخر)

1. **وضع المحاكي مع وكيل https داخل التطبيق:** `useEmulator` على `127.0.0.1` ووكيل محلي يرسل إلى Aruma بـ https.
   يعمل اليوم (11/12)، لكنه سطر كود، ويُبقي المكتبة في وضع المحاكي.
2. **إعادة كتابة وقت البناء** (تحويل bytecode لمكتبة Google بإضافة Gradle): يعدّل كود Google المُوزَّع داخل التطبيق.
   **قرار قانوني للمالك**، ولم يُجرَّب ولم يُبنَ.

### الخطوات التالية

1. ~~إصلاح `requestType` الرقمي في Aruma~~: تم على main، وصار 12/12.
2. تشغيل واحد على Firebase الحقيقي لـ Android، لحسم فرق `providerId`.
3. ~~بناء `sdk/android/aruma-redirect` وهدف `aruma-prod` لـ Android~~: تم على http (12/12). **بقي https** (CA في مخزن المحاكي).
4. تحديث ADR-013: Android يُدعم في الإنتاج بالمكتبة، والمحاكي للتطوير.

## إعادة الإنتاج

في `aruma-conformance`:

```bash
node tools/aruma_seed.mjs --client android --url http://localhost:8094 --public-url http://10.0.2.2:8094 --setup-code <code>
tools/run_android.sh aruma                                   # وضع المحاكي
node tools/aruma_seed.mjs --target aruma-prod-discovery --client android --url http://localhost:8094 --public-url http://10.0.2.2:8094
tools/run_android.sh apk aruma-prod-discovery                # APK الإنتاج، بلا تشغيل
# frida-compile tools/frida-android/src/{observe,watch,rewrite,factory}.js، ثم spawn للتطبيق بأحدها
tools/run_android.sh collect aruma-prod-discovery <name>     # ← results/results-<name>-flutter-android.json
```

- النتائج: `results-aruma-flutter-android.json`، و`results-aruma-prod-{discovery,frida,factory}-flutter-android.json`، والتفاصيل في `results/ARUMA-REPORT.md`.
