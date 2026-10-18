# @aruma/web

Point the **official Firebase web SDK**, in its normal production mode (no `connectAuthEmulator`),
at an Aruma server. Keep Aruma's config file and add one line to `vite.config`; the app's code does
not change.

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

[العربية](#العربية)

## The one line

```js
// vite.config.js
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'https://aruma.example.com' })], // your server's public_url
});
```

```js
// The app, unchanged: the firebaseConfig Aruma's dashboard gives you, no connectAuthEmulator.
const app = initializeApp(firebaseConfig);
const auth = getAuth(app);
```

## How it works

Firebase Auth on the web has its hosts compiled in. Aruma serves Google's exact paths under its own
host (ADR-014), so a redirect is only a host rewrite:

| The SDK requests | @aruma/web sends |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<url>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<url>/securetoken.googleapis.com/<path>` |

- Firebase Auth sends every request with `fetch`, and looks `fetch` up on each call. `install()`
  wraps `globalThis.fetch` once. It rewrites only https requests on the default port to those two
  hosts, whether the request is a string, a `URL` or a `Request`. It keeps the method, headers,
  body and signal. Every other request (Firestore, Installations, your own API…) passes through
  untouched.
- The Vite plugin injects the install as the first module of `index.html`, before the app's own
  scripts. Installing after `firebase/auth` is imported also works in the browser, because the SDK
  looks `fetch` up per request (tested).
- Unlike emulator mode, production mode keeps the SDK's own checks: for popup and redirect
  sign-in it validates the app's origin with `getProjectConfig` and opens
  `https://<authDomain>/__/auth/iframe` and `/__/auth/handler`. Aruma serves all of them (ADR-014).

## Install

```sh
npm install --save-dev @aruma/web
```

### Vite (recommended)

The block above. Options:

| Option | Default | Meaning |
|---|---|---|
| `url` | — | The Aruma server. Missing or empty: the plugin does nothing. |
| `allowInsecure` | `false` | Accept `http` to a host that is not loopback. Development only. |
| `log` | `false` | Log each rewrite in the console: method, host and path. |
| `mode` | `'fetch'` | `'fetch'`: the run-time redirect above. `'build'`: see below. |

### Other bundlers (webpack, Rollup, esbuild…)

Make this the first import of the app entry:

```js
// src/aruma.js
import { install } from '@aruma/web';
install({ url: 'https://aruma.example.com' });
```

```js
// webpack.config.js
module.exports = { entry: { main: ['./src/aruma.js', './src/index.js'] } };
```

Or import `@aruma/web/auto`, which reads `globalThis.__ARUMA__` (`{ url, allowInsecure, log }` or a
URL string) and, in Vite-style builds, `import.meta.env.VITE_ARUMA_URL`.

### Build-time mode (`mode: 'build'`, Vite 8+)

No run-time code: the plugin rewrites the two host literals inside `@firebase/auth` while building
(`"identitytoolkit.googleapis.com"` becomes `"aruma.example.com/identitytoolkit.googleapis.com"`,
the same for `securetoken`, and the scheme when Aruma is on loopback http). `EID_FROM_APIHOST` uses
the same literal, so it stays consistent. It also runs in the dev server's dependency optimizer.

- The build **fails** if the literals are not found, for example after a Firebase release that
  changes how they are written. Your app never ships talking to Google.
- `fetch` stays untouched, so it also covers code that calls a captured `fetch`.
- It needs Vite 8 (Rolldown); on an older Vite the plugin refuses `mode: 'build'`.

## Safety

- **https only.** `http` is accepted only for `localhost`, `127.0.0.0/8` and `::1`, unless
  `allowInsecure` is set.
- **Fails closed.** When a URL is given but refused, the Vite plugin stops the build, `install()`
  throws, and in every case Firebase Auth requests fail (`auth/network-request-failed`) instead of
  being sent to Google with the user's password.
- **Does nothing when not configured.** No URL, no wrapper.
- **Logs.** Logging is off by default. When on, it shows the method, host and path only: never the
  query (it holds the API key), headers, tokens or bodies.

## API

```ts
install({ url, allowInsecure?, log? }): boolean   // false when url is missing; throws when refused
installFromConfig(config): boolean                // the same, but logs instead of throwing
uninstall(): void
status(): { url } | { blocked } | null
rewriteURL(url, base): string | null              // the pure rewrite rule
```

## Limits

- **Google and Apple popup and redirect sign-in** still load `apis.google.com/js/api.js`: the
  declared exception of ADR-013.
- **Server-side rendering in Node:** the Node build of `@firebase/auth` captures `fetch` when it is
  imported. Install before importing `firebase/auth`, or use `mode: 'build'`.
- **A library that replaces `fetch` without calling the previous one** after `install()` removes the
  redirect. Libraries that wrap `fetch` (calling the previous one) are fine.
- **Only Auth.** Firestore, Realtime Database and Storage have their own settings and later phases.

---

## العربية

# @aruma/web

يوجّه **مكتبة Firebase الرسمية للويب** إلى خادم Aruma وهي في وضع الإنتاج العادي، دون `connectAuthEmulator`.
يكفي ملف الإعداد من Aruma، وسطر واحد في `vite.config`. كود التطبيق لا يتغيّر.

## السطر الواحد

```js
// vite.config.js
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'https://aruma.example.com' })], // public_url لخادمك
});
```

## كيف يعمل

عناوين Google ثابتة في مكتبة Auth للويب. وAruma يخدم مسارات Google نفسها تحت نطاقه (ADR-014)، فالتحويل تبديل للنطاق وحده:

| تطلب المكتبة | يرسل @aruma/web إلى |
|---|---|
| `https://identitytoolkit.googleapis.com/<path>` | `<url>/identitytoolkit.googleapis.com/<path>` |
| `https://securetoken.googleapis.com/<path>` | `<url>/securetoken.googleapis.com/<path>` |

- المكتبة ترسل كل طلبات Auth بـ `fetch`، وتبحث عنه عند كل طلب.
- `install()` يلفّ `globalThis.fetch` مرة واحدة، ولا يحوّل إلا طلبات https على المنفذ الافتراضي إلى هذين النطاقين:
  - نصًا كان الطلب، أو `URL`، أو `Request`.
  - ويُبقي الطريقة والترويسات والجسم وإشارة الإلغاء.
  - وكل طلب آخر يمر كما هو.
- إضافة Vite تحقن التثبيت أول وحدة في `index.html`، قبل سكربتات التطبيق.
- والتثبيت بعد استيراد `firebase/auth` يعمل أيضًا في المتصفح (مُختبَر).

## لغير Vite

اجعل هذا أول استيراد في مدخل التطبيق:

```js
import { install } from '@aruma/web';
install({ url: 'https://aruma.example.com' });
```

أو استورد `@aruma/web/auto`، فهو يقرأ `globalThis.__ARUMA__`، أو `import.meta.env.VITE_ARUMA_URL`.

## وضع البناء (`mode: 'build'`، Vite 8 فأحدث)

- لا كود وقت التشغيل: تستبدل الإضافة عنواني Google النصيين داخل `@firebase/auth` أثناء البناء، وكذلك في مُحسّن الاعتماديات في خادم التطوير.
- **يفشل البناء** إن لم تجد النصين، كأن يغيّرهما إصدار جديد من Firebase. فلا يُشحن تطبيق يكلّم Google.

## الأمان

- **https وحده،** إلا لـ `localhost` و`127.0.0.0/8` و`::1`، أو مع `allowInsecure` للتطوير.
- **يفشل مغلقًا:** إن وُجد عنوان ورُفض، توقف الإضافة البناء، و`install()` يرمي خطأ.
  وفي كل الأحوال تفشل طلبات Auth، ولا تذهب إلى Google بكلمة مرور المستخدم.
- **بلا إعداد لا يفعل شيئًا.**
- **السجل** مطفأ افتراضيًا. وإن فُعّل، فلا يحوي إلا الطريقة والنطاق والمسار: لا الاستعلام (فيه مفتاح API)، ولا الترويسات، ولا الرموز، ولا الأجسام.

## الحدود

- دخول Google وApple بالنافذة أو التحويل يحمّل `apis.google.com/js/api.js`، وهو الاستثناء المعلن في ADR-013.
- في Node (SSR) تلتقط مكتبة Auth `fetch` عند استيرادها: ثبّت قبل الاستيراد، أو استخدم وضع البناء.
- Auth وحده. Firestore وRTDB وStorage لها مراحلها.
