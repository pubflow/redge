# Redge Document API and Realtime: Critical Feasibility

## Verdict

Redge should keep the existing RESP TCP server as the Redis/Valkey compatibility surface and add a separate HTTP/WebSocket Document API for application clients. This gives web and serverless apps a simple JSON API without forcing Redge to emulate PostgreSQL, MongoDB, or a second Redis wire protocol.

## Rejected Alternatives

- PostgreSQL wire protocol: too large for Redge's current scope and misleading because Redge is not a relational SQL server.
- Custom raw TCP protocol: harder to deploy publicly, harder to secure with common HTTPS proxies, and worse for browser/serverless clients.
- Document API through Redis hashes only: useful for Redis compatibility, but it couples document semantics to Redis data structures and does not solve indexing/search cleanly.
- Automatic indexing of every JSON field: convenient at first, but causes high write amplification, unbounded cardinality, and surprising storage costs.

## Protocol Strategy

- RESP TCP remains stable and isolated.
- HTTP handles CRUD, query, health checks, and ordinary tooling.
- WebSocket is an optional low-latency transport and realtime subscription channel over the HTTP surface.
- The TypeScript SDK can expose one API and choose WebSocket or HTTP internally.

## Deployment and TLS

HTTPS/WSS should be terminated by the hosting platform or reverse proxy by default. Native Redis TLS remains separate for `rediss://` clients because the existing Redis listener is a raw TCP protocol, not HTTP.

Native HTTP TLS can be added later if Redge needs to run without a proxy.

