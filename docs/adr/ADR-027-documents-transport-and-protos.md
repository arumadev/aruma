# ADR-027: قاعدة المستندات: النقل (connect على المنفذ الواحد) والـ protos

- الحالة: مسودة
- التاريخ: 2026-10-26
- المرحلة: F0 من الإصدار 0.4 (قاعدة المستندات، Document Database)
- **يحل محل:** جزء من ADR-010 S-8 (CORS) على مسارات بيانات قاعدة المستندات وحدها، إن تحقق شرط البند 5.2. وADR-010 لا يُعدَّل.
- المراجع: ADR-008 (المدخل الواحد)، ADR-010 (S-8، S-11، S-14)، ADR-014 (CORS ومفاتيح API لنقاط عميل Auth)، ADR-007 (مفاتيح API)، ADR-018 وADR-019 (توجيه RTDB قبل `httpx.Common` وحد جسمه)، ADR-022 §1 (مسارات Storage وحدها)، ADR-025 (الاستعلامات والمعاملات)، ADR-026 (البث وتدفق الكتابة)، ADR-028 (النطاق والتحقق)، ADR-029 (مصادقة Admin)، و:
  - تجربة F0 للنقل والمصادقة `.cache/briefs/research/spike-transport-auth.md` (**SP**، خارج git)، وكودها في الفرع `spike/documents-transport` تحت `test/spike/documents/transport/`، وتشغيل CI رقم 40347056578 (`documents-spike`: Go وNode تحت `test/ci/egress-guard.sh`، وAndroid على محاكي API 34، وiOS على محاكي macos-15، والثلاثة خضراء).
  - الخطة المعتمدة `.cache/briefs/plan-v04-firestore.html` (**PL**)، القسم 4، صفوف ADR-027.
  - أبحاث v0.4 في `.cache/briefs/research/`: `firestore-clients.md` (**CL**، ومتطلباته «MUST n»)، وتسجيلات F0 من الإنتاج في `test/oracle/documents/corpus/` وتحليلها `f0-production-recordings.md` (**PR**).
  - المستودع: `third_party/README.md` (قاعدة الدمج)، و`tools/protomerge/`، و`buf.yaml` و`buf.gen.yaml`، وهدفا `proto` و`proto-check` في `Makefile`، و`internal/server/server.go` (`Handler` و`Run`)، و`internal/platform/httpx/middleware.go` (`CommonWithLimit` و`StatusRecorder`)، و`internal/archtest/archtest_test.go`.

## السياق

مكتبات Firestore تتكلم ثلاثة نقول (CL):

- **gRPC على HTTP/2:** الجوال (Android وiOS وFlutter)، وAdmin Node وGo. وفيه Listen وWrite تدفقان ثنائيا الاتجاه يعيشان ساعات.
- **REST بـ JSON:** Admin Node بـ `preferRest`، وواجهة Admin لأداة firebase-tools.
- **WebChannel:** SDK الويب الكامل.

وAruma منفذ عام واحد (ADR-008) على `net/http`، يتكلم HTTP/1.1 وحده اليوم. مهلتا القراءة والكتابة فيه 60 ثانية (`server.go: Run`)، وكل طلب يمر بـ `httpx.Common`، وفيه حد جسم 1 MiB و`StatusRecorder`. وفي Go مرشحان يخدمان gRPC على خادم `net/http` قائم:

- `connectrpc.com/connect`؛
- `grpc.Server.ServeHTTP` من grpc-go.

أما `grpc.Server.Serve` فيحتاج مستمعًا خاصًا به، أي منفذًا ثانيًا. والخطة اشترطت تجربة يفوز فيها من يمر بعملاء Android وiOS وNode وGo بالتدفق ثنائي الاتجاه (PL §4).

والـ protos لها مصدران. googleapis فيها أحدث الحقول، لكن ينقصها `Write.verify` الذي ترسله مكتبات الويب والجوال في المعاملات، وKor فقده لأنه ولّد من googleapis وحدها (PL §4).

## القرار

### 1. connect، لا grpc-go

**خادم gRPC هو `connectrpc.com/connect` v1.21.1، مركّبًا على خادم `net/http` الخاص بنا.** والكود المولَّد رسائل `protoc-gen-go` ومعالجات `protoc-gen-connect-go` وحدها. ولم يعد grpc-go تبعية: حُذف `protoc-gen-go-grpc` من `buf.gen.yaml`، وحُذفت ملفات `*_grpc.pb.gen.go`، وخرج `google.golang.org/grpc` من `go.mod` (ومعه `golang.org/x/net` غير المباشر). و`go list -deps ./...` لا يحوي أي حزمة grpc.

#### 1.1 العملاء (SP، «Results per client»)

معيار النجاح ثلاثة أشياء:
- استدعاء unary؛
- بث Listen يبقى مفتوحًا حتى رسالة يرسلها الخادم بعد 65 ثانية من إضافة الهدف، والعميل يتحقق أن 60 ثانية على الأقل مضت؛
- مصافحة Write (رمز بث بلا نتائج) ثم كتابة واحدة مؤكَّدة.

وفي الجدول «c» تعني connect و«g» تعني grpc-go `ServeHTTP`.

| العميل (الإصدار) | h2c c | h2c g | TLS c | TLS g |
|---|---|---|---|---|
| Go `cloud.google.com/go/firestore` 1.29.0 (grpc-go 1.83.2) | نجح | نجح | نجح | نجح |
| Node `firebase-admin` 14.5.0 / `@google-cloud/firestore` 9.4.0 (grpc-js 1.14.6)، gRPC | نجح | نجح | نجح | نجح |
| Node بـ `preferRest: true` (REST على HTTP/1.1، وListen يبقى على gRPC) | نجح | نجح | نجح | نجح |
| Android `firebase-firestore` 27.0.1 (grpc-java-okhttp 1.84.0) | نجح | نجح | نجح | نجح |
| iOS `FirebaseFirestore` 12.19.3 (grpc-c++ 1.69.0) | نجح | نجح | فشل متوقع (البند 7) | فشل متوقع |

- **Go وNode:** 18 تنويعة، كلها كما يُتوقع، محليًا وعلى CI.
- **تفاصيل الحالة:** وصل `google.rpc.Help` (رابط الفهرس) في `grpc-status-details-bin`، فقرأه عميل grpc-go وgax (`statusDetails: [Help]`). والبايتات متطابقة في المكدّسين (`TestStatusDetails`).
- **حارس الشبكة:** لم يخرج أي اتصال عن الجهاز في أي تشغيل (SP، «Network guard»).

