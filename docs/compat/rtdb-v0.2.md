# توافق Realtime Database: الإصدار 0.2

- **الحالة:** بدأ في R4، 2026-10-21، بقرارات القواعد. وأضاف R5 واجهة REST ومكتبات Admin. يكتمل في R7 بنتائج الأجهزة والمراجعة الأمنية.
- **المرجع السلوكي:** المحاكي الرسمي 4.11.2، بإطارات مسجّلة في `internal/services/rtdb/wire/testdata/golden` (`capture_r4.mjs`)، تعيد تشغيلها `TestRTDBMatchesEmulator` على Aruma. وحيث يختلف الإنتاج عن المحاكي نتبع الإنتاج ونقول ذلك.
- **الإنتاج:** في R7 سُجّلت السيناريوهات نفسها من Firebase الحقيقي (2026-10-21، بإذن المالك) في `golden/production`، وتقارنها `TestProductionGoldenServerFrames` و`TestProductionCancelledQueries` و`TestRTDBRESTMatchesProduction`. النتائج في «الإنتاج والمحاكي» أدناه.

## اختبارات Firebase الرسمية

| الوضع | النتيجة | ملاحظة |
|---|---|---|
| الإنتاج (`databaseURL` بنطاق فرعي وTLS) | 332 من 332 | |
| المحاكي (`?ns=`) | 335 من 336 | الفاشل `resolves timestamps locally`، مصنّف توقيتًا في `classified.json`: الخادم على الجهاز نفسه يرد في الجزء نفسه من الألف من الثانية |

تعمل منذ R4 بلا `dev_open_rules`: الاختبارات تأخذ صلاحيتها من القواعد المفتوحة التي تحمّلها، ويقيّمها محرك القواعد في كل عملية، كما في Firebase.

## قرارات القواعد

1. **القواعد على كل عملية لغير المسؤول.**
   - `g` و`q`: `.read` مع متغير `query` من استعلام الطلب. والاستماع بلا استعلام مرتّب بالأولوية (`orderByPriority`)، كما في المحاكي.
   - `p` و`m`: `.write` و`.validate` على القيم بعد حل قيم الخادم، فالقواعد ترى الطابع الزمني الفعلي.
   - تُفحص داخل معاملة الكاتب، فترى القاعدة البيانات بعد الكتابات التي سبقت الأمر في الطابور.
   - خطأ في قراءة البيانات أثناء الفحص يرد `unavailable`، ولا يُعدّ رفضًا أبدًا.
2. **المسؤول وحسابات الخدمة تتجاوز القواعد**، `.validate` معها. و`authvar` يجعل مكتبة Admin تعمل بالمتغير الذي أرسلته، و`noauth` يجعلها مجهولة.
3. **القاعدة الجديدة مقفلة** (ADR-020). و`dev_open_rules` يعطي العملاء على الجهاز نفسه وحدهم القواعد المفتوحة `{".read": true, ".write": true}` بدل قواعد القاعدة، و`.indexOn` يبقى من قواعد القاعدة.
4. **onDisconnect يُفحص مرتين.**
   - عند التسجيل: `o` ككتابة، و`om` ابنًا ابنًا، كل ابن على البيانات المخزنة، كما في المحاكي.
   - عند التنفيذ: كل كتابة مسجّلة وحدها، بهوية الجلسة لحظة الإغلاق. المرفوض يسقط والباقي يُكتب. فالكتابة المسجّلة لا تنفذ بعد `unauth` أو بعد الدخول بمستخدم آخر أو بعد تشديد القواعد.
   - `oc` لا يفحص شيئًا.
5. **تغيير القواعد يعيد فحص كل استماع مفتوح** (PUT `/.settings/rules.json`).
   - Firebase لا يوثق ما يحدث للاستماعات المفتوحة. المحاكي يلغي فورًا ما لم يعد مسموحًا (`rules_change.json`)، ونحن كذلك: القاعدة التي تُشدَّد لسد تسرب تسده الآن، لا عند إعادة اتصال العميل.
   - ما بقي مسموحًا يبقى كما هو، بلا بيانات جديدة.
   - إن فشل الفحص بخطأ قاعدة البيانات تبقى الاستماعات: لا كتابة تمر أثناء العطل، فلا جديد يصلها.
   - القواعد الجديدة تحل محل القديمة في ترتيب الكاتب، و`.indexOn` منها في اللحظة نفسها.
