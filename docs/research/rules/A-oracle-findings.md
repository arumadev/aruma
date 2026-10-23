# S0-A: ما حسمه مرجع لغة قواعد الأمان

- **الحالة:** نتائج مقيسة، 2026-10-24. تبني عليها مرحلة S1 (محرّك اللغة المشترك بين Firestore وStorage).
- **المصدر:** [test/oracle/rules/](../../../test/oracle/rules/README.md). كل ادعاء أدناه معه معرّفات الحالات التي
  تثبته؛ الحالة نفسها (القاعدة والطلب والنتيجة والرسالة) في `test/oracle/rules/production/<topic>.json`
  لمعرّفات `aruma/…`، وفي `imported/` لمعرّفات `hidane/…` و`pyric/…`.
- **من أين النتيجة:** «Test API» = ‏`firebaserules.googleapis.com/v1/projects/chatapp-8ae21:test` (تقييم بلا نشر)،
  وهو ما سجّلناه بأنفسنا. «المحاكي» = ‏cloud-firestore-emulator v1.22.0 وcloud-storage-rules-runtime v1.1.3
  كصندوقين أسودين. «المنشور» (`deployed`) = ملاحظات Pyric على Storage حقيقي بقواعد منشورة. **Test API ليست
  الإنتاج المنشور**: آثارها المعروفة في القسم 6 وفي [parity-exceptions.md](../../../test/oracle/rules/parity-exceptions.md).
  لم ننشر أي قواعد.
- **الحجم:** 689 حالة من حالاتنا في مجموعة التطوير (و142 محجوبة)، و1345 شرطًا من hidane أعدنا تقييمها عبر
  Test API، و1649 حالة Test API و274 قيمة من Storage منشور من Pyric.
- **المحجوب:** قرابة خُمس حالاتنا وشروط hidane المحجوبة في الفرع `oracle-rules-heldout`، لا يقرؤها إلا S1-E.
  هذه الوثيقة لا تستشهد إلا بحالات التطوير.

## 1. الحدود