#### 1.2 المعايير

| المعيار | connect | grpc-go `ServeHTTP` | الدليل |
|---|---|---|---|
| الحجم | `connect` وprotobuf: 48 حزمة خارج المكتبة القياسية | grpc وx/net وx/sys وx/text وgenproto: 127 حزمة، ويرفع `cloud.google.com/go/compute/metadata` | `go list -deps` |
| HTTP/1.1 | يجيب gRPC unary والتدفق من الخادم على HTTP/1.1 (`200`، `grpc-status 0`)، ويخدم gRPC-Web وConnect. والتدفق ثنائي الاتجاه يحتاج HTTP/2 | `505`: HTTP/2 وحده | `TestHTTP1` |
| الملاءمة لـ `net/http` | مبني له | باب جانبي تجريبي في grpc-go | — |
| التحكم في التدفق والضغط المرتد | من HTTP/2 في `net/http`. حين تمتلئ نافذة العميل يتوقف `Send`: صفر رسائل من 1 MiB قُبلت لعميل لا يقرأ | النقل نفسه. قُبلت رسالة واحدة، لأن goroutine الكتابة فيه تأخذ واحدة | `TestBackpressure` |
| تجاوز مهلات الخادم | `RST_STREAM INTERNAL_ERROR` عند `WriteTimeout`، وسليم حين يرفع المسار المهلات | مثله | `TestStreamOutlivesServerTimeouts` |
| `http.Flusher` | تحتاجه التدفقات وحدها، والـ unary يعمل عبر `StatusRecorder` | يرفض كل استدعاء: `500 gRPC requires a ResponseWriter supporting http.Flusher` | `TestNeedsFlusher` |
| الإيقاف مع تدفقات طويلة | `Shutdown` وحده ينتظر حتى مهلته، وسياق التصريف ينهي التدفقات بـ `UNAVAILABLE` في 0 ثانية | مثله. و`GracefulStop` لا يفيد: `Drain` على نقل `ServeHTTP` مجرد `Close` | `TestShutdownDrainsStreams`؛ `internal/transport/handler_server.go:482` في grpc-go |
| keepalive (iOS وAndroid يرسلان ping كل 30 ثانية) | HTTP/2 في `net/http` يجيب ping ولا يفرض سياسة | مثله تحت `ServeHTTP` | كود `net/http` وgrpc-go |
| المعترضات (interceptors) | دالة للـ unary، وواجهة من 3 دوال للتدفق | دالة لكل منهما | `stacks.go` |

**ما تساوى فيه المكدّسان تساوى لأن grpc-go تحت `ServeHTTP` يجري هو أيضًا على HTTP/2 في `net/http`.** فالفرق الحجم، وHTTP/1.1، والملاءمة. **و`ServeHTTP` يبقى بديلًا جاهزًا:** المسار ورفع المهلات والتصريف كود واحد مع المكدّسين، والرجوع إليه سجل جديد يعيد المولّد.

### 2. التوجيه على المنفذ الواحد

#### 2.1 بنوع المحتوى قبل المسار

في `server.Handler()`، قبل `main` كما يُوجَّه Storage وRTDB:

- **طلب `content-type` فيه `application/grpc`، أو يبدأ بـ `application/grpc+` أو `application/grpc;`، يذهب إلى معالج gRPC لقاعدة المستندات.** لا خدمة أخرى تتكلم gRPC.
  - وطريقة لا نخدمها تُرد بصيغة gRPC (`UNIMPLEMENTED`)، لا بصفحة اللوحة ولا بـ JSON.
  - وإن كانت قاعدة المستندات معطّلة: `UNIMPLEMENTED` ونص يقول إنها غير مفعّلة على هذا الخادم. وإن كانت مفعّلة ونسخة أخرى تملك قفلها: `UNAVAILABLE` ونص يقول ذلك (ADR-024 §7).
- **المسارات التي يخدمها:** `/google.firestore.v1.Firestore/*` و`/google.firestore.admin.v1.FirestoreAdmin/*` و`/google.longrunning.Operations/*`، من معالجات `firestorepbconnect` و`adminpbconnect` و`longrunningpbconnect`.

#### 2.2 داخل الخدمة

- **المشروع والقاعدة** يُقرآن من رسالة الطلب (`database` أو `name` أو `parent`)، ومن `google-cloud-resource-prefix` حيث لا يحملهما الجسم.
  - ولا يُوجَّه بـ `x-goog-request-params`: يصل بثلاث صيغ وبتغطية مختلفة بين العملاء (SP، «What each client sends»)، كما يقول عقد العملاء.
  - والترويستان غائبتان عن استدعاءات Write منخفضة المستوى في Go وNode، فـ Write يأخذ قاعدته من أول رسالة.
- **ربط المشروع والقاعدة، قاعدة واحدة لكل طلب:** **كل اسم مورد في الطلب أو في رسالة البث يجب أن يُحلَّل إلى `(project, database)` نفسه، وهذا الزوج يجب أن يكون مما يحق للطالب** (مشروع حساب الخدمة، أو مشروع يملكه حساب رمز المالك، أو مشروع رمز ID، ADR-029 §3 القاعدة 6). والأسماء كلها:
  - `database` و`parent` و`name` في الطلب، و`google-cloud-resource-prefix`، ومسار REST؛
  - في Commit وBatchWrite وWrite: كل `writes[].update.name`، و`delete`، و`verify`، وو`transform.document`؛
  - **ولا يشمل الربط قيم المراجع** داخل الحقول والتحويلات والمرشحات والمؤشرات: الإنتاج يقبل مرجعًا إلى مشروع آخر قيمةً، ويخزّنه ويطابقه ويرتّبه (`values-order.json` `r-other-project`، و`values-equality.json`، و`values-bson.json`). فهي بيانات لا أسماء موارد؛
  - في BatchGetDocuments: كل `documents[]`؛
  - المعاملة: مشروعها وقاعدتها من معرّفها الموثَّق (ADR-025 §7.5)؛
  - في Listen: `database` في كل رسالة، و`parent` و`documents` في كل `add_target`؛
  - في Write: قاعدة البث من أول رسالة، وكل رسالة بعدها بالقاعدة نفسها.
