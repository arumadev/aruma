# Verifying users on your server

[العربية](#التحقق-من-المستخدمين-في-خادمك)

Your server keeps `firebase-admin`. Only the place `getAuth` comes from changes: that is the one
line ADR-013 allows for a developer's server.

**Node** (`@aruma/admin-auth`):

```diff
  import { initializeApp, cert } from 'firebase-admin/app';
- import { getAuth } from 'firebase-admin/auth';
+ import { getAuth } from '@aruma/admin-auth';

  initializeApp({ credential: cert('service-account.json') });
  const decoded = await getAuth().verifyIdToken(idToken, true);
```

**Go** (`github.com/arumadev/aruma/sdk/go/adminauth`):

```diff
- client, err := app.Auth(ctx)
+ client, err := adminauth.NewClient(ctx, app, adminauth.WithCredentialsFile("service-account.json"))
```

What you need:

1. **A service account key** created for the project in the dashboard. It is the
   file `cert()` already reads. The libraries exchange it for a one-hour token at Aruma.
2. **The server address.** It is taken from the key file's `token_uri`; set `ARUMA_URL` to
   override it. It must be `https` (plain `http` works on loopback only).
3. **No `FIREBASE_AUTH_EMULATOR_HOST`.** With it, the official SDK in your process accepts
   unsigned tokens, so the libraries refuse to start. Outside production you can allow it
   explicitly (`allowEmulatorHost` in Node, `AllowEmulatorHost()` in Go).

What stays the same:

- **Types and errors.** You get `DecodedIdToken` / `*auth.Token` and `UserRecord`, and the same
  error codes (`auth/id-token-revoked`, `auth.IsIDTokenRevoked`…).
- **Custom tokens.** `createCustomToken` / `CustomToken` is the official one, unchanged, and Aruma
  accepts its tokens.
- **Methods.** `verifyIdToken`, `verifySessionCookie`, `createSessionCookie`, `createUser`,
  `getUser`, `getUserByEmail`, `getUsers`, `updateUser`, `deleteUser`, `deleteUsers`,
  `listUsers`, `setCustomUserClaims` and `revokeRefreshTokens` behave as in `firebase-admin`.
  Custom claims reach ID tokens at the next refresh. Revocation has one-second resolution, as in
  Firebase.

Not yet: phone numbers, multi-factor, tenants, and the email action links
(`generatePasswordResetLink` and friends).

Under the hood the libraries call Aruma's Admin API
(`/identitytoolkit.googleapis.com/v1/projects/<project>/…`). It accepts a service account token
of the project or the project owner's CLI token, and refuses everything else, including the
`Bearer owner` token the official SDKs send in emulator mode. Any OIDC library can also verify
Aruma ID tokens through `<server>/issuers/<project>/.well-known/openid-configuration`.

---

<div dir="rtl">

## التحقق من المستخدمين في خادمك

يبقى `firebase-admin` في خادمك كما هو. يتغير فقط مصدر `getAuth`، وهذا هو السطر الواحد الذي يسمح
به ADR-013 لخادم المطوّر.

- **Node:** استورد `getAuth` من `@aruma/admin-auth` بدل `firebase-admin/auth`.
- **Go:** استدعِ `adminauth.NewClient(ctx, app, adminauth.WithCredentialsFile(...))` بدل `app.Auth(ctx)`.

ما تحتاجه:

1. **مفتاح حساب خدمة** تنشئه للمشروع في اللوحة، وهو الملف الذي يقرؤه `cert()`
   أصلًا. تبادله المكتبتان برمز صالح ساعة من Aruma.
2. **عنوان الخادم:** يؤخذ من `token_uri` في ملف المفتاح، ويغيّره المتغير `ARUMA_URL`. يجب أن يكون
   `https`، و`http` مسموح على الجهاز المحلي فقط.
3. **غياب `FIREBASE_AUTH_EMULATOR_HOST`:** وجوده يجعل المكتبة الرسمية في عمليتك تقبل رموزًا غير
   موقّعة، فترفض المكتبتان البدء. ويمكن السماح به صراحةً خارج الإنتاج فقط.

ما يبقى كما هو:

- **الأنواع والأخطاء:** `DecodedIdToken` و`UserRecord` ورموز الأخطاء نفسها، مثل
  `auth/id-token-revoked` و`auth.IsIDTokenRevoked`.
- **Custom token:** يبقى `createCustomToken` الرسمي دون تغيير، ويقبل Aruma رموزه.
- **الدوال:** تعمل كما في `firebase-admin`. والـ claims تظهر في رمز الدخول بعد التجديد التالي،
  والإبطال بدقة الثانية كما في Firebase.

لم يُدعم بعد: أرقام الهاتف، والعامل الثاني، والـ tenants، وروابط البريد
(`generatePasswordResetLink` وأخواتها).

</div>
