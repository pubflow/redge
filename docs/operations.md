# Operations

This guide covers practical deployment and maintenance notes for Redge.

## Local Development

```powershell
go mod tidy
go run ./cmd/redge
```

Default local configuration:

```env
DATABASE_URL=sqlite://redge.db
REDGE_ADDR=0.0.0.0:6379
REDGE_HTTP_ENABLED=true
REDGE_HTTP_ADDR=0.0.0.0:8080
REDGE_ADMIN_ENABLED=true
REDGE_ADMIN_ADDR=0.0.0.0:9090
REDGE_MIGRATIONS_AUTO=true
```

Smoke test:

```powershell
curl.exe http://127.0.0.1:8080/health
curl.exe http://127.0.0.1:8080/ready
redis-cli -p 6379 ping
redis-cli -p 6379 set hello world ex 60
redis-cli -p 6379 get hello
curl.exe http://127.0.0.1:9090/ready
```

## Coolify Deployment

Use one container with two exposed protocols:

- Route your HTTPS domain, for example `https://test1.conn.redgedb.com`, to container port `8080`.
- Expose container port `6379` as a Coolify public TCP port for Redis clients.

Do not use `https://...` as the Redis connection URL. Use the public TCP port that Coolify assigns:

```text
redis://:PASSWORD@test1.conn.redgedb.com:PUBLIC_PORT/0
```

If Redis TLS is enabled, use `rediss://` instead:

```text
rediss://:PASSWORD@test1.conn.redgedb.com:PUBLIC_PORT/0
```

If you use a fixed Coolify port mapping of `6379:6379`, clients can use:

```text
redis://:PASSWORD@test1.conn.redgedb.com:6379/0
```

Prefer object-style client configuration when available because it avoids URL escaping mistakes:

```text
host=test1.conn.redgedb.com
port=6379
password=PASSWORD
db=0
```

If the password is embedded in a URL and contains characters such as `@`, `:`, `/`, `#`, `?`, or `%`, URL-encode it first.

Recommended variables:

```env
REDGE_ENV=production
REDGE_PROTECTED_MODE=true
REDGE_REQUIRE_AUTH=true
REDGE_PASSWORD=<strong-random-password>
DATABASE_URL=sqlite:///data/redge.db
REDGE_ADMIN_ENABLED=false
REDGE_MAX_CONNECTIONS=1000
```

For the default SQLite backend, mount a persistent volume at `/data`. PostgreSQL, MySQL, Turso/libSQL, and D1 are still selected through `DATABASE_URL`.

Security presets:

- Private recommended: do not publish `6379`; connect over private network, VPN, tunnel, or internal service discovery. Keep password auth enabled.
- Public strong: publish TCP `6379`, enable Redis TLS, use a long generated password stored as a Coolify secret, and set `REDGE_ALLOWED_IPS` when client IPs are predictable.

Native Redis TLS example:

```env
REDGE_TLS_ENABLED=true
REDGE_TLS_CERT_FILE=/certs/fullchain.pem
REDGE_TLS_KEY_FILE=/certs/privkey.pem
REDGE_TLS_MIN_VERSION=1.2
```

Mount the certificate directory read-only into the container, for example `/certs`. With Redis TLS enabled, plaintext `redis://` clients fail; clients must use TLS, such as `rediss://`.

If mounting files is inconvenient, provide the certificate through environment variables instead. Redge checks TLS material in this order: direct PEM env, base64 env, then file paths.

Direct PEM env works when your platform preserves multiline secrets, or when you store literal `\n` sequences:

```env
REDGE_TLS_ENABLED=true
REDGE_TLS_CERT_PEM="-----BEGIN CERTIFICATE-----\n...\n-----END CERTIFICATE-----"
REDGE_TLS_KEY_PEM="-----BEGIN PRIVATE KEY-----\n...\n-----END PRIVATE KEY-----"
```

