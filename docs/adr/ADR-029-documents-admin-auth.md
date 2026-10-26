# ADR-029: قاعدة المستندات: مصادقة Admin بـ JWT حساب الخدمة الموقّع ذاتيًا

- الحالة: مسودة
- التاريخ: 2026-10-26
- المرحلة: F0 من الإصدار 0.4 (قاعدة المستندات، Document Database)
- **يحل محل:** جزء من ADR-010 S-3، وS-11 على هذا السطح (`PERMISSION_DENIED` لا `NOT_FOUND` لمشروع غير مشروع الطالب، القاعدة 6)، على سطح قاعدة المستندات وحده (البند 1). وADR-010 لا يُعدَّل.
- المراجع: ADR-004 (عقد رمز المستخدم ومُصدِره)، ADR-007 (المعرّفات وبريد حساب الخدمة)، ADR-010 (S-3، S-11، S-15، §2)، ADR-022 §14 (مصادقة واجهة GCS بـ `aruma_sat_`)، ADR-025 §7.1 (تصنيف الطالب)، ADR-026 §4 (إغلاق البث عند `exp`)، ADR-027 (النقل وh2c وTLS)، ADR-028 (النطاق)، و:
  - تجربة F0 للنقل والمصادقة `.cache/briefs/research/spike-transport-auth.md` (**SP**، خارج git)، أقسام «What each client sends» و«Admin auth findings» و«Network guard».
  - الخطة المعتمدة `.cache/briefs/plan-v04-firestore.html` (**PL**): صف «ADR-010 تعديل» في القسم 4، والقرار 2 في القسم 9 (الخيار (أ) توصية، و(ب) حزمة مساعدة).
  - الكود:
    - `internal/platform/authn/authn.go:146-171` (`authenticate`: يرفض اليوم `owner` وكل JWT)؛
    - `internal/tenancy/core/oauth.go` (`ExchangeAssertion`، و`verifyWithAny`، و`clockSkew`، و`maxAssertionLifetime`)؛
    - `internal/tenancy/core/dataplane.go` (`VerifyServiceAccountJWT`، و`ErrOtherProject`)؛
    - `internal/tenancy/core/serviceaccounts.go` (`ServiceAccountEmail`، و`CreateServiceAccountKey`)؛
    - `internal/configgen/configgen.go` (`ServiceAccountKeyFile`، و`TokenURL`)؛
    - `internal/services/storage/gcs/auth.go` و`sdk/go/adminstorage`؛
    - `internal/config/config.go:89-91` و`:570` (العلم `rtdb.dev_open_rules` وشرط loopback فيه)، و`:45-46` (`trusted_proxies`)، و`internal/platform/httpx/clientip.go` (`PeerIsLoopback`).

## السياق

- **ADR-010 S-3:** «`Bearer owner` وأي JWT بـ `alg: none` مرفوضان دائمًا، ويُسجَّل الرصد». والكود أشد: `authn.authenticate` يرفض كل Bearer على شكل JWT، فلا يقبل إلا `aruma_pat_` و`aruma_sat_`.
- **Storage حلّها بحزمة مساعدة** (`sdk/go/adminstorage`، ADR-022 §14). تبادل الحزمة JWT الحساب في `/oauth2/v1/token` برمز `aruma_sat_`، ويتغير سطر واحد في التطبيق.
- **التجربة قاست ما ترسله مكتبتا Admin إلى Firestore** (SP، Q1-Q3): **كلتاهما ترسل JWT موقّعًا ذاتيًا بـ RS256 مباشرة إلى الخدمة في كل RPC، ولا تتصل بـ `token_uri` أبدًا،** سواء سمّى ملف المفتاح عنوان Google أو عنوان Aruma.

| | Go `cloud.google.com/go/firestore` 1.29.0 | Node `firebase-admin` 14.5.0 |
|---|---|---|
| `alg` / `typ` | `RS256` / `JWT` | `RS256` / `JWT` |
| `kid` | `private_key_id` | **غائب**: `cert()` يعطي `@google-cloud/firestore` البريد والمفتاح وحدهما |
| `iss`، `sub` | `client_email` كلاهما | `client_email` كلاهما |
| `scope` | `https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/datastore` | مثله |
| `aud` | موجود، نص فارغ | غائب |
| `exp − iat` | 3600 ثانية | 3600 ثانية |
| النقل | يُرسل على h2c كما على TLS | على gRPC مع `ssl:true` وحده، ومع `ssl:false` يرسل `Bearer owner` ويتجاهل المفتاح. وعلى REST (`preferRest`) يرسل الـ JWT مع `ssl:false` أيضًا |

