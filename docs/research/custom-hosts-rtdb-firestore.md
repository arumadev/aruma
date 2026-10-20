# RTDB وFirestore في وضع الإنتاج: عنوان مخصص بلا مكتبة تحويل

- **الحالة:** بحث مكتمل، 2026-10-20. مدخل لجلسة الأسئلة قبل RTDB، ولقرار `databaseURL` المؤجَّل في ADR-007.
- **السؤال:** هل تتصل مكتبات RTDB وFirestore الرسمية بعنوان Aruma مباشرة في وضع الإنتاج (لا `useEmulator`)، بواجهة عامة، دون مكتبة تحويل كالتي احتاجها Auth؟
- **المنهج:**
  - كود المكتبات المفتوح على GitHub، فرع `main` في 2026-10-20: firebase-js-sdk `d7bb0e16f2`، وfirebase-android-sdk `f71ec348f8`، وfirebase-ios-sdk `dc5821fd8c`، وflutterfire `31dd9ba4d9`.
  - ومراجع Firebase الرسمية.
  - لا فكّ لأي كود، ولا اتصال بمشروع Firebase.

## الجواب المختصر

**نعم، في المكتبات الأربع، مع شروط.** بخلاف Auth الذي عناوينه ثابتة في الكود:
- **RTDB** يأخذ عنوانه من ملف الإعداد (`databaseURL`)، فلا يحتاج سطرًا في الكود.
- **Firestore** له إعداد `host` رسمي موثّق في المراجع، ويحتاج سطرًا واحدًا.

| المنتج | الويب | Android | iOS | Flutter |
|---|---|---|---|---|
| RTDB | نعم بشروط: ملف الإعداد | نعم: ملف الإعداد | نعم بشروط: ملف الإعداد | نعم: يمرّر العنوان إلى المكتبة الأصلية |
| Firestore | نعم: `initializeFirestore(app, {host, ssl})` | نعم: `setHost` و`setSslEnabled` | نعم بشروط: `settings.host` | نعم بشروط: `Settings(host:, sslEnabled:)` |

## Realtime Database

**كيف تقرأ كل مكتبة العنوان:**
- **الويب**، `packages/database/src/core/util/libs/parser.ts` (السطور 104-191):
  - اسم القاعدة (namespace) هو أول جزء من النطاق إذا كان للنطاق 3 أجزاء أو أكثر، و`?ns=` يتقدّم عليه دائمًا.
  - النطاق ذو الجزأين (`example.com`) بلا `?ns=` خطأ قاتل («Cannot parse Firebase url»)، عدا `localhost`.
  - بلا منفذ صريح يكون الاتصال آمنًا دائمًا (https وwss).
  - المسار غير الجذري مرفوض.
- **Android**، `firebase-database/.../core/utilities/Utilities.java` (السطور 39-86): الاسم من `?ns=`، وإلا فأول جزء من النطاق، لأي نطاق.
- **iOS**، `FirebaseDatabase/Sources/Utilities/FUtilities.m` (السطور 147-217):
  - **النطاق ذو الأجزاء الثلاثة بالضبط يأخذ أول جزء ويتجاهل `?ns=`** (السطور 183-185).
  - والنسخة تُخزَّن بمفتاح النطاق والمسار (`FIRDatabaseComponent.m` 131-134)، فـ `?ns=a` و`?ns=b` على النطاق نفسه تعطيان النسخة نفسها.
- **النقل:** `wss://<host>/.ws?v=5&ns=<ns>`، والاستطلاع الطويل في الويب `https://<host>/.lp?`. وللنطاقات غير `firebaseio.com` تُلحق المكتبات `ns=` دائمًا.
- **حقل `h` في المصافحة:** يستبدل الخادمُ به النطاقَ الداخلي، والنطاق الذي يبدأ بـ `s-` يُخزَّن دائمًا. على Aruma أن يرسل نطاقه هو، بلا `s-`.
- **الدخول:** رمز المستخدم ورمز App Check يُرسلان داخل WebSocket نفسه (`auth` و`appcheck`)، فلا تتصل RTDB بأي عنوان Google غير عنوان القاعدة. وتجديد الرمز عمل Auth.
- **التوثيق:** المراجع تقول إن `url` هو «عنوان نسخة Realtime Database». لكن الأمثلة كلها على `*.firebaseio.com`، فالنطاق الخارجي يعمل في الكود ولا يذكره التوثيق صراحة.

