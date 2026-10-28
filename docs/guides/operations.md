# Operations

Operator guide for running Aruma services behind your own domain, certificates and proxy. Today it covers Realtime Database. Other services will get their own sections.

## Realtime Database

Every SDK finds a database by the first label of its host, as Firebase's own
`<ns>.firebaseio.com` does (ADR-018). In production each project's database therefore has a host
of its own, `https://<project_id>-default-rtdb.<rtdb.domain>`, and that host needs a DNS name, a
certificate and a proxy that passes it through unchanged. This section sets them up.

### 1. `rtdb.domain`

```yaml
public_url: https://aruma.example.com
rtdb:
  enabled: true
  domain: rtdb.example.com      # ARUMA_RTDB_DOMAIN
metrics_listen: 127.0.0.1:9464  # private; the TLS ask below is served here
trusted_proxies: [127.0.0.1/32] # the proxy's address (section 4 below)
```

- Config files and the dashboard then carry `databaseURL: https://<project_id>-default-rtdb.rtdb.example.com`.
- That URL has no port, so the proxy must serve the database hosts on 443. `rtdb.domain` takes no
  port. Every SDK does accept a port in `databaseURL` (`https://<ns>.rtdb.example.com:8443`): web,
  Admin Node, Android, iOS, Admin Go and Admin Python keep the first label as the namespace, and
  https with a port stays TLS. But you would then have to write that URL into every client
  yourself.
- A dedicated subdomain (`rtdb.example.com`, four-label hosts) is the suggested form. A domain of its
  own (`example-db.com`) works too, and suits a proxy whose free certificate covers one level only.
- `public_url` and `project_domain` must lie outside `rtdb.domain`: every host under it belongs to
  the database.
- Without `rtdb.domain`, databases are reached as `<public_url>/?ns=<project_id>-default-rtdb`. That
  form breaks the iOS, Admin Go and Admin Python SDKs, so it only suits local development on
  `http://127.0.0.1`; the server warns at start when `public_url` is https and `rtdb.domain` is unset.

### 2. The wildcard DNS record

One record covers every project, present and future:

```
*.rtdb.example.com.  300  IN  A     203.0.113.10
*.rtdb.example.com.  300  IN  AAAA  2001:db8::10
```

The address is the proxy in front of Aruma. Nothing else is needed per project.

### 3. Certificates: Caddy with `on_demand_tls`

A wildcard certificate needs a DNS-01 challenge and an API key at your DNS provider. Caddy can
instead obtain one certificate per database host the first time a client connects, if Aruma vouches
for the name. Aruma answers `GET /rtdb/tls-ask?domain=<name>` on `metrics_listen` only: 200 when the
name is `<ns>.<rtdb.domain>` and `ns` the database of an existing, active project, 404 for anything
else, so no one can make Caddy request certificates for names that serve nothing.

```caddyfile
{
	on_demand_tls {
		ask http://127.0.0.1:9464/rtdb/tls-ask
	}
}

aruma.example.com {
	reverse_proxy 127.0.0.1:8080
}

*.rtdb.example.com {
	tls {
		on_demand
	}
	reverse_proxy 127.0.0.1:8080
}
```

- `ask` must point at the private address. If Caddy runs on another machine or in a container,
  bind `metrics_listen` to an interface only Caddy can reach (a private network), never a public
  one: the answer tells which projects exist.
- The first connection to a new project's host waits a few seconds for its certificate; the SDKs
  wait 30 seconds before they give up and retry.
- Caddy keeps each certificate in its storage and renews it on its own: restarting Aruma neither
  asks again nor issues anything new.
- A name the ask refuses gets no certificate, and the client's TLS handshake fails (an "internal
  error" alert). Caddy logs those refusals only at debug level: a `log` block in the global options
  with `level DEBUG` and `include tls` shows "on-demand certificate issuance denied" with Aruma's 404.
