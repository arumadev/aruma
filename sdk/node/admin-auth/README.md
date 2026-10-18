# @aruma/admin-auth

Verify Aruma ID tokens and session cookies, and manage Aruma Auth users, from a Node.js server
that already uses `firebase-admin`. Results are the official `firebase-admin` types
(`DecodedIdToken`, `UserRecord`, …) and errors are the official `FirebaseAuthError` codes
(`auth/id-token-expired`, `auth/user-not-found`, …).

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

[العربية](#العربية)

## The one line

```diff
  import { initializeApp, cert } from 'firebase-admin/app';
- import { getAuth } from 'firebase-admin/auth';
+ import { getAuth } from '@aruma/admin-auth';

  initializeApp({ credential: cert('service-account.json') }); // the key file Aruma issued
  const decoded = await getAuth().verifyIdToken(idToken, true);
```

`createCustomToken` is the official one, unchanged: `getAuth().createCustomToken()` calls
`firebase-admin` itself, which signs locally with the key file.

## Install

```sh
npm install @aruma/admin-auth firebase-admin@^14
```

`firebase-admin` 14 is a peer dependency. Node.js 20 or later.

## Configuration

| What | Where it comes from, first match wins |
|---|---|
| Aruma server | `getAuth(app, { url })`, then `ARUMA_URL`, then the key file's `token_uri` (Aruma writes `<server>/oauth2/v1/token` there) |
| Service account key | `getAuth(app, { credential })` (path or object), then the app's `cert()` credential, then `GOOGLE_APPLICATION_CREDENTIALS` |
| Project | the app's `projectId`, then the key file's `project_id` |

The server URL must be `https` (plain `http` only on loopback), and never a Google address.

**Emulator guard.** `getAuth()` throws if `FIREBASE_AUTH_EMULATOR_HOST` is set: with it,
`firebase-admin` in the same process accepts unsigned tokens and sends the literal admin token
`owner`. Pass `{ allowEmulatorHost: true }` to start anyway; this is ignored when
`NODE_ENV=production`.

## API

The `BaseAuth` methods, with the same signatures:

- `verifyIdToken(idToken, checkRevoked?)`, `verifySessionCookie(cookie, checkRevoked?)`
- `createSessionCookie(idToken, { expiresIn })` (5 minutes to 14 days, in milliseconds)
- `createUser`, `getUser`, `getUserByEmail`, `getUsers`, `updateUser`, `deleteUser`, `deleteUsers`, `listUsers`
- `setCustomUserClaims`, `revokeRefreshTokens`
- `createCustomToken` (the official implementation)

Verification fetches the keys through the issuer's discovery document
(`<server>/issuers/<project>/.well-known/openid-configuration`; session cookies use
`/session-issuers/`), accepts RS256 with a `kid` only, and checks `aud`, `iss`, `sub`, `exp` (no
tolerance), `iat` and `auth_time` (at most 5 minutes in the future). `checkRevoked` reads the user
and compares `auth_time` with `tokensValidAfterTime`, as `firebase-admin` does. `validSince` has
one-second resolution, so a token issued in the same second as a revocation still passes.

## Tests

`npm test` type-checks the declarations and runs the unit tests and the live tests. The live
tests start a real Aruma server (`test/smoke/genconfig -serve`), so they need Go and the local
Postgres (`ARUMA_TEST_DATABASE_URL`). Set `ARUMA_URL` and `ARUMA_TEST_DIR` to use a running one.

## License

Apache-2.0.

---

<div dir="rtl">

## العربية

مكتبة `@aruma/admin-auth` تتحقق من رموز دخول Aruma (ID tokens) ومن الـ session cookies، وتدير
مستخدمي Aruma Auth، من خادم Node.js يستعمل `firebase-admin` أصلًا. ما تعيده هو أنواع
`firebase-admin` الرسمية (`DecodedIdToken` و`UserRecord` وغيرهما)، وأخطاؤها هي رموز
`FirebaseAuthError` الرسمية (`auth/id-token-expired` و`auth/user-not-found` وغيرهما).

Aruma غير تابع لـ Google ولا معتمد منها. Firebase علامة تجارية لـ Google LLC.

### السطر الواحد

غيّر سطر الاستيراد فقط: `getAuth` من `@aruma/admin-auth` بدل `firebase-admin/auth`. يبقى
`initializeApp` و`cert` بملف المفتاح الذي أصدره Aruma كما هما. و`createCustomToken` هو الرسمي
نفسه دون تغيير.

### الإعداد

- **عنوان الخادم:** الخيار `url`، ثم المتغير `ARUMA_URL`، ثم `token_uri` في ملف المفتاح. يجب أن
  يكون `https` (و`http` مسموح على الجهاز المحلي فقط)، ولا يكون عنوان Google أبدًا.
- **مفتاح حساب الخدمة:** الخيار `credential`، ثم مفتاح `cert()` في التطبيق، ثم
  `GOOGLE_APPLICATION_CREDENTIALS`.
- **المشروع:** `projectId` في التطبيق، ثم `project_id` في ملف المفتاح.

**حارس المحاكي:** ترفض `getAuth()` البدء إن وُجد `FIREBASE_AUTH_EMULATOR_HOST`، لأن
`firebase-admin` في العملية نفسها يقبل حينها رموزًا غير موقّعة. الخيار
`{ allowEmulatorHost: true }` يسمح بالبدء خارج الإنتاج فقط، ويُهمَل إن كان `NODE_ENV=production`.

### التحقق

تجلب المكتبة المفاتيح عبر مستند الاكتشاف للمُصدِر، وتقبل RS256 مع `kid` فقط، وتفحص `aud` و`iss`
و`sub` و`exp` (دون تسامح) و`iat` و`auth_time` (خمس دقائق في المستقبل على الأكثر). والخيار
`checkRevoked` يقرأ المستخدم ويقارن `auth_time` بـ`tokensValidAfterTime` كما يفعل
`firebase-admin`.

### الترخيص

Apache-2.0.

</div>