- **يُتحقق من كلها قبل أي عمل:** قبل أي بحث أو قفل أو قراءة. والمخالفة `PERMISSION_DENIED`، أو نص الإنتاج إن سُجّل (ADR-028 §7.6، ما بقي؛ صف مؤقت `mixed-project-resource-names` في `parity-exceptions.json`). وهي استثناء من S-11 على هذا السطح، بحجة ADR-029 §3 القاعدة 6. وعلى Listen يُرد الهدف المخالف `REMOVE` مع `cause`، والبث باقٍ (ADR-026 §1.3).
- **اختبارات العزل** (ADR-028 §7.2): لكل RPC جسم يخلط مشروعين (القاعدة لمشروع الطالب والكتابة أو المستند لمشروع آخر)، وهدف Listen لمشروع آخر، ورسالة Write ثانية بقاعدة أخرى. كلها تُرفض ولا تمس شيئًا.
- **قاعدة غير موجودة** تُرد `NOT_FOUND` **بالنصوص الثلاثة المسجّلة** (`f0-missing-database.json`؛ PR §a)، بعد ربط المشروع:
  - **REST، سطح البيانات:** HTTP 404 والنص «The database {db} does not exist for project {p} Please visit https://console.cloud.google.com/datastore/setup?project={p} to add a Cloud Datastore or Cloud Firestore database. » (بلا نقطة بعد المشروع، **وبمسافة في آخره**؛ `:7`). ملفوفًا في مصفوفة على batchGet وrunQuery وrunAggregationQuery، وكائنًا على غيرها.
  - **gRPC:** الرمز 5 **برسالة فارغة** وبلا `grpc-status-details-bin` (`:285`-`:431`)، أيًّا كانت الترويسات. فنص MUST 27 المخمَّن لا يطابق الإنتاج على gRPC.
  - **واجهة Admin** (`GET /v1/projects/{p}/databases/{db}`، أول ما يرسله `firebase deploy`): HTTP 404 والنص «Project '{p}' or database '{db}' does not exist.» (`:263`).
  - **وعلى Listen وWrite يُغلق البث بها عند الفتح** بالرمز 5 والنص الفارغ، قبل أي رد (`:431`)، لأن البث كله لقاعدة واحدة (ADR-026 §1.3).

#### 2.3 اختبار ملكية المسارات

اختبار في `internal/server` يمنع أن تطالب خدمتان بالمسار نفسه:

- **كل خدمة تعلن مطالباتها:** البادئات، وأنماط `ServeMux`، وقاعدة المضيف (RTDB، ADR-018)، وقاعدة نوع المحتوى (gRPC).
- **ثابتًا:** لا بادئة لخدمة بادئة لبادئة خدمة أخرى، إلا استثناء معلنًا باسمه. الاستثناء الوحيد اليوم مسارات قاعدة المستندات تحت `/v1/projects/` (البند 5).
- **بالطلبات:** مدونة طلبات، واحد على الأقل لكل واجهة، ومنها:
  - gRPC إلى `/google.firestore.v1.Firestore/Listen`؛
  - WebChannel إلى `/google.firestore.v1.Firestore/Listen/channel` بنوع `application/x-www-form-urlencoded`؛
  - `/v1/projects/p/databases/(default)/documents/c/d`؛
  - `/v1/projects/p/rulesets`؛
  - `/.ws` و`/v0/b/…` و`/upload/storage/v1/…`.

  كل طلب يصل خدمة واحدة بالضبط، هي المتوقعة له.
- **خدمة جديدة لا تُركَّب دون صفها في الاختبار.**

### 3. التغييرات الأربعة في الإنتاج

التجربة أثبتت أن المسار يحتاجها مع أي من المكدّسين (SP، «Production wiring»).

#### 3.1 خارج `httpx.Common`

`StatusRecorder` في Common يخفي `http.Flusher`، وconnect يحتاجه للتدفقات. فلمسار gRPC وسيط خاص به، لا يغلّف الكاتب بشيء يخفي `Flush`:

- **ما يبقى من Common:** معرّف الطلب، وعنوان العميل عبر `trusted_proxies`، والتعافي من panic إلى `INTERNAL` بصيغة gRPC، وسطر سجل واحد لكل RPC عند نهايته برمزه، ومقاييس `documents_*`.
- **ما يسقط: `MaxBytesReader`.** حد Common (1 MiB) يعد جسم بث Write كله، لا رسالة واحدة.
- **حد الرسالة الواحدة** بدله: `connect.WithReadMaxBytes`، بقيمة من `documents.limits` افتراضيها 17 MiB. هذا ما يقبله الجوال (مستند 16 MiB وزيادته، MUST 19)، وحد grpc-go الافتراضي (4 MiB) كان سيرفضه. **وهو حد إطار وحده:** حد الإنتاج للطلب 10 MiB (OF §2a)، يفرضه الـ core على كل طلب وكل دفعة Write في كل سطح (gRPC وREST وWebChannel). ورمز الإنتاج ونصه فوقه لم يُسجَّلا، ويُسجَّلان في F2.
- **وبهذا يُطبَّق S-14 من ADR-010:** الحجم على وحدة البروتوكول، كما فعل RTDB (ADR-019) وStorage (ADR-022 §1)، والمهلات في البند 3.2.

#### 3.2 مهلتا الخادم: تُرفعان بعد أول رسالة موثَّقة، لا عند الفتح

`net/http` يطبق `ReadTimeout` و`WriteTimeout` (60 ثانية) على كل بث HTTP/2، فبدون رفعهما يُقطع كل Listen عند 60 ثانية. والقياس (`TestStreamOutlivesServerTimeouts`): بمهلات ثانية واحدة وصلت 3 رسائل من 5 ثم انقطع البث، وبرفعها وصلت الخمس في 2.5 ثانية. لكن رفعهما عند فتح البث، قبل قراءة أي رسالة، يترك بثًا بلا مصادقة ولا رسالة مفتوحًا بلا حد، وهو ما يمنعه S-14 («مهلات قراءة وخمول»). لذلك:

