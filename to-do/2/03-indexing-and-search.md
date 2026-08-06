# Indexing and Search Roadmap

## Explicit Indexes

Document API v1 requires explicit indexes. A collection can configure:

- `fields`: top-level scalar fields used for equality filters.
- `searchFields`: top-level string fields included in full-text search.

Writes rebuild index rows for the changed document based on the collection configuration.

## Why Not Auto-Index Everything

Automatic indexing is rejected for v1 because it:

- multiplies every write into many index writes
- creates unbounded cardinality for fields such as email, UUID, and timestamps
- makes D1/Turso cost harder to predict
- gives users a false sense that arbitrary MongoDB-style queries are cheap

## Search

Search text is denormalized from configured string fields. Native SQL full-text should be used where practical:

- PostgreSQL: `tsvector`/GIN.
- SQLite/libSQL/D1: FTS5 where available.
- MySQL: `FULLTEXT` where practical.

The portable baseline can fall back to bounded `LIKE` search until native FTS paths are implemented per backend.

## Acceptance Criteria

- Index rows are rebuilt on insert, replace, patch, delete, and expiry cleanup.
- Querying an unindexed field returns a clear client error.
- Search never indexes fields that were not explicitly configured.

