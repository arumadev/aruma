# Security rules in Aruma

Security rules decide what your app's users can read and write. Aruma uses the same rules languages as Firebase, so you can bring your rules files as they are. This guide covers all three services: Realtime Database, Cloud Storage and Firestore (called the Document Database in Aruma). For the exact status of each feature, see [compatibility](../reference/compatibility.md#security-rules).

## Which language each service uses

| Service | Language | File in a Firebase project | Where Aruma runs it |
|---|---|---|---|
| Realtime Database | JSON rules (`.read`, `.write`, `.validate`, `.indexOn`) | `database.rules.json` | The Realtime Database itself |
| Cloud Storage | Rules language, `service firebase.storage` | `storage.rules` | One rules engine, shared with Firestore |
| Firestore (Document Database) | Rules language, `service cloud.firestore` | `firestore.rules` | The same engine |

Storage and Firestore share one grammar and one standard library. Realtime Database rules are a separate JSON format. They support the whole language except `getPriority()`, which is rejected when you save. See [Realtime Database](realtime-database.md#rules).

## The default is closed

- A new project starts with locked rules. Realtime Database is `{".read": false, ".write": false}`. Storage and Firestore are `allow read, write: if false;`.
- Clients can reach nothing until you publish rules.
- Admins are not bound by rules, as in production. That means a service account of the project, the project owner's CLI token (`aruma_pat_...`), the Admin SDKs, and the dashboard's own data screens.
- Storage download links (with a `token=`) skip the rules. See [Cloud Storage](storage.md#4-download-urls-and-tokens).

## Write and deploy rules

You have three ways, and they end in the same place.

### With the `firebase` tool

Keep your `firebase.json`:

```json
{
  "database":  { "rules": "database.rules.json" },
  "storage":   { "rules": "storage.rules" },
  "firestore": { "rules": "firestore.rules" }
}
```

Point the tool at Aruma with a CLI token (dashboard > Account > CLI tokens) and https addresses. Each service needs its own address variables. Set the ones for the services you deploy:

```sh
A=https://aruma.example.com
export FIREBASE_API_URL=$A FIREBASE_TOKEN_URL=$A FIREBASE_RULES_URL=$A \
  FIREBASE_FIREBASESTORAGE_URL=$A FIREBASE_SERVICE_USAGE_URL=$A FIRESTORE_URL=$A \
  FIREBASE_RTDB_MANAGEMENT_URL=$A FIREBASE_TOKEN=<CLI token> FIREBASE_SKIP_INFORMATIONAL_IAM=1

firebase deploy --only database --project chat-app
firebase deploy --only storage --project chat-app
firebase deploy --only firestore:rules --project chat-app
```

- A compile error is printed with its line and column, and the previous rules stay.
- Deploying a file that has not changed says "already up to date". It does not create a new ruleset.
- Storage rules are covered in [Cloud Storage](storage.md#7-storage-rules). Firestore rules in [Moving your app's Firestore to Aruma](firestore-migration.md).
- The tool reaches Storage and Firestore rules through the same `firebaserules` API that Firebase has. It is meant for the tool, and this guide does not document it for direct use.

### In the dashboard

| Service | Where | What the editor offers |
|---|---|---|
| Realtime Database | project > Realtime Database > Rules | JSON editor, templates (Locked mode, Test mode), errors with line and column, **Publish**, **Discard changes** |
| Cloud Storage | project > Cloud Storage > Rules | Templates (Locked, Per-user folders, Any signed-in user), **Check**, **Publish**, **Discard changes** |
| Firestore | project > Document Database > Rules | Checks as you type, a version history, **Publish**, **Discard**, and a Rules Playground |

The dashboard warns you when the rules you are about to publish give anyone access.

### With the management API

Only Realtime Database rules have an API that is documented for you to call:

- `PUT /.settings/rules.json` with an admin token. Add `?dryRun=true` to check without saving. See the [REST section](realtime-database.md#rest).
- `GET` and `PUT` on `/console/v1/projects/{project}/database/rules` with a session or CLI token. See [console-api](../reference/console-api.md#realtime-database-data-and-rules).

An error comes back as `line:column: message`.

## `dev_open_rules`

`dev_open_rules` opens a service to your own machine while you develop, like Firebase's test mode. It exists for two services:

| Service | Key | Variable |
|---|---|---|
| Realtime Database | `rtdb.dev_open_rules` | `ARUMA_RTDB_DEV_OPEN_RULES` |
| Firestore (Document Database) | `documents.dev_open_rules` | `ARUMA_DOCUMENTS_DEV_OPEN_RULES` |

Cloud Storage has no such setting. To try Storage, publish a rules file such as "Any signed-in user".

What it does:

- Clients on the same machine can read and write every database past the rules, without admin credentials.
- The server refuses to start with it on unless `public_url` and `listen` are both loopback addresses. It applies to loopback peers only.
- Any page open in a browser on your machine counts as such a client.

What it does not do: it does not test your rules. With it on, every request is allowed, so a rule that denies too much looks fine. Turn it off and try your rules with a real signed-in user before you ship. See [configuration](../reference/configuration.md#document-database) for the exact wording.

## What the rules look like

Storage:

```
rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /users/{uid}/{rest=**} {
      allow read: if request.auth != null;
      allow write: if request.auth.uid == uid
                   && request.resource.size < 5 * 1024 * 1024
                   && request.resource.contentType.matches('image/.*');
    }
  }
}
```

Firestore:

```
rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid} {
      allow read, write: if request.auth != null && request.auth.uid == uid;
    }
    match /posts/{id} {
      allow read: if true;
      allow create: if request.auth != null
                    && get(/databases/$(database)/documents/users/$(request.auth.uid)).data.role == 'editor';
    }
  }
}
```

Realtime Database:

```json
{
  "rules": {
    "messages": {
      ".indexOn": ["ts"],
      "$id": { ".read": "auth != null", ".write": "auth != null" }
    }
  }
}
```

Queries on Realtime Database are filtered on the server, so add `.indexOn` for each child you order by. See [Realtime Database](realtime-database.md#rules).

## How Aruma evaluates rules

Aruma has its own rules engine for Storage and Firestore. It follows Firebase's language and its limits. It decides the same way (allow or deny) as Firebase on every case it was compared on. Rules run when you publish (compile) and on every request (evaluate).

**Supported:**

- Strings, regular expressions, math, hashing, bytes, timestamps and durations, paths, lists, maps and sets.
- `request.auth` and `request.auth.token` claims. Numbers keep their exact type and precision.
- `get()`, `exists()`, `getAfter()` and `existsAfter()` on Firestore documents.
- `firestore.get()` and `firestore.exists()` from Storage rules, which read Firestore.
- `debug()`, which returns its first argument, as published rules do.

**Lookup budgets.** A rule that reads documents is limited:

| Where | Documents a request may read |
|---|---|
| Firestore | 10 per request, 20 for a batch or transaction |
| Storage (`firestore.get()` and `firestore.exists()`) | 2 per request |

**Language limits** are Firebase's own, and you can lower them but never raise them (a higher value is refused at startup). Under `rules.limits` in `aruma.yaml`, or as `ARUMA_RULES_<KEY>`:

| Key | Default |
|---|---|
| `call_depth` | 20 |
| `function_lets` | 10 |
| `function_args` | 7 |
| `expression_depth` | 98 |
| `match_depth` | 10 |
| `match_segments` | 100 |
| `match_variables` | 20 |
| `source_bytes` (a rules file) | 262144 |
| `expressions` (per request) | 1000 |
| `lookups_per_operation` | 10 |
| `lookups_per_request` | 20 |
| `storage_lookups` | 2 |
| `rulesets_per_project` | 2500 |

**Aruma's own limits** protect the server from hostile rules: the total size of compiled regular expressions in a ruleset (4 MiB), the compiled rulesets kept in memory, one pattern's program size, and evaluation work and memory. Only unusual rulesets reach them. A file past them fails to compile with "Regular expressions are too complex to compile safely" or "The rules are too large to keep compiled". See [configuration](../reference/configuration.md#security-rules-limits) for the keys.

**Closed by default.** If a request matches no `allow` statement, it is denied. A project with no published rules denies every client request.

## Test your rules locally

There is no emulator to start. Run Aruma on your machine and test with real users:

1. Run the server as the "Running locally" section of the [README](../../README.md) describes, and turn on the service you test.
2. Publish your rules with the dashboard or the `firebase` tool, against `http://127.0.0.1:8080` or your development address.
3. Sign in as two or more test users with Aruma Auth, and run the reads and writes your app makes. Try the ones that should fail, too.
4. Leave `dev_open_rules` off.

Tools that help:

- **Firestore Rules Playground** in the dashboard: pick a method (get, create, update, delete), a path, whether the user is signed in, the auth fields and the document data. It shows whether the request is allowed or denied and which `allow` statements matched.
- **Check** on the Storage rules page and the live check on the Firestore page: they compile your rules and show errors and warnings with line and column.
- **Dry run** for Realtime Database: `PUT /.settings/rules.json?dryRun=true`.

The firebaserules `:test` call is partial: compile errors are reported as Firebase reports them, but a ruleset with `testCases` is refused (400) and a `testSuite` is not run. A rules unit-test setup that sends `testCases` will not work against Aruma.

## Differences from Firebase

The allow or deny decision is the same. These are the documented differences:

- `values()` of a map keeps the order of the written map, where Firebase's order is not stable. `keys()` is always sorted. Do not depend on the order of `values()`.
- A replacement text in `replace()` that is not valid (a lone `$`) is an evaluation error in Aruma, so the rule denies. Firebase answers an internal error. Both reject the request.
- `'x'[float(...)]` compiles in Aruma and denies at evaluation. Firebase fails at compile time.
- Aruma reports errors and a few warnings: an unknown or unused function, and a missing `rules_version`. Firebase reports many more type and argument-count warnings. A clean deploy on Aruma does not prove a clean deploy on Firebase.
- Error messages can differ in wording. A very large `split()` is rejected with "evaluation exceeded the allocation limit of 16777216 bytes".
- Storage `list` on a path that does not end in `/` is decided on the folder.
- When the server is busy, compiling waits for a free slot. A request that waits too long gets 503 or `CANCELLED`. Retry.
- Realtime Database: `getPriority()` is rejected when you save, and queries need `.indexOn` to be filtered on the server.

The full list is in [known differences](../reference/known-differences.md#security-rules-firestore-and-storage), and the status of each feature is in [compatibility](../reference/compatibility.md#security-rules).

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `permission_denied`, `PERMISSION_DENIED`, `storage/unauthorized` | The rules are locked by default, or do not allow the request | Publish rules, and test with a signed-in user |
| Everything works locally and fails after you turn off `dev_open_rules` | The rules never allowed the request | Fix the rules. The setting hid the problem |
| The server will not start with `dev_open_rules` | `public_url` or `listen` is not a loopback address | Use `127.0.0.1` for both, or turn it off |
| "Regular expressions are too complex to compile safely" | The ruleset is past Aruma's regex limit | Reduce the patterns |
| Deploy works on Aruma, warns on Firebase | Aruma reports fewer warnings | Deploy to a Firebase project, or read the warnings there |
| A Storage rule with `firestore.get()` is denied | More than 2 lookups, or the document is missing | Keep to 2 lookups and check the path |
