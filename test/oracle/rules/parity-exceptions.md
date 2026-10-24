# حيث يختلف المحاكي عن Rules Test API (مجموعة التطوير)

- **ما قورن:** كل حالة من `production/` (ما قرّرته Rules Test API، 2026-10-23/24) شُغّلت على المحاكي
  الرسمي (`emulator/`)، وقورنت فئة النتيجة: `allow` أو `deny` أو `error`. الرسائل لا تُقارن، فصياغة كل
  محاكٍ تختلف. شروط hidane قورنت بما سجّلته hidane من المحاكي نفسه (v1.22.0).
- **المحاكيات:** `cloud-firestore-emulator-v1.22.0.jar` و`cloud-storage-rules-runtime-v1.1.3.jar`، على
  Java 21، كصناديق سوداء. التفاصيل في [README.md](README.md).
- **الحصيلة:** موضوعاتنا: 617 متطابقة، 29 مختلفة، 43 لم تُشغّل على المحاكي (سببها في `emulator/<topic>.json`:
  `request.time` لا يُختار عبر REST، أو طلب رفضته Test API نفسها، أو auth لا يكون ID token، أو مسار ليس
  مستندًا). شروط hidane: 1324 متطابقة، 9 مختلفة، 12 غير قابلة للمقارنة.
- **الخلاصة:** لم نجد خلافًا واحدًا في دلالات اللغة نفسها بين المحاكي وTest API. كل خلاف أدناه إمّا أثر من
  طريقة Test API في أخذ المدخلات، وإمّا من طريقة المحاكي في بناء الطلب، وإمّا حدّ لا تطبّقه Test API.
- **ما لا نعرفه:** Test API ليست الإنتاج المنشور. ما يقوله هذا الملف عن «الإنتاج» يعني Test API إلا حيث
  نذكر `deployed` (ملاحظات Pyric على Storage حقيقي).

## آثار Test API

ما يلي سلوك لـ Test API نفسها وليس للغة؛ لا يُقلَّد في المحرّك، ويُقارن S1-E هذه الحالات بالسلوك المنشور.

