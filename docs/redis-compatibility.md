# Redis Compatibility

Redge speaks RESP2 and is intended to work with standard Redis and Valkey clients for the supported command subset.

Redis TCP can run as plaintext `redis://` or native TLS `rediss://` when `REDGE_TLS_ENABLED=true`. TLS does not change the command set.

## Supported Commands

### Connection and Server

| Command | Notes |
| --- | --- |
| `AUTH` | Password auth when `REDGE_PASSWORD` is set. Supports `AUTH password` and `AUTH default password`. |
| `PING` | Supports optional message. |
| `ECHO` | Returns the provided bulk string. |
| `QUIT` | Returns `OK`. |
| `SELECT` | Selects a logical database number. |
| `CLIENT SETNAME` | Stores the connection-local client name. |
| `CLIENT GETNAME` | Returns the connection-local client name. |
| `INFO` | Returns minimal `server` and `keyspace` sections. |
| `COMMAND` | Returns an empty array for broad client compatibility. |
| `DBSIZE` | Returns live key count for the selected logical database. |

### Strings, TTLs, and Counters

| Command | Notes |
| --- | --- |
| `GET` | Uses the L1 cache for string keys. |
| `MGET` | Batch string reads while preserving requested key order. |
| `SET` | Supports `EX`, `PX`, `NX`, `XX`, `GET`, and `KEEPTTL`. |
| `MSET` | Batch string writes. |
| `SETNX` | Equivalent to `SET key value NX`, returns `1` when written and `0` when skipped. |
| `GETSET` | Returns the previous string value and stores the new value. |
| `GETDEL` | Returns the previous string value and deletes the key. |
| `SETEX` | Equivalent to setting a string with seconds TTL. |
| `DEL` | Deletes string and sorted-set keys. |
| `EXISTS` | Counts existing non-expired keys. |
| `TYPE` | Returns `none`, `string`, or `zset`. |
| `STRLEN` | Returns string byte length, `0` for missing keys, or `WRONGTYPE` for non-strings. |
| `MEMORY USAGE` | Returns an approximate byte size for GUI compatibility. |
| `EXPIRE` | Sets a seconds TTL. |
| `PERSIST` | Removes an existing TTL. |
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
| `ZRANGEBYSCORE` | Supports score bounds, optional `WITHSCORES`, and optional `LIMIT offset count`. |
| `ZREVRANGEBYSCORE` | Descending score range variant with optional `WITHSCORES` and `LIMIT`. |
| `ZSCORE` | Returns the member score or null. |
| `ZCOUNT` | Supports finite scores, infinities, and exclusive bounds. |
| `ZINCRBY` | Increments a member score by a float delta; creates the member when missing. |
| `ZPOPMIN` | Pops one or more lowest-score members (`count` optional, default 1). |
| `ZPOPMAX` | Pops one or more highest-score members (`count` optional, default 1). |

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
- Another Redis Desktop Manager for basic string key browsing/editing.

## Important Differences From Redis

Redge is durable-database backed. That is the point, but it means some Redis internals do not map perfectly.

| Area | Difference |
| --- | --- |
| `SCAN` | Uses lexicographic keyset pagination. It is stable and cheap with indexes, but not identical to Redis cursor internals. |
| `MEMORY USAGE` | Returns an approximation because Redge is database-backed, not Redis memory-backed. |
| `MULTI/EXEC` | Provides simple queued execution, not full Redis transaction semantics. |
| D1 backend | D1 HTTP calls are remote and usage-billed by rows read/written. High-write workloads should be measured. |
| Cache | The L1 cache is process-local and short lived. It is not the durable source of truth. |

## HTTP APIs Are Separate

The JSON Document API is not part of Redis compatibility. It uses HTTP and optional WebSocket endpoints backed by separate document tables.

The public Store API is also separate from RESP compatibility. It exposes app-friendly HTTP routes for string, TTL, counter, scan, and sorted-set use cases, but it is not Redis-over-HTTP. RESP clients continue to use the Redis-compatible TCP protocol.

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
