package docapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pubflow/redge/internal/config"
	"github.com/pubflow/redge/internal/database"
	"github.com/pubflow/redge/internal/store/sqlstore"
	"go.uber.org/zap"
)

func newTestServer(t *testing.T) *Server {
	return newTestServerWithOptions(t, nil)
}

func newTestServerWithOptions(t *testing.T, mutate func(*Options)) *Server {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "redge-docapi-test.db")
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
	if err := st.MigrateDocs(context.Background()); err != nil {
		t.Fatalf("migrate docs: %v", err)
	}
	opts := Options{Store: st, Logger: zap.NewNop(), Token: "test-token", MaxBodyBytes: 4096, WSEnabled: true}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

func TestHTTPCRUDQueryAndPatch(t *testing.T) {
	srv := newTestServer(t)

	req := jsonReq(http.MethodPut, "/v1/collections/products/indexes", `{"fields":["category"],"searchFields":["name"]}`)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("configure indexes status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = jsonReq(http.MethodPost, "/v1/collections/products", `{"id":"p1","name":"Blue Shirt","category":"apparel","price":29}`)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("insert status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authedReq(http.MethodGet, "/v1/collections/products/p1", nil)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = jsonReq(http.MethodPatch, "/v1/collections/products/p1", `{"price":31}`)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = authedReq(http.MethodGet, "/v1/collections/products?where=category:eq:apparel&search=Blue&limit=10", nil)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("query status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Docs []map[string]any `json:"docs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode query: %v", err)
	}
	if len(body.Docs) != 1 {
		t.Fatalf("expected one doc, got %d body=%s", len(body.Docs), rec.Body.String())
	}
}

func TestAuthRequired(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/collections/products/p1", nil)
	rec := httptest.NewRecorder()

	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestCORSPreflightAllowsConfiguredOrigin(t *testing.T) {
	srv := newTestServerWithOptions(t, func(opts *Options) {
		opts.CORSEnabled = true
		opts.CORSOrigins = []string{"https://app.example.com"}
	})
	req := httptest.NewRequest(http.MethodOptions, "/v1/collections/products", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()

	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("missing allow origin header: %#v", rec.Header())
	}
}

func TestIPAllowlistRejectsDisallowedClient(t *testing.T) {
	srv := newTestServerWithOptions(t, func(opts *Options) {
		opts.IPCheck = true
		opts.AllowedIPs = []string{"203.0.113.10"}
	})
	req := authedReq(http.MethodGet, "/v1/collections/products/p1", nil)
	req.RemoteAddr = "198.51.100.7:12345"
	rec := httptest.NewRecorder()

	srv.srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRateLimitRejectsExcessRequests(t *testing.T) {
	srv := newTestServerWithOptions(t, func(opts *Options) {
		opts.RateLimitEnabled = true
		opts.RateLimitRPS = 1
		opts.RateLimitBurst = 1
	})

	first := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(first, authedReq(http.MethodGet, "/v1/collections/products/p1", nil))
	second := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(second, authedReq(http.MethodGet, "/v1/collections/products/p1", nil))

	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", second.Code, second.Body.String())
	}
}

func TestWebSocketSubscriptionReceivesInsert(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeWS(t, ctx, conn, map[string]any{"id": "s1", "op": "sub", "collection": "orders"})
	var subResp map[string]any
	readWS(t, ctx, conn, &subResp)
	if subResp["ok"] != true || subResp["subId"] == "" {
		t.Fatalf("unexpected sub response: %#v", subResp)
	}

	req := jsonReq(http.MethodPost, "/v1/collections/orders", `{"id":"o1","status":"new"}`)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("insert status=%d body=%s", rec.Code, rec.Body.String())
	}

	var event Event
	readWS(t, ctx, conn, &event)
	if event.Event != "insert" || event.Collection != "orders" || event.ID != "o1" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestWebSocketSubscribeAliasAndDocIDUpsert(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeWS(t, ctx, conn, map[string]any{"id": "s1", "op": "subscribe", "collection": "orders"})
	var subResp map[string]any
	readWS(t, ctx, conn, &subResp)
	if subResp["ok"] != true || subResp["subId"] == "" {
		t.Fatalf("unexpected sub response: %#v", subResp)
	}

	writeWS(t, ctx, conn, map[string]any{
		"id":         "u1",
		"op":         "upsert",
		"collection": "orders",
		"docId":      "o2",
		"doc":        map[string]any{"status": "new"},
	})
	var first map[string]any
	var second map[string]any
	readWS(t, ctx, conn, &first)
	readWS(t, ctx, conn, &second)
	if !hasWSResponse(first, "u1") && !hasWSResponse(second, "u1") {
		t.Fatalf("missing upsert response: first=%#v second=%#v", first, second)
	}
	eventPayload := first
	if hasWSResponse(first, "u1") {
		eventPayload = second
	}
	eventData, err := json.Marshal(eventPayload)
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	var event Event
	if err := json.Unmarshal(eventData, &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.Event != "insert" || event.Collection != "orders" || event.ID != "o2" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestWebSocketSubscriptionLimit(t *testing.T) {
	srv := newTestServerWithOptions(t, func(opts *Options) {
		opts.WSMaxSubscriptions = 1
	})
	ts := httptest.NewServer(srv.srv.Handler)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeWS(t, ctx, conn, map[string]any{"id": "s1", "op": "sub", "collection": "orders"})
	var first map[string]any
	readWS(t, ctx, conn, &first)
	if first["ok"] != true {
		t.Fatalf("unexpected first sub response: %#v", first)
	}
	writeWS(t, ctx, conn, map[string]any{"id": "s2", "op": "sub", "collection": "orders"})
	var second map[string]any
	readWS(t, ctx, conn, &second)
	if second["ok"] != false {
		t.Fatalf("expected second sub to fail, got %#v", second)
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

func jsonReq(method, target, body string) *http.Request {
	req := authedReq(method, target, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func writeWS(t *testing.T, ctx context.Context, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal ws: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write ws: %v", err)
	}
}

func readWS(t *testing.T, ctx context.Context, conn *websocket.Conn, out any) {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read ws: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("decode ws %q: %v", data, err)
	}
}

func hasWSResponse(v map[string]any, id string) bool {
	return v["id"] == id && v["ok"] == true
}
