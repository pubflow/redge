# Redge

Redge is a Redis/Valkey protocol compatible edge KV server backed by SQL databases.

This first implementation includes:

- RESP2 TCP listener.
- Public HTTP status server for platform health checks.
- Redis client compatibility for common string, TTL, sorted set, scan, pipeline, and simple transaction flows.
- GUI-friendly inspection commands such as `TYPE`, `DBSIZE`, `STRLEN`, and `MEMORY USAGE`.
- Multi-database support through `DATABASE_URL`: SQLite, libSQL/Turso, PostgreSQL, MySQL, and Cloudflare D1.
- L1 in-memory cache powered by Ristretto/TinyLFU with TTL, max keys, max bytes, and admin metrics.
- Admin HTTP server with health, cache metrics, cleanup, paginated key inspection, single-key deletion, and sorted-set inspection.
- Optional JSON Document API with explicit field indexes and local WebSocket realtime subscriptions.
- Optional public Store HTTP API with strings, TTL, counters, scans, and sorted sets for app clients that need HTTPS instead of Redis TCP.

## Run Locally

```powershell
go mod tidy
go run ./cmd/redge
```

Then test:

```powershell
curl.exe http://127.0.0.1:8080/health
curl.exe http://127.0.0.1:8080/ready
redis-cli -p 6379 ping
redis-cli -p 6379 set hello world ex 60
redis-cli -p 6379 get hello
```

## Deploy on Coolify

Redge exposes two different protocols:

- HTTP status on container port `8080`.
- Redis/Valkey TCP on container port `6379`.

Point your Coolify domain, for example `https://test1.conn.redgedb.com`, to port `8080`. That URL is for `/health`, `/ready`, and `/version`; it is not a Redis URL.

Expose Redis by making container port `6379` public as a TCP/public port in Coolify. Clients connect with the domain plus that public port:

```text
redis://:PASSWORD@test1.conn.redgedb.com:PUBLIC_PORT/0
```

For public encrypted Redis, enable native Redis TLS and use `rediss://`:

```text
rediss://:PASSWORD@test1.conn.redgedb.com:PUBLIC_PORT/0
```

If you map host port `6379` directly to container port `6379`, clients can use:

```text
redis://:PASSWORD@test1.conn.redgedb.com:6379/0
```

Prefer object-style client config when your library supports it:

```text
host=test1.conn.redgedb.com
port=6379
password=PASSWORD
db=0
```

If `PASSWORD` contains URL-significant characters like `@`, `:`, `/`, `#`, `?`, or `%`, URL-encode it before putting it in a `redis://` URL.

Recommended production variables:

```env
REDGE_ENV=production
REDGE_PROTECTED_MODE=true
REDGE_REQUIRE_AUTH=true
REDGE_PASSWORD=<strong-random-password>
DATABASE_URL=sqlite:///data/redge.db
REDGE_ADMIN_ENABLED=false
REDGE_DOCAPI_ENABLED=false
REDGE_STOREAPI_ENABLED=false
REDGE_MAX_CONNECTIONS=1000
```

Use a persistent volume mounted at `/data` when using the default SQLite database.

Security presets:

- Private recommended: route HTTPS to `8080`, do not publish `6379`, and access Redis through a private network, VPN, tunnel, or internal service discovery. Keep `REDGE_REQUIRE_AUTH=true`.
- Public strong: publish TCP `6379`, enable `REDGE_TLS_ENABLED=true`, provide cert/key through base64 env secrets or mounted files, use a long random `REDGE_PASSWORD`, and set `REDGE_ALLOWED_IPS` when client IPs are known.

Coolify's HTTPS domain protects only the HTTP status route. It does not automatically encrypt Redis TCP, so public Redis clients should use `rediss://` with TLS enabled in Redge.

## Deploy (Nixpacks)

This repo includes `nixpacks.toml` with Go 1.25 build and a startup command that maps platform `PORT` to the HTTP status server.

Recommended environment variables:

```env
REDGE_ENV=production
DATABASE_URL=sqlite:///data/redge.db
REDGE_ADMIN_ENABLED=false
REDGE_HTTP_ENABLED=true
# Optional:
# REDGE_ADDR=0.0.0.0:6379
# PORT=8080
# ADMIN_PORT=9090
```

Notes:

- If your platform sets `PORT`, Redge will bind HTTP status to `0.0.0.0:${PORT}`.
- Redis TCP remains on `REDGE_ADDR`, defaulting to `0.0.0.0:6379`.
- If you want admin HTTP on a second port, set `REDGE_ADMIN_ENABLED=true` and `ADMIN_PORT`.

## Deploy (Docker)

Build and run:

```powershell
docker build -t redge:latest .
docker run --rm -p 8080:8080 -p 6379:6379 -e REDGE_REQUIRE_AUTH=true -e REDGE_PASSWORD=<strong-random-password> redge:latest
```

Production-like example with persistent SQLite data:

```powershell
docker run -d --name redge \
	-p 8080:8080 \
	-p 6379:6379 \
	-v redge_data:/data \
	-v /path/to/certs:/certs:ro \
	-e REDGE_ENV=production \
	-e REDGE_PROTECTED_MODE=true \
	-e REDGE_REQUIRE_AUTH=true \
	-e REDGE_PASSWORD=<strong-random-password> \
	-e REDGE_TLS_ENABLED=true \
	-e REDGE_TLS_CERT_FILE=/certs/fullchain.pem \
	-e REDGE_TLS_KEY_FILE=/certs/privkey.pem \
	-e DATABASE_URL=sqlite:///data/redge.db \
	-e REDGE_ADMIN_ENABLED=false \
	redge:latest
```

If you do not want to mount certificate files, set base64 env values instead:

