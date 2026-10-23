# C: إدارة Cloud Storage من أداة `firebase` (نشر القواعد وتهيئتها)

بحث من الشيفرة المصدرية فقط، بلا تشغيل للأداة على مشروع حقيقي. هدفه تحديد ما يجب أن يجيب عنه خادم Aruma حتى يعمل `firebase deploy --only storage` و`firebase init storage`.

## المصادر

| المستودع | الإصدار | الـ commit | الرخصة | الاختصار في الاستشهاد |
|---|---|---|---|---|
| firebase-tools | 15.34.0 | `4e6eb923dd5e4a081eb227baaee58df85c17d044` | MIT | `FT src/...:سطر` |

- المسار المحلي للقراءة فقط: `/Volumes/Work/aurma-project/.cache/research/src/firebase-tools`.
- **VERIFIED** تعني أن الادعاء مقروء في الشيفرة عند السطر المذكور.
- **INFERENCE** تعني استنتاجًا من الشيفرة أو من وثائق Google العامة، ويحتاج إلى تأكيد بالتسجيل.
- طلبات Firebase console نفسها خارج النطاق ولم تُرصد.

## 0. ملخص سريع

- **اسم الإصدار (release) لقواعد Storage:** `projects/{p}/releases/firebase.storage/{bucket}`. اسم الـ bucket لا يُرمَّز في الـ URL، فيحتوي المسار على `/` ونقاط. VERIFIED: `FT src/rulesDeploy.ts:285-288`، `FT src/gcp/rules.ts:233,262,268`، `FT src/rulesDeploy.spec.ts:576-586`.
- **مصدر اسم الـ bucket الافتراضي:** طلب واحد هو `GET {FIREBASE_FIREBASESTORAGE_URL}/v1alpha/projects/{p}/defaultBucket`. الأداة تأخذ `bucket.name.split("/").pop()`. لا تستعمل `adminSdkConfig` ولا `resources.storageBucket` في هذا المسار. VERIFIED: `FT src/gcp/storage.ts:196-224`.
- **ترتيب استدعاءات firebaserules في النشر:**

  | الخطوة | الطلب |
  |---|---|
  | تحقق الترجمة | `POST /v1/projects/{p}:test` |
  | قائمة الإصدارات | `GET /v1/projects/{p}/releases` |
  | محتوى آخر ruleset | `GET /v1/projects/{p}/rulesets/{id}` |
  | الإنشاء، إن تغيّر المحتوى | `POST /v1/projects/{p}/rulesets` |
  | الإطلاق | `PATCH /v1/projects/{p}/releases/firebase.storage/{bucket}`، ثم `POST /v1/projects/{p}/releases` إن فشل |
  | عند 429 فقط | `GET .../rulesets` وأحيانًا `DELETE .../rulesets/{id}` |

- **نفس واجهة القواعد تخدم Firestore** باسم الإصدار `cloud.firestore`، أو `cloud.firestore/{databaseId}` لقاعدة مسمّاة. VERIFIED: `FT src/rulesDeploy.ts:33-36,285-288`.

## 1. `firebase deploy --only storage` من البداية إلى النهاية

### 1.1 شكل الإعداد في `firebase.json`

| الشكل | الحقول | المصدر |
|---|---|---|
| مفرد | `"storage": {"rules": "storage.rules", "target"?: "...", "predeploy"?, "postdeploy"?}` | VERIFIED: `FT src/firebaseConfig.ts:22-25,156-159` |
| مصفوفة | `"storage": [{"bucket": "...", "rules": "...", "target"?: "..."}]`. الحقل `bucket` إلزامي في النوع | VERIFIED: `FT src/firebaseConfig.ts:161-165` |
| نص المساعدة | يعرض الشكلين معًا، ومعهما `target` | VERIFIED: `FT src/deploy/storage/index.ts:7-23` |

- يُطابَق الهدف `target` مع ملف `.firebaserc` بهذه البنية: `targets.{project}.storage.{targetName} = [bucket, ...]`.
  - VERIFIED: `FT src/rc.ts:113-115`، وتسجيل النوع `storage: { resource: "bucket", exclusive: true }` في `FT src/rc.ts:14`.
- الأمر `firebase target:apply storage <name> <bucket>` لا يستدعي أي API. VERIFIED: غياب أي Client في `commands/target-apply.ts`.

### 1.2 مراحل النشر

يجري المنسّق `deploy/index.ts` المراحل بهذا الترتيب: predeploy ثم prepare ثم deploy ثم release ثم postdeploy (VERIFIED: `FT src/deploy/index.ts:239-249`).

- مع `--dry-run` تُضاف مرحلة prepare وحدها (VERIFIED: `FT src/deploy/index.ts:224-230`).
  - يجري عندها فحص الـ bucket الافتراضي والترجمة عبر `:test`.
  - لا يُنشأ ruleset ولا يُطلق إصدار.

