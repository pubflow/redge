# Redge Documentation

Redge is a Redis/Valkey protocol compatible server backed by SQL databases and an edge-friendly in-process cache.

It is designed for applications that want Redis-like ergonomics while storing data in cheaper, durable databases such as SQLite, libSQL/Turso, PostgreSQL, MySQL, or Cloudflare D1.

## Documents

- [Configuration](./configuration.md): environment variables, database URLs, cache settings, admin settings, and production notes.
- [Redis Compatibility](./redis-compatibility.md): supported commands, client behavior, and current limitations.
- [Document API](./document-api.md): JSON document CRUD, explicit indexes, query, and WebSocket realtime.
- [Store API](./store-api.md): public HTTP key-value and sorted-set primitives for app clients.
- [Production Readiness](./production-readiness.md): release gates, runtime smoke tests, and deployment checklist.
- [Client Examples](./client-examples.md): `@pubflow/redge` TypeScript SDK, plus `redis-cli`, `ioredis`, and `go-redis`.
- [Admin API](./admin-api.md): health checks, readiness, cache metrics, cleanup, auth, and IP controls.
- [Database Schema](./schema.md): tables, indexes, data encoding, TTL behavior, and backend differences.
- [Cache](./cache.md): Ristretto/TinyLFU L1 cache behavior, metrics, sizing, and invalidation.
- [Operations](./operations.md): running Redge locally and in production, cleanup, D1 notes, and troubleshooting.

## Quick Start

```powershell
go mod tidy
go run ./cmd/redge
```

By default Redge listens for Redis/Valkey TCP on `0.0.0.0:6379`, exposes public status HTTP on `0.0.0.0:8080`, uses `sqlite://redge.db`, runs migrations automatically, and exposes the admin API on `0.0.0.0:9090`.

```powershell
curl.exe http://127.0.0.1:8080/health
redis-cli -p 6379 ping
redis-cli -p 6379 set hello world ex 60
redis-cli -p 6379 get hello
```

## Current Status

Redge currently supports strings, TTLs, counters, basic sorted sets, `SCAN`, RESP pipelining, simple `MULTI`/`EXEC`, an optional JSON Document API, and an optional public HTTP Store API.

It does not yet support Redis Cluster, Lua scripting, Pub/Sub, Streams, blocking list commands, geospatial commands, or probabilistic data structures.

## Document API Quick Start

Enable the Document API with:

```env
REDGE_DOCAPI_ENABLED=true
REDGE_API_TOKEN=change-me
```

Configure explicit indexes before querying by fields:

```sh
curl -X PUT http://127.0.0.1:8080/v1/collections/products/indexes \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"fields":["category"],"searchFields":["name"]}'
```

Insert and query:

```sh
curl -X POST http://127.0.0.1:8080/v1/collections/products \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"id":"p1","name":"Blue Shirt","category":"apparel"}'

curl "http://127.0.0.1:8080/v1/collections/products?where=category:eq:apparel&search=Blue" \
  -H "Authorization: Bearer change-me"
```

## Store API Quick Start

Enable the Store API with:

```env
REDGE_STOREAPI_ENABLED=true
REDGE_API_TOKEN=change-me
```

Set and read a key over HTTPS-friendly HTTP:

```sh
curl -X PUT http://127.0.0.1:8080/v1/kv/session:1 \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"value":"hello","ttl_seconds":60}'

curl http://127.0.0.1:8080/v1/kv/session:1 \
  -H "Authorization: Bearer change-me"
```
