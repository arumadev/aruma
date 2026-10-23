# المسار C: أنظمة الاستعلامات التفاعلية (reactive queries) ودروسها لتصميم Listen

- **التاريخ:** 2026-10-23. بحث قراءة فقط: لم يُشغَّل أي خادم ولم يُستنسخ مستودع كبير. الكود قُرئ عبر `gh api` عند الـ commit المذكور، والمقالات عبر WebFetch.
- **الرموز:** **[V]** متحقَّق منه في المصدر المذكور (رابط، أو `repo/path:line @sha`). **[I]** استنتاجي. **[U]** لم أتحقق منه.
- **الغرض:** يغذّي ADR-026 (البث اللحظي). السؤال المحوري: حين يُكتب مستند، كيف نعرف أي استعلامات مفتوحة تأثرت، وكيف نحافظ على نوافذ `limit`/`orderBy`، وكيف نستأنف بعد انقطاع، وماذا نفعل بالمستهلك البطيء، وما الذي انكسر عند غيرنا في الإنتاج.
- **تذكير بقيودنا:** Aruma لا يستعمل Postgres RLS (ADR-017 أجّله). العزل بعمود `project_number bigint REFERENCES control.projects` في كل جدول خاص بمشروع، مع `internal/store/scopecheck` واختبارات عزل. وفي RTDB: كاتب واحد لكل قاعدة، و`seq` في المعاملة نفسها، وتوزيع داخل العملية (ADR-019).

## الـ commits المقروءة

| المستودع | الـ commit |
|---|---|
| `instantdb/instant` | `b97a1164fdc5` |
| `supabase/realtime` | `f7bf61fc49b4` |
| `electric-sql/electric` | `3f9809fb0bd8` |
| `rocicorp/mono` (Zero) | `fdd783d29161` |
| `rethinkdb/rethinkdb` | الفرع `v2.4.x` |
| `meteor/meteor` | `4c94e92457c0` |
| `firebase/firebase-js-sdk` | `3a5d6a04eb68` |

---

## 0. Firestore نفسه: ما تقوله Google عن Listen