- **وضع المحاكي:** Go مع `FIRESTORE_EMULATOR_HOST` يرسل `Bearer owner`.
- **معيار v0.4:** يشغّل أي مشروع Firebase نموذجي دون تعديل (ADR-028). والخيار (ب) في الخطة يغيّر كود كل تطبيق Admin.

## القرار

### 1. ما يحل محله من ADR-010 S-3

**حين يُعتمد هذا السجل، وعلى سطح قاعدة المستندات وحده**، وهو:
- gRPC `google.firestore.v1.Firestore` و`google.firestore.admin.v1.FirestoreAdmin` و`google.longrunning.Operations`؛
- REST `/v1/projects/{p}/databases…`؛
- WebChannel (ADR-027 §2، §5).

يتغير فيه أمران:

1. **JWT موقّع بـ RS256 بمفتاح حساب خدمة يُقبل** بالقواعد في البند 3.
2. **`Bearer owner` يُقبل في حالة واحدة:** حين يفعّله `documents.dev_owner_token: true` صراحة (معطّل افتراضيًا)، ويكون `public_url` و`listen` كلاهما loopback، ويكون نظير الطلب loopback (البند 6). ويُرفض ويُسجَّل في كل حالة أخرى، كما يقول S-3.

**ويبقى من S-3 بلا تغيير:**
- JWT بـ `alg: none` مرفوض دائمًا، ويُسجَّل؛
- وعلى كل سطح آخر (Management API، وواجهة Auth الإدارية، وGCS، وRTDB، واللوحة) يبقى S-3 كما هو، ويبقى الـ JWT مرفوضًا.

وفي الكود يصير القبول **علمًا في `authn.Policy` تضعه سياسة قاعدة المستندات وحدها** (`ServiceAccountJWT`، ومعه `DevOwner`). واختبار يفشل إن وضعته سياسة أخرى.

### 2. الاعتمادات المقبولة على السطح

| الاعتماد | من يرسله | يصير |
|---|---|---|
| JWT حساب الخدمة الموقّع ذاتيًا | Admin Go وNode دون تعديل | حساب خدمة (البند 3) |
| `aruma_sat_` | تطبيق يبني مصدر رموزه بنفسه ويتبادل في `/oauth2/v1/token` (البند 5) | حساب خدمة، بـ `VerifyServiceToken` الموجود. والنطاقات المسجّلة مع الرمز تخضع لقاعدة البند 3.4 |
| `aruma_pat_` لحساب يملك المشروع | firebase-tools (`deploy --only firestore`، و`firestore:delete -r`) | مدير المشروع («رمز مالك المشروع» في ADR-025 §7.1)، كما في Management API |
| `aruma_pat_` لأي حساب آخر (عضو بدور آخر، أو غير عضو) | — | **يُرفض `PERMISSION_DENIED`** قبل أي بحث في البيانات. وربط الأدوار بقاعدة المستندات سجل جديد إن احتيج |
| `Bearer owner` | المكتبات في وضع المحاكي، وNode بـ `ssl:false` على gRPC | مدير، بشروط البند 6 وحدها |
| رمز Firebase ID | المستخدم النهائي | مستخدم تحكمه القواعد (ADR-021، F5)، خارج هذا السجل |
| لا شيء | المستخدم النهائي دون دخول | مجهول تحكمه القواعد |

**التمييز بين JWT حساب الخدمة ورمز ID بالمُصدِر:**
- رمز ID مُصدِره عنوان (ADR-004)، فيذهب إلى التحقق من رموز ID؛
- و`iss` الذي هو بريد ويساوي `sub` يذهب إلى البند 3؛
- وما سوى ذلك `UNAUTHENTICATED`.

ولا يُجرَّب تحقق بعد فشل آخر.

### 3. قواعد التحقق من JWT حساب الخدمة

