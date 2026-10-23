# المسار B: بروتوكول GCS JSON API كما تستعمله Admin SDKs (Node وGo) (بحث)

- **التاريخ:** 2026-10-23. بحث فقط: لم يُبنَ شيء، ولم يُستدعَ أي API لـ Google، ولم تُلمس أي اعتمادات (credentials).
- **السؤال:** ما طلبات HTTP التي ترسلها `@google-cloud/storage` (Node) و`cloud.google.com/go/storage` (Go) حين يستدعي المستخدم `admin.storage().bucket().file().save()/download()/getMetadata()/...`، وما الردود التي تنتظرها؟ الهدف أن يخدمها خادم Aruma دون تعديل في الشيفرة.
- **الطريقة:** قراءة الشيفرة المصدرية سطرًا بسطر. يُوسَم كل ادعاء بـ **[V]** إن قرأناه في الشيفرة، وبـ **[I]** إن كان استنتاجًا أو سلوكًا لخادم Google لا تُظهره الشيفرة. ملاحظات الالتقاط من `test/oracle/storage/emulator/*` شُرحت في §9.

## 0. المصادر والرخص

| الاختصار | المصدر | الإصدار / الـ commit | الرخصة | المسار المقروء |
|---|---|---|---|---|
| `GCSNODE` | `@google-cloud/storage` (npm pack، شيفرة مترجمة لكنها مقروءة) | 8.4.0 (firebase-admin-node يطلب `^8.1.0`) | Apache-2.0 | `.cache/research/src/s0c/gcs-node/package/build/cjs/src` |
| `TEENY` | `teeny-request` (طبقة HTTP التي تستعملها GCSNODE في الطلبات غير القابلة للاستئناف) | 11.1.1 (GCSNODE يطلب `^11.0.1`)، من registry.npmjs.org | Apache-2.0 | `.cache/research/src/s0c/teeny-request/package/build/src/index.js` |
| `GCSGO` | `cloud.google.com/go/storage` (zip من proxy.golang.org) | v1.62.1 | Apache-2.0 | `.cache/research/src/s0c/gcs-go/cloud.google.com/go/storage@v1.62.1` |
| `GAPI` | `google.golang.org/api` (zip من proxy.golang.org) | v0.274.0 (من go.mod الخاص بـ GCSGO). firebase-admin-go يرفعه إلى v0.279.0. قارنّا النسختين: `internal/gensupport/*` و`googleapi/googleapi.go` و`transport/http/dial.go` متطابقة بايتًا ببايت، و`storage/v1/storage-gen.go` يختلف بتعليق وبحقل كاش في Bucket فقط (إزاحة +3 أسطر بعد السطر 386) | BSD-3-Clause | `.cache/research/src/s0c/gapi/google.golang.org/api@v0.274.0` (والنسخة الأخرى في `s0c/gapi-0.279`) |
| `FAN` | firebase-admin-node | 14.5.0، commit `08a3963555237e95d37fc83051a73dc2662cf568` | Apache-2.0 | `.cache/research/src/firebase-admin-node/src/storage` |
| `FAG` | firebase-admin-go | commit `66a72c371b4dbc592317ab7e7f99ce00d76c62ae` (`go.mod`: storage v1.62.1، api v0.279.0) | Apache-2.0 | `.cache/research/src/firebase-admin-go/{storage,firebase.go,internal}` |
| `FTOOLS` | firebase-tools (للتحقق المتقاطع مع ما التُقط فقط) | 15.34.0، `4e6eb923dd5e4a081eb227baaee58df85c17d044` | MIT | `src/emulator/storage/apis/{gcloud,shared}.ts` |

صيغة الاستشهاد: `GCSNODE file.js:1139` تعني `build/cjs/src/file.js` السطر 1139، و`GCSNODE common/util.js` تعني `nodejs-common/util.js`، و`GCSNODE util.js` تعني `src/util.js`. ونكتب `GAPI resumable.go` اختصارًا لـ `internal/gensupport/resumable.go`، و`GAPI gen` لـ `storage/v1/storage-gen.go`.
التنزيلات الثلاثة (`gapi` و`gapi-0.279` و`teeny-request`) مسجّلة في `.cache/research/src/s0c/PROVENANCE.md` مع بقية النسخ.

## 1. الخلاصة في عشرة أسطر

1. **Node** يرسل عمليات البيانات الوصفية إلى `{baseUrl}/b/{bucket}/o/{encodeURIComponent(name)}`. عند ضبط `STORAGE_EMULATOR_HOST` تكون **`baseUrl` قيمةَ المتغير كما هي، دون `/storage/v1`**. الرفع يذهب دائمًا إلى `{apiEndpoint}/upload/storage/v1/b/{bucket}/o` [V] (`GCSNODE storage.js:474`، `file.js:3604`، `resumable-upload.js:149`).
2. **Go** يرسل JSON إلى `{endpoint}/storage/v1/b/...` مع `alt=json&prettyPrint=false&projection=full`. **أما القراءة (`NewReader`) فتذهب افتراضيًا إلى XML API**: `GET {scheme}://{host}/{bucket}/{PathEscape(object)}` [V] (`GCSGO http_client.go:878-891`، `option.go:121-146`).
3. **`file.save()` و`bucket.upload()` في Node يستعملان resumable دائمًا** ما لم يمرّر المستخدم `resumable:false`. لا يوجد حد 5 MiB في 8.4.0 [V] (`GCSNODE file.js:1569-1573`، `bucket.js:3490-3600`). الوضع الافتراضي طلب `PUT` واحد مع `Content-Range: bytes 0-*/*` وجسم chunked.
4. **Go** يختار `uploadType=multipart` إذا كان المحتوى أصغر من `ChunkSize` (الافتراضي 16 MiB)، وإلا `resumable`. يرسل أجزاء resumable بـ **`POST`** (لا `PUT`) مع `X-GUploader-No-308: yes`، وينتظر **`200` مع `X-Http-Status-Code-Override: 308`**. إن جاءه 308 حقيقي فشل [V] (`GAPI media.go:194-199`، `resumable.go:75,111,121-125,281-283`).
5. Node في وضع الأجزاء (`chunkSize`) ينتظر **`308` حقيقيًا** مع `Range: bytes=0-N` [V] (`GCSNODE resumable-upload.js:673-699`). على الخادم إذن أن يفرّق بين العميلين بالترويسة `X-GUploader-No-308`.
6. يجب أن يحتوي رد الرفع النهائي على `crc32c` صحيح: Base64 قياسي لأربعة بايتات big-endian. إن غاب أو اختلف **حذف Node الكائن** ورمى `FILE_NO_UPLOAD` [V] (`GCSNODE file.js:3692-3730`). ويرسل Go قيمة `crc32c` التي حسبها في JSON الـ multipart، أو في `X-Goog-Hash` مع آخر جزء [V].
7. يجب أن تكون الحقول `generation` و`metageneration` و`size` (و`totalBytesRewritten` و`objectSize` و`projectNumber`) **سلاسل JSON**. وسم Go `,string` يرفض الأرقام [V] (`GAPI gen:2385,2410,2433,2943,2954,534`).
8. **المصادقة:** في وضع المحاكي لا يرسل أي من العميلين ترويسة `Authorization`. وفي Node مع `apiEndpoint` مخصص لا تُرسَل المصادقة أيضًا ما لم يُضبط `useAuthWithCustomEndpoint:true`. وحتى مع هذا الخيار **تخرج طلبات resumable من `file.save()` بلا مصادقة**، لأن `startResumableUpload_` لا يمرّر الخيار. هذا خطأ في العميل [V] (`GCSNODE file.js:3538-3562` مقابل `file.js:1293`، `resumable-upload.js:137-145`).
9. تنزيل Node يتحقق من CRC32C **فقط** إن أرسل الخادم `x-goog-stored-content-encoding: identity`. وإن أرسلها دون `x-goog-hash` صحيح فشل التنزيل بـ `CONTENT_DOWNLOAD_MISMATCH` [V] (`GCSNODE file.js:1097-1130`، `hash-stream-validator.js:74-100`).
10. `getDownloadURL()` في FAN يطلب `GET {STORAGE_EMULATOR_HOST|https://firebasestorage.googleapis.com}/v0/b/{bucket}/o/{enc}`، ثم يقرأ `downloadTokens` ويبني الرابط `...?alt=media&token=...` [V] (`FAN index.ts:74-90`). لا يوجد مقابل له في FAG [V، بحث نصي].

## 2. اختيار العنوان والبروتوكول (http/https)

### 2.1 Node (`GCSNODE storage.js:457-474`)

```js
let apiEndpoint = `https://storage.${universe}`;            // 460
const EMULATOR_HOST = process.env.STORAGE_EMULATOR_HOST;    // 463
if (typeof EMULATOR_HOST === 'string') { apiEndpoint = Storage.sanitizeEndpoint(EMULATOR_HOST); customEndpoint = true; }
if (options.apiEndpoint && options.apiEndpoint !== apiEndpoint) { apiEndpoint = sanitizeEndpoint(options.apiEndpoint); customEndpoint = true; }
const baseUrl = EMULATOR_HOST || `${options.apiEndpoint}/storage/v1`; // 474
```

- `sanitizeEndpoint` تضيف `https://` إن غاب البروتوكول وتحذف الشرطات المائلة في آخر العنوان [V] (`storage.js:525-530`، `PROTOCOL_REGEX` في `storage.js:49`). أما `baseUrl` فتأخذ قيمة المتغير **الخام**.
- **FAN** يحوّل `FIREBASE_STORAGE_EMULATOR_HOST=host:port` إلى `STORAGE_EMULATOR_HOST=http://host:port`، بشرط ألا يكون `STORAGE_EMULATOR_HOST` مضبوطًا مسبقًا. ويرفض أي قيمة تحتوي بروتوكولًا (`invalid-emulator-host`) [V] (`FAN storage.ts:48-59`). النتيجة أن `FIREBASE_STORAGE_EMULATOR_HOST` تعني **http دائمًا**. من يريد https يضبط `STORAGE_EMULATOR_HOST=https://host` بنفسه [I، من الشيفرة نفسها].
- FAN لا يمرّر `apiEndpoint` ولا `useAuthWithCustomEndpoint`. يمرّر فقط `projectId` و`credentials{private_key, client_email}`، أو لا شيء مع ADC [V] (`FAN storage.ts:73-95`). **الطريق الوحيد إلى Aruma عبر FAN هو متغير البيئة.**
- إن ضُبط `STORAGE_EMULATOR_HOST=localhost:9199` دون بروتوكول صارت `apiEndpoint=https://localhost:9199` وبقيت `baseUrl="localhost:9199"`. يفشل الطلب عندها لأن `new URL` لا يجد بروتوكولًا صالحًا [I]. وإن احتوت القيمة مسارًا مثل `http://h/storage/v1` صار الرفع إلى `http://h/storage/v1/upload/storage/v1/...` [V، بالتركيب].