6. **تغيير الهوية يعيد فحص الاستماعات قبل الرد**: `auth` بمستخدم آخر، و`unauth`، والإلغاء، وانتهاء الرمز. ويُلغى بـ `c` ما لم يعد مسموحًا.
7. **ترتيب دفعات `c`** في المحاكي والإنتاج لا يتبع قاعدة يعتمد عليها عميل. عندنا بالمسار ثم بالاستعلام.

## الدخول برموز Aruma

- `auth` برمز ID من Aruma Auth يمر عبر `idtoken.Verifier`، والجلسة تعمل بـ `auth = {provider, uid, token}`، و`user_id` كما يضيفه المحاكي. و`gauth` لرموز المسؤول وحدها: رمز ID عليه `permission_denied` «Permission denied» كما يرد الإنتاج (المحاكي يقبله مسؤولًا).
- الرد يحمل متغير `auth` و`expires`، وهو `exp` الرمز. المحاكي يرد `expires: null` لأنه لا يتحقق من الرموز.
- **`auth.provider` مدعوم** خلافًا لـ ADR-020 (الذي أجّله): قيمته `firebase.sign_in_provider` بلا `.com`، و`anonymous` إن لم يذكر الرمز شيئًا، كما يرد المحاكي (`auth_user.json`).
- الرمز المرفوض يرد حالته (`invalid_token` أو `expired_token`) بنص الإنتاج: «Could not parse auth token.» لما ليس JWT، و«Unsupported jwt algorithm: none» للرمز غير الموقّع، و«Auth token is expired» (بلا نقطة) للمنتهي. والرمز الموقّع الذي لا يتحقق نصه في الإنتاج غير معروف: «Invalid token.». والعطل يرد `unavailable` لأنه لا يقول شيئًا عن الرمز.
- **فرق مقصود:** الرمز المرفوض يُخرج الجلسة إلى المجهول، والإنتاج والمحاكي يبقيانها على هويتها السابقة (`production/auth.json`). والمكتبات تغلق الاتصال عند أي رفض على أي حال (`onAuthRevoked_`)، فلا يراه إلا عميل خام.
- **الانتهاء:** عند `exp` تصل `ac expired_token`، فتجدد المكتبة رمزها.
- **الإلغاء:** تعطيل المستخدم أو حذفه أو إلغاء جلساته أو تغيير كلمة مروره يصل عبر `idtoken.Revocations`. الجلسة المطابقة تتلقى `ac invalid_token` خلال ثانية، ثم `c` لكل استماع لم يعد مسموحًا. وإن فات المشترك شيء (`ErrOverflow`) يُعاد التحقق من رمز كل جلسة.

## اختلافات أخرى عن المحاكي

