# Redge Documentation

Redge is a Redis/Valkey protocol compatible server backed by SQL databases and an edge-friendly in-process cache.

It is designed for applications that want Redis-like ergonomics while storing data in cheaper, durable databases such as SQLite, libSQL/Turso, PostgreSQL, MySQL, or Cloudflare D1.

## Documents

- [Configuration](./configuration.md): environment variables, database URLs, cache settings, admin settings, and production notes.
- [Redis Compatibility](./redis-compatibility.md): supported commands, client behavior, and current limitations.
- [Client Examples](./client-examples.md): examples for `redis-cli`, `ioredis`, and `go-redis`.
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

Redge currently supports strings, TTLs, counters, basic sorted sets, `SCAN`, RESP pipelining, and simple `MULTI`/`EXEC`.

It does not yet support Redis Cluster, Lua scripting, Pub/Sub, Streams, blocking list commands, geospatial commands, or probabilistic data structures.
