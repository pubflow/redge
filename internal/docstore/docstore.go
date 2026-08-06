package docstore

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound       = errors.New("document not found")
	ErrConflict       = errors.New("document already exists")
	ErrInvalidIndex   = errors.New("field is not indexed")
	ErrInvalidRequest = errors.New("invalid document request")
)

type Document struct {
	Collection string
	ID         string
	Value      []byte
	Version    int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ExpiresAt  *time.Time
}

type IndexConfig struct {
	Fields       []string `json:"fields"`
	SearchFields []string `json:"searchFields"`
}

type WriteOptions struct {
	IndexedFields []string
	SearchFields  []string
	TTL           time.Duration
}

type WhereFilter struct {
	Field string
	Op    string
	Value string
}

type QueryOptions struct {
	Where  *WhereFilter
	Search string
	Limit  int
	Cursor string
}

type QueryResult struct {
	Documents  []Document
	NextCursor string
	HasMore    bool
}

type Store interface {
	MigrateDocs(ctx context.Context) error
	ConfigureIndexes(ctx context.Context, db int, collection string, cfg IndexConfig) (IndexConfig, error)
	GetIndexConfig(ctx context.Context, db int, collection string) (IndexConfig, error)
	PutDoc(ctx context.Context, db int, collection, id string, value []byte, opts WriteOptions, createOnly bool) (Document, bool, error)
	GetDoc(ctx context.Context, db int, collection, id string) (Document, error)
	DeleteDoc(ctx context.Context, db int, collection, id string) (bool, error)
	QueryDocs(ctx context.Context, db int, collection string, opts QueryOptions) (QueryResult, error)
}
