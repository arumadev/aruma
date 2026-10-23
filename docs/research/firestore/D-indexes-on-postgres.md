# المسار D: فهارس Firestore فوق Postgres (لـ ADR-024)

- **التاريخ:** 2026-10-23. بحث بالقراءة فقط: لم يُشغَّل Postgres ولا محاكٍ ولا خادم.
- **الرموز:** **[V]** متحقق منه في المصدر (رابط، أو `path:line @sha`)، **[I]** استنتاج مني، **[U]** لم أتحقق منه.
- **اللقطات المقروءة:**
  - Kor `omelas-tech/kor@376abea` (Go، Apache-2.0).
  - hidane `hidane-dev/hidane@abd4664` (Rust، Apache/MIT). أخذنا منه الأفكار فقط.
  - `firebase/firebase-js-sdk@3a5d6a0`: `packages/firestore/src/...`.
  - `google/orderedcode@9a3d15e`.
  - `cockroachdb/cockroach@d30c905`: `pkg/util/encoding/encoding.go`.
  - `postgres/postgres@9412c35` (فرع `REL_18_STABLE`).
- **تنبيه عن العزل:** Aruma **لا يستعمل RLS اليوم**. أجّله ADR-017، والعزل الحالي ثلاثة أشياء: عمود `project_number` في كل جدول، والفحص `internal/store/scopecheck`، واختبارات العزل. وADR-017 نفسه يذكر «خدمة تُبنى استعلاماتها وقت التشغيل (قد يحدث مع Firestore)» سببًا لإعادة النظر. لذلك نعرض RLS في §3.6 **قرارًا مفتوحًا** بأدلة الجانبين، لا شيئًا موجودًا.

## 0. الخلاصة

1. **ترتيب Firestore لا يُكتب بـ `ORDER BY` على jsonb.** نحتاج مفتاحًا من البايتات: ترتيب `memcmp` عليه هو ترتيب Firestore نفسه. يُحسب في Go، ويُخزَّن في `bytea`، ويقارنه Postgres بـ `memcmp` ثم الطول (`varlena.c:4069-4087`) [V].
   - Google تفعل الشيء نفسه. مكتبة الويب تحمل ترميزًا **مشتقًا** من ترميز الخادم، لا نسخة مطابقة منه (§2.1):
     - تعليق `ordered_code_writer.ts:20`: «These constants are taken from the backend».
     - تعليق `firestore_index_value_writer.ts:50-51`: «copied from the backend… Code that has different behavior was modified» [V].
2. **جدول إدخالات فهارس يُكتب في معاملة المستند نفسها** (نمط IndexEntries، وهو أيضًا ما فعله Kor) أفضل من فهارس Postgres الأصلية على jsonb. ثلاثة أسباب:
   - (أ) ترتيب الأنواع المختلطة.
   - (ب) خطة استعلام محددة سلفًا.
   - (ج) كلفة GIN على كامل المستند عند Kor. عندنا منها رقمان مختلفان المصدر:
     - **حادثة إنتاج:** «~1.5 MB of WAL per 21 kB document» وامتلأ القرص. ورسالة الـ commit `62bffa8` تقول إن السبب المفترض أولًا، full-page writes من تكرار الـ checkpoint، «was mostly wrong». أما أن GIN هو السبب في الحادثة فاستنتاج من القياس التالي، لا قياس للحادثة نفسها.
     - **قياس اصطناعي** (`walcost_test.go`، خلف `KOR_WAL_COST`): مستندات متداخلة على شكل TMDB. النتيجة 65.0 kB WAL لكل مستند مع GIN، و6.5 kB بدونه، أي 90% من WAL. والكاتب نفسه يقول إن الأرقام تعتمد على العتاد «and the point is the ratio» [V].
   - **[I] الثقة متوسطة لا عالية:** (أ) و(ب) حجتان في التصميم، و(ج) قياس واحد على شكل واحد. التجربة F0-2 (§8) تقيس على أشكالنا.
3. **مفتاح فهرس النطاق `COLLECTION` يجب أن يبدأ بمسار المجموعة الأم.** وثائق حساب الحجم عند Google تضع «اسم المستند الأب» في كل إدخال فهرس على نطاق المجموعة، ولا تضعه في نطاق `COLLECTION_GROUP` [V].
   - Kor لا يضع الأب في المفتاح، ولا يقيّد المسح به. استنتاجي من قراءة الكود، دون تشغيل: استعلام على `users/A/posts` يخدمه فهرس قد يعيد مستندات من `users/B/posts` [I].
4. **الفهرس الأحادي التلقائي يكفيه اتجاه واحد في Postgres.** المسح العكسي لـ `(field, value, name)` تصاعديًا يعطي بالضبط `value DESC, __name__ DESC`، وهو ترتيب Firestore للفهرس التنازلي [I]. أما في Firestore فكل حقل يأخذ فهرسين، تصاعديًا وتنازليًا [V].
5. **ثلاث مفاجآت في Postgres:**
   - **حد حجم إدخال B-tree** نحو 2704 بايت (`nbtree.h:157-173`) [V]، وFirestore يسمح بإدخال حتى 7.5 KiB، وباسم مستند حتى 6 KiB [V]. المفتاح المركّب لن يتسع لكل ما يقبله Firestore.
   - **ضغط المفتاح:** قيمة الفهرس فوق `TOAST_INDEX_TARGET` (نحو 510 بايت) تُضغط داخل الفهرس (`indextuple.c:117-127`، `heaptoast.h:68`) [V]. فيُفك ضغطها عند كل مقارنة [I].
   - **jsonb مع RLS:** عامل `@>` على jsonb ليس leakproof (`pg_proc.dat:10413`)، وPostgres لا يستعمل شرطًا غير leakproof شرطَ فهرس قبل شروط RLS (`indxpath.c:2604-2608`) [V]. فلو فعّلنا RLS بدور لا يتجاوزه، لن يُستعمل فهرس GIN الذي يعتمد عليه Kor. أما مقارنات `bytea` و`text` و`int8` فكلها leakproof [V].
6. **التسجيلات من الإنتاج تكشف ما لا يظهر في المحاكي.**
   - المحاكي يقبل كل استعلام دون فهرس [V].
   - الإنتاج يطلب فهرسًا مركّبًا حتى لـ `order_by __name__ desc` وحده.
   - استعلامات hidane المسجّلة احتاجت 47 تعريف فهرس في الإنتاج: مركّبة، وأحادية لـ `COLLECTION_GROUP`، وفهارس vector [V].

## 1. ترتيب القيم في Firestore: ما يجب أن يحققه المفتاح