1. **الشكل:**
   - `Authorization: Bearer <ثلاثة مقاطع base64url>`؛
   - `alg` في الترويسة `RS256` بالضبط: لا `none` (يُسجَّل كما يقول S-3)، ولا HS* (مفتاح عام سرًّا)، ولا خوارزمية أخرى؛
   - و`typ`، إن وُجد، هو `JWT`.
2. **الهوية:**
   - `iss` غير فارغ، و`sub == iss`؛
   - `iss` هو `client_email` لحساب خدمة موجود وغير معطّل، ومشروعه `ACTIVE`.
3. **التوقيع:**
   - **`kid` موجود:** يجب أن يكون `private_key_id` (عمود `key_id`) لمفتاح نشط لذلك الحساب، أي غير معطّل وداخل `valid_after` و`valid_before`. ولا يُجرَّب مفتاح آخر إن لم يطابق.
   - **`kid` غائب:** يُجرَّب كل مفتاح نشط للحساب بالترتيب، لأن firebase-admin لا يرسل `kid` أبدًا.
   - **هذا ما يفعله `verifyWithAny` حرفًا** (`tenancy/core/oauth.go`)، وهو نفسه في `ExchangeAssertion` و`VerifyServiceAccountJWT`. ويُفحص التوقيع قبل أي شيء عن الحساب، فالحساب المجهول والمعطّل والمحذوف يبدون سواء دون مفتاحه.
4. **النطاق والجمهور:**
   - **`scope`** (مفصول بمسافات) **يجب أن يحوي** `https://www.googleapis.com/auth/datastore` أو `https://www.googleapis.com/auth/cloud-platform`. ورمز بلا `scope` يُرفض.
     - التجربة لم ترَ عميلًا يرسل الشكل ذا الجمهور وحده.
     - وGo قد يرسل `aud=https://firestore.googleapis.com/` بلا نطاق في بعض خياراته (استنتاج من الكود، لم يُرصد).
     - إن ظهر في مجموعات SDK، يُقبل بسجل جديد.
   - **`aud`** إما:
     - غائب، أو نص فارغ؛
     - `https://firestore.googleapis.com/`؛
     - أو `public_url` لـ Aruma، يُقارن بعد حذف `/` واحدة من آخره.

     وإن كان مصفوفة، فكل عنصر فيها من هذه القيم. **وأي قيمة أخرى تُرفض.** هذا يمنع إعادة إرسال assertion كُتب لـ `/oauth2/v1/token` (جمهوره عنوان نقطة الرموز) إلى سطح البيانات.
5. **الوقت** (`clockSkew` = 60 ثانية، و`maxAssertionLifetime` = ساعة، من `oauth.go`):
   - `iat ≤ now + 60 s`؛
   - `exp > now − 60 s`؛
   - `exp > iat`؛
   - `exp − iat ≤ 3600 s + 60 s`.
6. **المشروع:** **كل** اسم مورد في الطلب أو رسالة البث يُحلَّل إلى المشروع والقاعدة نفسيهما، والمشروع مشروع الحساب: `google-cloud-resource-prefix`، و`database` و`name` و`parent` في الجسم، ومسار REST، وكل اسم مستند في الكتابات والتحويلات والشروط والقراءات، وكل هدف Listen، ومعرّف المعاملة. القائمة الكاملة والقاعدة الواحدة في ADR-027 §2.2، ويُتحقق منها كلها قبل أي عمل. **فلا يكفي حقل واحد يطابق** بينما يعمل الـ core على اسم في حقل آخر.
   - **الاختلاف `PERMISSION_DENIED`**، كـ `ErrOtherProject` في `dataplane.go`، أو نص الإنتاج إن سُجّل (ADR-028 §7.6). وهو استثناء من S-11 على هذا السطح بحجة ADR-022 §14 نفسها: معرّف المشروع في إعداد كل تطبيق، والرفض يقع قبل أي بحث في البيانات.
   - **وكل فشل آخر `UNAUTHENTICATED`،** وعليه تجدد المكتبات رمزها (ADR-026 §4).