- Let's Encrypt allows 50 new certificates per registered domain per week. Beyond that rate of new
  projects, or to avoid the first-connection wait, use a wildcard certificate obtained with DNS-01
  (a Caddy build with your DNS provider's module, lego, certbot or acme.sh). Aruma does not care how
  the certificate was issued.
- Behind Cloudflare's proxy the free certificate covers one level below the zone: either leave the
  `*.rtdb` record DNS only, or use a two-label `rtdb.domain` of its own.

### 4. Proxies: keep the host

The database is named by the host the client connected to, and the server tells clients that host
in the handshake, where they reconnect. A proxy must therefore either:

- **keep the `Host` header** as the client sent it (Caddy's `reverse_proxy` does by default;
  nginx needs `proxy_set_header Host $host;`), or
- **send `X-Forwarded-Host`** with the client's host, from an address listed in `trusted_proxies`
  (`ARUMA_TRUSTED_PROXIES`). Aruma believes `X-Forwarded-Host` and `X-Forwarded-For` only from
  those addresses; from anyone else they are ignored.

A proxy that rewrites the host to `127.0.0.1:8080` without either makes every database request fail
with "Specify the database". If you set the header yourself in Caddy, use `header_up Host
{hostport}`, not `{host}`: the handshake hands that value back to the client, and `{host}` drops a
port other than 443.

**List the proxy in `trusted_proxies` in any case.** Without it, every connection seems to come from
the proxy's address, so the per-address limits (`rtdb.limits.connections_per_ip`, 500 by default,
and the per-address rate limits of the token, console and Auth endpoints) apply to all your users
together. For Caddy on the same machine that
is `127.0.0.1/32`.

### 5. Timeouts: at least 60 seconds idle

Clients hold WebSockets open for hours and long polls for up to 20 seconds per request. The SDKs
send a keep-alive after 45 seconds of silence and Aruma closes a connection after 120 silent
seconds, so every hop between them must allow **at least 60 seconds** of idle time on a connection,
and must pass WebSocket upgrades (`Upgrade` and `Connection` headers).

- **Caddy:** nothing to set. `reverse_proxy` streams WebSockets with no idle limit, and waits for
  a long poll's answer with no limit either. Caddy closes every proxied WebSocket when its config is
  reloaded, unless you set `stream_close_delay` (for example `stream_close_delay 5m` inside
  `reverse_proxy`). That way a reload does not make every client reconnect at the same moment.
- **nginx:**
  ```nginx
  location / {
      proxy_pass http://127.0.0.1:8080;
      proxy_http_version 1.1;
      proxy_set_header Host $host;
      proxy_set_header Upgrade $http_upgrade;
      proxy_set_header Connection $connection_upgrade;  # map $http_upgrade $connection_upgrade
      proxy_read_timeout 120s;
      proxy_send_timeout 120s;
  }
  ```
- **Load balancers** (AWS ALB, Google Cloud, HAProxy `timeout tunnel`): raise the idle timeout to
  120 seconds or more.
- Aruma's own 60-second request timeout and 1 MB body cap do not apply to database connections.

### 6. Capacity: descriptors and the database pool

Measured on the reference server:

- **Each connection holds one file descriptor** and about 40-45 KiB of memory (two goroutines, their
  stacks, and the session), plus about 5 KiB per listen. The process's descriptor limit therefore
  caps the connections: raise it (`LimitNOFILE=` in a systemd unit, `ulimit -n`, `--ulimit nofile` in
  Docker). On stock macOS `kern.maxfilesperproc` stops one process at about 10,200 connections.
- **Each busy database holds a Postgres connection** while its writer commits a batch. When more
  databases write at once than the pool has connections, they queue for one: ten busy databases on
  the default pool (max(4, CPUs)) wrote about 40% less than with `pool_max_conns=32`. Size the pool
  above the number of databases that write at the same time, in `database_url`:
  `postgres://…/aruma?pool_max_conns=32`, within Postgres's `max_connections`.

### 7. Check it

```sh
curl -s -o /dev/null -w '%{http_code}\n' 'http://127.0.0.1:9464/rtdb/tls-ask?domain=chat-app-default-rtdb.rtdb.example.com'  # 200
curl -s -o /dev/null -w '%{http_code}\n' 'https://chat-app-default-rtdb.rtdb.example.com/.settings/rules.json'  # 403 without a credential: the host reaches the database
curl -s 'https://no-such-default-rtdb.rtdb.example.com/'  # fails in the TLS handshake: no certificate for unknown names
```

`test/experiments/rtdb-caddy.sh` runs this whole section on one machine: Caddy with its local CA,
nip.io's public wildcard DNS, the official SDKs through it, and Google's own test suites.

