package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/docstore"
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

func TestDocumentIndexesAreRebuiltOnUpdate(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.MigrateDocs(ctx); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	if _, err := st.ConfigureIndexes(ctx, 0, "products", docstore.IndexConfig{Fields: []string{"category"}, SearchFields: []string{"name"}}); err != nil {
		t.Fatalf("configure indexes: %v", err)
	}
	if _, _, err := st.PutDoc(ctx, 0, "products", "p1", []byte(`{"name":"Blue Shirt","category":"apparel"}`), docstore.WriteOptions{}, true); err != nil {
		t.Fatalf("put doc: %v", err)
	}
	if _, _, err := st.PutDoc(ctx, 0, "products", "p1", []byte(`{"name":"Blue Mug","category":"home"}`), docstore.WriteOptions{}, false); err != nil {
		t.Fatalf("update doc: %v", err)
	}

	oldResult, err := st.QueryDocs(ctx, 0, "products", docstore.QueryOptions{Where: &docstore.WhereFilter{Field: "category", Op: "eq", Value: "apparel"}, Limit: 10})
	if err != nil {
		t.Fatalf("query old index: %v", err)
	}
	if len(oldResult.Documents) != 0 {
		t.Fatalf("expected old index to be empty, got %d", len(oldResult.Documents))
	}

	newResult, err := st.QueryDocs(ctx, 0, "products", docstore.QueryOptions{Where: &docstore.WhereFilter{Field: "category", Op: "eq", Value: "home"}, Search: "Mug", Limit: 10})
	if err != nil {
		t.Fatalf("query new index: %v", err)
	}
	if len(newResult.Documents) != 1 || newResult.Documents[0].ID != "p1" {
		t.Fatalf("unexpected new query result: %#v", newResult.Documents)
	}
}

func TestDocumentQueryRejectsUnindexedField(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.MigrateDocs(ctx); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	_, err := st.QueryDocs(ctx, 0, "products", docstore.QueryOptions{Where: &docstore.WhereFilter{Field: "category", Op: "eq", Value: "apparel"}, Limit: 10})
	if err == nil {
		t.Fatal("expected unindexed field error")
	}
}

func TestRESPPrimitiveStoreMethods(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if err := st.MSet(ctx, 0, []store.KeyValue{{Key: "a", Value: []byte("1")}, {Key: "b", Value: []byte("2")}}, store.SetOptions{}); err != nil {
		t.Fatalf("mset: %v", err)
	}
	values, err := st.MGet(ctx, 0, "a", "missing", "b")
	if err != nil {
		t.Fatalf("mget: %v", err)
	}
	if len(values) != 3 || string(values[0].Data) != "1" || values[1] != nil || string(values[2].Data) != "2" {
		t.Fatalf("unexpected mget values: %#v", values)
	}
	old, deleted, err := st.GetDel(ctx, 0, "a")
	if err != nil || !deleted || old == nil || string(old.Data) != "1" {
		t.Fatalf("getdel old=%v deleted=%v err=%v", old, deleted, err)
	}
	if got, err := st.Get(ctx, 0, "a"); err != nil || got != nil {
		t.Fatalf("expected a deleted, got=%v err=%v", got, err)
	}
	if _, _, err := st.Set(ctx, 0, "ttl", []byte("v"), store.SetOptions{TTL: time.Hour}); err != nil {
		t.Fatalf("set ttl: %v", err)
	}
	if ok, err := st.Persist(ctx, 0, "ttl"); err != nil || !ok {
		t.Fatalf("persist ok=%v err=%v", ok, err)
	}
	if _, exists, hasTTL, err := st.TTL(ctx, 0, "ttl"); err != nil || !exists || hasTTL {
		t.Fatalf("ttl after persist exists=%v hasTTL=%v err=%v", exists, hasTTL, err)
	}
}

func TestZRangeByScoreStoreMethod(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	for _, item := range []struct {
		score  float64
		member string
	}{{1, "one"}, {2, "two"}, {3, "three"}} {
		if _, err := st.ZAdd(ctx, 0, "z", item.score, []byte(item.member)); err != nil {
			t.Fatalf("zadd %s: %v", item.member, err)
		}
	}
	members, err := st.ZRangeByScore(ctx, 0, "z", store.ScoreBound{Value: 1}, store.ScoreBound{Value: 3}, 1, 2, false)
	if err != nil {
		t.Fatalf("zrangebyscore: %v", err)
	}
	if len(members) != 2 || string(members[0].Member) != "two" || string(members[1].Member) != "three" {
		t.Fatalf("unexpected range: %#v", members)
	}
	rev, err := st.ZRangeByScore(ctx, 0, "z", store.ScoreBound{Value: 1}, store.ScoreBound{Value: 3}, 0, 1, true)
	if err != nil {
		t.Fatalf("zrevrangebyscore: %v", err)
	}
	if len(rev) != 1 || string(rev[0].Member) != "three" {
		t.Fatalf("unexpected reverse range: %#v", rev)
	}
}
