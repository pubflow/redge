# Configuration

Redge loads configuration from `.env` and environment variables. Environment variables override defaults.

## Server

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_ADDR` | `0.0.0.0:6379` | Redis/Valkey protocol TCP listener address. |
| `REDGE_HTTP_ENABLED` | `true` | Enables the public HTTP app server. Required when Document API or Store API is enabled. |
| `REDGE_HTTP_ADDR` | `0.0.0.0:8080` | Public HTTP app listener address. Docker/Nixpacks map platform `PORT` here. |
| `REDGE_ENV` | `development` | Runtime environment: `development`, `test`, or `production`. |
| `REDGE_LOG_LEVEL` | `info` | Intended log level: `debug`, `info`, `warn`, or `error`. |
| `REDGE_LOG_FORMAT` | `json` | Log format: `json` or `console`. Development mode uses console logging. |
| `REDGE_API_TOKEN` | empty | Single app token for Document API, Store API, and WebSocket routes. Required in production when either app API is enabled. |
| `REDGE_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout. |
| `REDGE_MAX_REQUEST_BYTES` | `1048576` | Maximum RESP request size. Must be at least `1024`. |
| `REDGE_READ_TIMEOUT` | `30s` | TCP read timeout. |
| `REDGE_WRITE_TIMEOUT` | `30s` | TCP write timeout. |

## App HTTP API

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_API_MAX_BODY_BYTES` | `1048576` | Maximum JSON request body size for Document API and Store API. |
| `REDGE_API_ALLOWED_IPS` | empty | Comma, semicolon, or newline separated IP/CIDR allowlist for app API clients. |
| `REDGE_API_IP_CHECK_ENABLED` | `false` | Enables app API IP allowlist checks. |
| `REDGE_API_CORS_ENABLED` | `false` | Enables explicit browser CORS responses for app API routes. |
| `REDGE_API_CORS_ORIGINS` | empty | Comma-separated allowed browser origins such as `https://app.example.com`. Use `*` only for trusted non-browser/internal scenarios. |
| `REDGE_API_RATE_LIMIT_ENABLED` | `false` | Enables a simple in-process per-token/per-IP token bucket rate limit. |
| `REDGE_API_RATE_LIMIT_RPS` | `30` | Tokens added per second when rate limiting is enabled. |
| `REDGE_API_RATE_LIMIT_BURST` | `60` | Maximum burst tokens when rate limiting is enabled. |
| `REDGE_DOCAPI_ENABLED` | `false` | Enables JSON document routes and optional WebSocket route on `REDGE_HTTP_ADDR`. |
| `REDGE_STOREAPI_ENABLED` | `false` | Enables the public app-facing HTTP Store API. |
| `REDGE_WS_ENABLED` | `true` | Enables `GET /v1/ws` WebSocket upgrades when Document API is enabled. |
| `REDGE_WS_MAX_MESSAGE_BYTES` | `65536` | Maximum WebSocket message size. |
| `REDGE_WS_IDLE_TIMEOUT` | `60s` | Maximum idle time waiting for the next WebSocket message. |
| `REDGE_WS_MAX_SUBSCRIPTIONS` | `32` | Maximum local realtime subscriptions per WebSocket connection. |

Document API, Store API, and WebSocket routes all run on `REDGE_HTTP_ADDR` and use `Authorization: Bearer <REDGE_API_TOKEN>`. WebSocket clients that cannot set headers may pass `?token=`.

Platform HTTPS protects the app HTTP listener when routed through the platform proxy; Redis TCP still needs native Redis TLS for public `rediss://` clients. Admin auth remains separate and only uses `REDGE_ADMIN_TOKEN`.

