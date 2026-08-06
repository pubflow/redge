# Store HTTP API

The Store API is Redge's public app-facing HTTP surface for key-value and sorted-set primitives. It runs on the public Redge HTTP app listener and is separate from the Admin API and RESP TCP. Use it from browsers, serverless functions, React Native, edge runtimes, Python, Go, or any environment where HTTPS is easier than raw Redis TCP.

The Store API is disabled by default.

```env
REDGE_STOREAPI_ENABLED=true
REDGE_API_TOKEN=change-me
```

## Authentication

Requests use:

```http
Authorization: Bearer <token>
```

The Store API only uses `REDGE_API_TOKEN`. `REDGE_ADMIN_TOKEN` is intentionally separate, and `REDGE_PASSWORD` is only for Redis/RESP clients.

## Security Controls

| Variable | Default | Description |
| --- | --- | --- |
| `REDGE_API_MAX_BODY_BYTES` | `1048576` | Maximum JSON request body size. |
| `REDGE_API_CORS_ENABLED` | `false` | Enables explicit browser CORS responses. |
| `REDGE_API_CORS_ORIGINS` | empty | Comma-separated allowed origins. Use exact app origins in production. |
| `REDGE_API_RATE_LIMIT_ENABLED` | `false` | Enables in-process per-token/per-IP token bucket limiting. |
| `REDGE_API_RATE_LIMIT_RPS` | `30` | Refill rate. |
| `REDGE_API_RATE_LIMIT_BURST` | `60` | Burst size. |
| `REDGE_API_ALLOWED_IPS` | empty | Comma, semicolon, or newline separated IP/CIDR allowlist. |
| `REDGE_API_IP_CHECK_ENABLED` | `false` | Enables the allowlist. |

Prefer platform or reverse-proxy TLS for public HTTPS. Native Redis TLS remains separate and only applies to RESP TCP.

## KV Routes

| Method | Route | Description |
| --- | --- | --- |
| `GET` | `/v1/kv/{key}` | Read a string key. |
| `PUT` | `/v1/kv/{key}` | Set a string/binary value. |
| `DELETE` | `/v1/kv/{key}` | Delete a key. |
| `POST` | `/v1/kv/batch/get` | Read multiple keys. |
| `POST` | `/v1/kv/batch/set` | Set multiple keys. |
| `POST` | `/v1/kv/{key}/getdel` | Atomically get and delete. |
| `POST` | `/v1/kv/{key}/incr` | Increment an integer value. |
| `POST` | `/v1/kv/{key}/expire` | Set a seconds TTL. |
| `POST` | `/v1/kv/{key}/persist` | Remove the TTL. |
| `GET` | `/v1/kv/{key}/ttl` | Read TTL state. |
| `GET` | `/v1/kv?match=prefix:*&cursor=0&limit=100` | Scan keys. |

Writes accept either text or base64:

```json
{"value":"hello","ttl_seconds":60,"nx":true}
```

```json
{"value_base64":"AP8="}
```

`PUT /v1/kv/{key}` supports `ttl_seconds`, `nx`, `xx`, `keep_ttl`, and `get`.

`POST /v1/kv/{key}/incr` accepts:

```json
{"by": 2}
```

If `by` is omitted or zero, Redge increments by `1`. The compatibility alias `delta` is also accepted.

String reads return:

```json
{
  "db": 0,
  "key": "session:1",
  "type": "string",
  "value": "hello",
  "value_base64": "aGVsbG8=",
  "value_size": 5,
  "ttl_state": "volatile",
  "ttl_seconds": 60,
  "version": 1
}
```

Missing keys return `404`. Wrong-type reads return `409` with `code: "wrong_type"`.

## Sorted Set Routes

| Method | Route | Description |
| --- | --- | --- |
| `POST` | `/v1/zsets/{key}/members` | Add or update one member score. |
| `GET` | `/v1/zsets/{key}/members?start=0&stop=99` | Range by rank. |
| `GET` | `/v1/zsets/{key}/byscore?min=0&max=100&limit=100&offset=0&rev=false` | Range by score. |
| `DELETE` | `/v1/zsets/{key}/members/{member}` | Remove one text member. |
| `DELETE` | `/v1/zsets/{key}/byscore?min=...&max=...` | Remove a score range. |
| `GET` | `/v1/zsets/{key}/score/{member}` | Read a member score. |
| `GET` | `/v1/zsets/{key}/count?min=...&max=...` | Count a score range. |
| `GET` | `/v1/zsets/{key}/card` | Cardinality. |

Add/update:

```json
{"member":"alice","score":42}
```

or binary-safe:

```json
{"member_base64":"AP8=","score":42}
```

Score bounds support finite numbers, `-inf`, `+inf`, `inf`, and exclusive bounds such as `(10`.

## Scope

Store API v1 covers strings, TTLs, counters, scans, and sorted sets. Lists, sets, hashes, streams, Lua, Pub/Sub, Redis Cluster, and Redis-over-HTTP are out of scope.