#### قبل المراحل (أمر deploy)

- `requirePermissions` بالصلاحيات `firebase.projects.get` و`firebaserules.releases.create` و`firebaserules.rulesets.create` و`firebaserules.releases.update`. VERIFIED: `FT src/commands/deploy.ts:88-101`، `FT src/deploy/index.ts:69-73`، `FT src/requirePermissions.ts:9`.
- الفحص يرسل `POST {FIREBASE_RESOURCEMANAGER_URL}/v1/projects/{p}:testIamPermissions`. VERIFIED: `FT src/gcp/iam.ts:179-216,223-234`.
  - الجسم: `{"permissions":[...مرتبة]}`.
  - الترويسة: `x-goog-user-project: {p}`.
  - الاستجابة المقروءة: `{"permissions":[...]}`.
- **الفحص معلوماتي فقط.** رمي الخطأ "Authorization failed. This account is missing the following required permissions..." يقع داخل `try` ويُلتقط فورًا، فيُسجَّل في debug ويُكمَل النشر. VERIFIED: `FT src/requirePermissions.ts:31-43`.
- `FIREBASE_SKIP_INFORMATIONAL_IAM` يلغي الطلب كليًا. VERIFIED: `FT src/gcp/iam.ts:187-194`.

#### prepare (`FT src/deploy/storage/prepare.ts`)

1. إذا لم يوجد مفتاح `storage` ينتهي بصمت (`:14-17`).
2. تحليل `--only`: إما `storage` كاملة، أو `storage:<target>` (`:19-32`).
3. **إن كان الإعداد مفردًا وكان هناك مشروع**، تستدعي `gcp.storage.getDefaultBucket(project)` وتضيف `bucket` إلى الإعداد (`:37-40`).
   - في شكل المصفوفة لا تُستدعى، ويُؤخذ الـ bucket من الإعداد أو من الـ target.
4. لكل عنصر: إذا وُجد `target` تفشل `rc.requireTarget` برسالة `Deploy target X not configured for project P. Configure with: firebase target:apply storage X <resources...>` (`:43-46`، `FT src/rc.ts:206-219`).
   - ثم `rulesDeploy.addFile(rules)` (`:47-51`).
   - تقرأ `addFile` الملف من مجلد المشروع. اسم الملف في الـ ruleset هو المسار كما كُتب في `firebase.json`، مثل `storage.rules`.
   - عند الفشل تظهر "Error reading rules file X". VERIFIED: `FT src/rulesDeploy.ts:73-84`.
5. أهداف `--only storage:x` غير الموجودة تعطي "Could not find rules for the following storage targets: ..." (`:54-58`).
6. `rulesDeploy.compile()` يرسل `:test` لكل ملف (`:64`).

#### deploy (`FT src/deploy/storage/deploy.ts:9-15`)

يستدعي `rulesDeploy.createRulesets("firebase.storage")`. التفاصيل في 1.4.

#### release (`FT src/deploy/storage/release.ts:11-36`)

- لكل إعداد:
  - إن وُجد `target` يُطلق لكل bucket في `rc.target(...)`.
  - وإلا يُطلق لـ `ruleConfig.bucket`.
- الإطلاقات تجري بالتوازي عبر `Promise.all`.
- تستدعي `rulesDeploy.release(rules, "firebase.storage", bucket)`. بلا bucket تفشل بـ `Cannot release resource type "firebase.storage"`. VERIFIED: `FT src/rulesDeploy.ts:277-279`.
- مفتاح الـ ruleset هو اسم الملف. فإن تشارك bucketان ملف قواعد واحدًا، يُطلق كلاهما على ruleset واحد. VERIFIED: `FT src/rulesDeploy.ts:189-192,281-284`.

### 1.3 حلّ اسم الـ bucket: `getDefaultBucket`

VERIFIED: `FT src/gcp/storage.ts:196-224`.

```text
1) ensure(projectId, firebaseStorageOrigin(), "storage", silent=false)   # Service Usage
2) GET {FIREBASE_FIREBASESTORAGE_URL}/v1alpha/projects/{projectId}/defaultBucket
   Authorization: Bearer <token>
   → يقرأ body.bucket.name ثم .split("/").pop()
```

- الأصل الافتراضي `https://firebasestorage.googleapis.com`، ويغيّره `FIREBASE_FIREBASESTORAGE_URL`. VERIFIED: `FT src/api.ts:148-149`.
- **الإصدار المستعمل هو `v1alpha` وليس `v1beta`.** VERIFIED: `FT src/gcp/storage.ts:199-205`.
- النوع المعلن للاستجابة: `{name, location, bucket:{name}}`. VERIFIED: `FT src/gcp/storage.ts:115-121`.
- الشكل الإنتاجي على الأرجح `{"name":"projects/{p}/defaultBucket","location":"US-CENTRAL1","bucket":{"name":"projects/{p}/buckets/{p}.firebasestorage.app"}}`. INFERENCE.
  - الأداة تأخذ آخر مقطع بعد `/`، فاسم bucket مجرد يعمل كذلك.

