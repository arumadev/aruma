# Examples

Two example apps that run on an Aruma server with the official Firebase SDKs. They are the same app, **Team Board**, built twice: once for the web and once with Flutter. Both use the same data model and the same security rules, so a web client and a Flutter client of one project share teams, tasks, chat and presence.

| Example | Stack | Platforms |
|---|---|---|
| [web](web) | React, TypeScript, Vite, the `firebase` package and `@aruma/web` | Browsers |
| [flutter](flutter) | Flutter, FlutterFire and the Aruma helper packages | Web, Android, iOS |

## What the app shows

| Service | In the app |
|---|---|
| Authentication | Sign-up and sign-in with email and password, and a guest (anonymous) user |
| Document Database (Firestore-compatible) | Teams joined by a code, tasks that update live, rules that read membership with `exists()` and `getAfter()`, a collection group query for "my teams" |
| Realtime Database | Team chat, and who is online with `onDisconnect` |
| Cloud Storage | A profile picture, and one attachment per task that only the team's members may open |

The apps are in English and Arabic (right to left), with light and dark themes.

## The same code on Firebase

Each app takes its backend from one config file and the address of the Aruma server. Neither has code written for Aruma apart from the documented setup lines, and each README says how to run the same code on Firebase or on the Firebase emulators.

## The shared model

- The security rules are `firestore.rules`, `database.rules.json` and `storage.rules`, the same three files in both examples. Deploy them from either one.
- `firestore.indexes.json` holds one collection group index, for the "my teams" query. Aruma and Firebase both refuse that query without it. The Firebase emulator does not check indexes.
- Realtime Database rules cannot read the Document Database, so a user who joins a team also writes `members/{team}/{uid}` in the Realtime Database. A member the owner removes keeps chat and presence access until that entry is deleted too. Each README says so; a real app would write and delete the entry from its server.

## What was tested

| | Web example | Flutter example |
|---|---|---|
| End to end in Chromium, two users and a guest, against a real Aruma server | Yes, in CI | Yes, the web build, in CI |
| The same code against the Firebase emulators | Yes | Not run |
| Android build | Does not apply | Configured as the guides describe; the build was not completed |
| iOS build | Does not apply | Configured as the guides describe; not built |
| Real Firebase | Not run | Not run |

Each example has an `e2e/run.sh` that starts a server on a throwaway database, deploys the rules, builds the app and drives it in a browser. Nothing in a run leaves the machine.
