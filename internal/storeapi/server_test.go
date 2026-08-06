package storeapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/store/sqlstore"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "redge-storeapi-test.db")
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
	st, err := sqlstore.New(conn, zap.NewNop())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(Options{Store: st, Cache: cache.NewMemory(cache.Options{}), Logger: zap.NewNop(), Token: "test-token", MaxBodyBytes: 4096})
}

func TestKVLifecycleAndBinaryValues(t *testing.T) {
	srv := newTestServer(t)
	raw := []byte{0, 1, 2, 255}
	req := jsonReq(http.MethodPut, "/v1/kv/bin", map[string]any{"value_base64": base64.StdEncoding.EncodeToString(raw), "ttl_seconds": 60})
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	var setBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &setBody)
	if setBody["value_base64"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("unexpected set body: %#v", setBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/bin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	var getBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &getBody)
	if getBody["ttl_state"] != "volatile" || getBody["value_base64"] != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("unexpected get body: %#v", getBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodPost, "/v1/kv/bin/persist", bytes.NewReader([]byte(`{}`))))
	if rec.Code != http.StatusOK {
		t.Fatalf("persist status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodPost, "/v1/kv/bin/getdel", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("getdel status=%d body=%s", rec.Code, rec.Body.String())
	}
	var getdelBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &getdelBody)
	if getdelBody["type"] != "string" || getdelBody["deleted"] != true {
		t.Fatalf("unexpected getdel body: %#v", getdelBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodDelete, "/v1/kv/bin", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBatchIncrScanAndAuth(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/kv/missing", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/kv/batch/set", map[string]any{"items": []map[string]any{{"key": "a", "value": "1"}, {"key": "b", "value": "2"}}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("batch set status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/kv/a/incr", map[string]any{"by": 2}))
	if rec.Code != http.StatusOK {
		t.Fatalf("incr status=%d body=%s", rec.Code, rec.Body.String())
	}
	var incrBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &incrBody)
	if incrBody["value"].(float64) != 3 {
		t.Fatalf("unexpected incr body: %#v", incrBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv?match=*&limit=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scan status=%d body=%s", rec.Code, rec.Body.String())
	}
	var scanBody struct {
		Keys []string `json:"keys"`
	}
	mustDecode(t, rec.Body.Bytes(), &scanBody)
	if len(scanBody.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %#v", scanBody.Keys)
	}
}

func TestWrongTypeAndZSets(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/rank/members", map[string]any{"member": "a", "score": 1}))
	if rec.Code != http.StatusOK {
		t.Fatalf("zadd status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/rank", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected wrong type 409, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/zsets/rank/byscore?min=0&max=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("range by score status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Members []map[string]any `json:"members"`
	}
	mustDecode(t, rec.Body.Bytes(), &body)
	if len(body.Members) != 1 || body.Members[0]["member"] != "a" {
		t.Fatalf("unexpected zset body: %#v", body)
	}
}

func TestPhaseATypeExistsBatchDelete(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPut, "/v1/kv/s1", map[string]any{"value": "hello"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/z1/members", map[string]any{"member": "m", "score": 1}))
	if rec.Code != http.StatusOK {
		t.Fatalf("zadd status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/s1/type", nil))
	mustStatus(t, rec, http.StatusOK)
	var typBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &typBody)
	if typBody["type"] != "string" {
		t.Fatalf("expected string type, got %#v", typBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/z1/type", nil))
	mustStatus(t, rec, http.StatusOK)
	mustDecode(t, rec.Body.Bytes(), &typBody)
	if typBody["type"] != "zset" {
		t.Fatalf("expected zset type, got %#v", typBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/missing/type", nil))
	mustStatus(t, rec, http.StatusOK)
	mustDecode(t, rec.Body.Bytes(), &typBody)
	if typBody["type"] != "none" {
		t.Fatalf("expected none type, got %#v", typBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/s1/exists", nil))
	mustStatus(t, rec, http.StatusOK)
	var existsBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &existsBody)
	if existsBody["exists"] != true {
		t.Fatalf("expected exists true, got %#v", existsBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, authedReq(http.MethodGet, "/v1/kv/missing/exists", nil))
	mustStatus(t, rec, http.StatusOK)
	mustDecode(t, rec.Body.Bytes(), &existsBody)
	if existsBody["exists"] != false {
		t.Fatalf("expected exists false, got %#v", existsBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/kv/batch/exists", map[string]any{"keys": []string{"s1", "missing", "z1"}}))
	mustStatus(t, rec, http.StatusOK)
	var batchExists struct {
		Count float64          `json:"count"`
		Items []map[string]any `json:"items"`
	}
	mustDecode(t, rec.Body.Bytes(), &batchExists)
	if batchExists.Count != 2 || len(batchExists.Items) != 3 {
		t.Fatalf("unexpected batch exists: %#v", batchExists)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/kv/batch/delete", map[string]any{"keys": []string{"s1", "missing"}}))
	mustStatus(t, rec, http.StatusOK)
	var batchDelete map[string]any
	mustDecode(t, rec.Body.Bytes(), &batchDelete)
	if batchDelete["deleted"].(float64) != 1 {
		t.Fatalf("unexpected batch delete: %#v", batchDelete)
	}
}

func TestPhaseBMultiZAddZRem(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/board/members", map[string]any{
		"members": []map[string]any{
			{"member": "alice", "score": 10},
			{"member": "bob", "score": 20},
			{"member_base64": base64.StdEncoding.EncodeToString([]byte{0, 1}), "score": 5},
		},
	}))
	mustStatus(t, rec, http.StatusOK)
	var addBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &addBody)
	if addBody["added"].(float64) != 3 {
		t.Fatalf("expected added=3, got %#v", addBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/board/members", map[string]any{"member": "carol", "score": 30}))
	mustStatus(t, rec, http.StatusOK)

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/board/members/remove", map[string]any{"members": []string{"alice", "bob"}}))
	mustStatus(t, rec, http.StatusOK)
	var remBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &remBody)
	if remBody["removed"].(float64) != 2 {
		t.Fatalf("expected removed=2, got %#v", remBody)
	}
}

func TestPhaseCZIncrByAndPop(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/scores/members", map[string]any{
		"members": []map[string]any{
			{"member": "a", "score": 1},
			{"member": "b", "score": 2},
			{"member": "c", "score": 3},
		},
	}))
	mustStatus(t, rec, http.StatusOK)

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/scores/incrby", map[string]any{"member": "a", "by": 4.5}))
	mustStatus(t, rec, http.StatusOK)
	var incrBody map[string]any
	mustDecode(t, rec.Body.Bytes(), &incrBody)
	if incrBody["score"].(float64) != 5.5 {
		t.Fatalf("unexpected incrby: %#v", incrBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/scores/popmin", map[string]any{"count": 1}))
	mustStatus(t, rec, http.StatusOK)
	var popBody struct {
		Members []map[string]any `json:"members"`
	}
	mustDecode(t, rec.Body.Bytes(), &popBody)
	if len(popBody.Members) != 1 || popBody.Members[0]["member"] != "b" {
		t.Fatalf("expected popmin b, got %#v", popBody)
	}

	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, jsonReq(http.MethodPost, "/v1/zsets/scores/popmax", map[string]any{"count": 1}))
	mustStatus(t, rec, http.StatusOK)
	mustDecode(t, rec.Body.Bytes(), &popBody)
	if len(popBody.Members) != 1 || popBody.Members[0]["member"] != "a" {
		t.Fatalf("expected popmax a, got %#v", popBody)
	}
}

func mustStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func authedReq(method, target string, body *bytes.Reader) *http.Request {
	if body == nil {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Authorization", "Bearer test-token")
	return req
}

func jsonReq(method, target string, body any) *http.Request {
	data, _ := json.Marshal(body)
	req := authedReq(method, target, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func mustDecode(t *testing.T, data []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
}