الأخطاء:

| الحالة | ما يحدث | المصدر |
|---|---|---|
| HTTP 404 | `Firebase Storage has not been set up on project '{p}'. Go to https://console.firebase.google.com/project/{p}/storage and click 'Get Started' to set up Firebase Storage.` | VERIFIED: `:214-219` |
| `bucket.name` فارغ | "Your project is being set up. Please wait a minute before deploying again." ثم يُعاد رميه بعد طباعة "Unexpected error when fetching default storage bucket." | VERIFIED: `:206-212,221-222` |
| غياب الكائن `bucket` كليًا | `TypeError` من `response.body?.bucket.name`، فيظهر "Unexpected error" | INFERENCE |

- **يجب أن يحتوي جسم Aruma دائمًا على `bucket`.**
- الأداة لا تستعمل `GET firebase.googleapis.com/v1beta1/projects/{p}` (`resources.storageBucket`) ولا `adminSdkConfig` في النشر أو التهيئة. VERIFIED: الاستدعاءان الوحيدان لـ `getDefaultBucket` في `FT src/deploy/storage/prepare.ts:38` و`FT src/init/features/storage/rules.ts:5`.

### 1.4 فحص تفعيل الـ API (`ensure`)

VERIFIED: `FT src/ensureApiEnabled.ts:28-50,65-74,112-183`.

- الاسم المفحوص هو `hostname` الأصل الممرَّر، أي مضيف `FIREBASE_FIREBASESTORAGE_URL`. VERIFIED: `:171`.
  - إن وُجّه المتغير إلى `https://aruma.example:8443` يصبح اسم الخدمة `aruma.example`.
- الطلبات:

  | الطلب | التفاصيل |
  |---|---|
  | `GET {FIREBASE_SERVICE_USAGE_URL}/v1/projects/{p}/services/{hostname}` | الترويسة `x-goog-user-project: {p}`. الشرط `body.state === "ENABLED"` |
  | `POST .../services/{hostname}:enable` | بلا جسم، إن لم يكن مفعّلًا |
  | `GET` المتكرر | استطلاع كل 10 ثوانٍ حتى 12 مرة. ثم إعادة `enable` مرة واحدة، ثم "Timed out waiting for API ... to enable" (`:11-14,141-155`) |

- الرسائل: "ensuring required API firebasestorage.googleapis.com is enabled..." ثم "required API ... is enabled" (`:172-174,43-45`).
- **ذاكرة مخبئية:** إذا ظهر الـ API مفعّلًا يُحفظ في configstore بالمفتاح `apiEnablementCache[projectId][apiName]`، ولا يُفحص مرة أخرى. VERIFIED: `:35-37,222-241`.
- أخطاء `enable`:
  - الفوترة تعطي رسالة Blaze.
  - `PERMISSION_DENIED` تعطي رابط console (`:76-109`).
- `FIREBASE_SERVICE_USAGE_URL` يغيّر الأصل. VERIFIED: `FT src/api.ts:154-155`.

### 1.5 كل طلبات `firebaserules.googleapis.com`

العميل: `new Client({urlPrefix: rulesOrigin(), apiVersion: "v1"})`، والأصل يغيّره `FIREBASE_RULES_URL`. VERIFIED: `FT src/gcp/rules.ts:6-8`، `FT src/api.ts:142-143`.

| # | الدالة | الطلب | الجسم | الحقول المقروءة من الاستجابة | المصدر |
|---|---|---|---|---|---|
| R1 | `testRuleset` | `POST /v1/projects/{encodeURIComponent(p)}:test` | `{"source":{"files":[{"name":"storage.rules","content":"..."}]}}` | `issues[].severity` و`issues[].sourcePosition.line/column` و`issues[].description` | `FT src/gcp/rules.ts:291-297`، `FT src/rulesDeploy.ts:306-338` |
| R2 | `listReleases` | `GET /v1/projects/{p}/releases?pageSize=10&pageToken=` (الصفحة الأولى بـ `pageToken` فارغ) | لا شيء | `releases[].name/rulesetName/createTime` و`nextPageToken` | `:47-66,85-96` |
| R3 | `getRulesetContent` | `GET /v1/projects/{p}/rulesets/{id}` | لا شيء | `source.files[]` | `:112-122` |
| R4 | `createRuleset` | `POST /v1/projects/{p}/rulesets` | `{"source":{"files":[...]}}`، ويضاف `"attachment_point"` (snake_case) لقاعدة Firestore المسمّاة | `name` | `:196-219` |
| R5 | `updateRelease` | `PATCH /v1/projects/{p}/releases/firebase.storage/{bucket}` | `{"release":{"name":"projects/{p}/releases/firebase.storage/{bucket}","rulesetName":"projects/{p}/rulesets/{id}"}}` بلا `updateMask` | `name` | `:255-277` |
| R6 | `createRelease` | `POST /v1/projects/{p}/releases` | `{"name":"projects/{p}/releases/firebase.storage/{bucket}","rulesetName":"..."}` | `name` | `:227-247` |
| R7 | `listRulesets` | `GET /v1/projects/{p}/rulesets?pageSize=100&pageToken=` | لا شيء | `rulesets[].name/createTime` و`nextPageToken` | `:124-162` |
| R8 | `deleteRuleset` | `DELETE /v1/projects/{p}/rulesets/{id}` | لا شيء | الحالة فقط | `:183-189` |

