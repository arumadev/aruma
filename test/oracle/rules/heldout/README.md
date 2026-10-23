# المجموعة المحجوبة من مرجع قواعد الأمان

**لا يقرأ هذا المجلد إلا الاختبار الأخير S1-E.** بُناة S1 (المحلّل والمقيّم والمكتبة) لا يفتحونه، ولا
يستعملونه بذورًا للـ fuzz، ولا يضبطون عليه شيئًا. لذلك يعيش في فرع مستقل، `oracle-rules-heldout`، لا
يحوي إلا `test/oracle/rules/heldout/**`؛ ومجموعة التطوير ووثائقها في `test/oracle/rules/` على الفرع
`s0-rules-oracle` (ثم `main`)، ولا شيء فيها من معرّفات هذه الحالات أو محتواها.

المخطط والمصطلحات كما في `test/oracle/rules/README.md` على فرع التطوير.

## ما فيه

```
ids.txt                    معرّف كل حالة محجوبة، سطر لكل حالة
production/<topic>.json    حالاتنا المحجوبة: ما قرّرته Rules Test API (provenance: rules-test-api)
production/raw/<topic>.json  الطلبات والردود كما هي (بلا token)
emulator/<topic>.json      الحالات نفسها على المحاكيات الرسمية
parity-exceptions.md       حيث اختلف المحاكي عن Test API في هذه الحالات
hidane/original/           rules-heldout.json لـ hidane، وأسطر heldout/* من ملف نتائج المحاكي
hidane/normalised/         نتائج المحاكي لتلك الشروط بالمخطط الموحّد
cases-src/*.mjs            وحدات تعريف الموضوعات كلها (تعرّف حالات التطوير والمحجوبة معًا)
NOTICE                     إشعار ترخيص hidane
```

## كيف اختيرت

- **حالاتنا:** كل حالة غير مثبّتة (`pin`) في وحدات `cases-src/` تُحجب إن كانت قيمة
  `sha256("aruma-s0a-rules-heldout-v1:<topic>/<id>")` (أول أربعة بايتات قسمةً على 2^32) أقل من 0.25.
  المثبّتة هي التي تستشهد بها وثيقة النتائج في فرع التطوير. الحصيلة قرابة خُمس كل الحالات؛ العدد الدقيق
  في `ids.txt`. سُجّلت المحجوبة في طلبات API منفصلة عن حالات التطوير.
- **hidane:** شروط `spikes/rules-eval/cases/rules-heldout.json` (أسماؤها `heldout/<n>`)، وقد سجّلتها hidane
  بعد كتابة مقيّماتها ولم تضبطها عليها. نتائج المحاكي لها من ملف hidane
  `results/rules-conditions-official-v1.22.0.json` (commit ‏`abd4664b0d610781eb3f0fec45642e7bc9953533`)،
  وما قرّرته Rules Test API لها في `production/hidane-conditions.json`.

## كيف يستعملها S1-E

يقيّم المحرّك كل حالة كما يقيّم حالات التطوير، ويقارن `expected.outcome` لكل حالة `provenance:
rules-test-api` أو `deployed`. حالات Test API الموسومة في `parity-exceptions.md` (هنا أو في فرع التطوير)
بأنها «أثر من Test API» تُقارن بما يفعله الإنتاج المنشور لا بحرفها.
