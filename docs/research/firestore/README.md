# بحث Firestore (F0): الخلاصة وما تشير إليه الأدلة لـ ADR-024..027

- **الحالة:** توصيات للمراجعة، 2026-10-23. لم يُعتمد منها شيء. تصير ADRs بعد موافقة المالك وتجارب F0.
- **الطريقة:** بحث بالقراءة فقط: لا خوادم ولا محاكيات ولا Firebase حقيقي.
- **الرموز:** [V] موثّق في المصدر، [I] استنتاج، [U] غير متحقق منه.

## المسارات

| الملف | المحتوى |
|---|---|
| [A-google-architecture.md](A-google-architecture.md) | ورقة Firestore (ICDE 2023، النص الكامل مقروء)، وSpanner، والحدود الموثّقة وأسبابها |
| [B-implementations.md](B-implementations.md) | Kor @376abea، hidane @abd4664، waldflam، skunkteam، Firenook وغيرها |
| [C-reactive-systems.md](C-reactive-systems.md) | InstantDB، Convex، Supabase، Electric، Zero، RethinkDB، Meteor، Hasura، LiveGraph |
| [D-indexes-on-postgres.md](D-indexes-on-postgres.md) | ترتيب القيم، ترميز المفاتيح، حدود B-tree، مخطط مقترح |
| [E-transactions.md](E-transactions.md) | ما ترسله كل مكتبة فعلًا، رموز الإعادة، المحاكاة فوق Postgres |

## أهم ما تغيّر عن الخطة

1. **Kor ليس مرجعًا للبث ولا للمعاملات** (B §1).
   - لا Listen فيه، ولا Write stream، ولا WebChannel، فهي في خارطة الطريق فقط.
   - سجل تغييراته مرتّب بـ xid مع علامة ماء، لا بترتيب الـ commit، ولا يقرؤه شيء غير اختبار.
   - معاملاته ترفض الاستعلامات بـ `UNIMPLEMENTED`.
   - يعيد كتابة مدخلات الفهرس كلها مع كل كتابة.
   - «الاكتشافات الخمسة» كلها من المحاكي.
   - **ما نأخذه من Kor:** ترميز المفاتيح، وجدول الفهارس، والاختبار التفاضلي، ودرس GIN. لا أكثر.
