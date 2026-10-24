# adminstorage (Go)

Reach Aruma Cloud Storage from a Go server with the official `cloud.google.com/go/storage`
client. `Bucket` and `DefaultBucket` return the official `*storage.BucketHandle`, as
firebase-admin-go's `app.Storage(ctx)` client does, and `DownloadURL` returns a link on your
Aruma server.

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

[العربية](#العربية)

## The one line

```go
// client, err := app.Storage(ctx)
client, err := adminstorage.NewClient(ctx,
	adminstorage.WithCredentialsFile("service-account.json"), // the key file Aruma issued
	adminstorage.WithDefaultBucket("my-app.aruma.example.com"))

bucket, err := client.DefaultBucket()
w := bucket.Object("photos/cat.png").NewWriter(ctx)
// … write, Close …
url, err := client.DownloadURL(ctx, bucket.Object("photos/cat.png"))
```

```sh
go get github.com/arumadev/aruma/sdk/go/adminstorage
```

The client is separate from `firebase.App`. Do not pass `option.WithEndpoint` to
`firebase.NewApp` instead: firebase-admin-go hands the app's options to every service, and
Cloud Messaging then posts to `<aruma>/storage/v1//projects/…/messages:send`.

## Configuration

| What | Where it comes from, first match wins |
|---|---|
| Aruma server | `WithURL`, then `ARUMA_URL`, then the key file's `token_uri` |
| Service account key | `WithCredentialsFile` / `WithCredentialsJSON`, then `GOOGLE_APPLICATION_CREDENTIALS` |
| Default bucket | `WithDefaultBucket` (the project's `storageBucket`) |
| HTTP transport | `WithHTTPClient` (its `Transport`), then a clone of `http.DefaultTransport` |

The server URL is the server's `public_url`: an origin without a path, `https` (plain `http`
only on loopback), never a Google address.

**How it reaches Aruma.** `storage.NewClient` with `option.WithEndpoint("<aruma>/storage/v1/")`
and `storage.WithJSONReads()` (the default XML reads are not served). The client authorizes
itself: it exchanges the key at Aruma's token endpoint and puts the token on every request to
the Aruma origin, the resumable uploads' chunks included, and refuses a request to any other
origin. It never lets `cloud.google.com/go/auth` read the key file, which would sign a token of
its own (or refuse the file) when `universe_domain` is not `googleapis.com`, and it looks for no
default credentials. Redirects are not followed.

**Emulator guard.** `NewClient` fails if `STORAGE_EMULATOR_HOST` or
`FIREBASE_STORAGE_EMULATOR_HOST` is set: with it, the official client sends no credential.

**`DownloadURL`** reads the object's Firebase metadata from Aruma (`GET /v0/b/<bucket>/o/<name>`),
which gives an object without a download token its first one, and returns
`<server>/v0/b/<bucket>/o/<name>?alt=media&token=<token>`, the name escaped as
`encodeURIComponent` does: the form every platform's SDK resolves. A missing object is
`storage.ErrObjectNotExist`, one without a token `ErrNoDownloadToken`.

## Tests

`go test ./...` runs the unit tests against a fake server. The live scenario is
`test/smoke/run.sh admin-go-storage`, which starts a real Aruma (Go and the local Postgres are
needed).

## License

Apache-2.0.

---

<div dir="rtl">

## العربية

حزمة `adminstorage` توصل خادم Go إلى Cloud Storage في Aruma بالعميل الرسمي
`cloud.google.com/go/storage`. و`Bucket` و`DefaultBucket` تعيدان `*storage.BucketHandle` الرسمي كما
يفعل عميل `app.Storage(ctx)` في firebase-admin-go، و`DownloadURL` يعيد رابطًا على خادم Aruma.

Aruma غير تابع لـ Google ولا معتمد منها. Firebase علامة تجارية لـ Google LLC.

### الإعداد

- **عنوان الخادم:** `WithURL`، ثم `ARUMA_URL`، ثم `token_uri` في ملف المفتاح. وهو `public_url` الخادم:
  أصل بلا مسار، `https` (و`http` على الجهاز المحلي فقط)، ولا يكون عنوان Google أبدًا.
- **العميل منفصل عن `firebase.App`:** `option.WithEndpoint` على `firebase.NewApp` يكسر FCM وFirestore.
- **الاعتماد:** تبادل المفتاح عند نقطة رموز Aruma، والرمز على كل طلب إلى أصل Aruma ومنه قطع الرفع
  المتقطع، ورفض أي أصل آخر. لا يقرأ `cloud.google.com/go/auth` ملف المفتاح، فلا يغيّر `universe_domain`
  شيئًا، ولا بحث عن اعتمادات افتراضية.
- **حارس المحاكي:** يفشل `NewClient` إن وُجد `STORAGE_EMULATOR_HOST` أو `FIREBASE_STORAGE_EMULATOR_HOST`.
- **`DownloadURL`** يقرأ الـ metadata من Aruma ويعيد الرابط الموحّد
  `<server>/v0/b/<bucket>/o/<الاسم مرمَّزًا>?alt=media&token=<الرمز>`.

### الترخيص

Apache-2.0.

</div>
