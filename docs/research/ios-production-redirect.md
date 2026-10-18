# iOS في وضع الإنتاج: تحويل طلبات Auth داخل التطبيق (ArumaRedirect)

- الحالة: تجربة مكتملة (spike)، 2026-10-18. فرع `p2-j-ios-redirect`.
- السؤال: هل تعمل مكتبة Firebase الرسمية لـ iOS مع Auth في Aruma **في وضعها العادي**، دون `useEmulator`، بتحويل طلباتها داخل التطبيق؟
- الجواب: **نعم.** 12/12 سيناريو Auth طابقت Firebase، على http وعلى https، وصفر طلب Auth وصل إلى Google.
- المراجع: ADR-013 (الاستثناء 4: iOS)، ADR-014، `phase2-identity-toolkit.md` §0 و§4، والحزمة `sdk/ios/ArumaRedirect`.

## لماذا

`Auth.useEmulator` في iOS يفرض `http` (§0 في `phase2-identity-toolkit.md`)، فلا يصلح للإنتاج. وعناوين Google ثابتة في المكتبة.
لكن Aruma يخدم مسارات Google نفسها تحت نطاقه (ADR-014)، فالتحويل تبديل للنطاق وحده:

| تطلب المكتبة | تذهب إلى |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<ArumaURL>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<ArumaURL>/securetoken.googleapis.com/<path>` |
| `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` | `<ArumaURL>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` |

هذا ليس انتحالًا لنطاق Google (المرفوض في ADR-013): لا DNS ولا شهادات لنطاقات Google. التطبيق نفسه يرسل إلى خادم Aruma بشهادته هو.

## كيف تعمل

1. **`URLProtocol`** لا يقبل إلا العائلات الثلاث: https، والمنفذ الافتراضي، والمسار v3 وحده على `www.googleapis.com`. وكل طلب آخر يمر كما هو.
2. **يعيد كتابة العنوان** ويُبقي الطريقة والترويسات والاستعلام والجسم. والجسم يُقرأ من `httpBodyStream`، فهكذا يصل عادةً إلى URLProtocol.
3. **يرسل الطلب بجلسة URLSession داخلية.** إعدادها يُستبعد منه البروتوكول نفسه، ويُوسم الطلب المحوّل، فلا حلقة.
4. **يعيد للمكتبة الرد والبيانات والأخطاء** بعنوان Google الأصلي. ويُسلّم التحويلات (3xx) للعميل، ويُلغي الطلب الداخلي عند الإلغاء أو انتهاء المهلة. والتحقق من الشهادة بثقة النظام العادية.
5. **التثبيت:**
   - `URLProtocol.registerClass` يصل إلى `URLSession.shared` وحده.
   - فيُعدَّل مصنعا `URLSessionConfiguration.default` و`.ephemeral` ليكون البروتوكول أولًا في كل جلسة.
   - Firebase Auth يرسل عبر GTMSessionFetcher، وهو يبني جلسته من `ephemeralSessionConfiguration`.
6. **صفر كود:** هدف Objective-C صغير فيه `+load` يقرأ `ArumaURL` من Info.plist ويثبّت الحزمة قبل `main`.
   - Swift ليس فيه `+load`.
   - يعمل في SwiftPM (اختبار `testLoaderRanAtLoadTime`).
   - ويعمل في CocoaPods مع `use_frameworks!` (تطبيق Flutter): سجل الجهاز يُظهر `ARUMA_REDIRECT installed` قبل `didFinishLaunching` بـ 0.4 ثانية.
   - في CocoaPods بمكتبات ساكنة، يضمن ربطها العلم `-ObjC` الذي يضيفه CocoaPods.
   - وللتثبيت بالكود: `ArumaRedirect.install(url:)`.

## ما يضيفه المطوّر

| المنصة | الإضافة |
|---|---|
| Xcode / SwiftPM | الحزمة (المنتج `ArumaRedirect`) + `ArumaURL` في Info.plist |
| CocoaPods وFlutter | سطر `pod 'ArumaRedirect', …` في `ios/Podfile` + `ArumaURL` |
| ملف الإعداد | `GoogleService-Info.plist` من Aruma كما هو |
| كود التطبيق | **لا شيء** |

**Flutter:** لا يحتاج إضافة Flutter (plugin)، إذ لا واجهة Dart، وتطبيقات Flutter على iOS تستخدم CocoaPods افتراضيًا.
فسطر Podfile أنظف من غلاف plugin لا يضيف إلا قوالب. وفي وضع SwiftPM في Flutter تُضاف الحزمة من Xcode.
في تطبيق التوافق، السطر مشروط بـ `ARUMA_REDIRECT_PATH`، حتى لا يتغير بناء الهدفين `firebase` و`aruma`.

**الأمان في الحزمة:**
- https وحده، إلا لـ localhost و`127.0.0.0/8` و`::1`، أو مع `ArumaAllowInsecureURL` للتطوير.
- **تفشل مغلقة:** إن وُجد `ArumaURL` ورُفض، تفشل طلبات Auth ولا تذهب إلى Google بكلمة مرور المستخدم.
- غياب المفتاح يعني أن الحزمة لا تفعل شيئًا.
- السجل لا يحوي الاستعلام (فيه مفتاح API) ولا الترويسات ولا الرموز ولا الأجسام.

## النتائج

تطبيق التوافق (`aruma-conformance`، الهدف الجديد `aruma-prod`):
- Flutter على محاكي iPhone 16 Pro Max (iOS 18.5)، وFirebaseAuth 12.19.0.
- خادم بقاعدة جديدة `aruma_prodspike`.

| السيناريو | Firebase | aruma-prod http | aruma-prod https |
|---|---|---|---|
| auth.sign_up_email | pass | pass | pass |
| auth.sign_up_duplicate_email | pass | pass | pass |
| auth.sign_up_weak_password | pass | pass | pass |
| auth.sign_in_email | pass | pass | pass |
| auth.sign_in_wrong_password | pass | pass | pass |
| auth.sign_in_anonymous | pass | pass | pass |
| auth.token_refresh | pass | pass | pass |
| auth.update_display_name | pass | pass | pass |
| auth.update_password | pass | pass | pass |
| auth.send_password_reset | pass | pass | pass |
| auth.delete_user | pass | pass | pass |
| auth.sign_out | pass | pass | pass |
| auth.sign_in_google | skip | skip | skip |

- `compare/compare.mjs`: **12 متطابق، 0 مختلف** في التشغيلين، بالقيم المرصودة كاملة: الأخطاء، ومحتوى الرمز، والعمر 3600.
- النتائج في `results-aruma-prod-flutter-ios.json` و`results-aruma-prod-https-flutter-ios.json`.
- Firestore وRTDB وRemote Config متخطّاة، فهي لم تُبنَ بعد.

## دليل الطلبات

من سجل الجهاز في تشغيل http. وتشغيل https مثله، والوجهة `https://localhost:8443`:

