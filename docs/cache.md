# Cache

Redge includes a process-local L1 cache powered by Ristretto.

The cache is designed to reduce database reads for hot string keys while keeping the database as the durable source of truth.

## What Is Cached

Currently Redge caches `GET` results for string keys.

When a string key is read:

1. Redge checks the L1 cache by `db:key`.
2. On hit, it returns the cached bytes.
3. On miss, it reads from the database.
4. If the key exists, it stores a copy in Ristretto with a short TTL.

## Invalidation

Redge deletes the L1 cache entry when commands mutate a key:

- `SET`
- `SETEX`
- `DEL`
- `EXPIRE`
- `INCR`
- `DECR`
- `INCRBY`
- `DECRBY`
- `ZADD`
- `ZREM`
- `ZREMRANGEBYSCORE`

Sorted set reads are not cached yet.

## TTL Behavior

The cache TTL is bounded by `REDGE_CACHE_DEFAULT_TTL`.

If the database key has a shorter TTL, Redge uses that shorter TTL. If the key is persistent or has a longer TTL, Redge uses `REDGE_CACHE_DEFAULT_TTL`.

This keeps cache entries fresh and reduces stale reads in multi-process deployments.

## Ristretto Settings

| Variable | Meaning |
| --- | --- |
| `REDGE_CACHE_ENABLED` | Turns the cache on or off. |
| `REDGE_CACHE_MAX_KEYS` | Sizes TinyLFU counters. Redge uses `MaxKeys * 10` counters. |
| `REDGE_CACHE_MAX_BYTES` | Ristretto `MaxCost`. Each item cost is roughly key bytes + value bytes + overhead. |
| `REDGE_CACHE_DEFAULT_TTL` | Maximum/default cache TTL for entries. |

## Metrics

Cache metrics are available from:

```http
GET /admin/v1/cache
```

Important fields:

| Field | Meaning |
| --- | --- |
| `enabled` | Whether the cache is enabled. |
| `engine` | Cache implementation. Currently `ristretto`. |
| `hits` | Successful cache reads. |
| `misses` | Cache misses. |
| `hit_ratio` | Hit ratio reported by Ristretto. |
| `keys_added` | Keys admitted into the cache. |
| `keys_updated` | Existing keys updated. |
| `keys_evicted` | Keys evicted by Ristretto. |
| `sets_rejected` | Sets rejected by policy or capacity. |
| `set_ok` | Redge-level successful set attempts. |
| `set_dropped` | Redge-level set attempts not accepted by Ristretto. |

## Sizing Guidance

For small apps, the default is fine:

```env
REDGE_CACHE_MAX_KEYS=100000
REDGE_CACHE_MAX_BYTES=134217728
REDGE_CACHE_DEFAULT_TTL=5s
```

For larger apps, size by hot key working set:

```env
REDGE_CACHE_MAX_KEYS=500000
REDGE_CACHE_MAX_BYTES=536870912
REDGE_CACHE_DEFAULT_TTL=3s
```

Short TTLs are usually better for Redge because the database is durable and multiple Redge instances may be running.

## Current Limitations

- No stale-while-revalidate yet.
- No distributed invalidation across Redge instances yet.
- No cache warming API yet.
- No hot-key detection API yet.
- Sorted set reads are not cached yet.

These are natural next steps for making Redge a more sophisticated edge cache.
