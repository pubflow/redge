# Document API

The Document API is an optional HTTP and WebSocket surface for JSON documents. It runs on the public Redge HTTP app listener and is separate from the Redis/Valkey RESP keyspace.

Enable it with:

```env
REDGE_DOCAPI_ENABLED=true
REDGE_API_TOKEN=change-me
REDGE_WS_ENABLED=true
REDGE_API_RATE_LIMIT_ENABLED=true
REDGE_API_CORS_ENABLED=true
REDGE_API_CORS_ORIGINS=https://app.example.com
```

In production, Redge requires `REDGE_API_TOKEN` when the Document API is enabled. `REDGE_PASSWORD` is only for Redis/RESP clients.

## Authentication

Use bearer auth:

```http
Authorization: Bearer change-me
```

For WebSocket clients that cannot send headers, `?token=change-me` is also accepted.

## Indexes

Field queries require explicit indexes. Redge does not automatically index every JSON field.

```sh
curl -X PUT http://127.0.0.1:8080/v1/collections/products/indexes \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"fields":["category"],"searchFields":["name","description"]}'
```

Read index configuration:

```sh
curl http://127.0.0.1:8080/v1/collections/products/indexes \
  -H "Authorization: Bearer change-me"
```

`fields` are top-level scalar fields used for equality filters. `searchFields` are top-level string fields copied into `search_text`.

## HTTP CRUD

Insert a document:

```sh
curl -X POST http://127.0.0.1:8080/v1/collections/products \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"id":"p1","name":"Blue Shirt","category":"apparel","price":29}'
```

If `id` is omitted, Redge generates one.

Read a document:

```sh
curl http://127.0.0.1:8080/v1/collections/products/p1 \
  -H "Authorization: Bearer change-me"
```

Replace or upsert a document:

```sh
curl -X PUT http://127.0.0.1:8080/v1/collections/products/p1 \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"name":"Blue Shirt","category":"apparel","price":31}'
```

Patch a document with a shallow merge:

```sh
curl -X PATCH http://127.0.0.1:8080/v1/collections/products/p1 \
  -H "Authorization: Bearer change-me" \
  -H "Content-Type: application/json" \
  -d '{"price":35}'
```

Delete a document:

```sh
curl -X DELETE http://127.0.0.1:8080/v1/collections/products/p1 \
  -H "Authorization: Bearer change-me"
```

## Query

List documents:

```sh
curl "http://127.0.0.1:8080/v1/collections/products?limit=20" \
  -H "Authorization: Bearer change-me"
```

Filter by an indexed field:

```sh
curl "http://127.0.0.1:8080/v1/collections/products?where=category:eq:apparel" \
  -H "Authorization: Bearer change-me"
```

Search configured search text:

```sh
curl "http://127.0.0.1:8080/v1/collections/products?search=shirt" \
  -H "Authorization: Bearer change-me"
```

Combine filter and search:

```sh
curl "http://127.0.0.1:8080/v1/collections/products?where=category:eq:apparel&search=shirt" \
  -H "Authorization: Bearer change-me"
```

Responses include `next_cursor` and `has_more` for keyset pagination.

## WebSocket

Connect:

```text
ws://127.0.0.1:8080/v1/ws?token=change-me
```

Request/response messages include a request `id`:

```json
{ "id": "r1", "op": "get", "collection": "products", "docId": "p1" }
```

```json
{ "id": "r1", "ok": true, "result": { "collection": "products", "id": "p1", "doc": { "name": "Blue Shirt" } } }
```

Supported operations:

- `insert`
- `get`
- `upsert`
- `patch`
- `delete`
- `find`
- `sub` or `subscribe`
- `unsub` or `unsubscribe`

Document operations accept `docId` or `key` for the document id. Upsert and insert messages send the JSON object in `doc`:

```json
{ "id": "u1", "op": "upsert", "collection": "products", "docId": "p1", "doc": { "name": "Blue Shirt" } }
```

Subscribe to local realtime events:

```json
{ "id": "s1", "op": "subscribe", "collection": "orders" }
```

Events are best-effort and local to one Redge instance:

```json
{ "subId": "doc_...", "event": "insert", "collection": "orders", "id": "o1", "doc": { "status": "new" } }
```

Distributed fanout is intentionally not part of v1.

## Production Controls

Recommended production settings:

```env
REDGE_DOCAPI_ENABLED=true
REDGE_API_TOKEN=<strong-random-token>
REDGE_API_RATE_LIMIT_ENABLED=true
REDGE_API_RATE_LIMIT_RPS=30
REDGE_API_RATE_LIMIT_BURST=60
REDGE_WS_MAX_MESSAGE_BYTES=65536
REDGE_WS_IDLE_TIMEOUT=60s
REDGE_WS_MAX_SUBSCRIPTIONS=32
```

If the API is called directly from browsers, enable explicit CORS:

```env
REDGE_API_CORS_ENABLED=true
REDGE_API_CORS_ORIGINS=https://app.example.com
```

For private deployments, add an IP/CIDR allowlist:

```env
REDGE_API_IP_CHECK_ENABLED=true
REDGE_API_ALLOWED_IPS=203.0.113.10,10.0.0.0/8
```

`?token=` is supported for WebSocket clients that cannot set headers, but `Authorization: Bearer` is preferred for HTTP clients.

## Limits

- Documents must be JSON objects.
- `PATCH` is a shallow merge.
- `where` supports `field:eq:value` only in v1.
- `limit` is capped at `100`.
- Document API currently uses logical DB `0`.
- Search is a portable `search_text` query; native backend FTS can be added later without changing the public API.
