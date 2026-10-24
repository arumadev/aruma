# Configuration

Aruma reads an optional YAML file (`--config` or `ARUMA_CONFIG`). Every key can be overridden by an `ARUMA_*` environment variable, and the environment wins over the file. Unknown keys are rejected, so a typo fails loudly. [`aruma.example.yaml`](../aruma.example.yaml) shows every key with its default.

Check a configuration without starting the server:

```bash
aruma config check
```

## Reference

| Variable | Required | Default | Meaning |
|---|---|---|---|
| `ARUMA_DATABASE_URL` | yes | | PostgreSQL connection URL |
| `ARUMA_PUBLIC_URL` | yes | | The address your SDKs reach, for example `https://aruma.example.com` |
| `ARUMA_MASTER_KEY` | yes | | 32 random bytes in base64 (`openssl rand -base64 32`). Every other key is derived from it |
| `ARUMA_LISTEN` | | `:8080` | The single public entry point |
| `ARUMA_METRICS_LISTEN` | | `127.0.0.1:9464` | Private address for `/metrics`. The value `-` disables it |
| `ARUMA_SIGNUP_OPEN` | | `false` | Let anyone create an account from the console |
| `ARUMA_PROJECT_DOMAIN` | | host of `public_url` | Base for `authDomain`, container names and service-account emails |
| `ARUMA_TRUSTED_PROXIES` | | | CIDR ranges whose `X-Forwarded-For` is believed |
| `ARUMA_SESSION_IDLE_TIMEOUT` | | `30m` | Console session idle lifetime |
| `ARUMA_SESSION_ABSOLUTE_TIMEOUT` | | `8h` | Console session absolute lifetime |
| `ARUMA_MAIL_DEV_MAILBOX` | | `true` if `public_url` is on loopback, otherwise `false` | Development mailbox: Auth emails of projects without SMTP are kept in the console instead of being sent. An SMTP server is configured per project from the console |
| `ARUMA_MAIL_SMTP_ALLOW_PRIVATE` | | `true` if `public_url` is on loopback, otherwise `false` | Allow an SMTP server on a loopback, private or link-local address (for example Postfix on the same machine). Off by default so the console's SMTP settings cannot reach the server's internal network or the cloud metadata address. Turn it on if your mail relay is local |
| `ARUMA_RTDB_ENABLED` | | `false` | Turn on the Realtime Database |
| `ARUMA_RTDB_DOMAIN` | | | Parent domain for database hosts: `https://<project_id>-default-rtdb.<domain>`. Needs a public DNS record, a certificate and a proxy that preserves the host, see the [Realtime Database operator guide](guides/rtdb-operator.md). Without a value, databases are served with `?ns=` on `public_url`, for local development only |
| `ARUMA_PG_BIN` | | | Directory of the PostgreSQL tools when they are not on `PATH`. Used by `make dev` |

## Minimal production example

```yaml
database_url: postgres://aruma:secret@127.0.0.1:5432/aruma?sslmode=require
public_url: https://aruma.example.com
master_key: "<output of: openssl rand -base64 32>"
trusted_proxies: ["10.0.0.0/8"]
```

Keep `master_key` safe, back it up, and keep it out of version control. Every other key is derived from it.

## Operator commands

```bash
aruma migrate up|status                       # the server never runs DDL and refuses to start on an old schema
aruma serve
aruma accounts create --email you@example.com # password from the terminal without echo, or from stdin
aruma accounts disable|enable|reset-password --email you@example.com
aruma backup --out aruma.dump                 # pg_dump, custom format
aruma restore --in aruma.dump --yes           # with the server stopped, in a single transaction
aruma config check
```