| الأثر | الحالات | المنشور / المحاكي |
|---|---|---|
| تأخذ `request` حرفيًّا: حقل لم يُرسل غير معرّف. بلا `auth` يكون `request.auth == null` خطأً «Property auth is undefined on object»؛ بـ `auth: null` يصحّ | `aruma/encoding/auth_absent`، `aruma/storage/auth_absent`؛ و`aruma/encoding/auth_null`، `aruma/storage/auth_null` | المحاكي: `request.auth == null` صحيح لطلب بلا مستخدم |
| كذلك `request.time` (`Property time is undefined`) و`request.query` و`request.resource` | `aruma/encoding/time_unset_is_timestamp`، `aruma/encoding/request_query_on_get`، `aruma/methods/list_query_limit`، `aruma/storage/request_resource_on_get` | الطلب الحقيقي فيه `time` دائمًا. محاكي Storage يعطي `request.resource == null` في القراءة؛ محاكي Firestore يجعله غير معرّف (`aruma/encoding/request_resource_null_on_get`) |
| تأخذ `resource` حرفيًّا: بلا `resource` يكون `resource == null` خطأً «Null value error»؛ بـ `resource: null` يصحّ | `aruma/encoding/resource_absent_is_null`، `aruma/storage/resource_absent_on_get`؛ و`aruma/encoding/resource_null_is_null`، `aruma/storage/resource_null_on_get` | المحاكي: المستند الغائب `resource == null` |
| المورد لا يُشتق من المسار: لا `id` ولا `__name__` في مورد Firestore، ولا حقل في كائن Storage غير ما أُرسل | `aruma/encoding/resource_name`، `aruma/hidane-conditions/rules/138`…`149`، `aruma/storage/create_name_absent` | المحاكي يعطي `resource.id` و`resource.__name__` |
| نتيجة `get()` المحاكاة بلا `id` ولا `__name__` | `aruma/storage/fs_get_id`، `aruma/storage/fs_get_name` | المحاكي يعطيهما (و`__name__` فيه بادئة `projects/<p>`) |
| `get()`/`exists()` لمستند غير محاكى: «Function not found error: Name: [get]» | `aruma/limits/get_unmocked`، `aruma/storage/fs_get_unmocked`، و`pyric/firestore/get-missing-doc/0` و`/1` و`/3` | المحاكي يبحث فعلًا: `get()` لمستند غائب يعطي `null`. **والمنشور كذلك `null`:** مرجع [rules.firestore](https://firebase.google.com/docs/reference/rules/rules.firestore) يقول إن `get()` تُرجع «the document, or null if it does not exist» (قُرئ 2026-10-24). و«error» في Pyric `rules-firestore-get-missing-doc` أثر من محاكاة Test API لا من اللغة: المستند فيها غير محاكى لا غائب، ورسالته «Function not found error: Name: [get]». ولا ملاحظة `deployed` تفصل بين الاثنين (`missing-get` في `stdlib-realstorage-p3-lookup-budget-iam-enabled` يرفض في الحالين). فالمحرك يعطي `null` (S1-C)، والحالات أعلاه تبقى كما سُجّلت لأنها تقيس Test API |
| محاكاة `get()` بنتيجة `undefined` = «Service call error»؛ بنتيجة `value: null` يُرجع `get()` القيمة `null` | `aruma/limits/get_mock_null`، `aruma/limits/get_mock_value_null` | — |
| لا حدّ لعدد `get()`/`exists()`: 50 مستندًا محاكى مقبولة، و11 في Storage | `aruma/limits/exists_21`، `aruma/limits/exists_50`، `aruma/storage/fs_lookups_3`، `aruma/storage/fs_lookups_11` | المحاكي يرفض من 11 مستندًا (`aruma/limits/exists_11`). Storage المنشور يرفض المستند الثالث المختلف (`pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/three`) |
| أرقام JSON: الصحيح منها int ولو كُتب `2.0` أو `1e2`؛ ما فوق 2^53 يُقرأ double | `aruma/encoding/json_2_0_is_int`، `aruma/encoding/json_2_0_is_float`، `aruma/encoding/json_1e2_is_int`، `aruma/encoding/json_2pow53_plus1_is_int` | مستند فيه `doubleValue: 2.0` هو float في المحاكي |
| نص بصيغة RFC 3339 يصير timestamp | `aruma/encoding/json_ts_string_is_timestamp`، `aruma/encoding/json_ts_string_is_string` | في المحاكي يبقى نصًّا |
| نص يبدأ بـ `/databases/` يصير path، وخريطة `{latitude, longitude}` تصير latlng | `aruma/hidane-conditions/rules/11`، `aruma/hidane-conditions/rules/145`، `aruma/hidane-conditions/rules/150` | النص نص والخريطة خريطة في مستند حقيقي |
| `request.auth.uid` أي نوع | `aruma/encoding/auth_uid_int` | ID token: نص دائمًا |
| تقبل الطريقة `write` في الطلب وتطابقها مع `allow write` | `aruma/methods/method_write_verb` | لا طلب حقيقي طريقته `write` |
| مسار Firestore لا يُشترط أن يسمّي مستندًا (عدد مقاطع فردي مقبول) | `aruma/followups/nested_under_recursive_deep` | المحاكي يرفض المسار |
| مسار Storage ينتهي بـ `/` مرفوض: «should be URL-encoded» | `aruma/storage-paths/list_on_prefix` | — |
| `debug()` غير موجودة: «Function not found error: Name: [debug]» | `aruma/errors/debug_function`، `aruma/storage/debug` | المحاكي: `debug(x)` يُرجع `x`. المنشور: الوثائق تقول لا أثر له، ولم نتحقق |
| حجم المصدر يُقاس على رسالة الطلب (انظر وثيقة النتائج) | `aruma/followups/size_comment_262119`، `aruma/followups/size_comment_262120` | المحاكي لا يُختبر هنا |

## خلافات المحاكي (ليست من Test API)

| الخلاف | الحالات | التفصيل |
|---|---|---|
| حدّ عمليات البحث في محاكي Firestore يعدّ تقييمين | `aruma/limits/exists_get_same_6` | `exists()` و`get()` على 6 مستندات: Test API تقبل، والمحاكي يرفض بـ «Service call error» لأن التقييم الأول (الذي يفشل دائمًا لما يقرأ `resource`، firebase-tools #6252) يُحسب في الحدّ. `aruma/limits/exists_10` مقبولة في الاثنين، و`aruma/limits/exists_11` و`aruma/limits/get_11` مرفوضة في المحاكي |
| `list` في Storage | `aruma/storage/read_list`، `aruma/storage/method_list`، `aruma/storage-paths/list_on_folder` | Test API تطابق المسار المعطى نفسه مع `match`؛ المحاكي يعدّ المسار مجلّدًا ويطابق أبناءه: `list` على `p` يقبله `match /p/{f}`، و`list` على `p/a` لا يقبله. لم نتحقق من المنشور |
| `__name__` لنتيجة `firestore.get()` في محاكي Storage | `aruma/storage/fs_get_name` | المحاكي يعطي الاسم بالبادئة `projects/<p>/databases/...` كما يرسله محاكي Firestore، فالمقارنة بـ `/databases/...` تعطي `false` |

## الحالات المختلفة كلها

### موضوعاتنا

| case | condition | Test API | emulator | message |
|---|---|---|---|---|
| `aruma/encoding/json_2_0_is_float` | resource.data.g is float | deny | allow |  |
| `aruma/encoding/json_2_0_is_int` | resource.data.g is int | allow | deny | evaluation error at L14:21 for 'get' @ L14, false for 'get' @ L14 |
| `aruma/encoding/json_1e2_is_int` | resource.data.g is int && resource.data.g == 100 | allow | deny | evaluation error at L17:21 for 'get' @ L17, false for 'get' @ L17 |
| `aruma/encoding/json_ts_string_is_string` | resource.data.t is string | deny | allow |  |
| `aruma/encoding/json_ts_string_is_timestamp` | resource.data.t is timestamp | allow | deny | evaluation error at L32:21 for 'get' @ L32, false for 'get' @ L32 |
| `aruma/encoding/resource_absent_is_null` | resource == null | error | allow | Error: firestore.rules line [65], column [21]. Null value error. |
| `aruma/encoding/resource_name` | resource.__name__ == /databases/(default)/documents/c_resource_name/doc | error | allow | Error: firestore.rules line [83], column [21]. Property __name__ is undefined on object. |
| `aruma/encoding/request_resource_null_on_get` | request.resource == null | allow | error | Property resource is undefined on object. for 'get' @ L86, Property resource is undefined on object. for 'get' @ L86 |
| `aruma/encoding/auth_absent` | request.auth == null | error | allow | Error: firestore.rules line [116], column [21]. Property auth is undefined on object. |
| `aruma/errors/debug_function` | debug(true) | error | allow | Error: firestore.rules line [11], column [21]. Function not found error: Name: [debug]. |
| `aruma/limits/exists_11` | 11 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_20` | 20 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_21` | 21 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/exists_50` | 50 exists() calls on distinct documents, all mocked true | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/get_11` | 11 get() calls on distinct documents, all mocked | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [get] … |
| `aruma/limits/exists_get_same_6` | exists() and get() on each of 6 documents (12 calls, 6 documents) | allow | error | evaluation error at L6:21 for 'get' @ L6, Service call error. Function: [exists] … |
| `aruma/limits/get_unmocked` | get() of a document the test case does not mock | error | allow | Error: firestore.rules line [6], column [21]. Function not found error: Name: [get]. |
| `aruma/limits/get_mock_null` | get() mocked to return no document | error | allow | Error: firestore.rules line [6], column [21]. Service call error. Function: [get] … |
| `aruma/methods/list_query_limit` | request.query.limit <= 10 | error | deny | false for 'list' @ L68 |
| `aruma/storage-paths/list_on_folder` | list on the folder p/a | allow | deny |  |
| `aruma/storage/resource_absent_on_get` | resource == null | error | allow | Error: storage.rules line [50], column [21]. Null value error. |
| `aruma/storage/request_resource_on_get` | request.resource == null | error | allow | Error: storage.rules line [53], column [21]. Property resource is undefined on object. |
| `aruma/storage/auth_absent` | request.auth == null | error | allow | Error: storage.rules line [77], column [21]. Property auth is undefined on object. |
| `aruma/storage/read_list` | true | allow | deny |  |
| `aruma/storage/method_list` | request.method == 'list' | allow | deny |  |
| `aruma/storage/fs_get_unmocked` | firestore.get(/databases/(default)/documents/users/alice) == null | error | allow | Error: storage.rules line [134], column [21]. Function not found error: Name: [firestore.get]. |
| `aruma/storage/fs_get_id` | firestore.get(/databases/(default)/documents/users/alice).id == 'alice' | error | allow | Error: storage.rules line [137], column [21]. Property id is undefined on object. |
| `aruma/storage/fs_get_name` | firestore.get(…/users/alice).__name__ == /databases/(default)/documents/users/alice | error | deny | Error: storage.rules line [140], column [21]. Property __name__ is undefined on object. |
| `aruma/storage/debug` | debug(true) | error | allow | Error: storage.rules line [5], column [21]. Function not found error: Name: [debug]. |

### شروط hidane

| case | condition | Test API | emulator (hidane) | Test API message |
|---|---|---|---|---|
| `aruma/hidane-conditions/rules/138` | `resource.__name__ == /databases/(default)/documents/rooms/r1` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/139` | `resource.__name__ == /databases/$(database)/documents/rooms/$(room)` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/140` | `request.path == /databases/$(database)/documents/rooms/$(room)` | deny | allow | (ليس خلافًا: عندنا يُقرأ المستند من `hd0140/r1` لا من `rooms/r1`) |
| `aruma/hidane-conditions/rules/141` | `resource.id == 'r1'` | error | allow | Property id is undefined on object. |
| `aruma/hidane-conditions/rules/142` | `resource.id == room` | error | allow | Property id is undefined on object. |
| `aruma/hidane-conditions/rules/144` | `resource.__name__[4] == 'r1'` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/147` | `path('/databases/(default)/documents/rooms/r1') == resource.__name__` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/148` | `resource.__name__ == path('/databases/' + database + '/documents/rooms/r1')` | error | allow | Property __name__ is undefined on object. |
| `aruma/hidane-conditions/rules/149` | `string(resource.__name__) == '/databases/(default)/documents/rooms/r1'` | error | allow | Property __name__ is undefined on object. |

غير القابلة للمقارنة (12): الشروط التي تقرأ `one_f` أو `geo` أو `ref` أو `by`، وقيمها في `production/hidane-conditions.json`
بالوسم `comparable: false`. منها ما كشف آثار الترميز أعلاه (`rules/11` و`rules/145` و`rules/150`).
