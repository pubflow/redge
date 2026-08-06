# Document API Roadmap

## Goal

Add a JSON Document API for web and serverless applications while keeping RESP compatibility separate.

## HTTP API

- `POST /v1/collections/{collection}` inserts a document.
- `GET /v1/collections/{collection}/{id}` reads one document.
- `PUT /v1/collections/{collection}/{id}` replaces or creates a document.
- `PATCH /v1/collections/{collection}/{id}` shallow-merges JSON object fields.
- `DELETE /v1/collections/{collection}/{id}` deletes one document.
- `GET /v1/collections/{collection}` lists or queries documents.
- `GET /v1/collections/{collection}/indexes` returns configured indexes.
- `PUT /v1/collections/{collection}/indexes` configures explicit field and search indexes.

## Document Model

Documents are canonical JSON objects stored separately from Redis keys. Each row records:

- logical database number
- collection
- id
- JSON value
- version
- created and updated timestamps
- optional expiration timestamp
- denormalized search text

## Query Model

v1 supports conservative queries:

- keyset pagination by document id
- one equality filter through an explicit field index
- optional text search over configured search fields
- bounded `limit`

No joins, aggregation pipeline, arbitrary nested planner, or automatic indexing are included in v1.

## SDK Expectations

The TypeScript SDK should expose collection methods such as `insert`, `get`, `upsert`, `patch`, `delete`, `find`, `where`, `search`, and `subscribe`. Transport selection can be `http`, `ws`, or `auto`.

