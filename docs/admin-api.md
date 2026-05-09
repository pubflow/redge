# Admin API

The admin API exposes health, readiness, service information, cache metrics, and manual expired-key cleanup.

By default it listens on `0.0.0.0:9090`.

## Security

Admin security is controlled with:

- `REDGE_ADMIN_TOKEN`
- `REDGE_ADMIN_ALLOWED_IPS`
- `REDGE_ADMIN_IP_CHECK_ENABLED`
- `REDGE_ADMIN_READONLY`

When `REDGE_ADMIN_TOKEN` is set, protected endpoints require:

```http
Authorization: Bearer <token>
```

In `production`, Redge refuses to start with admin enabled unless `REDGE_ADMIN_TOKEN` is set.

If IP checking is enabled, Redge checks the client IP using:

1. `CF-Connecting-IP`
2. The first IP in `X-Forwarded-For`
3. The socket remote address

`REDGE_ADMIN_ALLOWED_IPS` supports exact IPs and CIDR ranges.

## Public Endpoints

### `GET /health`

Basic process health.

Response:

```json
{
  "status": "ok",
  "service": "redge"
}
```

### `GET /ready`

Checks whether the configured store can respond to `Ping`.

Ready response:

```json
{
  "status": "ready"
}
```

Not-ready response:

```json
{
  "status": "not_ready",
  "error": "database error"
}
```

## Protected Endpoints

### `GET /admin/v1/info`

Returns service and store statistics.

Example:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" http://127.0.0.1:9090/admin/v1/info
```

Response:

```json
{
  "service": "redge",
  "version": "0.1.0",
  "database": "sqlite",
  "keys": 42,
  "admin_readonly": false
}
```

Fields:

| Field | Description |
| --- | --- |
| `service` | Service name. |
| `version` | Redge version string. |
| `database` | Active backend type: `sqlite`, `libsql`, `postgres`, `mysql`, or `d1`. |
| `keys` | Count of non-expired logical Redis keys. |
| `admin_readonly` | Whether write-capable admin routes are disabled. |

### `GET /admin/v1/cache`

Returns Ristretto L1 cache configuration and metrics.

Example:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" http://127.0.0.1:9090/admin/v1/cache
```

Response shape:

```json
{
  "enabled": true,
  "engine": "ristretto",
  "max_keys": 100000,
  "max_bytes": 134217728,
  "set_ok": 120,
  "set_dropped": 0,
  "hits": 800,
  "misses": 200,
  "hit_ratio": 0.8,
  "keys_added": 120,
  "keys_updated": 30,
  "keys_evicted": 10,
  "cost_added": 10000,
  "cost_evicted": 1000,
  "sets_dropped_internal": 0,
  "sets_rejected": 0,
  "gets_dropped": 0,
  "gets_kept": 1000
}
```

Some Ristretto metric fields appear only when the cache engine is initialized successfully.

### `GET /admin/v1/keys`

Lists keys with cursor pagination. This endpoint is designed for CLIs and GUIs; it does not dump the full keyspace in one response.

Query parameters:

| Parameter | Default | Description |
| --- | --- | --- |
| `db` | `0` | Logical Redis database. |
| `match` | `*` | Redis-style match pattern. |
| `cursor` | `0` | Cursor returned by the previous response. |
| `count` | `100` | Page size. Maximum `1000`. |

Example:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" "http://127.0.0.1:9090/admin/v1/keys?match=session:*&cursor=0&count=100"
```

Response:

```json
{
  "db": 0,
  "match": "session:*",
  "count": 100,
  "cursor": "0",
  "next_cursor": "session:abc",
  "keys": ["session:123", "session:abc"]
}
```

Keep calling the endpoint with `cursor=<next_cursor>` until `next_cursor` is `"0"`.

### `GET /admin/v1/keys/{key}`

Returns metadata for one key. URL-encode the key when it contains `/`, spaces, or reserved characters.

Example:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" "http://127.0.0.1:9090/admin/v1/keys/session%3A123"
```

String response:

```json
{
  "db": 0,
  "key": "session:123",
  "type": "string",
  "ttl_state": "volatile",
  "ttl_seconds": 299,
  "value": "{\"userId\":\"u_123\"}",
  "value_base64": "eyJ1c2VySWQiOiJ1XzEyMyJ9",
  "value_size": 18,
  "version": 3
}
```

Sorted set response:

```json
{
  "db": 0,
  "key": "rate:user:1",
  "type": "zset",
  "ttl_state": "volatile",
  "ttl_seconds": 60,
  "zcard": 8
}
```

`ttl_state` can be:

| State | Meaning |
| --- | --- |
| `volatile` | The key has a TTL. |
| `persistent` | The key exists without TTL. |
| `missing` | The key does not exist. |

### `DELETE /admin/v1/keys/{key}`

Deletes one key from the selected database. This endpoint is blocked when `REDGE_ADMIN_READONLY=true`.

Example:

```powershell
curl.exe -X DELETE -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" "http://127.0.0.1:9090/admin/v1/keys/session%3A123"
```

Response:

```json
{
  "db": 0,
  "key": "session:123",
  "deleted": 1
}
```

### `GET /admin/v1/zsets/{key}`

Returns a paginated range of sorted set members.

Query parameters:

| Parameter | Default | Description |
| --- | --- | --- |
| `db` | `0` | Logical Redis database. |
| `start` | `0` | Inclusive start index. Supports negative indexes. |
| `stop` | `99` | Inclusive stop index. Supports negative indexes. Maximum response window is `1000` members. |

Example:

```powershell
curl.exe -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" "http://127.0.0.1:9090/admin/v1/zsets/rate%3Auser%3A1?start=0&stop=99"
```

Response:

```json
{
  "db": 0,
  "key": "rate:user:1",
  "start": 0,
  "stop": 99,
  "members": [
    {
      "member": "request-1",
      "member_base64": "cmVxdWVzdC0x",
      "score": 1778277327714
    }
  ]
}
```

### `POST /admin/v1/cleanup`

Deletes expired keys manually. This endpoint is blocked when `REDGE_ADMIN_READONLY=true`.

Example:

```powershell
curl.exe -X POST -H "Authorization: Bearer $env:REDGE_ADMIN_TOKEN" http://127.0.0.1:9090/admin/v1/cleanup
```

Response:

```json
{
  "deleted": 128
}
```

The current endpoint cleanup limit is `1000` keys per request.

## Recommended Deployment

For production, bind admin to localhost or an internal network:

```env
REDGE_ADMIN_ADDR=127.0.0.1:9090
REDGE_ADMIN_TOKEN=strong-random-token
REDGE_ADMIN_IP_CHECK_ENABLED=true
REDGE_ADMIN_ALLOWED_IPS=127.0.0.1,10.0.0.0/8
```
