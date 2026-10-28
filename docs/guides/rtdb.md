# ربط تطبيقك بقاعدة البيانات اللحظية في Aruma

يبقى تطبيقك على مكتبات Firebase الرسمية كما هي، ويأخذ رابط قاعدة البيانات (`databaseURL`) من ملف الإعداد الذي يولّده Aruma. هذا الدليل لمن يستعمل Realtime Database في Firebase وينقل تطبيقه إلى Aruma. وجانب الخادم (النطاق والشهادات والوكيل والمهلات) في [دليل التشغيل](rtdb-operator.md).

## ما يعمل في 0.2

| الميزة | الحالة |
|---|---|
| الويب (`firebase` 11 و12 و13)، Android (BoM 35)، iOS (`FirebaseDatabase` 12) | مدعومة: القراءة والكتابة والاستماع والاستعلامات والمعاملات وonDisconnect والعمل دون اتصال |
| Admin Node (`firebase-admin` 14) وAdmin Go (v4) | مدعومة، بالرابط الإنتاجي وحده (انظر الجدول التالي) |
| أداة `firebase` (`database:*` و`deploy --only database`) | مدعومة، بالرابط الإنتاجي وحده |
| القواعد | اللغة كاملة عدا `getPriority()`، وتُرفض عند الحفظ |
| REST على `/<path>.json` | مدعومة، **بلا البث** (`text/event-stream`) |
| Admin Python | **غير مدعوم:** `google-auth` يوقّع بـ `aud` ثابت على Google، ويرسل رمز Aruma إلى `iamcredentials.googleapis.com` |

التفاصيل، وكل فرق عن Firebase، والحدود: [docs/compat/rtdb-v0.2.md](../compat/rtdb-v0.2.md) وADR-020.

## رابط قاعدة البيانات

تجده في لوحة التحكم (المشروع ← قاعدة البيانات اللحظية ← `databaseURL`، مع زر نسخ)، وفي ملفات الإعداد: `databaseURL` للويب، و`firebase_url` في `google-services.json`، و`DATABASE_URL` في `GoogleService-Info.plist`. وله صيغتان (ADR-018):

| الصيغة | متى | مثال |
|---|---|---|
| **الإنتاج:** مضيف لكل قاعدة | الخادم معه `rtdb.domain` | `https://chat-app-default-rtdb.rtdb.example.com` |
| **التطوير:** `?ns=` على `public_url` | بلا `rtdb.domain`، على جهازك فقط | `http://127.0.0.1:8080/?ns=chat-app-default-rtdb` |

ما يصلح مع كل مكتبة:

| المكتبة | الإنتاج | التطوير (`?ns=`) |
|---|---|---|
| الويب، Android | نعم | نعم. وAndroid يحتاج إذن `http` للمضيف (`network_security_config`)، و`adb reverse` ليصل الجهاز إلى `127.0.0.1` |
| iOS | نعم | على `127.0.0.1` أو `localhost` فقط. على مضيف من ثلاثة أجزاء (`aruma.example.com/?ns=`) تأخذ المكتبة الجزء الأول وتتجاهل `ns` |
| Admin Node، Admin Go | نعم | **لا:** رابط `http` يضعهما في وضع المحاكي، فيرسلان الرمز `owner`، وAruma لا يقبله (Node يعمل مجهولًا، وGo يتلقى 401) |
| أداة `firebase` | نعم | **لا:** يضيع `?ns=` من الطلب، والأداة لا ترسل رمزها إلا إلى https |
| Unity/C++ | نعم | **لا:** لا تعرف `ns` |

**دوال المحاكي** (`connectDatabaseEmulator` في الويب، و`useEmulator` في Android وiOS) غير لازمة: صيغة التطوير في ملف الإعداد تكفي، ولا يستدعيها أي من اختبارات Aruma. وإن استعملتها في تطبيق عميل فهي تبدّل المضيف والمنفذ وتُبقي اسم القاعدة من `databaseURL`. ولا تمرّر `mockUserToken`: Aruma لا يقبل رمزًا غير موقّع.

## الربط من كل منصة