قواعد يجب احترامها:

- **كل دالة تتحقق من `response.status === 200` حرفيًا.** أي حالة 2xx أخرى، مثل 201 أو 204، تمر إلى `_handleErrorResponse` فترفض بـ "Unexpected error encountered with rules." VERIFIED: `FT src/gcp/rules.ts:10-19,62,116,140,185,213,241,271`.
  - **لذلك يرد Aruma بـ 200 على الإنشاء والحذف والتعديل، والحذف يعيد `{}`.**
- كل حالة 4xx أو 5xx يرميها `apiv2` قبل ذلك. VERIFIED: `FT src/apiv2.ts:604-623`.
  - الخطأ يصبح `Request to {url} had HTTP Error: {status}, {body.error.message}`. VERIFIED: `FT src/responseToError.ts:5-62`.
  - غلاف خطأ Google `{"error":{"code","message","status"}}` يكفي.
- **الترتيب يعتمد على `createTime`.** تُرتَّب الإصدارات والـ rulesets تنازليًا بـ `b.createTime.localeCompare(a.createTime)`. غياب `createTime` يرمي TypeError. VERIFIED: `FT src/gcp/rules.ts:95,161`.
  - **يجب أن يحتوي كل عنصر على `createTime` بصيغة RFC3339 قابلة للمقارنة نصيًا.**
- **`pageToken=` فارغ في الطلب الأول** لأن القيمة الافتراضية `""` تُضاف إلى URLSearchParams. VERIFIED: `FT src/gcp/rules.ts:54,131`، `FT src/apiv2.ts:403-416`.
- **R5 ثم R6:** `updateOrCreateRelease` تلتقط **أي** خطأ من PATCH، ثم تحاول POST. VERIFIED: `FT src/gcp/rules.ts:279-289`.
  - الإنتاج يرد 404 على PATCH لإصدار غير موجود. INFERENCE.
  - على Aruma أن يرد خطأ على PATCH لإصدار غير موجود، أو أن يُنشئه مباشرة (upsert). كلاهما يعمل مع الأداة.
- **R1 يُفهم كنجاح مع قائمة مشكلات.** الأداة تقرأ `body.issues` من استجابة 200 فقط.
  - كل `severity === "ERROR"` يصبح خطأً بصيغة `[E] line:column - description`. الأخطاء توقف النشر بـ `Compilation error(s) in storage.rules:` و`exit: 1`.
  - بقية المستويات تصبح تحذيرات `[W] ...`.
  - VERIFIED: `FT src/rulesDeploy.ts:309-335`.
  - في الإنتاج يكون `TestRulesetResponse.issues[]` بحقول `sourcePosition{fileName,line,column,currentOffset,endOffset}` و`description` و`severity`، بقيم `SEVERITY_UNSPECIFIED|DEPRECATION|WARNING|ERROR`. INFERENCE من وثائق API، تؤيده واجهة `Issue` في `FT src/mcp/tools/core/validate_security_rules.ts:12-24`.
  - إن ردّ Aruma بـ 400 بدل 200 مع `issues`، تطبع الأداة رسالة الخطأ الخام بلا تنسيق السطر والعمود.
- **R3 ثم R4 (عدم تكرار الرفع).** تُختار أول release يبدأ اسمها بـ `projects/{p}/releases/firebase.storage`، بلا لاحقة bucket في مسار النشر، بعد الترتيب التنازلي. ثم يُقرأ محتواها.
  - إن تطابق `files` مع `_.isEqual` يُعاد استعمال اسم الـ ruleset وتظهر "latest version of storage.rules already up to date, skipping upload...". VERIFIED: `FT src/rulesDeploy.ts:186-201`، `FT src/gcp/rules.ts:28-45`.
  - **المقارنة تشمل `name` و`content` لكل ملف.** إن أعاد Aruma حقلًا إضافيًا مثل `fingerprint` داخل `files[]` يفشل التطابق ويُرفع ruleset جديد في كل نشر، وهذا غير ضار. INFERENCE.
  - المطابقة بـ `startsWith` قد تخلط bucket باسم يبدأ باسم bucket آخر في `init` (القسم 2)، فتختار release خاطئًا. VERIFIED: `FT src/gcp/rules.ts:34-39`.
