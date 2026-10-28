# Getting started

This page takes you from nothing to a web app that signs a user in against your own Aruma server. It uses a local server and the Auth service. Other services and platforms are in the [guides](README.md#guides).

You need Go 1.26 or later, PostgreSQL 18, Node 24 (to build the dashboard and for your web app), and `openssl`. A local PostgreSQL from [DBngin](https://dbngin.com) or [Postgres.app](https://postgresapp.com) works.

## 1. Run Aruma

Get the source:

```bash
git clone https://github.com/arumadev/aruma.git
cd aruma
```

Build the dashboard once. It is embedded in the binary.

```bash
make dashboard
```

Then start the server for development:

```bash
make dev
```

`make dev` creates a `.env` file with a random master key on the first run, creates the database, runs the migrations and starts the server on <http://localhost:8080>. If the PostgreSQL tools are not on your `PATH`, set `ARUMA_PG_BIN` to their directory first, for example `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin make dev`.

To run the binary yourself, for example on a server:

```bash
make build                                   # writes bin/aruma and bin/arumactl
export ARUMA_DATABASE_URL='postgres://aruma:secret@127.0.0.1:5432/aruma?sslmode=disable'
export ARUMA_PUBLIC_URL='https://aruma.example.com'
export ARUMA_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: every other key is derived from it
bin/aruma migrate up                         # the server never changes the schema itself
bin/aruma serve
```

These three settings are the only required ones. See [configuration](reference/configuration.md) for the rest and [`aruma.example.yaml`](../aruma.example.yaml) for every key. Check a configuration without starting the server with `aruma config check`.

Open the address in a browser. The first start prints a one-time setup `code` in the log. Enter it on the setup page to create your first account. Sign-up is closed after that.

## 2. Create a project and an app

In the dashboard:

1. Choose **New project**, give it a name, and choose **Create project**. The project ID goes into your apps' config files and cannot be changed.
2. Choose **Add app**, pick **Web**, and register it.
3. Copy the `firebaseConfig` snippet it shows. It is the same object the Firebase console gives you, with your server's address in it.

Authentication with email and password is on by default. Turn other sign-in methods on under **Authentication**.

The same steps work from the command line. Create a CLI token in the dashboard (My account, CLI tokens), then:

```bash
bin/arumactl login --server http://localhost:8080
bin/arumactl projects create chat-app --name "Chat App"
bin/arumactl apps create --project chat-app --platform web
bin/arumactl apps sdkconfig --project chat-app <APP_ID> --out .
```

## 3. Point a web app at Aruma

Your app keeps the official `firebase` package. Add `@aruma/web` and one line to the Vite config. The package is not published yet, so install it from your clone of this repository:

```bash
npm install firebase
npm install --save-dev /path/to/aruma/sdk/web/aruma-web
```

```js
// vite.config.js
import { defineConfig } from 'vite';
import aruma from '@aruma/web/vite';

export default defineConfig({
  plugins: [aruma({ url: 'http://localhost:8080' })], // your server's public_url
});
```

Your app code does not change. Use the `firebaseConfig` from the dashboard and call the SDK as usual:

```js
import { initializeApp } from 'firebase/app';
import { getAuth, createUserWithEmailAndPassword } from 'firebase/auth';

const app = initializeApp(firebaseConfig); // from the dashboard
const auth = getAuth(app);                 // no connectAuthEmulator

const cred = await createUserWithEmailAndPassword(auth, 'ada@example.com', 'a-long-password');
console.log(cred.user.uid);
```

The plugin accepts `http` only for `localhost` and other loopback addresses. For any other host, use https. Other bundlers, Next.js and Flutter web are covered in [Connecting a web app to Aruma Auth](guides/auth-web.md) and [SDK packages](sdks.md).

## 4. Verify

- Run the app and create the user. In the dashboard, open **Authentication**. The user is listed there.
- Open the browser's network panel. Auth requests go to your server's address, and none go to `googleapis.com`.
- Check that the server is healthy: `curl http://localhost:8080/readyz`.

## Next

- Android, iOS and Flutter: [SDK packages](sdks.md).
- Verify users on your server: [Verifying users on your server](guides/auth-admin.md).
- Realtime Database, Cloud Storage and the Document Database are off by default. Turn them on in the configuration: see [configuration](reference/configuration.md), [Realtime Database](guides/realtime-database.md) and [Moving your app's Firestore to Aruma](guides/firestore-migration.md).
- Put it behind your own domain and certificates: [Operations](guides/operations.md).
- Back up with `aruma backup --out aruma.dump` and restore with `aruma restore --in aruma.dump --yes` while the server is stopped.