| الوضع | JSON (بيانات وصفية/قائمة/حذف/نسخ) | الرفع | تنزيل الوسائط | المصادقة |
|---|---|---|---|---|
| إنتاج | `https://storage.googleapis.com/storage/v1/b/...` | `https://storage.googleapis.com/upload/storage/v1/b/{b}/o` | `https://storage.googleapis.com/storage/v1/b/{b}/o/{enc}?alt=media` | `Bearer` |
| `FIREBASE_STORAGE_EMULATOR_HOST=127.0.0.1:9199` | `http://127.0.0.1:9199/b/{b}/o/{enc}?…` (**بلا `/storage/v1`**) | `http://127.0.0.1:9199/upload/storage/v1/b/{b}/o?…` | `http://127.0.0.1:9199/b/{b}/o/{enc}?alt=media` | لا شيء |
| `new Storage({apiEndpoint:'http://h:p'})` | `http://h:p/storage/v1/b/...` | `http://h:p/upload/storage/v1/b/{b}/o` | `http://h:p/storage/v1/b/{b}/o/{enc}?alt=media` | لا شيء، إلا مع `useAuthWithCustomEndpoint:true` (انظر §3) |

**النتيجة لـ Aruma:** يجب أن يخدم المسارين `/b/...` و`/storage/v1/b/...` كليهما. المحاكي يفعل ذلك أيضًا [V] (`FTOOLS gcloud.ts:97-101,147,173`).

### 2.2 Go (`GCSGO storage.go:150-233`، ونسخة مطابقة في `http_client.go:63-157`)

- **إنتاج:** النموذج `https://storage.UNIVERSE_DOMAIN/storage/v1/` يصير `https://storage.googleapis.com/storage/v1/`. ويضيف العميل النطاقات `devstorage.full_control` و`cloud-platform` و`User-Agent: gcloud-golang-storage/1.62.1` [V] (`storage.go:81,160-168`).
- **`STORAGE_EMULATOR_HOST`:** إن لم يحتو المتغير `://` يُبنى `http://{host}`، ويُضبط المسار إلى `storage/v1/`. يُضاف أيضًا `option.WithoutAuthentication()` و`SkipDialSettingsValidation()`، ولهذا لا يفشل الإنشاء حتى لو مرّر المستخدم ملف اعتمادات [V] (`storage.go:176-201`، `GAPI internal/settings.go:166-173`). يمكن أن يكون البروتوكول https إن كتبه المستخدم صراحةً [V] (`storage.go:179-185`).
- **FAG** ينسخ `FIREBASE_STORAGE_EMULATOR_HOST` إلى `STORAGE_EMULATOR_HOST` كما هي، بلا فحص للبروتوكول [V] (`FAG storage/storage.go:38-40`).
- **`option.WithEndpoint`:** القيمة التي تحتوي `://` تُستعمل كما هي ويجب أن تنتهي بـ `/storage/v1/`. وقيمة بصيغة `host[:port]` تُدمج مع النموذج الافتراضي [V] (`GAPI internal/cba.go:164-186`). يأخذ مضيف XML وبروتوكوله من نقطة النهاية المختارة [V] (`storage.go:216-231`).
- **FAG:** `firebase.NewApp(ctx, cfg, opts...)` يمرّر **الخيارات نفسها لكل الخدمات** (Auth وDB وFirestore وStorage...) بعد `WithScopes(FirebaseScopes...)` [V] (`FAG firebase.go:194-216,99-105`). معنى ذلك أن `option.WithEndpoint` على مستوى التطبيق يحوّل كل الخدمات معًا. لـ Storage وحدها يُنشئ المستخدم `storage.NewClient(ctx, option.WithEndpoint(...), ...)` بنفسه [I].
- **مسار الرفع مطلق:** `ResolveRelative(base, "/upload/storage/v1/b/{bucket}/o")` يُسقط أي بادئة مسار في الـ endpoint [V] (`GAPI gen:11145`، `googleapi.go:377-400`).

| الوضع | JSON | الرفع | القراءة (XML افتراضيًا) | المصادقة |
|---|---|---|---|---|
| إنتاج | `https://storage.googleapis.com/storage/v1/b/...?alt=json&prettyPrint=false&projection=full` | `https://storage.googleapis.com/upload/storage/v1/b/{b}/o?alt=json&name=…&prettyPrint=false&projection=full&uploadType=…` | `https://storage.googleapis.com/{b}/{PathEscape(o)}` | `Bearer` |
| `STORAGE_EMULATOR_HOST=127.0.0.1:9199` | `http://127.0.0.1:9199/storage/v1/b/...` | `http://127.0.0.1:9199/upload/storage/v1/b/{b}/o?…` | `http://127.0.0.1:9199/{b}/{o مع %2F}` | لا شيء |
| `WithEndpoint("https://h/storage/v1/")` + اعتمادات | `https://h/storage/v1/b/...` | `https://h/upload/storage/v1/...` | `https://h/{b}/{o}` | `Bearer` من حساب الخدمة |

### 2.3 ترميز اسم الكائن

| العميل | الموضع | الترميز |
|---|---|---|
| Node JSON | معرّف الكائن | `encodeURIComponent(name)`: الحرف `/` يصير `%2F`، والمسافة `%20`، وتبقى `!'()*-._~` كما هي [V] (`GCSNODE file.js:603`). اسم الـ bucket يُرسَل **غير مرمّز** (`bucket.js:769-770`، `file.js:3604`) |
| Node rewrite | المصدر والوجهة | `encodeURIComponent` لكل اسم [V] (`file.js:910`) |
| Node رفع | `name` في الاستعلام | الرفع البسيط يستعمل `querystring.stringify`، فتصير المسافة `%20` [V] (`TEENY index.js:349-353`). الرفع resumable يمر عبر `gaxios params` فيُرمَّز بـ `URLSearchParams`، فتصير المسافة `+` [I] (`resumable-upload.js:445-448`) |
| Go JSON | `{object}` في مسار URI template | يُرمَّز كل حرف خارج `A-Za-z0-9-._~`، فيصير `/` بالشكل `%2F` [V] (`GAPI internal/third_party/uritemplates/uritemplates.go:20,51-56`، `googleapi.go:406-412`) |
| Go XML | مسار القراءة | `RawPath: "/%s/%s", bucket, url.PathEscape(object)`، فيصير `/` بالشكل `%2F`، وهذا في الإنتاج أيضًا [V] (`GCSGO http_client.go:887-891`) و[I] لسلوك `url.PathEscape` |
| Go رفع | `name` في الاستعلام | `url.Values.Encode`، فتصير المسافة `+` [I] |

**النتيجة:** على Aruma أن يفك ترميز المسار مرة واحدة (`%2F` تصير `/`)، وأن يقرأ الاستعلام بقواعد النماذج (`+` = مسافة). `r.URL.Query()` في Go يفعل ذلك. أما المسار فيجب أن يُقرأ من `EscapedPath()` ويُقسَّم **قبل** فك الترميز، وإلا انكسرت الأسماء التي تحتوي `/`.

### 2.4 الترويسات الثابتة

| العميل | الترويسات |
|---|---|
| Node (كل الطلبات) | `User-Agent: gcloud-node-storage/8.4.0` (`GCSNODE util.js:227-232`). `x-goog-api-client: gl-node/<node> gccl/8.4.0-CJS gccl-invocation-id/<uuid>[ gccl-gcs-cmd/…]` و`x-goog-gcs-idempotency-token: <uuid>` (`common/util.js:716-748`، `util.js:205-226,244-246`). `Accept-Encoding: gzip,deflate` افتراضيًا (`TEENY index.js:97-101`) [V] |
| Go JSON | `x-goog-api-client: gl-go/<go> gdcl/<api-ver>`، تُدمَج معها `gccl-invocation-id/<id> gccl-attempt-count/<n> gl-go/… gccl/1.62.1` من سياق الاستدعاء، و`X-Goog-Gcs-Idempotency-Token` [V] (`GAPI params.go:64-78`، `send.go:44-80,186-194`، `GCSGO invoke.go:41-48,168-174`) |
| Go رفع (multipart وطلب البدء) | `setClientHeader` يكتب فوقها القيمة `gl-go/<go> gccl/1.62.1` فقط [V] (`GCSGO http_client.go:1104`، `storage.go:106-108`) |
| Go أجزاء resumable | `X-Goog-Api-Client: gl-go/… gdcl/… gccl-invocation-id/… gccl-attempt-count/…` و`X-Goog-Gcs-Idempotency-Token` و`X-GUploader-No-308: yes` و`Content-Type: <mType>` [V] (`GAPI resumable.go:91-111`) |
| Go `userProject` | استعلام `userProject` في JSON، وترويسة `X-Goog-User-Project` في قراءة XML (`http_client.go:901-903`). وتضيف طبقة النقل `X-Goog-User-Project` إن وُجد quota project (`GAPI transport/http/dial.go:102-104,240-242`) [V] |

لا يحتاج Aruma هذه الترويسات لشيء. لكن يجب ألا يرفض الطلب بسببها. ويمكن استعمال `x-goog-gcs-idempotency-token` لاحقًا لإزالة التكرار [I].