## Redis Authentication

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_PASSWORD` | empty | Password accepted by the Redis `AUTH` command. |
| `REDGE_REQUIRE_AUTH` | `false` | When `true`, startup fails unless `REDGE_PASSWORD` is set. |
| `REDGE_PROTECTED_MODE` | `true` | In production, prevents public Redis binds without `REDGE_PASSWORD`. |
| `REDGE_ALLOWED_IPS` | empty | Comma, semicolon, or newline separated IP/CIDR allowlist for Redis TCP clients. |
| `REDGE_MAX_CONNECTIONS` | `1000` | Maximum simultaneous Redis TCP connections. `0` disables the limit. |
| `REDGE_AUTH_FAILURE_DELAY` | `250ms` | Delay before replying to failed Redis `AUTH` attempts. |
| `REDGE_TLS_ENABLED` | `false` | Enables native TLS on the Redis TCP listener. Clients must use `rediss://` or TLS options. |
| `REDGE_TLS_CERT_PEM` | empty | TLS certificate chain PEM for Redis TLS, read directly from env. Highest priority. |
| `REDGE_TLS_KEY_PEM` | empty | TLS private key PEM for Redis TLS, read directly from env. Must be set with `REDGE_TLS_CERT_PEM`. |
| `REDGE_TLS_CERT_B64` | empty | Base64 encoded TLS certificate chain PEM. Used when PEM env values are not set. |
| `REDGE_TLS_KEY_B64` | empty | Base64 encoded TLS private key PEM. Must be set with `REDGE_TLS_CERT_B64`. |
| `REDGE_TLS_CERT_FILE` | empty | TLS certificate chain file for Redis TLS, for example `/certs/fullchain.pem`. Used when env PEM/base64 values are not set. |
| `REDGE_TLS_KEY_FILE` | empty | TLS private key file for Redis TLS, for example `/certs/privkey.pem`. Must be set with `REDGE_TLS_CERT_FILE`. |
| `REDGE_TLS_MIN_VERSION` | `1.2` | Minimum TLS version: `1.2` or `1.3`. |

If `REDGE_PASSWORD` is set, clients must authenticate before running most commands. `AUTH`, `PING`, and `QUIT` remain available before authentication. Redge accepts both `AUTH password` and `AUTH default password`.

TLS material precedence is direct PEM env, then base64 env, then mounted files. If the selected source is incomplete, startup fails. Base64 env values are often easiest in Coolify because they avoid multiline formatting issues.

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("fullchain.pem"))
[Convert]::ToBase64String([IO.File]::ReadAllBytes("privkey.pem"))
```

```sh
base64 -w0 fullchain.pem
base64 -w0 privkey.pem
```

Coolify's HTTPS domain certificate is handled by Traefik for HTTP routes and does not automatically encrypt Redis TCP. To use `rediss://`, provide a certificate/key to Redge through one of the variables above.

## Database

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | `sqlite://redge.db` | Database connection URL. |
| `DATABASE_MAX_OPEN_CONNS` | `50` | Maximum open SQL connections for GORM-backed databases. |
| `DATABASE_MAX_IDLE_CONNS` | `10` | Maximum idle SQL connections. |
| `DATABASE_CONN_MAX_LIFETIME` | `1h` | Maximum SQL connection lifetime. |
| `REDGE_MIGRATIONS_AUTO` | `true` | Run schema migrations on startup. |

Supported `DATABASE_URL` formats:

```env
DATABASE_URL=sqlite://redge.db
DATABASE_URL=file:local.db
DATABASE_URL=libsql://example.turso.io
DATABASE_URL=postgres://user:pass@localhost:5432/redge?sslmode=disable
DATABASE_URL=postgresql://user:pass@localhost:5432/redge?sslmode=disable
DATABASE_URL=mysql://user:pass@tcp(localhost:3306)/redge?parseTime=true
DATABASE_URL=d1://cloudflare_account_id/d1_database_id
DATABASE_URL=d1://cloudflare_account_id/d1_database_id?apiToken=cloudflare_token
```

### libSQL/Turso

| Variable | Default | Description |
| --- | --- | --- |
| `TURSO_AUTH_TOKEN` | empty | Optional auth token for libSQL/Turso. Aliases: `DATABASE_AUTH_TOKEN`, `DB_AUTH_TOKEN`, `LIBSQL_AUTH_TOKEN`. |

If the token is included as `authToken=`, `token=`, `auth_token=`, or `jwt=` in the URL, Redge removes it from the GORM DSN and passes auth separately.

### Cloudflare D1

| Variable | Default | Description |
| --- | --- | --- |
| `D1_API_TOKEN` | empty | Required when `DATABASE_URL` starts with `d1://`. |
| `D1_BASE_URL` | empty | Optional Cloudflare API base URL override. |
| `D1_RETRY_MAX` | `3` | Maximum D1 HTTP retries. |
| `D1_RETRY_MIN_BACKOFF` | `200ms` | Minimum retry backoff. |
| `D1_RETRY_MAX_BACKOFF` | `2s` | Maximum retry backoff. |