- **الصلاحية بعد قراءة البيانات:** كان الرفض يسبق قراءة الجسم (المراجعة #3). القواعد تحتاج القيمة الجديدة، فصارت البيانات تُقرأ أولًا. ورسائل العميل غير المسجّل محدودة بـ `unauthenticated_message_bytes`، فقراءتها رخيصة.

## REST (R5)

- **المرجع:** 403 طلبًا سُجّلت من المحاكي 4.11.2 في `internal/services/rtdb/api/testdata/emulator/rest.golden.json` (`capture_rest.mjs`)، تعيد تشغيلها `TestRTDBRESTMatchesEmulator` على Aruma وتقارن الحالة والجسم وETag. كل فرق مسجّل في `restKnownDifferences` مع سببه.
- **العنوان:** `/<path>.json` على مضيف القاعدة `https://<project>-default-rtdb.<rtdb.domain>`، أو على المضيف الرئيسي مع `?ns=` للتطوير (ADR-018).

### المدعوم

- **الطرق:** `GET` و`PUT` و`POST` و`PATCH` و`DELETE` و`OPTIONS`، و`X-HTTP-Method-Override` ترويسةً أو `x-http-method-override` معاملًا.
  - `PUT` يرد القيمة المكتوبة بعد حل قيم الخادم، بالأولويات، والمصفوفة مصفوفة، كما يرد المحاكي.
  - `POST` يولّد معرّف push على الخادم بخوارزمية المكتبات نفسها، ويرد `{"name": ...}`.
  - `PATCH` تحديث متعدد المسارات ذري، يرد الأبناء كما كُتبت مع `null`. و`PATCH null` يحذف، كما في المحاكي.
- **المعاملات:**
  - `print=pretty` بتنسيق المحاكي (`"key" : value`)، و`print=silent` يرد 204.
  - `shallow=true`: كل ابن `true`، والورقة قيمتها، و`".priority": true` أولًا للكائن ذي الأولوية كما يرد الإنتاج والمحاكي (ومع الحدود يُعد أول المفاتيح كما يعده المحاكي). ويُقرأ من الصفوف المخزنة صفحةً صفحة دون تحميل ما تحتها. ومع `limitToFirst` أو `limitToLast` بلا `orderBy` يحد المفاتيح بالترتيب المعجمي، كما يستعمله `firebase database:remove`.
  - `format=export` بالأولويات.
  - `timeout` حتى 15 دقيقة، و`writeSizeLimit` يُقبل ولا يوقف شيئًا (الحد عندنا `rest_write_bytes`)، و`disableTriggers` يُقبل ولا أثر له: لا مشغّلات في Aruma.
- **الاستعلامات:** `orderBy` (`"$key"` و`"$value"` و`"$priority"` ومسار ابن) مع `limitToFirst` و`limitToLast` و`startAt` و`startAfter` و`endAt` و`endBefore` و`equalTo`، بنصوص أخطاء المحاكي. والترتيب بابن أو بالقيمة بلا `.indexOn` يرد 400 «Index not defined…» (ADR-020 §1). والاستعلام الذي لا يختار شيئًا يرد `{}`، أو `null` إن كان له حد.
- **ETag:**
  - `X-Firebase-ETag: true` يعيد `ETag`، وهو بصمة العقدة نفسها التي يقبلها `h` في البروتوكول، و`null_etag` للعقدة الغائبة.
  - `if-match` على `PUT` و`DELETE`: عند الاختلاف 412 مع القيمة الحالية وETag. و`*` يكتب دون شرط.
  - `if-none-match` على `GET`: 304 عند التطابق. وعلى `PUT`: يكتب إلا إن تطابق، كما في المحاكي.
  - الكتابة المشروطة والفحص ذريان في الكاتب. وقراءة القيمة لرد 412 تخضع لقواعد القراءة: من لا يقرأ العقدة يتلقى 401 لا بياناتها.
- **الصلاحية:**
  - الرمز في `Authorization: Bearer` أو `?access_token=` أو `?auth=`، بهذا الترتيب، ويُؤخذ كما يأخذه الإنتاج (`production/rest_auth.json`):
  - **`Bearer` و`access_token` لرمز المسؤول وحده:** رمز حساب الخدمة أو رمز CLI (`aruma_sat_` و`aruma_pat_`) يتجاوز القواعد كما في Firebase. ومعه `auth_variable_override=<JSON>` يعمل بذلك المتغير، و`null` يجعله مجهولًا، كما ترسله مكتبتا Go وPython. والتجاوز بلا رمز مسؤول يُتجاهل، كما في المحاكي. وغير ذلك هناك، ورمز ID المستخدم منه، 401 «Unauthorized request.».
  - **`?auth=` رمز ID لمستخدم Aruma Auth**، يُتحقق منه للمشروع. والرمز الذي لا يتحقق، منتهيًا أو مزوّرًا أو غير رمز، يُتجاهل ويعمل الطلب مجهولًا: المسار العام يُقرأ، والمحمي «Permission denied».
  - القراءة بلا استعلام تراها القواعد `PlainREST` (مرتبة بالمفتاح)، وبالاستعلام متغير `query` منه. و`.validate` المرفوضة 401 «Permission denied»، كالرفض.
- **أخطاء JSON** بصيغة `{"error" : "…"}`، بنصوص المحاكي حيث له نص. و`Access-Control-Allow-Origin: *` على كل رد، كما يرد المحاكي.
- **`/.settings/rules.json`:** `GET` يرد المستند كما حُفظ بتعليقاته، و`?format=strict` بلا تعليقات (`getRulesJSON` في Admin Node)، و`PUT ?dryRun=true` يفحص ولا يحفظ (`firebase deploy --dry-run`). وأخطاء الصياغة `السطر:العمود: الرسالة\n` بلا مسار القاعدة، كما يكتبها Firebase. بلا رمز 403، وبرمز مستخدم 401.

### غير المدعوم

- **البث** (`Accept: text/event-stream`): مؤجَّل في ADR-020. يرد 400 بنص يقول ذلك، فيفشل `listen()` في Admin Python فورًا بدل أن يعلق. وكذلك `firebase database:profile`.
- **JSONP** (`callback=`): لا ترسله مكتبة، ويفتح القراءة لأي صفحة. يُتجاهل ويرد JSON.
- `download=`: يُتجاهل.
- أسرار القاعدة القديمة (database secrets) في `?auth=`: لا وجود لها في Aruma.

### اختلافات مقصودة عن المحاكي

- **الرموز:** المحاكي يأخذ أي `Bearer` أو `access_token` على أنه المالك. Aruma يأخذهما كما يأخذهما الإنتاج: رمز مسؤول يتحقق، وإلا 401 «Unauthorized request.». و`?auth=` الذي لا يتحقق يتجاهله الثلاثة.
- **`auth_variable_override` ليس كائنًا:** 400 «Invalid auth variable override.» بدل 503 «Firebase database instance is inactive».
- **طول المسار:** في REST يعد المحاكي الفواصل ضمن 768 بايت، وفي البروتوكول لا يعدها (`limits.json`). نعد بطريقة البروتوكول في الحالتين.
- **مسارات النقطة** (`/.inspect.json` و`/.settings.json`): مسار غير صالح 400. و`DELETE` و`POST` على `rules.json` 405.
- **HEAD** بلا جسم، و304 بلا `Content-Type`: كما يفرض HTTP وخادم Go.
- **المضيف الرئيسي:** بلا `.json` يذهب الطلب إلى اللوحة. ومضيف القاعدة يرد «Append .json to your URL…» كالمحاكي.

## الإنتاج والمحاكي (R7)

سُجّلت في 2026-10-21 من قاعدة Firebase حقيقية (`europe-west1`، بإذن المالك) بعميل خام (WebSocket وREST وطلبات `/.lp`) وبمكتبة JS الرسمية 13.0.0 في Node، ثم من المحاكي 4.11.2 ومن Aruma بالسكربت نفسه (`golden/production/capture_production.mjs` و`sdk_production.mjs`). كُتب كل شيء تحت `/aruma-ref/<run>/` وحُذف بعده، والقواعد أُضيف إليها ذلك المسار وحده مدة التشغيل ثم أُعيدت كما كانت حرفًا بحرف.

| الموضوع | الإنتاج | المحاكي 4.11.2 | Aruma بعد R7 |
|---|---|---|---|
| المصافحة `h` | `ts` و`v:"5"` و`h` مضيف شظية (`s-gke-…firebasedatabase.app`) و`s` من 32 حرفًا. `p` يُرد بـ `o`، و`"0"` بلا رد، والاتصال الساكن 75 ثانية بلا keepalive لا يُغلق | مثله، و`h` مضيفه | مثله، و`h` مضيفه |
| رد `auth` الناجح | `auth` فيه `provider` و`uid` و`user_id` و`token`، ونسخة من مطالبات الرمز غير المحجوزة (`email` و`email_verified` و`name`)، و`expires` هو `exp` | الشكل نفسه، و`expires: null` | بلا نسخ المطالبات في أعلى `auth`. **ملاحظة فقط:** المكتبات لا تقرأ بيانات الرد |
| رمز مرفوض | «Could not parse auth token.»، و«Unsupported jwt algorithm: none»، و`expired_token` «Auth token is expired»، وتبقى الهوية السابقة | يقبل غير الموقّع والمنتهي، ويبقي الهوية | **طابق** النصوص. ويُخرج الجلسة إلى المجهول (فرق مقصود أعلاه) |
| `gauth` برمز ID | `permission_denied` | يقبله مسؤولًا (`auth: null`) | **طابق** الإنتاج |
| `unauth` | `ok` «unauthenticated» | مثله | مثله |
| الرفض على `q` و`g` و`p` و`m` و`o` و`om` و`.validate` | `permission_denied` «Permission denied»، و`oc` مقبول | مثله | مثله |
| دفعات `c` بعد `unauth` | كلها قبل الرد. الاستعلام يعود مضافًا إليه `sin`/`ein` بقيمة `true` حيث غابا، والاستعلام بلا حدود ولا نهايات `null`، فلا تجد مكتبة JS استماع حدٍّ بلا نهايات ولا تلغيه. ترتيبها غير ثابت | مثله | **طابق** (كان يعيد الاستعلام كما أُرسل) |
| استعلام بلا فهرس | رد `q` فيه `{"w":["no_index"]}`، و`g` باستعلام `no_index` «Index not defined…»، وأخطاء REST `orderBy` بالنصوص نفسها | مثله | مثله |
| حجم الكتابة | `writeSizeLimit=tiny` لا يرفض 305 KB. والقيمة المجهولة «{tiny, large, small, medium, unlimited}». ومفتاح أطول من 768 بايت داخل البيانات يُرد بإطار خطأ `t:"e"` بلا رد. والمسار فوق 768 بايت والعمق فوق 32 بنصوصهما | الترتيب «{medium, tiny, large, unlimited, small}»، والباقي مثله | **طابق** الترتيب. ويرد على المفتاح الطويل بـ `invalid_parameters` (فرق قديم، المكتبات تتحقق قبل الإرسال). ولم يُجرَّب حد 16 MB على الإنتاج |
| ETag و`If-Match` | 28 حرفًا base64 لـ SHA-1، و`null_etag` للغائب، و412 بالقيمة والـ ETag، و`if-match: null_etag` يكتب على الغائب | مثله | مثله |
| `print=silent` و`print=pretty` | 204 لكل الطرق. والأجسام المنسقة والأخطاء تنتهي بسطر جديد | مثله | **طابق** السطر الأخير |
| `shallow` | `".priority": true` للكائن ذي الأولوية | مثله | **طابق** (كان يعد الأبناء وحدهم) |
| قاعدة المصفوفات | مفاتيح أعداد صحيحة تملأ أكثر من نصف المدى تُرد مصفوفة، `{"0":"a","3":"d"}` مصفوفة، و`{"5":"x"}` و`{"00":…}` و`{"-1":…}` كائنات | مثله | مثله |
| قيم الخادم | `timestamp` عدد صحيح، و`increment` صحيح+صحيح صحيح، ومع عشري عشري، وقيمة `.sv` مجهولة «Invalid data…» | مثله | مثله، والزيادة الآن تحفظ الصحيح |
| الأرقام | تبقى كما كُتبت: صحيح 64 بت أو عشري، تُرد بصيغة Java (`1.0` و`1.0E300` و`1.5E-7` و`1.2345678901234567E19`)، و`9007199254740993` دقيق، و`-0.0` يبقى، والأولوية عشري دائمًا (`5.0`) | مثله | **طابق**، إلا `-0.0` يعود `0.0`: jsonb بلا صفر سالب. مرئي في Android وiOS (Long وDouble) ومكتبات Admin |
| REST والرموز | `?auth=` الذي لا يتحقق يُتجاهل (العام 200، والمحمي «Permission denied»). و`Bearer`/`access_token` لغير المسؤول، ورمز ID منه، 401 «Unauthorized request.» | يتجاهل `?auth=` الرديء، ويأخذ أي `Bearer` مالكًا | **طابق** الإنتاج (كان يرد 401 على `?auth=` ويقبل رمز ID في `Bearer`) |
| الاستطلاع الطويل `/.lp` | الأمر `start` بمعرّف عددي كبير وكلمة سر من 10 أحرف، وصفحة `dframe` نفسها، و`pRTLPCB` بالترتيب، وترويسة `x-firebase-response` | الشكل نفسه | الشكل نفسه، وكلمة السر 20 حرفًا ورابط القطع فيه `ns=` |
| مسار بلا `.json` | 200 صفحة دخول Google | 400 «Append .json…» | مضيف القاعدة كالمحاكي، والرئيسي للوحة |
| مكتبة JS في Node | 28 حالة: قيم ورموز أخطاء ونصوص وتحذيرات `Using an unspecified index…` | — | مطابقة للإنتاج في الحالات كلها |

ما لم يُختبر على الإنتاج: حد 16 MB للكتابة عبر WebSocket (ثقيل على مشروع حقيقي)، ونص رفض الرمز الموقّع الذي لا يتحقق، ونص `ac` عند انتهاء الرمز أثناء الاتصال، وما يفعله `shallow` بالأولوية مع الحدود (اتبعنا المحاكي).

## مكتبات Admin

| المكتبة | النقل | التحقق | النتيجة |
|---|---|---|---|
| Node (`firebase-admin` 14) | WebSocket، والقواعد عبر REST | `test/smoke/admin-node/rtdb.test.mjs` (`run.sh admin-node-rtdb`) بـ `databaseURL` الإنتاجي وTLS | 9 من 9: get وset وupdate وpush وremove وtransaction، والقواعد ذهابًا وإيابًا، و`orderByChild` مع `.indexOn`، و`databaseAuthVariableOverride` تحت القواعد. وonDisconnect لا يخص المسؤول |
| Go (`firebase-admin-go` v4) | REST | `test/smoke/admin-go/rtdb` (`run.sh admin-go-rtdb`) بـ `databaseURL` الإنتاجي وTLS | ناجحة: Get وSet وUpdate وPush وDelete وTransaction بـ ETag، و`GetWithETag` و`SetIfUnchanged` و`GetIfChanged` و`GetShallow`، والاستعلامات، و`AuthOverride` |
| Python (`firebase-admin` 7.7) | REST | لم يُضف إلى الاختبارات | لا يعمل بعد مع Aruma لسببين خارج REST: (1) `google-auth` يوقّع الطلب بـ `aud=https://oauth2.googleapis.com/token` ثابتًا، ونقطة الرموز في Aruma ترفضه (`invalid_grant`). (2) بعد كل تجديد يرسل `google-auth` 2.62 رمز Aruma إلى `iamcredentials.googleapis.com`، ما لم يكن `universe_domain` في ملف المفتاح غير `googleapis.com`. وبعد تجاوز الأول تجريبيًا نجحت get وset وupdate وpush وdelete وtransaction، وبقي اتصال `iamcredentials` محجوبًا. و`listen()` غير مدعوم (البث مؤجَّل) |

كلاهما في CI ضمن مهمة smoke.

## firebase-tools

- **التجربة:** `test/experiments/firebase-cli-rtdb.sh`، بـ firebase-tools 15.34.0 والقاعدة بعنوانها الإنتاجي خلف TLS: 39 فحصًا ناجحة، ولا اتصال خارج الجهاز. والتفاصيل في `docs/research/experiments.md` (R5).
- **المجرَّب:** `database:set` و`get` (مع `--shallow` و`--pretty` و`--export` والاستعلامات) و`update` و`push` و`remove`، و`deploy --only database` مع `--dry-run` وبدونه.
- **كيف تجد الأداة القاعدة:** من `resources.realtimeDatabaseInstance` في مشروع واجهة الإدارة، ثم `databaseUrl` من واجهة إدارة القاعدة (`GET /v1beta/projects/{p}/locations/-/instances/{id}`). يقدّمهما Aruma منذ R5 حين تعمل القاعدة.
- **الإعداد:** `FIREBASE_API_URL` و`FIREBASE_TOKEN_URL` و`FIREBASE_RTDB_MANAGEMENT_URL` على عنوان Aruma، و`FIREBASE_TOKEN` رمز CLI. و`FIREBASE_SKIP_INFORMATIONAL_IAM=1` يمنع محاولة الأداة الاتصال بـ `cloudresourcemanager.googleapis.com`، وهي محاولة لا يتوقف عليها شيء.
- **الشكل `?ns=` لا يصلح للأداة:** `database:get` و`database:remove` يرسلان مسار العنوان وحده، فيضيع `?ns=`. والنطاق الفرعي يصلح.

## ما لم يأت بعد

- البث `text/event-stream` (ADR-020 §3).
- `getPriority()` في القواعد يُرفض عند الحفظ.