| الحدّ | المقيس | متى يُفحص | الحالات |
|---|---|---|---|
| عمق نداء الدوال | سلسلة من 21 دالة (f1 من الشرط، ثم 20 نداءً متداخلًا) تُترجم؛ الـ 22 خطأ ترجمة: «Maximum allowed call depth of 20 is reached for [f1->…->f21] call stack.» | الترجمة | `aruma/limits/call_depth_20`، `aruma/limits/call_depth_21`، `aruma/limits/call_depth_22`، `aruma/limits/call_depth_30` |
| العودية | ممنوعة كليًّا، ولو لم تُستدعَ فعلًا: «Recursive call is not allowed.»؛ والدوران بين دالتين: «Detected a call cycle between [p,q] functions.» | الترجمة | `aruma/limits/recursion_0`، `aruma/limits/recursion_10`، `aruma/limits/mutual_recursion_4` |
| `let` في الدالة | 11 تُقبل، 12 خطأ ترجمة: «Maximum allowed variable count of 10 for a given function has been reached.» (الرقم في الرسالة 10 والمقبول 11). المعاملات لا تُحسب (5 معاملات و10 `let` مقبولة) | الترجمة | `aruma/limits/lets_9`، `aruma/limits/lets_11`، `aruma/limits/lets_12`، `aruma/limits/lets_10_params_5` |
| `let` خارج الدوال | خطأ نحوي | الترجمة | `aruma/limits/let_outside_function` |
| `let` في rules_version 1 | خطأ ترجمة (الدوال نفسها مقبولة في v1) | الترجمة | `aruma/limits/let_rules_v1`، `aruma/storage-paths/let_v1`، `aruma/storage-paths/function_v1` |
| تعقيد التعبير | «Expression is too complex to evaluate safely.» عند عمق تداخل قرابة 100: سلسلة `&&` من 99 حدًّا تُقبل و100 لا؛ أقواس متداخلة 98 تُقبل و99 لا؛ شروط ثلاثية متداخلة 48 تُقبل و49 لا (كل منها يضيف مستويين). *استنتاج:* حدّ على عمق شجرة التعبير لا على عدد عُقدها | الترجمة | `aruma/followups/and_true_99`، `aruma/limits/and_true_100`، `aruma/limits/parens_98`، `aruma/limits/parens_99`، `aruma/followups/ternary_nested_48`، `aruma/followups/ternary_nested_49` |
| التعبيرات المقيّمة | 1000 لكل طلب: قائمة حرفية من 996 عددًا ثم `size() ==` (1000 عقدة) تُقبل، و997 خطأ: «Unable to evaluate the expression as the maximum of 1000 expressions to evaluate has been reached.»؛ شجرة `&&` متوازنة بـ 256 ورقة (511 عقدة) تُقبل و512 (1023) لا | التقييم | `aruma/limits/list_literal_996`، `aruma/limits/list_literal_997`، `aruma/limits/and_balanced_8`، `aruma/limits/and_balanced_9` |
| حجم المصدر | ملف واحد اسمه `firestore.rules`: محتوى 262,119 بايت يُقبل و262,120 يُرفض (400): «Total size of sources must be smaller than 262144.» *استنتاج:* يُقاس حجم رسالة `Source` المسلسلة (المحتوى + الاسم + 25 بايت تأطير = 262,144) | الطلب | `aruma/followups/size_comment_262119`، `aruma/followups/size_comment_262120`، `aruma/limits/size_comment_256000` |
| حجم الـ ruleset المترجم | 950 دالة صغيرة تُقبل؛ 2000 تعطي `503 UNAVAILABLE` لا خطأ ترجمة. لم يُحسم الحدّ | الطلب | `aruma/limits/size_functions_950`، `aruma/limits/size_functions_2000` |
| `get()`/`exists()` | Test API لا تطبّق حدًّا (50 مستندًا محاكى مقبولة). المحاكي يرفض من 11 مستندًا مختلفًا (`Service call error`)، ويعدّ تقييمَي الشرط معًا. Storage المنشور → Firestore: مستندان مختلفان يُقبلان والثالث يُرفض، والتكرار مجاني | التقييم | `aruma/limits/exists_50`، `aruma/limits/exists_10`، `aruma/limits/exists_11`، `aruma/limits/exists_get_same_6`، `pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/two`، `pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/three`، `pyric/storage/stdlib-realstorage-p3-lookup-budget-iam-enabled/repeat` |

**الحسم:** جدول الوثائق (20) هو الصحيح لعمق النداء، وهو فحص ترجمة لا تقييم. حدّ 10/20 لعمليات البحث في
Firestore لم نقسه على الإنتاج (Test API لا تطبّقه)؛ المحاكي يطبّق 10 لطلب مستند واحد كما تقول الوثائق.

## 2. دلالات الخطأ

`E` خطأ تقييم (هنا `resource.data.missing == 1`: «Property missing is undefined on object.»). «error» = رفض
سببه الخطأ.

| التعبير | النتيجة | الحالة |
|---|---|---|
| `E \|\| true` | allow | `aruma/errors/err_or_true` |
| `true \|\| E` | allow | `aruma/errors/true_or_err` |
| `E \|\| false` | error | `aruma/errors/err_or_false` |
| `false \|\| E` | error | `aruma/errors/false_or_err` |
| `E && false` | deny (false) | `aruma/errors/err_and_false` |
| `false && E` | deny (false) | `aruma/errors/false_and_err` |
| `E && true` | error | `aruma/errors/err_and_true` |
| `true && E` | error | `aruma/errors/true_and_err` |
| `!E` | error | `aruma/errors/not_err` |
| `E ? x : y` | error | `aruma/errors/ternary_err_cond` |
| `true ? true : E` | allow (الفرع غير المأخوذ لا يُقيّم) | `aruma/errors/ternary_true_skips_err` |
| دالة لا تستعمل معاملًا قيمته خطأ | allow (المعاملات كسولة) | `aruma/errors/fn_ignores_err_arg`، `aruma/errors/fn_ignores_div_zero_arg` |
| `let x = E` لا يُستعمل | allow (`let` كسول) | `aruma/errors/let_unused_err` |
| `'horses' \|\| true`، `1 \|\| true` | allow | `aruma/errors/horses_or_true`، `aruma/errors/nonbool_int_or_true` |
| `'horses' && false` | deny (false) | `aruma/errors/horses_and_false` |
| `'abc' && true` | error: «Type error. Received: [string] Expected: [bool].» | `aruma/errors/nonbool_string_and_true` |
| `!1` | error: «Unsupported operation error. Received: !int.» | `aruma/errors/not_nonbool` |
| شرط قيمته ليست bool | error: «Type error. Received: [int] Expected: [bool].» | `aruma/errors/nonbool_condition_field` |

