# Redis Compatibility

Redge speaks RESP2 and is intended to work with standard Redis and Valkey clients for the supported command subset.

## Supported Commands

### Connection and Server

| Command | Notes |
| --- | --- |
| `AUTH` | Password auth when `REDGE_PASSWORD` is set. |
| `PING` | Supports optional message. |
| `ECHO` | Returns the provided bulk string. |
| `QUIT` | Returns `OK`. |
| `SELECT` | Selects a logical database number. |
| `CLIENT SETNAME` | Stores the connection-local client name. |
| `CLIENT GETNAME` | Returns the connection-local client name. |
| `INFO` | Returns a minimal server section. |
| `COMMAND` | Returns an empty array for broad client compatibility. |

### Strings, TTLs, and Counters

| Command | Notes |
| --- | --- |
| `GET` | Uses the L1 cache for string keys. |
| `SET` | Supports `EX`, `PX`, `NX`, `XX`, `GET`, and `KEEPTTL`. |
| `SETEX` | Equivalent to setting a string with seconds TTL. |
| `DEL` | Deletes string and sorted-set keys. |
| `EXISTS` | Counts existing non-expired keys. |
| `EXPIRE` | Sets a seconds TTL. |
| `TTL` | Redis-style seconds TTL response. |
| `PTTL` | Redis-style milliseconds TTL response. |
| `INCR` | Integer increment by `1`. |
| `DECR` | Integer decrement by `1`. |
| `INCRBY` | Integer increment by a signed delta. |
| `DECRBY` | Integer decrement by a signed delta. |

### Sorted Sets

| Command | Notes |
| --- | --- |
| `ZADD` | Supports one or more `score member` pairs. Advanced options are not implemented yet. |
| `ZCARD` | Counts members. |
| `ZREM` | Removes one or more members. |
| `ZREMRANGEBYSCORE` | Supports finite scores, `-inf`, `+inf`, and exclusive bounds like `(10`. |
| `ZRANGE` | Supports `start`, `stop`, negative indexes, and optional `WITHSCORES`. |
| `ZSCORE` | Returns the member score or null. |
| `ZCOUNT` | Supports finite scores, infinities, and exclusive bounds. |

### Iteration

| Command | Notes |
| --- | --- |
| `SCAN` | Supports `MATCH` and `COUNT`. Cursor values are keyset cursors, not Redis hash-table cursors. |

### Pipelining and Transactions

Redge supports RESP pipelining because the TCP server reads sequential RESP commands and writes sequential RESP replies. This works with clients such as ioredis and go-redis.

Redge also supports simple `MULTI`, `EXEC`, and `DISCARD`:

- Commands inside `MULTI` are queued.
- `EXEC` runs the queued commands and returns an array of replies.
- Nested `MULTI` is rejected.
- Watch/optimistic transaction semantics are not implemented.

## Tested Clients

The current implementation has been smoke-tested with:

- `redis-cli`
- `ioredis@5`
- `github.com/redis/go-redis/v9`

## Important Differences From Redis

Redge is durable-database backed. That is the point, but it means some Redis internals do not map perfectly.

| Area | Difference |
| --- | --- |
| `SCAN` | Uses lexicographic keyset pagination. It is stable and cheap with indexes, but not identical to Redis cursor internals. |
| `MULTI/EXEC` | Provides simple queued execution, not full Redis transaction semantics. |
| D1 backend | D1 HTTP calls are remote and usage-billed by rows read/written. High-write workloads should be measured. |
| Cache | The L1 cache is process-local and short lived. It is not the durable source of truth. |

## Not Supported Yet

- Redis Cluster
- Lua scripting with `EVAL` or `EVALSHA`
- Pub/Sub
- Streams
- Lists, blocking pop/push, and queue primitives
- Sets and hashes
- Geospatial commands
- HyperLogLog and other probabilistic structures
- `WATCH`/`UNWATCH`
- Redis modules