- **مهلة أول رسالة، 10 ثوانٍ** (`documents.limits.stream_first_message`، ADR-026 §3): عند بدء كل RPC تدفقي يُضبط موعد القراءة على الآن + 10 ثوانٍ بـ `http.NewResponseController(w).SetReadDeadline`. وما لم تُقرأ أول رسالة كاملة ويُتحقق من اعتمادها وربط مشروعها (البند 2.2) قبله، يُنهى الـ RPC.
- **بعدها وحدها تُرفع المهلتان:**
  - **التدفق من الخادم** (BatchGetDocuments، وRunQuery، وRunAggregationQuery): رسالته الوحيدة قُرئت، فتُستبدل مهلة الكتابة الثابتة بمهلتين:
    - **مهلة لكل رسالة مكتوبة، 30 ثانية** (`documents.limits.stream_write_stall`): تُضبط قبل كل كتابة بـ `SetWriteDeadline`، فعميل توقف عن القراءة يُقطع بعدها ولا يحجز البث.
    - **سقف كلي:** `grpc-timeout` إن أرسله العميل، وإلا 5 دقائق (`documents.limits.server_stream_max`)، وعنده `DEADLINE_EXCEEDED`. ومهلة الاستعلام والتجميع في ADR-025 تبقى داخلها.
    - وتُحسب هذه الـ RPCs في `streams_per_ip` ما دامت مفتوحة.
  - **Listen وWrite:** تُرفع المهلتان معًا (`time.Time{}`).
- **ما يحد Listen وWrite المفتوحين:**
  - `exp` الرمز، فيُغلق البث بـ `UNAUTHENTICATED` (ADR-026 §4، ADR-029)؛
  - **خمول بلا هدف** (Listen بلا هدف، أو Write بلا طلب): 5 دقائق (`stream_idle`، ADR-026 §3)، وهو وحده مع الحدين التاليين ما يحد بث المجهول؛
  - `streams_per_project` و`streams_per_ip` الموجودان (ADR-026 §3)؛
  - التصريف (البند 3.4)، وفقد قفل النسخة (ADR-024 §7)، وإلغاء العميل، و`grpc-timeout` إن أرسله.
- **وحد البث المتزامن على الاتصال الواحد** يُضبط صراحة في `http.Server.HTTP2.MaxConcurrentStreams` (الافتراضي في Go اليوم 250)، فلا يفتح اتصال واحد تدفقات بلا حد قبل أن تعدها الحدود أعلاه. وقيمته تُراجع في F2 مع نوافذ HTTP/2 (البند 3.3).
- **الـ unary يبقى تحت مهلتي الخادم.**

#### 3.3 h2c على المنفذ العام

- **`http.Server.Protocols`** فيه HTTP/1 وHTTP/2 و`SetUnencryptedHTTP2(true)`، وهو HTTP/2 بمعرفة مسبقة (prior knowledge)، الشكل الذي يستعمله كل عميل gRPC. ولا ترقية `Upgrade: h2c`. وHTTP/1.1 يبقى كما هو لكل الخدمات، ومنها WebSocket في RTDB.
- **الوكيل أمام Aruma** ينهي TLS ويتكلم h2c إلى Aruma: في Caddy `transport http { versions h2c 2 }`.
  - **وكيل ينزل إلى HTTP/1.1 يكسر Listen وWrite** مع أي من المكدّسين.
  - والـ unary والتدفق من الخادم يعملان معه مع connect.
- **نوافذ الاستقبال في HTTP/2** (لكل بث ولكل اتصال) تحد سرعة رفع Commit وWrite الكبيرة. تُضبط بـ `http.Server.HTTP2` إن أظهرت اختبارات الحدود في F2 ذلك (SP، الخطر 3).

#### 3.4 التصريف على مستوى التطبيق

`http.Server.Shutdown` ينتظر التدفقات المفتوحة حتى مهلته، فيصير Listen الذي يعيش ساعات إيقافًا معلقًا 15 ثانية في كل نشر. لذلك:

- **قاعدة المستندات تنفذ `Shutdown(ctx)`** كما ينفذه RTDB، ويستدعيه `Run` قبل `http.Server.Shutdown` داخل الـ 15 ثانية نفسها:
  - البث الجديد يُرد `UNAVAILABLE`؛
  - يُلغى سياق تصريف ينهي كل Listen وWrite مفتوح بـ `UNAVAILABLE`؛
  - والـ unary الجاري ينتهي طبيعيًا تحت `Shutdown`.
- **`UNAVAILABLE` لأن كل العملاء يعيدون الاتصال عليه،** ويستأنف Listen من `resume_token` (ADR-026). والقياس (`TestShutdownDrainsStreams`): التدفقات انتهت في 0 ثانية.
- **هذا يقصّر التسليم بين نسختين أيضًا،** فالجديدة تنتظر القفل الاستشاري حتى 30 ثانية (ADR-024 §7؛ ADR-026 §5).

### 4. keepalive والتدفقات الخاملة

- **لا سياسة ping في الخادم، ولا ping منه في v0.4.0.** iOS وAndroid يرسلان ping كل 30 ثانية، و`net/http` يجيبه ولا يفرض سياسة. أما `Serve` في grpc-go فكان سيرسل GOAWAY `too_many_pings`، وهي حالة hidane عند 90 ثانية (استنتاج من الكود، لم يُقس).
- **خلف وكيل:** الوكيل أو موازن الحمل قد يغلق تدفق HTTP/2 خاملًا أو يفرض سياسة ping خاصة به. والتجربة أبقت التدفقات 65 ثانية على `net/http` مباشرة. **قبل إغلاق F2 يُختبر إعداد Caddy الموصى به بتدفق يبقى 95 ثانية** (`SPIKE_LATER=95s`، مع رفع مهلة الانتظار في العملاء)، والنتيجة في وثيقة النشر (SP، الخطر 2).

### 5. REST تحت `/v1/`، وWebChannel، وسطح المتصفح

#### 5.1 المسارات

- **REST** بمسارات الإنتاج نفسها:
  - البيانات: `/v1/projects/{p}/databases/{d}/documents…`، أي GET وPATCH وDELETE على المستند، و`:commit` و`:batchGet` و`:runQuery` و`:runAggregationQuery` و`:beginTransaction` و`:rollback` و`:listCollectionIds` و`:batchWrite` و`:partitionQuery`؛
  - واجهة Admin: `/v1/projects/{p}/databases`، و`…/collectionGroups/{cg}/indexes|fields`، و`…/operations` (ADR-025 §4.5).
