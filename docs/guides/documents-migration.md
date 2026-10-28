# نقل Firestore في تطبيقك إلى قاعدة المستندات في Aruma

تبقى مكتبات Firebase الرسمية (Cloud Firestore) كما هي، وتبدّل **المضيف** وحده. هذا الدليل لمن يستعمل Firestore في مشروع حي وينقله إلى Aruma: توجيه المكتبات، ثم الفهارس والقواعد وTTL، ثم البيانات، ثم اللوحة. والخدمة تسمى في Aruma **Document Database (قاعدة المستندات)**، وتحتها «Firestore-compatible»؛ وتبقى أسماء البروتوكول عند Google (`google.firestore.v1` و`firestore.googleapis.com` في المكتبات و`service cloud.firestore` في القواعد). وسلوكها مطابق لما سُجّل من الإنتاج، وما يخالفه مكتوب في [docs/compat/documents-v0.4.md](../compat/documents-v0.4.md) وADR-027 وADR-028.

**علامات هذا الدليل:** ما لم يُشغَّل في اختبارات المستودع مكتوب عليه «**غير مُتحقَّق**». وما ثبت بأداة أو مكتبة رسمية على خادم حي مكتوب مصدره.

## ما يعمل

| الميزة | الحالة |
|---|---|
| الويب (`firebase` 11 و12 و13): الكامل (WebChannel) وlite (REST) | مدعومة، بـ `@aruma/web` (أدناه). المتصفح الحي: مجموعة F6 |
| Admin Node (`firebase-admin`) وAdmin Go (`cloud.google.com/go/firestore`) | مدعومتان على gRPC وREST (`preferRest`)، `test/smoke/admin-node` و`test/smoke/admin-go/documents` |
| Android وiOS وFlutter | النقل مُجرَّب على Android 27.0.1 وiOS 12.19.3 (ADR-027 §2). **مجموعاتها الكاملة في F8، غير مُتحقَّقة بعد**؛ وFlutter يعتمد المكتبات الأصلية نفسها |
| الأداة `firebase` | `deploy --only firestore:indexes` و`firestore:rules`، و`firestore:indexes`، و`firestore:databases:list` و`get`، و`firestore:delete -r`: تثبتها `TestDocumentsWithTheFirebaseCLI` بـ firebase-tools 15.34.0 تحت حارس الشبكة |
| الفهارس المركّبة، واستثناءات الحقل الواحد، وTTL | مدعومة، وتُصدَّر وتُنشر بلا فقد (الاختبار نفسه) |
| القواعد (`service cloud.firestore`) | مدعومة، [docs/compat/rules-v0.3.md](../compat/rules-v0.3.md) |
| قاعدة بيانات اسمها غير `(default)` | **غير مدعومة:** لكل مشروع `(default)` وحدها، وغيرها `NOT_FOUND` (ADR-028 §6) |
| `find_nearest` بفهرس متجه مطابق، وQuery Explain، وPITR، والنسخ الاحتياطي، وتصدير GCS واستيراده | `UNIMPLEMENTED` برسالة تسمّي الميزة (ADR-028 §6) |
| مشغّلات Firestore وExtensions | غير موجودة (تحتاج بيئة Functions) |

## 1. توجيه المكتبات إلى Aruma

كل مكتبة تبني عنوانها من `host` و`ssl` في إعداداتها (وتضمّن `firestore.googleapis.com` افتراضيًا). فالتوجيه تبديل المضيف **بوضع الإنتاج** لا بدوال المحاكي. ويحتاج الإنتاج **شهادة TLS من جهة عامة** على وكيل أمام Aruma (Caddy مثلًا) يكلّم Aruma بـ h2c: iOS لا يثق إلا بجهات عامة، فشهادة موقّعة ذاتيًا أو من جهة خاصة تعني أنه لا يتصل (ADR-027 §7). ومقطع الوكيل في [rtdb-operator.md](rtdb-operator.md).

**الويب** (الحزمة `@aruma/web`، وتفاصيلها في [web-auth.md](web-auth.md) و`sdk/web/aruma-web/README.md`، قسم Cloud Firestore): لا تغيير في كود التطبيق.

```js
// vite.config: بإضافة firestore: true يُستبدل مضيف Firestore وقت البناء
plugins: [aruma({ url: 'https://aruma.example.com', firestore: true })],
```

وبلا أداة بناء تصلح (CDN مثلًا) تُرفض الطلبات؛ استورد `firebase/firestore` من npm. وإن تعذّرت إعادة الكتابة وقت البناء فاستعمل إعدادات المكتبة نفسها:

```js
import { initializeFirestore } from 'firebase/firestore';   // أو 'firebase/firestore/lite'
import { arumaFirestoreSettings } from '@aruma/web/firestore';
const db = initializeFirestore(app, arumaFirestoreSettings('https://aruma.example.com'));
```

