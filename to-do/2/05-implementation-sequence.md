# Implementation Sequence

## Milestone 1: Roadmap and Config

- Add this `to-do/2` roadmap set.
- Add Document API config flags.
- Document deployment expectations for HTTPS/WSS.

## Milestone 2: Document Store

- Add durable document tables and index tables.
- Implement SQL-backed document CRUD, explicit index configuration, query, and portable search.
- Add D1 support using explicit SQL and base64-free JSON text values.

## Milestone 3: HTTP API

- Add `internal/docapi`.
- Implement auth, body limits, CRUD, shallow patch, query, and index configuration routes.
- Wire startup and graceful shutdown in `cmd/redge`.

## Milestone 4: WebSocket

- Add `/v1/ws`.
- Implement request/response dispatch.
- Add local subscriptions for insert, update, and delete events.
- Add bounded queues and disconnect cleanup.

## Milestone 5: Tests and Docs

- Add focused unit tests for `docapi`.
- Add store tests for SQL document operations.
- Add WebSocket tests.
- Update user docs and compatibility docs.

## Acceptance Criteria

- Existing RESP tests still pass.
- Document API is off by default.
- Production startup refuses an unauthenticated public Document API.
- HTTP and WebSocket document operations work against the same durable document tables.

