# F: دروس معمارية لـ Cloud Storage (الاتساق، الخدمة الآمنة، الرفع المتقطع، الحصص)

- **الحالة:** بحث للمرحلة S0، 2026-10-23. يغذّي ADR-022 (معمارية Storage) وADR-023 (النطاق). لا يقرر شيئًا وحده.
- **المنهج:** مصادر أولية حيث أمكن: كود MinIO وgo-cloud وfirebase-js-sdk وnodejs-storage (`raw.githubusercontent.com`، `gh api`)، وثائق GCS وtus وGarage وCeph وAWS، كود محاكي firebase-tools المحلي (v15.34.0، MIT) صندوقًا مقروءًا لا مشغَّلًا، وكود Go القياسي المحلي (go1.27.2). ترويسات حية التقطتها بـ `curl -I` من مضيفات عامة (GitHub raw، googleusercontent، GCS) في 2026-10-23. **لم أطلب شيئًا من firebasestorage.googleapis.com** (لا Firebase حقيقي في هذا المسار).
- **العلامات:** [V] قرأته في المصدر الأولي في هذه الجلسة. [S] مصدر ثانوي. [U] لم أتحقق منه. [I] استنتاجي.
- **قيود ملزمة:** العزل بين المشاريع حسب ADR-017 (RLS **مؤجَّل**): كل جدول بـ `project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE`، وكل استعلام مقيّد بـ `project_number` أو معلَّم `-- scope:` (`internal/store/scopecheck`)، واختبارات عزل. وأي حساب ينشر قواعد مشروعه ويختار أسماء كائناته، فكل حد يصمد أمام **مشروع معادٍ** لا طلب معادٍ فقط.
- **ملفات أخرى:** `storage/A..E*.md` لمسارات أخرى؛ هنا الدروس المعمارية فقط، والتوصيات في آخر الملف.

---

## 1. البيانات الوصفية والبايتات: كيف تنكسر الأنظمة الحقيقية

### 1.1 ما تفعله الأنظمة

| النظام | التصميم | الدرس | المصدر |
|---|---|---|---|
| **MinIO** | يكتب إلى `.minio.sys/tmp/<uuid>`، يتحقق أن المكتوب = الحجم المعلن (`errLessData`/`errMoreData`)، ثم `Fdatasync`، ثم `renameAll` إلى المسار النهائي. والبيانات الوصفية `xl.meta` بجانب البيانات (لا قاعدة منفصلة) | ملف مؤقت ← تحقق الحجم ← fsync ← rename هو الحد الأدنى للمتانة | [V] `cmd/xl-storage.go`:2180-2198 و2227-2243 (commit `b8631cf5`) |
| **Garage** | البيانات الوصفية في LMDB أو SQLite، والكتل على القرص. `metadata_fsync` و`data_fsync` **معطّلان افتراضيًا**؛ «LMDB is prone to database corruption after an unclean shutdown»، والعلاج لقطات دورية `metadata_auto_snapshot_interval` | إطفاء fsync افتراضيًا ثمنه فساد عند انقطاع الكهرباء؛ تشكو منه الوثائق نفسها | [V] garagehq.deuxfleurs.fr/documentation/reference-manual/configuration |
| **SeaweedFS** | Filer (بيانات وصفية في Postgres/MySQL/Redis...) فوق Volume servers (البايتات) — نمط «metadata في SQL، bytes في مكان آخر» حرفيًا. أداة `volume.fsck` تجد القطع اليتيمة. وثغرة: كتابة FUSE طويلة ترفع القطع ولا تحفظ المرجع إلا عند الإغلاق، فقد يحذفها `volume.fsck` كيتيمة؛ الحل تفريغ دوري للمرجع (`-metadataFlushSeconds`، 120 ث) | **أداة اليتامى تحتاج مهلة سماح** أطول من أطول عملية جارية، وإلا حذفت بيانات حية | [S] wiki weed-shell، وcommit في المرآة gitea (لم أقرأ الـ PR في GitHub) |
| **Ceph RGW** | فهرس الـ bucket منفصل عن الكائنات في RADOS، ومعاملة بمرحلتين: prepare على الفهرس ← كتابة الكائن ← complete/cancel. إن مات RGW في الوسط «an index entry may get stuck in this 'pending' state»، ويُصلَح كسولًا عند القراءة بـ«dir suggest» | **سجل نوايا قبل الكتابة** يجعل الاسترداد محليًا، دون مسح التخزين كله | [S] docs.ceph.com/en/latest/dev/radosgw/bucket_index (عبر نتائج البحث) |
| Ceph RGW | اليتامى «RADOS objects that are left behind after their associated RGW objects are removed»، سببها «bugs that prevented these RADOS objects from being deleted»؛ الأداة `rgw-orphan-list` **تجريبية**، ويجب «sanity checked» قبل الحذف، وتعطي إيجابيات كاذبة على pool خاطئ أو buckets بلا فهرس | **مسح اليتامى يُبلِّغ ولا يحذف** افتراضيًا | [V] docs.ceph.com/en/squid/radosgw/orphans |
| **tusd** (local) | ملفان لكل رفع (البيانات + `.info`)، وأقفال على القرص بالـ hard links؛ تفشل على NFS قديم ومجلدات VirtualBox | لا نعتمد على دلالات نظام ملفات خاصة (hard links، rename عبر الأجهزة) | [V] tus.github.io/tusd/storage-backends/local-disk |
| **tusd** (S3) | يخزن القطع مؤقتًا على القرص قبل S3 ليبلغ الحد الأدنى للجزء، ويحفظ ما دونه في كائن `.part`؛ ويتطلب «strong read-after-write consistency» وقفلًا موزعًا مع أكثر من نسخة | الرفع المتقطع فوق S3 يحتاج تخزينًا مرحليًا (staging) | [V] tus.github.io/tusd/storage-backends/aws-s3 |