Next.js وwebpack وMetro وFlutter web لها نظائرها في README الحزمة. ولا `connectFirestoreEmulator`.

**Android وiOS وFlutter** (غير مُتحقَّق في مجموعاتها، والأسماء من وثائق المكتبات): ملفا `google-services.json` و`GoogleService-Info.plist` لا يحملان مضيف Firestore، فاضبطه في الكود قبل أول استعمال:

```kotlin
// Android
firestore.firestoreSettings = firestoreSettings { host = "aruma.example.com"; isSslEnabled = true }
```
```swift
// iOS
let s = FirestoreSettings(); s.host = "aruma.example.com"; s.isSSLEnabled = true
Firestore.firestore().settings = s
```
```dart
// Flutter
FirebaseFirestore.instance.settings = const Settings(host: 'aruma.example.com', sslEnabled: true);
```

أما على جهازك للتطوير بلا TLS فـ `isSslEnabled = false` (ADR-027 §7): وAndroid يحتاج `network_security_config` يسمح بـ `http` للمضيف. والمستخدم النهائي يدخل بـ Aruma Auth، ورمز ID يرافق كل طلب فتحكمه القواعد.

**Admin Node:** بـ TLS يعمل مفتاح حساب الخدمة دون تعديل (يرسل `firebase-admin` توقيعًا ذاتيًا يقبله Aruma، ADR-029 §2). ما يلي مأخوذ من `test/smoke/admin-node/documents.test.mjs` إلا `ssl: true`:

```js
initializeApp({ credential: cert('service-account.json'), projectId });
getFirestore().settings({ host: 'aruma.example.com', ssl: true });   // preferRest: true للـ REST
```

مع جهة خاصة اضبط `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` (grpc-js يتجاهل `NODE_EXTRA_CA_CERTS`). وبلا TLS (`ssl: false`) يرسل gRPC النص `Bearer owner` لا المفتاح، فلا يُقبل إلا على خادم تطوير loopback فيه `documents.dev_owner_token` (ADR-029 §6). **Admin Go:** `firestore.NewClient(ctx, project, option.WithEndpoint("aruma.example.com:443"), option.WithCredentialsFile("service-account.json"))`، أو `FIRESTORE_EMULATOR_HOST` للتطوير المحلي (h2c). Admin Python وJava وغيرهما: **غير مُتحقَّق**.

لا تستعمل `FIRESTORE_EMULATOR_HOST` على خادم حقيقي: يضع المكتبة في وضع المحاكي فترسل `owner`، وAruma يرفضه خارج loopback.

## 2. الفهارس: من المشروع الحي إلى Aruma

الفهارس المركّبة التي أنشأتها بنفسك، **وتلك التي أنشأها زر «إنشاء الفهرس» في رابط خطأ console، كلها فهارس مركّبة عادية** في الإنتاج، فتصدّرها الأداة معها. وفي Aruma الاستعلام الذي يحتاج فهرسًا غير موجود يُرفض بخطأ الإنتاج ونص رابطه (`documents.index_enforcement: enforce`، وهو الافتراضي)، فانقل الفهارس **قبل** أن يستعمل تطبيقك Aruma.

```sh
# 1) من المشروع الحي (دخولك العادي، بلا متغيرات Aruma في البيئة)
firebase firestore:indexes --project <live> > firestore.indexes.json
# 2) إلى Aruma (البيئة في القسم التالي)
firebase deploy --only firestore:indexes --project <aruma-project>
```

يحمل الملف `indexes` (المركّبة بنطاقيها COLLECTION وCOLLECTION_GROUP، وarray-contains، والمتجهات) و`fieldOverrides` (استثناءات الحقل الواحد، وحقول TTL بـ `"ttl": true`). ونشر الملف المصدَّر ثانية لا يغيّر شيئًا: يثبته الاختبار على ملف فيه مركّبة ومتجه واستثناءان وTTL، فلا فقد ولا فرق. ويُبنى كل فهرس في الخلفية (`CREATING` ثم `READY`)، واستعلام يحتاجه قبل ذلك يُرفض كما في الإنتاج. الحدّان: 1000 فهرس مركّب و1000 استثناء لكل قاعدة (`documents.limits`، `docs/configuration.md`).

**غير مُتحقَّق:** تصدير مشروع حي كبير بكل أشكال الفهارس (وضع Datastore، ونطاقات أخرى). إن رُفض سطر فالرد يسمّيه؛ أخبرنا به.

الأداة لا تصل إلى Aruma إلا بضبط البيئة وحدها (رمز CLI من اللوحة: حسابي ← رموز CLI)، وهذا ما يضبطه الاختبار:

```sh
A=https://aruma.example.com
export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
  FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A FIRESTORE_URL=$A \
  FIREBASE_TOKEN=<aruma_pat_…> FIREBASE_SKIP_INFORMATIONAL_IAM=1
```

