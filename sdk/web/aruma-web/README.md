# @aruma/web

Point the **official Firebase web SDK**, in its normal production mode (no `connectAuthEmulator`,
no `connectStorageEmulator`), at an Aruma server. Keep Aruma's config file and add one line to the
bundler's config; the app's code does not change. Auth is redirected at run time (below); Storage
is rewritten at build time ([Firebase Storage](#firebase-storage)).

Aruma is not affiliated with or endorsed by Google. Firebase is a trademark of Google LLC.

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
  untouched, except Storage's and Firestore's (below). **Callable Functions still go to Google and
  carry the user's Aruma ID token** (`Authorization: Bearer <token>`): Aruma does not serve them, so
  an app on Aruma Auth should not use them (SR3-10, the Functions part, still open). Firestore is
  served by Aruma now: see Cloud Firestore below. While Auth is installed, `install()` refuses
  Firestore's connections to `firestore.googleapis.com` and never sends them, so Firestore cannot
  reach Google with the token by mistake: in a browser, `fetch` and `XMLHttpRequest` get a `403`;
  in Node, the connection fails (see Limits).
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

It needs Node 20.19 or later (the build tools load ES modules with `require`).

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
- **Auth on Aruma never sends its ID token to Google Storage.** `@firebase/storage` takes the ID
  token from the app's Auth whatever its own host, and sends it as `Authorization: Firebase <token>`
  with every request. So while Auth is redirected, a Storage SDK request to
  `firebasestorage.googleapis.com` is never sent: it fails at once with `storage/unauthorized`
  (answered locally with 403, which the SDK does not retry; the reason is in the error's
  `serverResponse` and, once, in the console),
  and the Vite plugin without `storage: true` stops a build that includes `@firebase/storage`. Turn
  Storage on (see [Firebase Storage](#firebase-storage)). A plain `fetch` of a download link, which
  carries no ID token, is left alone.
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
- **Firestore in Node (SSR, scripts).** The Node Firestore SDK talks gRPC over HTTP/2
  (`@grpc/grpc-js`), not `fetch`. While Auth is on Aruma, `install()` also patches Node's built-in
  networking so that nothing to `firestore.googleapis.com` (or a subdomain) leaves the process: the
  DNS lookup (`dns.lookup`, `dns.promises.lookup`), `net.Socket#connect` (so `net.connect`), `tls.connect`,
  `http2.connect`, and `http.request`/`https.request`/`get`, including an HTTP `CONNECT` whose target
  is that host (the `HTTP_PROXY`/`HTTPS_PROXY` route of grpc-js). They fail with the error code
  `ARUMA_FIRESTORE_BLOCKED` before any byte is written; the Firestore SDK reports `unavailable` ("the
  client is offline") and `install()` logs the reason once. Other hosts are untouched, and the ES
  module named imports (`import { connect } from 'node:tls'`) see the patched functions. This needs
  Node 20.16 or later (`process.getBuiltinModule`; the package asks for 20.19), applies to the
  global scope only (not to `globalObject` options), and does not close a connection opened before
  `install()` ran. Point Node's Firestore at Aruma with the rewrite or `arumaFirestoreSettings`; it is
  then never refused, because it does not name Google's host. In a browser none of this runs.
- **A library that replaces `fetch` without calling the previous one** after `install()` removes the
  redirect. Libraries that wrap `fetch` (calling the previous one) are fine.
- **Auth, Storage and Firestore.** Realtime Database needs nothing (its `databaseURL` is in the
  config). Callable Functions are not served by Aruma and still go to Google.
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

**An app that set up Auth alone must turn Storage on before it uses Storage.** Without it, every
Storage request would go to Google with the user's Aruma ID token in it. So the Vite plugin without
`storage: true` fails a build that includes `@firebase/storage`, and `install()` fails those
requests in the browser (with any bundler, and in the Vite dev server).

### The one line, per bundler

| Bundler | The line | Measured |
|---|---|---|
| Vite 5 to 8 | `aruma({ url, storage: true })` in `vite.config` (`@aruma/web/vite`) | `vite build` and the dev server, 5.4.21, 6.4.5, 7.3.7, 8.3.5 |
| SvelteKit (any Vite SSR) | the same line: it also sets `ssr.noExternal` | Kit 3.1.0, adapter-node: the server and the client |
| webpack | `arumaStorage.webpack({ url })` (`@aruma/web/unplugin`) | 5.112.2 |
| esbuild | `arumaStorage.esbuild({ url })` | 0.28.2 |
| Rollup, Rspack | `arumaStorage.rollup({ url })`, `arumaStorage.rspack({ url })` | Rollup 4.65.0, Rspack 1.7.12 |
| Rolldown | `arumaStorage.rolldown({ url })` | inside Vite 8 only |
| Next.js | `export default withArumaStorage(config, { url })` (`@aruma/web/next`) | 16.4.3: `next build --webpack`, `next build` and `next dev` (Turbopack); the App Router (server components and client) and the Pages Router (`getServerSideProps`) |
| Expo, React Native (Metro) | `module.exports = withArumaStorage(getDefaultConfig(__dirname), { url })` (`@aruma/web/metro`) | Expo 58.0.19 (Metro 0.87.1): the web export, and the Android bundle; with react-native-svg-transformer 1.5.3 applied before it |
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
- Any emitted script (a Web Worker's bundle, a chunk of a child compiler) that carries the Storage
  SDK with Google's host literal, wherever it came from.
- Anything that loads the SDK from a CDN, in an import or in the output (`index.html` included):
  `www.gstatic.com/firebasejs/` (also `//www.gstatic.com/…`), and the Storage SDK from any absolute
  URL (jsdelivr, unpkg, esm.sh …). That copy never passes through the bundler.
- The Storage host must be a host name, an IP address and an optional port: it is written into the
  SDK's code.

Turbopack gives a loader no end of build: there a file whose literals are not found fails the build,
but a build that never reached the SDK cannot. Metro's bundle fails when a `@firebase/storage` module
in it was not rewritten (its serializer's `processModuleFilter`).

### Web Workers

Vite builds a worker with `worker.plugins` alone; the Vite plugin adds itself there. webpack and
Rspack build `new Worker(new URL(…))` in the same compilation, so the plugin reaches it. esbuild and
Rollup do not follow `new Worker(new URL(…))`: make the worker an entry point (input) of the same
build. Measured with a worker that uploads, links and deletes, in all five.

### Order with other config wrappers

- **Metro:** apply `withArumaStorage` last, around the others. A wrapper applied after it that sets
  `transformer.babelTransformerPath` (react-native-svg-transformer, measured) replaces the rewrite,
  and the bundle then fails: `withArumaStorage(withSvg(getDefaultConfig(__dirname)), { url })`.
- **Next.js:** `withArumaStorage` adds `firebase`, `@firebase/storage` and `@firebase/storage-compat`
  to `transpilePackages`, so that the server bundles the SDK in both routers (the Pages Router leaves
  `node_modules` external otherwise, and its server would talk to Google: measured), and it refuses
  them in `serverExternalPackages`.

### Links from before

After the rewrite the SDK parses links against Aruma's host, so `ref(storage, link)` with a link on
`firebasestorage.googleapis.com` (stored before the switch, or made by another platform) throws
`storage/invalid-url`. Convert it first: `ref(storage, toArumaUrl(link, 'https://aruma.example.com'))`.
`gs://` URLs and the unified link work as they are.

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

## Cloud Firestore

Aruma serves Firestore at the root of its host: REST (the lite SDK, with `fetch`) and WebChannel
(the full SDK: `/google.firestore.v1.Firestore/Listen/channel` and `/Write/channel`). Both SDKs build
every URL from the `host` and `ssl` of their settings, and compile `firestore.googleapis.com` in as
the default. The Firestore SDK sends the user's ID token with every request, and the full SDK puts
it in the WebChannel body (`headers=…`), where no header check sees it. So, as with Storage, the
default is a **build-time rewrite**: the definition of the default host in each `@firebase/firestore`
dist file (full and lite, browser, React Native and Node builds) becomes Aruma's host, and its
default `ssl` becomes `false` when Aruma is on http. The SDK is in production mode (not
`connectFirestoreEmulator`, which changes the client's behaviour), and `getFirestore()` needs no
change. The URL must be the root of Aruma's host (no path).

**An app that set up Auth alone must turn Firestore on before it uses Firestore.** The Vite plugin
without `firestore: true` fails a build that includes `@firebase/firestore`, and `install()` fails
every request to Google's Firestore host in the browser (with any bundler, and in the Vite dev
server), and in Node every connection to it (DNS lookup, TCP, TLS, HTTP/2, HTTP proxy tunnel).

| Bundler | The line |
|---|---|
| Vite 5 to 8 (and SvelteKit) | `aruma({ url, firestore: true })`, with `storage: true` too if the app uses Storage |
| webpack, Rspack, Rollup, esbuild, Rolldown | `arumaFirestore.webpack({ url })` and so on (`@aruma/web/unplugin`) |
| Next.js | `withArumaFirestore(config, { url })` (`@aruma/web/next`), alone or around `withArumaStorage` |
| Expo, React Native (Metro) | `withArumaFirestore(config, { url })` (`@aruma/web/metro`), alone or around `withArumaStorage` |
| Flutter web | `npx aruma-web flutter-web --url <url> --products storage,firestore` |

The build fails instead of shipping Google's host, as for Storage: a `@firebase/firestore` file with
two host literals (or, for http, without the `ssl` default next to it); a bundle in which a
Firestore module still names Google or is left external; a browser build in which no module carries
the Firestore host (`required: false` turns this one off); any emitted script that still holds the
host as a string; and any Firestore SDK loaded from a CDN (`www.gstatic.com/firebasejs/`, jsdelivr,
unpkg, esm.sh …). Web Workers, config-wrapper order and `serverExternalPackages` work as for Storage.

### At run time (no rewrite)

For a build the rewrite cannot reach, use the SDK's own settings, in production mode:

```js
import { initializeFirestore } from 'firebase/firestore';        // or 'firebase/firestore/lite'
import { arumaFirestoreSettings } from '@aruma/web/firestore';
const db = initializeFirestore(app, arumaFirestoreSettings('https://aruma.example.com'));
```

It returns `{ host, ssl }` (and keeps your other settings); call it before the first `getFirestore()`.
The SDK file still holds Google's host as its default, so with the Vite plugin you still pass
`firestore: true`; without a rewrite, `install()` still refuses any request that goes to
`firestore.googleapis.com` from a browser (`fetch`, `XMLHttpRequest`) and any connection to it from
Node (below).

### What is not covered

- **Callable Functions** are not served by Aruma: `httpsCallable` goes to
  `https://<region>-<project>.cloudfunctions.net/<name>` with the Aruma ID token, as the SDK
  builds it. Do not use them with Aruma Auth. (The Functions part of SR3-10 stays open.)
- Firestore from a page that loads the SDK from a CDN cannot be rewritten: the build refuses it, and
  `install()` refuses its requests. Import `firebase/firestore` from npm.
- The Node build uses gRPC (`@grpc/grpc-js`) instead of WebChannel; the rewrite sets its host and
  `ssl` the same way, and Aruma's gRPC listener answers it.
- Tested here: the rewrite rules on the 4.19.0 dist (browser, lite, React Native, Node), the guard
  (browser fetch and XHR; in Node, every connection path above, and the Node SDK itself, which
  attempts no connection and fails as `unavailable`),
  and a `vite build` of the full and lite SDK that leaves no `firestore.googleapis.com` in any
  emitted file. The end-to-end run against a live Aruma (WebChannel) is F6's browser suite.