7. **الاتصال غير المشفّر:**
   - Go يرسل الـ JWT على h2c، ومن رآه على الطريق يعيد استعماله حتى `exp`.
   - **على طلب وصل دون TLS** (`r.TLS == nil`) يُقبل الاعتماد في حالتين فقط، والنظير هو `RemoteAddr` لا `X-Forwarded-For`:
     1. **خادم تطوير:** `public_url` و`listen` كلاهما loopback (فحص إعداد، كما في `owner`)، **و**النظير loopback. فلا مسار إلى الخادم إلا من الجهاز نفسه.
     2. **وراء وكيل موثوق:** النظير داخل `trusted_proxies` (قائمة CIDR في الإعداد، **فارغة افتراضيًا**، `config.go:45-46`)، **و**`X-Forwarded-Proto` هو `https` بالضبط. **والغائب يُعامل كاتصال غير مشفّر**، فيُرفض. والوكيل يجب أن يضع الترويسة بنفسه لا أن يمرر ترويسة العميل، والوثائق تقول ذلك (Caddy يفعله افتراضيًا).
   - **ولا يكفي أن يكون النظير loopback على خادم غير loopback:** وكيل على الجهاز نفسه (Caddy إلى `127.0.0.1`، أشيع نشر) يجعل كل عميل يبدو محليًا، ومنه عميل على منفذ `:80` غير مشفّر (`PeerIsLoopback`، `clientip.go:52-53`). فهذا الوكيل يُدرج في `trusted_proxies` ويرسل `X-Forwarded-Proto`، كأي وكيل.
   - **والقاعدة نفسها لـ `aruma_sat_` و`aruma_pat_` على هذا السطح،** لأن إعادة استعمالهما ممكنة بالمثل.
   - والرفض `UNAUTHENTICATED` بنص يقول إن الاعتماد يحتاج TLS، وتحذير في السجل بعنوان العميل دون الرمز (S-15).
   - **قيمة القاعدة كشف الإعداد الخاطئ بصوت عالٍ، لا منع إعادة الاستعمال:** اعتماد أُرسل مرة بلا تشفير مكشوف حتى `exp`، ومن التقطه يعيده عبر TLS فيُقبل. والعلاج حذف المفتاح، ويسري خلال 60 ثانية (القاعدة 8).
8. **التخزين المؤقت والانتهاء:**
   - **الرمز المتحقَّق منه يُخبَّأ ببصمة SHA-256** مدة أقصاها 60 ثانية، ولا يتجاوز `exp`. كل RPC في Go وNode يحمل الـ JWT نفسه حتى ساعة، فالتخبئة تغني عن تحقق RSA في كل طلب. وحدّ الـ 60 ثانية يُبقي وعد `DeleteServiceAccountKey` («assertions signed with it stop working at once») في حدود دقيقة.
   - **Listen وWrite يحملان الرمز مرة واحدة، عند الفتح.**
     - بث حساب الخدمة يُغلق بـ `UNAUTHENTICATED` عند `exp` (ADR-026 §4).
     - ويعيد التحقق من الحساب والمفتاح كل 60 ثانية، فيُغلق بالرمز نفسه إن عُطّل الحساب أو حُذف المفتاح أو المشروع.

### 4. من أين تأتي المفاتيح العامة

**من حسابات الخدمة التي يصدرها Aruma اليوم، بلا تخزين جديد ولا نوع مفتاح جديد:**

