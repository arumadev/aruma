# المراجعة الأمنية: القواعد وCloud Storage قبل v0.3

- **التاريخ:** 2026-10-24
- **الأساس:** `origin/main` عند `c92a40b` (بعد S6-I). الإصلاح الوحيد على فرع `s7-b-security`.
- **المراجع:** مراجعة مستقلة (S7-B) لم تكتب الكود. ركّزت على خدمة البايتات والرفع (صف S7 في `docs/research/storage-layout.md`)، ثم محرك القواعد، ثم أسطح الإدارة الجديدة، ثم حزم SDK وتبديلات وقت البناء.
- **النطاق:**
  - `internal/services/storage/{api,gcs,media,core,blob,wire,console,queries}`.
  - `internal/platform/rules`، و`internal/tenancy/core/rules.go`، و`internal/admin/mgmt/{rules,storage}.go`.
  - `internal/admin/web` (CSP اللوحة)، و`internal/server`، و`internal/platform/httpx`.
  - `sdk/{node/admin-storage,go/adminstorage,web/aruma-web,android/*,ios/*}`.
- **ما سبقها:** المراجعات المحكومة s2b، وs2-integration، وs3a إلى s3d، وs4a، وs4bc، وs4d، وs5a إلى s5c، وs6a، وs6w، وs6i في `.cache/briefs/reviews/`. لم يُعَد ما وُجد فيها وأُصلح، إلا للتحقق من أن الإصلاح على `main`.

## الطريقة

- **نموذج تهديد أولًا** (القسم التالي)، ثم هجوم على كل منطقة بدليل: كود بموضعه، أو اختبار، أو تشغيل.
- **خادم حقيقي:** إعادة الإنتاج على خادم الاختبار في `test/acceptance` فوق Postgres 18 (قواعد بالبادئة `aruma_s7b_`).
- **قياسات المحرك:** على Apple M1 بذاكرة 8 GB، باختبارات مؤقتة داخل الحزمة حُذفت بعد القياس. أُعيد تشغيل قياسَي SR3-02 وSR3-03 مرتين بيدين مختلفتين، فتطابقت الأرقام.
- **SDK:** قراءة الكود، ونصوص Node صغيرة على ملفات الحزم المثبتة. لا بناء Gradle ولا Xcode، لأن الجهاز مشترك مع المحاكيات.
- **الاعتماديات:** `npm audit --package-lock-only` للحزم الثلاث. أما `govulncheck` فلم يعمل (ما لم يُتحقق منه).

## نموذج التهديد

**الفاعلون:**

| الفاعل | ما يملكه |
|---|---|
| عميل مجهول على الإنترنت | اسم الـ bucket (في إعداد كل تطبيق)، وأي رابط يصله |
| مستخدم مسجّل في مشروع | `Firebase <idToken>` لمشروعه |
| حامل رابط تنزيل | `token=` في الاستعلام: صلاحية حاملة |
| حامل رابط رفع | `upload_id`: صلاحية حاملة على جلسة واحدة |
| مالك مشروع آخر على النسخة نفسها | مشروع معادٍ: قواعده، وأسماؤه، وحساب خدمته (ADR-022 §17) |
| مالك المشروع | كعكة اللوحة، ورمز CLI، وحسابات الخدمة |
| الوكيل والشبكة | `X-Forwarded-*`، وhttp العادي |
| Google | طرف ثالث قد تصله رموز Aruma من SDK غير مضبوط |
| سلسلة التوريد | مرآة `arumadev/firebase-ios-sdk`، وإضافة Gradle، وحزم npm |

**الأصول:** بايتات الكائنات وبياناتها، ورموز التنزيل ومعرّفات جلسات الرفع، ورموز الهوية وحسابات الخدمة وCLI، وأصل اللوحة (كعكة الجلسة وCSRF)، والحصة والقرص والذاكرة والمعالج، ومتانة الرفع الذي قيل للعميل إنه نجح، والقواعد المنشورة.

**حدود الثقة:**

1. الإنترنت إلى المدخل الواحد.
2. الأسطح الأربعة: `/v0` (قواعد أو رمز)، وGCS (حساب خدمة)، والكونسول (كعكة وCSRF)، وmgmt (رمز).
3. حد المشروع داخل الجداول والقرص.
4. بايتات المستخدم على أصل اللوحة نفسه.
5. العملية والقرص المحلي.
6. SDK مبني في تطبيق المطوّر، وGoogle.

## النتائج

| # | الخطورة | النتيجة | الحالة |
|---|---|---|---|
| SR3-01 | major | مجلد بيانات فارغ يُتبنّى ولو كانت في القاعدة كائنات، أي قرص غير مركَّب: كل قراءة 503 وكل رفع جديد يضيع | **مُصلحة في `0add0c6`** |
| SR3-02 | blocker | مجموع كلفة أنماط regex الحرفية في ruleset بلا حد، وruleset أثقل من الذاكرة المؤقتة يُترجم من جديد مع كل طلب | **مُصلحة في S7-B1** (`58bbedd`) |
| SR3-03 | blocker | رسائل أخطاء التقييم تنسخ نصوصًا بحجم يختاره المهاجم دون أن تُحسب على العدّاد، والمفسِّر يبقيها حية | **مُصلحة في S7-B1** (`58bbedd`) |
| SR3-04 | major | أخطاء دورات الدوال تكبر تربيعيًا | **مُصلحة في S7-B1** (`58bbedd`) |
| SR3-05 | minor | ترجمة القواعد من الكونسول خارج سيمافور الترجمة | **مُصلحة في S7-B1** (`58bbedd`)، وتكدّس التحميل بالنشر مؤجَّل وموثّق |
| SR3-06 | major | إعداد الويب لـ Auth وحده يرسل رمز هوية Aruma إلى `firebasestorage.googleapis.com` بلا تحذير | **مُصلحة في `s7-sdk-fixes`** |
| SR3-07 | minor | Android مع `ArumaStorageRedirect=false` يمرر طلبات Storage إلى Google، وiOS يرفضها | **مُصلحة في `s7-sdk-fixes`** |
| SR3-08 | minor | المخاطرة المقبولة لإعادة توجيه Node أوسع مما يقوله README | **وُثّقت في `s7-sdk-fixes`** |
| SR3-09 | minor | لا شيء يثبّت محتوى مرآة iOS (بند S6-I المفتوح) | **مُصلحة في `s7-sdk-fixes`** |
| SR3-10 | major | Auth على Aruma في الويب: Firestore والدوال القابلة للاستدعاء ما زالت ترسل رمز هوية Aruma إلى Google | **مؤجلة إلى v0.4** (Firestore)، موثّقة في README |