## 3. المصادقة: ما يراه الخادم

| الحالة | Node | Go |
|---|---|---|
| إنتاج (Google) | `authClient.authorizeRequest(reqOpts)` يضيف `Authorization: Bearer <access token>` من google-auth-library [V] (`GCSNODE common/util.js:494-503`). نطاقات Storage: `iam` و`cloud-platform` و`devstorage.full_control` (`storage.js:504-508`) | `Authorization: Bearer` من حساب الخدمة عبر `cloud.google.com/go/auth` [V] (`storage.go:164-176`). تُضاف نطاقات FAG (`FAG internal/internal.go:26-33`) |
| `STORAGE_EMULATOR_HOST` | `customEndpoint=true` وغياب `useAuthWithCustomEndpoint`، فتُعاد الطلبات **دون مصادقة** [V] (`common/util.js:495-500`). يشمل ذلك `getDownloadURL()` لأنه يستعمل `makeAuthenticatedRequest` نفسها (`FAN utils.ts:29-41`) | `option.WithoutAuthentication()`، فلا ترويسة `Authorization` [V] (`storage.go:196-198`، `dial.go:176-177`) |
| endpoint مخصص مع `useAuthWithCustomEndpoint:true` | الطلبات العادية والرفع البسيط **تحمل** `Bearer`. لكن **resumable من `createWriteStream` / `save()` / `upload()` لا تحمله**: `Upload` يستبدل عميل المصادقة بـ `gaxios` حين لا يكون المضيف من `googleapis.com` و`cfg.useAuthWithCustomEndpoint !== true` (`resumable-upload.js:126-147`). و`startResumableUpload_` لا يمرّر هذا الحقل أصلًا (`file.js:3538-3562`)، بينما `createResumableUpload()` يمرّره (`file.js:1293`) [V]. هذا عدم اتساق داخل العميل، أي خطأ فيه. وفي GCS الحقيقي يعمل رابط الجلسة نفسه كتصريح [I] | `WithEndpoint` + اعتمادات = `Bearer` على كل الطلبات، ومنها أجزاء resumable، لأنها تمر بـ `c.s.client` نفسه (`GAPI gen:11193`) [V] |

- **تبادل الرمز:** FAN يمرّر `private_key` و`client_email` فقط، فتستعمل google-auth-library عنوان Google الافتراضي. وFAG يحترم `token_uri` الموجود في ملف المفتاح. التفاصيل في `docs/research/auth-and-secrets.md` §"Service account token exchange" [I هنا].
- **المحاكي الرسمي** يفترض أن كل طلب GCS مدير، ويمرّر `authorization: "Bearer owner"` داخليًا في النسخ [V] (`FTOOLS gcloud.ts:483`). لا يرسل أي عميل `Bearer owner` بنفسه.
- **نتيجة تصميمية لـ Aruma [I]:** مسار "المحاكي" هو الطريق العملي الوحيد لـ FAN، وهو **بلا مصادقة** دائمًا. معنى ذلك أن طلبات Admin تصل إلى Aruma مجهولة الهوية ولا يمكن تمييزها عن عميل عام. تحتاج v0.3 إلى قرار ADR بين خيارين:
  - (أ) نقطة GCS "موثوقة شبكيًا": مقيدة بواجهة خاصة أو قائمة IP، ومعطلة افتراضيًا.
  - (ب) طلب `Bearer` صالح، ثم لا تعمل مع FAN إلا عبر `new Storage({apiEndpoint, useAuthWithCustomEndpoint:true})`. وحتى هذا لا يكفي لرفع resumable بسبب الخطأ المذكور، ولا لـ `getDownloadURL`.

## 4. العمليات غير الرفع: الطلبات والردود

**الرموز:** `B` في Node هي `baseUrl` من §2.1. و`G` في Go هي `.../storage/v1/`. يضيف Go دائمًا `alt=json&prettyPrint=false` إلى JSON [V] (`GAPI gen:10699-10700` وما يماثلها). يضيف TEENY علامة `?` في آخر الرابط **حتى لو كان الاستعلام فارغًا**، لأن `typeof qs === 'object'` [V] (`TEENY index.js:349-353`).

### 4.1 `objects.get` (`getMetadata` و`exists` و`get` و`Attrs`)

- **Node:** `GET {B}/b/{b}/o/{enc}?[generation=N][&userProject=…][&ifGenerationMatch=…]`. المعاملات من `requestQueryObject` [V] (`GCSNODE file.js:291-310,420-441`). لا يرسل `projection` ولا `alt`. `file.exists()` تتحول إلى `get()` ثم `getMetadata()`. الرد **404 يعني `false`**، وأي خطأ آخر يُرمى [V] (`common/service-object.js:143-156,157-215`).
- **Go:** `GET {G}b/{b}/o/{o}?alt=json&prettyPrint=false&projection=full[&generation=…&ifGenerationMatch=…&ifMetagenerationMatch=…&softDeleted=…]` [V] (`GCSGO http_client.go:426-452`، `GAPI gen:10694-10712`). الرد 404 يتحول إلى `ErrObjectNotExist` المغلَّف [V] (`storage.go:2866-2873`). لا يوجد `Exists()` في Go.
- **الرد:** 200 مع object resource كما في §7.

### 4.2 `objects.list` (`getFiles` و`Objects`)

- **Node:** `GET {B}/b/{b}/o?<query>`. `query` هي ما مرّره المستخدم كما هو (`prefix` و`delimiter` و`maxResults` و`pageToken` و`versions` و`startOffset` و`endOffset` و`matchGlob` و`includeTrailingDelimiter` و`includeFoldersAsPrefixes` و`softDeleted`...). يحذف `decorateRequest` الحقل `autoPaginate` [V] (`GCSNODE bucket.js:2213-2226`، `common/util.js:625-631`). يقرأ `resp.items` و`resp.nextPageToken`، ويُعاد `prefixes` للمستدعي داخل `apiResponse` [V] (`bucket.js:2233,2251-2256`). مع الترقيم التلقائي يكرر الطلب حتى يختفي `nextPageToken` [I، paginator].
- **Go:** يرسل **المجموعة كلها دائمًا، حتى الفارغ منها**: `projection=full&delimiter=&prefix=&startOffset=&endOffset=&versions=false&includeTrailingDelimiter=false&matchGlob=&includeFoldersAsPrefixes=false&pageToken=` ثم `maxResults` إن وُجد حجم صفحة، و`fields` إن استُعمل `SetAttrSelection` [V] (`GCSGO http_client.go:354-387`). المُعدِّلات تضبط القيمة دون فحص للفراغ (`GAPI gen:11247-11250,11289-11308,11358-11361`). يقرأ `Items` و`Prefixes` و`NextPageToken` [V] (`http_client.go:394-401`).
- **على الخادم:** أن يعامل القيم الفارغة (`delimiter=` و`prefix=` و`pageToken=` و`matchGlob=` و`startOffset=` و`endOffset=`) معاملة الغائبة، وأن يقبل `versions=false` و`includeTrailingDelimiter=false` و`includeFoldersAsPrefixes=false`، وأن يتجاهل `projection` و`fields` و`prettyPrint` و`userProject` و`alt=json`.
- **الرد:** `{"kind":"storage#objects","items":[…],"prefixes":[…],"nextPageToken":"…"}`. البنية في `GAPI gen:2733-2743`. يجوز حذف المفاتيح الفارغة [V، الوسوم `omitempty`].

### 4.3 `objects.patch` (`setMetadata` و`Update`)

- **Node:** `PATCH {B}/b/{b}/o/{enc}?<qs>`. الجسم JSON هو كائن البيانات الوصفية كما مرّره المستخدم، مع `Content-Type: application/json` [V] (`GCSNODE common/service-object.js:221-246`، `TEENY index.js:311-321`). لا يعيد المحاولة إلا مع `ifMetagenerationMatch` [V] (`file.js:3651-3675`).
- **Go:** `PATCH {G}b/{b}/o/{o}?alt=json&prettyPrint=false&projection=full[&ifMetagenerationMatch=…]`. الجسم جزئي: يُرسَل `"contentType":null` إن أفرغه المستخدم، و`"metadata":null` إن مرّر خريطة فارغة لحذف كل المفاتيح [V] (`GCSGO http_client.go:454-570`، وخاصة 466-473 و504-513). `Metadata map[string]string` تعني: "اضبطها إلى `map{}` للحذف" (`storage.go:1110`).
- **دلالة الدمج [I، سلوك GCS]:** PATCH يدمج `metadata` مفتاحًا بمفتاح، والمفتاح الذي قيمته `null` يُحذف، و`"metadata":null` تمسح الكل. الرد هو الكائن بعد التحديث مع `metageneration+1`.

### 4.4 `objects.delete`

- **Node:** `DELETE {B}/b/{b}/o/{enc}?<qs>`. مع `ignoreNotFound` يتحول 404 إلى نجاح [V] (`common/service-object.js:117-141`). لا يعيد المحاولة إلا مع `ifGenerationMatch` [V] (`file.js:1655-1674,3651-3664`).
- **Go:** `DELETE {G}b/{b}/o/{o}?alt=json&prettyPrint=false[&generation=…&ifGenerationMatch=…]` [V] (`GCSGO http_client.go:413-424`، `GAPI gen:10524-10540`). يقبل أي 2xx.
- **الرد:** 204 بلا جسم [I، GCS]. يكفي أي 2xx [V] (`googleapi.go:142-145`، `common/util.js:225-237`).

### 4.5 النسخ والنقل (`file.copy` و`file.move` و`CopierFrom`): كلاهما يستعمل `rewriteTo`