- **R7 وR8 (تنظيف الحصة)** يُستدعيان فقط إذا أعاد R4 الحالة **429**. VERIFIED: `FT src/rulesDeploy.ts:15-22,231-260`.
  - الشرط عدد rulesets أكبر من **1000**، وليس 2500.
  - تسأل الأداة: "You have N rules, do you want to delete the oldest 10 to free up space?"
  - تحذف أقدم 10 rulesets غير مُطلقة، واحدًا تلو الآخر، ثم تعيد المحاولة كاملة.
  - إن كان العدد 1000 أو أقل يُبتلع الخطأ وتُعاد قائمة فارغة، فيفشل الإطلاق لاحقًا لأن `rulesetNames[key]` غير معرَّف. INFERENCE من `:239-261`.
- **الترويسات:** `Authorization: Bearer <access token>` و`User-Agent: FirebaseCLI/15.34.0` و`X-Client-Version`. VERIFIED: `FT src/apiv2.ts:20-32,374-386`.
  - **إن كان الأصل `http:`** ترسل الأداة `Bearer owner` بدل الرمز. VERIFIED: `FT src/apiv2.ts:381-382,684-687`.
  - Aruma يرفض `Bearer owner` (ADR-010 S-3)، فيلزم https. انظر `docs/research/experiments.md`.

### 1.6 فحص صلاحية القواعد العابرة للخدمات (cross-service)

VERIFIED: `FT src/rulesDeploy.ts:121-161,202-204`.

- يعمل فقط إذا تحققت ثلاثة شروط معًا:
  - المحتوى يطابق `/firestore\.(get|exists)/`.
  - الوضع تفاعلي.
  - الـ ruleset سيُرفع فعلًا.
- الطلبات بالترتيب:
  1. `getProjectNumber` يرسل `GET {FIREBASE_API_URL}/v1beta1/projects/{p}` ويقرأ `projectNumber`. VERIFIED: `FT src/getProjectNumber.ts:8-16`.
  2. `POST {FIREBASE_RESOURCEMANAGER_URL}/v1/projects/{number}:getIamPolicy` بلا جسم. VERIFIED: `FT src/gcp/resourceManager.ts:27-32,136-167`.
     - يبحث عن binding للدور `roles/firebaserules.firestoreServiceAgent` يحتوي العضو `serviceAccount:service-{number}@gcp-sa-firebasestorage.iam.gserviceaccount.com`.
  3. إن لم يجده يسأل "Cloud Storage for Firebase needs an IAM Role to use cross-service rules. Grant the new role?".
     - عند الموافقة يرسل `getIamPolicy` ثم `POST .../projects/{number}:setIamPolicy` بالجسم `{"policy":{...},"updateMask":"bindings"}`، مع إعادة المحاولة حتى 7 مرات. VERIFIED: `FT src/gcp/resourceManager.ts:42-55,95-130`.
- **أي خطأ هنا يتحول إلى تحذير فقط:** "Cross-service Storage rules may not function properly". VERIFIED: `:155-160`.
- خيارات Aruma:
  - يُرجع policy فيها الـ binding المطلوب، فلا يظهر السؤال.
  - أو يرد خطأ، فيظهر تحذير والنشر يكمل.

### 1.7 رسائل النشر الناجح

VERIFIED: `FT src/rulesDeploy.ts:197,206,295-298,307,337`.

```text
i  firebase.storage: checking storage.rules for compilation errors...
✔  firebase.storage: rules file storage.rules compiled successfully
i  storage: uploading rules storage.rules...
✔  storage: released rules storage.rules to firebase.storage
```

- العنوان الأول يستعمل `this.type` (`firebase.storage`)، والباقي يستعمل `RulesetType` (`storage`).
- رسالة الإطلاق لا تذكر اسم الـ bucket.

## 2. `firebase init storage`

- الاختيار في قائمة init نصّه "Storage: Configure a security rules file for Cloud Storage". VERIFIED: `FT src/commands/init.ts:61-62`، `FT src/init/index.ts:104-106`.

### 2.1 الأسئلة والملفات

VERIFIED: `FT src/init/features/storage/index.ts:17-66`.

1. يطبع نصًا تعريفيًا عن قواعد Storage (`:21-25`).
2. **إن كان هناك `projectId`** يستدعي `getRulesFromConsole(projectId)` بلا `try/catch` (`:33-39`). فأي خطأ هنا يوقف التهيئة.
   - إن وُجدت قواعد منشورة: "Downloaded the existing Storage Security Rules from the Firebase console".