**القاعدة:** `||` و`&&` تبادليّان ويمتصّان الخطأ: إن حسم أحد الطرفين (`true` لـ `||`، `false` لـ `&&`) فهو
النتيجة أيًّا كان الآخر، خطأً أو قيمة ليست bool؛ وإلا فالنتيجة خطأ. القيمة غير المنطقية تُعامل كخطأ.
المحاكي يطابق Test API في كل حالات هذا القسم (`emulator/errors.json`).

أخطاء أخرى:

| الحالة | النتيجة | الحالات |
|---|---|---|
| مفتاح ناقص | «Property X is undefined on object.»؛ و`'k' in map` يعطي false؛ `get(k, d)` يعطي d، ومع مسار يمرّ بغير خريطة يعطي d أيضًا | `aruma/errors/missing_eq`، `aruma/errors/missing_key_in`، `aruma/errors/missing_get_default`، `aruma/errors/nested_get_through_scalar` |
| خاصية على null / على عدد | «Null value error.» / «Type error. Received: [int] Expected: [map,path].» | `aruma/errors/null_member`، `aruma/errors/scalar_member` |
| `==` بين نوعين | false بلا خطأ؛ `<` بينهما خطأ «Unsupported operation error» | `aruma/errors/int_eq_string`، `aruma/errors/int_ne_string`، `aruma/errors/string_lt_int`، `aruma/errors/null_lt_int` |
| قسمة int على صفر | «Divide by zero error.» | `aruma/errors/div_zero` |
| قسمة float على صفر | ±Infinity؛ `0.0/0.0` NaN، و`NaN == NaN` false | `aruma/errors/float_div_zero_inf`، `aruma/errors/neg_float_div_zero`، `aruma/errors/zero_div_zero_nan`، `aruma/math/nan_eq_nan` |
| فيض int في الجمع والطرح | «Integer value is out of the [-9223372036854775808, 9223372036854775807] range.» | `aruma/errors/int_max_plus_one`، `aruma/errors/int_min_minus_one` |
| `INT_MIN / -1` و`INT_MIN % -1` | بلا خطأ: `INT_MIN` و0 | `aruma/errors/int_min_div_neg1`، `aruma/followups/int_min_div_neg1_value`، `aruma/errors/int_min_mod_neg1` |
| `int()` لـ inf ولما فوق المدى ولـ NaN | يُشبَّع إلى `INT_MAX`، وNaN يصير 0 | `aruma/followups/int_of_inf_value`، `aruma/followups/int_of_huge_float_value`، `aruma/followups/int_of_nan_value` |
| قسمة int وباقيها | تقطع نحو الصفر؛ إشارة الباقي إشارة المقسوم | `aruma/errors/int_div_trunc`، `aruma/errors/int_mod_sign` |
| معرّف مجهول | يُترجم، وقيمته null: «Null value error.» | `aruma/errors/unknown_identifier` |
| دالة مجهولة، عدد معاملات خاطئ | يُترجم، وخطأ عند التقييم: «Function not found error: Name: [nosuch].»، «Incorrect number of arguments» | `aruma/errors/unknown_function`، `aruma/errors/wrong_arg_count` |
| `debug(x)` | Test API: «Function not found error: Name: [debug].»؛ المحاكي يقبلها | `aruma/errors/debug_function`، `aruma/storage/debug` |
| ثوابت عددية | الأس (`1e3`، `1.0e3`) غير مقبول: «Unexpected 'e3'.»؛ `9223372036854775808` «Invalid number literal»؛ `1/0` بلا مسافات يُقرأ مسارًا | `aruma/errors/float_exponent_literal`، `aruma/errors/int_literal_overflow`، `aruma/errors/div_zero_unspaced` |

