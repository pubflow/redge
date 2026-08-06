# WebSocket Realtime Roadmap

## Goal

Use WebSocket as an optional transport for low-latency document operations and realtime subscriptions.

## Endpoint

`GET /v1/ws` upgrades an authenticated HTTP request to WebSocket.

## Message Shape

Requests include an `id` so clients can multiplex operations:

```json
{ "id": "r1", "op": "get", "collection": "products", "key": "prod_1" }
```

Responses echo the request id:

```json
{ "id": "r1", "ok": true, "doc": { "name": "shirt" } }
```

Subscription events use `subId`:

```json
{ "subId": "sub_1", "event": "insert", "collection": "orders", "id": "ord_1" }
```

## v1 Realtime Semantics

- Local instance only.
- Best-effort event delivery.
- Bounded per-subscriber queues.
- Disconnect cleanup.
- HTTP remains the fallback transport.

## Later Distributed Modes

Distributed fanout should be explicit and opt-in. Possible modes include Postgres notify, NATS, or SQL event-table polling.