- **Node:** `POST {B}/b/{src}/o/{enc(src)}/rewriteTo/b/{dstBucket}/o/{enc(dst)}?[sourceGeneration&rewriteToken&ifGenerationMatch&destinationKmsKeyName&destinationPredefinedAcl&userProject]`. الجسم هو `options` المتبقية JSON، وهو عادة `{}` [V] (`GCSNODE file.js:810-935`). **يكرر الطلب ما دام `resp.rewriteToken` موجودًا، ولا يقرأ `done`** [V] (`file.js:920-933`). `file.move()` = `copy` ثم `DELETE` للمصدر إن اختلف الاسم أو الـ bucket [V] (`file.js:3052-3080`). أما `moveFileAtomic` فتستعمل `.../moveTo/o/…` وهي خارج النطاق (`file.js:2863-2912`).
- **Go:** `POST {G}b/{sb}/o/{so}/rewriteTo/b/{db}/o/{do}?alt=json&prettyPrint=false&projection=full[&rewriteToken&maxBytesRewrittenPerCall&ifGenerationMatch&ifSourceGenerationMatch&sourceGeneration…]`. الجسم هو `Copier.ObjectAttrs` بعد تحويله [V] (`GCSGO http_client.go:810-866`، `GAPI gen:12309`). **يكرر حتى يصل `done:true`** [V] (`copy.go:131-145`).
- **الرد:** `{"kind":"storage#rewriteResponse","totalBytesRewritten":"N","objectSize":"N","done":true,"resource":{…object…}}`. الرقمان **سلسلتان** [V] (`GAPI gen:2938-2954`). المحاكي يرد بالشكل نفسه (`FTOOLS gcloud.ts:503-509`).
- **دلالة جسم rewrite [I، GCS]:** الحقول المرسلة في الجسم تستبدل بيانات الوجهة الوصفية. وإن كان الجسم `{}` تُنسخ بيانات المصدر.
- `compose` (`bucket.combine` و`ComposerFrom`): `POST …/b/{b}/o/{dst}/compose` (`GAPI gen:10113`). **خارج v0.3.**

### 4.6 `buckets.get` (`bucket.exists` و`bucket.getMetadata` و`Bucket.Attrs`)

- **Node:** `GET {B}/b/{bucket}?`. الـ 404 يعني `exists()=false` [V] (`GCSNODE bucket.js:767-772`، `service-object.js:143-156`).
- **Go:** `GET {G}b/{bucket}?alt=json&prettyPrint=false&projection=full` [V] (`GCSGO http_client.go:275-296`، `GAPI gen:4704`). الـ 404 يتحول إلى `ErrBucketNotExist` (`storage.go:2877-2884`). `newBucket` لا يفشل إلا إن كان `retentionPolicy` غير صالح (`bucket.go:850-858`).
- **رد كافٍ:** `{"kind":"storage#bucket","id":"b","name":"b","projectNumber":"0","metageneration":"1","location":"US","storageClass":"STANDARD","timeCreated":"…","updated":"…","etag":"CAE="}`. هنا `projectNumber` و`metageneration` و`generation` **سلاسل** [V] (`GAPI gen:493,525,534`).

### 4.7 خارج النطاق

ACL و`makePublic` (`POST …/o/{o}/acl` مع `{entity:'allUsers',role:'READER'}`) [V] (`file.js:2739-2747`). وكذلك IAM والإشعارات وHMAC وإنشاء الـ buckets و`moveTo` و`restore`. الروابط الموقّعة (`getSignedUrl`) تُحسب محليًا وتشير إلى `{apiEndpoint}/{bucket}/{object}?X-Goog-Signature=…`، أي XML API مع تحقق V4 [V] (`signer.js:128-129,178`). كلها خارج v0.3.

## 5. الرفع

### 5.1 أي آلية يختار كل عميل

| الاستدعاء | الآلية | المرجع |
|---|---|---|
| Node `file.save(data)` و`file.createWriteStream()` و`bucket.upload(path)` | **resumable دائمًا** في وضع الطلب الواحد (لا `chunkSize`) | [V] `file.js:3354,1569-1573`، `bucket.js:3500` |
| Node مع `{resumable:false}` | `uploadType=multipart` | [V] `file.js:1569-1570,3599-3650` |
| Node مع `{chunkSize:n}` | resumable متعدد الأجزاء | [V] `file.js:3557`، `resumable-upload.js:518,592-628` |
| Go `w := obj.NewWriter(ctx)`، `ChunkSize=16 MiB` افتراضيًا | **multipart** إن كان المحتوى `< ChunkSize`. وإلا resumable. الحالة الحدّية: المحتوى `== ChunkSize` أو مضاعفه يسلك resumable وينتهي بجزء فارغ | [V] `storage.go:1254-1263`، `GAPI googleapi.go:55-62`، `media.go:121-133,194-199`، `buffer.go:53-69` |
| Go مع `w.ChunkSize=0` | multipart متدفّق، دون طول ودون إعادة محاولة | [V] `media.go:122-123`، `writer.go:99-104` |
| Go: `ChunkSize` يُقرَّب للأعلى إلى مضاعف 256 KiB | | [V] `googleapi.go:289-291` |

**تحديد نوع المحتوى:**
- Node يأخذ `metadata.contentType`. إن غاب أو كان `'auto'` استنتجه من امتداد الاسم بمكتبة `mime` [V] (`file.js:1536-1542`).
- Go يأخذ `attrs.ContentType`، وإن غاب "يشمّ" المحتوى بـ `gax.DetermineContentType` [V] (`media.go:155-160`، والاستنشاق نفسه [I]). عندها لا يظهر `contentType` في JSON، بل في ترويسة جزء الوسائط وفي `X-Upload-Content-Type`.
- **على الخادم:** `contentType` من JSON أولًا، ثم من `Content-Type` جزء الوسائط أو من `X-Upload-Content-Type`، ثم `application/octet-stream` [I، GCS].

### 5.2 multipart (`uploadType=multipart`)

| | Node (`resumable:false`) | Go (`< ChunkSize`) |
|---|---|---|
| الطلب | `POST {apiEndpoint}/upload/storage/v1/b/{b}/o?name={name}&uploadType=multipart[&ifGenerationMatch&kmsKeyName&predefinedAcl&userProject]` [V] (`file.js:3604-3632`، `common/util.js:286-312`) | `POST …/upload/storage/v1/b/{b}/o?alt=json&name=…&prettyPrint=false&projection=full&uploadType=multipart[&ifGenerationMatch…]` [V] (`GAPI gen:11127-11166`، `GCSGO http_client.go:1078-1101`) |
| Content-Type | `multipart/related; boundary=<uuid>` [V] (`TEENY index.js:412-421`) | `multipart/related; boundary=<random>` [V] (`media.go:38-46`) |
| الأجزاء | `--b\r\nContent-Type: application/json\r\n\r\n{json}\r\n--b\r\nContent-Type: <ct\|application/octet-stream>\r\n\r\n<data>\r\n--b--`، **دون CRLF بعد الحد الأخير**، ودون `Content-Length` [V] (`TEENY index.js:389-410`) | جزء JSON (`application/json`) ثم جزء الوسائط بنوع المحتوى، عبر `mime/multipart` [V] (`media.go:88-107,242`) |
| JSON | ما في `options.metadata`: `contentType` و`metadata` و`cacheControl`...، **دون `name`** | `{"bucket":…,"name":…,"contentType"?,"metadata"?,…,"crc32c":"<auto>"}` [V] (`storage.go:1394-1421`، `GAPI gen:11129-11136`) |
| الجسم | stream، أي `Transfer-Encoding: chunked` [V/I] | بطول معروف في الوضع العادي |
| إعادة المحاولة | لا (`maxRetries:0`) [V] (`common/util.js:290-294`) | فقط إن كان الطلب idempotent أو `RetryAlways` (`http_client.go:1110-1132`) |
| الرد المنتظر | 2xx مع object JSON (`file.js:3634-3646`) | 2xx مع object JSON (`GAPI gen:11205-11213`) |

يجب أن يأخذ الخادم الاسم من `name` في الاستعلام أولًا، ثم من `name` في JSON إن غاب [I]. وهذا ما يفعله المحاكي (`FTOOLS gcloud.ts:342`).

### 5.3 بدء resumable

| | Node | Go |
|---|---|---|
| الطلب | `POST {apiEndpoint}/upload/storage/v1/b/{b}/o?name={file}&uploadType=resumable[&ifGenerationMatch…&kmsKeyName&predefinedAcl&userProject]` [V] (`resumable-upload.js:442-466,821-824`) | `POST …/upload/storage/v1/b/{b}/o?alt=json&name=…&prettyPrint=false&projection=full&uploadType=resumable[…]` [V] |
| الجسم | JSON = `metadata` بعد حذف `contentType` و`contentLength` منها، وهو غالبًا `{}` [V] (`resumable-upload.js:418-429,449`) | JSON = raw Object (`Content-Type: application/json`) [V] (`GAPI gen:11128,11137`) |
| ترويسات | `X-Upload-Content-Type` (من `metadata.contentType`)، و`X-Upload-Content-Length` إن مرّر المستخدم `metadata.contentLength`، و`Origin` إن وُجد [V] (`resumable-upload.js:422-431,452-469`) | `X-Upload-Content-Type: <mType>` إن لم يكن فارغًا [V] (`media.go:260-273`). لا يرسل `X-Upload-Content-Length` |
| الرد المنتظر | أي 2xx (أو 308) مع **`Location`**. يُقرأ بـ `respHeaders.get('location')` [V] (`resumable-upload.js:473-477,826-829`) | 2xx مع `Location`. **الجسم يُتجاهَل** [V] (`GAPI gen:11186-11191`) |

يجب أن يكون `Location` **رابطًا مطلقًا** يستطيع العميل الوصول إليه: البروتوكول والمضيف كما رآهما العميل، أو `public_url` مضبوطًا [I]. السبب أن `http.NewRequest("POST", rx.URI)` وgaxios يستعملانه كما هو (`resumable.go:75`، `resumable-upload.js:638`). شكل GCS المعتاد هو `…/upload/storage/v1/b/{b}/o?uploadType=resumable&name=…&upload_id=<id>`، والمحاكي يقلّده (`FTOOLS gcloud.ts:295-300`).