```
ARUMA_REDIRECT installed: Firebase Auth requests go to http://localhost:8092
ARUMA_REDIRECT POST https://www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser -> http://localhost:8092/www.googleapis.com/identitytoolkit/v3/relyingparty/signupNewUser
ARUMA_REDIRECT POST https://www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo -> http://localhost:8092/www.googleapis.com/identitytoolkit/v3/relyingparty/getAccountInfo
ARUMA_GUARD blocked https://firebaseinstallations.googleapis.com/v1/projects/chat-app/installations/
```

| العنوان الإنتاجي الذي طلبه SDK | العدد | وصل إلى Aruma |
|---|---|---|
| `www.googleapis.com/.../relyingparty/getAccountInfo` | 28 | 28 |
| `.../verifyPassword` | 15 | 15 |
| `.../signupNewUser` | 13 | 13 |
| `.../deleteAccount` | 11 | 11 |
| `.../setAccountInfo` | 2 | 2 |
| `.../getOobConfirmationCode` | 2 | 2 |
| `securetoken.googleapis.com/v1/token` | 2 | 2 |
| **المجموع** | **73** | **73** |

- حارس التطبيق (`LoopbackGuard`) يرفض أي طلب إلى غير localhost ويسجّله.
  - رأى الطلبات المحوّلة الـ 73 متجهة إلى localhost.
  - ورفض طلبًا واحدًا فقط، إلى `firebaseinstallations.googleapis.com`، وليس من Auth، كما في وضع المحاكي.
  - **طلبات Auth إلى Google: صفر.**
