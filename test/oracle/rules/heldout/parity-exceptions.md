# حيث يختلف المحاكي عن Rules Test API (المجموعة المحجوبة)

مقارنة الحالات المحجوبة كما في `test/oracle/rules/parity-exceptions.md` على فرع التطوير: فئة النتيجة
(`allow` أو `deny` أو `error`) من Rules Test API (`production/`) مقابل المحاكي الرسمي (`emulator/`،
cloud-firestore-emulator v1.22.0 وcloud-storage-rules-runtime v1.1.3 على Java 21)، وشروط hidane المحجوبة
مقابل ما سجّلته hidane من المحاكي. أنواع الآثار مشروحة هناك.

ملاحظات على ما يظهر هنا وحده:

- `aruma/encoding/json_2pow53_plus1`: Test API تقرأ رقم JSON فوق 2^53 على أنه double فتفقد دقّته؛ المحاكي
  يأخذه `integerValue`. أثر ترميز.
- `aruma/encoding/resource_id` و`aruma/limits/exists_unmocked` وشرطا hidane: آثار Test API المعروفة
  (لا `id` ولا `__name__` في المورد، و`exists()` غير المحاكى «Function not found»).
- `aruma/storage-paths/v2_rest_string`: Test API تقبل `string(rest)` لمتغير `{rest=**}` (وهو path)، والمحاكي
  يقول «Unsupported operation error. Received: string(path)». خلاف حقيقي بين الاثنين؛ المنشور غير معروف.
- `aruma/storage-paths/encoded_name`: Test API تفكّ `%20` في مسار الكائن قبل المطابقة، ومحاكي Storage لا.
- `aruma/storage/request_method`: `request.method` غير معرّف في محاكي Storage كما يقوده firebase-tools
  (لا يضعه في المتغيرات)، وموجود في Test API.

## الحالات المختلفة كلها

Our topics: 128 same, 6 different, 8 not run on the emulator.

| case | condition | Test API | emulator | message |
|---|---|---|---|---|
| `aruma/encoding/json_2pow53_plus1` | resource.data.g == 9007199254740993 | deny | allow |  |
| `aruma/encoding/resource_id` | resource.id == 'doc' | error | allow | Error: firestore.rules line [11], column [21]. Property id is undefined on object. |
| `aruma/limits/exists_unmocked` | exists() of a document the test case does not mock | error | allow | Error: firestore.rules line [6], column [21]. Function not found error: Name: [exists]. |
| `aruma/storage-paths/v2_rest_string` | v2: string({rest=**}) | allow | error | Error: storage.rules line [5], column [21]. Unsupported operation error. Received: string(path). Expected: string(int), string(float), string(null), string(stri |
| `aruma/storage-paths/encoded_name` | an object name given percent-encoded | allow | deny |  |
| `aruma/storage/request_method` | request.method == 'get' | allow | error | Error: storage.rules line [17], column [21]. Property method is undefined on object. |

hidane's conditions: 97 same, 2 different, 7 not comparable.

| case | condition | Test API | emulator (hidane) | Test API message |
|---|---|---|---|---|
| `aruma/hidane-conditions/heldout/37` | `resource.__name__[3] == 'rooms' && resource.__name__[4] == room` | error | allow | Error: firestore.rules line [116], column [22]. Property __name__ is undefined on object. |
| `aruma/hidane-conditions/heldout/38` | `request.path[4] == resource.id` | error | allow | Error: firestore.rules line [119], column [41]. Property id is undefined on object. |