3. يسأل "What file should be used for Storage Rules?"، والافتراضي `storage.rules` (`:41-44`).
4. `confirmWriteProjectFile` يسأل عن الاستبدال إن وُجد الملف (`:45`).
5. actuate:
   - يكتب في `firebase.json` القيمة `"storage": {"rules": "<الملف>"}`، **بالشكل المفرد دائمًا وبلا `bucket`**.
   - يكتب ملف القواعد (`:59-65`).

القالب الافتراضي حرفيًا (VERIFIED: `FT templates/init/storage/storage.rules:1-12`):

```text
rules_version = '2';

// Craft rules based on data in your Firestore database
// allow write: if firestore.get(
//    /databases/(default)/documents/users/$(request.auth.uid)).data.isAdmin;
service firebase.storage {
  match /b/{bucket}/o {
    match /{allPaths=**} {
      allow read, write: if false;
    }
  }
}
```

### 2.2 الطلبات (`getRulesFromConsole`)

VERIFIED: `FT src/init/features/storage/rules.ts:4-27`.

```text
1) ensure(...)                              # Service Usage، كما في 1.4
2) GET /v1alpha/projects/{p}/defaultBucket  # firebasestorage
3) GET /v1/projects/{p}/releases?pageSize=10&pageToken=   (كل الصفحات)
4) أول release يبدأ بـ projects/{p}/releases/firebase.storage/{defaultBucket}
5) GET /v1/projects/{p}/rulesets/{id}       # إن وُجد
```

- إن لم يوجد release يُستعمل القالب.
- إن كان الـ ruleset بلا ملفات: "Ruleset has no files". وإن كان فيه أكثر من ملف: "Ruleset has too many files: N". كلاهما بـ `exit 1`.
- **عند غياب الـ bucket الافتراضي** (HTTP 404 في الخطوة 2) تفشل التهيئة كلها برسالة "Firebase Storage has not been set up on project '{p}'. Go to https://console.firebase.google.com/project/{p}/storage and click 'Get Started' to set up Firebase Storage." VERIFIED: `FT src/gcp/storage.ts:214-219` مع غياب الالتقاط في `index.ts:33-39`.
  - هذه هي رسالة "Set up Firebase Storage in the console" المقصودة.
  - **على Aruma أن يُرجع defaultBucket دائمًا لأي مشروع فيه Storage مفعّل.**
- بلا مشروع (اختيار "Don't set up a default project") لا تُرسل أي طلبات، ويُكتب القالب مباشرة.

## 3. نقاط تماس أخرى مع Storage في الأداة (موجز)

| الموضع | ما يُرسل أو يُقرأ | المصدر |
|---|---|---|
| `emulators:start --only storage` | المفاتيح `emulators.storage.{host,port}`، والمنفذ الافتراضي 9199 | `FT src/firebaseConfig.ts:310-313`، `FT src/emulator/constants.ts:15` |
| إعداد قواعد المحاكي | `storage.rules`. في شكل المصفوفة **يُشترط `target`** ويُتجاهل `bucket`: "Must supply 'target' in Storage configuration"، بخلاف النشر. مشاريع `demo-*` بلا إعداد تأخذ قواعد مفتوحة | `FT src/emulator/storage/rules/config.ts:22-80` |
| متغيرات بيئة المحاكي | `FIREBASE_STORAGE_EMULATOR_HOST=host:port` و`STORAGE_EMULATOR_HOST=http://host:port` | `FT src/emulator/env.ts:28-31` |
| `GET /b` في المحاكي | إن لم يوجد bucket يطلب `GET {FIREBASE_API_URL}/v1beta1/projects/{p}/adminSdkConfig` ويقرأ `storageBucket` | `FT src/emulator/storage/files.ts:134-144`، `FT src/emulator/adminSdkConfig.ts:56-82` |
| `adminSdkConfig` عند غياب الرد | الافتراض `{p}.appspot.com` | `FT src/emulator/adminSdkConfig.ts:22-29,62-71` |
| `apps:sdkconfig` | `GET /v1beta1/projects/-/webApps/{appId}/config`. الحقل `storageBucket` يظهر في الإعداد كما هو | `FT src/management/apps.ts:563-579,645-650` |
| init.js في Hosting | `storageBucket` من `getAppConfig` في `fetchWebSetup`، والافتراض `{p}.appspot.com` | `FT src/fetchWebSetup.ts:84-90` |
| نشر Functions | `GET /v1beta1/projects/{p}/adminSdkConfig`، ثم `storageBucket` يصبح معامل `STORAGE_BUCKET` | `FT src/functionsConfig.ts:115-121`، `FT src/deploy/functions/params.ts:494-499` |
| مشغّلات Storage في Functions | `GET {FIREBASE_STORAGE_URL}/storage/v1/b/{bucket}`، ويُقرأ `location` لتحديد منطقة المشغّل. الخطأ: "Can't find the storage bucket region" | `FT src/deploy/functions/services/storage.ts:26-34,58-107`، `FT src/gcp/storage.ts:329-342` |
| ربط IAM لمشغّلات Storage | `GET {FIREBASE_STORAGE_URL}/storage/v1/projects/{number}/serviceAccount` ويُقرأ `email_address`، مع تفعيل `storage.googleapis.com` | `FT src/deploy/functions/services/storage.ts:41-50`، `FT src/gcp/storage.ts:555-568`، `FT src/deploy/functions/prepare.ts:1022-1034` |
| Extensions | `GET {firebasestorage}/v1beta/projects/{p}/buckets` بالشكل `{buckets:[{name:"projects/{n}/buckets/{id}"}]}`، ويُطابَق الاسم مع `^{p}(...)*.(appspot.com|firebasestorage.app)$`. كذلك `STORAGE_BUCKET` من `adminSdkConfig` | `FT src/extensions/provisioningHelper.ts:125-136`، `FT src/extensions/extensionsHelper.ts:120-158` |
| أدوات MCP | `get_security_rules` تقرأ release `firebase.storage` بلا bucket، أي أول تطابق. `validate_security_rules` ترسل `:test` باسم الملف `test.rules`. `get_download_url` ترسل `GET {firebasestorage}/v0/b/{b}/o/{enc}` وتقرأ `downloadTokens` | `FT src/mcp/tools/core/get_security_rules.ts:52-63`، `FT src/mcp/tools/core/validate_security_rules.ts:146-153`، `FT src/gcp/storage.ts:576-600` |
| `firebase projects:list` و`init` | لا يقرآن `resources.storageBucket` لأي منطق Storage. الحقل معرَّف في النوع فقط | VERIFIED: `FT src/types/project/index.d.ts:23` وغيابه عن مسارات init |