### SR3-01: مجلد بيانات فارغ تحت قاعدة فيها كائنات

- **الموضع:** `bindInstance` في `internal/services/storage/core/boot.go`.
- **الخلل:** ADR-022 §5.1 يتبنّى نصف الربط الموجود (INSTANCE أو صف `storage.instance`) «حين `p/` و`s/` فارغان»، والهدف ألا يُحذف شيء خطأً. لكن هذا بالضبط شكل قرص USB لم يُركَّب عند الإقلاع، أو `data_dir` بمسار خاطئ: نقطة التركيب مجلد فارغ على قرص النظام.
- **الدليل (خادم حقيقي، اختبار مؤقت `TestS7BScratchUnmountedDisk`):**

  ```
  upload with the disk mounted: 200 {"name":"before.txt",...}
  restart on the empty mount point: booted
  read an object uploaded before: 503 {"error":{"code":503,"message":"The service is unavailable; try again."}}
  upload during the outage: 200 {"name":"after.txt",...}
  files written under the mount point: [INSTANCE LOCK p/944840691890/2d/2dbc699d-…]
  ```

- **العاقبة:**
  - كل قراءة 503 (`objects.go`، «an object's file does not open»).
  - كل رفع جديد يُقبل ويُكتب على قرص النظام، وهو يمتلئ (`/` صغير على الجهاز المستهدف).
  - حين يعود القرص فوق نقطة التركيب يختفي ما كُتب أثناء الغياب، وتبقى صفوفه بلا ملفات: **رفع مؤكَّد للعميل ثم مفقود**.
  - ويكفي لحدوث ذلك وحدةُ systemd تبدأ قبل التركيب.
- **المعالجة (`0add0c6`):**
  - الإقلاع لا يتبنّى مجلدًا فارغًا إلا إن لم تنتظر القاعدة فيه ملفًا: لا كائن، ولا جلسة رفع حية. الاستعلام `PointsToFiles` في `queries/gc.sql`، بنطاق `global`.
  - وإلا يُرفض الإقلاع برسالة تقول: القرص غير مركَّب أو `data_dir` خاطئ. وإن ضاعت الملفات نهائيًا، يكتب المشغّل معرّف النسخة في `INSTANCE` بيده ليبدأ بما بقي.
  - عُدّل نص ADR-022 §5.1.
- **الاختبار:**
  - أربع حالات جديدة في `TestBootInstanceBinding` (`core/boot_test.go`)، فشلت كلها قبل الإصلاح (`Boot = <nil>`).
  - و`TestStorageBootRefusesAnUnmountedDisk` (`test/acceptance/storage_boot_test.go`) على خادم حقيقي: يرفع كائنًا، ثم يقلع على مجلد فارغ، ثم يتحقق من الرفض ومن أن `INSTANCE` لم يُكتب.
- **أثناء التشغيل:** إن اختفى القرص والخادم يعمل، فالمتجر يفتح كل شيء عبر `os.Root` على واصف المجلد القديم (`blob/disk.go`)، فتفشل العمليات بـ EIO ولا تذهب إلى نقطة التركيب. فشل مغلق حتى إعادة التشغيل، وإعادة التشغيل يحكمها الإصلاح.

### SR3-02: مجموع كلفة أنماط regex الحرفية بلا حد

- **الموضع:**
  - `check.go:633` يترجم كل نص حرفي يُمرَّر إلى `matches` أو `split` أو `replace`.
  - `compileLiteralRegex` (`regex.go:119-135`) يحد النمط الواحد وحده (10,000 تعليمة)، ولا شيء يحد مجموعها.
  - `Weight()` (`api.go:137`) يحسبها، والذاكرة المؤقتة ترفض ruleset أثقل من `compiled_rulesets_bytes` (128 MiB، `limits.go:88`؛ `releaseCache.put` في `tenancy/core/rules.go:642-645`، و«served, not kept» في `rules_test.go:511`).
- **الدليل:** مئة سطر `allow read: if 'x'.matches('a{1000}…z{k}');` بأنماط مختلفة، والمصدر 10 KB: ترجمة واحدة 184 ms، وذروة 206 MiB، و811 MiB مخصّصة، و130 MiB باقية، و`Weight` 170 MiB. أي فوق الحد، فلا يُخزَّن أبدًا. ومع 300 نمط (30 KB): 710 ms و670 MiB ذروة. والنمو خطي حتى حد المصدر (256 KiB، نحو 2,600 نمط)، وهذا استقراء لا قياس.
- **العاقبة:**
  - طلب `:test` أو إنشاء ruleset واحد بالمصدر المسموح ينهي ذاكرة الخادم المشترك. يكفيه رمز CLI أو كعكة اللوحة، أي **مالك أي مشروع على النسخة**.
  - وruleset منشور من هذا النوع يجعل كل طلب Storage، المجهول منه أيضًا، ترجمةً كاملة من جديد.
  - وهذا يخالف ADR-022 §17 («يصمد أمام مشروع معادٍ») وحدود ADR-021 §4.
- **الإصلاح المقترح:**
  - ميزانية لكل ruleset على مجموع وزن برامج الأنماط الحرفية، تُرد خطأ ترجمة.
  - ورفض ruleset يتجاوز `Weight()` فيه جزءًا من `compiled_rulesets_bytes` عند الترجمة، فلا يُنشر ruleset لا يُخزَّن.

### SR3-03: رسائل الأخطاء خارج عدّاد الذاكرة

- **الموضع:** `errorf` (`errors.go:25-27`) يكتب القيمة كاملة في الرسالة، ولا يحسب شيئًا. ومن مواضعه:
  - `errConversion` لـ `int()` و`float()` (`value.go:459`، و`library.go:168`، `:188`).
  - و`errProperty` على مفاتيح الخرائط والمسارات (`ops.go:352`، `:362`، `:421`).
  - و`lookup.go:112`، و`path.go:81`، `:160`.
  - و`regex.go:824` الذي يبني رسالته مع كل `replace()`.

  و`operands()` يبقي خطأ كل عنصر حتى النهاية (`eval.go:153-165`)، وكذلك المعاملات و`let` (`eval.go:385`).
- **الدليل:** `function h(s){ return [int(s), …×100]; }` مع `s` من نص حرفي بـ 64 KiB مضاعف بـ `let` حتى 2 MiB، والمصدر 66 KB:
  - تقييم `get` واحد مرفوض أخذ ذروة 250 MiB و409 MiB مخصّصة في 64 ms.
  - ومع 200 عنصر ونص 2 MiB: 522 MiB.
  - والمحسوب على العدّاد نحو 4 MiB، فلا يتحرك `Inflight` (512 MiB).
- **العاقبة:** بعد نشر قواعد كهذه، بضعة طلبات GET مجهولة متزامنة تنهي ذاكرة العملية. فهو تجاوز لحدود الذاكرة في ADR-021 §4 من مشروع معادٍ.
- **الإصلاح المقترح:** قصّ القيم في الرسائل كما يفعل `regex.go:142` (`%.40q`)، أو حساب بايتات الرسالة على `m.alloc`.

### SR3-04: أخطاء الدورات تربيعية

- **الموضع:** `check.go:673` يصدر خطأً لكل حافة راجعة، يسرد المكدّس كله.
- **الدليل:** 6,200 دالة، كل منها تستدعي `f0` والتي بعدها (246 KiB): 6,200 خطأ، و104 MiB من النص، وذروة 433 MiB، في 345 ms. و`:test` يكتبها كلها JSON (`mgmt/rules.go:233-235`)، وإنشاء الـ ruleset يجمعها في رسالة واحدة (`:287`).
- **الإصلاح المقترح:** كل دورة مرة واحدة، وسقف للأسماء في الرسالة، وسقف لعدد الأخطاء.

### SR3-05: الكونسول يترجم خارج السيمافور

- **الموضع:** `console/rules.go:161` (`:validate`)، و`putRules` عبر `tenancy/core/rules.go:83`: كلاهما يستدعي `Compile` بلا `compileSlot`، وmgmt وحده يأخذه (إصلاح s5a).
- **العاقبة:** يضخّم SR3-02 وSR3-04 بالتوازي.
- **الإصلاح المقترح:** سيمافور واحد في `tenancy`، يمر به كل من يترجم.
- **وبعد الإصلاح، مؤجَّل إلى قائمة الأعمال (S7-B1):** تكدّس التحميل بالنشر (SR3-05-publish-pileup في مراجعة S7-B1): كل نشر ينسى التحميل الجاري لإصداره، فيبدأ الطلب التالي تحميلًا ثانيًا، ومشروع واحد ينشر ويقرأ في حلقة يملأ أدوار التحميل.

### SR3-06: Auth وحده في الويب يسرّب رمز الهوية إلى Google

- **الموضع:**
  - `sdk/web/aruma-web/src/index.js:17`، `:88`: `AUTH_HOSTS` وحدهما يُحوَّلان.
  - و`src/vite.js:145-151`: بلا `storage: true` تعود إضافة Auth وحدها.
  - و`@firebase/storage` يضيف `Authorization: Firebase <idToken>` لكل طلب (`packages/storage/src/implementation/request.ts:237`).
- **الدليل (نص Node على الحزمة):**

  ```
  https://firebasestorage.googleapis.com/v0/b/p.appspot.com/o/a?alt=media auth=Firebase ID_TOKEN
  vite plugins without storage:true -> [ 'aruma-web' ]
  ```

  وقسم Storage في README (`:141-160`) بلا تحذير. أما iOS وAndroid فيحوّلان Storage افتراضيًا.
- **العاقبة:** مستعمل v0.2 يضيف Storage إلى تطبيقه دون أن يعرف الخيار، فيذهب رمز هوية Aruma حي إلى Google مع كل محاولة. Google لا تقبل الرمز، لكنه بيانات اعتماد تخرج إلى طرف ثالث.
- **الإصلاح المقترح:**
  - حين لا يعمل `installStorage`، تحجب أغلفة `fetch` و`XHR` في `install()` مضيفَ Storage طلبًا مرفوضًا.
  - ويفشل بناء Vite أو unplugin لـ Auth إن رأى وحدة من `@firebase/storage` والخيار مطفأ.
- **الإصلاح (`s7-sdk-fixes`):** طبقتان، لأن الإضافة لا ترى في البناء إلا وحداتها، وAuth يُضبط بإضافة Vite أو في التشغيل (`install()` و`/auto`):
  - في التشغيل: `install()` يرفض طلب مكتبة Storage إلى نطاق Google (فيه `Authorization` أو `X-Firebase-Storage-Version`)، في XHR بعد ترويساته فلا يُرسل ولا يوضع الرمز عليه، وفي fetch (بناء Node). إلا إن كان احتياط `installStorage` مثبّتًا فيأخذه. ويُجاب الطلب محليًا بـ 403: المكتبة تعيد خطأ الشبكة و5xx و408 و429 حتى تنفد ميزانيتها (دقيقتان، و10 للرفع؛ `isRetryStatusCode` في `implementation/utils.ts:24-40`، وفحص `!hitServer` في `implementation/request.ts:127-139`)، أما 403 فيصير `storage/unauthorized` في الحال (`sharedErrorHandler`، `implementation/requests.ts:122-124`) والسبب في `serverResponse`. ورسالة واحدة في الكونسول تقول لماذا. و`fetch` لرابط تنزيل بلا رمز هوية يمر. فالمسار مغطّى مع أي bundler وفي خادم تطوير Vite. `index.js` (`guardXHR`، `refusedFetch`).
  - في البناء: إضافة Vite بلا `storage: true` تُفشل البناء على أي وحدة `@firebase/storage`، في البناء وفي مُحسّن خادم التطوير لوضع `build` (الذي لا كود تشغيل فيه). `vite.js` (`refuseStorage`).
  - الأساس في المكتبة: `_getAuthToken` يأخذ الرمز من Auth التطبيق أيًّا كان نطاق Storage (`packages/storage/src/service.ts:266-277`، 0.14.6)، و`addAuthHeader_` يضعه (`implementation/request.ts:237`)، وبناء المتصفح يرسل بـ `open` ثم `setRequestHeader` ثم `send` (`platform/browser/connection.ts:77-88`).
  - الاختبارات: `test/redirect.test.js` (الحارس، والترتيبان مع `installStorage`)، و`test/vite.test.js`، وفي `test/e2e/web-storage` تطبيق Auth وحده بلا إضافة، بميزانية المكتبة الافتراضية، يفشل رفعه و`getMetadata` بـ `storage/unauthorized` في أقل من 5 ثوانٍ ولا يصل إلى Google (`tests/auth-only.spec.mjs`)، وبناء Vite له يجب أن يفشل (`build.mjs`).

### SR3-07: وضع Android الصارم لا يرفض Storage

- **الموضع:** `aruma-redirect/…/AuthEndpoints.java:56-59` يعيد `null`، فيفتح `ArumaStreamHandlerFactory.java:62` رابط Google الأصلي. أما iOS (`ArumaRedirectURLProtocol.swift:45-52`) فيفشل الطلب. وREADME Android (`:102-104`) يوحي بالفشل المغلق.
- **العاقبة:** رابط تنزيل قديم بمضيف Google يُفتح بـ `HttpURLConnection` في تطبيق مبني بالإضافة، فيذهب رمز التنزيل إليها.
- **الإصلاح المقترح:** `refused(host, "ArumaStorageRedirect is false")` لمضيف Storage بدل `null`، مع اختبار.
- **الإصلاح (`s7-sdk-fixes`):** `AuthEndpoints.rewrite` يرفض مضيف Storage (`StorageRefusedException`)، على https وعلى http العادي (المعالج يأخذ http أيضًا لهذا وحده)، فلا يُفتح رابط Google. ويجيب المعالج الرفض بـ `RefusedConnection`: 403 على الجهاز بلا اتصال، لأن firebase-storage تحوّل `IOException` إلى `NETWORK_UNAVAILABLE` (`NetworkRequest.performRequestStart`) وتعيدها مع 5xx و408 و429 (`ExponentialBackoffSender.isRetryableError`، لم يتغيرا منذ 2024، فهما في 23.0.0)، أما 403 فيصير `ERROR_NOT_AUTHORIZED` في الحال. والاختباران `storageIsRefusedWhenTheRedirectIsOff` و`refusedStorageIsAnsweredLocallyWith403`، وREADME بالعربية والإنجليزية.

### SR3-08: نطاق إعادة توجيه Node

- **الدليل:**
  - teeny-request 11.1.1 يستدعي node-fetch 3.3.2 بـ `follow` الافتراضي، وnode-fetch يبقي `Authorization` ما دام المضيف هو نفسه أو نطاقًا فرعيًا منه والبروتوكول نفسه (`node-fetch/src/index.js:209`، `utils/is.js`)، حتى 20 قفزة.
  - تجربة: `evil.aruma.localtest:… auth=Bearer SECRET`، و`aruma.localtest:<منفذ آخر> auth=Bearer SECRET`، و`other.localtest auth=undefined`.
  - وREADME (`sdk/node/admin-storage/README.md:47-51`) يقول «منفذ آخر للمضيف نفسه» وحده.
- **الإصلاح:** تصحيح README: أي نطاق فرعي، وأي منفذ، والبروتوكول نفسه. والرفع المتقطع لا يتبع إعادة توجيه أصلًا (`maxRedirects: 0`).
- **الحالة (`s7-sdk-fixes`):** README يذكر النطاق كاملًا: المضيف نفسه أو أي نطاق فرعي منه، بأي منفذ، بالبروتوكول نفسه، حتى 20 قفزة، والرفع المتقطع لا يتبع.

### SR3-09: محتوى مرآة iOS غير مثبّت

- **الموضع:** `check-firebase-storage.sh:34` يتحقق من وجود `ARUMA-NOTICE.md` و`ArumaStorageOrigin.swift` وحدهما، والعلامة عند الإقلاع اسم صنف (`StorageBuild.swift:24-28`).
- **العاقبة:** وسم منقول، أو مرآة مخترقة، يمر من البوابة ويشحن كودًا يحمل رموز الهوية. و`make-mirror.sh:95-104` يمنع أداتنا وحدها من نقل وسم، وحماية SwiftPM ثقة عند أول استعمال لكل جهاز، فمشغّلات CI الجديدة بلا بصمات.
- **الإصلاح المقترح:** `make-mirror.sh` يبني بشكل قابل للتكرار، فيُلتزم بجدول `وسم → commit/tree`، ويقارن سكربت الفحص `rev-parse HEAD^{tree}` به.
- **الإصلاح (`s7-sdk-fixes`):** `sdk/ios/firebase-storage-patch/mirror.lock` يثبّت `وسم → commit tree` (`12.19.3` → `f749478f…`، tree `c2f1efee…`، وهو ما حلّته مرآة e2e ونسخة Flutter كلتاهما). و`check-firebase-storage.sh` يقارن `rev-parse HEAD` و`HEAD^{tree}` به ويفشل على غيره، و`make-mirror.sh` يرفض بناءً يخالف التثبيت ويطبع سطر الوسم الجديد. وREADME يطلب التزام `Package.resolved` والحل بـ `-onlyUsePackageVersionsFromResolvedFile`. لم يُنقل وسم.

### SR3-10: Auth وحده في الويب يرسل رمز الهوية إلى Firestore والدوال عند Google

- **المصدر:** مراجعة PR #20 (`.cache/briefs/reviews/s7-sdk-review.md`). نطاق جديد، لا تراجع في إصلاح SR3-06.
- **الموضع:**
  - `@firebase/firestore` 4.19.0: `dist/common-DE1gv9M5.esm.js:8009` يضع `Authorization: Bearer <idToken>`، و`:8664-8672` يرمّز الترويسات في جسم طلب WebChannel (`encodeInitMessageHeaders = true`)، و`:10251` المضيف `firestore.googleapis.com`.
  - `@firebase/functions` 0.14.0: `dist/esm/index.esm.js:593` يضع `Authorization: Bearer <authToken>`، و`:488` العنوان `https://<region>-<project>.cloudfunctions.net/<name>`.
  - و`sdk/web/aruma-web/README.md:43` كان يقول إن هذه الطلبات تمر، دون أن يقول إنها تحمل الرمز.
- **العاقبة:** ما قيّم SR3-06 major عليه، عبر مسار أشيع: كل طلب Firestore ودالة من تطبيق على Auth في Aruma يحمل رمز هوية Aruma حيًّا إلى Google. ولا يمسكه حارس بالترويسات، لأن رمز Firestore في الجسم.
- **القرار: مؤجلة إلى v0.4 (Firestore)،** حين تنتقل هذه الخدمات إلى Aruma. وحتى ذلك README (بالإنجليزية والعربية) يقول إن هذه الطلبات تذهب إلى Google ومعها الرمز، وإن التطبيق على Auth في Aruma لا يستعملها.

## ما فُحص ووُجد سليمًا

| الخاصية | الموضع والدليل |
|---|---|
| `upload_id` من 128 بت (`crypto/rand`، base64url)، ويُخزَّن هاشه HMAC وحده، وطلب الجلسة يُقبل فقط بمشروع الـ bucket واسم الكائن نفسيهما | `core/uploads.go` (`StartUpload`، `readSession`)، `platform/crypto/tokens.go:21-42` |
| `Authorization` على طلبات الجلسة يُتجاهل، والهوية من `start`، وحالة المستخدم وملكية حساب اللوحة تُفحصان عند الإنهاء | `api/upload.go` (`uploadSession`)، `core/uploads.go:625` (`checkIdentity`) |
| رموز التنزيل UUID v4 يولّدها الخادم، والعميل لا يزرعها، ومختومة بـ KEK مربوطة بالمشروع والاسم | `core/tokens.go`، `api/upload.go` (`StripClientTokens`) |
| مقارنة الرمز بزمن ثابت مع كل رموز الكائن، والرمز غير المطابق 403 بلا قواعد، **والكائن الغائب يأخذ 403 نفسه**، فلا مرقاة وجود عبر الرمز | `core/tokens.go:76-83`، `core/objects.go:111` |
| لا رمز ولا `upload_id` في السجلات: السطر يكتب المسار بلا الاستعلام، والتدقيق يكتب عدد الرموز لا قيمها، ورابط رفع اللوحة في جسم الرد وحده مع `no-store` | `httpx/middleware.go:66-73`، `console/objects.go:321`، `:357`، `console/uploads.go`، `httpx/json.go:21` |
| `/v0`: الـ bucket قبل الاعتماد، و`Bearer` غير المقبول 403 قبل أي بحث عن كائن، وAdmin يحتاج مشروعًا `ACTIVE` يملكه | `api/api.go` (`serve`)، `api/auth.go` (`admin`) |
| GCS لحسابات الخدمة وحدها، مربوطة بمشروعها، والرفض قبل البحث (شرط استثناء S-11)، ووجهة rewrite في المشروع نفسه | `gcs/auth.go`، `gcs/gcs.go` (`route`)، `gcs/objects.go:198-201` |
| القواعد تفشل مغلقة: سياق بلا `prepareRules` 503، والإصدارة الغائبة رفض، و`ErrOverloaded` 503، والعطل 503، ولا سماح على خطأ | `core/rules.go:44-107` |
| الوجود بعد الصلاحية: الرفض 403 للموجود والغائب معًا، و404 بعد السماح وحده (دلالة Firebase) | `core/objects.go` (`Stat`، `Open`، `Delete`) |
| ترويسات `media`: النوع مخزّن لا مخمَّن، و`nosniff`، و`sandbox` لكل نوع، والنوع الذي فيه فاصلة أو لا يُحلَّل `octet-stream`، وCR/LF في الترويسات تُسقط، وPDF `attachment` | `media/headers.go` |
| رفض العمّال والـ Service Worker من كل أصل، والسكربت والأسلوب والـ worklet وXSLT من أصل اللوحة | `media/serve.go` (`refused`) |
| CSP اللوحة بمصادر `<host>/_next/static/`، ولا إعادة توجيه تحت `/_next/static/`، ومسار RTDB يستثنيه. و`ServeMux` في Go 1.27 ينظّف `EscapedPath` فلا يعيد توجيه `..%2f` | `admin/web/web.go:186-252`، `server.go` (`isRTDB`)، `$GOROOT/src/net/http/server.go:2734-2769` |
| تحذير عند الإقلاع حين Storage على http عادي (لا `Sec-Fetch-*`) | `internal/app/app.go:271-275` |
| Range: نطاق واحد، والأرقام مشبعة لا تفيض، والنطاقات المتعددة 200 كاملًا بمسح واحد | `wire/range.go` |
| فك gzip تدفقًا بذاكرة ثابتة، ومحدود بـ `object_bytes`، والقطع بعد الرأس `ErrAbortHandler` لا رد مختوم ناقص | `media/gunzip.go`، `media/serve.go` (`cut`) |
| multipart: ميزانية 16 KiB للرؤوس، وJSON حتى `json_body_bytes`، وجزءان بالضبط، ولا `ParseMultipartForm` | `wire/multipart.go` |
| الأسماء: UTF-8، و1024 بايت، وبلا CR/LF/NUL، وليست `.` ولا `..`. ومفاتيح الملفات UUID لا من الاسم، وكل فتح عبر `os.Root` | `core/helpers.go:29-45`، `blob/disk.go` |
| عنوان العميل: `X-Forwarded-For` من اليمين ومن الوكلاء الموثوقين وحدهم، وعدّاد واحد للكتابة المتزامنة لكل IP على `/v0` وGCS، وIPv6 لكل /64 | `httpx/clientip.go:13-31`، `api/api.go`، `gcs/gcs.go`، `ratelimit/inflight.go` |
| slowloris: مهلتا القراءة والكتابة تتقدمان نافذةً كلما وصل `min_bytes_per_second × 30`، و`ReadHeaderTimeout` 10 ثوانٍ، و`MaxHeaderBytes` 64 KiB | `api/upload.go` (`pacedBody`)، `media/pace.go`، `server.go:272-276` |
| ENOSPC أو كتابة ناقصة: `errStoreMismatch`، ولا يُعلن شيء، والطلب التالي يقص الملف إلى `received` | `core/insert.go` (`stream`)، `core/uploads.go` (`appendChunk`، `cutStaging`) |
| فشل fsync (EIO): البايتات غير المزامنة لا تُعلن أبدًا، وتُقص قبل الإلحاق التالي، فلا يُعاد fsync على صفحات قد تكون سقطت | `core/uploads.go` (`appendChunk`، `cutStaging`) |
| لا `mmap` في Storage، فلا SIGBUS من ملفات الكائنات (`grep` فارغ في `blob/`) | `blob/` |
| الحجز يُرد عند فشل `Insert` تحت `WithoutCancel`، و`COMMIT` تحت `WithoutCancel`، وحجوزات الانهيار تُصالَح عند الإقلاع | `core/insert.go` (`releaseReservation`)، `core/durability.go`، `core/boot.go` (`reconcileUsage`) |
| الكونسول: كعكة وCSRF عبر `consolemount`، وحد جسم 1 MiB، والتنزيل `attachment` و`no-store` و`CORP: same-origin`، وكل كتابة في التدقيق | `console/console.go`، `media/headers.go` |
| mgmt بالرموز وحدها (لا كعكة، فلا CSRF)، وحساب الخدمة يرى مشروعه وحده، وإنشاء القواعد ونشرها وحذفها في التدقيق | `admin/mgmt/mgmt.go:46`، `tenancy/core/rules.go:108`، `:229`، `:336` |
| محرك القواعد: إصلاح المواضع (s5a) على `main` (`7e0d0e8`)، و29,104 خطأ صياغة تُترجم في 6 ms | `parser.go:343`، `check.go:182` |
| حدود التعشيش: 100k من `(` و120k من `[` و20k `match` متداخل و100k `\|\|` تُرفض في 8 ms أو أقل | قياس المراجعة |
| ميزانية العمل تُحسب قبل التنفيذ، وcache الـ regex محدود (256 مدخلًا، 4 MiB)، والأنماط المبنية وقت التشغيل تُحسب قبل ترجمتها | `meter.go`، `regex.go` |
| `\|\|` و`&&` مع الخطأ كما في Firebase، و`firestore.*` تحت `NoDocuments` خطأ (رفض)، و`Inflight` المستنفد 503 | `eval.go`، `lookup.go:166`، `meter.go:80` |
| Node `@aruma/admin-storage`: حارس الأصل يقارن `URL.origin` (الحالة و:443 مطبَّعان، والنقطة الأخيرة والمنفذ الآخر والنطاق الفرعي مرفوضة)، والرفع بـ `maxRedirects: 0`، وتبادل الرمز `redirect:'error'`، ومتغيرات المحاكي مرفوضة | `sdk/node/admin-storage` |
| Go `adminstorage`: فحص الأصل في `RoundTrip` (`transport.go:156-170`)، و`CheckRedirect` يمنع، و`SignedURL` خطأ صريح، ومتغيرا المحاكي مرفوضان | `sdk/go/adminstorage` |
| iOS وAndroid: الأصل المرقَّع يرفض مضيفات Google والمسار والاستعلام وبيانات الاعتماد، و`fromArumaUrl` يرفض المقاطع الفارغة (بند S6-I مغلق في Android)، و`StorageGuard` يوقف تطبيقًا بلا الإضافة ولا احتياط | `sdk/ios`، `sdk/android` |
| الويب: `HOST_SHAPE` يتحقق من المضيف ويُكتب بـ `JSON.stringify`، واستيراد CDN مرفوض، وفحص Metro في الـ serializer، ورفض `serverExternalPackages` | `sdk/web/aruma-web/src/storage.js:50-56`، `:103` |
| `npm audit` (npm 11.19.0، `--package-lock-only`، مع dev وبدونه): admin-storage 60 حزمة إنتاج، وadmin-auth 1، وaruma-web 9، **ولا ثغرة** | 2026-10-24 |

## المخاطر المتبقية المقبولة لـ v0.3

| المخاطرة | لماذا تُقبل |
|---|---|
| **Node: teeny-request يتبع إعادة التوجيه** إلى المضيف نفسه أو نطاق فرعي منه، بأي منفذ وبالبروتوكول نفسه، حتى 20 قفزة، ومعه `Bearer` (حُكم في s4d؛ نطاقه الصحيح في SR3-08) | لا يصل إلى أصل آخر. ويحتاج وكيلًا أمام Aruma يعيد التوجيه، أو نطاقًا فرعيًا يملكه غيرك. والرفع المتقطع لا يتبع |
| **Turbopack:** بناء لم تصل فيه وحدة إلى المحمّل لا يستطيع الفشل (`next.js:11-13`). **وMetro** يفشل على الوحدات المجمّعة وحدها (`metro.js:88-91`) | لا خطّاف نهاية بناء في الأداتين (s6w). والاحتياط وقت التشغيل يغطي الطلبات، لا روابط `<img>` |
| غير المضبوط لا يفعل شيئًا في كل محوّل ويب، وفي iOS وAndroid بلا `ArumaURL`، وFirebaseStorage المرقَّع بلا `ArumaURL` يذهب إلى Google (`storage-host.patch:55-59`) | قرار المسؤول: اختبارات المكتبة الرسمية تمر، وفحص `ArumaRedirect` المغلق يمسك الطلبات |
| أوضاع الاحتياط (`useArumaStorage`، والتحويل وقت التشغيل) تترك روابط التنزيل على مضيف Google. وFlutter web يعود إلى gstatic بصمت إن حُذف مقطع `index.html` | موثّق (ADR-022 §16). والتبديل وقت البناء هو الطريق الأساسي |
| **http العادي:** لا `Sec-Fetch-*`، فرفض السكربت من أصل اللوحة لا يعمل. والحماية هناك CSP اللوحة بالمسار وحدها | Fetch Metadata §3. وتحذير عند الإقلاع، والدليل يشترط https |
| رابط التنزيل صلاحية حاملة في الاستعلام | دلالة Firebase. لا يُسجَّل، و`Referrer-Policy: no-referrer` على ردودنا، والإبطال باستبدال الرمز |
| فك الضغط حتى `object_bytes` لكل تنزيل، والتنزيلات بلا حد لكل IP، والقارئ البطيء يمسك ملفًا وgoroutine بالحد الأدنى للمعدل | ADR-022 §10.7، §17. الكلفة بقدر ما يستهلكه العميل من نطاق، والسقف عند وكيل عكسي |
| جلسة رفع GCS مربوطة بحساب خدمة تبقى صالحة بعد حذف الحساب أو مفتاحه، حتى عمرها (7 أيام). `checkIdentity` يفحص المستخدمين وحسابات اللوحة وحدهم (`core/uploads.go:625`) | GCS: «This session URI acts as an authentication token». ومشروع محذوف يوقفها (`Bucket` يقبل `ACTIVE` وحده) |
| حجز يلتزم التزامًا ملتبسًا (انقطاع القاعدة عند `COMMIT` في `ensureReserved`) يبقى محجوزًا حتى المصالحة عند الإقلاع التالي | يحتاج عطل اتصال بالقاعدة في لحظته، ولا يمس البيانات |
| الحدود لكل IP في الذاكرة، وخادم واحد | كما في v0.1 وv0.2 |
| استثناء S-11: وجود الـ bucket ليس سرًا (403/401 لمشروع آخر) | ADR-022 §14. والرفض قبل أي بحث عن كائن (فُحص أعلاه) |
| فرق توقيت صغير في مسار الرمز: الختم يُفتح للكائن الموجود وحده (`objects.go:99-110`). **رأي** | ميكروثوانٍ لفك AES-GCM، خلف ذهاب إلى القاعدة وإياب عبر الشبكة. لم يُقَس |
| حلقة تحميل القواعد في `tenancy` بلا `recover`، فـ panic فيها يُسقط العملية. **رأي**: لم يوجد مدخل يُحدث panic | `Evaluate` نفسه تحت `recover` الوسيط (500، لا سماح) |

## قائمة الإصدار

أُغلقت في S7-C (2026-10-24) على `main` عند `d9391be`، ومعها فرع `s7-c-close`. أدلة CI من التشغيل [40111454561](https://github.com/arumadev/aruma/actions/runs/40111454561)، وكل وظائفه خضراء.

- [x] دمج [`0add0c6`](https://github.com/arumadev/aruma/commit/0add0c6) (SR3-01): على `main`.
- [x] **SR3-02 وSR3-03 مُصلحتان باختبار حدود** (blocker)، في دمج S7-B1 [`58bbedd`](https://github.com/arumadev/aruma/commit/58bbedd):
  - SR3-02: [`f5e68e5`](https://github.com/arumadev/aruma/commit/f5e68e5) و[`aad792b`](https://github.com/arumadev/aruma/commit/aad792b) و[`ccff9a5`](https://github.com/arumadev/aruma/commit/ccff9a5)؛ `TestLiteralPatternsHaveABudget`، و`TestRulesetWeightIsBounded`، و`TestClassTablesAreCounted` (ما يحمله ملف عند `regex_bytes` مقيسًا: 0.63 إلى 0.84 مما تحسبه الميزانيات)، و`TestCorpusFitsTheBudgets`.
  - SR3-03: [`eead850`](https://github.com/arumadev/aruma/commit/eead850)؛ `TestErrorMessagesAreBounded`.
- [x] SR3-04 مُصلحة (major): [`db6d084`](https://github.com/arumadev/aruma/commit/db6d084)؛ `TestCallCycleErrorsAreBounded`.
- [x] SR3-06 مُصلحة (major): `install()` يرفض و`vite` يُفشل البناء (`s7-sdk-fixes`، [`d9391be`](https://github.com/arumadev/aruma/commit/d9391be)).
- [x] SR3-05 مُصلحة: [`a67de58`](https://github.com/arumadev/aruma/commit/a67de58) و[`6e7b526`](https://github.com/arumadev/aruma/commit/6e7b526)؛ `TestEveryCompilationTakesASlot`. وتكدّس التحميل بالنشر (SR3-05-publish-pileup) مؤجَّل وموثّق في `docs/compat/rules-v0.3.md` (القيود) و`docs/releases/v0.3.0.md`.
- [x] SR3-07 وSR3-09 مُصلحتان (`s7-sdk-fixes`).
- [x] SR3-10 (major): مؤجلة إلى v0.4 بقرار المنسّق، وموثّقة في README `@aruma/web` (`sdk/web/aruma-web/README.md:43-47`، وبالعربية) وفي `docs/releases/v0.3.0.md`. تُغلق حين يخدم Aruma Firestore (والدوال).
- [x] README حزمة Node بنطاق إعادة التوجيه الصحيح (SR3-08، `s7-sdk-fixes`).
- [x] `govulncheck ./...` نظيف، ومعه `npm audit` على الحزم الثلاث (2026-10-24):
  - govulncheck 1.9.0 (قاعدة 2026-10-21) وجد GO-2026-6809 (`crypto/x509`، مُصلحة في go1.27.3) قابلة للوصول عبر مصافحة TLS. سطر `toolchain go1.27.3` في `go.mod` ([`ee609d6`](https://github.com/arumadev/aruma/commit/ee609d6)) يصلحها: «Your code is affected by 0 vulnerabilities». والباقي GO-2026-5932 (`x/crypto/openpgp`) غير مستورد. و`sdk/go/adminauth` و`sdk/go/adminstorage` نظيفتان بـ go1.27.3؛ وهما مكتبتان، فالمكتبة القياسية فيهما من Go المستعمِل.
  - `npm audit --package-lock-only` (npm 11.19.0، مع dev وبدونه): admin-storage وadmin-auth وaruma-web، «found 0 vulnerabilities». يُعاد إن تأخر الوسم عن هذا اليوم.
- [ ] `docs/guides/storage.md`: **لم يُكتب بعد، ويأتي بعد الإصدار.** والنقاط الثلاث في قسم Storage من `docs/configuration.md` (ومعه كل مفاتيح `storage.*` و`rules.limits`)، وفي «Upgrading from v0.2» في `docs/releases/v0.3.0.md`:
  - `data_dir` على قرص يُركَّب قبل الخدمة (systemd `RequiresMountsFor=`)، وما تقوله رسالة SR3-01.
  - وhttps شرط للنسخ غير المحلية.
  - و`strategy: Recreate`.
- [ ] `make lint` و`make sqlc-check` و`make test` نظيفة على الوسم: نظيفة على `d9391be` (وظيفة Go في [40111454561](https://github.com/arumadev/aruma/actions/runs/40111454561))، و`make lint` و`go build ./...` على `s7-c-close`. **تبقى للمالك:** CI أخضر على commit الوسم.
- [x] مصفوفة المتصفحات (ADR-022 §10.6): 14/14 على Chromium وFirefox وWebKit في [40111454561](https://github.com/arumadev/aruma/actions/runs/40111454561).
- [x] أوراكل القواعد 98%: مجموعة التطوير 87/87 و3643/3643 و1985/1985 بلا سماح زائد، بعد S7-B1 (`docs/compat/rules-v0.3.md`)، و`TestOracle` في وظيفة Go.
- [x] e2e الأجهزة بحارس الشبكة: Android وiOS لـ Storage شغّلهما S6-A وS6-I محليًا (`test/e2e/android-storage`، و`test/e2e/ios-storage/README.md`)، وFlutter على Android محليًا (S6-F، ‏26/26 debug وrelease). وiOS في CI على `main` (`.github/workflows/ios-storage.yml`، دُمج في `87ad20f`): Swift ‏4/4 وFlutter ‏26/26 واختبارات المرآة 173/173 ([run 40115235140](https://github.com/arumadev/aruma/actions/runs/40115235140)). و`codemagic.yaml` بديل اختياري لم يُشغَّل بعد (توكن المالك يحتاج تجديدًا). وAndroid على الجهاز ليس في CI.
- [x] قائمة ADR-010 S-1 إلى S-15 باختباراتها السلبية خضراء، ومعها الاختبارات الجديدة هنا: `test/acceptance` (ومنه `isolation_test.go`) في `make test`، وظيفة Go في [40111454561](https://github.com/arumadev/aruma/actions/runs/40111454561).

**ما يبقى للمالك:** الوسم `v0.3.0` (رسالته الفقرة الأولى من `docs/releases/v0.3.0.md`، كالوسمين السابقين)؛ وCI أخضر على commit الوسم. وتشغيل Codemagic اختياري، ودليل `docs/guides/storage.md` بعد الإصدار.

## ما لم يُتحقق منه

- **`govulncheck`:** لم يعمل في المراجعة (يجلب قاعدة الثغرات من الشبكة). شغّله S7-C (قائمة الإصدار أعلاه).
- SR3-02 بالحجم الكامل وSR3-03 بأقصى عدد عناصر: استقراء خطي، أُبقي تحت 1 GB عمدًا.
- اختبارات Go لـ `sdk/go/adminstorage`: وحدات غير موجودة في الذاكرة المحلية، ولم تُجلب. واختبارات حزم Node: لا `node_modules`.
- لا بناء Android ولا iOS ولا تشغيل محاكيات، لقاعدة الذاكرة.
- هل يبقي URLSession في Apple `Authorization` على إعادة توجيه بين مضيفين تمررها `ArumaRedirect` (`ArumaRedirectURLProtocol.swift:119-127`).
- حمايات GitHub على مرآة `arumadev` (قواعد الوسوم).
- سلوك ext4 وXFS عند خلع قرص USB (القياس على APFS وحده). والمنطق يعتمد على `os.Root`.
- أنواع المحتوى الغريبة تحت `sandbox` (`multipart/x-mixed-replace`، وPDF في Firefox): خارج مصفوفة المتصفحات المقيسة.

## إعادة التشغيل

- SR3-01:

  ```
  ARUMA_TEST_DB_PREFIX=aruma_s7b_ go test ./internal/services/storage/core/ -run TestBootInstanceBinding
  go test ./test/acceptance/ -run TestStorageBootRefusesAnUnmountedDisk
  ```

- على الفرع: `go vet ./internal/services/storage/... ./test/acceptance/` نظيف، واختبارات `internal/services/storage/...` و`internal/store/...` ناجحة كلها.