- سجل الخادم (أو وكيل TLS بـ HTTP/2) يحوي المسارات الـ 73 نفسها.
- **https:**
  - وكيل TLS محلي أمام Aruma على `https://localhost:8443`، و`public_url` للخادم هو العنوان نفسه.
  - شهادة `localhost` من CA مؤقت أُضيف بـ `xcrun simctl keychain booted add-root-cert`، وحُذف مفتاحه الخاص بعد الإصدار.
  - في الإنتاج تكفي شهادة عامة عادية لنطاق Aruma.

## المخاطر

1. **تغيّر SDK:** العناوين تفصيل داخلي.
   - FirebaseAuth 12 ما زال على v3 لأغلب الطلبات.
   - إن انتقل إلى نطاق أو مسار جديد، يمر الطلب دون تحويل إلى Google بمفتاح Aruma، فيفشل (مفتاح غير معروف) ولا يتسرّب شيء إلى Aruma.
   - **لكن كلمة المرور تصل إلى Google حينها.** العلاج:
     - توسيع المطابقة لتشمل نطاقات `*.googleapis.com` الخاصة بـ Auth.
     - تثبيت إصدار Firebase.
     - تشغيل اختبارات التوافق في CI قبل كل ترقية.
2. **مراجعة App Store:**
   - `URLProtocol` وتبديل الدوال (swizzling) على `URLSessionConfiguration` واجهات عامة، تستخدمها أدوات معروفة (مراقبة الشبكة، المحاكاة).
   - لا API خاصة، فلا سبب معروف للرفض.
   - لكن على المطوّر أن يصرّح بأن بيانات المستخدم تذهب إلى خادمه في «App Privacy»، وهذا صحيح أصلًا.
3. **Firestore/gRPC (المراحل التالية):**
   - Firestore على iOS يستخدم gRPC-C++ بمقابسه الخاصة، لا URLSession، فلا يراه أي URLProtocol.
   - تحويله يحتاج آلية أخرى: `settings.host` في Firestore (موثّق ويقبل TLS)، أو طبقة في Aruma.
   - RTDB يستخدم WebSocket (SocketRocket أو `NSURLSessionWebSocketTask`)، وURLProtocol لا يعترض WebSocket، فيحتاج دراسة.
   - Storage وFunctions وRemote Config على URLSession، فالآلية نفسها تصلح لها.
4. **الجلسات الخلفية:**
   - `URLSessionConfiguration.background` لا يقبل بروتوكولات مخصّصة.
   - Auth لا يستخدمها، لكن رفع الملفات في الخلفية (Storage) يستخدمها، فلن يُحوَّل.
5. **ترتيب البروتوكولات:**
   - أي مكتبة أخرى تضع URLProtocol خاصًا بها أولًا قد تسبق الحزمة.
   - حارس تطبيق التوافق احتاج أن يتنحّى للحزمة صراحةً.
6. **التجربة على المحاكي وحده:**
   - لم تُجرَّب على جهاز حقيقي، ولا مع SwiftPM داخل تطبيق iOS (جُرّب SwiftPM باختبارات macOS).
   - ولا مع CocoaPods بمكتبات ساكنة.

## التوصية

- **اعتماد ArumaRedirect طريقًا لـ iOS في الإنتاج:**
  - يحقق «صفر كود»: حزمة ومفتاح في Info.plist، وهو أقرب إلى وعد U-04 من `useEmulator`.
  - ويعمل على https، بخلاف `useEmulator` الذي يفرض http.
- **يُحدَّث ADR-013 (الاستثناء 4):** iOS يُدعم بالحزمة، لا بـ `useEmulator`. وتُنقل الحزمة إلى طبقات الجوال في المرحلة 5، مع:
  - اختبار توافق في CI على إصدار Firebase المثبّت، وإنذار عند ظهور عنوان Auth جديد غير محوَّل.
  - توسيع المطابقة لنطاقات Auth الأخرى (إن ظهرت)، وإبقاء الفشل مغلقًا.
  - التجربة على جهاز حقيقي، ومع SwiftPM في تطبيق iOS، ونشر الحزمة (وسم git وCocoaPods trunk).
- **لا تُعمَّم الآلية على Firestore وRTDB:** لكل منهما قرار مستقل في مرحلته.