### 1.2 `gocloud.dev/blob/fileblob`: ما تكشفه قراءة الكود

الإصدار v0.47.0، الملف عند commit `aa1fa83b` (2026-09-21):

1. **لا fsync إطلاقًا** [V]: `grep Sync fileblob.go` لا يجد شيئًا. `Close` يغلق الملف المؤقت، يكتب sidecar `.attrs` بـ `os.Create` عادي، ثم `os.Rename` (`fileblob.go`:836-871؛ `attrs.go`:40-43). بعد انقطاع كهرباء قد يبقى الاسم الجديد على ملف فارغ أو ناقص في بعض أنظمة الملفات [I، وهو الخطر الذي يتجنبه MinIO بـ `Fdatasync` قبل rename].
2. **rename عبر الأجهزة:** الملف المؤقت افتراضيًا في `os.TempDir`، فإن كان على جهاز آخر «`os.Rename` will fail with `invalid cross-device link`»؛ والعلاج `NoTempDir` (`fileblob.go`:18-23) [V]. وتاريخ الخيار متقلب: #3294، #3299، #3314 [V، عناوين القضايا].
3. **ملفات `.tmp` يتيمة** بعد قتل العملية أثناء الكتابة: #3286 (2023) [V].
4. **عبور مسارات:** `escapeKey` لم يهرب `..` وحده، فمفتاح `..` أو بادئة `../` تخرج من جذر الـ bucket (قراءة وكتابة وحذف عشوائي): #3667، مغلقة 2026-03 [V].
5. **`IfNotExist` ليس ذريًا** بين كاتبَين متزامنين (mutex لكل writer): #3809، مفتوحة 2026-10-23 [V].
6. **s3blob:** ذاكرة عالية مع رفعات multipart متوازية لأن كل writer ينشئ uploader بمخزن أجزاء خاص (#2807، 2020) [V].

**ما يترتب [I]:** gocloud.dev/blob مناسب لـ S3 (حيث `PutObject`/`CompleteMultipartUpload` الناجح متين)، لكن fileblob **لا يكفي وحده** لضمان «الملف متين قبل صف البيانات الوصفية». الخيارات: (أ) سائق قرص محلي صغير خاص بنا (temp في المجلد نفسه ← `f.Sync()` ← rename ← `Sync` للمجلد الأب)، أو (ب) fileblob مع `NoTempDir` و`metadata=skip` وقبول خطر انقطاع الكهرباء موثّقًا. وفي الحالتين: **مفتاح الملف لا يُشتق من اسم الكائن أبدًا** (البند 4 يثبت أن الهروب النصي يخطئ).

### 1.3 أنماط الأعطال وترتيب الكتابة المقترح [I]

Postgres متين عند الـ commit؛ إذن كل ما يشير إليه صف ملتزم يجب أن يكون متينًا **قبل** الـ commit، وكل ما يُحذف يُحذف **بعد** الـ commit.

| النافذة | النتيجة دون تصميم | التصميم المقترح |
|---|---|---|
| الملف كُتب، العملية ماتت قبل commit الصف | ملف يتيم لا يعرفه أحد | **صف نية** (Ceph prepare): `INSERT` في `storage_blob_intents (project_number, blob_key, created_at)` **قبل** كتابة الملف النهائي، ويُحذف في معاملة إدراج الكائن نفسها. المنظّف يحذف ملفات النوايا الأقدم من مهلة سماح |
| الصف التزم والملف غير متين | كائن يقرأ ملفًا فارغًا/ناقصًا | fsync للملف والمجلد قبل الـ commit (local)؛ نجاح `Close` في S3 |
| استبدال كائن موجود (generation جديد) | rename فوق ملف القارئ الحالي | **مفتاح ملف جديد لكل generation** (`p/<project_number>/<uuid>`)، لا كتابة فوق ملف. القارئ القديم يكمل على الملف القديم |
| حذف/استبدال، ماتت العملية قبل حذف الملف القديم | تسرب مساحة | في معاملة الحذف/الاستبدال نفسها: `INSERT INTO storage_blob_gc (project_number, blob_key)` (transactional outbox)؛ عامل يحذف بعد الـ commit، ويكرر بأمان (الحذف idempotent) |
| حذف الملف قبل الصف | 500 للقراء بين اللحظتين | ممنوع: الملف يُحذف بعد commit الصف دائمًا |
| أخطاء لم يتوقعها التصميم | تسرب صامت | مسح دوري **يبلّغ فقط** (مقياس + سجل): ملفات بلا صف ولا نية، وصفوف بلا ملف؛ بمهلة سماح > أقصى عمر لجلسة رفع (درس SeaweedFS وCeph) |

---

## 2. خدمة ملفات المستخدمين من نطاق اللوحة نفسه

### 2.1 ما تفعله الجهات الكبرى

- **Google:** نقلت المحتوى عالي الخطورة إلى نطاقات معزولة، أشهرها `*.googleusercontent.com` [S، Google Security Blog، «Content hosting for the modern web»، 2012-08-29؛ لم أستطع قراءة متن المقال آليًا].
- **دليل Google الحديث** (David Dworken، «Securely hosting user data in modern web applications»، web.dev، 2023-06-08) [V]:
  - للمحتوى غير النشط: `X-Content-Type-Options: nosniff`، `Content-Disposition: attachment; filename="download"`، `Content-Security-Policy: sandbox`، `Content-Security-Policy: default-src 'none'`، `Cross-Origin-Resource-Policy: same-site`؛ و«When in doubt, `application/octet-stream` is a safe choice».
  - للمحتوى الذي يجب أن يُعرض (HTML، SVG): رأس CSP `sandbox` «likely a sufficient solution» إن لم يكن Spectre واختراق المُصيِّر ضمن نموذج التهديد. وللعزل الأقوى: نطاقات فرعية لكل ملف في Public Suffix List.
  - دفاع إضافي: `frame-ancestors 'none'`، `X-Content-Security-Policy: sandbox` لـ IE11.
- **ترويسات حية** (`curl -I`، 2026-10-23) [V]:

| المضيف | Content-Type | CSP | nosniff | CORP | ACAO | Content-Disposition |
|---|---|---|---|---|---|---|
| `raw.githubusercontent.com` (README.md) | `text/plain; charset=utf-8` | `default-src 'none'; style-src 'unsafe-inline'; sandbox` | نعم | `cross-origin` | `*` | — |
| `raw.githubusercontent.com` (PNG) | `image/png` | نفسها | | | | |
| `raw.githubusercontent.com` (SVG) | `image/svg+xml` | نفسها | | | | |
| `lh3.googleusercontent.com` (صورة) | `image/png` | — | نعم | `cross-origin` | `*` | `inline;filename="unnamed.png"` |
| `storage.googleapis.com` (كائن عام) | `text/csv` | — | — | — | — | — ، ومعه `x-goog-hash: crc32c=…`، `md5=…`، `x-goog-stored-content-encoding: identity`، `accept-ranges: bytes` |

  الدرس: GitHub يخدم SVG كـ `image/svg+xml` لكن تحت `sandbox`، فيُعرض ولا ينفّذ سكربتًا؛ و**كلا مضيفَي المحتوى يرسلان `CORP: cross-origin`** لأن المحتوى يُضمَّن من مواقع أخرى.

- **محاكي Firebase** (MIT) [V] `src/emulator/storage/apis/shared.ts`:
  - `Content-Disposition` = `contentDisposition` المخزّن **أو `attachment`** افتراضيًا، مع `filename*` بترميز RFC 5987 لآخر مقطع من الاسم (السطور 27-31).
  - `Content-Type` المخزّن أو `application/octet-stream` (24)؛ `ETag`، `x-goog-generation`، `x-goog-hash` (40-45).
  - **فك ضغط gzip في الذاكرة بالكامل** (`gunzipSync`) حين المخزن gzip والعميل لا يقبل gzip، تقليدًا لـ decompressive transcoding في GCS (14-22)، ونطاق واحد فقط من Range (49-61).
  - في `apis/firebase.ts`:548-560 تُنسخ `contentDisposition`/`contentEncoding`/`cacheControl`/`contentLanguage` كما خزّنها العميل.
- **إنتاج Firebase:** ترويسات `firebasestorage.googleapis.com` لـ `alt=media` **[U]**: لم أجد مصدرًا موثوقًا يذكر قيمة `Content-Disposition` الافتراضية ولا وجود nosniff/CORP. التسجيل من مشروع المالك في S0 يحسمها. لكن مهما كانت، Firebase يخدم من نطاق **لا كوكيز لوحة فيه**، ونحن لا.
- **CORS للتنزيل:** وثائق Firebase: «To download data directly in the browser, you must configure your Cloud Storage bucket for cross-origin access (CORS)» بملف `cors.json` و`gsutil cors set` [V، firebase.google.com/docs/storage/web/download-files]. أي `getBytes/getBlob` من المتصفح يحتاج إعدادًا صريحًا؛ أما `<img src>` فلا يحتاج CORS.

### 2.2 ما يعنيه هذا لـ Aruma [I]

1. **الترويسات العامة الحالية تتعارض مع التضمين:** `securityHeaders` يضع على كل رد `Cross-Origin-Resource-Policy: same-origin` و`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` (`internal/server/server.go`:132-147) [V]. لو بقيت `CORP: same-origin` على `alt=media` فأي تطبيق ويب على أصل آخر يضع رابط التنزيل في `<img>` أو `<video>` **سيُحجب**؛ وهذا الاستعمال الأشيع لـ `getDownloadURL()`. المطلوب لمسار التنزيل: `CORP: cross-origin` (كما GitHub وgoogleusercontent)، لا `same-site` كما يقترح دليل Dworken، لأن Firebase نفسه عبر أصول دائمًا.
2. **الخطر الحقيقي من النطاق المشترك** هو التصفح المباشر (top-level navigation) لملف نشط: HTML أو SVG أو XML. `sandbox` بلا `allow-scripts` يجعل المستند بأصل معتم فلا سكربت ولا وصول لكوكيز اللوحة أو localStorage. و`nosniff` يمنع تحويل `text/plain` إلى HTML. و`Content-Disposition: attachment` طبقة ثالثة **لكنها لا تُفرض** إلا إن لم يطلب العميل غيرها، لأن العميل يملك `contentDisposition` في metadata (`firebase.ts`:548-550)؛ لذا لا نعتمد عليها.
3. **Service Worker:** ملف JS مرفوع على أصل اللوحة يمكن تسجيله Service Worker بنطاق `/v0/b/<bucket>/o/` إن وُجد XSS في أي صفحة على الأصل، فيعترض كل تنزيلات الـ bucket ويبقى بعد إصلاح الثغرة. المتصفح يرسل الرأس `Service-Worker: script` عند جلب سكربت SW (مواصفة W3C Service Workers) [U: لم أقرأ نص المواصفة في هذه الجلسة]. الحل: رفض أي طلب تنزيل يحمل هذا الرأس (أو `Sec-Fetch-Dest: serviceworker`) بـ 403.
4. **لا sniffing من طرفنا:** `http.ServeContent` يخمن النوع من الامتداد ثم من أول 512 بايت إن لم يُضبط `Content-Type` (`net/http/fs.go`:222-225، 287-295) [V]. ملف اسمه `x.html` بلا contentType سيصير `text/html`. يجب ضبط `Content-Type` دائمًا قبل `ServeContent` (`application/octet-stream` إن كان فارغًا).
5. **Range:** `ServeContent` يتجاهل الـ Range إن تجاوز مجموع النطاقات حجم الملف («probably an attack, or a dumb client») (`fs.go`:340-346) [V]. وSafari/iOS يحتاج Range ليشغّل الفيديو ويقفز فيه [S، Apple «Creating Video» القديمة، WebKit bug 211323]. المحاكي يخدم النطاق الأول فقط من الطلبات المتعددة (`shared.ts`:51)؛ وسلوك الإنتاج مع multi-range [U].
6. **gzip:** لا نقلّد `gunzipSync` الكامل: كائن 10 MB مضغوط من صفر قد ينتفخ إلى غيغابايتات في ذاكرة الخادم (zip bomb). إن طبّقنا decompressive transcoding فبتدفق `gzip.Reader` محدود بـ `io.LimitReader` وسقف قابل للضبط، وإلا نخدم البايتات كما هي ونوثّق الفرق.
7. **نطاق منفصل اختياري:** ADR-008 (منفذ واحد ونطاق واحد) يبقى الافتراضي، لكن إعداد اختياري `storage.download_origin` (مثل `files.example.com`) يعطي عزل googleusercontent لمن يريده، دون كسر التوافق (رابط التنزيل يبنيه الخادم في `downloadTokens`؟ لا: يبنيه العميل من مضيف المكتبة — [U] يحسمه مسار الإنتاج S6). لذلك: الترويسات إلزامية دائمًا، والنطاق المنفصل تحسين لاحق.

### 2.3 الترويسات المقترحة لكل رد `alt=media` (تنزيل برمز أو بقواعد)

```
Content-Type: <contentType المخزن، أو application/octet-stream إن فارغ>   (لا sniffing أبدًا)
X-Content-Type-Options: nosniff
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox
Cross-Origin-Resource-Policy: cross-origin
X-Frame-Options: DENY            (موجود عامًا؛ يمنع تأطير ملف داخل صفحة مهاجم)
Referrer-Policy: no-referrer     (موجود عامًا)
Content-Disposition: <المخزن إن وُجد، وإلا كما يسجّله الإنتاج في S0>; filename*=UTF-8''<آخر مقطع>
Cache-Control: <المخزن>، وإلا قيمة الإنتاج المسجّلة
ETag / x-goog-generation / x-goog-metageneration / x-goog-hash: crc32c=..,md5=.. / x-goog-stored-content-encoding
Accept-Ranges: bytes
```

- CSP هي سياسة GitHub raw حرفيًا: مجرّبة على ملايين الصور وSVG المفتوحة مباشرة. إن أظهرت مصفوفة المتصفحات أن الفيديو/الصوت/PDF لا يُعرض تصفحًا مباشرًا تحت `default-src 'none'`، نضيف `media-src`/`img-src` فقط، ولا نرفع `sandbox` أبدًا [U: لم أختبر في متصفح].
- وعلى رد 403/404 من مسار التنزيل: CSP العامة كما هي.

### 2.4 حالات اختبار إلزامية (acceptance + e2e متصفح)

1. رفع `evil.html` بـ `contentType: text/html` ثم فتح رابط التنزيل في Chromium وFirefox وWebKit (Playwright): لا يُنفَّذ `<script>`، و`document.cookie`/`localStorage` غير متاحين، و`window.origin === "null"`.
2. `evil.svg` بـ `image/svg+xml` و`<script>` داخله: يُعرض الشكل ولا يعمل السكربت؛ ويعمل داخل `<img>` في صفحة أخرى.
3. ملف بلا contentType واسم `x.html` ومحتوى HTML: الرد `application/octet-stream` (لا sniff من الخادم ولا من المتصفح).
4. `contentType: text/plain` ومحتوى `<html><script>`: يُعرض نصًا.
5. `contentDisposition: inline` يضعه العميل على HTML: ما زال sandbox يمنع السكربت.
6. صفحة على أصل آخر تضع `<img src=downloadURL>` و`<video src=downloadURL>`: تُحمَّل (CORP cross-origin)، ومع COEP `require-corp` على الصفحة أيضًا.
7. `fetch(downloadURL)` من أصل آخر دون إعداد CORS: يفشل كما في Firebase؛ ومع إعداد CORS للـ bucket: ينجح.
8. `navigator.serviceWorker.register(downloadURL)` من صفحة على أصل اللوحة: يُرفض (403 لرأس `Service-Worker: script`).
9. `<iframe src=downloadURL>` في صفحة أخرى: محجوب (`X-Frame-Options: DENY`).
10. Range: `bytes=0-0` ← 206؛ نطاق خارج الحجم ← 416 مع `Content-Range: bytes */N`؛ نطاقات مجموعها > الحجم ← 200 كاملًا؛ فيديو MP4 يُشغَّل ويُقفز فيه في Safari/iOS (e2e).
11. كائن `contentEncoding: gzip` بحجم صغير يتمدد إلى 1 GiB، وطلب بلا `Accept-Encoding: gzip`: الذاكرة لا تتجاوز سقفًا ثابتًا، والرد إمّا البايتات كما هي أو خطأ واضح.
12. Admin Node `file.download()` بالـ validation الافتراضي: ينجح (يحتاج `x-goog-hash` و`x-goog-stored-content-encoding`، انظر 3.4).

---

## 3. الرفع المتقطع

### 3.1 البروتوكولات

| | Firebase client (`X-Goog-Upload-*`) | GCS JSON (Admin) | tus 1.0 |
|---|---|---|---|
| البدء | `X-Goog-Upload-Protocol: resumable`، `Command: start`، `Header-Content-Length`، `Header-Content-Type` ← `X-Goog-Upload-URL` [V، `requests.ts`:428-441] | `uploadType=resumable` ← session URI | `POST` + `Upload-Length` ← `Location` |
| الاستئناف | `Command: query` ← `X-Goog-Upload-Size-Received` [V، 465-471] | `PUT` بلا بيانات و`Content-Range: bytes */N` ← 308 + `Range` [V، الخطة/nodejs-storage] | `HEAD` ← `Upload-Offset` («MUST always include») [V] |
| الإرسال | `Command: upload` أو `upload, finalize` مع `X-Goog-Upload-Offset` [V، 540-548] | `PUT` + `Content-Range` | `PATCH` + `Upload-Offset`؛ عدم التطابق ← **409** دون تعديل [V] |
| الإزاحة القديمة | [U] في الإنتاج. المحاكي **يتجاهل `X-Goog-Upload-Offset` ويلحق البايتات دائمًا** (`upload.ts`:176-184) [V] — إعادة المحاولة تفسد الملف عنده | «Once Cloud Storage persists bytes ... those bytes cannot be overwritten, and Cloud Storage ignores attempts to do so» [V، docs resumable-uploads] | 409 |
| البيانات الجزئية | [U] | «query the server for the persisted offset» (قد يكون أقل مما أُرسل) [V] | «SHOULD always attempt to store as much of the received data as possible» [V] |
| حجم القطعة | المحاكي يرسل `x-goog-upload-chunk-granularity: 10000` (`firebase.ts`:261) [V]؛ الإنتاج [U] | «must be a multiple of 256 KiB» في المكتبات [V] | حر |
| العمر | [U] | الجلسة «active for up to a week»؛ بعدها 410 ثم 404 [V]؛ الحصص: 7 أيام [V] | `Upload-Expires`؛ بعدها 404/410 [V] |
| السلامة | [U] | MD5 عبر `Content-MD5` للكائن النهائي؛ «Integrity checks cannot be performed on intermediate chunks» [V]؛ وmetadata.crc32c/md5Hash يجعل **الخادم** يتحقق [V، nodejs-storage] | `Upload-Checksum` لكل قطعة، 460 ويُرمى الجزء [V] |
| التفويض | المحاكي يحفظ `Authorization` عند start ويقيّم القواعد **عند finalize فقط** (`firebase.ts`:253-259، 345-347، و`finalizeOneShotUpload`) [V] | «This session URI acts as an authentication token» [V] | خارج المواصفة |
| finalize مكرر | المحاكي يعيد رمز الرد السابق (`UploadPreviouslyFinalizedError`، `firebase.ts`:340-342) [V] | | `PATCH` على رفع نهائي ← 403 (concatenation) [V] |

### 3.2 ما ترسله مكتبة الويب فعلًا [V]

`firebase-js-sdk` `packages/storage/src/task.ts` (main، 2026-10-23):
- القطعة الأولى 256 KiB (`RESUMABLE_UPLOAD_CHUNK_SIZE`، `requests.ts`:497)، وتتضاعف بعد كل نجاح ما دام `currentSize * 2 < 32 MiB` (`task.ts`:336-343)؛ أي أن أكبر قطعة فعليًا **16 MiB** رغم تعليق «Max chunk size is 32M». وتعود إلى 256 KiB بعد أي خطأ (120-123).
- طلبات `upload` لا تُعاد تلقائيًا: «each retry should be preceded by another query request» (`task.ts`:319) [V]. أي أن الخادم سيرى `query` ثم `upload` بالإزاحة التي أعلنها.
- الرفع resumable حين الحجم > 256 KiB (`task.ts`:173)، وإلا multipart.
- مهلات إعادة المحاولة: 10 دقائق للرفع، دقيقتان للعمليات (`constants.ts`:36، 43).

**ما يترتب [I]:**
- **القطع الأولى (256 KiB…4 MiB) أصغر من حد S3 الأدنى للجزء** (5 MiB، والأخير بلا حد أدنى؛ 10,000 جزء كحد أقصى) [V، docs.aws.amazon.com/AmazonS3/latest/userguide/qfacts.html]. فلا يمكن ربط قطعة العميل بجزء S3 واحد لواحد؛ نحتاج تخزينًا مرحليًا (كما tusd).
- **الأبسط والصحيح لـ v0.3:** الجلسة تُكتب في ملف staging على القرص المحلي لكلا الواجهتين الخلفيتين (قرص وS3)، وعند finalize: تحقق ← رفع إلى المخزن النهائي (rename محلي أو `PutObject`/multipart إلى S3 من الملف) ← commit. ثمنه: قرص محلي بحجم الرفعات المتزامنة، وزمن finalize لملف كبير على S3. الحل التدفقي (أجزاء S3 بـ 5+ MiB أثناء الرفع) تحسين لاحق.

### 3.3 دلالات مقترحة للجلسة [I]

- **الحقيقة في القرص، والصف للبيانات الوصفية:** طلب `upload` يلحق ما وصل بالملف المرحلي، ثم `f.Sync()`، ثم يحدّث `received` في الصف. بعد انهيار بين الاثنين يكون طول الملف ≥ `received`؛ عند الطلب التالي نقص الملف إلى `received` (الصف هو الحقيقة المعلنة للعميل). وهكذا لا يُبلَّغ العميل أبدًا بإزاحة غير متينة.
- **الإزاحة:** `offset == received` ← إلحاق؛ `offset < received` ← تجاهل البايتات المتداخلة وإلحاق الباقي (دلالة GCS: المحفوظ لا يُكتب فوقه)؛ `offset > received` ← رفض (فجوة). رموز الحالة والنصوص تُسجَّل من الإنتاج في S0 قبل التثبيت.
- **انقطاع الطلب في المنتصف:** نحفظ ما وصل (tus) لأن العميل سيسأل بـ `query` على أي حال. ولا نعلن إلا ما فُرِّغ إلى القرص.
- **الهاش:** crc32c (Castagnoli، `hash/crc32`) وmd5 يُحسبان أثناء التدفق. حالة الهاش تُحفظ مع الإزاحة: هاشات المكتبة القياسية تنفّذ `encoding.BinaryMarshaler` «allows its internal state to be saved and used for additional processing later» (`hash/hash.go`:12-18) [V]. البديل الأبسط: إعادة قراءة الملف المرحلي عند finalize (كلفة قراءة إضافية).
- **finalize:** يتحقق من (1) الحجم = `X-Goog-Upload-Header-Content-Length` المعلن، (2) crc32c/md5 إن أرسلها العميل في metadata (nodejs-storage: «providing `metadata.crc32c` or `metadata.md5Hash` will cause the server to perform validation») [V، `src/file.ts`:1895-1904 commit `7ed31194`]، (3) القواعد. ثم ينقل الملف ويكتب الصف في معاملة. و**finalize مكرر** يعيد النتيجة نفسها (المحاكي يفعل ذلك).
- **القواعد مرتين:** عند `start` بالحجم والنوع المعلنين (رفض مبكر قبل نقل الغيغابايتات)، وعند `finalize` بالقيم الفعلية وبتفويض `start`. الرفض في أيهما رفض. هذا لا يكون أبدًا أكثر تساهلًا من تقييم واحد؛ وقد يكون أشد إن كان الإنتاج يقيّم مرة واحدة على قيم مختلفة [U، سؤال أوراكل لـ S0].
- **معرّف الجلسة:** 128 بت عشوائية (GCS: الـ URI نفسه رمز تفويض). مقيّد بـ `project_number` وbucket واسم الكائن في الصف؛ طلب على مسار آخر بالمعرّف نفسه يُرفض.
- **العمر:** 7 أيام كـ GCS افتراضيًا وقابل للضبط؛ المنظّف يحذف الملف المرحلي والصف معًا. والبايتات المرحلية **تُحسب في حصة المشروع** (3.5).

### 3.4 Admin SDKs والتحقق من السلامة

- `@google-cloud/storage`: «By default, data integrity is validated with a CRC32c checksum» عند التنزيل والرفع (`src/file.ts`:1424-1428) [V]. وعند التنزيل لا يتحقق إلا إن كان `x-goog-stored-content-encoding` = `identity` (أو gzip مع محتوى مضغوط)، ويقرأ الهاش من `x-goog-hash` (1590-1600) [V]. إذن: `crc32c` بترميز GCS (base64 لأربعة بايتات big-endian) و`md5Hash` (base64) في البيانات الوصفية، والترويستان في التنزيل، وإلا فشلت التحقق أو تُخطّى بصمت.

### 3.5 الملفات الكبيرة في Go [V، إلا ما عليه I]

- **الحد:** `http.MaxBytesReader` «prevents clients from accidentally or maliciously sending a large request» ويطلب إغلاق الاتصال بعد الحد (`net/http/request.go`:1199-1209). الحد لكل طلب = `min(الحد الأقصى للكائن، المعلن − received)`.
- **لا تخزين في الذاكرة:** multipart الخاص بـ Firebase (`multipart/related`: جزء JSON ثم البيانات) يُقرأ بـ `mime/multipart.NewReader` جزءًا جزءًا، وجزء الـ metadata بسقف صغير؛ لا `ParseMultipartForm`/`FormValue` (CVE-2023-45290: سطر طويل يخصص ذاكرة بلا حد قبل go1.22.1) [S]. المحاكي يقرأ الجسم كله إلى Buffer (`reqBodyToBuffer`، `firebase.ts`:366-368) — لا نقلده [V].
- **المهلات:** الخادم يبقي `ReadTimeout`/`WriteTimeout` 60 ث (`internal/server/server.go`:196-200) [V]. معالج الرفع **لا** يصفّر المهلة كما في WebSocket (`rtdb/api/ws.go`:44-46) [V]، بل يمدّها **تدريجيًا**: بعد كل قراءة ناجحة `rc.SetReadDeadline(now + 30s)` [I]. هكذا رفع 5 GiB بطيء لكن متقدم يكمل، وslowloris (بايت كل 29 ث) يُقطع عند سقف كلي للطلب أو حد أدنى للمعدل.
- **التنزيل:** `ServeContent` فوق `io.ReadSeeker`؛ قارئ gocloud يدعم Seek منذ #3142 [V، عنوان القضية]. مع `SetWriteDeadline` متدرج بالمنطق نفسه.

---

## 4. الحصص وإساءة الاستعمال

### 4.1 أسماء الكائنات

- **GCS** [V، docs.cloud.google.com/storage/docs/objects]: «any sequence of valid Unicode characters»، 1-1024 بايت UTF-8، لا CR ولا LF، لا يبدأ بـ `.well-known/acme-challenge/`، لا `.` ولا `..` كاسم كامل. ويُنصح بتجنب `#`، `[ ] * ?`، محارف تحكم XML، و`./`/`../` كمقاطع. الصفحة **لا تذكر** حساسية الحالة ولا تطبيع Unicode.
- **مكتبة الويب** تحذف المقاطع الفارغة (`a//b` ← `a/b`) ولا تمس `.` و`..` (`path.ts`:37-47) [V]؛ فالاسم `a/../b` يصل كما هو. والمحاكي في `list` يحذف الأسماء ذات المقاطع الفارغة (`firebase.ts`:192-194، 567-581) [V]، لكن GCS JSON/Admin يقبلها.
- **Postgres:** «the character with code zero (sometimes called NUL) cannot be stored» في `text` [V، postgresql.org/docs/current/datatype-character.html]. إن كان U+0000 مقبولًا في GCS («any valid Unicode») [U] فعمود `text` سيفشل بخطأ 500؛ القرار: رفضه صراحة بـ 400 وتوثيقه، أو `bytea`.
- **ما يترتب [I]:**
  - الاسم **نص معتم**: لا تطبيع NFC/NFD، ولا طي حالة، ولا تفسير لـ `..`. `Café` بـ NFC و`Café` بـ NFD كائنان مختلفان (كما في GCS ما دام لا يذكر تطبيعًا)، ويُختبر ذلك ضد الإنتاج.
  - ولذلك مفتاح التخزين **لا يُشتق من الاسم** (`p/<project_number>/<uuid>`): APFS على macOS لا يفرّق الحالة افتراضيًا وNTFS لا يقبل `:` و`<>|"`، وتجربة fileblob (#3667) تثبت أن الهروب النصي يخطئ.
  - عمود الاسم `text COLLATE "C"`: الترتيب بالبايتات كترتيب GCS في list، والفهرس الفريد `(project_number, bucket, name)` يفرّق كل البايتات. وهذا ما فعلناه في RTDB (`internal/store/migrations/rtdb/00001_init.sql`:26-31) [V].
  - التحقق عند الحد الأمامي: UTF-8 صالح، ≤ 1024 بايت، لا CR/LF/NUL، القيود المحجوزة أعلاه؛ ويُختبر بـ fuzz.

### 4.2 البيانات الوصفية

- GCS: «8 KiB combined for all custom metadata keys and values»، وتحديث واحد للبيانات الوصفية في الثانية لكل كائن [V، docs.cloud.google.com/storage/quotas].
- [I] نفرض 8 KiB للـ customMetadata (المجموع بالبايتات بعد فك JSON)، وسقفًا لعدد المفاتيح، وسقفًا لجسم JSON الخام قبل فكّه (مثلًا 64 KiB) حتى لا يُخصَّص قبل الفحص. وحدود `contentType`/`cacheControl`/`contentDisposition` كطول رأس معقول (الخادم يرفض الرؤوس > 64 KiB أصلًا، `server.go`:197).
- `contentDisposition` و`contentType` يعودان في رؤوس الاستجابة: يُرفض فيهما CR/LF وغير المحارف المرئية (حقن رؤوس) [I؛ `net/http` يرفض CR/LF في قيم الرؤوس عند الكتابة، فالفشل سيكون 500 لا 400].

### 4.3 الحصص والمعدلات

- **GCS:** ~1,000 كتابة و~5,000 قراءة في الثانية لكل bucket مبدئيًا ثم يتوسع، وكتابة واحدة في الثانية للاسم نفسه («429 Too Many Requests») [V، quotas + objects]. [I] لا نقلّد حد «1/ث لكل كائن»: الحد ليس صلاحية، وغيابه لا يجعلنا أكثر تساهلًا في القواعد؛ نوثّقه فرقًا.
- **Firebase:** لا حصة حجم لكل مشروع غير الفوترة؛ الحماية الوحيدة شروط مثل `request.resource.size < 5 * 1024 * 1024` في القواعد [V، storage rules-conditions]. وحادثة «ملف عام واحد، فاتورة ~98 ألف دولار في يوم» تظهر خطر التنزيل المتكرر [S، مدونة ثانوية، لا نعتمد على أرقامها].
- **ما يترتب [I]** (حدود قابلة للضبط كحدود RTDB):
  - حجم أقصى للكائن (افتراضي معتدل، لا 5 TiB).
  - **حصة بايتات لكل مشروع** تشمل البايتات المرحلية. وتُحجز عند `start` بالحجم المعلن (`UPDATE ... SET reserved = reserved + $n WHERE project_number = $1 AND used + reserved + $n <= quota`)، وتتحول إلى `used` في معاملة commit، وتُحرَّر عند الإلغاء أو الانتهاء. دون الحجز، مئة رفع متوازٍ يمر كل منها من الفحص.
  - عدد الجلسات المفتوحة لكل مشروع ولكل uid/IP، ومعدل `start` عبر `internal/platform/ratelimit`.
  - مساحة قرص حرة دنيا لمجلد staging: تحتها يُرفض `start` بـ 503 بدل أن يمتلئ القرص المشترك مع Postgres.
  - لا فك ضغط ولا معالجة صور على الخادم في v0.3 (لا zip bombs إلا عبر transcoding، 2.2-6).

---

## 5. التوصيات لـ ADR-022 وADR-023

| # | القرار المقترح | الدليل | الثقة |
|---|---|---|---|
| S1 | **مفتاح الملف** `p/<project_number>/<uuid>`، جديد لكل generation، لا يُشتق من الاسم، ولا كتابة فوق ملف قائم | fileblob #3667 (عبور مسارات)؛ APFS/NTFS؛ القارئ المتزامن | عالية |
| S2 | **ترتيب الكتابة:** صف نية ← ملف متين (fsync للملف والمجلد، أو نجاح S3) ← معاملة واحدة (صف الكائن + حذف النية + `storage_blob_gc` للقديم) ← حذف القديم بعد الـ commit. ومسح دوري **يبلّغ ولا يحذف** بمهلة سماح > عمر الجلسة | MinIO (`Fdatasync` قبل rename)؛ Ceph prepare/complete وتحذير rgw-orphan-list؛ SeaweedFS volume.fsck؛ Garage fsync معطّل = فساد | عالية |
| S3 | **السائق المحلي ليس fileblob كما هو:** إمّا سائق قرص صغير خاص (temp في المجلد نفسه، `Sync`، rename، `Sync` للمجلد) أو fileblob بـ `NoTempDir` + `metadata=skip` مع قبول موثّق لخطر انقطاع الكهرباء. gocloud.dev/blob يبقى لـ S3 | fileblob بلا fsync (`fileblob.go`:836-871)؛ #3286، #3294، #3809 | عالية للمشكلة، متوسطة للحل (يحتاج قياس كلفة fsync) |
| S4 | **ترويسات `alt=media` في 2.3 إلزامية** لكل تنزيل، ومنها `CSP: default-src 'none'; style-src 'unsafe-inline'; sandbox` و**`CORP: cross-origin`** (استثناء من الترويسة العامة `same-origin`)؛ `Content-Type` دائمًا مضبوط قبل `ServeContent`؛ رفض `Service-Worker: script` | GitHub raw وgoogleusercontent (curl)؛ دليل Dworken؛ `server.go`:132-147؛ `fs.go`:222-295 | عالية لـ nosniff/sandbox/CORP، متوسطة لرفض SW (المواصفة لم تُقرأ هنا) |
| S5 | `Content-Disposition`: نحترم ما خزّنه العميل، والافتراضي **كما يسجّله الإنتاج** في S0؛ لا نعتمد عليه كحماية | المحاكي `attachment` افتراضيًا، العميل يتحكم بالحقل؛ الإنتاج [U] | متوسطة |
| S6 | حالات الاختبار الاثنتا عشرة في 2.4، منها مصفوفة متصفحات (Chromium، Firefox، WebKit) للـ HTML وSVG والفيديو وRange | — | عالية |
| S7 | **الرفع المتقطع:** staging على القرص المحلي لكلا الخلفيتين، fsync قبل إعلان الإزاحة، دلالة GCS للتداخل (يُتجاهل)، رفض الفجوات، حفظ الجزء المقطوع، finalize ذري ومكرر بالنتيجة نفسها، crc32c+md5 وحجم عند finalize، عمر 7 أيام قابل للضبط | GCS resumable docs؛ tus؛ task.ts (قطع 256 KiB…16 MiB)؛ S3 حد 5 MiB؛ المحاكي يتجاهل الإزاحة | عالية |
| S8 | **القواعد عند start وعند finalize** (تفويض start)، والرفض في أيهما رفض | المحاكي يقيّم عند finalize فقط؛ الرفض المبكر يوفر النقل | متوسطة (سلوك الإنتاج [U]) |
| S9 | **Go:** `MaxBytesReader` بالحد المتبقي، `mime/multipart.Reader` تدفقًا، مهلة قراءة/كتابة **متدرجة** عبر `ResponseController` لا صفرية، ولا `ParseMultipartForm` | `request.go`:1199-1209؛ CVE-2023-45290؛ `server.go`:196-200 | عالية |
| S10 | **الأسماء:** نص معتم بلا تطبيع ولا طي حالة، `text COLLATE "C"`، حدود GCS (1024 بايت، CR/LF، `.`/`..`، acme)، ورفض NUL بـ 400 | GCS objects؛ Postgres NUL؛ RTDB `COLLATE "C"` | عالية (عدا NUL: يحسمه الأوراكل) |
| S11 | **البيانات الوصفية:** 8 KiB للمخصصة، سقف للجسم قبل فك JSON، رفض CR/LF في الحقول التي تصير رؤوسًا | GCS quotas | عالية |
| S12 | **الحصص:** حجم أقصى للكائن، حصة بايتات لكل مشروع **بحجز عند start** تشمل staging، حد للجلسات المفتوحة، معدل `start`، حد أدنى لمساحة القرص الحرة | §4.3 | متوسطة للأرقام، عالية للآلية |
| S13 | **gzip:** لا `gunzip` في الذاكرة؛ v0.3 يخدم البايتات كما هي (فرق موثّق) أو فك تدفقي محدود | المحاكي `gunzipSync`؛ nodejs-storage يعتمد على `x-goog-stored-content-encoding` | متوسطة |
| S14 | **الجداول** (`storage_objects`، `storage_uploads`، `storage_blob_intents`، `storage_blob_gc`، استعمال الحصة) كلها بـ `project_number ... REFERENCES control.projects ON DELETE CASCADE`، وفي قائمة `scopecheck`، واختبارات العزل؛ وحذف المشروع يدفع ملفاته إلى `storage_blob_gc` قبل CASCADE | ADR-017 | عالية |
| S15 (ADR-023) | خارج v0.3: decompressive transcoding الكامل، أجزاء S3 تدفقية أثناء الرفع، نطاق تنزيل منفصل (إعداد اختياري لاحق)، حد «1/ث لكل كائن»، معالجة الصور. داخل v0.3: كل ما سبق | — | متوسطة |

### ما يخالف الخطة أو يدققها

1. **«RLS» في الخطة (القسم 4، S2):** ADR-017 أجّل RLS؛ الجداول بـ `project_number` و`scopecheck` واختبارات العزل (S14).
2. **«كتابة الملف أولًا ثم الصف» لا يكفي:** ينقصه fsync (fileblob لا يفعله)، وصف نية قبل الكتابة، وحذف القديم بعد الـ commit عبر outbox (S2، S3). و«مهمة تنظيف اليتامى» يجب أن تبلّغ لا أن تحذف، وبمهلة سماح.
3. **«gocloud.dev/blob (قرص افتراضيًا)»:** صحيح لـ S3، لكن fileblob بلا fsync، وكان فيه عبور مسارات حتى آذار 2026، و`IfNotExist` غير ذري اليوم (S3).
4. **«القطع في ملف مؤقت … ثم يُنقل الملف»:** صحيح، ويضاف أن staging محلي **حتى مع S3** لأن قطع المكتبة (256 KiB…) أصغر من حد S3 الأدنى (5 MiB)، وأن المحاكي ليس مرجعًا للإزاحات (يتجاهلها).
5. **ترويسات الحماية:** الخطة تذكر nosniff وCSP sandbox؛ يضاف **`CORP: cross-origin`** وإلا انكسر `<img src=downloadURL>` من أي تطبيق، لأن الترويسة العامة اليوم `same-origin`. و`Content-Disposition` لا يُعتمد عليه لأن العميل يتحكم به.
6. **«البيانات الوصفية في Postgres مع RLS، الملفات عبر gocloud»:** يضاف أن مفتاح الملف من `project_number` ومعرّف داخلي جديد لكل generation (الخطة تقول «معرّف داخلي»، وهذا يثبّته ويمنع الكتابة فوق ملف قارئ).