- **البادئة `/v1/projects/` مأخوذة:** Management API مركّب عليها (firebaserules وService Usage وdefaultBucket). فقاعدة المستندات **تطالب بـ `/v1/projects/{p}/databases` وما تحته أولًا**، كاستثناء معلن في اختبار البند 2.3، وما سواه يبقى لـ Management API.
- **التنفيذ:** معالجات مكتوبة يدويًا، تحوّل JSON بـ `protojson` إلى رسائل الطلب نفسها وتستدعي الـ core نفسه الذي يستدعيه gRPC. connect لا يحوّل REST، وكل مكتبة transcoding تضيف تبعية أو عملية.
  - حد الجسم من `documents.limits`، كما فعل RTDB وStorage.
  - الأخطاء بصيغة Google في JSON: `{"error":{code, message, status[, details]}}`. وفي تسجيلات F0 خلت أخطاء REST في الإنتاج من `details`، إلا «Index entry is too large.» ففيه `google.rpc.ErrorInfo{domain: "firestore.googleapis.com", reason: "INDEX_ENTRY_TOO_LARGE"}` (`f0-errors.json:473`). فـ `details` تُرسل حيث سُجّلت وحدها، و`details` خطأ الفهرس الناقص على REST (رابط `google.rpc.Help` على gRPC، البند 1.1) يُسجَّل في F3.
- **Listen وWrite لا REST لهما.** و`preferRest` في Node يبقي Listen على gRPC (التجربة).
- **WebChannel** على `/google.firestore.v1.Firestore/{Listen,Write}/channel`، بنوع محتوى ليس gRPC، فيُوجَّه بالمسار. يقوم على core Listen وWrite نفسه، ومساره محجوز في اختبار البند 2.3 من F0. **قرارات سطحه في البنود 5.2 إلى 5.4 من الآن**، وما يبقى لـ F6 بروتوكوله وحده: مصافحة VER=8 و`gsessionid`، وتقطيع القناة الخلفية بطول UTF-16، وردود POST الأمامية، و`noop`، والاستطلاع الطويل (MUST 21).

#### 5.2 CORS: على نمط ADR-014

الويب يكلّم REST وWebChannel من أصل تطبيق المطوّر، فهي نقاط عميل كنقاط Auth (ADR-014)، لا نقاط إدارة.

- **مفتوح** على مسارات البيانات وحدها: `/v1/projects/{p}/databases/{d}/documents` وما تحته (ومنه `:commit` و`:runQuery` وأخواتها)، و`/google.firestore.v1.Firestore/{Listen,Write}/channel`.
- **مغلق كما في S-8** على كل ما سواها: واجهة Admin (`/v1/projects/{p}/databases` وget القاعدة، و`…/collectionGroups/…`، و`…/operations…`)، وطرق gRPC (لا يطلبها متصفح)، وManagement API واللوحة. واختبار يثبت الاثنين: مسار بيانات يجيب preflight، ومسار Admin لقاعدة المستندات يبقى في قائمة `TestSecurityHeadersAndNoCORS` بلا أي ترويسة.
- **الـ preflight** (`OPTIONS`) على المسارات المفتوحة، كما سجّله الإنتاج (`f6-cors.json`): 200 وجسم فارغ `text/html`، و`Access-Control-Allow-Headers` صدى للترويسات المطلوبة (بلا `Access-Control-Request-Headers` لا ترويسة)، و`Access-Control-Allow-Origin` أصل الطلب مع `Allow-Credentials: true`.
  - **مسارات البيانات:** `Allow-Methods: DELETE,GET,HEAD,OPTIONS,PATCH,POST,PUT` و`Max-Age: 3600`.
  - **القناة:** `GET,POST` بلا `Max-Age`، وترويسة العرض في المصافحة `x-client-wire-protocol,x-http-session-id`.
  - **`OPTIONS` بلا `Origin`:** 400 فارغ.
  - **الفرق الوحيد:** الإنتاج يرد 403 بصفحة HTML على ترويسة لا يعرفها (`x-made-up-header`) من قائمة غير منشورة، ونحن نردد كل ما طُلب (استثناء `cors-unknown-request-header`).
- **الهوية في الرمز أو المفتاح، لا في الأصل ولا في cookie:** `Authorization`، أو `headers=` في أول جسم WebChannel، أو مفتاح API. وهذه المسارات لا تقرأ cookie أبدًا.
- **الأصل والاعتماد، وقد حسمه تسجيل الإنتاج `f6-cors.json` (و`f6-webchannel-*.json`):**
  - **مسارات البيانات والقناة تجيب معتمدة:** `Access-Control-Allow-Origin` هو `Origin` الطلب نفسه (ومنه `null` على REST)، مع `Access-Control-Allow-Credentials: true` و`Vary`، و**لا `*` أبدًا**. وسببه أن SDK الويب الكامل يرسل طلبات القناة معتمدة (`credentials: include`، `withCredentials`، `webchannel_connection.ts:129`)، والمتصفح يرفض `*` لطلب معتمد. ونحن نفعل مثله على المسارين، بلا مفتاح تبديل.
  - **لا يُقرأ cookie:** الهوية في `Authorization` أو جسم القناة أو المفتاح وحدها، فالاعتماد لا يمنح صفحة أجنبية ما لا يقدر عليه سكربت في مكان آخر (الحجة نفسها في تعليق `rtdb.allowed_origins`، `config.go:92-95`).
  - **مسارات Admin مغلقة** بلا أي ترويسة CORS، كما في S-8.
  - وهذا وحده الجزء الذي يحل محله هذا السجل من S-8 (السطر «يحل محل» أعلاه)، وما سواه يبقى كما هو.

#### 5.3 مفاتيح API وApp Check

- **مفتاح API كما يعامله Auth** (ADR-014، «المفاتيح ومشروع الطلب»): يصل في `?key=` أو `x-goog-api-key` (REST، وأول جسم WebChannel)، ولا يرسله Android ولا iOS ولا Admin (CL §3).
  - **غائب:** يُقبل، كما في الإنتاج لعملاء الجوال.
  - **حاضر:** يُحل بـ `ProjectForKey` (`tenancy/core/dataplane.go`)، والمجهول أو المعطّل 400 «API key not valid. Please pass a valid API key.» كما في Auth. ومشروعه يدخل ربط البند 2.2، ثم تُطبَّق قيوده بـ `Restrictions.Check` بالواجهة `firestore.googleapis.com`، والرفض بجواب Google في ADR-014 (403 `PERMISSION_DENIED` و`ErrorInfo`).
  - ونص الإنتاج لمفتاح غير صالح على Firestore يُسجَّل في F1، ويُطابَق إن اختلف.
- **`x-firebase-appcheck` يُقبل ويُتجاهل،** كما قرر ADR-023 لـ Storage، ويُوثَّق: Aruma لا تفرض App Check.