> **فخ التوجيه [V+I]:** Go يرسل **الأجزاء بـ `POST` إلى رابط الجلسة نفسه** (`resumable.go:75`). إن احتوى الرابط `uploadType=resumable`، وكان الخادم يوجّه بـ `uploadType` قبل `upload_id`، فسيبدأ جلسة جديدة بدل إضافة الجزء. هذا بالضبط ما يحدث في المحاكي (انظر §9-B2). القاعدة: **وجود `upload_id` يعني عملية على جلسة قائمة، أيًّا كانت الطريقة (`PUT` أو `POST`) وأيًّا كان `uploadType`.**

### 5.4 إرسال البيانات: Node بطلب واحد (الافتراضي)

- `PUT {sessionURI}` مع `Content-Range: bytes {offset}-*/{contentLength|*}`، وفي الحالة الشائعة **`bytes 0-*/*`**. الجسم `Readable` بلا `Content-Length`، أي chunked [V] (`resumable-upload.js:629-641`، `contentLength='*'` من `212-215`).
- **الرد المنتظر:** 2xx مع object JSON. يُطلق `metadata`، ثم يتحقق `createWriteStream` من الـ CRC (§5.8) [V] (`resumable-upload.js:717-737`). أي 308 هنا **خطأ** `Upload failed` لأن `chunkSize` غير مضبوط [V] (`resumable-upload.js:673-716`).
- **الدلالة المطلوبة من الخادم [I، مفروضة بالشيفرة]:** `bytes N-*/*` تعني أن بقية الكائن من البايت N تصل في هذا الطلب. عند انتهاء الجسم يُغلق الرفع بالحجم `N+len`، **ولو كان الحجم صفرًا** (حفظ ملف فارغ).
- **عند الانقطاع:** يُستدعى `attemptDelayedRetry` ثم `continueUploading` ثم `checkUploadStatus` (§5.6)، ويستأنف من `Range+1`. يحتفظ Node في هذا الوضع بآخر buffer فقط (`resumable-upload.js:569-573`). فإن قال الخادم إنه استلم أقل مما أُرسل، باستثناء ذلك الـ buffer، رمى Node `RangeError` "offset is lower than the number of bytes written" (`525-530`) [V]. **يجب إذن أن يحفظ الخادم البايتات المتدفقة تدريجيًا ويبلّغ عنها بدقة** [I].
- **ملاحظة:** في `save()` الافتراضي (دون `ifGenerationMatch`) تكون `autoRetry=false`، فتصير `maxRetries=0` ولا إعادة محاولة إطلاقًا [V] (`file.js:3345-3352,3535-3537`، `resumable-upload.js:208-210`).

### 5.5 Node متعدد الأجزاء (`chunkSize`)

- لكل جزء `PUT {sessionURI}` مع `Content-Length: n` و`Content-Range: bytes {a}-{b}/{total|*}` [V] (`resumable-upload.js:592-628`).
  - **آخر جزء** يُحدَّد بـ `!(await waitForNextChunk())`، أي انتهى المصدر ولم يبق شيء في الـ buffer (`604`).
  - المجموع معروف في آخر جزء فقط: `totalObjectSize = bytesToUpload + numBytesWritten` (`610-616`).
  - ما قبل آخر جزء يُرسَل بـ `*`.
- **الرد المنتظر لجزء غير أخير:** **`308`** مع ترويسة **`Range: bytes=0-{last}`**. يحلّلها Node بـ `Number(range.split('-')[1]) + 1` ليصل إلى الإزاحة التالية، ويعيد إرسال البايتات الناقصة من الكاش (`673-705`). **الرد 308 دون `Range` يرمي خطأ** `Upload failed` (`707-716`).
  - **لا يجوز إرسال `Location` مع 308**، لأن fetch يتبع 308 إن وُجد فيه `Location` [I، مواصفة fetch].
- **الرد المنتظر لآخر جزء:** 2xx مع object JSON.
- **إن رد الخادم 200 قبل آخر جزء:** يعامله Node كنهاية الرفع ويطلق `metadata`. عندها يختلف CRC البادئة عن CRC الكل، **فيحذف Node الكائن ويرمي `FILE_NO_UPLOAD`** [V] (`resumable-upload.js:717-737`، `file.js:3692-3730`). هذا ما التُقط مع المحاكي (§9-A3).

### 5.6 استعلام الحالة (Node فقط)

`PUT {sessionURI}` مع `Content-Length: 0` و`Content-Range: bytes */*`، وبلا جسم [V] (`resumable-upload.js:752-768`). المقبول:
- 308 مع `Range: bytes=0-N`، فتصير الإزاحة `N+1`.
- 308 دون `Range`، فتصير الإزاحة 0.
- 2xx: الرفع مكتمل.

التفاصيل في `789-801`. **Go لا يستعلم عن الحالة أبدًا** [V، لا يوجد مقابل في `resumable.go`].

### 5.7 أجزاء Go

- `POST {Location}` مع `Content-Length=size` و`Content-Type: <mType>` و`X-GUploader-No-308: yes` و`X-Goog-Gcs-Idempotency-Token`، ومع `X-Goog-Hash: crc32c=<b64>` **في آخر طلب فقط** [V] (`GAPI resumable.go:74-119`).
- `Content-Range` له ثلاثة أشكال (`resumable.go:81-91`):
  - جزء غير أخير: `bytes a-b/*`.
  - آخر جزء: `bytes a-b/{a+size}`.
  - **آخر جزء فارغ** حين يكون الطول مضاعفًا دقيقًا لـ `ChunkSize`: `bytes */{off}`.
- **الرد المنتظر لجزء غير أخير:** **`200` مع `X-Http-Status-Code-Override: 308`**. **Go لا يقرأ `Range`**: يفترض أن الجزء كله قُبل، ويستدعي `Media.Next()` (`resumable.go:121-125,337-345`). أما 308 الحقيقي فيُنتج الخطأ `unexpected 308 response status code` (`281-283`).
- **الرد المنتظر لآخر جزء:** 2xx مع object JSON يُفك بـ `DecodeResponseBytes`. أي جسم آخر، مثل `OK`، يُنتج `invalid character 'O' looking for beginning of value` [V] (`GAPI gen:11192-11213`) و[I] لنص الخطأ.
- **إعادة المحاولة:** **كل جزء يُعاد دائمًا**، حتى دون شروط مسبقة، لأن `rx.Retry` الفارغ يستعمل `shouldRetry` الافتراضي. الحالات: 5xx و408 و429 وأخطاء الشبكة، بمهلة 32 ثانية لكل جزء [V] (`resumable.go:203-293`، `retry.go:28,51-80,113-116`). الجزء المكرر يصل **بنفس `Content-Range`**.
- **ما يفرضه ذلك على الخادم [I]:**
  1. اقبل الجزء كاملًا أو ارفضه كاملًا. لا تحفظ بعض بايتات الجزء ثم ترد بالنجاح، لأن Go يتجاهل `Range`.
  2. إن وصل جزء يبدأ قبل الإزاحة المحفوظة، تجاهل البايتات المكررة ورد بالنتيجة نفسها، فهذه إعادة إرسال.
  3. إن وصل جزء يبدأ بعد الإزاحة المحفوظة (فجوة)، فرد 400 أو 503.

### 5.8 checksums أثناء الرفع

| | ما يرسله العميل | ما يتحقق منه العميل بعد الرد |
|---|---|---|
| Go multipart (`ChunkSize>0`) | `"crc32c"` في JSON محسوبة تلقائيًا [V] (`GAPI gen:11129-11136`، `media.go:202-215`) | لا شيء. يعتمد على أن الخادم سيرفض. وثيقة العميل تقول: "If the data written does not match the checksum, the write is rejected" [V] (`GCSGO writer.go:45-47`) |
| Go resumable | `X-Goog-Hash: crc32c=…` مع آخر طلب [V] (`resumable.go:113-116`) | لا شيء |
| Go `ChunkSize=0` | لا شيء في JSON | يقارن `crc32c` في الرد بما حسبه، ويفشل `Close()` عند الاختلاف [V] (`GCSGO http_client.go:1003-1027,1139-1143`) |
| Node (resumable وmultipart عبر `createWriteStream`) | لا شيء. `startResumableUpload_` لا يمرّر `crc32c` إلى `Upload`، فلا `X-Goog-Hash` [V] (`file.js:3538-3562`، `resumable-upload.js:170-178,1014-1031`) | `_validateIntegrity`: إن غاب `metadata.crc32c` أو اختلف (التحقق الافتراضي `crc32c`)، **حذف الكائن** (`DELETE`) ورمى `FILE_NO_UPLOAD`. ومع `validation:'md5'` يلزم `md5Hash` [V] (`file.js:3692-3730`) |

**على الخادم:**
- (1) أن يحسب `crc32c` و`md5Hash` بنفسه ويعيدهما في كل رد رفع.
- (2) إن أرسل العميل `crc32c` في JSON أو `X-Goog-Hash` (و`md5Hash`/`md5=`) وكانت مختلفة، فعليه أن يرفض بـ **400** دون إنشاء الكائن. رمز الحالة [I، GCS]. ويجب ألا يخزّن قيمة العميل كما هي.
- (3) الصيغة: Base64 قياسي **مع الحشو**. Node يقارن السلسلة حرفيًا (`crc32c.js:125-131`، `hash-stream-validator.js:115-125`)، وGo يشترط 4 بايتات بالضبط (`storage.go:1847-1856`).

## 6. التنزيل

### 6.1 Node: JSON media (`createReadStream` و`download`)