## 3. سلوك الدوال

**النصوص**

- `size()` وفهرسة النص ومقارنته بوحدات UTF-16: `'😀'.size() == 2`، و`'😀a'[2] == 'a'`، و`'😀' < '￿'`.
  (`aruma/strings/size_emoji_2`، `aruma/strings/size_emoji_1`، `aruma/followups/index_emoji_utf16`،
  `aruma/strings/compare_emoji_vs_bmp`)
- `matches()` مطابقة كاملة بـ RE2 (`aruma/strings/matches_partial_is_false`). نمط حرفي غير صالح لـ RE2 خطأ
  **ترجمة** «Invalid regular expression pattern»، ولو كان `|| true`: lookahead، backreference، possessive،
  `a{1001}` (`aruma/strings/matches_lookahead`، `aruma/strings/matches_backref`،
  `aruma/strings/matches_possessive`، `aruma/strings/matches_counted_large`، `aruma/strings/matches_invalid_or_true`).
  `.` لا تطابق السطر الجديد إلا بـ `(?s)` (`aruma/strings/matches_dot_newline`، `aruma/strings/matches_dotall_flag`).
  `matches` على غير نص: «Function not found» (`aruma/strings/matches_on_int`).
- `split()` بتعبير نمطي، والفراغات في الآخر تُحذف: `'a,b,'.split(',') == ['a', 'b']`، والفراغ في الأول يبقى،
  و`''.split(',') == ['']`، و`',,'.split(',')` حجمه 1، و`'abc'.split('') == ['', 'a', 'b', 'c']`.
  (`aruma/strings/split_trailing_empty_dropped`، `aruma/strings/split_trailing_empty_kept`،
  `aruma/strings/split_leading_empty`، `aruma/strings/split_empty_string`، `aruma/followups/split_only_separators_1`،
  `aruma/followups/split_empty_sep_size_4`، `aruma/followups/split_empty_sep_first_empty`،
  `aruma/followups/split_middle_empty_kept`، `aruma/strings/split_dot_is_regex`)
- `replace()` بتعبير نمطي، لكل التطابقات، والمجموعات بـ `$1` لا `\1`؛ النمط الفارغ يُدرج بين كل حرفين
  (`aruma/strings/replace_all`، `aruma/strings/replace_regex`، `aruma/strings/replace_group_dollar`،
  `aruma/strings/replace_group_backslash`، `aruma/strings/replace_empty_pattern`).
- `lower()` و`upper()` لـ ASCII فقط: `'É'.lower() == 'É'`، و`'ß'.upper() == 'ß'`
  (`aruma/followups/lower_non_ascii_unchanged`، `aruma/followups/upper_non_ascii_unchanged`،
  `aruma/strings/upper_sharp_s`، `aruma/strings/upper_sharp_s_unchanged`، `aruma/strings/lower_e_acute`).
- `trim()` يحذف ما قيمته ≤ U+0020 فقط؛ لا يحذف U+00A0 (`aruma/strings/trim_tab_newline`، `aruma/strings/trim_nbsp`).
- الفهرسة والمدى صارمان: خارج المدى خطأ «Index out of bound error»، ونهاية مدى بعد الطول خطأ، ومدى مقلوب
  «Illegal range error» (`aruma/strings/index_out_of_range`، `aruma/strings/range_past_end`، `aruma/strings/range_reversed`).