#### 5.4 أجسام المتصفح

- **REST يقبل `Content-Type: text/plain` بجسم JSON** (ما يرسله SDK الويب ليتجنب preflight)، كـ `application/json` (MUST 20).
- **WebChannel يقبل `application/x-www-form-urlencoded`** بحقول `count` و`ofs` و`reqN___data__` و`headers=` (MUST 21)، وحد الجسم من `documents.limits`.

#### 5.5 WebChannel كما نُفّذ في F6 (`webchannel/`)

المصادر: `webchannel_connection.ts` في firebase-js-sdk، وكود `goog.labs.net.webChannel` المنشور في closure-library (WebChannelBase وChannelRequest وWireV8)، ثم تسجيل الإنتاج `f6-webchannel-{listen,longpoll,write,errors}.json` و`f6-cors.json` حيث وُجد. وما لم يأتِ من أحدها **تخمين** معلَّم أدناه، يحسمه تمرير التطابق.

- **البث نفسه بث gRPC:** الجلسة تشغّل Listen بـ `grpc.Handler.ServeListen` خلف `grpc.ListenConn` و`grpc.Deadlines`، فالحدود (`streams_per_ip` و`streams_per_project` و`stream_outbox_bytes` و`stream_first_message` و`stream_idle`) والرمز وانتهاؤه وإلغاؤه والقواعد وإعادة الحكم وفقد القفل والتصريف كلها كما في gRPC (ADR-026 §1-§5). وWrite بـ `ServeWrite` على الطريقة نفسها، وبثه على القناة مسجَّل (`f6-webchannel-write.json`).
- **المصافحة:** POST بـ `VER=8` و`RID` و`database` و`X-HTTP-Session-Id=gsessionid`، وجسمها `headers=` (أسطر `Name:value\r\n`) ثم `count` و`ofs` و`reqN___data__`. الرد `[[0,["c",SID,"",8,15,30000]]]` وترويسة `X-HTTP-Session-Id` (**مسجَّل**، `f6-webchannel-listen.json`: وطول الجزء 51 وحدة، تُعدّ فيها نهاية السطر الأخيرة). SID من 16 بايتًا عشوائيًا وgsessionid من 40 (طولا الإنتاج). الهوية من `Authorization` في `headers=`، ولا يُفحص عند المصافحة (كالإنتاج)، فرفضها يصل على القناة الخلفية حالةً للبث.
- **التقطيع:** `<طول بوحدات UTF-16>\n<نص>`، والمصفوفات `[[id,payload],…]` من 0. والرسالة protojson بإزاحة مسافتين و`\n]` في آخرها، وردّا المصافحة والـ POST بسطر جديد في آخرهما، وسجلات القناة الخلفية بلا سطر (**مسجَّل**، `f6-webchannel-listen.json`). وتجميع الرسائل في السجلات بحسب ما يُنتج لا بحسب الإنتاج: **تخمين**.
- **حالة البث** تُرسل كما أنهى الإنتاج بث Write مرفوضًا بالقواعد: `{"__sm__":{"status":[[{"error":{code,message,status}}]]}}` ثم `["close"]` في سجل واحد، والـ code رمز HTTP (**مسجَّل**، `f6-webchannel-errors.json` `write.denied`؛ وأول رد Write فيه `streamId` هو `"0"` و`streamToken`، **مسجَّل** في `f6-webchannel-write.json`). أما إنهاء بث **Listen** بخطأ بالصيغة نفسها فلم يُسجَّل: **تخمين**. والرفض عند المصافحة (حدود الجلسات) يأتي في رد المصافحة نفسه بعد `"c"` ولا تُحفظ الجلسة: **تخمين**، ونصوصه نصوصنا.
- **الـ POST الأمامي:** يُرد `[حضور قناة خلفية, آخر id أُرسل, البايتات غير المؤكدة]`. والثالث 7 ثابتًا في كل حالة سُجلت، متدفقة أو استطلاعًا، ونحن نعيد 7 مثله (**مسجَّل**، `f6-webchannel-listen.json` و`f6-webchannel-longpoll.json` و`f6-webchannel-write.json`). والخرائط المكررة (إعادة المحاولة بالـ RID نفسه) لا تُؤخذ مرتين. ولا نرسل `x-client-wire-protocol` (الإنتاج يرسل `h2`): **فرق مقصود** (استثناء `webchannel-wire-protocol`). وغيابه لا يُبقي العميل على طلب أمامي واحد: مجمّع الطلبات (`ForwardChannelRequestPool`) يفتح حتى 10 طلبات معًا متى كانت الصفحة نفسها على h2 أو h3 (`nextHopProtocol` للصفحة)، والترويسة لا تفعل إلا أن ترفعه إلى ذلك. فقد تصل الخرائط بغير ترتيبها، ومفاتيحها `reqN` قد لا تتصل بعد إعادة محاولة (`WireV8.encodeMessageQueue`). فرقم الخريطة `ofs + N` من مفتاحها، وما وصل قبل خريطة أدنى منه يُحفظ حتى تصل، ثم تُسلَّم الخرائط المتصلة من بعد آخر خريطة أُخذت بترتيبها، والمحفوظة تُحسب من `message_bytes`، وما أُخذ من قبل لا يُؤخذ ثانية. ورد الـ POST واحد أُخذت خرائطه أم حُفظت، لأن العميل لا يقرأ فيه إلا حال القناة الخلفية (`handlePostResponse_`). وأجسام POST الجلسة الواحدة تُقرأ واحدًا بعد واحد، فلا يحمل المنتظرون منها أكثر من جسم. والترتيب والحفظ من عندنا لا من تسجيل.
- **القناة الخلفية:** كل ما بعد `AID` العميل يُعاد إرساله، وما أكّده يُحذف. `CI=0` تبث، و`noop` بعد 30 ثانية صمت (**مسجَّل**، وهو `30000` في المصافحة)، وتُنهى بعد 48 ثانية (**مسجَّل**: 48.5 في تشغيلين ونحو 52 في فحص) أو بعد أن تحمل ربع `stream_outbox_bytes` كي يؤكد العميل ما حملت (الثاني **تخمين**). و`CI=1` ترد بما عندها حالما يوجد، أو بعد 29 ثانية (**مسجَّل**: استطلاع بلا شيء انتهى بعد 29.5 ثانية بـ`noop` اثنين) أو `TO` العميل إن قصر. وقناة جديدة لا شيء معلّق لها تبدأ بـ `noop` إلا أول قناة بث في الجلسة، والاستطلاع الطويل يبدأ به دائمًا (**مسجَّل**، `f6-webchannel-longpoll.json`). وما يخالف الإنتاج في تجميع الاستطلاع استثناء `webchannel-long-poll-batching`. وترقيم الـ noop ألا يسبق مصفوفة لم تُكتب بعد: من عندنا.
- **حدود الجلسة** (لأن فتح جلسة يكلف المهاجم طلبًا واحدًا): لكل عنوان `streams_per_ip`، و100000 في الكل؛ أما `streams_per_project` فتُحسب به الجلسة حين يقبل بثُّها صاحبه كما يُحسب بث gRPC، لا بالمشروع الذي تسميه المصافحة قبل أي تحقق؛ والجلسة التي انتهى بثها تُسقط طلباتها غير المقروءة ولا تبقي إلا مصفوفات الإغلاق ليقرأها العميل، مدة الانتهاء وعشر ثوانٍ في الأكثر من نهاية البث لا يمددها طلب، والـ POST عليها يُقرأ جسمه ويُرمى؛ والمصفوفات غير المؤكدة حتى `stream_outbox_bytes` ثم ينتظر الإرسال ويحكمه حد البث كما في gRPC؛ والطلبات غير المقروءة حتى `message_bytes`، وينتظر الـ POST حتى `stream_write_stall` ثم 503؛ والجلسة بلا قناة خلفية ولا طلب 60 ثانية تنتهي. كلها **تخمين** في قيمها، والقيمة الكلية والانتهاء ثابتان في الكود لا في الإعداد.
- **SID مجهول:** 400 وصفحة HTML فيها «Your client has issued a malformed or illegal request. Unknown SID»، على القناتين وعلى `TYPE=terminate` (**مسجَّل**؛ ونص الصفحة سواها نصنا، استثناء `webchannel-unknown-sid-page`). و`terminate` الصحيح 200 فارغ، بـ POST أو GET. أما الطلب بلا `VER` فـ 200 فارغ كالإنتاج (**مسجَّل**) لكن بلا `set-cookie` لأن السطح لا يقرأ cookie (استثناء `webchannel-probe-cookie`)، وجسم لا يُقرأ 400 JSON.
- **مفتاح API (البند 5.3)** يُتحقق منه عند المصافحة من `headers=` أو `?key=`، والإنتاج **لا يتحقق منه** على القناة ولا على REST (`f6-webchannel-errors.json`). ويبقى سلوكنا قيدًا مقصودًا (استثناء `webchannel-api-key-validated`).
- **CORS (البند 5.2):** **مسجَّل** (`f6-cors.json`): أصل الطلب مع `Allow-Credentials: true` على القناة وREST، لا `*` ولا cookie، ومسارات Admin مغلقة. وليس في `rest/cors.go` مفتاح تبديل. والفرق الباقي في الـ preflight وحده، ومحلّه البند 5.2 (استثناء `cors-unknown-request-header`).
- **ما لم يُسجَّل** ويبقى **تخمينًا**: انتهاء الجلسة بلا قناة بعد 60 ثانية، وحدود الجلسات وقيمها، والمئة ألف في الكل، وربع `stream_outbox_bytes`، وإنهاء Listen بـ `__sm__`، وتجميع الرسائل في السجلات، ونصوص الرفض عند المصافحة. والاستثناءات المعلنة لـ F6 في `parity-exceptions.json`: `webchannel-api-key-validated` و`webchannel-probe-cookie` و`webchannel-wire-protocol` و`webchannel-unknown-sid-page` و`webchannel-long-poll-batching` و`cors-unknown-request-header` (ومعها `rest-json-compact` لـ REST).