العيّنات مأخوذة من اختبارات تمر على Aruma. القواعد التي تفترضها في [test/e2e/rtdb-rules.json](../../test/e2e/rtdb-rules.json).

**الويب** (Auth عبر `@aruma/web`، انظر [web-auth.md](web-auth.md)؛ وقاعدة البيانات لا تحتاج شيئًا):

```js
const app = initializeApp(firebaseConfig); // ملف Aruma كما هو، و databaseURL فيه
const db = getDatabase(app);               // بلا connectDatabaseEmulator
await set(ref(db, `users/${uid}/profile`), { name: 'سارة' });
await push(ref(db, room), { from, text, ts: serverTimestamp() });
const low = await get(query(ref(db, board), orderByChild('score'), limitToFirst(2)));
```
(من `test/e2e/web-rtdb/src/app.js` و`tests/rtdb.spec.mjs`)

**Android** (`google-services.json` من Aruma، و`firebase-database` من `firebase-bom:35.0.2`):

```java
FirebaseDatabase db = FirebaseDatabase.getInstance();
DatabaseReference me = db.getReference("status").child(uid);
me.onDisconnect().setValue(map("state", "offline", "at", ServerValue.TIMESTAMP));
Query top = db.getReference(board).orderByChild("score").limitToLast(3);
```
(من `test/e2e/android-rtdb/app/src/androidTest/java/com/aruma/e2e/RtdbE2ETest.java`، و`map(...)` فيه يبني `HashMap`)

**iOS** (`GoogleService-Info.plist` من Aruma):

```swift
FirebaseApp.configure()
let db = Database.database()
try await db.reference(withPath: "users/\(uid)/profile").setValue(["name": "سارة"])
let top = db.reference(withPath: board).queryOrdered(byChild: "score").queryLimited(toLast: 3)
```
(من `test/e2e/ios-rtdb/Tests/RtdbE2ETests.swift`)

**Admin Node:** `cert()` في `firebase-admin` يرسل توقيع المفتاح إلى Google ويتجاهل `token_uri`، فاستعمل اعتمادًا يطلب الرمز من Aruma. انسخ [aruma-credential.mjs](../../test/e2e/admin-node/aruma-credential.mjs) إلى مشروعك:

```js
import { arumaCredential } from './aruma-credential.mjs';
const app = initializeApp({ credential: arumaCredential('service-account.json'), projectId, databaseURL });
const db = getDatabase(app);
await db.ref('smoke/a').set({ n: 1, s: 'سلام' });
await db.setRules(rules);
```
(من `test/smoke/admin-node/rtdb.test.mjs`. و`databaseAuthVariableOverride: { uid: 'u1' }` في `initializeApp` يُخضعه للقواعد)

**Admin Go** (مفتاح حساب الخدمة يكفي: `golang.org/x/oauth2` يحترم `token_uri`):

```go
app, err := firebase.NewApp(ctx, &firebase.Config{DatabaseURL: databaseURL}, option.WithCredentialsFile("service-account.json"))
c, err := app.Database(ctx)
err = c.NewRef("go/basic").Set(ctx, map[string]any{"n": 1})
q := c.NewRef("scores").OrderByChild("score").LimitToFirst(3)
```
(من `test/smoke/admin-go/rtdb/rtdb_test.go`. و`AuthOverride` في `firebase.Config` يُخضعه للقواعد)

**أداة `firebase`** (عنوان Aruma على https، ورمز CLI من اللوحة: حسابي ← رموز CLI):

```sh
export FIREBASE_API_URL=https://aruma.example.com FIREBASE_TOKEN_URL=https://aruma.example.com \
  FIREBASE_RTDB_MANAGEMENT_URL=https://aruma.example.com FIREBASE_TOKEN=<CLI token> FIREBASE_SKIP_INFORMATIONAL_IAM=1
firebase database:set /smoke smoke.json --project chat-app --force
firebase database:get /smoke --project chat-app
firebase deploy --only database --project chat-app   # firebase.json: {"database": {"rules": "database.rules.json"}}
```
(من `test/experiments/firebase-cli-rtdb.sh`)

## القواعد

