# Production Readiness

This checklist defines the minimum bar before calling a Redge build production-ready.

## Required Checks

Run:

```powershell
go test ./...
go build ./cmd/redge
```

Then run a local runtime smoke with the Document API and Store API enabled:

```powershell
$env:REDGE_DOCAPI_ENABLED="true"
$env:REDGE_API_TOKEN="dev-token"
$env:REDGE_API_RATE_LIMIT_ENABLED="true"
$env:REDGE_API_CORS_ENABLED="true"
$env:REDGE_API_CORS_ORIGINS="http://localhost:3000"
$env:REDGE_STOREAPI_ENABLED="true"
$env:DATABASE_URL="sqlite://redge-smoke.db"
go run ./cmd/redge
```

In another shell:

```powershell
curl.exe http://127.0.0.1:8080/health
curl.exe -X PUT http://127.0.0.1:8080/v1/collections/products/indexes `
  -H "Authorization: Bearer dev-token" `
  -H "Content-Type: application/json" `
  -d "{\"fields\":[\"category\"],\"searchFields\":[\"name\"]}"
curl.exe -X POST http://127.0.0.1:8080/v1/collections/products `
  -H "Authorization: Bearer dev-token" `
  -H "Content-Type: application/json" `
  -d "{\"id\":\"p1\",\"name\":\"Blue Shirt\",\"category\":\"apparel\"}"
curl.exe "http://127.0.0.1:8080/v1/collections/products?where=category:eq:apparel&search=Blue" `
  -H "Authorization: Bearer dev-token"
curl.exe -X PUT http://127.0.0.1:8080/v1/kv/session:1 `
  -H "Authorization: Bearer dev-token" `
  -H "Content-Type: application/json" `
  -d "{\"value\":\"hello\",\"ttl_seconds\":60}"
curl.exe http://127.0.0.1:8080/v1/kv/session:1 `
  -H "Authorization: Bearer dev-token"
curl.exe -X POST http://127.0.0.1:8080/v1/zsets/rankings/members `
  -H "Authorization: Bearer dev-token" `
  -H "Content-Type: application/json" `
  -d "{\"member\":\"alice\",\"score\":42}"
curl.exe "http://127.0.0.1:8080/v1/zsets/rankings/byscore?min=0&max=100" `
  -H "Authorization: Bearer dev-token"
```

## Security Gate

- `REDGE_ENV=production` with `REDGE_DOCAPI_ENABLED=true` or `REDGE_STOREAPI_ENABLED=true` must have `REDGE_API_TOKEN`.
- Keep `REDGE_ADMIN_TOKEN` separate from app tokens.
- `REDGE_PASSWORD` is only for Redis/RESP auth and does not authorize HTTP APIs.
- Public Redis TCP still needs `REDGE_TLS_ENABLED=true` for `rediss://`.
- Public Document API should be behind HTTPS/WSS at the platform or reverse proxy layer.
- Enable `REDGE_API_RATE_LIMIT_ENABLED=true` for public deployments.
- Enable CORS only for known browser origins.
- Use `REDGE_API_IP_CHECK_ENABLED=true` for private app API deployments.

## Runtime Gate

- HTTP health, index configuration, insert, get, patch, query, and delete pass against a real running process.
- Store API `GET`, `PUT`, `DELETE`, batch get/set/exists/delete, `INCR`, TTL, `TYPE`, `EXISTS`, scan, zset rank/score, multi-member add/remove, `ZINCRBY`, and `ZPOPMIN`/`ZPOPMAX` routes pass against a real running process.
- WebSocket connect, subscribe, event delivery, unsubscribe, and close pass against a real running process.
- RESP smoke passes for classic and new commands: `PING`, `SET`, `GET`, `MSET`, `MGET`, `GETDEL`, `GETSET`, `PERSIST`, `ZADD`, `ZRANGEBYSCORE`, `ZREVRANGEBYSCORE`, `ZINCRBY`, `ZPOPMIN`, and `ZPOPMAX`.

## Backend Gate

- SQLite: required for local/default support.
- PostgreSQL: required before broad production recommendation.
- MySQL: recommended before claiming full SQL matrix confidence.
- D1: keep marked as edge/remote-sensitive until tested against a real Cloudflare D1 database because latency and billing differ from local SQL.

## Current Stable Surface

- RESP string, TTL, counter, scan, simple transaction, and sorted-set subset.
- Document API HTTP CRUD/query with explicit indexes.
- Local-instance WebSocket realtime subscriptions.
- Store API HTTP strings, TTL, counters, scans, key metadata, batch delete, and sorted sets (including incrby/pop).

## Current Non-Goals

- MongoDB-compatible query language.
- Distributed WebSocket fanout.
- Native full-text search ranking.
- Redis Cluster, Lua, Streams, lists, sets, hashes, and Pub/Sub.
