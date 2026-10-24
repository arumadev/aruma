# توافق Cloud Storage for Firebase: الإصدار 0.3

- **الحالة:** هيكل (S2-E، 2026-10-24) ليكتب فيه قراراته المؤقتة، كما بدأه S2-C على فرعه؛ المدمج يجمع الأقسام. يملأه S3-D بنتائج `test/compat/storage` وجدول الفروق، وS7 بأرقامه.
- **المرجع:** الفروق كلها في ADR-023 §3 بأرقامها، كل منها بمصدره وتصنيفه: **قيد** (أشد من Firebase عمدًا)، أو **مؤقت** (بلا دليل إنتاج)، أو **أصح من المحاكي**. وكل قرار مؤقت موضع واحد في الكود.

## اختبارات Firebase الرسمية

(S3-D)

## الفروق

القائمة الكاملة في ADR-023 §3 (البنود 1 إلى 37). ما يلي ما أضافته المهام بعدها، بموضعه في الكود.

### ربط القواعد (S2-E)

الربط مطابق لكل ملاحظات Pyric على bucket منشور (PY-RR، 182 من 182 مسبارًا: الإنشاء والتعديل والكتابة فوق موجود والتعديل فوق ما رفعه العميل وGCS)، ولمسبار `native-object-fields`، ولحالتي list في المحاكي (`aruma/storage/read_list` و`aruma/storage-paths/list_on_folder`)، و26 من 31 حالة `get` في Test API على مورد `default`، والخمس الباقية قراءات Firestore التي ترفض حتى v0.4 (`core/rules_oracle_test.go`).

50. **مؤقت: ترتيب `metadata` في القواعد بالمفتاح** (`rules.Map`)، فـ `values()` مرتبة كـ `keys()`. Test API وحدها تحفظ ترتيب الطلب، وهي تأخذ `request` حرفيًا؛ jsonb لا يحفظ ترتيبًا، ولا قياس من المحاكي ولا المنشور. إن قاسه تسجيل تغيّر `metadataValue` وحده، ومعه تخزين يحفظ الترتيب. `core/rules_fields.go`.
51. **مؤقت: `resource` سبعة عشر مفتاحًا:** مفاتيح `request.resource` الخمسة عشر (PY-RR) و`timeCreated` و`updated`، وغير المضبوط `null` إلا `contentEncoding` (`'identity'`، كما قاسه PY-RR في `request.resource`)، و`storageClass` = `'STANDARD'`. `core/rules_fields.go` (`existing`).
52. **مؤقت: list ببادئة لا تنتهي بـ `/` يُقرَّر على مجلدها** ومقطع مجهول، فلا تسمح قاعدة `p/a/{x}` بقائمة `p/a` التي تعيد `p/ab`. `core/rules.go` (`rulePaths`).
53. **قيد: اسم بمقاطع أكثر من `request_segments` للـ ruleset** (1024 افتراضيًا) **يُرفض 403**، لا 503. `core/rules.go` (`ruleRequest`).
54. **قيد: مصدر قواعد فيه NUL يُرفض 400**، والـ rulesets لكل مشروع حتى `rules.limits.rulesets_per_project` (2,500). `tenancy/core/rules.go`.
