---
title: Quick start
description: Build the console, start a local Aruma server, configure it for production and operate it from the command line.
---

Requirements: Go 1.26+, PostgreSQL 18 (for example through [DBngin](https://dbngin.com) or [Postgres.app](https://postgresapp.com)), and Node 24 to build the console.

```bash
make dashboard   # build the console and embed it in the binary (once)
make dev         # create .env with a random key on first run, then the database, migrations and the server
```

Open <http://localhost:8080>. The first start prints a one-time setup `code` in the log. Enter it on the setup page to create your first account.

If the Postgres tools are not on your `PATH`, set `ARUMA_PG_BIN`, for example `ARUMA_PG_BIN=/Users/Shared/DBngin/postgresql/18.1/bin`.

## Configure a server

Three settings are required. Everything else has a default.

| Variable | Meaning |
|---|---|
| `ARUMA_DATABASE_URL` | PostgreSQL connection URL |
| `ARUMA_PUBLIC_URL` | The address your SDKs reach, for example `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | 32 random bytes in base64 (`openssl rand -base64 32`). All other keys are derived from it |

Configuration is an optional YAML file (`--config` or `ARUMA_CONFIG`) with `ARUMA_*` environment variables taking precedence. The full reference is in [Configuration](/aruma/docs/reference/configuration/), and [`aruma.example.yaml`](https://github.com/arumadev/aruma/blob/main/aruma.example.yaml) shows every key.

## Operate it

```bash
aruma migrate up|status                 # the server never runs DDL and refuses to start on an old schema
aruma serve
aruma accounts create --email you@example.com
aruma backup --out aruma.dump           # pg_dump, custom format
aruma restore --in aruma.dump --yes     # with the server stopped, in a single transaction
aruma config check
```

## Use it from the command line

Create a CLI token in the console (My account, CLI tokens), then:

```bash
arumactl login --server https://aruma.example.com
arumactl projects create chat-app --name "Chat App"
arumactl apps create --project chat-app --platform android --package com.example.chat
arumactl apps sdkconfig --project chat-app <APP_ID> --out .
arumactl sa create --project chat-app backend
arumactl sa keys create --project chat-app backend@chat-app.sa.aruma.example.com --out key.json
```

The official `firebase` tool works too, through `FIREBASE_API_URL`, `FIREBASE_TOKEN_URL` and `FIREBASE_TOKEN`.

## Connect your app

Download the app's configuration file from the console and put it in your app in place of the Firebase one. Where a platform needs it, add the one build line described in [`sdk/`](https://github.com/arumadev/aruma/tree/main/sdk). For the Realtime Database see the [guide](/aruma/docs/guides/rtdb/).
