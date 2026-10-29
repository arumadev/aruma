# Getting started

This page takes you from nothing to a web app that signs a user in against your own Aruma server. It uses a local server and the Auth service. Other services and platforms are in the [guides](README.md#guides).

There are three ways to run it. Docker is the quickest; the other two need a PostgreSQL 18 you run yourself.

## 1. Run Aruma

### With Docker

You need Docker with Compose and `openssl`. `compose.yaml` runs Aruma and PostgreSQL 18, with the Realtime Database, Storage and the Document Database turned on. The image is built on your machine the first time, which takes a few minutes.

```bash
git clone --branch v0.4.0 https://github.com/arumadev/aruma.git
cd aruma
printf 'ARUMA_MASTER_KEY=%s\nPOSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32)" "$(openssl rand -hex 24)" > .env
docker compose up -d
docker compose logs aruma | grep code
```

Keep `.env` safe and back it up: every key Aruma uses is derived from `ARUMA_MASTER_KEY`, and without it the data in the volumes cannot be used. The server listens on `127.0.0.1:8080` only. Set `ARUMA_PORT` in `.env` to use another port. To serve other machines, put a TLS proxy in front and set `ARUMA_PUBLIC_URL` in `.env` to its address; see [operations](guides/operations.md#docker).

### With a release archive

Download the archive for your system from [Releases](https://github.com/arumadev/aruma/releases): Linux and macOS (`.tar.gz`) or Windows (`.zip`), on amd64 or arm64. It holds `aruma` and `arumactl`, with the dashboard inside `aruma`. Check it against `checksums.txt` before you run it; [operations](guides/operations.md#prebuilt-binaries) shows how, and what macOS and Windows say about binaries that are not signed. On Windows, Cloud Storage does not start; use Docker or WSL for it.

Create a database on your PostgreSQL 18, then:

```bash
export ARUMA_DATABASE_URL='postgres://aruma:secret@127.0.0.1:5432/aruma?sslmode=disable'
export ARUMA_PUBLIC_URL='http://localhost:8080'
export ARUMA_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: every other key is derived from it
./aruma migrate up                           # the server never changes the schema itself
./aruma serve
```

### From source

You need Go 1.27.3 (an older `go` downloads it by itself unless `GOTOOLCHAIN=local` is set), PostgreSQL 18, Node 24 (to build the dashboard and for your web app), and `openssl`. A local PostgreSQL from [DBngin](https://dbngin.com) or [Postgres.app](https://postgresapp.com) works.

```bash
git clone https://github.com/arumadev/aruma.git
cd aruma
make dashboard     # build the dashboard once; it is embedded in the binary
make dev
```

`make dev` creates a `.env` file with a random master key on the first run, creates the database, runs the migrations and starts the server on <http://localhost:8080>. If the PostgreSQL tools are not on your `PATH`, set `ARUMA_PG_BIN` to their directory first, for example `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin make dev`. `make build` writes `bin/aruma` and `bin/arumactl` to run elsewhere, as with an archive.

`ARUMA_DATABASE_URL`, `ARUMA_PUBLIC_URL` and `ARUMA_MASTER_KEY` are the only required settings. See [configuration](reference/configuration.md) for the rest and [`aruma.example.yaml`](../aruma.example.yaml) for every key. Check a configuration without starting the server with `aruma config check`.

Open the address in a browser. The first start prints a one-time setup `code` in the log. Enter it on the setup page to create your first account. Sign-up is closed after that.

## 2. Create a project and an app

In the dashboard:

1. Choose **New project**, give it a name, and choose **Create project**. The project ID goes into your apps' config files and cannot be changed.
2. Choose **Add app**, pick **Web**, and register it.
3. Copy the `firebaseConfig` snippet it shows. It is the same object the Firebase console gives you, with your server's address in it.

Authentication with email and password is on by default. Turn other sign-in methods on under **Authentication**.

The same steps work from the command line. Create a CLI token in the dashboard (Account, then CLI tokens), then:

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