- `GET {B}/b/{b}/o/{enc}?alt=media[&generation=N][&userProject=…]` مع `Accept-Encoding: gzip` و`Cache-Control: no-store`. ومع النطاق: `Range: bytes={start}-{end}`، أو `bytes=-N` للذيل [V] (`GCSNODE file.js:1138-1160`). يمرّر TEENY `compress=false` في وضع stream، فيصل جسم gzip خامًا إلى `file.js` [V] (`TEENY index.js:465`).
- **فك الضغط:** إن كان `content-encoding: gzip` و`decompress` مفعّلًا، وهو الافتراضي، يفك Node الضغط بنفسه [V] (`file.js:1092,1131-1133`).
- **التحقق:** يجري فقط عند تحقق الشرطين معًا [V] (`file.js:1044,1097-1130`):
  - `!rangeRequest`، وCRC32C مفعّل افتراضيًا.
  - **`safeToValidate`**، أي `x-goog-stored-content-encoding` تساوي `identity`، أو تساوي `gzip` مع `content-encoding: gzip`.

  إن غابت هذه الترويسة **لا يحدث تحقق إطلاقًا**. وإن وُجدت فالمطلوب `x-goog-hash` بصيغة `crc32c=<b64>,md5=<b64>`: يقسّمها Node على `,` **دون حذف المسافات** (`1104-1111`).
  - إن غابت القيمة المتوقعة يبقى `failed = crc32cEnabled`، فيرمي `CONTENT_DOWNLOAD_MISMATCH` (`hash-stream-validator.js:74-100`).
  - مع `validation:'md5'` وغياب `md5`، يرمي `MD5_NOT_AVAILABLE` (`file.js:1122-1126`).
  - **أرسل الترويسة مرة واحدة دون مسافة بعد الفاصلة.** الترويستان المنفصلتان تُدمجان بـ `", "` [I، fetch Headers]، فيصير المفتاح الثاني `" md5"`.
- **الأخطاء:** أي حالة غير 2xx تصير `ApiError{code: status}`، ونص الرسالة هو **نص جسم الرد** [V] (`file.js:1073-1085`، `common/util.js:225-237`). لا يفحص Node هل الرد 206 أم 200.

### 6.2 Go: XML افتراضيًا (`NewReader` و`NewRangeReader`)

- **المسار:** `GET {scheme}://{xmlHost}/{bucket}/{PathEscape(object)}`، بلا `alt` وبلا `/storage/v1`. ويُستعمل `HEAD` إن كان `length==0` [V] (`GCSGO http_client.go:884-899`). **XML هو الافتراضي** إلى أن يمرّر المستخدم `storage.WithJSONReads()` [V] (`option.go:121-146`، `http_client.go:878-881`).
- **مع JSON reads:** `GET {G}b/{b}/o/{o}?alt=media&prettyPrint=false&projection=full[&generation]` [V] (`http_client.go:965-990`، `GAPI gen:10719-10730`).
- **Range** (`http_client.go:1468-1476`):
  - `offset<0`: `bytes=-N`.
  - `offset>0` و`length<0`: `bytes=off-`.
  - `length>0`: `bytes=off-(off+len-1)`.
  - `NewReader` العادي: بلا Range.
- **الشروط المسبقة في XML:** ترويسات `x-goog-if-generation-match` و`x-goog-if-metageneration-match` [V] (`reader.go:315-332`).
- **التثبيت على generation:** أول رد يُقرأ منه `X-Goog-Generation`. أي إعادة فتح بعد انقطاع أو إعادة محاولة تُرسَل مع **`?generation=N`** في الاستعلام [V] (`http_client.go:956,1478-1481,1529-1536`). **يجب إذن أن يدعم مسار XML المعامل `generation`.**
- **حالة الرد:**
  - 404 يتحول إلى `ErrObjectNotExist`.
  - أي حالة خارج 2xx تصير `googleapi.Error{Code, Body}`.
  - إن كان `start>0` و`length!=0` ولم تكن الحالة **206** ظهر الخطأ "partial request not satisfied"، إلا عند فك الضغط من جهة الخادم.

  المرجع: `http_client.go:1490-1527`.
- **الترويسات التي يحلّلها** (`http_client.go:1546-1650`، `reader.go:281-313`):

| ترويسة الرد | الاستعمال | ملاحظة |
|---|---|---|
| `Content-Range` (مع 206) | يجب أن تكون `bytes a-b/total`، ويُؤخذ منها `total` حجمًا و`a` إزاحةً | الصيغة الخاطئة خطأ |
| `Content-Length` | حجم الكائن في الردود غير 206 | |
| `X-Goog-Hash` | يُقرأ `crc32c=` فقط، من قيم متعددة أو مفصولة بفاصلة | يُفحص CRC فقط إن كان المحتوى كاملًا، والحالة ليست 206، ولم يُفك الضغط. الاختلاف ينتج `bad CRC on read` |
| `X-Goog-Generation` | تثبيت generation | يجب أن يكون عددًا صحيحًا إن وُجد |
| `X-Goog-Metageneration` | `Attrs.Metageneration` | عدد صحيح، وإلا فشل الطلب. انتبه: المحاكي يرسل `x-goog-metadatageneration` (`FTOOLS shared.ts:43`) |
| `Last-Modified` | `http.ParseTime` | **التاريخ غير الصالح يُفشل القراءة** |
| `Content-Type` و`Content-Encoding` و`Cache-Control` | `ReaderObjectAttrs` | |
| `X-Goog-Stored-Content-Encoding` | كشف فك الضغط من جهة الخادم | |
| `X-Goog-Meta-*` | البيانات الوصفية للمستخدم | |
| `ETag` و`x-goog-stored-content-length` | **لا يُقرآن** | |

### 6.3 الضغط (transcoding)

لا يرسل Go `Accept-Encoding` إلا مع `ReadCompressed(true)` (`http_client.go:1448-1451`). أما `net/http` فيضيف `gzip` ضمنيًا في الطلبات الكاملة غير المحدودة بنطاق، ويفك الضغط بشفافية، ويضبط `res.Uncompressed`. عندها يُلغى فحص CRC [I، net/http]، و`http_client.go:1594` [V].

**توصية v0.3:** لا تضغط الردود ديناميكيًا أبدًا. أرسل البايتات كما خُزّنت، مع `Content-Encoding` المخزّن في الكائن إن وُجد. وأرسل دائمًا `x-goog-stored-content-encoding: identity|gzip` و`x-goog-hash: crc32c=…,md5=…` الصحيحين.

## 7. شكل object resource كما يحلّله العميلان

Go يفك JSON بوسوم `GAPI gen:2333-2480`، ثم يحوّلها بـ `newObject` (`GCSGO storage.go:1756-1805`). Node يأخذ الجسم كما هو (`service-object.js:205-215`)، ويحوّل فقط `size` إلى `Number` بعد resumable (`resumable-upload.js:731-733`). FAN يصف الحقول كلها سلاسل (`FAN utils.ts:2-18`).

| الحقل | النوع على السلك | من يقرأه / شرط |
|---|---|---|
| `kind` | `"storage#object"` | معلوماتي |
| `id` | `"{bucket}/{name}/{generation}"` [I، GCS] | معلوماتي |
| `selfLink`، `mediaLink` | string | Go يخزّن `MediaLink` فقط. لا أحد يتبعه [V، بحث: لا استعمال لـ `/download/storage`] |
| `name`، `bucket` | string | Node `getFiles` يبني `File` من `name` (`bucket.js:2235-2247`) |
| `generation` | **string** لـ int64 | Go وسم `,string` (`gen:2385`). الرقم غير المقتبس يُفشل الفك [V+I] |
| `metageneration` | **string** | `gen:2410` |
| `size` | **string** لـ uint64 | `gen:2433` |
| `componentCount` | number | `gen:2344`، اختياري |
| `contentType`، `contentEncoding`، `contentDisposition`، `contentLanguage`، `cacheControl`، `storageClass`، `etag` | string | |
| `md5Hash` | Base64(16 بايت) | Node يقارنه حرفيًا عند `validation:'md5'` |
| `crc32c` | Base64(4 بايتات BE) مع الحشو، مثل `"AAAAAA=="` | **إلزامي عمليًا** في ردود الرفع لـ Node (§5.8). Go يتجاهله بصمت إن كان غير صالح (`storage.go:1765`) |
| `timeCreated`، `updated`، `timeStorageClassUpdated`، `timeFinalized`، `customTime` | RFC 3339، مثل `2026-10-23T12:00:00.123Z` | Go يستعمل `time.Parse(RFC3339)`، والفشل يُعطي صفرًا بصمت (`storage.go:1732-1738`) |
| `metadata` | object من string إلى string | يتضمن `firebaseStorageDownloadTokens` (`uuid` أو قائمة مفصولة بفواصل) [I، الاتفاق مع v0 في A]. Go نوعه `map[string]string`، فالقيم غير النصية تُفشل الفك [V] (`gen:2405`) |
| `acl`، `owner` | | لا يُرسلان مع `projection=noAcl`. تجاهلهما في v0.3 [I] |

**مثال رد كافٍ:**
```json
{"kind":"storage#object","id":"b/a/b.txt/1729680000123456","selfLink":"…","mediaLink":"…",
 "name":"a/b.txt","bucket":"b","generation":"1729680000123456","metageneration":"1",
 "contentType":"text/plain","storageClass":"STANDARD","size":"13",
 "md5Hash":"…==","crc32c":"U5UsBg==","etag":"CMDw…=","timeCreated":"2026-10-23T12:00:00.123Z",
 "updated":"2026-10-23T12:00:00.123Z","timeStorageClassUpdated":"2026-10-23T12:00:00.123Z",
 "metadata":{"firebaseStorageDownloadTokens":"…"}}
```

## 8. شكل الخطأ وكيف يظهر للمستخدم

**الشكل المطلوب [I، شكل GCS المعتاد، ويستهلكه العميلان كما هو أدناه]:**
```json
{"error":{"code":404,"message":"No such object: b/a.txt",
          "errors":[{"domain":"global","reason":"notFound","message":"No such object: b/a.txt"}]}}
```

- **Node:**
  - `parseHttpRespBody` يحوّل `body.error` إلى `ApiError`، ويقرأ منه `code` و`errors` (أو من `JSON.parse(response.body).error.errors`) و`message` [V] (`common/util.js:107-125,250-266`).
  - إن **لم يكن الجسم JSON** يُبنى الخطأ من `statusCode` و`statusMessage` (`225-237`).
  - **انتبه:** حين يكون الجسم JSON، يُؤخذ `err.code` من **`error.code` في الجسم**، ويحل محل الخطأ المبني من الحالة (`199-206`). إن غاب `code` أو كان سلسلة **لن تعرف `exists()` أن الحالة 404**، فترمي بدل أن تعيد `false` (`service-object.js:143-156`). **الشرط: `code` رقم يساوي حالة HTTP.**