- `string(1.0) == '1.0'`؛ `string(list)` غير مدعوم؛ `int('1.5')` و`int(' 12')` خطأ «Type conversion error»؛
  `float('1e3')` مقبول (`aruma/strings/string_of_whole_float`، `aruma/strings/string_of_list`،
  `aruma/strings/int_of_float_string`، `aruma/strings/int_of_spaced_string`، `aruma/strings/float_of_exp_string`).
- `'b' in 'abc'` غير مدعوم (`aruma/strings/in_string`)؛ هروب غير معروف (`'\q'`) والنص الخام (`r''`) لا
  يُترجمان (`aruma/strings/escape_unknown`، `aruma/strings/raw_string`)؛ `\x41` و`é` مقبولان.

**الأعداد والتجزئة**

- int يساوي float عدديًّا (`1 == 1.0`)، لكن مساواة القوائم والخرائط صارمة النوع: `[1] == [1.0]` و
  `{'a': 1} == {'a': 1.0}` و`[0.0] == [-0.0]` كلها false، بينما المجموعات والانتماء تقارن عدديًّا:
  `[1].toSet() == [1.0].toSet()`، `1 in [1.0].toSet()`، `0.0 in [-0.0]` كلها true؛ و`[1].hasAll([1.0])` false.
  (`aruma/errors/int_eq_float`، `aruma/math/int_float_list_eq`، `aruma/followups/map_eq_int_float`،
  `aruma/collections/list_neg_zero`، `aruma/followups/set_eq_int_float`، `aruma/collections/set_in_float`،
  `aruma/followups/neg_zero_in_list`، `aruma/collections/list_hasall_in_float`)
- `math.round` يقرّب النصف نحو +∞ ويعيد int (`round(-2.5) == -2`)؛ `ceil`/`floor` يعيدان float ولو أُعطيا
  int؛ `abs` يحفظ النوع ويعيد `INT_MIN` كما هو؛ `math.isInfinite` و`math.trunc` غير موجودتين.
  (`aruma/math/round_neg_half`، `aruma/math/round_neg_half_away`، `aruma/math/ceil_int`، `aruma/math/abs_int_min`،
  `aruma/math/isinfinite`، `aruma/math/trunc_absent`)
- `toBase64()` بأبجدية URL-safe مع الحشو (`-_8=`)؛ `toHexString()` بأحرف كبيرة؛ `hashing.crc32`/`crc32c`
  تعيد 4 بايتات little-endian. (`aruma/math/bytes_base64_url`، `aruma/math/bytes_base64_std`،
  `aruma/followups/base64_url_md5`، `aruma/math/md5_hex_upper`، `aruma/followups/crc32_little_endian`،
  `aruma/followups/crc32_big_endian`)
- فهرسة bytes غير مدعومة (`aruma/math/bytes_index`)؛ latlng خارج المدى خطأ (`aruma/math/latlng_out_of_range`).

**الوقت**

- `dayOfWeek()` الاثنين = 1 (الأربعاء 3)؛ `nanos()` يحمل الميكروثواني؛ `toMillis()` يقطع
  (`aruma/time/day_of_week_wed_3`، `aruma/time/day_of_week_wed_4`، `aruma/time/nanos`، `aruma/time/ts_round_trip_offset`).
- `timestamp.date` يتحقق: 30 فبراير «Date is not valid»، والسنة بين 1 و9999 (`aruma/time/ts_date_feb_30`،
  `aruma/time/ts_date_year_0`، `aruma/time/ts_date_year_10000`).
- `timestamp + int` غير مدعوم، و`timestamp == string` false (`aruma/time/ts_plus_int`، `aruma/time/ts_eq_string`).
- وحدات `duration.value`: `w d h m s ms ns`؛ `us` وغيرها خطأ برسالة «Invalid regular expression pattern.
  Pattern: us.»؛ والمقدار int فقط (`aruma/time/units_us_absent`، `aruma/time/unit_unknown`، `aruma/time/unit_float_amount`).
  أجزاء المدة السالبة سالبة كلها (`aruma/time/duration_negative_parts`)؛ `duration == 0` false (`aruma/time/duration_eq_int`).