### 6. الـ protos

- **googleapis الأساس:** فيها أحدث الحقول (`concurrency_mode`، و`request_options` على 15 طلبًا، و`field_behavior`، وprotos الـ pipeline)، ومنها تُولَّد مكتبتا Google في Go وNode. مثبّتة عند commit `728c111a933ef4c7fc4cbc35894c559aa9eb2615`.
- **firebase-js-sdk** عند الوسم `@firebase/firestore@4.19.0` (commit `cc7ead49187d024958a4a3bcee87436dc03be12c`). يقرؤه فحص الدمج وحده ولا يُترجم.
- **قاعدة الدمج** بنصها في `third_party/README.md`:
  1. حقل أو قيمة enum في النسختين: الرقم والنوع نفسهما.
  2. حقل في نسخة js-sdk وحدها يُضاف إلى googleapis **برقمه نفسه**، بتعليق يبدأ `// Aruma: from firebase-js-sdk`.
  3. إن أعطت googleapis الرقم لحقل آخر أو حجزته، لا يُضاف شيء ويفشل الفحص.
  4. رسالة أو enum أو ملف في js-sdk وحدها: يفشل الفحص ويسمّيه.
  5. الحقول المجهولة لا تُسقط أبدًا: protobuf-go يحفظها افتراضيًا، والخادم لا يرميها في قراءة ثم كتابة (ADR-028 §6).
- **اليوم تضيف القاعدة حقلًا واحدًا:** `google.firestore.v1.Write.verify = 5` في الـ oneof `operation` (`write.proto`). وjs-sdk يبني نسخته بالطريقة نفسها، بتطبيق `verify.patch` على googleapis.
  - ولا تصادم: `reserved 8` في `ExecutePipelineRequest` عند js-sdk لم يعد في googleapis.
  - وما سوى ذلك فروق في التعليقات، أو googleapis أحدث.
- **تعارض المستقبل** لا يُحسم في الكود: يفشل الفحص، ويُكتب سجل جديد قبل نقل الـ protos (هذا السجل لا يُعدَّل بعد اعتماده).
- **`tools/protomerge`** يفحص القاعدة: أرقام الحقول وقيم enum، والأنواع، والنطاقات المحجوزة، والتصادمات. يشغّله `make proto` أولًا، واختباره يجري مع `go test ./...`.
- **التوليد:**
  - buf v1.73.0، وprotoc-gen-go v1.36.12، وprotoc-gen-connect-go v1.21.1، كلها عبر `go run` بإصدارات مثبّتة، فلا شيء يُثبَّت ولا شيء يُجلب من Buf Schema Registry.
  - المخرج يُسمّى `*.gen.go` كمخرج sqlc، في `internal/services/documents/wire/pb/{firestorepb,adminpb,typepb,longrunningpb}`، ومعالجات connect في `*pbconnect/`. والحجم بعد حذف stubs grpc-go نحو 22 ألف سطر (944 KB).
  - `google.api` و`google.rpc` لا تُولَّد، بل تؤخذ من genproto، لأن تفاصيل الأخطاء (`google.rpc.Help`) ومكتبات Google تربطها أيضًا، وملف proto يُسجَّل في البرنامج مرة واحدة.