المصدر الرسمي: [Data types: value type ordering](https://firebase.google.com/docs/firestore/manage-data/data-types).

| النوع | القاعدة | الحالة | ملاحظة للترميز |
|---|---|---|---|
| ترتيب الأنواع | null < bool < NaN < number < timestamp < string < bytes < reference < geopoint < array < vector < map | [V] الوثيقة، و`kor/internal/value/compare.go:13-28`، و`hidane-core/src/order.rs:7-8` | بايت نوع (tag) في أول كل ترميز |
| الأرقام | int64 وdouble متداخلة في ترتيب عددي واحد | [V] الوثيقة: "interleaved" | لا يصح فصل int عن double كما يفعل FDB tuple (§2.3) |
| NaN | كل قيم NaN متساوية، وأصغر من ‎-Infinity | [V] الوثيقة | Kor يجعلها نوعًا مستقلًا بين bool وnumber (`sortkey.go:30-33`) |
| ‎-0.0 | يساوي 0 في المقارنة | [V] بالتجربة: Kor وجد أن `x == 0` يجب أن يطابق ‎-0.0 (`docs/differential-testing.md:60-65`). والوثيقة تقول فقط "IEEE 754 total ordering" | المفتاح يجمع الصفرين، والمستند المخزّن يحفظ الإشارة |
| int مقابل double | مقارنة بالقيمة الدقيقة: `2^53+1 > 2^53` و`i64::MIN == -2^63` | [V] `hidane order.rs:19-21`، `kor compare.go` | Kor: أس ومانتيسا بطول 10 بايت. hidane: أقرب double ثم فرق صحيح بطول بايتين (`key.rs:36-40`) |
| string | بترتيب بايتات UTF-8، لا بوحدات UTF-16. أول 1500 بايت فقط تُعتبر في الاستعلام | [V] الوثيقة، و`compareUtf8Strings` في `values.ts:458` | يُقطع المفتاح عند 1500 بايت كما يقطع Firestore |
| bytes | بترتيب البايتات، وأول 1500 بايت | [V] الوثيقة | |
| timestamp | زمني، والدقة حتى microseconds (الزائد يُقطع للأسفل) | [V] الوثيقة | يُقطع عند الكتابة. وKor يرمّز الثواني (بقلب بت الإشارة) ثم nanos |
| reference و`__name__` | جزءًا جزءًا (segment). الأجزاء من الشكل `__id<n>__` تأتي أولًا ومرتبة عدديًا | [V] `path.ts:178-219`، `hidane order.rs:23-24` | **Kor لا يعالج `__id<n>__`**: لا يرد `__id` في `kor/internal` [V بالبحث] |
| geopoint | latitude ثم longitude | [V] الوثيقة | |
| array | عنصرًا عنصرًا، ثم الأقصر أولًا | [V] الوثيقة | علامة نهاية أصغر من علامة العنصر |
| map | المفتاح ثم القيمة، بترتيب المفاتيح، ثم الحجم | [V] الوثيقة | مفاتيح الـ map مرتبة ببايتات UTF-8 (`kor compare.go:218-226`) |
| vector | البُعد أولًا، ثم العناصر | [V] الوثيقة. الحد الأقصى للبُعد 2048 | |

ملاحظات على دلالة الاستعلام تؤثر في حدود المسح (اكتشفها Kor بالمقارنة مع المحاكي، `docs/differential-testing.md:57-93`) [V]:
- **المقارنة داخل النوع فقط.** `b <= "z"` لا يطابق الأرقام، فنطاق المسح يُحصر داخل بايت النوع: `[tag, PrefixEnd(tag))` (`sortkey.go:246-267`).
- **`!=` و`not-in`** يستبعدان null والحقل الغائب فقط، ولا يستبعدان NaN.
- **`== NaN` يطابق NaN، لكن `in [NaN]` لا يطابق شيئًا.**
- **`in [null, x]` لا يطابق null.** و`array-contains-any` يتجاهل null وNaN في القائمة.

## 2. ترميزات المفاتيح القابلة للترتيب بالبايتات

### 2.1 OrderedCode عند Google: الأصل
- **[V]** مكتبة الويب تنقل منطق الخادم، والتعليق يقول «These constants are taken from the backend» (`ordered_code_writer.ts:20-31`).
- **[V] لكن كاتب الفهرس نسخة معدّلة لا مطابقة:** «This file is copied from the backend. Code that is not used by Firestore was removed. Code that has different behavior was modified» (`firestore_index_value_writer.ts:50-51`). الفروق المعروفة عن الخادم:

  | الفرق | المصدر |
  |---|---|
  | **لا قطع:** «the SDK does not implement truncation»، وعلامة `NOT_TRUNCATED` تُكتب دائمًا | `:316-321` |
  | الأعداد الصحيحة تُكتب كـ double عبر `writeNumber` | `:113-115` |
  | المرجع يتجاوز أول خمسة أجزاء، لأن المكتبة لا تقبل إلا قاعدتها | `:53-55` |
  | BSON timestamp يُرمَّز «differently than the backend» لحدود `number` في JS | `:326-330` |
  | فيه تسميات أنواع BSON (`MIN_KEY` و`BSON_TIMESTAMP` و`BSON_BINARY` و`BSON_OBJECT_ID` و`REGEX` و`MAX_KEY`)، وهي حسب [data-types](https://firebase.google.com/docs/firestore/manage-data/data-types) «Supported only in Enterprise edition» | `:57-75` |

  - **[I]** فنأخذ منه تسميات الأنواع وترتيبها وبنية العلامات، ونأخذ الأرقام والقطع من الخادم كما تصفهما الوثائق (§1). ولا نعدّه مواصفة حرفية.
  - **[U] سؤال للإنتاج (F0-1):** كيف يعامل Standard edition خريطة شكلها شكل نوع BSON، مثل `{"__oid__": "..."}` (`detectMapRepresentation`)؟ هل يرتّبها كـ map عادية أم يرفضها؟ مسبار في `aruma-ref` يحسم ذلك.
  - داخل النص: `0x00` يصبح `0x00 0xFF`، والفاصل `0x00 0x01`.
  - و`0xFF` يصبح `0xFF 0x00`، و«ما لا نهاية» `0xFF 0xFF`.
- **[V]** النسخة المفتوحة في Go، `google/orderedcode` (Apache-2.0):
  - النص بالقاعدة نفسها (`orderedcode.go:210-215`).
  - double: البتات كما هي للموجب، وتُقلب كلها عدا الإشارة للسالب، و‎-0 يُرمَّز كـ +0 (`:246-249`).
  - int64 بطول متغير (`:259-263`).
  - الترتيب التنازلي بغلاف `Decr`، أي بعكس البايتات (`:111-118`).
- **[V]** كاتب الفهرس في مكتبة الويب، `firestore_index_value_writer.ts`:
  - تسميات الأنواع: `NULL=5, BOOLEAN=10, NAN=13, NUMBER=15, TIMESTAMP=20, STRING=25, BLOB=30, REFERENCE=37, GEOPOINT=45, ARRAY=50, VECTOR=53, MAP=55`، ومعها `REFERENCE_SEGMENT=60` (`:55-75`).
  - علامة «لم يُقطع» `NOT_TRUNCATED=2`، يجب أن تكون أصغر من كل تسميات الأنواع (`:77-79`).
  - تعليق يشير إلى `go/firestore-storage-format#encodings` (`:99-101`).
  - ‎-0 يُكتب 0 (`:207-214`).
  - المرجع يُكتب بأجزائه بعد تجاوز أول خمسة أجزاء (`projects/p/databases/d/documents`) (`:291-305`).
- **[I]** نستنتج أن الخادم **يقطع** القيم الطويلة ويُعلِّم القطع داخل المفتاح، وهذا يفسر نص الحدود: «Field values over 1500 bytes are truncated. Queries involving truncated field values may return inconsistent results» ([quotas](https://firebase.google.com/docs/firestore/quotas)) [V].
- **تحذير [V]:** مكتبة الويب تكتب الأعداد الصحيحة كـ double (`:113-115`)، فتفقد الدقة فوق 2^53. هذا مقبول في فهرس محلي على العميل، ولا يصح عندنا: الخادم يقارن بالقيمة الدقيقة (§1).

### 2.2 Kor: `internal/value/sortkey.go` @376abea
- **[V] التخطيط** (`:16-28`):
  - بايت نوع، مع فراغات بين الأنواع لما يُضاف لاحقًا.
  - النصوص والبايتات: `0x00` يصبح `{0x00 0xFF}`، والنهاية `{0x00 0x01}`.
  - المركّبات (array وmap وreference وvector): كل عنصر `{0x01 <item>}`، والنهاية `{0x00}`.
  - التنازلي: XOR 0xFF على الترميز كله، **ومعه بايت النوع**. هذا يعكس ترتيب الأنواع أيضًا، وهو المطلوب في Firestore لأن التنازلي يعكس كل شيء.
- **[V] الأرقام** (`:180-226`): فئة (‎-Inf، سالب، صفر، موجب، ‎+Inf)، ثم أس مُزاح بـ 1074 على بايتين، ثم مانتيسا 64 بت. الرقم السالب يُقلب. هكذا يتسع int64 كاملًا (63 بتًا) دون تقريب، ويتساوى `1` و`1.0`.
- **[V] الأسماء** (`:145-165`): جزءًا جزءًا، دون معالجة الأرقام `__id<n>__`. هذه **فجوة** أمام `path.ts:178-219` [V].
- **[I] شرط صحة العكس:** يصح عكس البايتات للتنازلي فقط إن كان الترميز خاليًا من البادئات (prefix-free)، أي لا يكون ترميز قيمة بادئة لترميز أخرى.
  - مثال الخطأ دون علامة نهاية: "a" (‏`61`) و"ab" (‏`61 62`). بعد العكس يصبحان `9E` و`9E 9D`، فيبقى الأقصر أصغر. والصحيح في الترتيب التنازلي أن يكون أكبر.
  - مع علامة النهاية: `61 00 01` و`61 62 00 01` يصبحان `9E FF FE` و`9E 9D FF FE`، والترتيب صحيح.

### 2.3 FoundationDB tuple layer
- **[V]** المصدر: [design/tuple.md](https://github.com/apple/foundationdb/blob/main/design/tuple.md).
  - null هو `0x00`. bytes ‏`0x01` وstring ‏`0x02`، وفي كليهما `0x00` يصبح `0x00 0xFF` ثم النهاية `0x00`. tuple متداخل ‏`0x05 … 0x00`.
  - الأعداد الصحيحة من `0x0c` إلى `0x1c` حسب الطول، و`0x14` هو الصفر، والسالب بمتمم الواحد.
  - double ‏`0x21`: قلب بت الإشارة للموجب، وقلب الكل للسالب.
- **[V] لماذا لا يصلح كما هو:**
  - عنده ‎-0.0 < 0.0، وNaN الموجب والسالب في طرفين متقابلين.
  - int وdouble نوعان منفصلان.
  - لا ترتيب تنازلي في المواصفة.
  - كل هذا يخالف Firestore (§1).
- **[I] ما نأخذه منه:** فكرة الطول المتغير للأعداد الصحيحة، وتكرار الـ escape.

### 2.4 CockroachDB: `pkg/util/encoding/encoding.go` @d30c905
- **[V]** المهرّبات `escape=0x00, escapedTerm=0x01, escaped00=0xff, escapedFF=0x00`، والتنازلي يستعمل `^` لكل منها (`:617-634`).
- **[V]** `EncodeBytesDescending` يرمّز تصاعديًا، ثم **يستبدل بايت العلامة بعلامة تنازلية منفصلة (`bytesDescMarker`)**، ثم يعكس الحمولة فقط (`:705-715`).
- **[I]** الفرق عن Kor مقصود: عمود CRDB له نوع واحد، فلا يحتاج عكس ترتيب الأنواع. Firestore يحتاجه لأن الحقل الواحد قد يحمل أنواعًا مختلفة، فطريقة Kor (عكس بايت النوع أيضًا) هي الصحيحة لنا.

### 2.5 Spanner
- **[U]** ترميز مفاتيح Spanner الداخلي غير منشور. الورقة تصف بيانات مرتبة بالمفتاح الأساسي، ومفاتيح DESC معلنة في المخطط.
- بنية Firestore فوقه (جدولا Entities وIndexEntries) في المسار A. هنا نأخذ منها ما تؤكده وثائق الحجم عن تركيب إدخال الفهرس (§4.1).

### 2.6 hidane: `crates/hidane-core/src/key.rs` @abd4664
- **[V] الوعد في رأس الملف:** `order::compare(a, b) == encode_value(a).cmp(&encode_value(b))`.
- القيم المتساوية تُرمَّز ببايتات متطابقة: `1` و`1.0`، و‎-0.0 و0، وNaN مع NaN، و`{a:1}` و`{a:1.0}`. فالمساواة بحث واحد، والمدى مسح واحد.
- الـ map تنتهي بـ `0x00 0x00`.
- مفتاح مجموعة الـ collection-group هو `collection id ++ path`، فكل مجموعة نطاق واحد (ADR 0002).

### 2.7 خلاصة التصميم [I]
- نكتب مُرمِّزًا واحدًا في Go: Kor في بنيته، وhidane في الأرقام والأسماء.
  - **من Kor:** بايت النوع، والـ escape، وعلامات العناصر، وعكس الكل للتنازلي.
  - **من hidane / مكتبة الويب:** ترتيب `__id<n>__` الرقمي.
  - **الأرقام:** أحد الحلين المجرّبين. حل Kor أطول بايتين، لكنه لا يحتاج حساب «أقرب double».
  - **القطع:** يُقطع عند 1500 بايت مع علامة.
- **مولّد fuzz** يتحقق من `Compare(a,b) == bytes.Compare(Enc(a),Enc(b))`:
  - ومن الشيء نفسه للتنازلي ولمفاتيح مركّبة متسلسلة.
  - ويغطي الحالات الحرجة: 2^53±1، و2^63، و‎-0، وNaN، و`__id-5__`، والنصوص فيها U+0000، والمصفوفة الفارغة مقابل الغائبة.

## 3. خصائص Postgres المؤثرة

### 3.1 `bytea` أم `text COLLATE "C"`
- **[V]** `byteacmp` يقارن بـ `memcmp` على الطول الأقصر ثم بالطول (`varlena.c:4069-4087`). وفي PG18 لـ `bytea` دعم فرز بمفاتيح مختصرة، عبر `varstr_sortsupport` بترتيب "C" (`varlena.c:4129-4140`). هذا يفيد الفرز في الدمج وفي CREATE INDEX.
- **[V] قيود `text`:** لا يحمل U+0000. ونحن نعرف ذلك من RTDB: «jsonb text cannot hold U+0000» (`internal/store/migrations/rtdb/00001_init.sql`)، فلا يصلح لمفتاح فيه `0x00`.
- **[I] ما نستنتجه:**
  - المفاتيح `bytea` دائمًا.
  - `COLLATE "C"` للأسماء النصية فقط إن احتجناها، كما في Kor `0001_init.sql:1-2`.
  - **جسم المستند: خياران، والقرار بقياس (F0-2)، والثقة متوسطة:**
    - **jsonb الخام لا يصلح:** يُسقط ‎-0.0، ولا يحمل U+0000 (رأينا الاثنين في RTDB)، ولا يميّز int64 عن double.
    - **(أ) jsonb معلَّم بالأنواع، أسلوب Kor:** مثل `{"i":"-42"}`. يحفظ ‎-0.0 وNaN وint64، لأن الأرقام نصوص، ويبقى قابلًا للقراءة بـ SQL في اللوحة وأدوات التشغيل. ويحتاج حلًا لـ U+0000، كترميز RTDB الحالي.
    - **(ب) protobuf في `bytea`، أسلوب hidane وGoogle** [P §IV-D1]: الأدق والأصغر غالبًا، لكنه معتم لـ SQL، فكل قراءة في اللوحة أو الأدوات تمر بفك الترميز في Go.
    - هذا قرار تخزين يمس المسار A أيضًا.

### 3.2 حدود حجم الإدخال
| الحد | القيمة | المصدر |
|---|---|---|
| إدخال B-tree في Postgres | نحو ثلث الصفحة: 2704 بايت لصفحة 8 KB «بعد ضغط TOAST إن وُجد» | [V] `nbtree.h:157-173`، [btree docs](https://www.postgresql.org/docs/18/btree.html) |
| ضغط قيمة داخل الفهرس | كل قيمة varlena أكبر من `TOAST_INDEX_TARGET = MaxHeapTupleSize/16` (نحو 510 بايت) وتخزينها EXTENDED أو MAIN تُضغط داخل الفهرس | [V] `indextuple.c:117-127`، `heaptoast.h:64-68` |
| إدخال فهرس في Firestore | 7.5 KiB، ومجموع إدخالات المستند 8 MiB، و40,000 إدخال لكل مستند | [V] [quotas](https://firebase.google.com/docs/firestore/quotas) |
| قيمة مفهرسة | 1500 بايت، وما زاد يُقطع | [V] quotas |
| اسم المستند / المعرّف / مسار الحقل | 6 KiB / 1500 بايت / 1500 بايت | [V] quotas |

- **[I] النتيجة:**
  - فهرس مركّب على نصين طولهما 1500 بايت مع معرّف طوله 1500 بايت يتجاوز 2704، فيفشل الإدراج إن لم يُضغط. وFirestore يقبله.
  - المفاتيح فوق 510 بايت تُضغط، فتُفك عند كل مقارنة في البحث.
- **الخيارات، وثلاثة منها لا تصلح:**
  - (أ) ~~قطع المفتاح كله عند حد آمن ثم إعادة فحص المستند.~~ **لا يصلح:**
    - يكسر تفرّد المفتاح الأساسي، وكسر التعادل بـ `__name__` الذي يأتي في ذيل المفتاح.
    - مفاتيح نطاق `COLLECTION_GROUP` تحمل الاسم الكامل، وطوله حتى 6 KiB.
  - (ب) ~~رفض الكتابة.~~ **لا يصلح:** أكثر تشددًا من Firebase في مستند قانوني.
  - (ج) ~~`SET STORAGE PLAIN`~~ **ضار:** يمنع الضغط داخل الفهرس (`indextuple.c:120-123`)، فيصل المفتاح الطويل إلى حد 2704 بايت أسرع. والضغط هو ما يجعل بعض المفاتيح الطويلة تتسع اليوم.
  - **(د) المقترح: مفتاح محدود الطول.**
    - المفتاح بادئة حافظة للترتيب بطول محدود، ثم digest للجزء الطويل، ثم إعادة فحص كل نتيجة على المستند نفسه بالمقارن الحقيقي.
    - هذا نهج Firenook (المسار B §5): مفتاح «exact» للقيم القصيرة، وبادئة مع SHA-256 لما يطول، وإعادة ترتيب كل مجموعة متساوية البادئة بالمقارن.
    - **[I]** يحفظ التفرد لأن الـ digest يميّز الذيل، ويحفظ الترتيب حتى حد البادئة. والمستندات ذات البادئة المشتركة تُرتّب بعد القراءة. وهي حالات نادرة ومحدودة.
- **التجربة F0-3 بأسوأ مستند قانوني:**
  - معرّف 1500 بايت، ومسار مجموعة أم 1500 بايت، وقيمة 1500 بايت، في فهرس مركّب من حقلين.
  - واسم 6 KiB في نطاق `COLLECTION_GROUP`.
  - وبايتات عشوائية غير قابلة للضغط.
  - نقيس: هل يُرفض الإدراج دون (د)؟ وما كلفة (د) في الكتابة والقراءة؟

### 3.3 Deduplication وbottom-up deletion والضغط
- **[V]** deduplication (من PG13) يدمج المفاتيح المكررة في posting list. لا يُستعمل مع `numeric` و`jsonb` و`float8` والمركّبات و`INCLUDE`، وبايتات `bytea` تقبله.
  - في الفهرس الفريد (مثل PK على `(…, key)`) يمتص «version churn» فقط، أي النسخ القديمة للصف نفسه.
  - bottom-up deletion يحذف نسخ churn قبل انقسام الصفحة ([btree docs](https://www.postgresql.org/docs/18/btree.html)).
- **[I]** مفاتيحنا فريدة لأنها تنتهي باسم المستند، فلا ربح من dedup. لكن bottom-up deletion مهم لأن إدخالات الفهرس تُحذف وتُدرج باستمرار.
- **[V/I]** B-tree في Postgres لا يضغط البادئات المشتركة في الأوراق. الوثيقة لا تذكر ضغط البادئة، وما فيه هو suffix truncation للمفاتيح الوسيطة فقط [I]. فالبادئة الطويلة (مسار المجموعة الأم) تتكرر في كل إدخال. والحل: بادئة قصيرة برقم (`collection_pk`) بدل المسار الكامل (§4.1).
- **[V]** وثيقة Firestore تحذر: «A query may have to skip over a large number of index entries if the early query results have recently been deleted» ([best practices](https://firebase.google.com/docs/firestore/best-practices)). المقابل عندنا [I]: إدخالات ميتة في B-tree حتى VACUUM أو `kill_prior_tuple`. فالطوابير (queue-like) على Firestore تتعبنا بالطريقة نفسها.

### 3.4 Index-only scan وخريطة الرؤية
- **[V]** index-only scan يحتاج أن تكون صفحة الـ heap «all-visible» في visibility map، وإلا يزور الـ heap ([docs](https://www.postgresql.org/docs/18/indexes-index-only-scans.html)).
- **[I] أثر ذلك علينا:**
  - جدول إدخالات بكتابات كثيفة يحتاج autovacuum نشطًا (إعدادات لكل جدول) حتى يبقى المسح index-only.
  - `count()` من Firestore («يُحسب قراءة لكل 1000 إدخال فهرس»، [aggregation](https://firebase.google.com/docs/firestore/query-data/aggregation-queries) [V]) يصبح مسح نطاق في الفهرس وحده إن بقيت الخريطة نظيفة.

### 3.5 HOT updates
- **[I]** إن لم يكن على جدول المستندات إلا فهرس المفتاح الأساسي، يصبح تحديث جسم المستند HOT ما دام في الصفحة متسع. فلا يلمس أي فهرس ([HOT](https://www.postgresql.org/docs/18/storage-hot.html)).
- GIN على `data`، كما في Kor، يمنع HOT في كل تحديث للبيانات. حجة إضافية لنقل الفهرسة إلى جدول منفصل.

### 3.6 RLS: قرار مفتوح
**أدلة موثقة:**
- **[V] الحماية لا تمنع الفهرس:** شرط السياسة يُضاف قبل شروط المستخدم، ويمنع Postgres استعمال شرط غير leakproof شرطَ فهرس قبل شروط RLS. المصدر:
  - `indxpath.c:2604-2608`: `if (!restriction_is_securely_promotable(rinfo, index->rel)) return;`.
  - `restrictinfo.c:415-434`: يُقبل الشرط إن كان مستوى أمانه لا يسبق شروطًا أدنى، أو كان leakproof.
- **[V] من هو leakproof:**
  - leakproof: `byteaeq`، `bytealt`، `byteage`، `byteacmp`، `texteq`، `text_lt`، `int8eq` (`pg_proc.dat:6197-6216, 167, 1573, 1380`).
  - ليس leakproof: `jsonb_contains` (`:10413`).
- **[I] النتيجة:**
  - مع RLS ودور لا يملك BYPASSRLS، يُستعمل فهرس bytea على `(database_id, index_id, key)` كاملًا.
  - ولا يُستعمل GIN على jsonb لعامل `@>`.
  - ملاحظة جانبية: مالك الجدول يتجاوز RLS ما لم يُضبط `FORCE ROW LEVEL SECURITY`.

**كلفة RLS المتوقعة [I]، وتقاس في F0:**
- **ضبط المشروع في كل معاملة:** `set_config('aruma.project', $1, true)` داخل المعاملة. عبر pgx هذا ذهاب وإياب إضافي، إلا إذا أُرسل في batch/pipeline مع أول استعلام.
- **الشرط الإضافي:** يصبح شبه مجاني إذا كان عمود العزل هو أول عمود في المفتاح.
- **الخطط المحفوظة:** `current_setting` دالة stable، فتصلح شرط فهرس في خطة عامة (generic plan).
- **التنظيف والكاتب الواحد:** يحتاجان دورًا يتجاوز RLS. هذا ما ذكره ADR-017 من كلفة.

**البديل دون RLS [I]:**
- **مُنشئ SQL واحد** في Go، لا يُخرج استعلامًا على جداول Firestore إلا وأول شرط فيه `database_id = $1`، مع اختبار يمنع غيره من بناء SQL (archtest أو scopecheck موسّع).
- **عمود العزل أول عمود في كل مفتاح أساسي.** بهذا يصبح شرط العزل جزءًا من حد المسح نفسه، لا فلترًا إضافيًا.
- **حارس وقت التشغيل:** يرفض أي خطة لا تقيّد بـ `database_id`.
- **اختبارات عزل** كما في `test/acceptance/isolation_test.go`.

**الأثر على المخطط:** المخطط في §6 يصلح للحالتين: عمود العزل أول عمود دائمًا، و`project_number` موجود حيث يطلبه scopecheck.

### 3.7 التقسيم حسب المشروع (partitioning)
- **[I]** نؤجله. البادئة الأولى في المفتاح تعطي «تقسيمًا منطقيًا» مجانًا داخل الـ B-tree.
- partitioning بالـ hash على `database_id` يفيد عند VACUUM والحذف الجماعي لمشروع كامل (`DROP`/`DETACH` بدل `DELETE`)، لكنه يعقّد الخطط.
- يُقاس فقط إن ظهر أن حذف مشروع كبير أو vacuum يؤذي الآخرين.

### 3.8 النقاط الساخنة
- **[V]** Firestore يحد الكتابة إلى مجموعة فيها حقل مفهرس متزايد تسلسليًا بـ 500 كتابة في الثانية ([best practices](https://firebase.google.com/docs/firestore/best-practices)). سببه تقسيم Spanner بالمدى.
- **[I]** B-tree في عقدة واحدة يحب الإدراج المتزايد (آخر صفحة يمين). فلا نحتاج محاكاة هذا الحد، ولا ينطبق علينا [U] بدون قياس.

## 4. صيانة الفهارس داخل المعاملة

### 4.1 ما تكشفه وثائق Google عن تركيب الإدخال [V]
من [storage-size](https://firebase.google.com/docs/firestore/storage-size):

| نوع الإدخال | مكوّناته |
|---|---|
| single-field، نطاق collection | اسم المستند + **اسم المستند الأب** + اسم الحقل + القيمة + 32 بايت |
| single-field، نطاق collection group | اسم المستند + اسم الحقل + القيمة + 48 بايت (بلا الأب) |
| composite، نطاق collection | اسم المستند + **الأب** + القيم + 32 بايت |
| composite، نطاق collection group | اسم المستند + القيم + 32 بايت |

**[I] ما نقرؤه من هذا:**
- المفتاح على نطاق المجموعة يبدأ بالأب، فاستعلام `users/A/posts` نطاق واحد.
- مفتاح المجموعة الأحادي يحمل اسم الحقل، أي أن كل الحقول الأحادية فهرس منطقي واحد، بادئته `(الأب، الحقل)`.
- مفتاح collection-group لا يحمل الأب. والـ collection id جزء من اسم المستند، وهو في الغالب أول البادئة.

### 4.2 الفهارس التلقائية والاستثناءات [V]
- **ما يُنشأ تلقائيًا** ([index-overview](https://firebase.google.com/docs/firestore/query-data/index-overview)):
  - لكل حقل غير مصفوفة وغير map: فهرسان على نطاق collection، تصاعدي وتنازلي.
  - الـ map: كل حقل فرعي، تكراريًا، وقيمة الـ map كاملة.
  - المصفوفة: تصاعدي وتنازلي على المصفوفة كاملة، وفهرس array-contains.
  - «Automatic indexes with collection group scope are not maintained by default».
- **الاستثناءات:** تُلغي الفهرسة التلقائية لحقل، و`*` يلغيها لكل حقول مجموعة. الحد 200 تهيئة أحادية بلا فوترة و1000 بها، و200 / 1000 فهرس مركّب، و100 حقل في الفهرس المركّب ([quotas](https://firebase.google.com/docs/firestore/quotas)).
- **مثال الوثيقة:** المستند `cities/SF` يولّد 13 إدخالًا تلقائيًا، و19 مع فهرسين يدويين.
- **حالات الاستثناء الموصى بها:** النصوص الطويلة، والقيم المتزايدة التي تحد الكتابة بـ 500/ث، وحقول TTL، والمصفوفات والـ maps الكبيرة التي «can approach the limit of 40,000 index entries per document» ([best practices](https://firebase.google.com/docs/firestore/best-practices)).
- **[U]** حد «20,000 حقل لكل مستند» الوارد في طلب البحث لم أجده في صفحة quotas الحالية. الموجود: 40,000 إدخال، وعمق 20 للـ map/array.

### 4.3 جدول إدخالات أم فهارس Postgres الأصلية
| المعيار | جدول `index_entries` (Kor، Spanner IndexEntries) | GIN ‏`jsonb_path_ops` على المستند | فهرس تعبيري B-tree لكل (مجموعة، حقل) |
|---|---|---|---|
| ترتيب Firestore المختلط | نعم، بالمفتاح | لا ترتيب أصلًا | لا: `ORDER BY data->'x'` يتبع ترتيب jsonb (Kor `docs/indexes.md:24-28` [V]) |
| مدى وlimit رخيص | مدى واحد، والكلفة بقدر limit (Kor `indexes.md:18-22` [V]) | يعطي مرشحين فقط، ثم الفرز في Go بكلفة O(المطابقات) (`store/query.go:145-198` [V]) | نعم لنوع واحد |
| كلفة الكتابة | إدخال لكل (فهرس، مستند). المصفوفة في array-contains إدخال لكل عنصر مميز | قياس Kor الاصطناعي على مستندات متداخلة بشكل TMDB: 65.0 kB WAL لكل مستند مع GIN، و6.5 kB بدونه (90%)، وأغلبه full-page images. وحادثة الإنتاج (~1.5 MB لكل مستند 21 kB) نُسبت إلى GIN بالاستنتاج من هذا القياس (commit `62bffa8`، `walcost_test.go:13-20`، `0003_containment_exclusions.sql:3-10` [V]). يُعاد القياس على أشكالنا في F0-2 | كل فهرس يُحدَّث مع كل تحديث غير HOT. وDDL ديناميكي لكل مستأجر |
| مع RLS | قابل للاستعمال لأن مقارنات bytea leakproof [V] | غير قابل لأن `@>` ليس leakproof [V] | يعتمد على الدالة |
| الفهرس الجزئي | لا يلزم | Kor وجد أن الفهرس الجزئي لا يُستعمل مع معامل مربوط في خطة عامة (`partialgin_test.go:13-20` [V]) | المشكلة نفسها |
| الاستثناءات | سهلة: لا نكتب إدخالات للحقل | على مستوى المجموعة فقط في Kor | — |

- **[I] التوصية:** جدول `index_entries` للفهارس كلها، الأحادية والمركّبة. ولا GIN.
- **[V] الفرق عن Kor:** Kor يحقق الفهارس الأحادية بـ GIN ومسار عام في Go، والمركّبة فقط بجدول. لذلك يبقى استعلام بلا فهرس مركّب عنده بكلفة O(المجموعة).

### 4.4 حذف الكل وإعادته أم التحديث بالفرق
- **[V] Kor:** يحذف كل إدخالات المستند ثم يدرجها (`DELETE … WHERE doc_name = $1`، `store/index.go:112`). يحتاج لذلك فهرسًا ثانويًا على `doc_name` يُحدَّث في كل كتابة (`0002_index_entries.sql:23-25`).
  - تبريره: «getting that wrong strands a stale entry» (`indexes.md:154-158`).
- **[I] البديل:**
  - الكاتب الواحد (ADR-019، وADR-026 المقترح) يقرأ المستند القديم أصلًا لحساب قبل/بعد للبث.
  - فنحسب `old_keys` و`new_keys` في Go، ونحذف `old−new` بالمفتاح الكامل، وندرج `new−old`.
  - بهذا يسقط الفهرس الثانوي على اسم المستند. وتحديث حقل واحد في مستند بمئة حقل يلمس إدخالين لا مئتين.
- **الخطر:** تغيّر الترميز بين إصدارين يترك إدخالات يتيمة. يعالجه:
  - رقم إصدار للترميز في `index_defs`، وإعادة بناء عند تغيّره.
  - فاحص دوري يعيد حساب مفاتيح عينة من المستندات ويقارنها.
  - ومعه ما يفعله Kor: إعادة فحص كل مستند مُعاد ضد الاستعلام، وإرجاع خطأ إن لم يطابق (`indexes.md:160-165`، وقد كشف خللًا حقيقيًا [V]).

### 4.5 الجاهزية والملء الخلفي
- **[V] عند Kor:** التسجيل غير التفعيل (`ready_at`)، والفهرس غير المملوء لا يخدم شيئًا، «silently returns fewer documents» (`0002_index_entries.sql:37-41`، `indexes.md:57-81`).
- **[I] عندنا:** نفس الفكرة، مع حالات Firestore الإدارية (`CREATING` / `READY`) في واجهة `firestore.googleapis.com/v1/.../indexes`.
  - firebase-tools يحتاج هذه الواجهة لـ `deploy --only firestore:indexes`، وهي من نطاق المسار B/F7.

### 4.6 تضخم الإدخالات
- **[I]** حجم الإدخال عندنا: صف heap (‏≈24 رأس + 4 ItemId + الأعمدة) + إدخال B-tree (‏≈8 + 4 + الأعمدة). المجموع نحو 2 إلى 2.5 ضعف حجم المفتاح، أي 150 إلى 250 بايت لإدخال قصير. Postgres لا يملك جدولًا منظَّمًا بالفهرس (index-organized).
- **مستند بعشرين حقلًا بسيطًا** = 20 إدخالًا أحاديًا (اتجاه واحد عندنا، مقابل 40 في Firestore) = 3 إلى 5 KB إضافية مع WAL. يُقاس في F0.
- **مصفوفة من 1000 عنصر مميز:**
  - عندنا: 1000 إدخال array-contains + إدخال المصفوفة كاملة.
  - في Firestore: إدخالا asc/desc للمصفوفة كاملة + 1000 إدخال array-contains.
  - الحد 40,000 يجب أن نطبقه بالعدّ نفسه الذي يستعمله Firestore، لا بعددنا الأصغر. وإلا قبلنا ما يرفضه [I].

## 5. أنماط الاستعلام وتكلفتها

| النمط | الخطة في جدول الإدخالات [I] | ملاحظات |
|---|---|---|
| مساواة على حقل + ترتيب `__name__` | مدى واحد في الفهرس الأحادي: `prefix=(coll, field, value)` | ترتيب `__name__` داخل البادئة تلقائي |
| مساواة على عدة حقول بلا فهرس مركّب | Firestore يدمج الفهارس الأحادية بـ zig-zag merge join ([blog 2019](https://firebase.blog/posts/2019/08/why-is-my-cloud-firestore-query-slow) [V]). عندنا: قفز متبادل في Go بين مسحين مرتبين بالاسم (`key >= $seek LIMIT n`)، أو `INTERSECT` في SQL | بطيء حين تكون التقاطعات نادرة، وهذا ضعف معروف عند Google نفسها [V] |
| inequality على حقل + مساواة | يحتاج فهرسًا مركّبًا في Firestore. مدى واحد: بادئة المساواة + حدود المتباينة، مقصورة على بايت النوع | تحذير Kor: المتباينة تتبع اتجاه الحقل في الفهرس، والمؤشر يتبع اتجاه الاستعلام (`indexes.md:132-144` [V]) |
| inequality على عدة حقول | Firestore يطلب فهرسًا فيه كل حقول المتباينات. مدى على الأولى، وفلترة البقية من **المفتاح نفسه** دون جلب المستند | يحتاج مفتاحًا قابلًا للفك (decode) |
| `in` (حتى 30) / `array-contains-any` (حتى 30) | عدة مدى + دمج بترتيب الاستعلام. any مع إزالة التكرار | Kor: يقارن لاحقة الترتيب لا المفتاح كله، لأن البادئات تختلف طولًا (`indexes.md:93-122` [V]). وany لا يستعمل offset+limit المسبق |
| `!=` و`not-in` (حتى 10) | مديان (أو 11) حول القيمة، بعد بايت null | يشمل الأنواع الأخرى وNaN، ويستبعد null والغائب [V] |
| `OR` | DNF بحد 30 فرعًا، وكل فرع خطة، ثم دمج مع إزالة التكرار | [V] hidane: الإنتاج يعدّ ‎-0.0 و0.0 قيمتين عند حساب الفروع ("Result had 33 disjunctions"، `parity-exceptions.md:160`) |
| المؤشرات startAt/After وendAt/Before | ترميز قيم المؤشر بنفس اتجاهات الحقول. `startAfter(كامل)` يصبح `key > enc`. والمؤشر الجزئي يستعمل `PrefixEnd(enc)` | Kor: `PrefixEnd` بحمل عند `0xFF` (`index/index.go:213-226` [V]) |
| `offset` | `OFFSET` في SQL يمسح الإدخالات ويتجاوزها | Firestore يُحسب القراءات المتجاوزة أيضًا [V best practices] |
| collection group | فهرس بنطاق collection-group، بادئته collection id، والذيل الاسم الكامل جزءًا جزءًا | [V] hidane: الإنتاج يقبل collection-group تحت مستند حسب الـ API، والمحاكي يرفض ما ليس `__name__` تصاعديًا (`parity-exceptions.md:149`) |
| `count` | `count(*)` على مدى الإدخالات، index-only إن كانت الخريطة نظيفة | Firestore: الكلفة بقدر الإدخالات الممسوحة، وحد 60 ث ثم `DEADLINE_EXCEEDED` [V] |
| `sum` / `avg` | «Aggregation queries read only indexed fields» [V]. القيمة في المفتاح نفسه | تحذير [I]: مفتاح hidane يساوي `1` بـ `1.0`، ومفتاح Kor كذلك. وsum في Firestore يعيد integer إن كانت كل القيم صحيحة. فإما بايت «نوع أصلي» بعد الترميز لا يدخل المقارنة، أو جلب المستند. يُقرَّر في F0 |
| ترتيب `__name__` تنازلي وحده | الإنتاج يطلب فهرسًا مركّبًا `__name__ DESCENDING`، و«Firestore does not support descending key scans» عند المحاكي | [V] `hidane parity-exceptions.md:140` |

## 6. الاستعلام بلا فهرس: الإنتاج مقابل المحاكي
- **[V] الإنتاج:** يعيد `FAILED_PRECONDITION` ومعه رابط لإنشاء الفهرس ([index-overview](https://firebase.google.com/docs/firestore/query-data/index-overview)، [Datastore errors](https://docs.cloud.google.com/datastore/docs/concepts/errors)).
- **[V] المحاكي:** «does not track compound indexes and instead will execute any valid query» ([connect_firestore](https://firebase.google.com/docs/emulator-suite/connect_firestore)).
- **[V] hidane:** يتبع المحاكي، ووثّق ذلك فرقًا مقصودًا. واستعلامات اختباراته المسجّلة احتاجت في الإنتاج 47 تعريفًا (`tools/production/indexes.json`):
  - فهارس بـ `__name__ DESCENDING` وحده.
  - فهارس مساواة على أكثر من 40 حقلًا.
  - فهارس أحادية لـ `COLLECTION_GROUP` عبر `fieldOverrides`.
  - فهارس vector.
- **[I] توصية لـ ADR-027:**
  - **وضع الإنتاج** يرفض كما يرفض Firestore. القاعدة: «لا نكون أكثر تساهلًا من Firebase». والتطبيق الذي يعمل على Aruma يجب أن يعمل إذا عاد إلى Firebase.
  - **وضع تطوير** صريح يخدم كل شيء، مثل `dev_open_rules`، ومعه تحذير في السجل.
  - **قرار «يحتاج فهرسًا»** يتبع قواعد Firestore، لا الخطة الفعلية عندنا. ويُختبر بتسجيلات الإنتاج (Rules Test API لا يكفي هنا: نحتاج تسجيلات استعلامات من المشروع المرجعي).
  - **مسار عام بطيء** يبقى مرجعًا للصحة فقط، كما فعل Kor.

## 7. مخطط مقترح للنقاش [I]

```sql
-- Isolation: database_id leads every key. firestore.databases carries project_number
-- (scopecheck / optional RLS). Named databases come later; '(default)' only in v0.4.
-- Child tables reach the project through database_id with ON DELETE CASCADE; whether they
-- also carry project_number (with the control.projects FK) is an ADR-024 decision, see notes.
CREATE TABLE firestore.databases (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    name           text   NOT NULL DEFAULT '(default)',
    seq            bigint NOT NULL DEFAULT 0,            -- single writer, as ADR-019
    UNIQUE (project_number, name)
);

-- One row per collection instance (users/A/posts): a short prefix for index keys,
-- and ListCollectionIds without a DISTINCT scan (as in Kor's collections table).
CREATE TABLE firestore.collections (
    database_id bigint NOT NULL REFERENCES firestore.databases (id) ON DELETE CASCADE,
    id          bigint GENERATED ALWAYS AS IDENTITY,
    parent      bytea  NOT NULL,   -- segment-ordered encoding of the parent document ('' for root)
    coll_id     text   COLLATE "C" NOT NULL,
    PRIMARY KEY (database_id, parent, coll_id),
    UNIQUE (database_id, id)
);

CREATE TABLE firestore.documents (
    database_id    bigint NOT NULL REFERENCES firestore.databases (id) ON DELETE CASCADE,
    -- only if ADR-024 puts project_number on child tables (scopecheck/RLS); then with the FK:
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    name_key       bytea  NOT NULL,   -- segment-ordered full name: one range per subtree, __id<n>__ numeric
    collection_pk  bigint NOT NULL,
    fields         bytea  NOT NULL,   -- protobuf MapValue, or tagged jsonb: decided by F0-2
    create_time    bigint NOT NULL,   -- microseconds
    update_time    bigint NOT NULL,
    PRIMARY KEY (database_id, name_key)
);

-- Every index, automatic or composite. key layout:
--   collection scope: [collection_pk 8B][index-specific prefix][values…][doc id tail]
--   group scope:      [escaped collection id][values…][full name tail]
-- single-field automatic: one logical index per kind; the prefix holds the escaped field
-- path. Ascending only: a backward scan gives DESC with __name__ DESC.
CREATE TABLE firestore.index_entries (
    database_id bigint NOT NULL REFERENCES firestore.databases (id) ON DELETE CASCADE,
    index_id    bigint NOT NULL,      -- 1 = auto asc, 2 = auto array-contains, ≥100 = defs
    key         bytea  NOT NULL,
    PRIMARY KEY (database_id, index_id, key)
);   -- no doc_name index: maintenance deletes exact keys from old-vs-new diffs

CREATE TABLE firestore.index_defs (
    database_id bigint NOT NULL REFERENCES firestore.databases (id) ON DELETE CASCADE,
    index_id    bigint NOT NULL,
    spec        text   NOT NULL,      -- canonical form from firestore.indexes.json
    scope       text   NOT NULL CHECK (scope IN ('COLLECTION','COLLECTION_GROUP')),
    encoding    int    NOT NULL,      -- key-encoding version; a change forces a rebuild
    state       text   NOT NULL CHECK (state IN ('CREATING','READY','NEEDS_REPAIR')),
    PRIMARY KEY (database_id, index_id)
);
-- plus field_overrides (exemptions, collection-group single-field), and the changelog
-- from ADR-026.
```

**ملاحظات:**
- **اسم المستند في ذيل المفتاح** يكفي لاستخراج `name_key` للجلب: في نطاق المجموعة `collection_pk` ثم المعرّف. لذا لا عمود `doc_name` منفصل. هل يستحق عمود `doc bytea` للوضوح؟ قرار يُقاس.
- **`project_number` في الجداول الفرعية:** يقرره ADR-024 مع قرار RLS (§3.6). خياران:
  - **(أ) الوراثة عبر `database_id`:** الجداول الفرعية تصل إلى المشروع عبر `database_id` بـ `ON DELETE CASCADE`. والـ scope annotation تشرح أن `database_id` لا يُعطى إلا لمشروعه. هذا نمط `rtdb.nodes` اليوم.
  - **(ب) `project_number` في كل جدول:** بالصيغة المعتمدة في `control/00001_init.sql:74` وما بعده: `project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE`. يزيد 8 بايت لكل صف فهرس، لكنه شرط لأي سياسة RLS بسيطة.
- **[V] عدم اتساق قائم يدخل ADR-024:** `rtdb.databases.project_number` بلا FK إلى `control.projects` (`internal/store/migrations/rtdb/00001_init.sql:12`)، بخلاف جداول `control`. فحذف مشروع لا يحذف قواعد RTDB تلقائيًا. نقرر لـ Firestore صراحة ألا نكرر ذلك، ونشير إلى RTDB لإصلاحه في migration جديد.

## 8. تجارب F0 مقترحة
1. **fuzz الترميز:** يتحقق من `Compare == bytes.Compare` تصاعديًا وتنازليًا ومتسلسلًا، ويشمل `__id<n>__` وحدود 2^53 و2^63. ثم **مقارنة الترتيب مع المحاكي** عبر مكتبة Go الرسمية، بأسلوب Kor (`docs/differential-testing.md`). ثم **عينة من الإنتاج** على المشروع المرجعي تحت `aruma-ref`، ومعها مسبار لخرائط بشكل أنواع BSON في Standard edition (§2.1).
2. **WAL والحجم والجسم:** نقيس `pg_current_wal_lsn` قبل وبعد، و`pg_relation_size`.
   - **الفهارس:** ثلاثة خيارات: (أ) `index_entries` بالفرق، و(ب) `index_entries` بحذف الكل وإعادته مع فهرس `doc`، و(ج) GIN على `data`، وهو أساس Kor.
   - **أشكال المستندات:** مسطّح بـ 20 و100 حقل، ومتداخل، وشكل TMDB نفسه الذي قاسه Kor، ومصفوفة من 1000 عنصر.
   - **جسم المستند:** protobuf `bytea` مقابل jsonb معلَّم بالأنواع. نقيس الحجم بعد TOAST، وWAL لكل كتابة، وCPU فك الترميز عند 1 KB و100 KB و1 MiB.
   - **وقائمة بالاستعلامات** التي تحتاج SQL على الأجسام في اللوحة والتشغيل (تصفح، بحث، تصدير، إصلاح). قائمة طويلة ترجّح jsonb المعلَّم.
3. **حدود المفتاح بأسوأ مستند قانوني** (§3.2): معرّف 1500 بايت، ومسار أم 1500 بايت، وقيمة 1500 بايت، واسم 6 KiB في نطاق `COLLECTION_GROUP`، وبايتات غير قابلة للضغط. نقيس الإدراج دون المفتاح المحدود (د) ومعه، وكلفة (د) في الكتابة والقراءة.
4. **index-only scan تحت الكتابة:** نسبة `Heap Fetches` في `EXPLAIN (ANALYZE, BUFFERS)` مع autovacuum افتراضي ثم معدّل.
5. **RLS نعم أم لا:** زمن مسح المدى وكلفة `set_config` لكل معاملة عبر pgx، مع RLS (دور بلا BYPASSRLS) ودونه.
6. **zig-zag:** دمج في Go مقابل `INTERSECT` في SQL لاستعلام مساواة على حقلين بتقاطع نادر.
7. **التجميعات:** `count` على مليون إدخال. وsum/avg من المفتاح مع بايت نوع أصلي، مقابل جلب المستندات.
8. **Offset عميق، ومدى بعد حذف جماعي:** الإدخالات الميتة قبل VACUUM.
9. **قواعد «يحتاج فهرسًا»:** تسجيلات من الإنتاج لكل شكل استعلام (أحادي، دمج، مركّب، `__name__ desc`، collection-group)، ومنها جدول قرار يُختبر.

## المصادر الأساسية
- **وثائق Firebase:**
  - [data-types](https://firebase.google.com/docs/firestore/manage-data/data-types)
  - [quotas](https://firebase.google.com/docs/firestore/quotas)
  - [storage-size](https://firebase.google.com/docs/firestore/storage-size)
  - [index-overview](https://firebase.google.com/docs/firestore/query-data/index-overview)
  - [best-practices](https://firebase.google.com/docs/firestore/best-practices)
  - [aggregation-queries](https://firebase.google.com/docs/firestore/query-data/aggregation-queries)
  - [queries](https://firebase.google.com/docs/firestore/query-data/queries): `in`/`array-contains-any` حتى 30، و`not-in` حتى 10، ولا يجتمع `not-in` و`!=`، و`array-contains` واحد لكل فرع. حد 30 فرعًا في DNF لم أقرأه نصًا في الصفحة، لكنه مؤكد برسالة الإنتاج عند hidane.
  - [connect_firestore](https://firebase.google.com/docs/emulator-suite/connect_firestore)
  - [Firebase blog 2019: why is my query slow](https://firebase.blog/posts/2019/08/why-is-my-cloud-firestore-query-slow)
- **الكود:**
  - Kor @376abea: `internal/value/{sortkey,compare}.go`، `internal/index/{index,plan}.go`، `internal/store/{query,index,containment}.go`، `internal/store/migrations/000{1..4}*.sql`، `docs/{indexes,differential-testing}.md`.
  - hidane @abd4664: `crates/hidane-core/src/{key,order}.rs`، `docs/adr/0002-storage-engine.md`، `docs/parity-exceptions.md`، `tools/production/indexes.json`.
  - firebase-js-sdk @3a5d6a0: `src/index/{ordered_code_writer,firestore_index_value_writer}.ts`، `src/model/{values,path}.ts`.
  - google/orderedcode @9a3d15e. cockroach @d30c905 `pkg/util/encoding/encoding.go`.
  - postgres @9412c35: `varlena.c`، `pg_proc.dat`، `indxpath.c`، `restrictinfo.c`، `nbtree.h`، `indextuple.c`، `heaptoast.h`.
- **وثائق Postgres:** [btree](https://www.postgresql.org/docs/18/btree.html)، [index-only scans](https://www.postgresql.org/docs/18/indexes-index-only-scans.html)، [HOT](https://www.postgresql.org/docs/18/storage-hot.html).
- **FoundationDB:** [tuple.md](https://github.com/apple/foundationdb/blob/main/design/tuple.md).