D1 is accessed through `github.com/pubflow/d1http`, not GORM. This keeps the HTTP API behavior explicit and avoids pretending D1 has local SQL transaction semantics.

The recommended D1 production form keeps the token separate:

```env
DATABASE_URL=d1://cloudflare_account_id/d1_database_id
D1_API_TOKEN=<cloudflare-token>
```

For quick deploys, Redge also accepts D1 connection params inside `DATABASE_URL`:

```env
DATABASE_URL=d1://cloudflare_account_id/d1_database_id?apiToken=<cloudflare-token>
DATABASE_URL=d1://cloudflare_account_id/d1_database_id?apiToken=<token>&baseUrl=https://api.cloudflare.com/client/v4
```

Redge extracts and removes sensitive D1 params before resolving the database URL. Token aliases are `apiToken`, `token`, `authToken`, `auth_token`, and `jwt`; base URL aliases are `baseUrl` and `base_url`.

## Cache

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_CACHE_ENABLED` | `true` | Enables the in-process L1 cache. |
| `REDGE_CACHE_MAX_KEYS` | `100000` | Target key count used to size TinyLFU counters. |
| `REDGE_CACHE_MAX_BYTES` | `134217728` | Ristretto max cost, roughly bytes plus per-entry overhead. |
| `REDGE_CACHE_DEFAULT_TTL` | `5s` | Maximum/default L1 cache TTL. |

The cache is intentionally short lived. Durable state lives in the configured database.

## Expiration Cleanup

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_EXPIRY_CLEANUP_ENABLED` | `true` | Enables periodic expired key cleanup. |
| `REDGE_EXPIRY_CLEANUP_INTERVAL` | `30s` | Cleanup interval. |
| `REDGE_EXPIRY_CLEANUP_LIMIT` | `1000` | Maximum expired keys removed per cleanup pass. |

Reads also lazily delete expired keys when they are encountered.

## Admin API

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_ADMIN_ENABLED` | `true` | Enables the HTTP admin API. |
| `REDGE_ADMIN_ADDR` | `0.0.0.0:9090` | Admin API listener address. |
| `REDGE_ADMIN_TOKEN` | empty | Optional bearer token for admin endpoints. Required in production when admin is enabled. |
| `REDGE_ADMIN_ALLOWED_IPS` | empty | Comma, semicolon, or newline separated IP/CIDR allowlist. |
| `REDGE_ADMIN_IP_CHECK_ENABLED` | `false` | Enables admin IP allowlist checks. |
| `REDGE_ADMIN_READONLY` | `false` | Disables write-capable admin endpoints such as cleanup. |

## Example `.env`

```env
REDGE_ADDR=0.0.0.0:6379
REDGE_HTTP_ENABLED=true
REDGE_HTTP_ADDR=0.0.0.0:8080
REDGE_DOCAPI_ENABLED=false
REDGE_API_TOKEN=
REDGE_API_CORS_ENABLED=false
REDGE_API_RATE_LIMIT_ENABLED=false
REDGE_WS_ENABLED=true
REDGE_STOREAPI_ENABLED=false
REDGE_ENV=production
REDGE_LOG_FORMAT=json

REDGE_REQUIRE_AUTH=true
REDGE_PASSWORD=change-me
REDGE_PROTECTED_MODE=true
REDGE_MAX_CONNECTIONS=1000
REDGE_TLS_ENABLED=true
REDGE_TLS_CERT_B64=<base64-fullchain-pem>
REDGE_TLS_KEY_B64=<base64-private-key-pem>

DATABASE_URL=postgres://redge:password@localhost:5432/redge?sslmode=disable
DATABASE_MAX_OPEN_CONNS=100
DATABASE_MAX_IDLE_CONNS=20
REDGE_MIGRATIONS_AUTO=true

REDGE_CACHE_ENABLED=true
REDGE_CACHE_MAX_KEYS=250000
REDGE_CACHE_MAX_BYTES=268435456
REDGE_CACHE_DEFAULT_TTL=5s

REDGE_ADMIN_ENABLED=true
REDGE_ADMIN_ADDR=127.0.0.1:9090
REDGE_ADMIN_TOKEN=change-me-too
REDGE_ADMIN_READONLY=false
```