### 0.1 البنية الموثّقة
من صفحة «Understand real-time queries at scale» (https://firebase.google.com/docs/firestore/real-time_queries_at_scale، آخر تحديث 2026-10-21) **[V]**:

1. **polling query أولًا:** حين يسجّل العميل مستمعًا، «The Cloud Firestore frontend queries the underlying storage system to bootstrap the dataset. It loads the entire result set of matching documents. We refer to this as a polling query». ثم تُقيَّم Security Rules، ثم ينتقل الاستعلام إلى **listen mode** ويُسجَّل عند «subscription handler».
2. **changelog في المعاملة نفسها:** «Transactionally, the system commits the same update to an internal changelog. The changelog establishes a strict ordering of changes as they happen».
3. **reverse query matcher:** «Instead of searching through documents to find those that match a query, it efficiently searches through queries to find those that match an incoming document». وبعد المطابقة: «Then the system evaluates the database's Firebase Security Rules to ensure that only authorized users receive the data». أي أن القواعد تُعاد لكل تحديث يُسلَّم.
4. **الكتابة والقراءة مترابطتان:** «When write operations fail or experience throttling, a read might stall waiting for consistent data from the changelog».
5. **الدُفعات الكبيرة تُبطئ الإشعار:** «Large batches might give you higher throughput from the writer's perspective but might actually increase the notification time for snapshot listeners».
6. **العودة إلى polling:** يرجع المستمع من listening إلى polling تلقائيًا عند إعادة توازن الـ changelog، أو hotspots، أو إعادة تشغيل خادم. «If your polling queries are fast enough, a polling state becomes transparent to your app's users».
7. **المستمع على مجموعة كبيرة كثيرة التغير مكلف حتى لو كانت نتيجته صغيرة:** «Snapshot listeners that try to filter too much incoming data might be slow even if the result set is small».

### 0.2 changelog واحد لكل قاعدة: هكذا عمل بث Firestore سنوات
من مدوّنة Google Cloud بتاريخ 2022-10-21 (https://cloud.google.com/blog/products/databases/building-scalable-real-time-applications-with-firestore) **[V]**:
- «Previously, the changelogs were managed by a single backend server for each Firestore database. This meant that the maximum write throughput for Firestore Native was limited to what could be processed by one server».
- وكان الحد: «a limit of 10,000 write operations per second per database». أُزيل في 2022 بتقسيم الـ changelog أفقيًا، وصارت معالجة الاستعلام تقرأ من عدة مصادر.
- **[I] التشابه جزئي:** الواحد لكل قاعدة عند Google كان **الـ changelog**، أي ترتيب التغييرات وتوزيعها. أما الـ commits فكانت متزامنة، كل منها معاملة Spanner مستقلة بأقفال لكل مستند [P §IV-D1، §IV-D2؛ المسار A §3]. فـ «تسلسل واحد وتوزيع واحد لكل قاعدة» جُرّب في الإنتاج حتى 2022 بحد 10k كتابة/ث. أما «كاتب واحد ينفّذ التحقق والقواعد والـ commit» في ADR-019 فلم يجرّبه أحد على هذا المقياس، وسقفه يُقاس في F0 (README، F0-11).

### 0.3 الاستئناف وحدوده كما يراها العميل والفوترة
- **30 دقيقة:** صفحة التسعير (https://firebase.google.com/docs/firestore/pricing) **[V]**: «If offline persistence is enabled and the listener is disconnected for more than 30 minutes … you will be charged for documents and index entries read as if you had issued a brand-new query». **[I]** أي أن الإنتاج لا يستأنف من resume token أقدم من نحو 30 دقيقة بقراءة الفروق، بل يعيد الاستعلام كاملًا.
- **إعادة تقييم القواعد** (الصفحة نفسها) **[V]**: تُحسب عند إصدار الاستعلام، وعند كل تحديث للنتائج، وعند عودة الجهاز متصلًا، و«Any time you update your rules»، و«Any time you update the dependent documents in your rules». **[I]** إذن المستمع المفتوح يتبع المستندات التي تقرؤها قواعده عبر `get()`/`exists()`، وتغيّر أي منها يعيد تقييمه. هذا متطلّب على ADR-026، لا تفصيل.
- **existence filter وbloom filter في العميل** **[V]** (`packages/firestore/src/remote/watch_change.ts:469-500`): إن اختلف `count` عن عدد المستندات المحلية للهدف، يجرّب العميل الـ bloom filter (`unchangedNames`). فإن فشل أو كان false positive، يعيد ضبط الهدف (`resetTarget`) ويعيد الاستعلام بلا resume token، ويضع المستندات المشكوك فيها في «limbo». يحل العميل مستندات limbo بأهداف مستند منفرد، بحد أقصى `MAX_CONCURRENT_LIMBO_RESOLUTIONS = 100` (`core/firestore_client.ts:110`). **[I]** عدم التطابق بعد الاستئناف يولّد على الخادم موجة من أهداف المستند الواحد.
- **العميل يحفظ resume token كل 5 دقائق على الأقل** **[V]** (`local/local_store_impl.ts:106`، `RESUME_TOKEN_MAX_AGE_MICROS = 5 * 60 * 1e6`). فقد يعود العميل بعد تعطّل برمز عمره دقائق، وعلى الخادم أن يقبله أو يرفضه بأمان.
- **`limitToLast` لا يصل إلى الخادم أصلًا** **[V]** (`core/query.ts:318-327`): يقلب العميل اتجاهات `orderBy` ويبدّل المؤشرات، فلا يرى الخادم إلا `limit` عاديًا. نافذة واحدة تكفي.

### 0.4 حدود الاستماع المنشورة
- صفحة الحصص الحالية لـ Firestore (https://firebase.google.com/docs/firestore/quotas) لم تعد تذكر عدد المستمعين لكل عميل ولا الاتصالات المتزامنة **[V، فحصتُ نص الصفحة]**. الرقمان القديمان (100 snapshot listener لكل عميل، ومليون اتصال متزامن) من ذاكرتي لا من مصدر حالي **[U]**.
- RTDB للمقارنة (https://firebase.google.com/docs/database/usage/limits) **[V]**: 200,000 اتصال متزامن لكل قاعدة (100 في خطة Spark)، ونحو 100,000 استجابة/ث «including simultaneous broadcast and read operations».

---

## 1. InstantDB (Clojure + Postgres، Apache-2.0)

**المصدر:** مقالة البنية `client/www/_posts/architecture.md` (منشورة على instantdb.com/essays/architecture)، و`server/src/instant/reactive/invalidator.clj` @b97a1164.

- **الكشف: إبطال خشن ثم إعادة تشغيل** **[V]**. يحوّل كل استعلام إلى مجموعة «topics» تصف جزء الفهرس الذي يهمّه، فكرة مأخوذة من Asana Luna وFigma LiveGraph (`architecture.md:291-303`). ويقرأ الـ invalidator الـ WAL ويحوّل كل تغيير إلى topics من الشكل نفسه، ثم يطابق بين المجموعتين (`:305-317`). المطابقة تعلّم الاستعلامات `stale` وتعيد الـ sockets المعنيّة (`invalidator.clj:65-75`)، ثم يُرسل لكل جلسة أمر `:refresh` (`:244-251`). بعدها تعيد الجلسة تشغيل استعلاماتها الـ stale على Postgres.
- **ما تعلّموه** **[V]**: «Our version zero for this algorithm was very inefficient. We would effectively do an N^2 comparison from every transaction topic to every query topic» (`architecture.md:317`). صاروا يحفظون الـ topics في بنية شجرية ويقلّمون مبكرًا. ويرفضون صراحةً أبسط الحلول: «we could tail every transaction and refresh every query … our database would get hammered» (`:285`).
- **الترتيب والتوازي** **[V]**: الإبطال تسلسلي داخل التطبيق الواحد ومتوازٍ بين التطبيقات، عبر «grouped queue» بطابور فرعي لكل `app-id`، حتى لا يستأثر تطبيق مزدحم بالعمال (`:323-337`)، وبحد `:max-workers 8` (`invalidator.clj:277-288`). ويُدمج عدة سجلات WAL متتالية لتطبيق واحد في سجل واحد (`combine-wal-records`، `:155-177`).
- **الضغط الخلفي** **[V]**: `drop-backpressure?` (`:261-272`) يُسقط سجل WAL لتطبيق تجاوز تأخر رأس طابوره عتبة محددة، وهو خلف feature flag. **[I]** إسقاط إبطال يعني أن الاستعلام يبقى قديمًا حتى تأتي كتابة لاحقة، فهو تنازل عن الصحة مقابل البقاء.
- **الاستئناف وفجوات WAL** **[V]**: يحمل «singleton slot» آلةٌ واحدة، وتوزّع سجلات WAL على بقية الآلات عبر gRPC (`:783-797`)، وتكتبها في جدول history قبل تأكيد الـ LSN «so that we never miss a wal entry» (`:438-440`). لكن كشف السجلات المفقودة عند انتقال الـ slot لا يفعل إلا التسجيل: `;; TODO: We should fetch the missing wal-records from the history table` (`:367`).
- **[I] الدرس:** حتى مع WAL، احتاجوا كاتب history وتسلسلًا خاصًا (`isn`) وslot وحيدًا متنقلًا. أي أنهم أعادوا بناء «changelog بتسلسل»، وهو ما يعطينا إياه ADR-019 مباشرة لأن كل الكتابات تمر بعمليتنا.

## 2. Convex (Rust، محرك تفاعلي خاص)

**المصادر:** https://stack.convex.dev/how-convex-works، و https://stack.convex.dev/fully-reactive-pagination، و https://docs.convex.dev/api/interfaces/server.PaginationResult، و https://docs.convex.dev/production/state/limits.

- **الكشف: read sets بنطاقات فهرس** **[V]**. كل دالة query تسجّل ما قرأته كنطاقات فهرس، مثل `items.remaining > 0`. يقرأ «subscription manager» سجل المعاملات مرة واحدة لكل الجلسات ويفحص التقاطع مع كل read set، وهي الخوارزمية نفسها التي يستعملها الـ committer لكشف تعارض OCC. عند التقاطع: «reruns the function and pushes its updated return value to the client»، أي إعادة تشغيل كاملة لا تصحيح تدريجي.
- **الاتساق** **[V]**: كاتب وحيد للسجل («the sole writer to the transaction log»)، وطوابع HLC، و«all queries in the client's query set are at the same timestamp». **[I]** هذا يقابل عندنا: كل الأهداف على اتصال واحد تتقدم معًا إلى `read_time` واحد عبر global `TargetChange(NO_CHANGE)`.
- **نوافذ limit** **[V]**: يحوّل Convex الصفحة من «limit query» إلى «range query». تُحفظ نهاية الصفحة كمؤشر ثابت، فتكبر الصفحة أو تصغر بين طرفيها. وحين تقرأ الصفحة بيانات كثيرة تُعاد `pageStatus: "SplitRecommended" | "SplitRequired"` مع `splitCursor`. **[I]** هذا لا يناسب Firestore: دلالة `limit(n)` عنده صارمة، فالمستند الذي يدخل قبل الحد يُخرج آخر مستند (removal)، ولا يحق للخادم توسيع النافذة. نأخذ منه فكرة الحد الأقصى للقراءة لكل إعادة حساب فقط.
- **الحدود** **[V]**: 32,000 مستند ممسوح لكل query، و16 MiB، و4,096 نداء `db.get`/`db.query` لكل دالة (أي حجم الـ read set). والجلسات المتزامنة من 1,000 إلى 200,000 حسب الفئة.
- **[I] الدرس:** read set مضبوط بالنطاقات يجعل الإبطال دقيقًا، لكن ثمن كل إبطال إعادة تشغيل كاملة. وهذا مقبول عندهم لأن الدوال صغيرة ومحدودة بحدود صارمة.

## 3. Supabase Realtime: `postgres_changes` وBroadcast

**المصادر:** https://supabase.com/docs/guides/realtime/postgres-changes، و`supabase/realtime` @f7bf61fc، والـ issues المذكورة.

- **الآلية** **[V]**: slot منطقي **مؤقت** بإضافة `wal2json` (`lib/extensions/postgres_cdc_rls/replications.ex:20`). يستطلعه poller دوريًا عبر `realtime.list_changes(publication, slot, max_changes, max_record_bytes)` (`replications.ex:99-141`)، ويقصّر الفاصل حين يوجد عمل ويطيله حين يخمل (`replication_poller.ex:18-23`). والدالة تطبّق RLS **لكل مشترك** («applies row level security policies for each subscriber»، `replication_poller.ex:3-5`).
- **التكلفة الموثّقة** **[V]**: «Postgres Changes authorizes every event against each subscriber … throughput scales with the number of subscribers, not the write rate. Changes are also processed on a single thread to preserve their order … larger compute add-ons don't meaningfully increase Postgres Changes throughput». ومن جدول Micro: مع RLS و3,000 عميل، 5 تغييرات/ث فقط و3,000 رسالة/ث وp95 يبلغ 616ms. وبلا RLS و500 عميل، 64 تغييرًا/ث. والتوصية: «If you expect more than ~3,000 concurrent subscribers on the same changes, use Broadcast».
- **Broadcast from Database** **[V]** (https://supabase.com/blog/realtime-broadcast-from-database): trigger يكتب في `realtime.messages`، والخادم يقرأ الـ WAL لهذا الجدول وحده، والتفويض بسياسة على قناة لا على كل صف لكل مشترك. **[I]** إذن الحل عندهم إرسال واحد يُوزَّع على الجميع، مع تفويض على مستوى القناة.
- **الحذف و RLS** **[V]**: «RLS policies are not applied to `DELETE` statements»، وتصفية الحذف تحتاج `replica identity full`. **[I]** المقابل عندنا: عند الحذف نحتاج الصورة القديمة للمستند لنعرف من كان يراه. الكاتب في عمليتنا يملكها بلا كلفة إضافية.
- **إخفاقات إنتاج** **[V]**:
  - **#2093 (مفتوح):** «One subscriber whose stored claims make RLS evaluation raise causes `postgres_changes` delivery to stop for every subscriber on the project — silently». فقدوا كل البث نحو ساعتين ونصف، بينما كانت كل المؤشرات خضراء: الـ slot يتقدّم والـ sockets مفتوحة.
  - **supabase-js #1599 (مغلق):** حالة `SUBSCRIBED` تصل قبل جاهزية مستمع الـ replication، فتضيع الكتابات في أول 1 إلى 3 ثوانٍ «silently».
  - **#1747:** اشتراكات تفشل بعد الخمول لأن slots الـ realtime غير موجودة.
  - الأحداث أثناء الانقطاع لا تُعاد. هذا نتيجة الـ slot المؤقت **[I]**، والوثائق لا تعد بأي ضمان تسليم.

## 4. ElectricSQL: من Satellite إلى Shapes

**المصادر:** https://electric.ax/blog/2024/07/17/electric-next، و https://electric.ax/docs/sync/guides/shapes، و https://electric.ax/blog/2025/08/13/electricsql-v1.1-released، و`electric-sql/electric` @3f9809fb.

- **لماذا أعادوا الكتابة** **[V]**: «The complexity of the stack has provided a wide surface for bugs»، ومستخدموهم الأوائل «reported performance and reliability issues». تركوا بروتوكول Satellite على WebSocket، وانتقلوا إلى HTTP قابل للتخزين المؤقت في CDN.
- **Shape** **[V]**: جدول واحد، و`where`، وأعمدة. لا `LIMIT` ولا `ORDER BY` في تعريف الـ shape، ويظهر `limit`/`orderBy` فقط في subset snapshots (`requestSnapshot()`). والـ shape غير قابل للتغيير. يحمل كل shape سجلًا (shape log) بـ `offset` و`handle`. وحين يُحذف الـ handle، بسبب تغيّر slot أو schema أو تجاوز `ELECTRIC_MAX_SHAPES`، يتلقى العميل `409` أو `must-refetch` ويعيد المزامنة من الصفر.
- **الكشف: فهرس عكسي للشروط** **[V]**. `Electric.Shapes.Filter.WhereCondition` (`packages/sync-service/lib/electric/shapes/filter/where_condition.ex:1-14`): شجرة في ETS، كل عقدة فيها شرط مفهرس، مع `equality_index.ex` و`inclusion_index.ex`، وما لا يُفهرس يذهب إلى `other_shapes` ويُقيَّم واحدًا واحدًا. والوثائق صريحة: «With non-optimized where clauses, throughput is inversely proportional to the number of shapes». أما `field = constant` و`field IN list` فتبقى ثابتة مع زيادة عدد الـ shapes.
- **إخفاق إنتاج ثم إصلاح** **[V]**: في v1.1 كان التخزين «a critical bottleneck» مع «replication lag climbing, CPUs maxing out». السبب تحديث فهرس CubDB مع كل إدراج، وتعطّل المعاملات الكبيرة القراءات طويلًا. والحل سجل append-only مقسّم إلى chunks، مع فهرس متفرق يكبر من الطرف فقط. وكانت النتيجة كتابة أسرع ×102، و«5x faster at 1,500 shapes».
- **[I] الدرس:** (1) الفهرس العكسي على شروط المساواة والعضوية هو ما يجعل المطابقة شبه مستقلة عن عدد المستمعين. (2) Electric تجنّب نوافذ limit الحية كليًا. أما Firestore فيفرضها، فلا نستطيع تجنّبها.

## 5. Zero / Replicache (rocicorp)

**المصادر:** https://zero.rocicorp.dev/docs/deployment، و`rocicorp/mono` @fdd783d2.

- **البنية** **[V]**: `replication-manager` يملك الـ slot ويغذّي نسخة SQLite. و`view-syncers` تخدم الاستعلامات من تلك النسخة، وتدير CVRs (client view records) في Postgres منفصل، و«change database» لسجل التغييرات الحديث. والجلسات يجب أن تبقى على المثيل نفسه («sticky sessions»). ومن الوثيقة: «Since Node is single-threaded, one client group can technically starve other groups».
- **الكشف: IVM حقيقي** **[V]**. تُبنى كل استعلامات ZQL كأنابيب مشغّلات (filter وjoin وtake)، والتغيير يمر فيها فيخرج فرقٌ لا إعادة تشغيل. وCVR يحفظ ما أُرسل لكل عميل، ليُحسب الفرق عند إعادة الاتصال.
- **نوافذ limit: مشغّل `Take`** **[V]** (`packages/zql/src/ivm/take.ts:57-66`): «It takes the first n nodes … It then keeps a *bound* of the last item it has accepted so that it can evaluate whether new incoming pushes should be accepted or rejected». عند إدراج قبل الحد يُخرج عنصر الحد (`:316-358`). وعند حذف من داخل النافذة يقرأ التالي بعد الحد من المصدر بـ `fetch({start: bound})` (`:375-416`). والتعقيد الحالي (Phase 1 وPhase 2 و`gateBound`، `:46-52`) جاء لمنع اتساع النافذة مؤقتًا أثناء معالجة دفعة فيها عدة إزالات. **[I]** أي أن معالجة عدة تغييرات في معاملة واحدة على النافذة نفسها مصدر أخطاء دقيقة. اختبارنا يجب أن يغطي معاملة تحذف عدة عناصر من النافذة وتضيف غيرها.
- **[I] الدرس:** IVM كامل (مع joins) أثقل مما يحتاجه Firestore، فاستعلامات Firestore على مجموعة واحدة بلا joins. لكن خوارزمية `Take` (حد + إعادة ملء من الفهرس) هي النموذج الصحيح لنوافذنا.

## 6. RethinkDB changefeeds

**المصادر:** https://rethinkdb.com/docs/changefeeds/javascript/، و`src/rdb_protocol/changefeed.cc` (فرع `v2.4.x`)، والـ issues.

- **الدعم** **[V]**: `.changes()` بعد `filter` و`getAll` و`between` و`min`/`max` و`orderBy.limit`، و`orderBy` يتطلب فهرسًا و`limit`. والوثائق: «cannot guarantee delivery … unidirectional with no acknowledgement».
- **نافذة limit على مسار الكتابة** **[V]**: `limit_manager_t::commit(rwlock_in_line_t *spot, …)` يجري تحت قفل الكتابة على الـ shard (`guarantee(spot->write_signal()->is_pulsed())`، `changefeed.cc:1125-1128`). يحذف ويضيف داخل النافذة، ثم يقص إلى `limit`. وإن نقصت النافذة وكان هناك ما على القرص، يقرأ المزيد من الفهرس الثانوي **وهو ما زال داخل القفل** (`read_more`، `:1186-1214`). **[I]** هكذا تبقى إعادة الملء متسقة مع تسلسل الكتابات، والثمن تأخير الكتابة بقدر عدد النوافذ المتأثرة.
- **فيض الطابور** **[V]**: إن تجاوز طابور التغييرات `changefeed_queue_size` (افتراضيًا 100,000) يُمسح الطابور كله (`skipped += queue->size(); queue->clear();`، `:1572-1580`)، ويتلقى العميل مستند خطأ «Changefeed cache over array size limit, skipped %zu elements.» (`:3335-3343`). وissue #6109 (مفتوح) يقرّ بالمشكلة: «most people are not going to handle `error` documents in their code … I doubt that a changefeed with skipped notifications is of much use anyway».
- **إخفاقات أخرى** **[V]**: #4851 «Changefeed results not correct on `.order_by` query»، أي نتائج النافذة خرجت عن الترتيب في 2.1.4. و#2953: لا استئناف، والتغييرات تضيع إن سقط المستهلك. و#4678 «Changefeed Crashing Bugs» (33 تعليقًا).
- **خيار `squash`** **[V]**: دمج التغييرات المتعددة للمستند في آخر حالة. **[I]** يقابله في Firestore أن اللقطة لا تحمل إلا آخر نسخة من كل مستند حتى نقطة الاتساق.

## 7. Meteor: oplog tailing (المثال الكلاسيكي لما يفشل)

**المصادر:** https://github.com/meteor/docs/blob/master/long-form/oplog-observe-driver.md، و`meteor/meteor` @4c94e924، ومنتدى Meteor.

- **الكشف** **[V]**: كل خادم Meteor يقرأ oplog Mongo كاملًا، ويطابق كل إدخال مع الاستعلامات المفتوحة عبر Minimongo. وما لا يفهمه المحرك (`skip`، أو `limit` بلا `sort`، أو `$where`…) يعود إلى polling كل 10 ثوانٍ.
- **نافذة limit بمخزن احتياطي** **[V]** (`packages/mongo/oplog_observe_driver.js:70-82, 266-331`): `_published` بحجم `limit`، و`_unpublishedBuffer` (MinMaxHeap) بحجم يصل إلى `limit` آخر. حين يخرج مستند من المنشور يُرفع أول عنصر في المخزن بلا استعلام. وحين يفرغ المخزن ولا يُعرف إن بقي شيء، يُعاد الاستعلام (`_needToPollQuery`).
- **التأخر** **[V]** (`packages/mongo/oplog_tailing.ts:13, 376-378`): إن تجاوز طابور الإدخالات `METEOR_OPLOG_TOO_FAR_BEHIND` (افتراضيًا 2000)، يُرمى الطابور وتُعاد **كل** الاستعلامات.
- **إخفاقات إنتاج** **[V، منتدى]**: «every meteor instance does oplog tailing. Which means the more instances you added, the more CPU database uses» (https://forums.meteor.com/t/meteor-scaling-redis-oplog-status-prod-ready/30855). والكتابات المجمّعة (bulk) تغرق عملية Node حتى بلا مستمعين متأثرين (https://forums.meteor.com/t/cpu-spikes-due-to-oplog-updates-without-subscriptions/60028). ومن هنا ظهر redis-oplog وoplogtoredis: قارئ واحد يوزّع.
- **[I] الدرس:** (1) المخزن الاحتياطي فوق النافذة يقلل استعلامات إعادة الملء. (2) «too far behind → أعد الكل» يولّد thundering herd في أسوأ لحظة. والأفضل إعادة ضبط الأهداف المتأخرة وحدها، وعلى دفعات.

## 8. Hasura: polling متعدد الإرسال (البديل المتعمَّد)

**المصدر:** https://github.com/hasura/graphql-engine/blob/master/architecture/live-queries.md **[V]**.
- رفضوا LISTEN/NOTIFY لأن الأحداث «might be dropped in case of the consumer restarting». ورفضوا WAL لأن «LR slots are expensive … often not available on managed database vendors». وقالوا إن مطابقة الحدث بالاستعلام «becomes intractable for anything complicated».
- الحل: تجميع الاستعلامات المتشابهة في SQL واحد بعلاقة من المتغيرات (multiplexing)، يُستطلع كل فترة. والقياس: مليون live query وPostgres عند نحو 28% حمل، مع latency أقل من ثانية.
- الثمن المعترف به: «latency when write-loads are small».
- **[I] الدرس:** نصلح المطابقة الدقيقة لأن استعلامات Firestore بسيطة (مجموعة واحدة، وفلاتر محدودة، ونافذة)، فلا تنطبق حجة «intractable». لكن فكرة تجميع المستمعين على الاستعلام نفسه (dedupe) تبقى قيّمة.

## 9. Figma LiveGraph، وAsana Luna (الأصل الذي اقتبسته Instant)

**المصادر:** https://www.figma.com/blog/livegraph-real-time-data-fetching-at-figma/، و https://www.figma.com/blog/livegraph-real-time-data-at-scale/ **[V]**.
- **الأصل:** كل مثيل يقرأ تيار التكرار كاملًا (نحو 10,000 كتابة/ث)، ويطابق التغيير مع «an index of all active subqueries for each type of object»، والنتائج في الذاكرة، والاستعلامات المتطابقة مشتركة بين العملاء.
- **ما انكسر:** تقسيم Postgres أفقيًا كسر «single ordered update stream»، فرقّعوه بدمج كل التيارات في تيار واحد. وصار «every database shard participated in every user optimistic update». ومع كل نشر يعيد كل العملاء الاتصال على cache فارغ: «thundering the database».
- **الجديد:** invalidator بلا حالة يحسب الإبطال من «query shapes». و«easy» (مساواة) مقابل «hard» (نطاقات، 11 من نحو 700 استعلام)، والصعبة تُبطَل بإفراط.
- **أهم قاعدة لنا: موعد القراءة مع الإبطال (read/invalidation rendezvous).** «LiveGraph cannot skip invalidations at any point». و«During an inflight read, incoming invalidations mark the read as invalidated». القراءة الجارية التي يصلها إبطال لا تُعتمد. **[I]** المقابل عندنا: polling query الأولى تسابق الكتابات. يجب ربطها بـ `seq` دقيق (القسم 11).

## 10. pg_ivm وأشباهه

**المصدر:** `sraoss/pg_ivm` README **[V]**.
- لا يدعم `ORDER BY` ولا `LIMIT`/`OFFSET` في تعريف الـ view (السطر 406).
- ويأخذ `ExclusiveLock` على الـ view عند تعديل جدول أساسي في READ COMMITTED، ويرمي خطأ في REPEATABLE READ/SERIALIZABLE (السطور 485-495).
- **[I]** غير صالح لنا: view لكل مستمع مستحيل عدديًا، والقفل يسلسل الكتابات، ولا نوافذ. نستبعده.

---

## 11. جدول المقارنة

| النظام | كيف يعرف الاستعلامات المتأثرة | الدقة | نوافذ limit/orderBy | الاستئناف | الضغط الخلفي | أبرز إخفاق موثّق |
|---|---|---|---|---|---|---|
| **Firestore** | changelog في المعاملة نفسها + reverse query matcher، ثم القواعد لكل تسليم | دقيق | يدعمها (التفاصيل غير منشورة) | resume token، ونحو 30 دقيقة، ثم existence filter + bloom، وإلا requery | العودة إلى polling | حد 10k كتابة/ث لقاعدة واحدة حتى 2022 |
| **InstantDB** | WAL → topics، تطابق شجري، ثم refresh (إعادة تشغيل SQL) | خشن | بإعادة التشغيل | history table، مع TODO للفجوات | grouped queue، وإسقاط خلف flag | N² في النسخة الأولى |
| **Convex** | read set = نطاقات فهرس، ومرور واحد على السجل | دقيق بالنطاق، ثم إعادة تشغيل | يحوّل limit إلى range، وsplit | لم يُوثَّق | حدود صارمة لكل دالة | — |
| **Supabase** | slot مؤقت + wal2json، وRLS لكل مشترك | دقيق لكن مكلف | لا يدعم | لا شيء (تضيع) | خيط واحد | #2093 خطأ مشترك واحد أوقف الجميع |
| **Electric** | فهرس عكسي (مساواة/عضوية)، والباقي خطي | دقيق | لا يدعمها في الـ shape الحي | offset/handle، و409 يعني must-refetch | CDN وسجل لكل shape | التخزين اختنق: replication lag وCPU |
| **Zero** | IVM (filter/join/take) | دقيق، فروق | `Take`: حد + fetch بعد الحد | CVR لكل client group | sticky sessions، ويمكن تجويع المجموعات | تعقيد Take مع الإزالات المتعددة |
| **RethinkDB** | على الـ shard عند الكتابة | دقيق | `limit_manager` تحت قفل الكتابة + `read_more` | لا شيء | طابور 100k، ثم مسح مع رسالة خطأ | #4851 ترتيب خاطئ، و#6109 skipped |
| **Meteor** | كل خادم يقرأ الـ oplog ويطابق بـ Minimongo | دقيق للمدعوم، وإلا polling | منشور + مخزن احتياطي | — | TOO_FAR_BEHIND يعني أعد الكل | CPU يتضاعف بعدد الخوادم |
| **Hasura** | polling مجمّع | — | SQL عادي | الاستعلام نفسه | الفاصل الزمني | latency في الحمل المنخفض |
| **LiveGraph** | فهرس subqueries، ثم shapes بلا حالة | دقيق للسهل، ومفرط للنطاق | لا يدعمها | — | — | thundering herd مع كل نشر |

---

## 12. ما يعنيه لـ ADR-026

كل ما يلي توصيات **[I]** مبنية على الأدلة أعلاه، إلا ما عليه [V].

**C1. نُبقي نموذج ADR-019 ولا نقرأ WAL أو LISTEN/NOTIFY.** الثقة عالية.
- Firestore نفسه عمل بـ changelog واحد لكل قاعدة حتى 2022 **[V]**. وهذا يدعم التسلسل والتوزيع الواحد. أما تنفيذ الـ commits كلها بالتتابع فشيء لم تفعله Google **[I]** (§0.2).
- كل من بنى على WAL اضطر إلى إعادة بناء تسلسل وhistory وslot وحيد (Instant)، أو خسر الأحداث عند الانقطاع (Supabase، slot مؤقت).
- Hasura يرفض الاثنين لأسباب موثّقة.
- القيد المعروف: كتابة تتجاوز Aruma (psql مباشرة) لا تصل المستمعين. نوثّقه في `docs/compat`.

**C2. المطابقة بالمستند قبل التعديل وبعده، في الذاكرة، بفهرس عكسي.** الثقة عالية.
- الكاتب يملك `before` و`after` لكل مستند في المعاملة (ويحتاج `before` للحذف، درس Supabase مع DELETE).
- المستوى الأول: المفتاح `(project_number, database_id, parent_path أو collection_id لـ collection-group)`.
- المستوى الثاني: فهرس مساواة/`in`/`array-contains` على أول فلتر قابل للفهرسة (نمط Electric `WhereCondition`)، والباقي يُقيَّم في Go بالمقارن نفسه الذي يخدم الاستعلامات.
- النتيجة لكل هدف: added إن طابق `after` فقط، وremoved إن طابق `before` فقط، وmodified إن طابق الاثنان.
- القياس المطلوب في F0: زمن المطابقة لكل كتابة مقابل عدد الأهداف المفتوحة (1k و10k و100k) على مجموعة واحدة ساخنة.

**C3. أهداف متطابقة تتشارك حالة واحدة.** الثقة متوسطة-عالية.
- المفتاح القانوني للاستعلام: `(project_number, database_id, canonical query)`، ويحمل النافذة والمطابقة مرة واحدة (LiveGraph وElectric وHasura).
- لكل مستمع: القواعد والتسليم فقط.

**C4. نوافذ limit: حد + مخزن احتياطي + إعادة ملء من الفهرس.** الثقة عالية في الخوارزمية، ومتوسطة في مكان التنفيذ.
- لكل نافذة: المجموعة المرتبة ومستند الحد (نمط Zero `Take` وRethinkDB `limit_manager`).
- مخزن احتياطي صغير بعد الحد (نمط Meteor) يقلل الاستعلامات.
- إدراج قبل الحد يعني إضافة المستند وإزالة مستند الحد. حذف أو خروج من داخل النافذة يعني أخذ التالي من المخزن، أو `SELECT … WHERE sortkey > bound ORDER BY sortkey LIMIT k` على جدول الفهرس.
- `limitToLast` يصل إلينا كـ `limit` مقلوب **[V]**.
- **مكان الملء: خياران يحسمهما F0:**
  - (أ) داخل معاملة الكاتب، كـ RethinkDB تحت القفل: متسق تلقائيًا، لكنه يطيل زمن الكتابة.
  - (ب) بعد الـ commit في goroutine التوزيع: نقرأ في snapshot ونسجّل `seq` الظاهر فيه، ثم نطبّق إدخالات السجل بعده على النافذة.
  - توصيتي (ب)، مع اختبار يكتب عدة إزالات وإضافات في معاملة واحدة على النافذة نفسها (فخ Zero).

**C5. polling query الأولى مربوطة بـ `seq`.** الثقة عالية.
- الترتيب: نسجّل الهدف في المطابِق أولًا ونخزّن ما يصله، ثم ننفذ الاستعلام في snapshot نقرأ فيه `seq` الحالي (S)، ونُسقط المخزَّن الذي `seq ≤ S` ونطبق الباقي. بعدها فقط نرسل `CURRENT`، ثم global `NO_CHANGE` بـ `read_time`.
- هذا يمنع خطأ Supabase #1599 (اشتراك «جاهز» يفقد كتابات)، وسباق LiveGraph بين القراءة والإبطال.
- يحتاج اختبارًا يكتب في اللحظة نفسها التي يُسجَّل فيها الهدف.

**C6. resume token = (عصر القاعدة/epoch، `seq`) مشفّر وغير شفاف.** الثقة متوسطة.
- **إن كان `seq` ما زال في نافذة السجل المحفوظ** (نقترح 30 دقيقة مثل فوترة الإنتاج **[V]**)، أمامنا طريقان:
  - إعادة تشغيل الاستعلام، وإرسال المستندات ذات `update_time > read_time(token)` فقط، مع `ExistenceFilter{count, unchanged_names(bloom)}` ليكشف العميل ما حُذف أو خرج. هذا ما يتوقعه كود العميل **[V watch_change.ts]**.
  - أو إعادة تشغيل السجل للهدف، وهو أدق لكنه يحتاج صور `before` في السجل.
- **وإن خرج من النافذة، أو تغيّر الـ epoch بعد استعادة نسخة احتياطية،** نتجاهل الرمز ونرسل اللقطة كاملة. والعميل يتعامل مع ذلك.
- تحذير: bloom filter خاطئ أو `count` خاطئ يدفع العميل إلى limbo resolution بما يصل إلى 100 هدف مستند متزامن لكل عميل **[V]**. فإن لم نطبّق الـ bloom بدقة، الأفضل ألا نرسله أصلًا (الحقل اختياري).

**C7. لا إسقاط صامت أبدًا. المستهلك البطيء يُعاد ضبطه.** الثقة عالية.
- طابور محدود لكل اتصال، مع دمج تغييرات المستند الواحد قبل الإرسال (squash) حتى نقطة الاتساق.
- إن فاض الطابور: نرسل `TargetChange(RESET)` للأهداف المتأثرة أو نغلق التدفق بـ `UNAVAILABLE`، فيعود العميل بـ resume token. هذا هو «العودة إلى polling» الموثّق في Firestore **[V]**.
- لا نفعل كـ RethinkDB (مسح ورسالة يتجاهلها الجميع، #6109)، ولا كـ Instant (إسقاط الإبطال).
- ولا «أعد الكل» عند التأخر كـ Meteor TOO_FAR_BEHIND: نعيد ضبط الأهداف المتأخرة وحدها، وعلى دفعات مع jitter.

**C8. القواعد لكل تسليم، بعزل أخطاء لكل هدف، مع تتبّع اعتماديات `get()`.** الثقة عالية في العزل، ومتوسطة في الاعتماديات.
- خطأ تقييم قواعد مستمع واحد يُنهي ذلك الهدف وحده بـ `PERMISSION_DENIED`/`removed` مع `cause`، ولا يمس أي هدف آخر. هذا الدرس المباشر من Supabase #2093.
- تقييم list rules لاستعلام ثابت يمكن تخزينه لكل (هدف، auth). لكن الإنتاج يعيد التقييم عند تغيّر القواعد وعند تغيّر «dependent documents» **[V pricing]**. فنسجّل مسارات `get()`/`exists()` التي قرأتها القواعد كأهداف داخلية، ونعيد التقييم عند تغيّرها.
- وعند نشر قواعد جديدة: إعادة تقييم كل الأهداف المفتوحة للمشروع، على دفعات.

**C9. التوزيع لا يعطّل الكاتب.** الثقة عالية.
- الكاتب يسلّم دفعة `(seq, changes)` إلى قناة. وmatcher لكل قاعدة يعالج بالترتيب، ثم يرسل إلى طوابير الاتصالات.
- الترتيب داخل القاعدة تسلسلي، والتوازي بين القواعد والمشاريع، مع عدالة بين المشاريع (درس Instant grouped queue وZero «starve other groups»).

**C10. مراقبة تكشف «كل شيء أخضر ولا شيء يصل».** الثقة عالية.
- مقاييس لكل قاعدة: `seq` المكتوب مقابل `seq` الموزَّع مقابل `seq` المسلَّم (lag)، وأهداف معاد ضبطها، وأخطاء قواعد لكل هدف، وزمن polling query.
- إخفاق Supabase #2093 دام ساعات لأن المؤشرات المتاحة كانت كلها سليمة.

## 13. أسئلة تُحسم بتجربة في F0

1. **زمن المطابقة:** مع 10k و100k هدف مفتوح على مجموعة واحدة، بفلتر مساواة وبدونه. هل يكفي فهرس المساواة وحده (C2)؟
2. **إعادة ملء النافذة:** داخل معاملة الكاتب أم بعدها (C4)؟ نقيس زمن الكتابة مع 1k نافذة `limit 20` متأثرة.
3. **الاستئناف:** requery + `update_time` + existence filter، أم إعادة تشغيل السجل؟ نقيس كلفة كل منهما ودقته مقابل المحاكي الرسمي، كصندوق أسود، بعد انقطاع 1 و10 و40 دقيقة.
4. **سلوك الإنتاج في `count`/bloom:** متى يرسلهما Firestore الحقيقي؟ يُسجَّل من مشروع المالك بشروط الموجز. وهل يرسل `removed_target_ids` لمستند خرج من نافذة limit، أم `DocumentRemove`؟
5. **حجم الطابور لكل اتصال وسياسة RESET:** تجربة عميل بطيء متعمّد عبر SDK الويب وAndroid. هل يستعيد العميل حالته بلا أخطاء؟
6. **تتبّع اعتماديات `get()` في القواعد:** هل يعيد الإنتاج فعلًا تقييم مستمع مفتوح حين يتغيّر مستند تقرؤه القواعد، فيُغلقه إن صار مرفوضًا؟ تجربة على Rules Test API لا تكفي، وتحتاج مستمعًا حيًا.