- **القاعدة الجديدة مقفلة** (`{".read": false, ".write": false}`). مكتبات Admin ورمز CLI تتجاوز القواعد، والعملاء لا يصلون إلى شيء حتى تنشر قواعدك.
- **النشر** بإحدى ثلاث: شاشة «القواعد» في اللوحة، أو `firebase deploy --only database`، أو `PUT /.settings/rules.json` برمز مسؤول (مع `?dryRun=true` للفحص دون حفظ). الخطأ يرد بـ `السطر:العمود: الرسالة`، والقواعد السابقة تبقى.
- **`rtdb.dev_open_rules`** (`ARUMA_RTDB_DEV_OPEN_RULES`) للتطوير فقط: يقبله الخادم حين يكون `public_url` و`listen` على loopback، ويعطي العملاء على الجهاز نفسه قواعد مفتوحة. لا تختبر قواعدك به.

**`.indexOn` أهم في Aruma مما تظن.** الاستعلامات تُصفّى على الخادم (ADR-020):

| الترتيب | بلا `.indexOn` | مع `.indexOn` |
|---|---|---|
| بالمفتاح أو بالأولوية | يُصفّى على الخادم | كذلك |
| بقيمة ابن (`orderByChild`) أو بالقيمة (`orderByValue`) | الاستماع: كل الأبناء مع تحذير `no_index` (في الويب «Using an unspecified index…»)، والمكتبة تصفّي بنفسها. `get()`: يفشل بـ «Index not defined, add ".indexOn"…». REST: ‏400 بالنص نفسه | يُصفّى على الخادم، ولا يصل إلا ما في النافذة |

```json
"messages": { ".indexOn": ["ts"], "$id": { ".write": "auth != null" } }
```

## REST

العنوان `<databaseURL>/<path>.json`، أو `http://127.0.0.1:8080/<path>.json?ns=<ns>` في التطوير. الاعتماد كما في `internal/services/rtdb/api/rest_auth.go`:

| أين | ما يُقبل | غير ذلك |
|---|---|---|
| `Authorization: Bearer`، ثم `?access_token=` | رمز مسؤول فقط: رمز حساب خدمة (`aruma_sat_…`، من `token_uri`) أو رمز CLI لمالك المشروع (`aruma_pat_…`). يتجاوز القواعد | 401 «Unauthorized request.»، ورمز ID المستخدم منه |
| `?auth=` | رمز ID لمستخدم Aruma Auth في المشروع، فيعمل الطلب بهويته | **يُتجاهل** (منتهيًا أو مزوّرًا أو غير رمز) ويعمل الطلب مجهولًا: العام يُقرأ، والمحمي 401 «Permission denied» |
| `auth_variable_override=<JSON>` | مع رمز مسؤول فقط: يعمل بذلك المتغير تحت القواعد، و`null` مجهولًا | يُتجاهل بلا رمز مسؤول. وJSON غير صالح 400 |

- أول اعتماد موجود هو الذي يُؤخذ، بالترتيب أعلاه.
- `/.settings/rules.json` لرمز المسؤول وحده: بلا رمز 403، وبرمز مستخدم 401.
- المدعوم: `GET` و`PUT` و`POST` و`PATCH` و`DELETE`، و`print=pretty|silent`، و`shallow=true`، و`format=export`، والاستعلامات (`orderBy="ts"` بعلامتي التنصيص)، و`X-Firebase-ETag: true` مع `if-match` (412 عند الاختلاف) و`if-none-match` (304).
- `writeSizeLimit` يُقبل بقيمه الخمس ولا يوقف شيئًا (الحد عند Aruma `rtdb.limits.rest_write_bytes`)، والقيمة المجهولة 400.
- **لا بث:** `Accept: text/event-stream` يرد 400. للاستماع استعمل مكتبة.

```sh
curl "https://chat-app-default-rtdb.rtdb.example.com/users/$UID.json?auth=$ID_TOKEN"
curl -X PUT -H "Authorization: Bearer $CLI_TOKEN" --data-binary @database.rules.json \
  "https://chat-app-default-rtdb.rtdb.example.com/.settings/rules.json"
```
(الثاني من `put_rtdb_rules` في `test/e2e/server.sh`)