2. **Google شغّلت نموذج ADR-019 نفسه في الإنتاج** حتى 2022: changelog على خادم واحد لكل قاعدة، بحد 10,000 كتابة/ث ([مدونة Google Cloud، 2022-10-21](https://cloud.google.com/blog/products/databases/building-scalable-real-time-applications-with-firestore)، تحققت من النص) (C §0).
3. **الفهارس:** الورقة تؤكد اختيار جدول فهارس خاص بمفتاح بايتات، وتحديثه بالفرق في معاملة الكتابة. ورفضت Google فهارس Spanner الأصلية للسببين نفسيهما اللذين يمنعان فهارس Postgres الأصلية عندنا (A §2.2).
4. **مكتبات الويب تحمل ترميز مفاتيح الخادم نفسه:** «These constants are taken from the backend» (`ordered_code_writer.ts:20`) (D §0). فلا نخترع ترميزًا.
5. **`Write.verify = 5` غائب عن googleapis وعن `firestorepb` في Go، وموجود في protos المكتبات.** معاملات الجوال والويب ترسله لكل مستند قرأته ولم تكتبه. Kor يُسقطه، فتفشل تلك المعاملات (E §0).

## القرارات التي تشير إليها الأدلة

| # | القرار المقترح | الدليل | الثقة |
|---|---|---|---|
| **ADR-024: التخزين** | | | |
| 1 | جدول `index_entries(database_id, index_id, key bytea)` لكل الفهارس، التلقائية والمركّبة. مخطط استعلام في Go. **لا GIN ولا فهارس jsonb** | Google رفضت فهارس Spanner لسببين ينطبقان على Postgres [P §IV-D1]. وGIN في Kor سبّب 90% من WAL وملأ قرصًا (`62bffa8`) | عالية |
| 2 | مفتاح الترتيب نسخة من ترميز الخادم كما في المكتبات (OrderedCode وIndexValueWriter)، مع: int وdouble في مجال واحد، وNaN أولًا، و`__id<n>__` رقميًا، وبايت نوع أصلي لا يدخل في الترتيب ليخدم `sum`. ويُختبر بـ fuzz ضد المحاكي ثم الإنتاج | D §1-2. Kor يفوّت `__id<n>__` | عالية |
| 3 | الفهرس التلقائي لكل حقل يُخزَّن تصاعديًا فقط. المسح العكسي يعطي `DESC` مع `__name__ DESC` | D §0 بند 4. Google تخزّن الاتجاهين | متوسطة: يُثبت بالاختبار التفاضلي |
| 4 | مفتاح النطاق `COLLECTION` يبدأ بالمجموعة الأم، و`COLLECTION_GROUP` لا يبدأ بها | وثائق حساب الحجم (D §0 بند 3). وKor على الأرجح يسرّب بين `users/A/posts` و`users/B/posts` [I] | عالية |
| 5 | جسم المستند protobuf في `bytea`، لا jsonb ولا JSON Kor المعلَّم بالأنواع (**يخالف الخطة**) | الورقة تخزّن protobuf [P §IV-D1]. jsonb يفقد `-0.0` (رأيناه في RTDB) ولا يحمل U+0000 | متوسطة-عالية: كلفته فك الترميز في اللوحة |
| 6 | الفهرس يُحدَّث بالفرق داخل الكاتب. للفهرس حالة `CREATING/READY/NEEDS_REPAIR` مع backfill، ونسخة ترميز، ومدقق دوري | [P §IV-D1-2]، وKor `docs/indexes.md` | عالية |
| 7 | **حدود المفتاح:** نقتطع القيمة المفهرسة عند 1500 بايت، وهو سلوك موثّق رسميًا ([quotas](https://firebase.google.com/docs/firestore/quotas)): «truncated… inconsistent query results». ونتعامل صراحة مع حد B-tree (~2704 بايت) لأن Firestore يسمح بمدخل 7.5 KiB واسم 6 KiB | A §8، D §3 | عالية في الاقتطاع، والتعامل مع الحد يُحسم في F0 |
| **ADR-025: البروتوكولات** | | | |
| 8 | منفذ واحد، والتوجيه بـ `content-type: application/grpc`، لأن مسارات WebChannel تحت بادئة خدمة gRPC. h2c خلف الوكيل فقط. ping متساهل لتفادي `GOAWAY too_many_pings` في iOS | hidane، والمحاكي #11238 (B §9) | عالية |
| 9 | توليد Go من الـ protos المضمّنة في مكتبات Firebase، وفيها `verify`. وفهم `verify` في REST JSON | E §0 بندا 2-3 | عالية |
| 10 | WebChannel إلزامي للويب: `getDoc`/`getDocs` تمر عبر Listen، والمعاملات والتجميعات عبر REST. أطوال الإطارات بوحدات UTF-16 | hidane `compatibility.md:93` و`webchannel.md` | عالية |
| **ADR-026: البث** | | | |
| 11 | نُبقي ADR-019: كاتب واحد لكل قاعدة، و`seq` في المعاملة، وتوزيع داخل العملية، **بلا WAL ولا LISTEN/NOTIFY**. و`commit_time` رتيب صارم بالميكروثانية لكل قاعدة: `max(now, last+1µs)` | Google حتى 2022 (C §0). Supabase بـ slot مؤقت يفقد الأحداث، وInstant أعاد بناء التسلسل. Kor وhidane: زمن غير رتيب أو مكرر (B §8) | عالية |
| 12 | مطابقة عكسية في الذاكرة بالمستند قبل وبعد، بفهرس على المساواة و`in`. الأهداف المتطابقة تتشارك حالة. اللقطة الأولى: الاشتراك أولًا، ثم استعلام عند `seq` معلوم، ثم `CURRENT`، ثم `NO_CHANGE` عام بـ `read_time`. استعلامات `limit` تُعاد كاملة أولًا (hidane)، ثم نوافذ بحد ومخزن احتياطي إن أثبت القياس الحاجة | [P §IV-D4]، والوثائق، وhidane `changes.rs:3-8`، وZero وRethinkDB (C §12) | عالية للمطابقة، ومتوسطة لتوقيت النوافذ |
| 13 | الاستئناف والضغط الخلفي: | | متوسطة-عالية |
| | • رمز الاستئناف `(epoch, seq)`، ونافذة ≥ 30 دقيقة، وبعدها استعلام كامل | الفوترة: الانقطاع أكثر من 30 دقيقة يُحاسَب استعلامًا جديدًا [V] | |
| | • existence filter بعدد دقيق فقط، وbloom اختياري | waldflam §3، `watch_change.ts:469-500` | |
| | • رمز غير صالح يعني `RESET`، لا خطأ | waldflam §3 | |
| | • المستمع البطيء: `RESET` للهدف أو `UNAVAILABLE`، **ولا إسقاط صامت أبدًا** | RethinkDB #6109، Instant | |
| | • خطأ القواعد معزول لكل هدف | Supabase #2093: مشترك واحد أوقف الكل ساعتين ونصف | |
| | • إعادة تقييم القواعد حين تتغير مستندات `get()` | [pricing] | |
| **ADR-027: النطاق** | | | |
| 14 | المعاملات تفاؤلية للجميع، بالتحقق داخل الكاتب مقابل سجل `seq`: مستندات، وphantoms بالمطابق نفسه الذي يخدم Listen. بلا اتصال محجوز، وبلا SERIALIZABLE، وبلا أقفال. نعلن `concurrencyMode: OPTIMISTIC`، ونؤجّل الوضع التشاؤمي ونوثّقه. ورموز الأخطاء حسب جدول الإعادة في كل مكتبة | OPTIMISTIC إعداد إنتاجي وافتراضي Enterprise. الجوال تفاؤلي دائمًا. Google ترفض أقفال الطرف الثالث خوفًا من DoS [P §III-E] (E §6) | متوسطة-عالية |
| 15 | **مؤجّل صراحة:** Pipelines/Enterprise، وKNN لـ `find_nearest`، و`PartitionQuery`، و`explain`، والقواعد المسماة (بقرار المالك)، وتعدد العقد، والحدود الخاصة بالتوزيع (500/50/5 و500 كتابة/ث). **ونطبّق** `FAILED_PRECONDITION` برابط حين يلزم فهرس، مع وضع تطوير صريح يخدم كل شيء | B §9 بند 16، [P §IV-D3]، وتسجيلات hidane من الإنتاج (47 فهرسًا) | متوسطة |

## قرار مفتوح: RLS لجداول Firestore أم لا (ADR-024)

ADR-017 أجّل RLS، ونصّ على إعادة النظر حين تظهر «خدمة تكتب استعلامات تُبنى وقت التشغيل». Firestore هو تلك الحالة.

**مع RLS:**
- مخطط الاستعلام يبني SQL ديناميكيًا لا يراه `scopecheck`، والعزل حينها في قاعدة البيانات لا في انضباط الكود.
- مقارنات `bytea` و`int8` كلها leakproof، فيبقى مسح نطاق `index_entries` صالحًا تحت السياسات (D §3، `indxpath.c:2607`).
- لا مشروع مفتوح يعزل على مستوى القاعدة. Kor يعزل ببادئة نصية لا يلتقط خطأها شيء (B §8.1).

**ضد RLS:**
- يحتاج `set_config`/`SET LOCAL` في كل معاملة عبر pgx. (يخفّف هذا أن الكاتب لكل قاعدة، فالمشروع ثابت في كل معاملة كتابة. لكن القراءات والاستعلامات وListen تأخذ اتصالات من المجمّع لكل طلب.)
- يكسر فهارس GIN، وهي غير leakproof، لكننا لا نستعملها أصلًا.
- يبطئ التنظيف العام الذي يحتاج دورًا يتجاوزه.
- يكسر الاتساق مع بقية الخدمات.

**البديل الوسط:**
- يُبنى SQL كله في builder واحد في Go، يضع `database_id` أول عمود في كل مفتاح ويرفض غيره.
- معه اختبار وقت التشغيل يفحص كل SQL ناتج، ونمط RTDB الحالي: `rtdb.nodes` مفتاحها `database_id`، والـ scope annotation تشرح أن `database_id` لا يُعطى إلا بالمشروع.

**يُحسم بتجربة F0-5:**
- كلفة RLS على مسح نطاق، وعلى `set_config` لكل معاملة.
- مقارنة ذلك ببساطة الـ builder.

ميلي [I]: builder مع حارس، وRLS على جداول Firestore وحدها إن كانت كلفته أقل من 5% في المسار الساخن.

## أسئلة تُحسم بتجربة في F0

| # | التجربة | تحسم | المصدر |
|---|---|---|---|
| F0-1 | fuzz الترميز: `Compare == bytes.Compare`، ثم مقارنة ترتيب مع المحاكي عبر مكتبة Go، ثم عينة إنتاج تحت `aruma-ref` | 2، 3 | D §8-1 |
| F0-2 | WAL والحجم وزمن commit لمستندات بـ 10 و100 و1000 حقل، ومصفوفة بـ 1000 عنصر. مقارنة: فرق، وحذف الكل وإعادته، وGIN | 1، 6 | A §10، D §8-2 |
| F0-3 | حدود المفتاح: قيمة 1500 بايت مع اسم 6 KiB، والضغط داخل الفهرس، و`SET STORAGE PLAIN` | 7 | D §8-3 |
| F0-4 | تسجيل الإنتاج لما «يحتاج فهرسًا» لكل شكل استعلام (دمج المساواة، و`__name__ desc`، وcollection-group) | 15 | D §8-9 |
| F0-5 | RLS مقابل builder: زمن المسح و`set_config` عبر pgx | القرار المفتوح | D §8-5 |
| F0-6 | زمن المطابقة لكل كتابة مع 1k و10k و100k هدف على مجموعة ساخنة. وإعادة ملء نافذة `limit` داخل معاملة الكاتب أو بعدها | 12 | C §13-1/2 |
| F0-7 | سلوك الإنتاج في الاستئناف: متى يأتي `count`/bloom، و`removed_target_ids` مقابل `DocumentRemove` عند الخروج من نافذة | 13 | C §13-4 |
| F0-8 | رموز أخطاء الإنتاج: `verify` قديم، ومعاملة منتهية ("transaction has expired")، و`new_transaction` على استعلام فارغ، و`set` بلا تغيير (هل يبقى `update_time`؟)، و`read_time` أقدم من ساعة | 9، 14 | E §7 E1 |
| F0-9 | كلفة جدول نسخ لساعة (`document_versions`) مقابل سجل ومخزن حلقي: يحسم دعم `read_time` والقراءة فقط واستئناف Listen | 13، 14 | E §7 E3، B §9-6 |
| F0-10 | تشغيل اختبارات المعاملات الرسمية (js-sdk وnodejs-firestore وGo) على نموذج أولي: أيها يفترض الانتظار بدل الإجهاض | 14 | E §7 E4 |

## المصادر الأساسية

- **ورقة Firestore** (ICDE 2023): https://storage.googleapis.com/gweb-research2023-media/pubtools/7076.pdf
- **ورقة Spanner** (OSDI 2012): https://static.googleusercontent.com/media/research.google.com/en//archive/spanner-osdi2012.pdf
- **وثائق Firestore:**
  - [real-time queries at scale](https://firebase.google.com/docs/firestore/real-time_queries_at_scale)
  - [quotas](https://firebase.google.com/docs/firestore/quotas)
  - [transaction-data-contention](https://firebase.google.com/docs/firestore/transaction-data-contention)
  - [pricing](https://firebase.google.com/docs/firestore/pricing)
  - [best practices](https://docs.cloud.google.com/firestore/docs/best-practices)
- **المستودعات:** omelas-tech/kor @376abea، hidane-dev/hidane @abd4664. والبقية بـ SHA داخل كل مسار.
