# RESP Primitives Roadmap

## Purpose

These commands improve Redis compatibility and reduce round trips. They are useful independently from the Document API and should remain part of the RESP/store layer.

## First Batch

- `MGET`: batch string reads while preserving requested key order.
- `MSET`: batch string writes.
- `GETDEL`: atomic get and delete.
- `GETSET`: atomic get and set.
- `SETNX`: command alias for `SET key value NX`.
- `PERSIST`: remove TTL from an existing key.
- `ZRANGEBYSCORE`: sorted set score range reads with optional `LIMIT`.
- `ZREVRANGEBYSCORE`: descending score range reads with optional `LIMIT`.

## Hash Compatibility Track

Hashes are valuable but are not a blocker for Document API v1. Implement them as Redis compatibility:

- `HSET`
- `HGET`
- `HMGET`
- `HGETALL`
- `HDEL`
- `HEXISTS`
- `HLEN`

Use a parent key in `redge_keys` with type `hash` and fields in `redge_hash_fields`.

## Acceptance Criteria

- RESP behavior matches Redis for supported syntax.
- SQL and D1 implementations preserve binary values.
- Existing string and zset behavior remains unchanged.
- New commands are documented in `docs/redis-compatibility.md`.