- **التسجيل مرة واحدة:** برنامج Aruma يسجّل `google/firestore/v1/*.proto` من `wire/pb`. فلا اختبار في وحدة Aruma يربط `cloud.google.com/go/firestore`، لأن `firestorepb` الخاص به سيتصادم عند init. **عملاء Go في الاختبارات في وحدات خاصة بهم.**
- **archtest:** `internal/services/documents/wire/pb` لا يستورد شيئًا داخليًا.
- **CI:** `make proto-check` في وظيفة Go بعد `sqlc-check`. يولّد في مجلد مؤقت ويفشل بـ `diff -r` إن تقادم المولَّد.

### 7. TLS: iOS يحتاج شهادة من جهة عامة

- **iOS لا يثق إلا بـ `roots.pem` المبني فيه.** gRPC في iOS (grpc-c++ 1.69.0 داخل FirebaseFirestore 12.19.3) لا يقرأ مخزن ثقة النظام، ولا تتيح الواجهة العامة إضافة جهة. فشهادة الجهة الخاصة في التجربة أفشلت كل استدعاء: `UNAVAILABLE: Ssl handshake failed (TSI_PROTOCOL_FAILURE)`.
- **العملاء الآخرون أمام جهة خاصة:**
  - Android يثق بها عبر `network_security_config` في التطبيق؛
  - grpc-js في Node يتجاهل `NODE_EXTRA_CA_CERTS` ويحتاج `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH`، وهذا سطر في الوثائق؛
  - Go يأخذ مخزن النظام.
- **الإنتاج:** شهادة من جهة عامة (مثل Let's Encrypt) على وكيل TLS أمام Aruma، والوكيل يكلّم Aruma بـ h2c (البند 3.3). **شهادة موقّعة ذاتيًا أو من جهة خاصة تعني أن iOS لا يتصل،** والوثائق تقول ذلك صراحة.
- **CI:** اختبار iOS على TLS يحتاج اسم مضيف عامًا بشهادة عامة (Let's Encrypt بـ DNS-01، شرط F0 في الخطة). وحتى يوجد، صفا TLS لـ iOS فشل متوقع في القائمة، وiOS يُختبر على h2c.
- **المحلي والتطوير:** h2c يعمل مع العملاء الأربعة (`ssl=false`):
  - iOS: `settings.host` مع `isSSLEnabled = false`؛
  - Android: `setHost` مع `setSslEnabled(false)`؛
  - Node: `ssl: false`؛
  - Go: `FIRESTORE_EMULATOR_HOST`.

  وما يرسله كل منها من اعتماد على اتصال غير مشفّر، وما يُقبل منه، في ADR-029.

## البدائل المرفوضة

- **grpc-go عبر `ServeHTTP`:** 127 حزمة مقابل 48، و`505` على HTTP/1.1، وباب جانبي تجريبي، ويرفض كل استدعاء دون `http.Flusher`. يبقى بديلًا جاهزًا (البند 1.2).
- **`grpc.Server.Serve` على منفذ ثانٍ:** يخالف المدخل الواحد (ADR-008)، ويضاعف إعداد TLS والوكيل. وسياسة ping فيه تصطدم بـ ping الجوال كل 30 ثانية (استنتاج).
- **تقسيم الاتصال على المنفذ نفسه بشم أول البايتات** (نمط cmux): يخرج gRPC عن `net/http` ووسطائه، ويعيد سياسة ping السابقة. لم يُقس.
- **إبقاء stubs المكدّسين:** كود ميت، وgrpc في `go.mod` بلا مستعمل.
- **REST عبر grpc-gateway أو Envoy:** تبعية أو عملية إضافية لما يكفيه `protojson` ومعالج لكل طريقة.
- **التوليد من googleapis وحدها:** يُسقط `Write.verify` كما في Kor. **ومن js-sdk وحدها:** تفوته أحدث الحقول التي ترسلها مكتبات Google.
- **جلب الـ protos من Buf Schema Registry:** شبكة عند البناء، ونسخة غير مثبّتة بجانب الكود.

## العواقب

- **`go.mod` بلا grpc.** وأي كود في الوحدة يستورده لاحقًا يحتاج سجلًا جديدًا.
- **المنفذ العام يتكلم h2c مع كل الخدمات،** لا مع قاعدة المستندات وحدها. وسلوك المسارات الأخرى لا يتغير: مهلاتها وحدودها كما هي. و`MaxConcurrentStreams` يُضبط صراحة (البند 3.2)، ونوافذ HTTP/2 تُراجع في F2.
- **CORS مفتوح على مسارات بيانات قاعدة المستندات وحدها** (البند 5.2)، وصف ADR-010 في README السجلات يشير إلى ذلك.
- **وثيقة النشر:** وكيل يتكلم h2c إلى Aruma (مع مقطع Caddy)، وشهادة من جهة عامة لـ iOS، و`GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` لـ Node مع جهة خاصة.
- **اختبار ملكية المسارات** يفشل أي خدمة جديدة تطالب بمسار مأخوذ.
- **تُحذف التجربة:** تبقى في فرعها، ولا يبقى منها في هذا الفرع إلا الـ protos والتوليد وقاعدة archtest وخطوة CI.
- **تحديث الـ protos** بخطوات `third_party/README.md`، والتعارض الجديد سجل جديد.
- **مخاطر تُحمل إلى المراحل:**
  - الخمول 95 ثانية خلف الوكيل (قبل إغلاق F2)؛
  - نوافذ HTTP/2 (F2)؛
  - connect أمام grpc-c++ وgrpc-java في الحالات الحدّية: إعادة المحاولة، وأطراف `grpc-timeout`، والأخطاء trailers-only. لم تمر بها التجربة، وتغطيها اختبارات العقد ومجموعتا Android وiOS في F8 (ADR-028 §7.2)؛
  - نتيجة اختبار CORS في المتصفح (F1، البند 5.2).
