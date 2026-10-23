# المسار E: المعاملات في Firestore وكيف نحاكيها فوق Postgres

- **التاريخ:** 2026-10-23. بحث بالقراءة فقط: لم يُشغَّل خادم ولا محاكٍ ولا Postgres.
- **الرموز:** **[V]** متحقَّق منه في المصدر (رابط، أو `repo@sha:path:line`)، **[I]** استنتاج مني، **[U]** غير متحقَّق.
- **المصادر ولقطاتها:**

  | المصدر | المرجع |
  |---|---|
  | firebase-js-sdk | `3a5d6a0` (`packages/firestore/src/...`) |
  | firebase-android-sdk | `ee683fe` |
  | firebase-ios-sdk | `81e7067` |
  | nodejs-firestore (Admin Node) | `1068f5c` |
  | google-cloud-go (Admin Go) | `f41be16` |
  | java-firestore (Admin Java) | `dd16eb0` |
  | google-cloud-python (Admin Python) | `eb8a76628adf10093959600071a18f50285c2ecf` (رأس `main` يوم القراءة. الصيغة المختصرة `eb8a766` لا يحلّها GitHub API في هذا المستودع الضخم، فنكتب الكاملة) |
  | googleapis (`google/firestore/v1`, `admin/v1`) | `cfc42c2` |
  | Kor `omelas-tech/kor` | `376abea` (لقطة tarball) |
  | hidane `hidane-dev/hidane` | `abd4664` (لقطة tarball، Rust: أفكار فقط) |

- **ملاحظة:** Firecrawl بلا رصيد، فاستُعمل WebSearch/WebFetch و`gh api`. مكتبات Admin لا تنفّذ المعاملات بنفسها، بل تفوّضها لمكتبات `google-cloud-firestore` في كل لغة، فقرأنا هذه.

## 0. الخلاصة في عشرة أسطر

1. **[V]** يوجد في Firestore **نوعان مختلفان تمامًا** من المعاملات على السلك:
   - **مكتبات الجوال والويب** لا تستدعي `BeginTransaction` أبدًا. تقرأ بـ `BatchGetDocuments` بلا معاملة، ثم ترسل `Commit` واحدًا بلا `transaction`. فيه شروط `update_time` على كل ما قرأته، وكتابة من نوع `verify` لكل مستند قرأته ولم تكتبه.
   - **مكتبات الخادم** تبدأ معاملة حقيقية لها معرّف: بـ `BeginTransaction` (Go وJava وPython)، أو كسولًا بـ `new_transaction` في أول قراءة (Node). ثم ترسل `Commit` بذلك المعرّف.
2. **[V]** الحقل `Write.verify = 5` **غير موجود** في الـ proto العام (googleapis) ولا في `firestorepb` الخاص بـ Go. لكنه موجود في الـ protos المضمّنة في مكتبات الويب وAndroid وiOS.
   - **النتيجة على Aruma:** إن ولّدنا الكود من `firestorepb` أو googleapis، فسيُسقَط `verify` بصمت. كل معاملة من الجوال قرأت مستندًا دون أن تكتبه ستفشل.
3. **[V]** Kor يقع في هذا الفخ بعينه، فهو مبني على `cloud.google.com/go/firestore v1.25.0`.
4. **[V]** وضع التزامن (concurrency mode) إعداد على مستوى قاعدة البيانات:
   - **Standard edition:** الافتراضي `PESSIMISTIC`.
   - **Enterprise edition:** الافتراضي `OPTIMISTIC`.
   - **الجوال والويب:** تفاؤلية دائمًا، أيًّا كان الإعداد.
   - **إذن OPTIMISTIC سلوك إنتاجي موثّق،** لا تبسيط منا.
5. **[V]** المكتبات تعيد المحاولة 5 مرات افتراضيًا، لكن **رموز الأخطاء التي تعيد بها تختلف**:
   - **Go وPython:** تعيد على `ABORTED` فقط.
   - **Node وJava:** تعيد على `ABORTED` وأي خطأ عابر. وتعيد كذلك على `INVALID_ARGUMENT` إن احتوت رسالته على "transaction has expired".
   - **الجوال والويب:** تعيد على `ABORTED` و`FAILED_PRECONDITION` و`ALREADY_EXISTS` وأي خطأ غير دائم.
6. **[V]** حدود الإنتاج:
   - المعاملة تعيش 270 ثانية، وتنتهي إن بقيت خاملة 60 ثانية.
   - حد انتظار القفل 20 ثانية.
   - حجم الطلب 10 MiB.
   - **لا حد لعدد الكتابات في الـ Commit منذ 2023-03-29.** حد الـ 500 القديم أُلغي.
7. **[V]** `WriteResult.update_time`: "If the write did not actually change the document, this will be the previous update_time". فالكتابة التي لا تغيّر شيئًا لا تغيّر النسخة، وKor يخالف هذا.
8. **[I]** **التوصية:** نتحقق من المعاملات تفاؤليًا عند الـ commit، داخل كاتب القاعدة الواحد (ADR-019)، مقابل سجل التغييرات بالتسلسل `seq`:
   - لا `SERIALIZABLE`.
   - لا اتصال Postgres محجوز بين طلبات المعاملة.
   - لا أقفال صفوف.
   - الاستعلامات داخل المعاملة نتحقق منها بمطابق الاستعلامات نفسه الذي يخدم Listen (ADR-026).