**القوائم والخرائط**

- `list + list` غير مدعوم (`concat` هو الطريق)؛ الفهرس السالب خطأ؛ المدى بعد النهاية خطأ؛ `join` يقبل الأعداد
  (`aruma/collections/list_plus`، `aruma/collections/list_index_negative`، `aruma/collections/list_range_past_end`،
  `aruma/collections/list_join_ints`).
- `hasAll([])` true و`[].hasAny([])` false (`aruma/collections/list_hasall_empty`، `aruma/collections/list_hasany_empty`).
- `keys()` مرتّبة (`{'b':1,'a':2}.keys() == ['a', 'b']`) (`aruma/collections/map_keys_order_sorted`،
  `aruma/collections/map_keys_order_insertion`)؛ في الخريطة الحرفية المفتاح المكرر يأخذ آخر قيمة
  (`aruma/collections/map_duplicate_key`)؛ مفتاح int خطأ عند التقييم (`aruma/collections/map_int_key`)؛
  `map.hasAll` غير موجودة (`aruma/collections/map_hasall_direct`).
- `x.diff(y).addedKeys()` = مفاتيح في `x` ليست في `y`؛ القيمتان 1 و1.0 «لم تتغيّرا»؛ تغيّر داخل خريطة متداخلة
  يُعلّم المفتاح الأعلى؛ `diff(list)` غير مدعوم (`aruma/followups/diff_added_direction`،
  `aruma/followups/diff_removed_direction`، `aruma/collections/diff_int_float_changed`، `aruma/collections/diff_nested`،
  `aruma/collections/diff_not_map`، `aruma/collections/diff_on_update`).

**المسارات و`match`**

- `$(x)` يُدرج مقطعًا واحدًا ولو حوى `/` (`aruma/paths/interpolate_slash`، `aruma/paths/interpolate_slash_size`)؛
  `path == string` false، و`string(path)` مدعوم في Firestore، و`path + string` لا (`aruma/paths/path_eq_string`،
  `aruma/paths/path_string`، `aruma/paths/path_concat`).
- rules_version 2: ‏`{rest=**}` قيمته path، ويطابق صفر مقاطع، ويجوز أن يتبعه مقاطع، ولا يجوز اثنان في مسار
  واحد. rules_version 1 (وغياب السطر): ‏`{rest=**}` في آخر المسار فقط، ويطابق مقطعًا أو أكثر، وقيمته في Test API
  **path أيضًا** لا نص (`aruma/paths/wild_v2_rest_is_path`، `aruma/paths/wild_v2_zero_segments_doc`،
  `aruma/paths/wild_v2_not_final`، `aruma/paths/wild_v2_two_recursive`، `aruma/paths/wild_v1_not_final`،
  `aruma/paths/wild_v1_zero_segments_doc`، `aruma/paths/wild_v1_rest_is_string`، `aruma/followups/v1_rest_is_path`،
  `aruma/followups/v1_rest_eq_path`، `aruma/storage-paths/v2_zero_segments`، `aruma/storage-paths/v1_zero_segments`).