- **Go:**
  - `CheckResponse` يفك `{"error":{…}}` إلى `googleapi.Error{Code, Message, Errors[{Reason, Message}], Details}`، وإن غاب `code` يأخذ حالة HTTP [V] (`googleapi.go:66-92,142-181`).
  - ردود الوسائط تمر بـ `CheckMediaResponse`، ولا تُفك؛ النص الخام يذهب إلى `Body` (`googleapi.go:214-224`).
  - الحالة 404 تصير `ErrObjectNotExist` أو `ErrBucketNotExist` (`storage.go:2866-2884`).

| الموقف | الحالة | `reason` [I] |
|---|---|---|
| كائن أو bucket غير موجود | 404 | `notFound` |
| شرط مسبق فشل (`ifGenerationMatch` و`ifMetagenerationMatch` و`x-goog-if-*`). ملاحظة: `ifGenerationMatch=0` تعني "يجب ألا يوجد" (`GCSGO storage.go:2242-2243`، `reader.go:327-330`) | 412 | `conditionNotMet` |
| checksum مختلف، أو `Content-Range` فاسد، أو جلسة بفجوة | 400 | `invalid` |
| جلسة رفع غير معروفة أو منتهية | 404 (و410 لما انتهى) | `notFound` |
| غير مصرّح أو ممنوع | 401 أو 403 | `required` / `forbidden` |
| ضغط أو خطأ مؤقت | 429 أو 503 | يعيد العميلان المحاولة. Node: 408 و429 و500 و502 و503 و504 مع `rateLimitExceeded` و`userRateLimitExceeded` (`common/util.js:344-363`، `storage.js:95-127`). Go: 408 و429 و5xx وأخطاء الشبكة (`invoke.go:192-247`) |

**سياسة إعادة المحاولة:**
- Node: الافتراض `RetryConditional`، مع `maxRetries=3` و`totalTimeout=600s` (`storage.js:55-87`). تُعطَّل إعادة المحاولة لـ delete وsetMetadata وsave وupload ما لم يوجد الشرط المسبق المناسب.
- Go: `RetryIdempotent`. القراءات وget وlist وbucket get كلها idempotent. أما Update وDelete وWriter وCopy وCompose فـ idempotent فقط مع الشروط (`storage.go:1047,1062,1141`، `writer.go:406-409`، `copy.go:123,228`). أجزاء resumable تُعاد دائمًا (§5.7).

## 9. ملاحظات الالتقاط من المحاكي مشروحةً من الشيفرة

### A. FAN 14.5.0 مع GCSNODE 8.4.0 (`admin_node.json` و`admin_gcs_endpoint.json`)

1. **`/b/<bucket>/o/<obj>?` بلا `/storage/v1`، وبعلامة `?` في الآخر، وبلا `Authorization`:**
   - المسار: FAN يضبط `STORAGE_EMULATOR_HOST=http://127.0.0.1:8865` (`FAN storage.ts:58`)، فتكون `baseUrl` هي هذه القيمة الخام (`GCSNODE storage.js:474`). يُبنى المسار `[baseUrl, 'b', bucket, 'o', enc]` (`service.js:109-128`، `service-object.js:250-262`).
   - علامة `?`: TEENY يضيف `'?' + querystring.stringify(qs)` حتى حين يكون `qs={}` (`TEENY index.js:349-353`).
   - غياب المصادقة: `customEndpoint=true` (`storage.js:466`)، وليس هناك `useAuthWithCustomEndpoint`، فتُعاد `reqOpts` كما هي (`common/util.js:495-500`).
   - الرفع إلى `/upload/storage/v1/...`: لأنه مبني من `apiEndpoint` (`file.js:3604`، `resumable-upload.js:149`).
   - `getDownloadURL`: `GET {STORAGE_EMULATOR_HOST}/v0/b/{b}/o/{enc}` (`FAN index.ts:75-78`، `utils.ts:24-41`). لا تظهر `?` لأن الطلب يمر مباشرة عبر `makeAuthenticatedRequest` دون `qs`. وهو بلا مصادقة للسبب نفسه.
2. **`apiEndpoint` مع `useAuthWithCustomEndpoint:true`:**
   - يُضاف `/storage/v1` لأن `EMULATOR_HOST` غير موجود (`storage.js:474`).
   - `Bearer` موجود على JSON والرفع البسيط (`common/util.js:494-503`)، لأن multipart يمر بـ `this.request` ثم `makeAuthenticatedRequest` (`file.js:3634-3646`).
   - **أما بدء resumable والـ PUT التالية فبلا `Authorization`:** القرار في `resumable-upload.js:126-147`، حيث يُستبدل `authClient` بـ `gaxios` إذا لم يكن المضيف تحت `googleapis.com` و`cfg.useAuthWithCustomEndpoint !== true`. والسبب أن `startResumableUpload_` لا يضع الحقل في `cfg` (`file.js:3538-3562`)، بينما `createResumableUpload` يضعه (`file.js:1293`).
   - **الحكم:** خطأ أو سهو في العميل، لا تصميم مقصود. لكن أثره العملي أن رابط الجلسة يعمل كتصريح، وأن **طلب البدء نفسه يصل بلا مصادقة** [V للسلوك، I للنية].
   - الجسم `{}` لأن `contentType` نُقل إلى `X-Upload-Content-Type` (`resumable-upload.js:418-429`)، بعد أن استنتجه `mime` من الاسم (`file.js:1536-1542`).
3. **`save()` لـ 13 بايت:**
   - resumable افتراضيًا (`file.js:1569-1573`)، مع `PUT` واحد و`Content-Range: bytes 0-*/*` (`resumable-upload.js:630`).
   - chunked لأن الجسم `Readable` بلا `Content-Length` (`558-585,636-641`).
   - مع `chunkSize: 4 MiB` يصير الطلب `bytes 0-4194303/*` (`622-623`، والمجموع `*` لأن هذا ليس آخر جزء، `610-616`)، وينتظر **308 مع `Range: bytes=0-4194303`** (`673-689`).
   - رد المحاكي 200 لأن مسار `PUT` عنده يُغلق الرفع من أول طلب (`FTOOLS gcloud.ts:194-228`). عامل Node الرد كنهاية الرفع (`resumable-upload.js:717-737`)، ثم وجد `crc32c` البادئة مختلفًا عن CRC الكل، **فحذف الكائن ورمى `FILE_NO_UPLOAD`** (`file.js:3692-3730`).
   - **آخر جزء:** المصدر انتهى ولا شيء في الـ buffer، فيُرسَل `bytes a-b/{a+len}` (`604,610-623`).
4. **الترويسات:**
   - `x-goog-api-client: gl-node/24.21.0 gccl/8.4.0-CJS gccl-invocation-id/<uuid>` (`common/util.js:735`، `util.js:205-226,244-246`).
   - `x-goog-gcs-idempotency-token` (`common/util.js:742-747`).
   - `cache-control: no-store` في التنزيل (`file.js:1148`).
   - النسخ والنقل: `POST …/o/<src>/rewriteTo/b/<b>/o/<dst>?` بجسم `{}`، لأن `json: options` (`file.js:908-913`، `TEENY index.js:311-321`). ينتهي حين يغيب `rewriteToken` (`file.js:920-933`).
   - النقل = rewrite ثم `DELETE` (`file.js:3052-3080`).
   - `bucket.exists()` = `GET /b/<bucket>?` (`service-object.js:143-215`).

### B. FAG مع GCSGO 1.62.1 وGAPI v0.279.0 (`admin_go.json`)

1. **المسارات:**
   - JSON إلى `/storage/v1/...?alt=json&prettyPrint=false&projection=full` (`storage.go:186-201`، `GAPI gen:10699-10700`، `http_client.go:428`).
   - الرفع إلى `/upload/storage/v1/...&uploadType=…` (`GAPI gen:11141-11147`).
   - القراءة بصيغة XML `GET /<bucket>/<object مع %2F>`: يُختار المضيف والمسار في `newRangeReaderXML` (`http_client.go:884-891`). المضيف `xmlHost` من نقطة النهاية المختارة (`http_client.go:126-151`). وXML هو الافتراضي (`option.go:121-146`).
   - **الصيغة في الإنتاج:** `https://storage.googleapis.com/<bucket>/<object>` مع `%2F` أيضًا، لأن `RawPath` يستعمل `url.PathEscape`.
   - الترويسات المحلَّلة في جدول §6.2: `x-goog-stored-content-length` و`ETag` **لا يُقرآن**، و`md5` في `x-goog-hash` يُتجاهل (`reader.go:294-312`).
2. **أجزاء resumable — تصحيح:**
   - الشيفرة ترسل **`POST`** إلى رابط الجلسة، لا `PUT` (`GAPI resumable.go:75`). والالتقاط يؤكد ذلك: الطلب الثاني في `test/oracle/storage/emulator/admin_go.json` (خطوة `Writer 9 MiB`) هو `POST ...&upload_id=...` (ذكر `PUT` في وصف المنسّق كان خطأ في النقل). [V]
   - مع `X-GUploader-No-308: yes` ينتظر العميل **200 مع `X-Http-Status-Code-Override: 308`**، ولا يقرأ `Range` (`resumable.go:104-125,337-345`).
   - رد المحاكي "200 OK" مصدره أن **POST إلى رابط يحتوي `uploadType=resumable`** يقع على معالج البدء. ذلك المعالج يبدأ جلسة جديدة ويرد `sendStatus(200)`، وجسمه النصي `OK` (`FTOOLS gcloud.ts:274-300`، والرابط يحتوي `uploadType=resumable` من السطر 298).
   - لم يرَ Go ترويسة override، فاعتبر الرد نهائيًا وحاول فك `OK` كـ JSON. فشل الفك (`GAPI gen:11192-11213`).
