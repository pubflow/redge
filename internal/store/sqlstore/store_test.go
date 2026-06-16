package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "redge-test.db")
	conn, err := database.NewConnection(&config.Config{
		DatabaseURL:          "sqlite://" + dbPath,
		DatabaseMaxOpenConns: 1,
		DatabaseMaxIdleConns: 1,
		DatabaseConnMaxLife:  time.Hour,
		Environment:          "test",
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("new connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	st, err := New(conn, zap.NewNop())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestSetUpdatesExistingStringWithoutDuplicateKey(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, wrote, err := st.Set(ctx, 0, "test65", []byte("v1"), store.SetOptions{}); err != nil || !wrote {
		t.Fatalf("set v1 wrote=%v err=%v", wrote, err)
	}
	if _, wrote, err := st.Set(ctx, 0, "test65", []byte("v2"), store.SetOptions{}); err != nil || !wrote {
		t.Fatalf("set v2 wrote=%v err=%v", wrote, err)
	}

	got, err := st.Get(ctx, 0, "test65")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Data) != "v2" {
		t.Fatalf("expected v2, got %q", got.Data)
	}
}

func TestGUIStyleStringEditFlow(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, _, err := st.Set(ctx, 0, "test65", []byte("hola"), store.SetOptions{}); err != nil {
		t.Fatalf("set initial: %v", err)
	}
	typ, err := st.Type(ctx, 0, "test65")
	if err != nil {
		t.Fatalf("type: %v", err)
	}
	if typ != store.TypeString {
		t.Fatalf("expected string type, got %q", typ)
	}
	if got, err := st.Get(ctx, 0, "test65"); err != nil || string(got.Data) != "hola" {
		t.Fatalf("get initial got=%v err=%v", got, err)
	}
	if _, wrote, err := st.Set(ctx, 0, "test65", []byte("hola editado"), store.SetOptions{}); err != nil || !wrote {
		t.Fatalf("set edited wrote=%v err=%v", wrote, err)
	}
	got, err := st.Get(ctx, 0, "test65")
	if err != nil {
		t.Fatalf("get edited: %v", err)
	}
	if string(got.Data) != "hola editado" {
		t.Fatalf("expected edited value, got %q", got.Data)
	}
}

func TestSetKeepTTLPreservesExistingExpiration(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, _, err := st.Set(ctx, 0, "ttl-key", []byte("v1"), store.SetOptions{TTL: time.Hour}); err != nil {
		t.Fatalf("set with ttl: %v", err)
	}
	before, exists, hasTTL, err := st.TTL(ctx, 0, "ttl-key")
	if err != nil || !exists || !hasTTL {
		t.Fatalf("ttl before=%v exists=%v hasTTL=%v err=%v", before, exists, hasTTL, err)
	}
	if _, _, err := st.Set(ctx, 0, "ttl-key", []byte("v2"), store.SetOptions{KeepTTL: true}); err != nil {
		t.Fatalf("set keepttl: %v", err)
	}
	after, exists, hasTTL, err := st.TTL(ctx, 0, "ttl-key")
	if err != nil || !exists || !hasTTL {
		t.Fatalf("ttl after=%v exists=%v hasTTL=%v err=%v", after, exists, hasTTL, err)
	}
	if after <= 0 || after > before {
		t.Fatalf("expected preserved positive ttl <= before, before=%v after=%v", before, after)
	}
}

func TestSameKeyCanExistInDifferentLogicalDBs(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if _, _, err := st.Set(ctx, 0, "same-key", []byte("db0"), store.SetOptions{}); err != nil {
		t.Fatalf("set db0: %v", err)
	}
	if _, _, err := st.Set(ctx, 1, "same-key", []byte("db1"), store.SetOptions{}); err != nil {
		t.Fatalf("set db1: %v", err)
	}

	db0, err := st.Get(ctx, 0, "same-key")
	if err != nil {
		t.Fatalf("get db0: %v", err)
	}
	db1, err := st.Get(ctx, 1, "same-key")
	if err != nil {
		t.Fatalf("get db1: %v", err)
	}
	if string(db0.Data) != "db0" || string(db1.Data) != "db1" {
		t.Fatalf("unexpected values db0=%q db1=%q", db0.Data, db1.Data)
	}
}

func TestIncrByUpdatesExistingStringWithoutDuplicateKey(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	first, err := st.IncrBy(ctx, 0, "counter", 1)
	if err != nil {
		t.Fatalf("first incr: %v", err)
	}
	second, err := st.IncrBy(ctx, 0, "counter", 1)
	if err != nil {
		t.Fatalf("second incr: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("unexpected increments first=%d second=%d", first, second)
	}
}