Base64 env is usually the easiest Coolify option because it avoids multiline formatting issues:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("fullchain.pem"))
[Convert]::ToBase64String([IO.File]::ReadAllBytes("privkey.pem"))
```

```sh
base64 -w0 fullchain.pem
base64 -w0 privkey.pem
```

```env
REDGE_TLS_ENABLED=true
REDGE_TLS_CERT_B64=<base64-fullchain-pem>
REDGE_TLS_KEY_B64=<base64-private-key-pem>
REDGE_TLS_MIN_VERSION=1.2
```

For public production, use a certificate from a trusted CA such as Let's Encrypt for the Redis hostname. A private CA or self-signed certificate can work, but every Redis client must trust that CA. Use client insecure settings only for local tests.

Coolify's HTTPS certificate belongs to the Traefik HTTP route. It does not automatically appear inside the Redge container and does not encrypt the Redis TCP public port. To make Redis secure over the public internet, enable Redge Redis TLS and connect with:

```text
rediss://:PASSWORD@test1.conn.redgedb.com:6379/0
redis-cli --tls -h test1.conn.redgedb.com -p 6379 -a "PASSWORD"
```

## Production Checklist

- Set `REDGE_ENV=production`.
- Set `REDGE_PASSWORD` and `REDGE_REQUIRE_AUTH=true`.
- Keep `REDGE_PROTECTED_MODE=true` so production fails closed when Redis is public without a password.
- Set `REDGE_ALLOWED_IPS` if only known clients should connect.
- Set `REDGE_ADMIN_TOKEN` if admin is enabled.
- Bind admin to a private address, for example `127.0.0.1:9090`.
- Use TLS at the load balancer or private network layer when exposing Redge externally.
- Choose the database backend based on write volume, latency, and durability needs.
- Tune SQL connection pool settings for PostgreSQL, MySQL, SQLite, and libSQL.
- Keep `REDGE_CACHE_DEFAULT_TTL` short in multi-instance deployments.

## Database Backend Notes

### SQLite

Good for local development, small deployments, and single-node workloads.

Redge enables:

```sql
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
```

### libSQL/Turso

Good when you want SQLite-like behavior with remote replication options.

Use:

```env
DATABASE_URL=libsql://example.turso.io
TURSO_AUTH_TOKEN=...
```

### PostgreSQL

Good for durable, high-concurrency deployments.

Use:

```env
DATABASE_URL=postgres://user:pass@host:5432/redge?sslmode=require
```

### MySQL

Good for teams already standardized on MySQL-compatible infrastructure.

Use:

```env
DATABASE_URL=mysql://user:pass@tcp(host:3306)/redge?parseTime=true
```

### Cloudflare D1

Good for edge-adjacent deployments and very cheap scale-to-zero style workloads.

Use:

```env
DATABASE_URL=d1://cloudflare_account_id/d1_database_id
D1_API_TOKEN=...
```

D1 is usage-billed by rows read and rows written. Workloads with many tiny writes, such as rate limiting, can still be affordable but should be measured carefully.

## Pipelining and D1

Redis pipelining is a client-side network optimization. The wire protocol sends multiple commands back-to-back, but it does not include an explicit pipeline begin/end marker.

Redge supports pipeline correctness: commands are read and replied to in order.

For D1, true batching of arbitrary pipelined commands requires a server-side coalescer with a tiny time window or a specialized command path. Redge does not currently coalesce arbitrary pipelines into one D1 HTTP request because Cloudflare D1 rejects multi-statement `/raw` requests with params.

## Expired Key Cleanup

Redge removes expired keys in two ways:

- Lazy cleanup during reads.
- Periodic background cleanup when `REDGE_EXPIRY_CLEANUP_ENABLED=true`.

Manual cleanup:

```powershell
curl.exe -X POST -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" http://127.0.0.1:9090/admin/v1/cleanup
```

## Monitoring

Useful endpoints:

```text
GET /health
GET /ready
GET /admin/v1/info
GET /admin/v1/cache
```

Important things to watch:

- Store readiness failures.
- Key count growth.
- Cache hit ratio.
- Cache evictions and rejected sets.
- D1 row reads/writes if using Cloudflare.
- SQL connection pool saturation if using PostgreSQL/MySQL.

## Troubleshooting

### `ECONNREFUSED host:6379`

The Redis TCP port is not reachable from the client. Check that Coolify has either:

- A fixed port mapping such as `6379:6379`.
- A public TCP port pointing to container port `6379`.

If `/health` works but Redis gets `ECONNREFUSED`, the HTTP route is fine and only TCP publishing/firewall needs attention.

### Startup fails with `REDGE_PROTECTED_MODE=true requires REDGE_PASSWORD`

Production is binding Redis to a public interface such as `0.0.0.0:6379` without a password. Set `REDGE_PASSWORD` and `REDGE_REQUIRE_AUTH=true`, or bind Redis to localhost/private networking.

### Redis clients are rejected by allowlist

If `REDGE_ALLOWED_IPS` is set, only matching client IPs/CIDRs can connect to the Redis TCP listener. Remove the variable or add the client public IP/CIDR.

### TLS clients cannot connect

Check:

- `REDGE_TLS_ENABLED=true`.
- TLS material is provided by a complete source: `REDGE_TLS_CERT_PEM`/`REDGE_TLS_KEY_PEM`, `REDGE_TLS_CERT_B64`/`REDGE_TLS_KEY_B64`, or `REDGE_TLS_CERT_FILE`/`REDGE_TLS_KEY_FILE`. Redge uses that precedence order.
- Clients use `rediss://`, `redis-cli --tls`, or library TLS options.
- For self-signed certificates, use a trusted CA or client-specific insecure/test settings only during development.

### `NOAUTH Authentication required.`

The server has `REDGE_PASSWORD` configured, but the client sent a data command before authenticating. Authenticate from the client.

```powershell
redis-cli -a "$env:REDGE_PASSWORD" ping
```

### `WRONGPASS invalid username-password pair or user is disabled.`

The client reached Redge and sent `AUTH`, but the password did not match `REDGE_PASSWORD`. Check:

- `REDGE_PASSWORD` in Coolify matches the client password exactly.
- The application was redeployed after changing environment variables.
- Passwords embedded in `redis://` URLs are URL-encoded.
- If your client sends a username, use `default`; Redge supports `AUTH password` and `AUTH default password`.

### `WRONGTYPE Operation against a key holding the wrong kind of value`

The key exists but has a different Redge type. For example, calling `GET` on a sorted set key.

### GUI shows unsupported command errors

Redis desktop clients often inspect keys before opening them. Redge supports the common inspection commands used for basic browsing: `TYPE`, `DBSIZE`, `STRLEN`, `INFO keyspace`, and `MEMORY USAGE`. If a GUI requests hashes, lists, sets, streams, modules, or Redis Cluster commands, those data structures are still outside Redge's current scope.

### D1 startup fails

Check:

- `DATABASE_URL` starts with `d1://`.
- It has exactly `d1://account_id/database_id`.
- `D1_API_TOKEN` is set.
- The token has access to the D1 database.

### Admin returns `401`

Send the bearer token:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" http://127.0.0.1:9090/admin/v1/info
```