3. **`"crc32c":"U5UsBg=="` في multipart:**
   - Go يضعها تلقائيًا (`GAPI gen:11129-11136`).
   - **على الخادم أن يتحقق منها** ويرفض الاختلاف. وثيقة العميل تقول إن الكتابة تُرفض (`GCSGO writer.go:45-47`)، و400 هي الحالة المعتادة [I].
   - العميل **لا** يتحقق من `crc32c` العائد إلا مع `ChunkSize=0` (`http_client.go:1003-1027,1139-1143`).
4. **`x-goog-api-client`:**
   - في الرفع `gl-go/1.27.2 gccl/1.62.1`، لأن `setClientHeader` تكتب فوق قيمة `SetHeaders` (`GCSGO http_client.go:1104`، `invoke.go:41`، `GAPI params.go:66,74-76`).
   - في JSON `gl-go/… gdcl/0.279.0 gccl-invocation-id/… gccl-attempt-count/1 …`، بالدمج من سياق `run()` (`invoke.go:116-118,168-174`، `send.go:44-80`).
   - **لا `Authorization`** في وضع المحاكي (`storage.go:196-198`).
   - مع `option.WithEndpoint` واعتمادات يُرسَل `Authorization: Bearer <access token>` على كل الطلبات، ومنها أجزاء resumable.
   - FAG يقبل `opts ...option.ClientOption` في `NewApp`، ويمرّرها إلى **كل** الخدمات (`FAG firebase.go:194-216,99-105`).
5. **`Bucket.Attrs` والمعاملات الفارغة في list:**
   - `Bucket.Attrs` = `GET /storage/v1/b/<b>?alt=json&prettyPrint=false&projection=full` (`http_client.go:277`).
   - list يرسل `delimiter=` و`prefix=` و`startOffset=` و`endOffset=` و`matchGlob=` و`pageToken=` فارغة، و`versions=false` و`includeTrailingDelimiter=false` و`includeFoldersAsPrefixes=false` (`http_client.go:354-387`). على الخادم أن يعامل كل نص فارغ كغياب (§4.2).

## 10. ما يجب أن يفعله خادم Aruma

### 10.1 المسارات الدنيا لـ v0.3

| # | الطريقة والمسار (مع `/storage/v1` **وبدونه** لـ JSON) | يخدم |
|---|---|---|
| 1 | `GET /b/{b}` | `bucket.exists`/`getMetadata` (Node)، `Bucket.Attrs` (Go) |
| 2 | `GET /b/{b}/o` | `getFiles` و`Objects` |
| 3 | `GET /b/{b}/o/{o}` (و`alt=media` للبايتات) | getMetadata وexists وAttrs وتنزيل Node وGo مع JSON reads |
| 4 | `PATCH /b/{b}/o/{o}` | setMetadata وUpdate |
| 5 | `DELETE /b/{b}/o/{o}` | delete، وجزء من move |
| 6 | `POST /b/{sb}/o/{so}/rewriteTo/b/{db}/o/{do}` | copy وmove وCopierFrom. نفّذه في طلب واحد وأعد `done:true` |
| 7 | `POST /upload/storage/v1/b/{b}/o?uploadType=multipart` | Node `resumable:false`، وGo تحت 16 MiB |
| 8 | `POST /upload/storage/v1/b/{b}/o?uploadType=resumable` (بلا `upload_id`) | بدء الجلسة |
| 9 | `PUT` **و`POST`** `/upload/storage/v1/b/{b}/o?…&upload_id=…` | بيانات الجلسة واستعلام الحالة |
| 10 | `GET`/`HEAD` `/{b}/{o…}` (XML، مع `?generation=` اختياريًا) | قراءات Go الافتراضية |
| 11 | `GET /v0/b/{b}/o/{o}` (ردّه يتضمن `downloadTokens`) و`?alt=media&token=` | `getDownloadURL()` في FAN، ومشترك مع المسار A |

اختياري ومفيد: `uploadType=media` (POST بالوسائط فقط) [I، لا يستعمله العميلان افتراضيًا]. وكذلك `/download/storage/v1/b/{b}/o/{o}?alt=media` كما يفعل المحاكي [I].

### 10.2 السلوك الإلزامي

1. **التوجيه:** يُقرأ المسار من `EscapedPath()` ويُقسَّم قبل فك `%2F`. والاستعلام بقواعد النماذج. والقيم الفارغة تُعامَل كغياب. والمعاملات غير المعروفة تُتجاهل (`alt=json` و`prettyPrint` و`projection` و`fields` و`userProject` و`softDeleted=false`).
2. **الأنواع:** `generation` و`metageneration` و`size` و`objectSize` و`totalBytesRewritten` و`projectNumber` سلاسل. الأوقات RFC 3339. `crc32c` و`md5Hash` بـ Base64 القياسي مع الحشو.
3. **ردود الرفع:** 200 مع object JSON كامل فيه `crc32c` و`md5Hash` **محسوبان على الخادم**. إن أرسل العميل checksum مختلفًا يُرد 400 ولا يُنشأ الكائن.
4. **بدء resumable:** 200 مع `Location` مطلق. وتُدمج البيانات الوصفية من JSON مع `X-Upload-Content-Type`.
5. **الجلسة** تقبل `PUT` و`POST`، بثلاث صيغ لـ `Content-Range`:
   - `bytes a-b/*`: جزء وسيط. الرد غير النهائي:
     - إن احتوى الطلب `X-GUploader-No-308: yes` (عميل Go): **`200` مع `X-Http-Status-Code-Override: 308`** ومع `Range`، بلا جسم JSON.
     - وإلا (عميل Node): **`308` مع `Range: bytes=0-{last}`** ودون `Location`. تُحذف `Range` فقط حين لا يوجد أي بايت محفوظ.
   - `bytes a-b/T` و`bytes */T`: نهاية الرفع. الأخيرة جزء نهائي فارغ.
   - `bytes N-*/*` و`bytes N-*/T`: **بقية الكائن في هذا الطلب، ويُغلق الرفع عند انتهاء الجسم**، ولو كان الحجم صفرًا.
   - `bytes */*` مع جسم فارغ: استعلام عن الحالة.
6. **سلامة الأجزاء:**
   - Go: الجزء ذرّي، وتكراره مقبول، والفجوة تُرد بـ 400.
   - Node في وضع الطلب الواحد: احفظ البايتات تدريجيًا، وبلّغ عنها بدقة في `Range`.
   - يجب أن تكون الجلسة نفسها بلا ترويسة `Authorization`، وتصريحها رابطها (`upload_id` غير قابل للتخمين، ومقيّد بالـ bucket والاسم، وصالح لمدة محدودة).
7. **التنزيل (JSON وXML):**
   - `Content-Type` و`Content-Length`.
   - مع النطاق: **206** و`Content-Range: bytes a-b/T`. النطاق غير القابل للتحقيق يُرد بـ 416 [I].
   - `x-goog-hash: crc32c=…,md5=…` في ترويسة واحدة بلا مسافة، و`x-goog-stored-content-encoding: identity` (أو `gzip`).
   - `x-goog-generation` و`x-goog-metageneration` بالتهجئة الصحيحة.
   - `Last-Modified` بصيغة HTTP-date صالحة. `X-Goog-Meta-<k>` في XML [I]. و`ETag`.
   - لا ضغط ديناميكي.
8. **الشروط المسبقة:** `ifGenerationMatch` (والقيمة 0 تعني "يجب ألا يوجد") و`ifGenerationNotMatch` و`ifMetagenerationMatch` و`ifMetagenerationNotMatch` في الاستعلام، و`x-goog-if-generation-match` و`x-goog-if-metageneration-match` في XML. الفشل يُرد بـ 412. و`generation` في get وdelete وXML.
9. **الأخطاء:** `{"error":{"code":<int = status>,"message":…,"errors":[{"domain":"global","reason":…,"message":…}]}}` لكل JSON. ونص خام مقبول لـ `alt=media` وXML.
10. **العنوان:** لا يملي الخادم بادئة مسار. تعمل الجذور `/b` و`/storage/v1/b` و`/upload/storage/v1` و`/{bucket}/…` و`/v0` تحت المضيف نفسه. وفي حالة التعارض المسار `/b/...` لـ JSON يسبق `/{bucket}/{object}` لـ XML، فلا يُسمح بـ bucket اسمه `b` أو `storage` أو `upload` أو `download` أو `v0` [I].

### 10.3 ما يمكن تأجيله إلى ما بعد v0.3

compose و`moveTo` و`restore` وsoftDeleted والإصدارات (`versions=true`). ACL و`makePublic` وIAM والإشعارات وHMAC وإنشاء الـ buckets وحذفها. الروابط الموقّعة (XML مع V4). `X-Goog-Upload-Protocol` (gRPC/XML resumable). مفاتيح التشفير CSEK (`x-goog-encryption-*`) وKMS. `matchGlob` (يُرد عليه 400 صريح بدل التجاهل الصامت [I]). `fields` و`projection=full` (يُعاد الكائن كاملًا دون `acl`). وrewrite المتعدد الطلبات (`rewriteToken` و`maxBytesRewrittenPerCall`)، إذ نرد دائمًا `done:true`. وإلغاء الجلسة بـ `DELETE`، إذ لا يرسله أي من العميلين [V، بحث في `resumable-upload.js`].

### 10.4 قرارات مفتوحة

1. **المصادقة** (§3): FAN و"المحاكي" بلا مصادقة دائمًا. نحتاج ADR يختار بين نقطة GCS موثوقة شبكيًا ومعطلة افتراضيًا، وبين `Bearer` إلزامي، مع توثيق أن `file.save()` في Node لا يرسله في resumable.
2. **`getDownloadURL` في وضع المحاكي:** طلب v0 للبيانات الوصفية يصل بلا مصادقة، فلا تمييز بين المدير والعميل العام. هل تُطبَّق القواعد عليه كأي مجهول؟ يُنسَّق ذلك مع A.
3. **رموز التنزيل:** هل يولّد Aruma `firebaseStorageDownloadTokens` تلقائيًا للكائنات المرفوعة عبر GCS API، أم فقط عند طلب v0؟ سلوك Google هنا غير ظاهر في الشيفرة [I].
