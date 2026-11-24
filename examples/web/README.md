# Team Board: a web example for Aruma

Team Board is a small app for small teams: sign in, create a team or join one with its code, share a live task list with file attachments, and chat while you see who is online. It is a real Vite + React + TypeScript app on the official `firebase` JS SDK, and it uses all four services Aruma serves:

| Service | What Team Board keeps there |
|---|---|
| Authentication | Email and password accounts, guests (anonymous sign-in), display names |
| Firestore (Aruma's Document Database) | Profiles, teams, members, tasks; live lists with `onSnapshot` |
| Realtime Database | The chat (last 50 messages, live) and who is online (`onDisconnect`, `.info/connected`) |
| Cloud Storage | Profile pictures and one attachment per task, with upload progress |

The same code runs on Aruma, on Firebase and on the Firebase Emulator Suite. The only differences are one config file and one build setting. A Flutter version of the same app (`examples/flutter`) uses the same data and rules, so a web user and a Flutter user of one project see each other's teams, tasks, chat and presence.

English and Arabic (right to left), light and dark, from 360 px phones to desktops.

| Tasks (desktop, English) | Chat (desktop, English) |
|---|---|
| ![The tasks of a team: open tasks first, an assignee per task, a PDF attached to one](docs/en-tasks.png) | ![The chat of a team, with who is online](docs/en-chat.png) |

| Tasks (phone, Arabic, dark) | Chat (phone, Arabic, dark) |
|---|---|
| ![The same task list in Arabic, right to left, in the dark theme on a phone](docs/ar-tasks.png) | ![The same chat in Arabic on a phone](docs/ar-chat.png) |

More screenshots are in [docs/](docs/): sign-in, teams and profile, in both languages.

## Prerequisites

- Node.js 20.19 or later (24 is what the repository tests with) and npm.
- An Aruma server with the Realtime Database, Cloud Storage and the Document Database turned on. The steps below start one.
- A clone of this repository: `@aruma/web` is not published to npm yet, so the app installs it from `sdk/web/aruma-web`.

## Run it against Aruma in five minutes

The commands run from the root of your clone unless they say otherwise.

### 1. Start Aruma

With Docker (see [Getting started](../../docs/getting-started.md)): `compose.yaml` already turns the three services on.

```bash
printf 'ARUMA_MASTER_KEY=%s\nPOSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32)" "$(openssl rand -hex 24)" > .env
docker compose up -d
docker compose logs aruma | grep code
```

Or with a binary (`make build`, or a release archive) and a PostgreSQL 18 database of your own:

```bash
export ARUMA_DATABASE_URL='postgres://aruma:secret@127.0.0.1:5432/aruma?sslmode=disable'
export ARUMA_PUBLIC_URL='http://localhost:8080'
export ARUMA_MASTER_KEY="$(openssl rand -base64 32)"   # keep it: every other key is derived from it
export ARUMA_RTDB_ENABLED=true ARUMA_DOCUMENTS_ENABLED=true
export ARUMA_STORAGE_ENABLED=true ARUMA_STORAGE_DATA_DIR="$HOME/aruma-storage"   # an absolute path
bin/aruma migrate up
bin/aruma serve
```

Open <http://localhost:8080>, enter the setup code the server printed, and create your account.

### 2. Create the project and the web app

In the dashboard: **New project** (call it `team-board`), then **Add app**, **Web**. Save the `firebaseConfig` it shows, as JSON, to `examples/web/firebase-config.json`.

Or from the command line, with a CLI token from the dashboard (**Account**, **CLI tokens**):

```bash
bin/arumactl login --server http://localhost:8080          # paste the CLI token
bin/arumactl projects create team-board --name "Team Board"
bin/arumactl apps create --project team-board --platform web   # prints the app's ID
bin/arumactl apps sdkconfig --project team-board <APP_ID> --out examples/web/firebase-config.json
```

`firebase-config.json` is the only place the app reads its backend from. It is in `.gitignore`; [firebase-config.example.json](firebase-config.example.json) shows its shape.

### 3. Deploy the rules and the index

A new project is closed: every service refuses clients until rules are published. The app ships its three rules files and one index setting:

| File | What it holds |
|---|---|
| [firestore.rules](firestore.rules) | Profiles, teams, members and tasks |
| [database.rules.json](database.rules.json) | Chat, presence, and the membership mirror they read |
| [storage.rules](storage.rules) | Profile pictures and task attachments |
| [firestore.indexes.json](firestore.indexes.json) | The collection group index "My teams" needs (`members.uid`) |

Deploy all four, and turn on guest sign-in, with the script (the same HTTP calls as `firebase deploy`):

```bash
cd examples/web
ARUMA_URL=http://localhost:8080 ARUMA_TOKEN=<your CLI token> npm run deploy:aruma -- --anonymous
```

It prints one line per file. You can also do it by hand:

- **Dashboard.** Paste each rules file into Realtime Database > Rules, Cloud Storage > Rules and Document Database > Rules, add the single-field exemption for `members.uid` (collection group scope, ascending) under Document Database > Indexes > Automatic, and turn on **Anonymous** under Authentication > Sign-in methods.
- **firebase-tools**, when Aruma is on https (the tool sends its token over https only; see [Security rules](../../docs/guides/security-rules.md)). [firebase.json](firebase.json) names all the files:

  ```bash
  A=https://aruma.example.com
  export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
    FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A FIRESTORE_URL=$A \
    FIREBASE_RTDB_MANAGEMENT_URL=$A FIREBASE_TOKEN=<CLI token> FIREBASE_SKIP_INFORMATIONAL_IAM=1
  npx firebase-tools deploy --only firestore,storage --project team-board
  ```

  The Realtime Database rules need the database's production URL for the tool (`rtdb.domain`); with the development form (`?ns=`) publish them with `PUT /.settings/rules.json`, as the script does ([Realtime Database](../../docs/guides/realtime-database.md#rules)).

### 4. Install and run

```bash
npm install
ARUMA_URL=http://localhost:8080 npm run dev
```

Open <http://127.0.0.1:5173>, create an account, create a team, and open the same address in a private window to join it as a second user with the team's code.

`ARUMA_URL` is `@aruma/web`'s build setting: your server's `public_url`. To stop typing it, put `ARUMA_URL=http://localhost:8080` in `examples/web/.env.local` (ignored by git). `vite.config.ts` hands it to the plugin:

```ts
plugins: [react(), aruma({ url: arumaURL, storage: true, firestore: true })],
```

The plugin points Auth (at run time), Firestore and Storage (at build time) at Aruma. The Realtime Database needs nothing: its `databaseURL` is in the config file. The app calls no emulator function and has no Aruma-specific code.

## Build for production

```bash
cd examples/web
ARUMA_URL=https://aruma.example.com npm run build   # type-check, then dist/
npm run preview                                     # serves dist/ on http://127.0.0.1:4173
```

`dist/` is static: serve it from any web server or CDN. The routes live in the URL's hash (`#/teams/...`), so no rewrite rules are needed. `@aruma/web` accepts `http` only for `localhost` and loopback addresses, so a server on another host needs https. The build fails rather than ship Google's Storage or Firestore host.

If you restrict the API key to websites (dashboard, **API keys**), add the address you serve the app from.

## The same code on Firebase

1. Create a Firebase project and a web app in the Firebase console, and save its config as `firebase-config.json`.
2. Turn on **Email/Password** and **Anonymous** in Authentication, and create Firestore, the Realtime Database and Storage.
3. Deploy the same files: `npx firebase-tools deploy --only firestore,database,storage --project <your-project>`.
4. Run with no `ARUMA_URL`: `npm run dev`, or `npm run build` for production. With no URL the plugin does nothing, so the SDK talks to Google as usual.

Download links of attachments are opened in a new tab, so the Storage bucket needs no CORS setting.

### The Firebase Emulator Suite

The emulators need Java 11 or later. [firebase.json](firebase.json) sets their ports (Firestore on 8180, so that it does not collide with Aruma on 8080).

```bash
cd examples/web
cat > firebase-config.json <<'EOF'
{
  "projectId": "demo-team-board",
  "appId": "1:000000000000:web:0000000000000000",
  "apiKey": "demo-api-key",
  "authDomain": "demo-team-board.firebaseapp.com",
  "databaseURL": "http://127.0.0.1:9000/?ns=demo-team-board-default-rtdb",
  "storageBucket": "demo-team-board.appspot.com",
  "messagingSenderId": "000000000000"
}
EOF
npx firebase-tools emulators:start --project demo-team-board --only auth,firestore,database,storage
# in another terminal:
VITE_FIREBASE_EMULATORS=true npm run dev
```

`VITE_FIREBASE_EMULATORS=true` is the one switch: [src/firebase.ts](src/firebase.ts) then calls `connectAuthEmulator`, `connectFirestoreEmulator`, `connectDatabaseEmulator` and `connectStorageEmulator` with the ports of `firebase.json`. The emulators load the rules from the same files. A `demo-` project needs no Google account. Never set the switch against Aruma: Aruma refuses the emulators' unsigned tokens.

## The data model, in short

| Where | Path | Fields |
|---|---|---|
| Firestore | `users/{uid}` | `name`, `photoURL`, `updatedAt` |
| Firestore | `teams/{teamId}` (the id is the join code) | `name`, `owner`, `createdAt` |
| Firestore | `teams/{teamId}/members/{uid}` | `uid`, `role` (`owner` or `member`), `name`, `joinedAt` |
| Firestore | `teams/{teamId}/tasks/{taskId}` | `title`, `done`, `assignee`, `createdBy`, `createdAt`, `updatedAt`, `attachment` (`{name, path}` or null) |
| Realtime Database | `members/{teamId}/{uid}` | `true`: the membership, mirrored for the database's rules |
| Realtime Database | `chat/{teamId}/messages/{pushId}` | `uid`, `name`, `text`, `ts` (server time) |
| Realtime Database | `presence/{teamId}/{uid}` | `name`, `online`, `lastSeen`; removed by `onDisconnect` |
| Storage | `avatars/{uid}/avatar` | The profile picture (an image under 2 MB) |
| Storage | `teams/{teamId}/tasks/{taskId}/{filename}` | A task's attachment (under 10 MB), members only |

- "My teams" is a collection group query on `members` by `uid`. Like Firebase, Aruma needs a collection group index for it: that is the one entry in `firestore.indexes.json`. The emulator does not check indexes.
- Creating a team is one batch: the team, then its owner's member document. Joining writes one's own member document, then the Realtime Database mirror.
- Tasks are listed newest first (a single-field index) and shown open first.
- The rules give each service the same picture: only members read and write a team's tasks, chat, presence and attachments; anyone signed in may look a team up by its code; a task is deleted by its author or the team's owner.
- The Realtime Database rules cannot read Firestore, so the mirror in `members/` is written by the user who joins. Anyone who knows a team's code can join it, so this grants nothing the code does not. A member the owner removes in Firestore keeps the mirror until it is deleted too.

The code follows the same split: [src/data/](src/data/) holds every SDK call, [src/screens/](src/screens/) the screens, and [src/i18n/](src/i18n/) the strings in `en.json` and `ar.json`.

## Tests

`e2e/run.sh` starts a fresh Aruma server (from this repository's `test/smoke/genconfig`), deploys the rules with `scripts/deploy-aruma.mjs`, builds the app and drives it with Playwright as two users, then a non-member and a guest: sign-up, a team, joining by code, tasks seen live, an attachment downloaded by the other user and refused to a non-member, chat in order, presence that goes when a browser closes, a rules denial on screen, the offline banner while the server is down, Arabic, and a reload that keeps the session. No request may leave the machine.

```bash
examples/web/e2e/run.sh                 # against Aruma (needs Go and a local PostgreSQL, see docs/testing.md)
examples/web/e2e/run.sh --emulators     # the same test against the Firebase Emulator Suite
examples/web/e2e/run.sh --screenshots   # regenerate docs/*.png
```

`npm run lint` and `npm run typecheck` check the code.

## Troubleshooting

| What you see | Why | What to do |
|---|---|---|
| `firebase-config.json is missing` when Vite starts | The config file is not in `examples/web/` | Save your web app's config there (step 2), or point `FIREBASE_CONFIG` at it |
| "The database needs an index for this list" on My teams (the SDK logs `The query requires a COLLECTION_GROUP_ASC index for collection members and field uid`) | The `members.uid` collection group index is not deployed | `npm run deploy:aruma`, or deploy `firestore.indexes.json` |
| "Permission denied: the security rules do not allow you to do this." everywhere | The rules are not deployed, so the project is closed | Step 3 |
| "Guest sign-in is turned off for this project." | Anonymous sign-in is off, as in a new Firebase project | `npm run deploy:aruma -- --anonymous`, or Authentication > Sign-in methods |
| `firebase deploy` fails with `HTTP Error: 401, Request is missing required authentication credential` against `http://localhost:8080` | firebase-tools sends its token over https only | Use `npm run deploy:aruma`, the dashboard, or put Aruma behind https |
| `Set either ARUMA_URL (Aruma) or VITE_FIREBASE_EMULATORS=true (the emulators), not both.` | Both are set, perhaps one in `.env.local` | Keep one |
| The emulators hang or cannot reach each other behind a corporate proxy | The Java emulators and firebase-tools pick up `JAVA_TOOL_OPTIONS` and `HTTP(S)_PROXY` | Start them with those variables unset, as `e2e/run.sh` does |
| `aruma serve` stops with `listen 127.0.0.1:9464: bind: address already in use` | Another Aruma on this machine has the metrics port | Set `ARUMA_METRICS_LISTEN` to another address, or `-` to turn metrics off; for the main port use `ARUMA_LISTEN` and `ARUMA_PUBLIC_URL` |
| The yellow "You are offline" banner stays | The Realtime Database connection (`.info/connected`) is down | Check that Aruma runs and that a proxy in front of it passes WebSockets and `/.lp` |

## Using @aruma/web outside this repository

`package.json` takes `@aruma/web` from this repository (`file:../../sdk/web/aruma-web`), like the repository's own web tests. In a project of your own, install it from your clone until it is published:

```bash
npm install firebase
npm install --save-dev /path/to/aruma/sdk/web/aruma-web
```

and keep the same line in `vite.config.ts`. Other bundlers are covered in the [SDK packages](../../docs/sdks.md) page and the package's README.
