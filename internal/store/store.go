package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrWrongType = errors.New("WRONGTYPE Operation against a key holding the wrong kind of value")
	ErrNotInt    = errors.New("ERR value is not an integer or out of range")
)

type Value struct {
	Type      string
	Data      []byte
	ExpiresAt *time.Time
	Version   int64
}

type SetOptions struct {
	TTL     time.Duration
	KeepTTL bool
	NX      bool
	XX      bool
	Get     bool
}

type Store interface {
	Migrate(ctx context.Context) error
	Ping(ctx context.Context) error
	Get(ctx context.Context, db int, key string) (*Value, error)
	Set(ctx context.Context, db int, key string, value []byte, opts SetOptions) (*Value, bool, error)
	Delete(ctx context.Context, db int, keys ...string) (int64, error)
	Exists(ctx context.Context, db int, keys ...string) (int64, error)
	Expire(ctx context.Context, db int, key string, ttl time.Duration) (bool, error)
	TTL(ctx context.Context, db int, key string) (time.Duration, bool, bool, error)
	IncrBy(ctx context.Context, db int, key string, delta int64) (int64, error)
	ZAdd(ctx context.Context, db int, key string, score float64, member []byte) (int64, error)
	ZCard(ctx context.Context, db int, key string) (int64, error)
	ZRem(ctx context.Context, db int, key string, members ...[]byte) (int64, error)
	ZRemRangeByScore(ctx context.Context, db int, key string, min, max ScoreBound) (int64, error)
	ZRange(ctx context.Context, db int, key string, start, stop int64) ([]ZMember, error)
	ZScore(ctx context.Context, db int, key string, member []byte) (float64, bool, error)
	ZCount(ctx context.Context, db int, key string, min, max ScoreBound) (int64, error)
	Scan(ctx context.Context, db int, cursor string, pattern string, count int) (ScanResult, error)
	CleanupExpired(ctx context.Context, limit int) (int64, error)
	Stats(ctx context.Context) (Stats, error)
	Close() error
}

type Stats struct {
	Keys int64 `json:"keys"`
}

type ScoreBound struct {
	Value     float64
	Infinite  int
	Exclusive bool
}

type ScanResult struct {
	Cursor string
	Keys   []string
}

type ZMember struct {
	Member []byte
	Score  float64
}