- match متداخل والـ glob. الفرق هو موضع الـ glob في مسار الكتلة الخارجية **نفسها** (لا في المسار الكامل).
  المحاكي يطابق Test API في كل ما يلي (`emulator/nesting.json`، `emulator/nesting-storage.json`):
  - **تحت كتلة ينتهي مسارها هي بـ `{x=**}`** (الـ glob أخير فيها)، في rules_version 2: يُقبل على أي عمق،
    ومع صفر مقاطع: `match /p/{r=**} { match /docs/{d} }` يغطّي `p/docs/d1` و`p/a/b/c/docs/d1`
    (`aruma/nesting/final_v2_zero`، `aruma/nesting/final_v2_depth_1`، `aruma/nesting/final_v2_depth_3`،
    `aruma/paths/wild_v2_nested_under_recursive`، `aruma/storage-paths/v2_nested_under_recursive`،
    `aruma/followups/nested_under_root_recursive`، `aruma/followups/nested_under_recursive_var`،
    `aruma/nesting-storage/storage_final_depth`). في rules_version 1 وبلا سطر الإصدار: لا يطابق
    (`no-matching-statement`) (`aruma/nesting/final_v1_depth_1`، `aruma/nesting/final_noversion_depth_1`،
    `aruma/nesting/final_v1_depth_3`، `aruma/nesting-storage/storage_final_v1`).
  - **تحت كتلة يتبع الـ glob فيها مقاطعُ أخرى** (`match /{g=**}/groups/{id} { match /members/{m} }`): لا يطابق
    (`no-matching-statement`)، لا على `groups/g1/members/m1` ولا على `orgs/o1/groups/g1/members/m1`، والكتلة
    الخارجية نفسها تطابق مستنداتها؛ وكذلك `/p/{r=**}/q/{id}` > `/docs/{d}` وصيغة Storage
    (`aruma/nesting/nonfinal_groups_top`، `aruma/nesting/nonfinal_groups_deep`،
    `aruma/nesting/nonfinal_groups_outer_doc`، `aruma/nesting/nonfinal_pq`، `aruma/nesting/nonfinal_pq_zero`،
    `aruma/nesting-storage/storage_nonfinal`، `aruma/nesting-storage/storage_nonfinal_top`). الصيغة المسطّحة
    `match /{r=**}/groups/{g}/members/{m}` تطابق الاثنين (`aruma/nesting/flat_groups_top`،
    `aruma/nesting/flat_groups_deep`). أي إن التداخل ليس مجرد وصل للمسارين.
  - glob واحد في المسار كله، المتداخل منه أيضًا: `{s=**}` في كتلة داخل `/a/{r=**}` خطأ ترجمة «Invalid glob
    match expression. Only one glob match is permitted in a match declaration path.»؛ وفي rules_version 1 الـ
    glob غير الأخير «Glob matches are permitted as the last segment in a match declaration path.»
    (`aruma/nesting/nested_second_glob`، `aruma/nesting/v1_nonlast_glob`).
  - الربط: `match /{document=**} { match /notes/{noteId} }` على `notes/n1/notes/n2` يعطي
    `document == /notes/n1` و`noteId == 'n2'` (`aruma/nesting/binding_both`، `aruma/nesting/binding_document`،
    `aruma/nesting/binding_note_id`).
- قاعدة على `/c/{d}` لا تغطّي `/c/{d}/sub/{e}`؛ الدالة على المستوى الأعلى لا ترى متغيرات match
  («Null value error.»)، والمعرّفة داخل match تراها؛ wildcard داخل مقطع خطأ؛ `rules_version = '3'` خطأ
  (`aruma/paths/nested_parent_rule_not_inherited`، `aruma/paths/function_outer_var_not_visible`،
  `aruma/paths/function_sees_match_var`، `aruma/paths/partial_segment_wildcard`، `aruma/paths/version_3`).

**الطرق**

- `read` = `get` + `list`، و`write` = `create` + `update` + `delete`؛ `allow get` لا يغطّي `list`، و`allow create`
  لا يغطّي `update` (`aruma/methods/read_list`، `aruma/methods/write_delete`، `aruma/methods/get_list`،
  `aruma/methods/create_update`)؛ `allow get;` بلا شرط يقبل (`aruma/methods/allow_without_condition`)؛ match بلا
  عبارة خطأ ترجمة (`aruma/methods/no_allow_statement`)؛ فعل مجهول (`allow fetch`) يُترجم ولا يطابق شيئًا
  (`aruma/followups/allow_unknown_verb_word`).

**Storage**