- **الحساب:** `CreateServiceAccount` من اللوحة أو Management API. بريده `<name>@<project_id>.sa.<project_domain>` (`ServiceAccountEmail`، ADR-007).
- **المفتاح:** `CreateServiceAccountKey` يولّد زوج RSA-2048.
  - **المفتاح الخاص** (PKCS#8) يُعاد مرة واحدة ولا يُخزَّن (ADR-010 §2)، في ملف المفتاح الذي يرسمه `configgen.ServiceAccountKeyFile`، وفيه:
    - `private_key_id` = `key_id`؛
    - `client_email`؛
    - `token_uri` = `<public_url>/oauth2/v1/token`.
  - **ويُحفظ وحده** شهادة X.509 موقّعة ذاتيًا في `control.service_account_keys.certificate_pem`.
- **البحث:** `ListActiveKeysByEmail(iss)`، وهو ما يخدمه `/oauth2/v1/x509/{email}` وما يتحقق به `ExchangeAssertion`. يرجع المفاتيح غير المعطّلة داخل مدة صلاحيتها، ثم يُفحص مشروع الحساب (القاعدة 6).
- **مفتاح حساب خدمة من Google** (`…iam.gserviceaccount.com`) لا مفتاح له هنا، فيُرد `UNAUTHENTICATED`. والوثائق تقول: نزّل ملف المفتاح من لوحة Aruma.

**مكان الكود:** طريقة جديدة في `tenancy/core` بجانب `ExchangeAssertion`، تشاركها `verifyWithAny` و`clockSkew` و`maxAssertionLifetime`. وتصل إليها `authn` عبر طريقة جديدة في `Verifier`، فلا تتكرر قواعد الوقت والتوقيع في مكانين.

### 5. `token_uri` و`/oauth2/v1/token`

- **لا حاجة لتوجيه `token_uri`:** المكتبتان لا تطلبانه لـ Firestore، بعنوان Google أو بعنوان Aruma (SP، Q1). وملفات مفاتيح Aruma تشير إليه على كل حال (`configgen.TokenURL`)، لما يحتاجه غير Firestore.
- **`/oauth2/v1/token` يبقى يعمل كما هو** لتطبيق يبني مصدر رموزه بنفسه، مثل `google.JWTConfigFromJSON(key, scopes…).TokenSource` في Go، يمرّره بـ `option.WithTokenSource`.
  - **بملف مفتاح Aruma:** يصل `/oauth2/v1/token` assertion فيه:
    - الترويسة `{alg=RS256 kid=<private_key_id>}`؛
    - المطالبات `{iss=<client_email> aud=<token_uri> scope=… iat=now-10s exp=iat+3600}`، بلا `sub`.

    و`ExchangeAssertion` يقبله كما هو ويعيد `aruma_sat_`، يقبله هذا السطح (البند 2).
  - **بملف فيه `token_uri` لـ Google ودون شبكة:** يفشل كل RPC، ومنه أول لقطة في Listen، بـ `UNAUTHENTICATED: … oauth2: cannot fetch token: Post "https://oauth2.googleapis.com/token"`. والوثائق تقول: استعمل ملف المفتاح من Aruma.
- **الاتجاهان منفصلان:**
  - assertion نقطة الرموز (جمهوره `token_uri`) لا يُقبل على سطح البيانات (القاعدة 4)؛
  - ورمز السطح (جمهوره فارغ أو غائب) لا يقبله `ExchangeAssertion`، لأنه يشترط جمهوره.

### 6. `Bearer owner`

- **يُقبل على هذا السطح وحده، بثلاثة شروط معًا،** كـ `rtdb.dev_open_rules` (`config.go:89-91`، `:570`) وكما يشترط MUST 25 («only in an explicit dev/emulator mode»):
  1. **علم صريح** `documents.dev_owner_token: true` (و`ARUMA_DOCUMENTS_DEV_OWNER_TOKEN`)، **معطّل افتراضيًا**؛
  2. **والإعداد loopback:** العلم يُرفض عند التحقق من الإعداد إن لم يكن `public_url` و`listen` كلاهما loopback، برسالة كرسالة `dev_open_rules`؛
  3. **والنظير loopback** (`PeerIsLoopback`). وهذا لا يكفي وحده، لأن وكيلًا على الجهاز يجعل كل عميل محليًا، لكنه مع الشرط 2 لا يترك مسارًا من خارج الجهاز.
- يصير الطالب مديرًا لكل مشروع على الخادم (ADR-025 §7.1)، ويُسجَّل سطر تحذير واحد عند الإقلاع. وبلا العلم، أي عملية أو حاوية جانبية على الجهاز كانت ستصير مديرًا لكل مشروع.
- **في كل حالة أخرى يُرفض ويُسجَّل** كما يقول S-3. والنص يقول ما العمل: «`Bearer owner` is accepted only when documents.dev_owner_token is set on a server whose public_url and listen are loopback addresses; connect with TLS to send the service account's credentials».

**مع Node بـ `ssl:false`:** firebase-admin يرسل `Bearer owner` على gRPC ويتجاهل ملف المفتاح، ويرسل الـ JWT على REST وحده (`preferRest`). فالنتيجة:

| الحالة | gRPC (الافتراضي) | REST (`preferRest`) |
|---|---|---|
| خادم تطوير على loopback مع `documents.dev_owner_token` | يعمل: `owner` يُقبل | يعمل: JWT على اتصال غير مشفّر من loopback (القاعدة 7، الحالة 1) |
| خادم تطوير على loopback بلا العلم | **يُرفض:** `owner` مرفوض، والمفتاح لا يُرسل | يعمل (القاعدة 7، الحالة 1) |
| خادم غير loopback، والعميل بلا TLS، من الجهاز نفسه أو من الشبكة أو عبر وكيل لا يرسل `X-Forwarded-Proto: https` | **يُرفض دائمًا** | يُرفض (القاعدة 7) |
| أي خادم، والعميل بـ `ssl:true` (الافتراضي) عبر وكيل TLS في `trusted_proxies` يرسل `X-Forwarded-Proto: https` | يعمل: JWT | يعمل: JWT (القاعدة 7، الحالة 2) |

فـ **Admin Node أمام خادم حقيقي يحتاج TLS** (البند 7 في ADR-027): جهة عامة، أو جهة خاصة مع `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH`. وتطبيق Node بلا TLS لا يكسب شيئًا من القواعد 1-7 على gRPC أصلًا. وهذا سطر في وثائق قاعدة المستندات.

و**Go** في وضع المحاكي (`FIRESTORE_EMULATOR_HOST`) يُعامل كـ Node على gRPC. وGo بملف مفتاح يرسل الـ JWT على h2c، فتحكمه القاعدة 7: يُقبل من خادم تطوير على loopback، أو عبر وكيل TLS موثوق. **والجوال** بلا Auth لا يرسل `owner` ولا شيئًا، فهو مجهول تحكمه القواعد.

### 7. الاختبارات السلبية

ADR-010 §1 يشترط لكل بند اختبارًا سلبيًا. هذه تُضاف إلى AC-28، وAC-28 يبقى كما هو على الأسطح الأخرى. كلها على gRPC وREST:

- **`UNAUTHENTICATED`:**
  - `alg` بقيمة `none` (مع السجل)، وHS256 بالمفتاح العام سرًّا، وRS384؛
  - `kid` مجهول، و`kid` لمفتاح حساب آخر، و`kid` لمفتاح معطّل أو محذوف؛
  - `sub ≠ iss`، و`iss` مجهول، وحساب معطّل، ومشروع محذوف؛
  - بلا `scope`، و`scope` بلا datastore ولا cloud-platform؛
  - `aud` = عنوان نقطة الرموز، و`aud` آخر، ومصفوفة فيها قيمة غير مقبولة؛
  - `iat` في المستقبل بعد 60 ثانية، و`exp` مضى قبل 60 ثانية، وعمر أطول من ساعة و60 ثانية؛
  - JWT (و`aruma_sat_` و`aruma_pat_`) على اتصال غير مشفّر: من نظير ليس loopback ولا وكيلًا موثوقًا؛ ومن وكيل موثوق مع `X-Forwarded-Proto: http`؛ **ومن وكيل موثوق بلا `X-Forwarded-Proto`**؛ **ومن وكيل على الجهاز نفسه (نظير loopback) على خادم `public_url` أو `listen` فيه غير loopback، بلا الترويسة**؛
  - `owner` بلا `documents.dev_owner_token`، و`owner` من نظير غير loopback، والعلم نفسه على إعداد غير loopback (يرفضه التحقق من الإعداد)؛
  - رمز ID يحمل `iss` بريدًا.
- **`PERMISSION_DENIED`:**
  - حساب خدمة لمشروع آخر؛
  - جسم يسمّي مشروع الطالب في `database` ومشروعًا آخر في كتابة أو تحويل أو شرط أو مستند في BatchGet، وهدف Listen لمشروع آخر، ورسالة Write ثانية بقاعدة أخرى (القاعدة 6؛ ADR-027 §2.2)، ولا يمس شيئًا؛
  - `aruma_pat_` لحساب لا يملك المشروع.
- **الأسطح الأخرى:** JWT حساب الخدمة و`owner`، حتى على إعداد loopback، **يبقيان مرفوضين** على Management API وواجهة Auth الإدارية وGCS.
- **إغلاق البث:** Listen بحساب خدمة يُغلق بـ `UNAUTHENTICATED` عند `exp`، وبعد تعطيل الحساب أو حذف المفتاح خلال 60 ثانية.

## البدائل المرفوضة

- **(ب) حزمة مساعدة بـ `aruma_sat_`، كـ `adminstorage`:**
  - تغيّر كود كل تطبيق Admin، وتحتاج حزمة لكل لغة؛
  - والـ JWT الموقّع ذاتيًا هو طريقة Google نفسها، ترسله المكتبات دون تعديل؛
  - ويبقى `aruma_sat_` مقبولًا لمن يريده (البند 2).
- **توجيه `token_uri` إلى Aruma حلًا:** المكتبات لا تطلبه لـ Firestore (SP، Q1)، فلا يغيّر شيئًا.
- **قبول الـ JWT على كل الأسطح:** عملاء Management API وAuth الإدارية وGCS يتبادلون اليوم، ولا دليل يحتاجه. والسطح الأصغر أسلم.
- **قبول الـ JWT على أي اتصال غير مشفّر:** يُعاد استعماله ساعة كاملة.
- **إبقاء S-3 كما هو (`owner` مرفوض دائمًا):** يكسر التطوير المحلي بوضع المحاكي في Go وNode. ويكسر Node بـ `ssl:false` على gRPC كليًا، لأنه لا يرسل غير `owner`.
- **قبول `owner` من أي نظير loopback دون شرط الإعداد:** وكيل على الجهاز يجعل كل عميل loopback.
- **قبول `owner` على خادم loopback دون علم صريح** (المسودة السابقة): كل عملية محلية تصير مديرًا لكل مشروع، وMUST 25 يشترط وضعًا صريحًا.
- **قبول الاعتماد غير المشفّر من أي نظير loopback، أو من وكيل موثوق لا يرسل `X-Forwarded-Proto`** (المسودة السابقة): أشيع نشر (Caddy على الجهاز نفسه) كان يمرر اعتماد كل عميل، ومنه عميل على `:80` غير مشفّر (القاعدة 7).
- **الأخذ بحقل واحد يطابق المشروع** (الترويسة أو `database`) والعمل على أسماء الحقول الأخرى: حساب خدمة لمشروع A يكتب في مشروع B (القاعدة 6).
- **التخبئة حتى `exp` دون حد:** حذف مفتاح أو تعطيل حساب يبقى بلا أثر حتى ساعة.
- **اشتراط `kid`:** يرفض firebase-admin كله، لأنه لا يرسله أبدًا.

## العواقب

- **README السجلات:** صف ADR-010 يشير إلى هذا السجل للجزء الذي حل محله. وADR-010 نفسه لا يُعدَّل.
- **`authn`** يكسب علمي سياسة لا تضعهما إلا قاعدة المستندات، ومعهما اختبار يمنع غيرها. و**`tenancy/core`** يكسب طريقة تحقق تشارك `ExchangeAssertion` قواعدها.
- **نموذج التهديد** (`docs/security/threat-model.md`) يكسب صفين:
  - **اعتماد أُرسل بلا تشفير مكشوف حتى `exp`:** من التقطه يعيده، ولو عبر TLS. والعلاج حذف المفتاح، ويسري خلال 60 ثانية (القاعدة 8). والقاعدة 7 تجعل النشر غير المشفّر يفشل بصوت عالٍ، ولا تمنع إعادة الاستعمال؛
  - و`owner` على خادم التطوير، خلف علم صريح وloopback في الإعداد والنظير.

  ومراجعة `authn` المستقلة قبل إغلاق المرحلة تشمل الكود الجديد (ADR-010 §4).
- **الوثائق:**
  - Admin Node أمام خادم حقيقي يحتاج TLS؛
  - Go على h2c مقبول من خادم تطوير على loopback، أو عبر وكيل في `trusted_proxies` يرسل `X-Forwarded-Proto: https`؛
  - `documents.dev_owner_token` للتطوير المحلي وحده؛
  - ملف المفتاح يُنزَّل من Aruma؛
  - `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` مع جهة خاصة.
- **ADR-025 §7.1 وADR-026 §4** يعتمدان على تصنيف الطالب وإغلاق البث هنا.
