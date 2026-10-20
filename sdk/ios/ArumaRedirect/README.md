# ArumaRedirect

Point the **official Firebase iOS SDK**, in its normal production mode (no `useEmulator`), at an
Aruma server. Add the package and one Info.plist key; no app code.

```xml
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

## How it works

Firebase Auth on iOS has its hosts compiled in. Aruma serves Google's exact paths under its own
host (ADR-014), so a redirect is only a host rewrite:

| The SDK requests | ArumaRedirect sends |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<ArumaURL>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<ArumaURL>/securetoken.googleapis.com/<path>` |
| `https://www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` | `<ArumaURL>/www.googleapis.com/identitytoolkit/v3/relyingparty/<rpc>` |

- A `URLProtocol` handles those three families and nothing else. Every other request
  (Installations, Analytics, Firestore, …) passes through untouched.
- It keeps the method, headers, query and body. It reads `httpBodyStream`, which is how
  URLProtocol usually receives a body.
- It performs the request with its own URLSession. That session never contains the protocol, and
  forwarded requests are marked, so nothing loops. The response, data and errors are relayed to the
  SDK under the original Google URL. Timeouts, cancellation and server redirects behave as usual.
- It installs itself before `main`: an Objective-C `+load` in the `ArumaRedirectLoader` target
  reads `ArumaURL` and registers the protocol. Then it puts the protocol first in
  `URLSessionConfiguration.default` and `.ephemeral`, which is where Firebase's sessions come from.
  `URLProtocol.registerClass` alone only reaches `URLSession.shared`.

## Install

**Swift Package Manager (Xcode):** add the package, then the `ArumaRedirect` product to the app target.

**CocoaPods, including Flutter apps:** in `ios/Podfile`, inside the app target:

```ruby
pod 'ArumaRedirect', :path => '<aruma>/sdk/ios/ArumaRedirect'
```

Flutter needs no plugin: there is no Dart API. The pod is linked into Runner, and `+load` runs
when the app starts. In Flutter's Swift Package Manager mode, add the package in Xcode instead.

Then add `ArumaURL` to `Info.plist`. Keep `GoogleService-Info.plist` from Aruma.

## Configuration (Info.plist)

| Key | Type | Meaning |
|---|---|---|
| `ArumaURL` | String | The Aruma server. Absent or empty: the package does nothing. |
| `ArumaAllowInsecureURL` | Bool | Accept `http` to a host that is not loopback. Development only. |
| `ArumaRedirectLog` | Bool | Log each rewrite: method, host and path. |

Safety:
- **https only.** `http` is accepted only for `localhost`, `127.0.0.0/8` and `::1`, unless
  `ArumaAllowInsecureURL` is set.
- **Fails closed.** When `ArumaURL` is present but refused, Auth requests fail with
  `URLError.cannotConnectToHost`. They are never sent to Google with the user's password.
- **Logs.** Logs never contain the query (it holds the API key), headers, tokens or bodies.

## Code instead of Info.plist

```swift
import ArumaRedirect

try ArumaRedirect.install(url: URL(string: "https://aruma.example.com")!)
```

Call it before Firebase sends its first Auth request, for example first in
`application(_:didFinishLaunchingWithOptions:)`. Leave `ArumaURL` out of Info.plist; the loader
then does nothing.

## Limits

- **Background URLSessions** ignore custom protocols. Firebase Auth does not use them.
- **Only Auth.** Firestore uses gRPC on its own sockets, not URLSession, so a URLProtocol
  cannot redirect it. Later phases need another mechanism.
- **The SDK's URLs are an implementation detail.** If a Firebase release moves Auth to new hosts
  or paths, they pass through unchanged and fail against Google with Aruma's API key. Nothing
  leaks to Aruma. Pin the Firebase version and run the conformance suite before upgrading.
- **Other Firebase pods still call Google.** With Analytics, Crashlytics or Remote Config in the
  app, the Installations SDK sends one `POST https://firebaseinstallations.googleapis.com/…` at
  launch, even with data collection off. It carries no Auth data. Leave those pods out if the
  app must not contact Google at all.

## Tests

```sh
swift test
```

The tests cover URL rewriting, body streams, forwarding through the protocol against a fake
server (no network), errors, timeouts, cancellation, redirects, Info.plist parsing, and the
`+load` loader.

## License

Apache-2.0.

---

## العربية

تجعل هذه الحزمة **مكتبة Firebase الرسمية لـ iOS**، في وضعها العادي (دون `useEmulator`)، تتحدث مع خادم
Aruma. أضف الحزمة ومفتاحًا واحدًا في Info.plist، دون أي سطر في كود التطبيق:

```xml
<key>ArumaURL</key>
<string>https://aruma.example.com</string>
```

**كيف تعمل:** Aruma يخدم مسارات Google نفسها تحت نطاقه (ADR-014). فالتحويل تبديل للنطاق وحده:
`https://identitytoolkit.googleapis.com/…` و`https://securetoken.googleapis.com/…`
و`https://www.googleapis.com/identitytoolkit/v3/relyingparty/…` تذهب إلى `<ArumaURL>/<النطاق الأصلي>/…`.
- الحزمة `URLProtocol` لا يأخذ إلا هذه العائلات الثلاث، وكل طلب آخر يمر كما هو.
- يُبقي الطريقة والترويسات والجسم، ويقرأ الجسم من `httpBodyStream`.
- يرسل الطلب بجلسته الداخلية، وهي لا تمر به، ويعيد الرد للمكتبة بعنوان Google الأصلي.
- يُثبَّت نفسه قبل `main` بـ `+load` في هدف Objective-C صغير. ويضع نفسه أولًا في
  `URLSessionConfiguration.default` و`.ephemeral`، ومنهما تبني Firebase جلساتها.

**التثبيت:**
- **Swift Package Manager:** أضف المنتج `ArumaRedirect` إلى هدف التطبيق.
- **CocoaPods وتطبيقات Flutter:** أضف السطر `pod 'ArumaRedirect', :path => '…'` في `ios/Podfile`.
  لا يحتاج Flutter إلى إضافة Dart، إذ لا واجهة Dart.

**الأمان:**
- https وحده، إلا لـ `localhost` و`127.0.0.0/8` و`::1`، أو مع `ArumaAllowInsecureURL` للتطوير.
- إن وُجد `ArumaURL` ورُفض، تفشل طلبات Auth ولا تذهب إلى Google.
- لا يُسجَّل الاستعلام (فيه مفتاح API) ولا الرموز ولا الأجسام.
- غياب `ArumaURL` يعني أن الحزمة لا تفعل شيئًا.

**بالكود بدل Info.plist:** `try ArumaRedirect.install(url: URL(string: "https://aruma.example.com")!)`، قبل أول طلب Auth.

**الحدود:**
- الجلسات الخلفية (background) لا تمر بـ URLProtocol.
- Firestore يستخدم gRPC لا URLSession، فلا تحوّله هذه الحزمة.
- عناوين المكتبة تفصيل داخلي قد يتغير. ثبّت إصدار Firebase وشغّل اختبارات التوافق قبل الترقية.
- مكتبات Firebase الأخرى تتصل بـ Google: مع Analytics أو Crashlytics أو Remote Config يرسل Installations طلبًا واحدًا إلى
  `firebaseinstallations.googleapis.com` عند الإقلاع، حتى مع إيقاف جمع البيانات، ولا بيانات Auth فيه.
  احذف هذه المكتبات إن كان التطبيق يجب ألا يتصل بـ Google أبدًا.
