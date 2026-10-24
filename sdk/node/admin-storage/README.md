# @aruma/admin-storage

Reach Aruma Cloud Storage from a Node.js server with the official `@google-cloud/storage`
client, as `firebase-admin/storage` does with Google. Buckets and files are
`@google-cloud/storage`'s own (`Bucket`, `File`, `save`, `download`, `copy`, …), and
`getDownloadURL` returns a link on your Aruma server.

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

[العربية](#العربية)

## The one line

```diff
  import { initializeApp, cert } from 'firebase-admin/app';
- import { getStorage, getDownloadURL } from 'firebase-admin/storage';
+ import { getStorage, getDownloadURL } from '@aruma/admin-storage';

  initializeApp({ credential: cert('service-account.json'), storageBucket: 'my-app.aruma.example.com' });
  const file = getStorage().bucket().file('photos/cat.png');
  await file.save(data, { contentType: 'image/png' });
  const url = await getDownloadURL(file); // https://aruma.example.com/v0/b/…/o/photos%2Fcat.png?alt=media&token=…
```

`getStorage()` returns a `Storage` of `@google-cloud/storage` (a subclass), whose `bucket()`
without a name is the app's `storageBucket`, as in `firebase-admin`.

## Install

```sh
npm install @aruma/admin-storage firebase-admin@^14
```

`firebase-admin` 14 is a peer dependency. Node.js 22 or later.

## Configuration

| What | Where it comes from, first match wins |
|---|---|
| Aruma server | `getStorage(app, { url })`, then `ARUMA_URL`, then the key file's `token_uri` |
| Service account key | `getStorage(app, { credential })` (path or object), then the app's `cert()` credential, then `GOOGLE_APPLICATION_CREDENTIALS` |
| Default bucket | the app's `storageBucket` |

The server URL is the server's `public_url`: an origin without a path, `https` (plain `http`
only on loopback), never a Google address. The client sends its token to that origin only: a
request to any other origin, port or scheme fails before it is sent, a resumable session whose
URL is elsewhere included, and uploads follow no redirect. One limit remains: the JSON
requests (metadata, list, copy, delete, downloads) are sent by `teeny-request`, which gives no
way to turn redirects off, so a redirect that the Aruma origin itself answered to another port
of the same host would carry the token. Aruma answers none on these paths, and its tests hold it
to that (`internal/services/storage/gcs/redirect_test.go`).

**How it reaches Aruma.** The client's `apiEndpoint` is the Aruma origin with
`useAuthWithCustomEndpoint`, and its `universeDomain` is the Aruma host: without it, `save()` and
`createWriteStream()`, which upload resumably by default, would start the session and send its
chunks without credentials. The access token comes from Aruma's token endpoint
(`<server>/oauth2/v1/token`), not from Google's, and `universe_domain` in the key file changes
nothing.

**Emulator guard.** `getStorage()` throws if `STORAGE_EMULATOR_HOST` or
`FIREBASE_STORAGE_EMULATOR_HOST` is set: with it, `@google-cloud/storage` sends its requests to
that host without credentials. There is no opt-in.

**`getDownloadURL`** must be this package's: the one in `firebase-admin/storage` sends the Aruma
access token to `firebasestorage.googleapis.com`. It reads the file's Firebase metadata from
Aruma, which gives a file without a download token its first one, and returns
`<server>/v0/b/<bucket>/o/<encodeURIComponent(name)>?alt=media&token=<token>`, the form every
platform's SDK resolves.

**Signed URLs are not supported** in Aruma v0.3: `getSignedUrl()` and the signed post policies
throw. Use `getDownloadURL()` for a link.

## Tests

`npm test` type-checks the declarations and runs the unit tests against a fake server. The
live scenario is `test/smoke/run.sh admin-node-storage`, which starts a real Aruma (Go and the
local Postgres are needed).

## License

Apache-2.0.

---

<div dir="rtl">

## العربية

مكتبة `@aruma/admin-storage` توصل خادم Node.js إلى Cloud Storage في Aruma بالعميل الرسمي
`@google-cloud/storage`، كما يفعل `firebase-admin/storage` مع Google. الـ buckets والملفات هي
أنواع `@google-cloud/storage` نفسها، و`getDownloadURL` يعيد رابطًا على خادم Aruma.

Aruma غير تابع لـ Google ولا معتمد منها. Firebase علامة تجارية لـ Google LLC.

### السطر الواحد

غيّر سطر الاستيراد فقط: `getStorage` و`getDownloadURL` من `@aruma/admin-storage` بدل
`firebase-admin/storage`. يبقى `initializeApp` و`cert` بملف المفتاح الذي أصدره Aruma كما هما، و`bucket()`
بلا اسم هو `storageBucket` التطبيق.

### الإعداد

- **عنوان الخادم:** الخيار `url`، ثم `ARUMA_URL`، ثم `token_uri` في ملف المفتاح. وهو `public_url`
  الخادم: أصل بلا مسار، `https` (و`http` على الجهاز المحلي فقط)، ولا يكون عنوان Google أبدًا.
- **مفتاح حساب الخدمة:** الخيار `credential`، ثم مفتاح `cert()` في التطبيق، ثم
  `GOOGLE_APPLICATION_CREDENTIALS`.
- **كيف تصل إلى Aruma:** `apiEndpoint` أصل Aruma مع `useAuthWithCustomEndpoint`، و`universeDomain`
  مضيف Aruma. بدونه يبدأ `save()` جلسة الرفع المتقطع ويرسل قطعها بلا اعتماد. والرمز من نقطة رموز
  Aruma لا من Google، و`universe_domain` في ملف المفتاح لا يغيّر شيئًا. والرمز لا يُرسل إلا إلى ذلك الأصل:
  أي طلب إلى أصل أو منفذ أو بروتوكول آخر يفشل قبل إرساله، ومنه جلسة رفع رابطها في مكان آخر، ولا
  تُتبع إعادة توجيه.
- **الروابط الموقّعة غير مدعومة** في v0.3: `getSignedUrl()` وسياسات الرفع الموقّعة ترمي خطأ.
- **حارس المحاكي:** ترفض `getStorage()` البدء إن وُجد `STORAGE_EMULATOR_HOST` أو
  `FIREBASE_STORAGE_EMULATOR_HOST`، بلا استثناء.
- **`getDownloadURL`** من هذه الحزمة وحدها: نسخة `firebase-admin/storage` ترسل رمز Aruma إلى
  `firebasestorage.googleapis.com`. هذه تقرأ الـ metadata من Aruma وتعيد الرابط الموحّد
  `<server>/v0/b/<bucket>/o/<الاسم مرمَّزًا>?alt=media&token=<الرمز>`.

### الترخيص

Apache-2.0.

</div>