## 4. Firestore على نفس الواجهة

- `RulesetServiceType.CLOUD_FIRESTORE = "cloud.firestore"`. VERIFIED: `FT src/rulesDeploy.ts:33-36`.
- اسم الإصدار:
  - `cloud.firestore` للقاعدة `(default)`.
  - `cloud.firestore/{databaseId}` للقاعدة المسمّاة.
  - VERIFIED: `FT src/rulesDeploy.ts:285-288`، `FT src/deploy/firestore/release.ts:18-21`.
- لقاعدة مسمّاة يضاف `"attachment_point": "firestore.googleapis.com/projects/{projectNumber}/databases/{db}"` إلى جسم R4. وهذا يستلزم أولًا `GET /v1beta1/projects/{p}` لرقم المشروع. VERIFIED: `FT src/rulesDeploy.ts:208-216`، `FT src/gcp/rules.ts:201-206`.
- مفتاح ruleset في Firestore هو `{filename}:{databaseId}`، بينما في Storage هو اسم الملف وحده. VERIFIED: `FT src/rulesDeploy.ts:189-192`.
- البحث عن آخر ruleset بـ `startsWith("…/releases/cloud.firestore")` يطابق أيضًا `cloud.firestore/{db}`. هذا عيب في الأداة لا يضر غالبًا. VERIFIED: `FT src/gcp/rules.ts:34-39`.
- **الخلاصة:** نفس endpoints الثمانية R1 إلى R8 تخدم Firestore وStorage. الفرق في اسم الإصدار والخدمة داخل `service ...` في النص.
  - قواعد RTDB لا تمر بـ firebaserules.

## 5. ما يجب أن يفعله خادم Aruma

### 5.1 متغيرات البيئة للأداة

| المتغير | الغرض |
|---|---|
| `FIREBASE_RULES_URL=https://<aruma>` | R1 إلى R8 |
| `FIREBASE_FIREBASESTORAGE_URL=https://<aruma>` | defaultBucket، ويحدد أيضًا اسم الخدمة في Service Usage |
| `FIREBASE_SERVICE_USAGE_URL=https://<aruma>` | لتعمل `ensure`، وإلا ذهب الطلب إلى Google |
| `FIREBASE_SKIP_INFORMATIONAL_IAM=1` | أو `FIREBASE_RESOURCEMANAGER_URL` مع `testIamPermissions` |
| `FIREBASE_API_URL` | مُعد مسبقًا لواجهة الإدارة؛ يُستعمل لرقم المشروع في cross-service وFirestore المسمّاة |
| `FIREBASE_STORAGE_URL` | فقط لمشغّلات Functions على Storage (`storage/v1/b/{bucket}`) |

- كلها **https**، لأن http يرسل `Bearer owner` (القسم 1.5).

### 5.2 قائمة المهام

