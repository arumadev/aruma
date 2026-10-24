# @aruma/web

Point the **official Firebase web SDK**, in its normal production mode (no `connectAuthEmulator`,
no `connectStorageEmulator`), at an Aruma server. Keep Aruma's config file and add one line to the
bundler's config; the app's code does not change. Auth is redirected at run time (below); Storage
is rewritten at build time ([Firebase Storage](#firebase-storage)).

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

// @aruma/web/storage
installStorage({ url, allowInsecure?, log? }): boolean
useArumaStorage(storage, { url? }): storage
toArumaUrl(link, base?): string
rewriteStorageLiterals(code, base): { code, hosts, schemes, ok }   // the build-time rule
```

## Limits

- **Google and Apple popup and redirect sign-in** still load `apis.google.com/js/api.js`: the
  declared exception of ADR-013.
- **Server-side rendering in Node:** the Node build of `@firebase/auth` captures `fetch` when it is
  imported. Install before importing `firebase/auth`, or use `mode: 'build'`.
- **A library that replaces `fetch` without calling the previous one** after `install()` removes the
  redirect. Libraries that wrap `fetch` (calling the previous one) are fine.
- **Auth and Storage.** Realtime Database needs nothing (its `databaseURL` is in the config);
  Firestore has its own phase.
- **SvelteKit and other frameworks without an `index.html`** do not run the Vite plugin's injection:
  call `install()` in the app's code (the Storage half needs no injection).

## Firebase Storage

Storage's SDK compiles its host in, and builds its download links from it, so a run-time redirect
alone leaves every link on `firebasestorage.googleapis.com`, where an `<img>`, a `<video>` or a link
someone shares goes to Google and fails (ADR-022 §16). So the default is a **build-time rewrite**:
the one definition `DEFAULT_HOST = 'firebasestorage.googleapis.com'` in each `@firebase/storage`
dist file becomes Aruma's host, and its `'https'` becomes `'http'` when Aruma is on http. The SDK
then sends every request to Aruma, and `getDownloadURL()` returns the unified link

```
https://aruma.example.com/v0/b/<bucket>/o/<path>?alt=media&token=<token>
```

which works in any loader and on every platform. The URL must be the root of Aruma's host (no
path). The app's code does not change.

### The one line, per bundler

| Bundler | The line | Measured |
|---|---|---|
| Vite 5 to 8 | `aruma({ url, storage: true })` in `vite.config` (`@aruma/web/vite`) | `vite build` and the dev server, 5.4.21, 6.4.5, 7.3.7, 8.3.5 |
| SvelteKit (any Vite SSR) | the same line: it also sets `ssr.noExternal` | Kit 3.1.0, adapter-node: the server and the client |
| webpack | `arumaStorage.webpack({ url })` (`@aruma/web/unplugin`) | 5.112.2 |
| esbuild | `arumaStorage.esbuild({ url })` | 0.28.2 |
| Rollup, Rolldown, Rspack | `arumaStorage.rollup / .rolldown / .rspack({ url })` | not measured (the same hooks as Vite and webpack) |
| Next.js | `export default withArumaStorage(config, { url })` (`@aruma/web/next`) | 16.4.3: `next build --webpack`, `next build` and `next dev` (Turbopack), server components and client |
| Expo, React Native (Metro) | `module.exports = withArumaStorage(getDefaultConfig(__dirname), { url })` (`@aruma/web/metro`) | Expo 58.0.19 (Metro 0.87.1): the web export, and the Android bundle |
| Flutter web | `npx aruma-web flutter-web --url <url>` once in the project, then `flutter build web` | Flutter 3.44.2, firebase_core_web 4.1.0 (JS SDK 13.1.0), firebase_storage 14.0.1 |

Measured means: the official SDK in production mode, built from npm, against a real Aruma, passes
the eleven operations of `test/e2e/web-storage` in Chromium (uploads, the 3 MB resumable one,
metadata, the link, getBytes, getBlob, list, `fetch` and `<img>` on the link, delete) with every
link unified and nothing sent off the machine. The rule was also checked on `@firebase/storage`
0.13.14 (firebase 11) and 0.14.5 (firebase 12), every dist file and the CDN build.

### The build fails instead of shipping Google's host

- A `@firebase/storage` file in which the host literal (or, for http, the protocol literal) is not
  found exactly once: a release this rule does not know.
- A bundle in which a `@firebase/storage` module still names Google, or is left external (Vite's
  SSR default, webpack `externals`). This is checked on the module set the bundler ends with, so a
  module restored from a persistent cache is checked too.
- A browser build with no `@firebase/storage` module (`required: false` turns this one off). Server
  builds and watch mode never fail for it.
- Anything that loads the SDK from `www.gstatic.com`, in an import or in the output (`index.html`
  included): that copy never passes through the bundler.

Turbopack and Metro give a plugin no end of build: there a file whose literals are not found fails
the build, but a build that never reached the SDK cannot.

### Flutter web

`firebase_core_web` loads the JS SDK from `www.gstatic.com` at run time. The tool reads the version
it pins (`.dart_tool/package_config.json`), takes `firebase-storage.js` of exactly that version from
the `firebase` npm package (`npm install --no-save firebase@<version>`, or `--firebase <dir>`),
rewrites it once, writes it to `web/aruma/firebase-storage-<version>.js` with a notice that it was
modified, and puts in `web/index.html`, in place of the `flutter_bootstrap.js` script, a block that
sets `flutterfire_ignore_scripts = [..., 'storage']`, imports that file as `window.firebase_storage`
and only then loads `flutter_bootstrap.js`. The file imports `firebase-app.js` from gstatic at the
same version, so it shares the app firebase_core_web creates. Run it again after upgrading
firebase_core (it replaces its own block).

### The fallback

For a build none of the above reaches (the SDK from gstatic in a page, a prebuilt bundle):

```js
import { installStorage, useArumaStorage } from '@aruma/web/storage';
installStorage({ url: 'https://aruma.example.com' });      // first: XHR and fetch to Google's host go to Aruma
const storage = useArumaStorage(getStorage(app));          // before its first operation: its links are on Aruma
```

- `installStorage` alone redirects the requests, but the links stay on Google's host (S0-B: 10 of
  11, the `<img>` fails). `useArumaStorage` sets the instance's internal `host` (not in the public
  types), once per `getStorage(app, bucket)` instance.
- `toArumaUrl(link)` turns a link on Google's host (with `:443`, as iOS writes it, or without) into
  the unified one, for links stored before.
- Refused URL: Storage requests fail as network errors instead of reaching Google.
- Measured: the same eleven operations, from a bundle that keeps Google's host (`test/e2e/web-storage`).

### Also needed

- **CORS on the bucket** for `getBytes`, `getBlob` and `fetch` of a link from another origin, as in
  Firebase (`gsutil cors set` with Firebase's documented `cors.json`). Uploads, metadata, list and
  `<img>` work without it.

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
- Auth وStorage. أما RTDB فلا يحتاج شيئًا (`databaseURL` في الإعداد)، ولـ Firestore مرحلته.
- SvelteKit وكل إطار بلا `index.html` لا يشغّل حقن إضافة Vite: استدعِ `install()` في كود التطبيق (ونصف Storage لا يحتاج حقنًا).

## Firebase Storage

مكتبة Storage تثبّت نطاقها في الكود وتبني منه روابط التنزيل. فالتحويل وقت التشغيل وحده يترك كل رابط على `firebasestorage.googleapis.com`، فيذهب `<img>` و`<video>` والرابط المُشارَك إلى Google ويفشل (ADR-022 §16). لذلك الافتراضي **تبديل وقت البناء**:

- التعريف الوحيد `DEFAULT_HOST = 'firebasestorage.googleapis.com'` في كل ملف dist من `@firebase/storage` يصير نطاق Aruma، و`'https'` يصير `'http'` حين Aruma على http.
- فترسل المكتبة كل طلب إلى Aruma، ويعيد `getDownloadURL()` الرابط الموحّد `https://aruma.example.com/v0/b/<bucket>/o/<path>?alt=media&token=<token>`، ويعمل في أي حمّال وعلى كل منصة.
- العنوان جذر نطاق Aruma (بلا مسار). وكود التطبيق لا يتغير.

### السطر الواحد لكل bundler

| الأداة | السطر | ما قيس |
|---|---|---|
| Vite 5 إلى 8 | `aruma({ url, storage: true })` في `vite.config` | `vite build` وخادم التطوير: 5.4.21 و6.4.5 و7.3.7 و8.3.5 |
| SvelteKit (وأي SSR على Vite) | السطر نفسه، ويضبط `ssr.noExternal` | Kit 3.1.0 مع adapter-node: الخادم والمتصفح |
| webpack | `arumaStorage.webpack({ url })` من `@aruma/web/unplugin` | 5.112.2 |
| esbuild | `arumaStorage.esbuild({ url })` | 0.28.2 |
| Rollup وRolldown وRspack | `arumaStorage.rollup` و`.rolldown` و`.rspack` | لم يُقَس (الخطافات نفسها) |
| Next.js | `withArumaStorage(config, { url })` من `@aruma/web/next` | 16.4.3: `next build --webpack` و`next build` و`next dev` (Turbopack)، ومكوّنات الخادم والمتصفح |
| Expo وReact Native (Metro) | `withArumaStorage(getDefaultConfig(__dirname), { url })` من `@aruma/web/metro` | Expo 58.0.19 (Metro 0.87.1): تصدير الويب وحزمة Android |
| Flutter web | `npx aruma-web flutter-web --url <url>` مرة في المشروع، ثم `flutter build web` | Flutter 3.44.2، وfirebase_core_web 4.1.0 (JS 13.1.0)، وfirebase_storage 14.0.1 |

- «قيس» يعني: المكتبة الرسمية في وضع الإنتاج، مبنية من npm، أمام Aruma حقيقي، تجتاز العمليات الإحدى عشرة في `test/e2e/web-storage` في Chromium، وكل رابط موحّد، ولا طلب يخرج من الجهاز.
- وفُحصت القاعدة أيضًا على `@firebase/storage` 0.13.14 (firebase 11) و0.14.5 (firebase 12): كل ملفات dist وبناء CDN.

### يفشل البناء بدل أن يشحن نطاق Google

- ملف من `@firebase/storage` لا يجد فيه نص النطاق (ونص البروتوكول على http) مرة واحدة بالضبط.
- وحدة `@firebase/storage` في الحزمة ما زالت تسمّي Google، أو متروكة خارجها (افتراض SSR في Vite، و`externals` في webpack). ويُفحص هذا على مجموعة الوحدات التي ينتهي بها البناء، فتُفحص الوحدة المسترجعة من الذاكرة المؤقتة أيضًا.
- بناء للمتصفح بلا وحدة `@firebase/storage` (ويطفئه `required: false`). وبناء الخادم ووضع المراقبة لا يفشلان لهذا.
- أي تحميل للمكتبة من `www.gstatic.com`، في استيراد أو في الناتج (و`index.html` منه).

أما Turbopack وMetro فلا يعطيان الإضافة نهايةً للبناء: يفشل البناء على ملف لم يُجد نصه، لكن لا يفشل بناء لم يصل إلى المكتبة أصلًا.

### Flutter web

- `firebase_core_web` يحمّل مكتبة JS من `www.gstatic.com` وقت التشغيل.
- الأداة تقرأ الإصدار المثبّت، وتأخذ `firebase-storage.js` بالإصدار نفسه من حزمة `firebase` في npm، وتبدّله مرة واحدة، وتكتبه في `web/aruma/firebase-storage-<version>.js` مع إشعار التعديل.
- وتضع في `web/index.html`، مكان سكربت `flutter_bootstrap.js`، مقطعًا يضبط `flutterfire_ignore_scripts = [..., 'storage']` ويستورد الملف في `window.firebase_storage`، ثم يحمّل `flutter_bootstrap.js`.
- الملف يستورد `firebase-app.js` من gstatic بالإصدار نفسه، فيتشارك التطبيق مع firebase_core_web. وتُعاد الأداة بعد ترقية firebase_core.

### الاحتياط

لما لا تصله أيٌّ مما سبق (المكتبة من gstatic في صفحة، أو حزمة مبنية سلفًا):

```js
import { installStorage, useArumaStorage } from '@aruma/web/storage';
installStorage({ url: 'https://aruma.example.com' });   // أولًا: XHR وfetch إلى نطاق Google يذهبان إلى Aruma
const storage = useArumaStorage(getStorage(app));       // قبل أول عملية: روابطه على Aruma
```

- `installStorage` وحده يحوّل الطلبات، وتبقى الروابط على Google (10 من 11 في S0-B، ويفشل `<img>`).
- `useArumaStorage` يضبط `host` الداخلي للنسخة (ليس في الأنواع العامة)، لكل نسخة من `getStorage(app, bucket)`.
- `toArumaUrl(link)` يحوّل رابطًا على نطاق Google (بـ `:443` أو بدونه) إلى الموحّد.
- مقيس: العمليات الإحدى عشرة نفسها، من حزمة تبقي نطاق Google.

### ويلزم أيضًا

CORS على الـ bucket لـ `getBytes` و`getBlob` و`fetch` على الرابط من أصل آخر، كما في Firebase (`cors.json` الموثّق). أما الرفع والبيانات الوصفية والقائمة و`<img>` فتعمل بدونه.