## ما يحسن أن تعرفه

- **الحدود** كحدود Firebase المنشورة (المسار 768 بايت، والعمق 32، والكتابة 16 MB)، ويضبطها المشغّل: [rtdb-v0.2.md](../compat/rtdb-v0.2.md).
- **الأرقام** تبقى كما كُتبت، صحيحًا 64 بت أو عشريًا، كما في الإنتاج: `Long` و`Double` في Android وiOS لا يختلطان.
- **الاستطلاع الطويل** (`/.lp`): متصفح فشل له WebSocket مرة (إعادة تشغيل الخادم مثلًا) يبدأ بعدها بالاستطلاع الطويل. Aruma يخدمه ثم يعيد المتصفح إلى WebSocket، فلا تحتاج `forceWebSockets()`. يكفي أن يمرّر الوكيل `/.lp` وترقية WebSocket.
- **تعطيل المستخدم أو حذفه أو إلغاء جلساته** يقطع استماعاته خلال ثوانٍ (0.5 إلى 3.5 ثانية في الاختبارات).
- **onDisconnect** يُفحص بالقواعد عند تسجيله وعند تنفيذه، بهوية الجلسة لحظة الانقطاع.
- **دون اتصال:** الكتابات تُطبّق محليًا وتصل حين يعود الخادم، وتخزين Android وiOS الدائم (`setPersistenceEnabled` و`isPersistenceEnabled` مع `keepSynced`) يعمل.

## اختبار تطبيقك على Aruma

- **خادم محلي:** اتبع «التشغيل المحلي» في [README](../../README.md)، وأضف `ARUMA_RTDB_ENABLED=true` إلى `.env`. بلا `rtdb.domain` تحمل ملفات الإعداد صيغة التطوير. وللرابط الإنتاجي محليًا (Admin والأداة) يشغّل `test/experiments/rtdb-caddy.sh` الإعداد كاملًا خلف Caddy.
- **أمثلة كاملة:** `test/e2e/web-rtdb` و`android-rtdb` و`ios-rtdb` (سيناريوهاتها في [test/e2e/README.md](../../test/e2e/README.md))، و`test/smoke/admin-node` و`admin-go`.

## حل المشكلات

| العَرَض | السبب | الحل |
|---|---|---|
| «Specify the database» أو «Firebase error. Please ensure that you have the URL…» | اسم القاعدة خطأ، أو الوكيل يغيّر `Host` | انسخ `databaseURL` من اللوحة؛ ويحفظ الوكيل المضيف ([التشغيل §4](rtdb-operator.md)) |
| iOS أو Admin Go لا يجدان القاعدة على `https://aruma.example.com/?ns=…` | صيغة `?ns=` على مضيف عام: iOS يأخذ الجزء الأول، وGo يلصق المسار بالرابط كما هو | الرابط الإنتاجي مع `rtdb.domain` |
| Admin Node يُرفض في كل شيء، أو Admin Go ‏401 | رابط `http`: وضع المحاكي والرمز `owner` | الرابط الإنتاجي على https، و`arumaCredential` في Node |
| `permission_denied` | القاعدة مقفلة افتراضيًا، أو القاعدة لا تسمح | انشر قواعدك، واختبرها بمستخدم حقيقي لا بـ `dev_open_rules` |
| تحذير «Using an unspecified index» أو `get()` يفشل بـ «Index not defined» | لا `.indexOn` للابن المرتّب به | أضف `.indexOn` في المسار الذي تذكره الرسالة |
| REST ‏401 «Unauthorized request.» | رمز ID في `Authorization` أو `access_token` | رمز ID في `?auth=`، أو رمز مسؤول في `Bearer` |
| المتصفح يبقى على الاستطلاع الطويل بعد إعادة التشغيل | الوكيل لا يمرّر ترقية WebSocket أو `/.lp` | مرّر الترويستين `Upgrade` و`Connection` وكل المسارات ([التشغيل §5](rtdb-operator.md)) |
| `listen()` في Admin Python أو `database:profile` يفشل بـ 400 | البث غير مدعوم في 0.2 | اقرأ بـ `GET`، أو استمع بمكتبة Node |