- [ ] **`GET /v1alpha/projects/{p}/defaultBucket`** يرد 200 بالجسم:

  ```json
  {"name": "projects/{p}/defaultBucket", "location": "<loc>", "bucket": {"name": "projects/{p}/buckets/{bucketId}"}}
  ```

  - الكائن `bucket` لا يغيب أبدًا.
  - 404 بغلاف Google إن لم يُفعَّل Storage للمشروع.
  - `{bucketId}` يطابق `storageBucket` في `WebAppConfig` و`adminSdkConfig` وفي الـ bucket الذي يخدمه `/v0/b/{bucket}`.
- [ ] **`GET /v1/projects/{p}/services/{name}`** يرد `{"name":"projects/{p}/services/{name}","state":"ENABLED"}` لأي `{name}`، بما فيه مضيف Aruma نفسه.
  - `POST .../{name}:enable` اختياري، ويكفي أن يرد 200 بعملية منتهية.
- [ ] **`POST /v1/projects/{p}:test`** يرد 200 بالجسم `{"issues":[...]}`، أو `{}` إن لم توجد مشكلات.
  - يترجم قواعد `service firebase.storage` و`service cloud.firestore`.
  - كل مشكلة بالشكل `{"sourcePosition":{"fileName","line","column","currentOffset","endOffset"},"description","severity":"ERROR"|"WARNING"}`.
  - يقبل `projectId` مرمّزًا في المسار.
- [ ] **`GET /v1/projects/{p}/releases`** يقبل `pageSize` و`pageToken` فارغًا.
  - يرد `{"releases":[{"name","rulesetName","createTime","updateTime"}],"nextPageToken"?}`.
  - `createTime` إلزامي.
- [ ] **`GET /v1/projects/{p}/rulesets/{id}`** يرد `{"name","source":{"files":[{"name","content"}]},"createTime"}`.
  - يُستحسن ألا يضيف `fingerprint` داخل الملفات، حتى ينجح كشف "already up to date". اختياري.
- [ ] **`POST /v1/projects/{p}/rulesets`** يرد **200** بالجسم `{"name":"projects/{p}/rulesets/{uuid}","source":...,"createTime"}`.
  - يرفض المصدر غير القابل للترجمة بـ 400.
  - يقبل `attachment_point` لـ Firestore.
  - يرد 429 فقط عند حد حقيقي، إن طُبّق.
- [ ] **`PATCH /v1/projects/{p}/releases/{releaseId...}`**: الـ `releaseId` قد يحتوي `/`، مثل `firebase.storage/{bucket}` و`cloud.firestore/{db}`. الراوتر يجب أن يقبل ذلك.
  - الجسم `{"release":{"name","rulesetName"}}` بلا `updateMask`.
  - يرد **200** بالـ Release، أو 404 إن لم يوجد.
- [ ] **`POST /v1/projects/{p}/releases`** بالجسم `{"name","rulesetName"}` يرد **200** بالـ Release.
  - يرد 409 إن كان الإصدار موجودًا. INFERENCE.
- [ ] **`GET /v1/projects/{p}/rulesets`** و**`DELETE /v1/projects/{p}/rulesets/{id}`**: الحذف يرد **200** بالجسم `{}`، ويرفض حذف ruleset مُطلق. يكفي تنفيذهما إن كان Aruma يرد 429.
- [ ] **ربط الإصدار بالخدمة:** خادم Storage في Aruma يقرأ القواعد الفعالة للـ bucket `B` من release `projects/{p}/releases/firebase.storage/B` لحظة التقييم، أو عند تغيّر الإصدار.
  - وخادم Firestore يقرأ `cloud.firestore` و`cloud.firestore/{db}`.
- [ ] **`POST /v1/projects/{p}:testIamPermissions`**، إن لم يُضبط `FIREBASE_SKIP_INFORMATIONAL_IAM`: يرد `{"permissions":[...المطلوبة الممنوحة]}`. الفشل غير قاتل.
- [ ] **اختياري للقواعد العابرة:** `POST /v1/projects/{n}:getIamPolicy` يرد policy فيها `roles/firebaserules.firestoreServiceAgent` لعضو `service-{n}@gcp-sa-firebasestorage.iam.gserviceaccount.com`، أو يرد خطأ فيظهر تحذير فقط.
- [ ] **الأخطاء:** غلاف `{"error":{"code":<http>,"message":"...","status":"NOT_FOUND|INVALID_ARGUMENT|..."}}`. الأداة تقرأ `message` والحالة فقط (`FT src/responseToError.ts:41-61`).

### 5.3 غير مرصود أو غير مؤكد

- طلبات Firebase console (تبويب Storage ومحرر القواعد): لم تُرصد.
- شكل `defaultBucket` الإنتاجي الكامل (`location` و`storageClass`؟)، وسلوك `:test` الإنتاجي مع أخطاء الترجمة (200 مع `issues` أم 400): INFERENCE حتى تؤكدهما التسجيلات.
