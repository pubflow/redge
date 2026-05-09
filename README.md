# Redge

Redge is a Redis/Valkey protocol compatible edge KV server backed by SQL databases.

This first implementation includes:

- RESP2 TCP listener.
- Redis client compatibility for common string, TTL, sorted set, scan, pipeline, and simple transaction flows.
- Multi-database support through `DATABASE_URL`: SQLite, libSQL/Turso, PostgreSQL, MySQL, and Cloudflare D1.
- L1 in-memory cache powered by Ristretto/TinyLFU with TTL, max keys, max bytes, and admin metrics.
- Admin HTTP server with health, cache metrics, cleanup, paginated key inspection, single-key deletion, and sorted-set inspection.

## Run Locally

```powershell
go mod tidy
go run ./cmd/redge
```

Then test:

```powershell
redis-cli -p 6379 ping
redis-cli -p 6379 set hello world ex 60
redis-cli -p 6379 get hello
```

## Database URLs

```env
DATABASE_URL=sqlite://redge.db
DATABASE_URL=libsql://example.turso.io
TURSO_AUTH_TOKEN=...
DATABASE_URL=postgres://user:pass@localhost:5432/redge?sslmode=disable
DATABASE_URL=mysql://user:pass@tcp(localhost:3306)/redge?parseTime=true
DATABASE_URL=d1://cloudflare_account_id/d1_database_id
D1_API_TOKEN=...
```

D1 uses `github.com/pubflow/d1http` directly instead of GORM or `database/sql`, so Redge can preserve D1 metadata and avoid fake SQL transaction semantics on the hot path.

## Redis Compatibility

Supported connection and server commands:

- `AUTH`, `PING`, `ECHO`, `QUIT`, `SELECT`, `CLIENT SETNAME`, `CLIENT GETNAME`, `INFO`, `COMMAND`.

Supported string and TTL commands:

- `GET`, `SET`, `SETEX`, `DEL`, `EXISTS`, `EXPIRE`, `TTL`, `PTTL`, `INCR`, `DECR`, `INCRBY`, `DECRBY`.

Supported sorted set commands:

- `ZADD`, `ZCARD`, `ZREM`, `ZREMRANGEBYSCORE`, `ZRANGE`, `ZSCORE`, `ZCOUNT`.

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

- D1 schema migrations use `d1http.BatchRaw` where it is safe.
- Parameterized hot-path commands use single prepared HTTP calls because the D1 HTTP API rejects multiple statements with params in one `/raw` request.
- ioredis pipeline correctness is supported at the Redis protocol layer. True D1 write coalescing for arbitrary client pipelines needs a dedicated server-side coalescer because Redis pipelining has no explicit begin/end marker on the wire.

Not supported yet:

- Redis Cluster, Lua scripting, Pub/Sub, Streams, hashes, sets, lists, blocking list commands, `KEYS`, `WATCH`/`UNWATCH`, geospatial commands, and probabilistic data structures.
