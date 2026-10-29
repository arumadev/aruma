<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/assets/banner-dark.svg">
  <img alt="Aruma: keep the Firebase SDK, own your backend" src="docs/assets/banner-light.svg" width="100%">
</picture>

<br>

**A self-hosted backend that speaks the official Firebase SDKs.**
One Go binary and PostgreSQL. Your apps keep the official SDKs; a few platforms need a small helper (see [SDK packages](docs/sdks.md)).

<br>

[![License](https://img.shields.io/badge/license-Apache%202.0-1446D8?style=flat-square)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27.3-0FD8C6?style=flat-square&logo=go&logoColor=white)](go.mod)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-18-1446D8?style=flat-square&logo=postgresql&logoColor=white)](#quick-start)
[![Status](https://img.shields.io/badge/status-pre--1.0-0A7D73?style=flat-square)](#status)

[Quick start](#quick-start) · [Status](#status) · [Docs](docs/README.md) · [SDK packages](docs/sdks.md) · [Contributing](CONTRIBUTING.md)

</div>

<br>

## What Aruma is

Aruma is a self-hosted replacement for Firebase. It is one Go binary with PostgreSQL and an embedded Next.js dashboard. It serves Firebase Auth, Realtime Database, Cloud Storage, Security Rules and the Document Database (Firestore-compatible) to the official Firebase SDKs, so for Auth, Realtime Database and Firestore on supported setups you change a config file and not your application code. Storage on iOS uses a patched build of the Storage SDK, Flutter Storage needs one helper call (`fromArumaUrl`), and servers that verify ID tokens use Aruma's admin libraries. See [SDK packages](docs/sdks.md).

It is for developers and teams who use Firebase and want to run the backend themselves, on a small VPS or on their own servers, with their data in a PostgreSQL they control.

Behavior is checked against real Firebase and the official emulators, and the official Firebase test suites run against Aruma. Differences we know of are documented in [known differences](docs/reference/known-differences.md).

## Status

Aruma is pre-1.0. The latest release is [v0.4.0](docs/CHANGELOG.md#v040). Part of the Document Database's verification has not run yet; the [changelog](docs/CHANGELOG.md#v040) lists it.

| Service | Status | Details |
|---|---|---|
| Platform: projects, apps, API keys, service accounts, dashboard, Management API | Released | [Architecture](docs/architecture.md) |
| Authentication | Released in v0.1.0 | [Compatibility](docs/reference/compatibility.md#auth), [web guide](docs/guides/auth-web.md), [Admin guide](docs/guides/auth-admin.md) |
| Realtime Database | Released in v0.2.0, off by default (`ARUMA_RTDB_ENABLED`) | [Compatibility](docs/reference/compatibility.md#realtime-database), [guide](docs/guides/realtime-database.md) |
| Security Rules | Released in v0.3.0 | [Compatibility](docs/reference/compatibility.md#security-rules), [guide](docs/guides/security-rules.md) |
| Cloud Storage | Released in v0.3.0, off by default (`ARUMA_STORAGE_ENABLED`) | [Compatibility](docs/reference/compatibility.md#cloud-storage), [guide](docs/guides/storage.md) |
| Document Database (Firestore-compatible) | Released in v0.4.0, off by default (`ARUMA_DOCUMENTS_ENABLED`); official mobile suites not run yet | [Compatibility](docs/reference/compatibility.md#firestore), [guide](docs/guides/firestore.md), [migration guide](docs/guides/firestore-migration.md) |
| Cloud Messaging, App Check, Remote Config, Functions | Planned | |

## Quick start

With Docker (Aruma and PostgreSQL 18, nothing else to install):

```bash
git clone --branch v0.4.0 https://github.com/arumadev/aruma.git && cd aruma
printf 'ARUMA_MASTER_KEY=%s\nPOSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32)" "$(openssl rand -hex 24)" > .env
docker compose up -d
docker compose logs aruma | grep code    # the one-time setup code
```

Open <http://localhost:8080>, enter the setup code to create your first account, then create a project and an app and copy the config file. Keep `.env`: every key Aruma uses is derived from `ARUMA_MASTER_KEY`.

Without Docker, download the archive for your system from [Releases](https://github.com/arumadev/aruma/releases) (Linux, macOS and Windows, amd64 and arm64) and point it at your PostgreSQL 18, or build from source with Go 1.27.3 and Node 24 (`make dashboard && make dev`).

[Getting started](docs/getting-started.md) goes on from here: the three ways to install, running on a server, creating a project and an app, and signing a user in from a web app. Settings are in [configuration](docs/reference/configuration.md).

## How apps connect

Your app keeps the official SDK and takes the config file from Aruma. Where a platform has Google's hosts compiled in, a small package under [`sdk/`](sdk) points the SDK at your server with one line of build or manifest configuration. The packages and what each one does are in [SDK packages](docs/sdks.md).

```mermaid
flowchart LR
    subgraph Apps["Your apps, official Firebase SDKs"]
        W[Web]
        I[iOS]
        A[Android]
        F[Flutter]
        N[Admin: Node, Go]
    end
    Apps -- "same SDK, new config" --> E
    subgraph Aruma["Aruma, one Go binary"]
        E[Single entry point] --> Auth
        E --> RTDB[Realtime Database]
        E --> Storage[Cloud Storage]
        E --> Docs[Document Database]
        E --> Console[Dashboard and Management API]
    end
    Auth --> PG[(PostgreSQL)]
    RTDB --> PG
    Storage --> PG
    Docs --> PG
    Console --> PG
```

Every project's data is reached through its project number, directly or through a project-scoped parent such as a Realtime Database id. A CI test fails any SQL query on a project table that neither filters by project nor carries a reviewed scope annotation. Aruma does not use PostgreSQL row-level security. See [architecture](docs/architecture.md#tenancy).

**Cloud Functions.** Aruma does not serve Cloud Functions. If an app signs in with Aruma Auth and calls a function with `httpsCallable`, the SDK sends the user's Aruma ID token to Google. Do not use callable functions in such an app. See [known differences](docs/reference/known-differences.md).

## The dashboard

Every Aruma server ships with a dashboard, embedded in the same binary. Create projects and apps, manage users, edit rules, browse data and files, and download the config files your apps need.

<p align="center">
  <img src="docs/assets/console-auth.png" alt="Aruma dashboard: Authentication, listing a project's users" width="100%">
</p>

<table>
  <tr>
    <td width="50%"><img src="docs/assets/console-rtdb.png" alt="Aruma dashboard: Realtime Database data and databaseURL"></td>
    <td width="50%"><img src="docs/assets/console-storage.png" alt="Aruma dashboard: Cloud Storage usage"></td>
  </tr>
</table>

## Documentation

- [Getting started](docs/getting-started.md)
- [Documentation index](docs/README.md)
- Guides: [web Auth](docs/guides/auth-web.md), [Admin Auth](docs/guides/auth-admin.md), [Realtime Database](docs/guides/realtime-database.md), [Cloud Storage](docs/guides/storage.md), [security rules](docs/guides/security-rules.md), [Firestore](docs/guides/firestore.md), [Firestore migration](docs/guides/firestore-migration.md), [mobile apps](docs/guides/mobile-apps.md), [operations](docs/guides/operations.md)
- Reference: [configuration](docs/reference/configuration.md), [compatibility](docs/reference/compatibility.md), [known differences](docs/reference/known-differences.md), [console API](docs/reference/console-api.md)
- [Architecture](docs/architecture.md), [security](docs/security.md), [testing](docs/testing.md), [benchmarks](docs/benchmarks.md)
- [Changelog](docs/CHANGELOG.md)

To report a vulnerability, read [SECURITY.md](SECURITY.md).

## Contributing

Issues and pull requests are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0, see [LICENSE](LICENSE) and [NOTICE](NOTICE). Firebase is a trademark of Google LLC. Aruma is not affiliated with Google and is not endorsed by it.