والقاعدة `(default)` موجودة لكل مشروع، فلا تُنشأ: وإنشاء قاعدة بالأداة (`POST databases`) `UNIMPLEMENTED`. ولا يدخل `firebase.json` إلا `"firestore": {"rules": "firestore.rules", "indexes": "firestore.indexes.json"}` لقاعدة `(default)`.

## 3. القواعد

انسخ `firestore.rules` من مشروعك (أو نزّلها بـ `firebase init firestore` على المشروع الحي) وانشرها: `firebase deploy --only firestore:rules --project <aruma-project>`، أو من شاشة «القواعد» في اللوحة (فيها فحص قبل الحفظ وساحة تجريب). والقاعدة الجديدة **مقفلة** حتى تنشر قواعدك، ومكتبات Admin ورمز CLI تتجاوز القواعد. ونشر القواعد المقروءة من الخادم ثانية لا يُنشئ مجموعة قواعد جديدة (يثبته الاختبار). اللغة وحدودها وما يختلف: [rules-v0.3.md](../compat/rules-v0.3.md). **غير مُتحقَّق:** قواعدك أنت؛ جرّبها في الساحة قبل التحويل.

## 4. TTL

يبقى التعريف في `fieldOverrides` (`"ttl": true` لحقل Timestamp واحد في كل مجموعة، كما في Firebase)، فيصل بنشر ملف الفهارس نفسه، أو من شاشة TTL في اللوحة. ثم يمرّ كنّاس يحذف المستندات التي فات وقتها، كل حذف Commit عادي فيراه المستمعون. وهو **كل ساعة** افتراضيًا (`documents.limits.ttl_sweep_interval`)، فالحذف قد يتأخر حتى ساعة بعد الوقت (والإنتاج يعد «عادة خلال 24 ساعة»)؛ ولا يحذف مستندًا حقله ليس Timestamp، ولا المجموعات الفرعية. يثبت الاختبار أن مستندًا منتهيًا حُذف وأن مستندًا لاحقًا وآخر حقله نص بقيا (الكنّاس مضبوط بثانية في الاختبار).

## 5. البيانات

ما هو موجود اليوم، بصدق:

- **لا استيراد مُدار** لتصدير GCS (`gcloud firestore export`)، ولا تصدير: كلاهما `UNIMPLEMENTED`، وحكمه في ADR-028 «أبدًا» (بنية Google Cloud). ولا أداة نقل جاهزة للبيانات (مؤجَّلة، ADR-028 §3).
- **النسخ بسكربت بمكتبات Admin الرسمية** (غير مُتحقَّق كنقل كامل؛ تدعم Aruma كل ما يلزمه: `listCollections` و`getAll` والاستعلامات و`bulkWriter` و`BatchWrite`): اقرأ من المشروع الحي بمفتاحه، واكتب في Aruma بمفتاح مشروعه، بتطبيقين `initializeApp(..., name)`. فيه ثلاثة أمور: مراجع المستندات (`DocumentReference`) تحمل اسم المشروع، فأعد بناءها على قاعدة Aruma؛ والمجموعات الفرعية تُنزَل بـ `listCollections()` على كل مستند، ومنها مستندات «غائبة» بلا حقول (`show_missing`، وAruma يدعمها)؛ وTimestamp بدقة النانو يُنقل كما هو، لكن `serverTimestamp()` لا يُستعمل في النسخ.
- **المستندات التي يحذفها TTL** يُرتَّب ألا تُنسخ منتهية، وإلا حذفها الكنّاس بعد ساعة.
- **دون توقف:** انسخ، ثم وجّه التطبيقات إلى Aruma. لا مزامنة مستمرة بين الاثنين.

وصحة النسخ تفحصها بعدّ المستندات (`count()`) في الجهتين، وعيّنة من القيم.

## 6. اللوحة

في اللوحة المشروع ← **قاعدة المستندات** (Document Database، `/project/documents`): متصفح البيانات (المجموعات والمستندات والحقول بأنواعها، إضافة وتعديل وحذف، وحذف متكرر)، ثم القواعد، والفهارس (المركّبة والاستثناءات)، وTTL، والاستعمال. وكتابة اللوحة كتابة مدير تتجاوز القواعد كما في Firebase console. **غير مُتحقَّق من هذا الدليل:** مطابقة الشاشات لما في Firebase console؛ تتبع اختبارات F7 للوحة.

## قائمة التحويل

1. فعّل الخدمة (`documents.enabled`، `docs/configuration.md`)، ووكيل TLS بشهادة عامة.
2. انقل الفهارس (القسم 2) وانتظر أن تصير كلها `READY`.
3. انشر القواعد (3) وTTL (4).
4. انسخ البيانات (5).
5. وجّه المكتبات (1)، وجرّب استعلاماتك: ما يرفضه خطأ «The query requires an index» فهرسه ناقص.