- حقول `resource` بأنواعها: `size` int، و`timeCreated`/`updated` timestamp، و`metadata` خريطة؛ `size` نصًّا
  (`'1024'`) لا يساوي 1024 (`aruma/storage/resource_size`، `aruma/storage/resource_time_created_is_ts`،
  `aruma/storage/resource_size_string`). `request.path` من نوع path (`aruma/storage/request_path`،
  `aruma/storage/request_path_string`). لا متغير `database` في Storage: يُكتب `(default)`
  (`aruma/storage/fs_database_var`). `firestore.get()`/`firestore.exists()` تعمل بالمحاكاة (`aruma/storage/fs_get`،
  `aruma/storage/fs_exists`).
- المنشور (Pyric): `request.resource` في create فيه 15 مفتاحًا، ومنها `name` وليس منها `timeCreated`
  (`pyric/storage/stdlib-realstorage-request-resource-fields/create/keys-size-15`،
  `pyric/storage/stdlib-realstorage-request-resource-fields/create/present-name`،
  `pyric/storage/stdlib-realstorage-request-resource-fields/create/present-timeCreated`).

## 4. المحاكي مقابل Test API

617 حالة متطابقة من 646 شُغّلت، و1324 من 1333 شرطًا من hidane. لم نجد خلافًا في دلالات اللغة (العوامل،
المكتبة، الأخطاء، المسارات): كل الخلافات آثار من Test API (القسم 6) أو من طريقة المحاكي في بناء الطلب، إلا:

- حدّ عمليات البحث يطبّقه المحاكي (من 11 مستندًا) ويعدّ تقييمين (`aruma/limits/exists_get_same_6`).
- `debug()` يعمل في المحاكي فقط.
- `list` في Storage: المحاكي يطابق أبناء المسار، وTest API المسار نفسه (`aruma/storage/read_list`،
  `aruma/storage-paths/list_on_folder`).
- `request.resource` في قراءة: غير معرّف في محاكي Firestore، وnull في محاكي Storage.

التفصيل حالةً حالة في [parity-exceptions.md](../../../test/oracle/rules/parity-exceptions.md).

## 5. ما لم تستطع Test API التعبير عنه

- **مستند غائب على حقيقته:** المورد يُعطى حرفيًّا؛ `resource: null` أقرب شيء، ولا يعطي `resource.id` ولا
  `resource.__name__` ولا يشتق شيئًا من المسار.
- **`request` الحقيقي:** يُعطى حرفيًّا، فلا نعرف منه ما يضعه الإنتاج في `request.auth.token` ولا `request.query`
  الحقيقي لاستعلام، ولا الحقول التي يضعها Storage في `request.resource` (عرفناها من Pyric المنشور).
- **قيم Firestore المنمّطة:** لا float صحيح (`2.0` يصير int)، ولا int فوق 2^53 بدقّة، ولا bytes ولا reference؛
  وفي المقابل تحوّل النصوص التي تشبه الوقت أو المسار، وخرائط `{latitude, longitude}`، إلى timestamp وpath وlatlng.
- **حدود البحث** (10/20 في Firestore، 2 في Storage): لا تُطبّق مع المحاكاة.
- **`debug()`**، والطرق المجمّعة الحقيقية (batch/transaction بحدّ 20)، و`getAfter()`/`existsAfter()` في كتابة
  متعددة، وطلبات `list` الحقيقية بقيودها (إثبات الاستعلام).
- **حدّ الـ ruleset المترجم:** يجيب 503 بدل خطأ واضح.
- **ملفات متعددة:** ملف واحد لكل طلب.

## 6. آثار Test API المعروفة

كل منها في جدول «آثار Test API» في [parity-exceptions.md](../../../test/oracle/rules/parity-exceptions.md) مع
حالاته: غياب `auth` (لا `null`) يجعل `request.auth == null` خطأً؛ غياب `resource` كذلك؛ نتائج `get()` المحاكاة
بلا `id`/`__name__`؛ `get()` غير المحاكى «Function not found»؛ لا حدود بحث (Storage المنشور يرفض الثالث)؛ ترميز
JSON أعلاه؛ قبول الطريقة `write`؛ مسارات Firestore لا تسمّي مستندًا؛ `debug()` مفقودة.
