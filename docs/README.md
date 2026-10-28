# Aruma documentation

Start with [Getting started](getting-started.md). All pages are in English.

## Start

- [Getting started](getting-started.md): run Aruma, create a project and an app, and sign a user in from a web app.
- [SDK packages](sdks.md): every package under `sdk/`, what it does and how to install it.
- [Changelog](CHANGELOG.md): what each release contains, its known limits and how to upgrade.

## Guides

- [Connecting a web app to Aruma Auth](guides/auth-web.md): the `@aruma/web` package and its modes.
- [Verifying users on your server](guides/auth-admin.md): the Node and Go Admin helpers for tokens and users.
- [Connecting your app to Aruma Realtime Database](guides/realtime-database.md): the database URL and each platform.
- [Moving your app's Firestore to Aruma](guides/firestore-migration.md): SDK settings, indexes, rules, TTL and data for the Document Database.
- [Operations](guides/operations.md): domains, certificates, proxies, timeouts and capacity.

## Reference

- [Configuration](reference/configuration.md): every setting, its environment variable and its default.
- [Compatibility](reference/compatibility.md): what works with the official SDKs and tools, per service and platform.
- [Known differences from Firebase](reference/known-differences.md): where Aruma behaves differently and what it means for your app.
- [Console API](reference/console-api.md): the private JSON API behind the dashboard.

## How it works

- [Architecture](architecture.md): what runs, ports and hosts, storage, tenancy, defaults and limits.
- [Security](security.md): what Aruma protects, its assumptions, and the review results.
- [How Aruma is tested](testing.md): the suites, where they run and how to run them.
- [Benchmarks](benchmarks.md): measured numbers, with the machine and the method.