**أثر ذلك على `databaseURL` في Aruma (ADR-007):**
- الصيغة المبدئية `<public_url>/?ns=<project_id>-default-rtdb` تنكسر على iOS حين يكون `public_url` بثلاثة أجزاء. مثلًا `https://aruma.example.com/?ns=chat-app-default-rtdb`: الويب وAndroid يأخذان `chat-app-default-rtdb`، وiOS يأخذ `aruma`.
- **الحلول الممكنة:**
  - نطاق فرعي لكل قاعدة: `https://chat-app-default-rtdb.rtdb.example.com`، أي أربعة أجزاء، فتتفق المكتبات كلها على أول جزء.
  - أو أن يتسامح الخادم مع الاسم الخاطئ في حالة iOS.
- **القرار في جلسة RTDB.** ومعه ملاحظة ADR-007 عن مكتبة Admin لـ Go.

## Firestore

- **الإعداد رسمي وموثّق، ومستقل عن المحاكي:**
  - الويب: «host: The hostname to connect to» في [FirestoreSettings](https://firebase.google.com/docs/reference/js/firestore_.firestoresettings).
  - Android: «Sets the host of the Cloud Firestore backend» في [FirebaseFirestoreSettings.Builder](https://firebase.google.com/docs/reference/android/com/google/firebase/firestore/FirebaseFirestoreSettings.Builder).
  - iOS: [FirestoreSettings](https://firebase.google.com/docs/reference/swift/firebasefirestore/api/reference/Classes/FirestoreSettings).
  - استدعاء المحاكي في الويب يكتب فوق `host` نفسه.
- **ما يجب أن يتكلّمه الخادم:**
  - **الويب:** WebChannel على `<base>/google.firestore.v1.Firestore/{Listen,Write}/channel`، وREST على `<base>/v1/…`.
  - **Android:** gRPC فوق HTTP/2، وTLS افتراضيًا.
  - **iOS:** gRPC C++ فوق HTTP/2 وTLS، **بقائمة شهادات جذرية مدمجة في المكتبة** (130 جذرًا مشتقة من Mozilla، منها ISRG Root X1 الخاص بـ Let's Encrypt). الشهادة الخاصة أو الموقّعة ذاتيًا تفشل إلا بإطفاء SSL.
- **المسار:** gRPC يقبل `host[:port]` فقط. فلا يصلح `example.com/firestore`، وFirestore يحتاج نطاقًا أو منفذًا خاصًا به.
- **Flutter على الويب:** يُسقط `host` بصمت إن لم يُضبط `sslEnabled` معه.
- **Android:** يستدعي `ProviderInstaller` من Play services محليًا قبل الاتصال، وهذا ليس طلبًا إلى Google.

## ما لا يغطيه إعداد العنوان

- تجديد رمز Auth: `securetoken.googleapis.com`. تغطيه مكتبات التحويل الحالية.
- App Check، إن ثُبّتت مكتبته.
- Installations: تستعمله Analytics وMessaging وRemote Config، لا RTDB ولا Firestore.
- هذه الأسماء من المعرفة العامة، ولم تُفحص في الكود في هذا البحث.

## الخلاصة لـ Aruma

- **RTDB:** لا يحتاج مكتبة تحويل. يكفي ملف الإعداد، بشرط أن نختار شكل `databaseURL` بعناية لأجل iOS.
- **Firestore:** لا يحتاج مكتبة تحويل، بل سطرًا واحدًا رسميًا هو `host`. لكنه يحتاج خادم gRPC/HTTP2 بشهادة من جهة عامة على الجوال، وWebChannel على الويب.
- **مكتبات التحويل تبقى لـ Auth وحده.**
