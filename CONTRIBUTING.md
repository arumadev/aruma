# Contributing to Aruma

Thanks for helping. Aruma aims to be a backend that official Firebase SDKs can talk to without surprises, so the bar for changes is that they are verified, not only written.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Code comments cite the architecture decisions by number (ADR-021 and so on). Most "why is it like this" questions are answered by them, and a change that goes against one needs a new decision, not only a patch.
- Behavior that mirrors Firebase is checked against real Firebase and the official emulators. If your change affects it, say how you verified it.

## Set up

Requirements: Go 1.26+, PostgreSQL 18, and Node 24 (only to build the console).

```bash
make dashboard   # build the console once
make dev         # run the server against a local database
```

See the [README](README.md#quick-start) for the details.

## Before you open a pull request

```bash
make lint    # gofmt, vet and the package dependency rules
make test    # unit, acceptance and isolation tests (needs a local Postgres)
make smoke   # the official SDKs against real config files, when your change touches the wire behavior
```

A pull request should:

- do one thing, with a title that says what it does,
- come with tests, and keep project isolation tests passing,
- update the relevant document under `docs/` when behavior changes,
- keep the package dependency rules, which are enforced by a test.

## Security issues

Do not file them as public issues. Follow [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