9. **[I]** نؤجل الوضع التشاؤمي، ونوثّقه فرقًا. والقراءة بـ `read_time` تُحسم بتجربة كلفة (F0).
10. **[V]** المحاكي الرسمي ليس أوراكل جيدًا للمعاملات:
    - أقفاله المشتركة تجعل معاملتين تنشئان المستند نفسه تفشلان معًا (firebase-tools #6758).
    - والإصدار v1.22.0 فوّت تعارضًا حقيقيًا (#11241).

## 1. أوضاع التزامن الموثّقة

### 1.1 ما تقوله الوثائق
المصدر: [transaction-data-contention](https://firebase.google.com/docs/firestore/transaction-data-contention)، **[V]**.

- **التشاؤمي:**
  - "Pessimistic transactions use database locks to prevent other operations from modifying data."
  - "A transaction releases its document locks at commit time."
  - "Transactions acquire their locks in chronological order."
- **التفاؤلي:** "If any document did change, the transaction handler retries the transaction."
- **الجوال والويب:**
  - "Mobile and web SDKs emulate optimistic concurrency transactions using write preconditions on document versions"
  - "even if the database concurrency mode is configured for `PESSIMISTIC`, mobile clients still behave optimistically."
- **مكتبات الخادم:** "The server client libraries use concurrency controls of the database setting."
- **الافتراضي:** `PESSIMISTIC` في Standard، و`OPTIMISTIC` في Enterprise ("to support operations that scan over entire collections").
- **العزل:** Firestore "guarantees serializable isolation"، ويرتّب المعاملات بوقت الـ commit (نتيجة البحث عن الصفحة نفسها).
- **رسالة التعارض:** `ABORTED: Too much contention on these documents. Please try again.`

**الإعداد في Admin API** [V] (`google/firestore/admin/v1/database.proto:63-88`)، `Database.ConcurrencyMode`:
- `OPTIMISTIC = 1`: الافتراضي في Enterprise.
- `PESSIMISTIC = 2`: الافتراضي في Standard.
- `OPTIMISTIC_WITH_ENTITY_GROUPS = 3`: "not supported for Firestore Native databases"، ويخص قواعد Datastore المرقّاة.

**[V] اختيار الوضع لكل معاملة** (`google/firestore/v1/common.proto:59-87`): `TransactionOptions.ReadWrite.concurrency_mode` يقبل `OPTIMISTIC` أو `PESSIMISTIC`.
- "A database is able to use different concurrency modes for different transactions simultaneously."
- **ملاحظة لافتة:** "3rd party auth requests are only allowed to create optimistic read-write transactions and must specify that here".
  - **[I]** أي إن الطلب الذي يحمل رمز مستخدم نهائي (Firebase Auth، تحكمه القواعد) لا يحصل على أقفال تشاؤمية أبدًا. وهذا يتسق مع كون العملاء دائمًا تفاؤليين.

### 1.2 كيف يحل الإنتاج التعارض في الوضع التشاؤمي
- **لا يذكره Firestore صراحة.**
- **Spanner، الذي يقوم عليه Firestore، يوثّقه** [V] ([concurrency-control](https://docs.cloud.google.com/spanner/docs/concurrency-control)):
  - "When Spanner detects a lock conflict, it uses the wound-wait algorithm"
  - "Spanner prioritizes older transactions over newer ones during conflicts"
  - القراءة في معاملة read-write تأخذ قفل `ReaderShared` يبقى حتى الـ commit.
- **[V] تعليق في كود Python** (`google-cloud-python/packages/google-cloud-firestore/google/cloud/firestore_v1/transaction.py:321-326`): إرسال `retry_transaction` بمعرّف المحاولة الفاشلة "preserves the 'spot in line' of the transaction, so exponential backoff is not required".
- **[I]** الخلاصة: الإنتاج يعطي الأولوية للمعاملة الأقدم (wound-wait)، و`retry_transaction` يحفظ عمرها عبر المحاولات.
  - هذا يفسّر لماذا لا تجوع معاملة تشاؤمية في الإنتاج.
  - ويفسّر كذلك لماذا يفشل المحاكي الذي ينتظر بلا أولوية (القسم 5.3).

## 2. ما ترسله كل مكتبة فعلًا

### 2.1 الجوال والويب: شروط مسبقة، بلا معاملة على الخادم

**[V] الويب** (`firebase-js-sdk@3a5d6a0:packages/firestore/src/core/transaction.ts`):

- **القراءة** (`:63-76`):
  - `lookup` يرفض محليًا أي قراءة بعد كتابة: `INVALID_ARGUMENT` "Firestore transactions require all reads to be executed before all writes."
  - ثم يستدعي `invokeBatchGetDocumentsRpc` بلا حقل `transaction`. فالخادم يرى قراءة عادية.
- **تسجيل النسخة** (`:118-144`):
  - يُحفظ `update_time` كل مستند مقروء، والغائب يُحفظ بالنسخة `SnapshotVersion.min()`.
  - إن اختلفت نسخة المستند بين قراءتين فالخطأ `ABORTED` "Document version changed between two reads.". يُحسب محليًا، بلا طلب.
- **شرط الكتابة** (`:150-161`): أول كتابة على مستند مقروء تحمل شرطًا:
  - `exists:false` إن كان غائبًا.
  - وإلا `update_time` بنسخته.
  - والمستند الذي لم يُقرأ يُكتب بلا شرط.
- **التحديث** (`:166-195`):
  - تحديث مستند قُرئ غائبًا يُرفض محليًا (`INVALID_ARGUMENT` "Can't update a document that doesn't exist.").
  - وتحديث مستند لم يُقرأ يحمل `exists:true`.
- **الـ commit** (`:97-116`): لكل مستند قُرئ ولم يُكتب تُضاف `VerifyMutation` بالشرط نفسه. ثم يُرسل `Commit` واحد بكل الكتابات، بلا `transaction`.
- **السلك** (`remote/serializer.ts:701-703`): `VerifyMutation` يُرسل بالصيغة `{verify: <name>, currentDocument: {...}}`.
- **الـ proto المضمّن** (`src/protos/google/firestore/v1/write.proto:44-47`): `string verify = 5;` وتعليقه "This only requires read access to the document".
- **[V] Android وiOS مطابقان:**
  - Android: `firebase-firestore/.../core/Transaction.java:80-88` للقراءة قبل الكتابة، و`:141` للـ verify، و`:192` لـ ABORTED، و`:207-242` للشروط.
  - iOS: `Firestore/core/src/core/transaction.cc:136-171`.
  - والحقل `verify = 5` موجود في `firebase-firestore/src/proto/google/firestore/v1/write.proto:47` (Android) و`Firestore/Protos/protos/google/firestore/v1/write.proto:48` (iOS).
- **[V] الحقل غائب عن الـ proto العام:** `googleapis@cfc42c2:google/firestore/v1/write.proto` فيه `update` و`delete` و`transform` فقط. ولا يوجد نوع `Write_Verify` في `google-cloud-go/firestore/apiv1/firestorepb/write.pb.go`.
- **[V] في hidane** (`docs/parity-exceptions.md:52`، D009): الـ `verify` يُقيَّم في القواعد كعملية `get` على المستند.
  - **[I]** لا يحتاج إذن كتابة، وهذا يطابق تعليق الـ proto.

**[V] إعادة المحاولة في الجوال والويب:**
- الويب (`transaction_runner.ts:115-128`): يعيد على `aborted` و`failed-precondition` و`already-exists`، وأي رمز ليس "permanent".
- الحد 5 محاولات (`transaction_options.ts:20-21`).
- التأخير أُسّي: يبدأ بثانية، بمعامل 1.5، وحده الأعلى 60 ثانية (`remote/backoff.ts:27-32`).
- Android (`TransactionRunner.java:98-105`) وiOS (`transaction_runner.cc:37-42`): القائمة نفسها.
- **تعليق في كود الويب:** "the backend will fail outdated reads with FAILED_PRECONDITION and non-matching document versions with ABORTED".
  - **[U]** أي الرمزين يعيده الإنتاج لشرط `update_time` لم يتحقق في `Commit` بلا معاملة؟ المحاكي يعيد `FAILED_PRECONDITION` "the stored version (x) does not match the required base version (y)" (hidane `crates/hidane/src/firestore/writes.rs:670-685`).
  - المكتبات تعيد المحاولة على الرمزين، فالفرق لا يكسر شيئًا. لكنه يُسجَّل من الإنتاج (تجربة E1-a).

**[I] ما يترتب على الخادم:**
- طريق الجوال والويب كله `Commit` ذري يتحقق من الشروط داخله. لا يحتاج سجل معاملات، ولا أقفالًا عبر الطلبات.
- وهو ما ينفّذه الكاتب الواحد بلا كلفة إضافية: يفحص الشروط على الحالة التي سيكتب فوقها، كما تفعل RTDB مع `datastale`.
- **الويب يرسل هذه الطلبات عبر REST** (`:batchGet` و`:commit`)، لا عبر WebChannel، كما ذكرت الخطة عن hidane. فـ `verify` يجب أن يُفهم في JSON أيضًا.

### 2.2 مكتبات الخادم: معاملة لها معرّف

**[V] Go** (`google-cloud-go@f41be16:firestore/transaction.go`):
- يبدأ كل محاولة بـ `BeginTransaction` (`:210-215`).
- يعيد على `codes.Aborted` فقط (`isAborted`، `:142-153`).
- يرسل في المحاولة التالية `ReadWrite{RetryTransaction: t.id}` (`:264-273`).
- `DefaultTransactionMaxAttempts = 5` (`:60`). والقراءة بعد الكتابة تُرفض محليًا (`errReadAfterWrite`، `:132`).
- `ReadOnly` يُرسل مع `read_time` اختياري (`:194-201`).

**[V] Node** (`nodejs-firestore@1068f5c:dev/src/transaction.ts`):
- **البدء كسول** (`withLazyStartedTransaction`، `:739-790`):
  - لا `BeginTransaction`. أول قراءة تحمل `new_transaction` بقيمة `readWrite{}`، أو `readWrite{retryTransaction}` في الإعادة، أو `readOnly{}`.
  - تُرسل بقية القراءات بعد وصول المعرّف.
  - إن لم يُرجع الخادم `transaction` في الرد، يفشل كل شيء بـ "Transaction ID was missing from server response" (`:778-783`).
- **المعاملة بلا قراءات** تُرسَل كدفعة ذرية بلا معرّف (`:553-563`).
- **إعادة المحاولة** (`isRetryableTransactionError`، `:981-1003`) تكون على:
  - `ABORTED` و409.
  - `CANCELLED` و`UNKNOWN` و`DEADLINE_EXCEEDED` و`INTERNAL` و`UNAVAILABLE` و`UNAUTHENTICATED` و`RESOURCE_EXHAUSTED`.
  - و`INVALID_ARGUMENT` فقط إن طابقت رسالته `/transaction has expired/`.
- `RESOURCE_EXHAUSTED` و409 يرفعان التأخير إلى حده الأعلى (`:1014-1024`).
- **القراءة للقراءة فقط مع `readTime`** لا تفتح معاملة أصلًا، بل تمرّر `read_time` لكل قراءة (`:755-760`). ومحاولتها واحدة (`:117-121`).
- **الثوابت:** `DEFAULT_MAX_TRANSACTION_ATTEMPTS = 5` (`dev/src/index.ts:181`). والتأخير يبدأ بثانية، بمعامل 1.5، وحده الأعلى 60 ثانية، مع jitter (`dev/src/backoff.ts:34-50`).

**[V] Java** (`java-firestore@dd16eb0:.../ServerSideTransactionRunner.java:272-290`): القائمة نفسها التي في Node، ومنقولة صراحة من firebase-js-sdk.

**[V] Python** (`google-cloud-python@eb8a76628adf:.../firestore_v1/transaction.py:307-326`): يعيد على `exceptions.Aborted` فقط، ولا يؤخّر بين المحاولات، معتمدًا على `retry_transaction`.
- **[V] وفوق ذلك، الإعادة تلتقط أخطاء `_commit()` وحدها:** دالة المستخدم تُستدعى في `_pre_commit` (`:286`، `return self.to_wrap(...)`) **خارج** `try` الإعادة (`:315-319`). فـ `ABORTED` يصل أثناء قراءة داخل دالة المستخدم يخرج إلى المستخدم دون أي إعادة.
- **[V] للمقارنة، Go** (`google-cloud-go@f41be16:firestore/transaction.go:222-256`): الخطأ من `f` أو من `Commit` يمر بالفحص نفسه، `isAborted(err)`، فيُعاد إن أعادت دالة المستخدم الخطأ كما هو.

**[I] ما يترتب على الخادم:**
1. **أن يقبل الطريقين لبدء المعاملة:**
   - `BeginTransaction`.
   - و`new_transaction` في `BatchGetDocuments` و`RunQuery` و`RunAggregationQuery`.
   - وفي الحالة الثانية يُرجع `transaction` في **أول** رسالة من الرد، ولو لم يطابق الاستعلام شيئًا (رسالة فيها `transaction` و`read_time` فقط). المرجع: `firestore.proto:479-492` و`:614-658`.
2. **المعاملة المنتهية تُرفض برسالة فيها النص "transaction has expired":**
   - وإلا لن تعيد Node وJava المحاولة.
   - المحاكي يعيد `ABORTED` "The referenced transaction has expired or is no longer valid." (hidane D025)، وهي تحتوي النص المطلوب.
   - **[U]** رمز الإنتاج: يُسجَّل في E1-b.
3. **`ABORTED` هو الرمز الوحيد الذي تعيد عليه كل المكتبات.** فأي فشل تحقق في معاملة لها معرّف يجب أن يكون `ABORTED`، لا `FAILED_PRECONDITION`.

## 3. حدود الكتابة ودلالاتها

| البند | القيمة | المصدر |
|---|---|---|
| عدد الكتابات في `Commit` أو معاملة | **بلا حد** منذ 2023-03-29. كان 500 | [V] [release notes](https://docs.cloud.google.com/firestore/docs/release-notes): "Firestore no longer limits the number of writes that can be passed to a `Commit` operation or performed in a transaction." |
| حجم الطلب | 10 MiB | [V] [quotas](https://firebase.google.com/docs/firestore/quotas) |
| عمر المعاملة | 270 ثانية، و60 ثانية خمول | [V] quotas و[transactions](https://firebase.google.com/docs/firestore/manage-data/transactions) |
| مهلة القفل | 20 ثانية ("exceeded the lock deadline (20 seconds)") | [V] transactions |
| التحويلات على مستند واحد | 500 | [V] quotas |
| حجم المستند | 1 MiB (1,048,576 بايت) | [V] quotas |
| استدعاءات القواعد في عملية ذرية | 20 للعملية كلها، و10 لكل مستند | [V] transactions |
| معدّل الكتابة على مستند واحد | لا رقم ثابت الآن: "depends highly on the workload". وعند معدّل عالٍ "contention, higher latency, or other errors". وقاعدة 500/50/5 للمجموعات الجديدة | [V] [best-practices](https://firebase.google.com/docs/firestore/best-practices) |

**دلالات من الـ proto** [V] (`googleapis@cfc42c2:google/firestore/v1/`):
- **`Precondition.update_time`** (`common.proto:52-54`): "must exist and have been last updated at that time. Timestamp must be microsecond aligned".
  - المحاكي يرفض أي جزء من الميكروثانية بـ `INVALID_ARGUMENT` "timestamp cannot have more than microseconds precision" (hidane `writes.rs:302-315`).
- **`CommitResponse.commit_time`** (`firestore.proto:574-576`): "Any read with an equal or greater `read_time` is guaranteed to see the effects of the commit".
- **`WriteResult.update_time`** (`write.proto`، رسالة `WriteResult`): "If the write did not actually change the document, this will be the previous update_time."
  - **[I]** فالكتابة التي لا تغيّر شيئًا لا تحرّك النسخة، ولا تُبطل معاملة تفاؤلية قرأت المستند، ولا تُطلق حدث Listen (المسار C يتحقق من هذا الجزء).
- **`REQUEST_TIME`:** "in a transaction, all the fields will get the same server timestamp".
- **`increment`:** عند تجاوز حد الأعداد الصحيحة يتشبّع إلى أكبر قيمة بالإشارة نفسها، ويتحول إلى double إن كان أحد الطرفين double.
- **`maximum` و`minimum`:** فيهما قواعد للأنواع المختلطة، والصفر بأنواعه، وNaN.
- **`append_missing_elements` و`remove_all_from_array`:** المساواة فيهما بين 3 و3.0، وNaN يساوي NaN. وكل هذه نصوص صريحة في `write.proto` تصلح جدول اختبار.
- **`BatchWrite`** (`firestore.proto:236-245`): غير ذري، وقد يطبّق الكتابات بغير ترتيبها، ولا يقبل أكثر من كتابة واحدة لكل مستند. وكل كتابة تنجح أو تفشل وحدها. يستعمله BulkWriter في Admin.
- **[V] قواعد المحاكي لتعدد الكتابات على مستند واحد في commit واحد** (hidane `writes.rs:98-107`، مسجّلة بـ `tools/oracle/same_document_writes.py`):
  - الشرط `update_time` يُسمح به في أول كتابة فقط.
  - الـ insert لا يتبع إلا delete.
  - وفي `check_verifies` (`writes.rs:42-63`) يُرفض بـ `INVALID_ARGUMENT` أن يُتحقق من المستند ثم يُكتب، أو أن يُتحقق منه مرتين في الطلب نفسه.
  - **[U]** هذه قواعد المحاكي. ولم نجد في الوثائق ما يؤكدها للإنتاج.

## 4. محاكاتها فوق Postgres: الخيارات

### 4.1 `SERIALIZABLE` (SSI) مع اتصال محجوز لكل معاملة Firestore
**ما يقوله Postgres** [V] ([transaction-iso](https://www.postgresql.org/docs/current/transaction-iso.html)):
- "applications using this level must be prepared to retry transactions due to serialization failures" بالرمز `40001`.
- أقفال الشرط (`SIReadLock`) "do not cause any blocking".
- "Don't leave connections dangling 'idle in transaction'".
- "A sequential scan will always necessitate a relation-level predicate lock".
- والإنذارات الكاذبة تزيد حين تُدمج أقفال الصفحات لنقص ذاكرة جدول أقفال الشرط.

**التقييم [I]:**
- **يحجز اتصالًا لكل معاملة.** معاملة Firestore تمتد عبر عدة RPCs حتى 270 ثانية، فيبقى الاتصال محجوزًا طوال ذلك. عميل بطيء أو معطَّل يحجزه 60 ثانية على الأقل.
  - مع مجمّع اتصالات صغير (خادم 8 GB) يكفي بضع عشرات من المعاملات المعلّقة لخنق كل القراءات.
- **يمسك أفق الـ `xmin`** فيمنع الـ vacuum، فتنتفخ الجداول الساخنة.
- **أقفال SSI على صفحات B-tree** لجدول فهارس `bytea` تعطي إنذارات كاذبة كثيرة على المجموعات الساخنة.
- **عن RLS:** Aruma لا يستعمل RLS اليوم. ADR-017 أجّله، والعزل اليوم بـ `project_number` و`internal/store/scopecheck`. لكن إن اعتُمدت RLS لاحقًا بـ `SET LOCAL` لكل معاملة Postgres، فالاتصال المحجوز لكل معاملة Firestore يزيد كلفتها. هذا اعتبار لقرار RLS المفتوح، لا افتراض.
- **لا يتسق مع ADR-019:** الكاتب الواحد يرتّب الكتابات أصلًا، فلا يضيف SSI شيئًا سوى الكلفة.
- **الحكم:** مرفوض.

### 4.2 `READ COMMITTED` مع `SELECT … FOR UPDATE` وفحص النسخة عند الـ commit (نموذج Kor)
**[V] Kor** (`internal/store/txn.go:14-21` و`store.go:199-335`):
- `BeginTransaction` ينشئ سجل قراءات في الذاكرة، يعيش 5 دقائق.
- القراءات تُخدم من أحدث حالة، ويُحفظ `update_time` أول قراءة.
- **عند الـ commit:**
  - قفل `pg_advisory_xact_lock(hashtext(name))` على كل مستند مقروء أو مكتوب، بترتيب ثابت لتفادي الـ deadlock.
  - ثم `SELECT … FOR UPDATE`.
  - ثم مقارنة النسخ: أي اختلاف يعيد `ABORTED`.
- السبب الذي يذكره لأقفال advisory: "Row locks (FOR UPDATE) cannot cover documents that do not exist yet, so concurrent creates of the same name would race" (`store.go:233-236`).
- **الاستعلامات داخل المعاملة** ترفض بـ `UNIMPLEMENTED` (`internal/rpc/server.go:73-75`)، وكذلك `read_time` (`:162-163`).

**ثغرات وجدتها في Kor [I]، كلها مستنتجة من قراءة الكود:**
1. **`verify` يضيع.** يستعمل `firestorepb` من `cloud.google.com/go/firestore v1.25.0` (`go.mod:6`)، والحقل 5 غير موجود فيه، فيُسقطه فك التشفير.
   - تصل الكتابة بلا عملية فيرد `TargetName` بـ `INVALID_ARGUMENT` "write has no operation" (`internal/writes/writes.go:50`).
   - النتيجة: كل معاملة من الويب أو الجوال تقرأ مستندًا ولا تكتبه تفشل فشلًا دائمًا، لأن `INVALID_ARGUMENT` لا يُعاد.
   - تقرير Kor في `docs/differential-testing.md:114` يقول إن المعاملات لم تُختبر تفاضليًا.
2. **`commitTime` قد يرجع إلى الوراء.** يُحسب بـ `time.Now()` قبل أخذ الأقفال (`store.go:200`).
   - إن حسب commit أ وقته ثم سبقه commit ب إلى القفل، يُكتب لأ وقت أقدم فوق وقت ب. فيرجع `update_time` المستند إلى الوراء.
   - هذا يكسر وعد `commit_time` في القسم 3، ويفتح باب ABA لشرط `update_time`.
3. **كل كتابة تحرّك `update_time`** (`writes.go:88،109،126`)، ولو لم تغيّر المستند. وهذا يخالف الـ proto.
4. **لا حماية من الـ phantoms،** لأن الاستعلامات مرفوضة داخل المعاملة. ومعاملات Admin التي تقرأ باستعلام (`t.get(query)`) شائعة.

**التقييم [I]:**
- النموذج صحيح للقراءات النقطية، ولا يحجز اتصالًا بين الطلبات. وهذه هي الفكرة الصائبة فيه.
- لكن الأقفال تصير بلا لزوم إن كان هناك كاتب واحد للقاعدة.
- ولا يغطي الاستعلامات.

### 4.3 التحقق التفاؤلي داخل الكاتب الواحد مقابل سجل التغييرات (التوصية)
انظر القسم 6.

### 4.4 الأقفال التشاؤمية في الذاكرة (نموذج المحاكي وhidane)
**[V] hidane** (`crates/hidane/src/firestore/transactions.rs:1-44`، مسجّل من المحاكي v1.22.0):
- **أقفال القراءة:** قفل مشترك على كل مستند يُقرأ، موجودًا كان أو غائبًا. و`ListDocuments` والاستعلامات تقفل **كل مجموعة تحمل معرّف المجموعة نفسه، أيًّا كان أبوها**.
- **انتظار الكتابة:** أي كتابة تنتظر أقفال المعاملات الأخرى حتى `LOCK_TIMEOUT = 2s`، ثم `ABORTED` "Transaction lock timeout.".
- **الخمول:** المعاملة الخاملة تنتهي بعد `IDLE_TIMEOUT = 60s`. وثائق المحاكي تقول 30 ثانية، والقياس أعطى 60 (`docs/parity-exceptions.md:135`، D069).
- **القراءة فقط:** المعاملة للقراءة فقط لا تقفل شيئًا، وتقرأ لقطة بدايتها.
- **`concurrency_mode: OPTIMISTIC`** يُقبل ويُتجاهل.

**التقييم [I]:**
- **هذا النموذج ليس الإنتاج:**
  - مهلة القفل في الإنتاج 20 ثانية، لا 2.
  - والإنتاج يحل التعارض بالأقدم أولًا، لا بالانتظار الأعمى.
  - والقفل على مستوى معرّف المجموعة كله أخشن بكثير مما يفعله Spanner بنطاقات المفاتيح.
- **ويُوقف الكاتب:** تطبيقه عندنا يجعل الكاتب الواحد ينتظر أقفالًا، فتتوقف كل كتابات القاعدة خلف معاملة عالقة. إلا إن كان الانتظار قبل دخول طابور الكاتب، وهذا تعقيد كبير.
- **الحكم:** يؤجَّل. وإن احتجناه لاحقًا فالتصميم الصحيح جدول أقفال في الذاكرة مع wound-wait بعمر المعاملة، وعمرها يُحفظ عبر `retry_transaction`.

## 5. سلوك المحاكي الرسمي، ولماذا لا نعتمده أوراكلًا للمعاملات

1. **[V] #6758** ([firebase-tools](https://github.com/firebase/firebase-tools/issues/6758)، مفتوحة منذ 2024-02-09): معاملتان متوازيتان تنشئان المستند نفسه **تفشلان كلتاهما** بـ `Aborted` "Transaction lock timeout.".
   - **[I]** السبب: كل منهما أخذت قفلًا مشتركًا على المستند الغائب، ثم انتظرت الأخرى لترقيته إلى قفل كتابة (deadlock)، ولا أولوية بالعمر.
   - مع OCC عندنا تنجح واحدة وتُعاد الأخرى.
2. **[V] #11241** (مفتوحة، 2026-10-05): المحاكي v1.22.0 يسمح لمعاملة بالـ commit بعد تغيّر مستند قرأته باستعلام `select`/`limit` مع `getAll` واستعلام collection-group. وهذا تراجع عن v1.21.0. hidane صنّفها خللًا وتجاوزها (D023).
3. **[V] #11160:** كتابات `batchWrite` متزامنة على المستند نفسه تفشل بـ "Transaction lock timeout." في v1.22.0.
4. **[V] #10518:** `get()` في القواعد يعطي `PERMISSION_DENIED` أحيانًا بعد انتهاء مهلة قفل.
5. **[V] #8300:** في وضع Datastore لا تنجح أي معاملة متزامنة.
6. **[V] hidane D085** (`parity-exceptions.md:114`): استعلام فاشل داخل معاملة يترك قفلًا لا يحرّره أحد.

**[I] الخلاصة:**
- **المحاكي أوراكل لما يلي:** رموز الأخطاء ورسائلها، وقواعد الطلب الواحد.
- **وليس أوراكلًا لما يلي:** التزامن نفسه.
- **أوراكل التزامن:** الوثائق، ثم تسجيلات الإنتاج (E1).

## 6. التصميم المقترح لـ Aruma [I]

### 6.1 المبدأ
- **الكاتب ينفّذ كل commit بالتتابع:** ADR-019 يعطي كل قاعدة كاتبًا واحدًا. فكل commit، بمعاملة أو بلا معاملة، يمرّ به ويُنفَّذ بعد سابقه.
- **التحقق داخل الكاتب لا يحتاج أقفالًا:**
  - يكفي التحقق التفاؤلي الخلفي (backward OCC validation)، تُفحص فيه المعاملة مقابل ما كُتب منذ قرأت.
  - الخطوات: هل غيّر أي commit منذ قراءة المعاملة شيئًا قرأته؟ إن نعم فـ `ABORTED`. وإلا فطبّق الكتابات وزد `seq`، في معاملة Postgres نفسها.
- **هذا قابل للتسلسل (serializable):** المعاملة تُرتَّب في نقطة الـ commit. وهو ما تعد به الوثائق ("serialized and isolated by commit time").

- **قيد معروف وبديله [I]:** كل هذا العمل يمر بكاتب واحد لكل قاعدة: التحقق والقواعد، بما فيها `get()`، وcommit Postgres. فسقف الكتابة للقاعدة هو سقف ذلك الكاتب.
  - Google لم تكن هكذا: commits مستقلة متزامنة، كل منها معاملة Spanner بأقفال لكل مستند [P §IV-D1، §IV-D2]. ولم يكن واحدًا لكل قاعدة إلا الـ changelog.
  - يُقاس السقف في F0 (README، F0-11). وإن لم يكفِ فالبديل: تقييم القواعد والتحقق الأول خارج الكاتب، بالتوازي، مقابل `seq` مقروء. ويبقى للكاتب الترتيب، وإعادة فحص رخيصة لنسخ المستندات منذ ذلك الـ `seq`، والـ commit.

### 6.2 حالة المعاملة
- **السجل في الذاكرة،** يمتلكه كاتب القاعدة أو يجاوره. وفيه:
  - `id`: 16 بايت عشوائية.
  - `database`.
  - `mode`: rw أو ro.
  - `first_read_seq`.
  - `reads`: كل مستند مقروء مع `update_time` أو "غائب"، ومع `seq` القراءة.
  - `queries`: الاستعلام المهيكل مع `seq` تنفيذه.
  - `created` و`last_used` و`origin_id`، وهو معرّف أول محاولة ويُورَث عبر `retry_transaction`، لأي أولوية مستقبلية.
- **لا اتصال Postgres محجوز بين الطلبات.**
- **الحدود:** 270 ثانية للعمر، و60 للخمول، و20 ثانية للانتظار الداخلي إن وُجد.
- **المعاملة المنتهية أو المجهولة:**
  - ترد بالرسالة "The referenced transaction has expired or is no longer valid."، وفيها النص الذي تطابقه Node وJava.
  - والرمز يُحسم بتسجيل الإنتاج (E1-b): `INVALID_ARGUMENT` أو `ABORTED`.
- **إعادة تشغيل الخادم تُفقد السجل.** فيرد الخادم على المعرّفات القديمة بالرسالة السابقة، والمكتبات تعيد المحاولة.

### 6.3 القراءة داخل المعاملة
- **تُخدم من أحدث حالة، بالطريق العادي.**
- **المهم أن تعرف القراءة أي `seq` رأت:**
  - يُقرأ `seq` القاعدة والمستندات في **عبارة SQL واحدة**، لأن العبارة الواحدة في Postgres ترى لقطة واحدة.
  - أو يُقرأ `seq` المنشور من الكاتب قبل القراءة. والقراءة حينها ترى ذلك الـ seq أو أحدث منه، وهذا متحفظ وآمن.
- **لا إجهاض عند القراءة. كشف التعارض عند الـ commit فقط** (تصحيح بعد المراجعة):
  - كانت المسودة الأولى تقترح «تحققًا تزايديًا» يعيد `ABORTED` عند القراءة التالية إن تعارضت مع قراءة سابقة. **هذا يكسر Admin Python:** الإعادة فيه لا تلتقط إلا أخطاء `_commit()`، ودالة المستخدم خارجها (`transaction.py:286` و`:307-319`، §2). فيصل `ABORTED` المبكر إلى المستخدم خطأً نهائيًا، بدل أن تُعاد المعاملة.
  - **فالقاعدة:** القراءة داخل المعاملة لا تعيد `ABORTED` بسبب تعارض. والتعارض يُكتشف في الكاتب عند `Commit` (6.4)، وهناك تعيد كل المكتبات المحاولة.
  - **الثمن [I]:** قد يرى كود المستخدم قراءتين غير متسقتين في محاولة واحدة، أي لا opacity. لكن تلك المحاولة لن تُلتزم أبدًا، لأن الـ commit سيرفضها. والإنتاج المتشائم لا يعرض هذا، لأن القراءة تأخذ قفلًا، فهو فرق يُوثَّق في `docs/compat`.
  - **البديل الذي يزيل الثمن:** القراءة من لقطة ثابتة عند `first_read_seq`. يحتاج تاريخ نسخ (6.6، وتجربة E3).
  - **الإجهاض المبكر مسموح فقط حيث تعيد كل المكتبات المحاولة:** عند `Commit`. أو عند قراءة لمعاملة انتهت مدتها، وهذا خطأ «transaction has expired» لا تعارض. وأي توسيع لاحق يمر بـ E4.

### 6.4 التحقق عند الـ commit داخل الكاتب
1. **المستندات:** لكل مستند مقروء تُقارن نسخته الحالية بالمسجلة. والكاتب يحمّل المستندات المستهدفة أصلًا، فلا كلفة تُذكر.
2. **الاستعلامات (phantoms):**
   - لكل تغيير في سجل التغييرات بعد `seq` تنفيذ الاستعلام، يُسأل: هل يطابق المستند الاستعلام قبل التغيير أو بعده؟ إن طابق فـ `ABORTED`.
   - **[I] هذا المطابق نفسه** الذي يقرر في ADR-026 أي Listen يتأثر بكتابة. فنبنيه مرة ونستعمله مرتين.
   - **استعلامات `limit` متحفظة:** تغيير يطابق الشرط لكنه خارج النافذة يُبطلها أيضًا. فيها إبطال زائد، ولا إبطال ناقص.
   - **التجميعات** (`count`/`sum`/`avg`) تُعامل كاستعلام بلا `limit`.
3. **مخزن مؤقت للتغييرات الحديثة:**
   - يحفظ في الذاكرة الحلقية لكل قاعدة صورة المستند قبل التغيير وبعده، لمدة لا تقل عن 270 ثانية أو بحد بايتات.
   - إن لم يغطِّ المخزن `seq` قراءة المعاملة، يعيد الكاتب `ABORTED`. وهذا آمن دائمًا.
4. **الشروط المسبقة** (`exists` و`update_time`) و`verify` تُفحص على الحالة الحالية، مع أثر الكتابات السابقة في الطلب نفسه.
   - **رموز commit بلا معاملة** (طريق الجوال والويب): `NOT_FOUND` و`ALREADY_EXISTS` و`FAILED_PRECONDITION`، كما في المحاكي حتى يثبت الإنتاج غيرها.
   - **فشل التحقق في معاملة لها معرّف:** `ABORTED` "Too much contention on these documents. Please try again." دائمًا.
5. **وقت الـ commit يعطيه الكاتب:**
   - `max(now_µs, last_commit_time+1µs)`: رتيب صارم لكل قاعدة، وبدقة الميكروثانية.
   - كل كتابات الـ commit تأخذه، ومعه قيم `REQUEST_TIME`.
   - المستند الذي لم يتغير فعلًا يُبقي `update_time` السابق، ولا يُكتب له تغيير في السجل.
   - **[I]** الرتابة تجعل مقارنة `update_time` بالمساواة سليمة بلا ABA، بخلاف Kor.
6. **التجميع في معاملة Postgres:** الكاتب يجمع حتى N commit في معاملة Postgres واحدة (ADR-019).
   - كل commit منطقي يُتحقق منه مقابل الحالة بعد سابقيه في الدفعة. والحالة طبقة فوق الذاكرة.
   - إن فشلت معاملة Postgres كلها، يعيد الكاتب تنفيذ الـ commits منفردة، أو يعيد لكل منها `UNAVAILABLE` أو `ABORTED`، وكلاهما يُعاد.
7. **القواعد تُقيَّم داخل الكاتب** على الحالة نفسها، بـ `getAfter` على طبقة الكتابات. و`verify` يُقيَّم كـ `get` (hidane D009). والتفصيل لمسار القواعد.

### 6.5 الوضع التشاؤمي
- **لا نطبّقه في v0.4.**
- **نعلن `concurrencyMode: OPTIMISTIC`** لقاعدة Aruma في واجهة الإدارة. وهو إعداد إنتاجي حقيقي، والافتراضي في Enterprise.
- **و`concurrency_mode: PESSIMISTIC` إن طُلب صراحة في `TransactionOptions`:**
  - إما يُقبل ويُنفَّذ تفاؤليًا مع توثيق الفرق.
  - أو يُرفض.
  - والأول أسلم للتوافق [I].
- **الفرق المرئي، ويُوثَّق في `docs/compat`:**
  - تحت تعارض شديد على مستند ساخن، تُستنفد المحاولات الخمس، بدل أن تنتظر الكتابات الخارجية كما في الإنتاج.
  - والكتابات خارج المعاملات لا تنتظر أبدًا.

### 6.6 للقراءة فقط و`read_time`
- **`readOnly{}` بلا `read_time`:** المحاكي يقرأ لقطة بدايتها. والإنتاج يعد بقراءات متسقة.
- **`read_time`:**
  - حتى ساعة في الماضي، أو 7 أيام بدقة الدقيقة مع PITR (`common.proto:94-99`).
  - المحاكي يرد على الأقدم بـ `FAILED_PRECONDITION` "The requested 'read_time' is too old." (hidane D065).
- **الخيار أ:** جدول `document_versions` (project_number، database_id، name، valid_from_seq، valid_to_seq، data) يكتبه الكاتب في المعاملة نفسها، ويُنظَّف بعد ساعة.
  - يحل `read_time`، والقراءة فقط، ولقطات المعاملات المتسقة.
  - وقد يخدم استئناف Listen.
  - **الكلفة:** كتابة إضافية لكل تحديث وحذف، ومثلها في WAL.
- **الخيار ب:** `UNIMPLEMENTED` أو `FAILED_PRECONDITION` لـ `read_time` في v0.4، كما فعل Kor. ومعه قراءة فقط "بأفضل جهد": أحدث حالة دون ضمان لقطة واحدة بين القراءات.
  - **مشكلة:** أي `ABORTED` هنا يصل للمستخدم: Node للقراءة فقط بمحاولة واحدة، وPython لا يعيد للقراءة فقط أصلًا (`retryable_exceptions` فارغة حين `_read_only`، `transaction.py:308-310`).
- **القرار:** في ADR-027 بعد قياس E3.

### 6.7 التعدد المستقبلي للعقد
السجل في الذاكرة يفترض عقدة واحدة تملك كاتب القاعدة. ومع تعدد العقد (ADR-019 البند 3) يُوجَّه كل طلب يحمل معرّف معاملة إلى عقدة كاتب قاعدته. ويُرمَّز في المعرّف ما يكفي لذلك: رقم العقدة ورقم جيل الكاتب.

## 7. تجارب F0

| # | التجربة | ما تحسمه | أين |
|---|---|---|---|
| E1-a | `:commit` بـ REST بلا معاملة فيه `verify` بنسخة قديمة، ثم `update` بـ `update_time` قديم | رمز الإنتاج ورسالته: `FAILED_PRECONDITION` أم `ABORTED` | الإنتاج `chatapp-8ae21`، مجموعة `aruma-ref/<run-id>`، بالشروط في الموجز |
| E1-b | معاملة Admin تُترك خاملة أكثر من 60 ثانية ثم commit، ثم معرّف مجهول، ثم rollback مكرر | رمز ورسالة "transaction has expired" التي تعتمد عليها Node وJava | الإنتاج |
| E1-c | `RunQuery` بـ `new_transaction` لا يطابق شيئًا | شكل الرد الأول: `transaction` مع `read_time` | الإنتاج |
| E1-d | `set` بالبيانات نفسها مرتين | هل يبقى `update_time` كما هو، وهل يُطلق Listen حدثًا | الإنتاج |
| E1-e | معاملتا Admin على المستند نفسه، مع كتابة خارجية بينهما (الوضع الافتراضي Standard=PESSIMISTIC) | زمن انتظار الكتابة الخارجية، وأي المعاملتين تنجح. لتوثيق الفرق، لا لمحاكاته | الإنتاج |
| E1-f | `read_time` أقدم من ساعة، وبجزء من الميكروثانية | الرموز والرسائل | الإنتاج |
| E2 | نموذج أولي للتحقق داخل الكاتب: ألف معاملة مفتوحة فيها استعلامات، مقابل تدفق كتابات | كلفة المطابقة لكل commit، وحجم المخزن الحلقي اللازم لـ 270 ثانية | محليًا، قياس داخل العملية |
| E3 | جدول `document_versions` بتنظيف ساعة، مقابل بلا تاريخ | تضخيم الكتابة وWAL والمساحة. يحسم 6.6 | Postgres محلي بقاعدة تحمل بادئة المهمة |
| E4 | تشغيل اختبارات المعاملات الرسمية على النموذج الأولي: `transaction.test.ts` في firebase-js-sdk، واختبارات النظام في nodejs-firestore، واختبارات التكامل في google-cloud-go، **واختبارات النظام للمعاملات في google-cloud-python** (`packages/google-cloud-firestore/tests/system/`)، لأنها الأضيق في الإعادة | أي اختبار يفترض سلوكًا تشاؤميًا (انتظار لا إجهاض) | الـ harness في `test/compat` |
| E5 | معاملتان تنشئان المستند نفسه، و`verify` لمستند غائب، وقراءة ثم تحديث مستند حُذف بين القراءة والـ commit | أن ننجح حيث يفشل المحاكي (#6758)، ونطابق الإنتاج | acceptance |

## 8. ما يدخل في ADRs

- **ADR-024 (التخزين):**
  - `update_time` و`create_time` بدقة الميكروثانية، يكتبهما الكاتب.
  - سجل التغييرات يحمل صورة المستند قبل التغيير وبعده، أو يكفي المخزن الحلقي في الذاكرة.
  - وقرار `document_versions` بعد E3.
- **ADR-025 (البروتوكولات):** نولّد Go من الـ protos المضمّنة في مكتبات Firebase، التي فيها `Write.verify = 5`، لا من `firestorepb` أو googleapis وحدها. ويُفهم `verify` في JSON الخاص بـ REST.
- **ADR-026 (البث):** مطابق الاستعلامات قبل/بعد مشترك بين Listen والتحقق من المعاملات.
- **ADR-027 (النطاق):** ثلاثة بنود:
  - **الوضع:** OPTIMISTIC فقط.
  - **`read_time` والقراءة فقط:** حسب E3.
  - **الوضع التشاؤمي وأولوية wound-wait:** مؤجلان وموثّقان.
