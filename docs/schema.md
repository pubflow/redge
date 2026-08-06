# Database Schema

Redge stores Redis-like keys in durable database tables. The cache is not the source of truth.

There are two logical tables:

- `redge_keys`: one row per Redis key.
- `redge_zset_members`: one row per sorted set member.

When the Document API is enabled and migrations are enabled, Redge also creates:

- `redge_docs`: one row per JSON document.
- `redge_doc_index_config`: explicit per-collection field/search index configuration.
- `redge_doc_indexes`: equality index entries for configured top-level scalar fields.

## SQL Backends

SQLite, libSQL/Turso, PostgreSQL, and MySQL use GORM migrations.

### `redge_keys`

| Column | Type | Purpose |
| --- | --- | --- |
| `db` | integer | Redis logical database number. Part of the primary key. |
| `key` | string | Redis key. Part of the primary key. |
| `type` | string | Logical Redge type. Currently `string` or `zset`. |
| `string_value` | bytes/blob | Raw string value for string keys. Empty for sorted set container keys. |
| `expires_at` | timestamp/null | Expiration time. Null means persistent key. |
| `version` | integer | Monotonic version used for invalidation and optimistic behavior. |
| `created_at` | timestamp | Creation time. |
| `updated_at` | timestamp | Last update time. |

Primary key:

```sql
(db, key)
```

Indexes:

- `type`
- `expires_at`

### `redge_zset_members`

| Column | Type | Purpose |
| --- | --- | --- |
| `db` | integer | Redis logical database number. |
| `key` | string | Parent sorted set key. |
| `member_hash` | string | SHA-256 hex digest of the member bytes. |
| `member` | bytes/blob | Raw member bytes. |
| `score` | float | Sorted set score. |
| `created_at` | timestamp | Member creation time. |
| `updated_at` | timestamp | Last member update time. |

Primary key:

```sql
(db, key, member_hash)
```

Indexes:

- `(key, score)` through GORM's `idx_redge_zset_score`

The primary key prevents duplicate members. The score index makes `ZRANGE`, `ZCOUNT`, and `ZREMRANGEBYSCORE` efficient for a single sorted set.

## Cloudflare D1 Schema

D1 uses explicit SQL through `github.com/pubflow/d1http`.

### `redge_keys`

```sql
CREATE TABLE IF NOT EXISTS redge_keys (
	db INTEGER NOT NULL DEFAULT 0,
	key TEXT NOT NULL,
	type TEXT NOT NULL,
	string_value TEXT,
	expires_at INTEGER,
	version INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, key)
);
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_redge_keys_expires_at
ON redge_keys (expires_at);
```

D1 stores times as Unix milliseconds.

String values are base64 encoded in `string_value` so binary Redis values can be represented safely in text columns.

### `redge_zset_members`

```sql
CREATE TABLE IF NOT EXISTS redge_zset_members (
	db INTEGER NOT NULL,
	key TEXT NOT NULL,
	member_hash TEXT NOT NULL,
	member_value TEXT NOT NULL,
	score REAL NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	PRIMARY KEY (db, key, member_hash)
);
```

Indexes:

```sql
CREATE INDEX IF NOT EXISTS idx_redge_zset_score
ON redge_zset_members (db, key, score);
```

D1 stores sorted set members as base64 in `member_value`.

## Key Types

Current values for `redge_keys.type`:

| Type | Meaning |
| --- | --- |
| `string` | A Redis string value stored directly on `redge_keys`. |
| `zset` | A sorted set container. Members live in `redge_zset_members`. |

Wrong-type operations return Redis-style `WRONGTYPE` errors.

## Document API Schema

Document rows are separate from Redis keys.

### `redge_docs`

| Column | Purpose |
| --- | --- |
| `db` | Logical database number, currently `0` for the Document API. |
| `collection` | Collection name. |
| `id` | Document id. |
| `value` | Canonical JSON document object. |
| `search_text` | Denormalized text from configured search fields. |
| `expires_at` | Optional expiration timestamp. |
| `version` | Monotonic document version. |
| `created_at` / `updated_at` | Document timestamps. |

Primary key:

```sql
(db, collection, id)
```

### `redge_doc_index_config`

Stores explicit collection indexes. Rows with `kind = 'field'` are equality indexes. Rows with `kind = 'search'` contribute to `search_text`.

### `redge_doc_indexes`

Stores one row per indexed scalar field value per document.

Primary key:

```sql
(db, collection, field, value_hash, id)
```

This keeps Document API querying explicit. Redge does not automatically index every JSON field.

## Expiration Model

Redge stores TTL metadata on `redge_keys.expires_at`.

Expiration is enforced in two ways:

- Lazy deletion: reads check `expires_at` and delete expired keys when found.
- Background cleanup: a periodic task removes expired keys in batches.

Sorted set members do not have independent TTLs. The parent key owns expiration.

## Deletion Model

Deleting a key removes:

- The row from `redge_keys`.
- Any related rows from `redge_zset_members`.

This keeps sorted set members from becoming orphaned.

## D1 Batch Note

D1 migrations use explicit single-statement HTTP calls where required by the D1 HTTP API.

Hot-path commands use single parameterized HTTP calls. Cloudflare D1's HTTP `/raw` endpoint rejects multiple statements with params in one request, so Redge avoids unsafe multi-statement parameter batching for writes like `ZADD` and `DEL`.
