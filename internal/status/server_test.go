package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

func TestStatusAllowsGetAndSetsSecurityHeaders(t *testing.T) {
	srv := New(Options{Store: statusTestStore{}, Logger: zap.NewNop(), DatabaseType: "sqlite"})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff header")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("expected no-store header")
	}
}

func TestStatusRejectsNonGet(t *testing.T) {
	srv := New(Options{Store: statusTestStore{}, Logger: zap.NewNop(), DatabaseType: "sqlite"})
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()

	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

type statusTestStore struct{}

func (statusTestStore) Migrate(ctx context.Context) error { return nil }
func (statusTestStore) Ping(ctx context.Context) error    { return nil }
func (statusTestStore) Type(ctx context.Context, db int, key string) (string, error) {
	return store.TypeNone, nil
}
func (statusTestStore) Get(ctx context.Context, db int, key string) (*store.Value, error) {
	return nil, nil
}
func (statusTestStore) Set(ctx context.Context, db int, key string, value []byte, opts store.SetOptions) (*store.Value, bool, error) {
	return nil, false, nil
}
func (statusTestStore) Delete(ctx context.Context, db int, keys ...string) (int64, error) {
	return 0, nil
}
func (statusTestStore) Exists(ctx context.Context, db int, keys ...string) (int64, error) {
	return 0, nil
}
func (statusTestStore) Expire(ctx context.Context, db int, key string, ttl time.Duration) (bool, error) {
	return false, nil
}
func (statusTestStore) TTL(ctx context.Context, db int, key string) (time.Duration, bool, bool, error) {
	return 0, false, false, nil
}
func (statusTestStore) IncrBy(ctx context.Context, db int, key string, delta int64) (int64, error) {
	return 0, nil
}
func (statusTestStore) ZAdd(ctx context.Context, db int, key string, score float64, member []byte) (int64, error) {
	return 0, nil
}
func (statusTestStore) ZCard(ctx context.Context, db int, key string) (int64, error) {
	return 0, nil
}
func (statusTestStore) ZRem(ctx context.Context, db int, key string, members ...[]byte) (int64, error) {
	return 0, nil
}
func (statusTestStore) ZRemRangeByScore(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	return 0, nil
}
func (statusTestStore) ZRange(ctx context.Context, db int, key string, start, stop int64) ([]store.ZMember, error) {
	return nil, nil
}
func (statusTestStore) ZScore(ctx context.Context, db int, key string, member []byte) (float64, bool, error) {
	return 0, false, nil
}
func (statusTestStore) ZCount(ctx context.Context, db int, key string, min, max store.ScoreBound) (int64, error) {
	return 0, nil
}
func (statusTestStore) Scan(ctx context.Context, db int, cursor string, pattern string, count int) (store.ScanResult, error) {
	return store.ScanResult{}, nil
}
func (statusTestStore) CleanupExpired(ctx context.Context, limit int) (int64, error) {
	return 0, nil
}
func (statusTestStore) Stats(ctx context.Context, db int) (store.Stats, error) {
	return store.Stats{}, nil
}
func (statusTestStore) Close() error { return nil }
