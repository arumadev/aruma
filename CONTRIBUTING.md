# Contributing to Aruma

Thanks for helping. Aruma aims to be a backend that official Firebase SDKs can talk to without surprises, so the bar for changes is that they are verified, not only written.

## Before you start

- For anything larger than a small fix, open an issue first so we can agree on the approach.
- Read [docs/architecture.md](docs/architecture.md) first. It says how the parts fit together. A change that goes against it needs a discussion in the issue, not only a patch.
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
test/ci/repo-hygiene.sh   # English-only docs and comments, no local paths
make test    # unit, acceptance and isolation tests (needs a local Postgres)
make smoke   # the official SDKs against real config files, when your change touches the wire behavior
```

A pull request should:

- do one thing, with a title that says what it does,
- come with tests, and keep project isolation tests passing,
- update the relevant document under `docs/` when behavior changes (see below),
- keep the package dependency rules, which are enforced by a test.

## Documentation

- Documentation is in English. This includes `README.md`, everything under `docs/`, code comments, and the READMEs next to the code. CI fails if a Markdown file outside `dashboard/` and `test/oracle/` has Arabic letters, or if a code comment has them. Translations of the dashboard live in `dashboard/messages/`.
- Documentation is for people who use or run Aruma. Write what a reader needs to do something or to decide something. Do not add working notes, research notes, plans or design records. Keep those in the issue or the pull request.
- Where things go:
  - `docs/getting-started.md`: the first run.
  - `docs/guides/`: how to do one task, for example connect a platform or run a service.
  - `docs/reference/`: facts to look up: configuration, compatibility, known differences, the console API.
  - `docs/architecture.md`, `docs/security.md`, `docs/testing.md`, `docs/benchmarks.md`: how Aruma works and how it is checked.
  - `docs/sdks.md`: the packages under `sdk/`. Each package has its own README.
  - `docs/CHANGELOG.md`: one section per release. Add your change to the newest section when a user can notice it.
- Use plain, short sentences. Link to other pages with relative links, and keep every link working. `test/ci/repo-hygiene.sh` checks the rules above, and it also fails on a new top-level entry under `docs/` and on paths from a developer's machine.

## Security issues

Do not file them as public issues. Follow [SECURITY.md](SECURITY.md).

## License

By contributing you agree that your contribution is licensed under the [Apache License 2.0](LICENSE).