```env
REDGE_TLS_ENABLED=true
REDGE_TLS_CERT_B64=<base64-fullchain-pem>
REDGE_TLS_KEY_B64=<base64-private-key-pem>
```

Generate those values from certificate files:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("fullchain.pem"))
[Convert]::ToBase64String([IO.File]::ReadAllBytes("privkey.pem"))
```

The Docker image maps platform `PORT` to the HTTP status server automatically. Redis TCP stays on `6379` unless you set `REDGE_ADDR`.

## Database URLs

```env
DATABASE_URL=sqlite://redge.db
DATABASE_URL=libsql://example.turso.io
TURSO_AUTH_TOKEN=...
DATABASE_URL=postgres://user:pass@localhost:5432/redge?sslmode=disable
DATABASE_URL=mysql://user:pass@tcp(localhost:3306)/redge?parseTime=true
DATABASE_URL=d1://cloudflare_account_id/d1_database_id
D1_API_TOKEN=...
# Quick D1 connection-string form:
DATABASE_URL=d1://cloudflare_account_id/d1_database_id?apiToken=cloudflare_token
```

D1 uses `github.com/pubflow/d1http` directly instead of GORM or `database/sql`, so Redge can preserve D1 metadata and avoid fake SQL transaction semantics on the hot path.
For production, prefer keeping D1 tokens in `D1_API_TOKEN`; when a token is embedded in `DATABASE_URL`, Redge extracts it and sanitizes the resolved URL before use.

## Redis Compatibility

Supported connection and server commands:

- `AUTH`, `PING`, `ECHO`, `QUIT`, `SELECT`, `CLIENT SETNAME`, `CLIENT GETNAME`, `INFO`, `COMMAND`, `DBSIZE`.

Supported string and TTL commands:

- `GET`, `MGET`, `SET`, `MSET`, `SETNX`, `GETSET`, `GETDEL`, `SETEX`, `DEL`, `EXISTS`, `TYPE`, `STRLEN`, `MEMORY USAGE`, `EXPIRE`, `PERSIST`, `TTL`, `PTTL`, `INCR`, `DECR`, `INCRBY`, `DECRBY`.

Supported sorted set commands:

- `ZADD`, `ZCARD`, `ZREM`, `ZREMRANGEBYSCORE`, `ZRANGE`, `ZRANGEBYSCORE`, `ZREVRANGEBYSCORE`, `ZSCORE`, `ZCOUNT`.

Supported iteration and batching behavior:

- `SCAN` with `MATCH` and `COUNT`.
- RESP pipelining from clients like ioredis.
- Simple `MULTI`, `EXEC`, and `DISCARD` queued execution.

Supported admin inspection routes:

- `GET /admin/v1/keys?match=session:*&cursor=0&count=100`
- `GET /admin/v1/keys/{key}`
- `DELETE /admin/v1/keys/{key}`
- `GET /admin/v1/zsets/{key}?start=0&stop=99`

Cloudflare D1 notes:

- D1 schema migrations use explicit single-statement HTTP calls where required by the D1 HTTP API.
- Parameterized hot-path commands use single prepared HTTP calls because the D1 HTTP API rejects multiple statements with params in one `/raw` request.
- ioredis pipeline correctness is supported at the Redis protocol layer. True D1 write coalescing for arbitrary client pipelines needs a dedicated server-side coalescer because Redis pipelining has no explicit begin/end marker on the wire.

Not supported yet:

- Redis Cluster, Lua scripting, Pub/Sub, Streams, hashes, sets, lists, blocking list commands, `KEYS`, `WATCH`/`UNWATCH`, geospatial commands, and probabilistic data structures.

## Document API

The Document API is disabled by default and runs on a separate HTTP listener:

```env
REDGE_DOCAPI_ENABLED=true
REDGE_API_TOKEN=<strong-random-app-token>
REDGE_WS_ENABLED=true
```

Routes:

- `POST /v1/collections/{collection}`
- `GET /v1/collections/{collection}/{id}`
- `PUT /v1/collections/{collection}/{id}`
- `PATCH /v1/collections/{collection}/{id}`
- `DELETE /v1/collections/{collection}/{id}`
- `GET /v1/collections/{collection}?where=field:eq:value&search=text`
- `PUT /v1/collections/{collection}/indexes`
- `GET /v1/ws`

Field queries require explicit indexes. Configure them with:

```json
{"fields":["category"],"searchFields":["name","description"]}
```

## Store API

The Store API is disabled by default and runs on the same public HTTP app listener as status and Document API:

```env
REDGE_STOREAPI_ENABLED=true
REDGE_API_TOKEN=<strong-random-app-token>
```

Routes include:

- `GET`, `PUT`, and `DELETE /v1/kv/{key}`
- `POST /v1/kv/batch/get`
- `POST /v1/kv/batch/set`
- `POST /v1/kv/{key}/getdel`
- `POST /v1/kv/{key}/incr`
- `POST /v1/kv/{key}/expire`
- `POST /v1/kv/{key}/persist`
- `GET /v1/kv/{key}/ttl`
- `GET /v1/kv?match=prefix:*&cursor=0&limit=100`
- `POST /v1/zsets/{key}/members`
- `GET /v1/zsets/{key}/members`
- `GET /v1/zsets/{key}/byscore`
- `DELETE /v1/zsets/{key}/members/{member}`
- `DELETE /v1/zsets/{key}/byscore`
- `GET /v1/zsets/{key}/score/{member}`
- `GET /v1/zsets/{key}/count`
- `GET /v1/zsets/{key}/card`

Document API and Store API use `REDGE_API_TOKEN`. Admin auth remains separate through `REDGE_ADMIN_TOKEN`, and Redis/RESP auth uses `REDGE_PASSWORD`.
