package docapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/pubflow/redge/internal/docstore"
	"go.uber.org/zap"
)

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr               string
	Token              string
	MaxBodyBytes       int64
	WSEnabled          bool
	AllowedIPs         []string
	IPCheck            bool
	CORSEnabled        bool
	CORSOrigins        []string
	RateLimitEnabled   bool
	RateLimitRPS       int
	RateLimitBurst     int
	WSMaxMessageBytes  int64
	WSIdleTimeout      time.Duration
	WSMaxSubscriptions int
	Store              docstore.Store
	Logger             *zap.Logger
}

type Server struct {
	opts    Options
	srv     *http.Server
	hub     *Hub
	allowed []ipRule
	limiter *rateLimiter
}

func New(opts Options) *Server {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 1 << 20
	}
	if opts.WSMaxMessageBytes <= 0 {
		opts.WSMaxMessageBytes = 64 << 10
	}
	if opts.WSIdleTimeout <= 0 {
		opts.WSIdleTimeout = time.Minute
	}
	if opts.WSMaxSubscriptions <= 0 {
		opts.WSMaxSubscriptions = 32
	}
	if opts.RateLimitRPS <= 0 {
		opts.RateLimitRPS = 30
	}
	if opts.RateLimitBurst <= 0 {
		opts.RateLimitBurst = 60
	}
	s := &Server{opts: opts, hub: NewHub(), allowed: parseIPRules(opts.AllowedIPs)}
	if opts.RateLimitEnabled {
		s.limiter = newRateLimiter(opts.RateLimitRPS, opts.RateLimitBurst)
	}
	mux := http.NewServeMux()
	mux.Handle("/health", s.secure(http.HandlerFunc(s.health)))
	mux.Handle("/ready", s.secure(http.HandlerFunc(s.ready)))
	mux.Handle("/v1/", s.secure(s.auth(http.HandlerFunc(s.route))))
	s.srv = &http.Server{Addr: opts.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) Mount(mux *http.ServeMux) {
	mux.Handle("/v1/", s.secure(s.auth(http.HandlerFunc(s.route))))
}

func (s *Server) ListenAndServe() error {
	s.opts.Logger.Info("document api listener ready", zap.String("addr", s.opts.Addr), zap.Bool("websocket", s.opts.WSEnabled))
	if err := s.srv.ListenAndServe(); err != nil {
		if errors.Is(err, http.ErrServerClosed) {
			return ErrClosed
		}
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "redge-docapi"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if p, ok := s.opts.Store.(interface {
		Ping(context.Context) error
	}); ok {
		if err := p.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "error": err.Error()})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if !s.applyCORS(w, r) {
			return
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if s.opts.IPCheck && !s.ipAllowed(clientIP(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "ip not allowed"})
			return
		}
		if s.limiter != nil && !s.limiter.allow(rateLimitKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "rate limit exceeded"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) bool {
	if !s.opts.CORSEnabled {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if !originAllowed(origin, s.opts.CORSOrigins) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "origin not allowed"})
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	return true
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got == "" {
				got = r.URL.Query().Get("token")
			}
			if got != s.opts.Token {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "document api token required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/ws" {
		s.handleWS(w, r)
		return
	}
	collection, id, suffix, ok := parseCollectionPath(r.URL.Path)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	if suffix == "indexes" {
		s.handleIndexes(w, r, collection)
		return
	}
	if id == "" {
		switch r.Method {
		case http.MethodPost:
			s.insert(w, r, collection)
		case http.MethodGet:
			s.query(w, r, collection)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.get(w, r, collection, id)
	case http.MethodPut:
		s.upsert(w, r, collection, id)
	case http.MethodPatch:
		s.patch(w, r, collection, id)
	case http.MethodDelete:
		s.delete(w, r, collection, id)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) insert(w http.ResponseWriter, r *http.Request, collection string) {
	doc, err := s.readDoc(r)
	if err != nil {
		writeError(w, err)
		return
	}
	id := stringField(doc, "id")
	if id == "" {
		id = newDocID()
	}
	value, err := canonicalJSON(doc)
	if err != nil {
		writeError(w, err)
		return
	}
	stored, _, err := s.opts.Store.PutDoc(r.Context(), 0, collection, id, value, writeOptionsFromRequest(r), true)
	if err != nil {
		writeError(w, err)
		return
	}
	s.hub.Publish(Event{Event: "insert", Collection: collection, ID: id, Doc: doc})
	writeJSON(w, http.StatusCreated, docEnvelope(stored))
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, collection, id string) {
	doc, err := s.opts.Store.GetDoc(r.Context(), 0, collection, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, docEnvelope(doc))
}

func (s *Server) upsert(w http.ResponseWriter, r *http.Request, collection, id string) {
	doc, err := s.readDoc(r)
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := canonicalJSON(doc)
	if err != nil {
		writeError(w, err)
		return
	}
	stored, created, err := s.opts.Store.PutDoc(r.Context(), 0, collection, id, value, writeOptionsFromRequest(r), false)
	if err != nil {
		writeError(w, err)
		return
	}
	event := "update"
	status := http.StatusOK
	if created {
		event = "insert"
		status = http.StatusCreated
	}
	s.hub.Publish(Event{Event: event, Collection: collection, ID: id, Doc: doc})
	writeJSON(w, status, docEnvelope(stored))
}

func (s *Server) patch(w http.ResponseWriter, r *http.Request, collection, id string) {
	patch, err := s.readDoc(r)
	if err != nil {
		writeError(w, err)
		return
	}
	current, err := s.opts.Store.GetDoc(r.Context(), 0, collection, id)
	if err != nil {
		writeError(w, err)
		return
	}
	base, err := decodeDoc(current.Value)
	if err != nil {
		writeError(w, err)
		return
	}
	for key, value := range patch {
		base[key] = value
	}
	value, err := canonicalJSON(base)
	if err != nil {
		writeError(w, err)
		return
	}
	stored, _, err := s.opts.Store.PutDoc(r.Context(), 0, collection, id, value, writeOptionsFromRequest(r), false)
	if err != nil {
		writeError(w, err)
		return
	}
	s.hub.Publish(Event{Event: "update", Collection: collection, ID: id, Doc: base})
	writeJSON(w, http.StatusOK, docEnvelope(stored))
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, collection, id string) {
	deleted, err := s.opts.Store.DeleteDoc(r.Context(), 0, collection, id)
	if err != nil {
		writeError(w, err)
		return
	}
	if !deleted {
		writeError(w, docstore.ErrNotFound)
		return
	}
	s.hub.Publish(Event{Event: "delete", Collection: collection, ID: id})
	writeJSON(w, http.StatusOK, map[string]any{"collection": collection, "id": id, "deleted": true})
}

func (s *Server) query(w http.ResponseWriter, r *http.Request, collection string) {
	opts, err := queryOptionsFromRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := s.opts.Store.QueryDocs(r.Context(), 0, collection, opts)
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(result.Documents))
	for _, doc := range result.Documents {
		items = append(items, docEnvelope(doc))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"collection":  collection,
		"docs":        items,
		"next_cursor": result.NextCursor,
		"has_more":    result.HasMore,
	})
}

func (s *Server) handleIndexes(w http.ResponseWriter, r *http.Request, collection string) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.opts.Store.GetIndexConfig(r.Context(), 0, collection)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	case http.MethodPut:
		var cfg docstore.IndexConfig
		if err := decodeJSONBody(r, s.opts.MaxBodyBytes, &cfg); err != nil {
			writeError(w, err)
			return
		}
		cfg, err := s.opts.Store.ConfigureIndexes(r.Context(), 0, collection, cfg)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) readDoc(r *http.Request) (map[string]any, error) {
	var doc map[string]any
	if err := decodeJSONBody(r, s.opts.MaxBodyBytes, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: document body must be a JSON object", docstore.ErrInvalidRequest)
	}
	return doc, nil
}

func parseCollectionPath(path string) (collection, id, suffix string, ok bool) {
	raw := strings.Trim(strings.TrimPrefix(path, "/v1/collections"), "/")
	if raw == "" {
		return "", "", "", false
	}
	parts := strings.Split(raw, "/")
	collection = parts[0]
	if !validName(collection) {
		return "", "", "", false
	}
	if len(parts) == 1 {
		return collection, "", "", true
	}
	if len(parts) == 2 && parts[1] == "indexes" {
		return collection, "", "indexes", true
	}
	if len(parts) == 2 && validName(parts[1]) {
		return collection, parts[1], "", true
	}
	return "", "", "", false
}

func queryOptionsFromRequest(r *http.Request) (docstore.QueryOptions, error) {
	q := r.URL.Query()
	opts := docstore.QueryOptions{
		Search: q.Get("search"),
		Cursor: q.Get("cursor"),
		Limit:  20,
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return opts, fmt.Errorf("%w: limit must be between 1 and 100", docstore.ErrInvalidRequest)
		}
		opts.Limit = n
	}
	if raw := q.Get("where"); raw != "" {
		parts := strings.SplitN(raw, ":", 3)
		if len(parts) != 3 || !validName(parts[0]) || parts[1] == "" {
			return opts, fmt.Errorf("%w: where must be field:op:value", docstore.ErrInvalidRequest)
		}
		opts.Where = &docstore.WhereFilter{Field: parts[0], Op: parts[1], Value: parts[2]}
	}
	return opts, nil
}

func writeOptionsFromRequest(r *http.Request) docstore.WriteOptions {
	q := r.URL.Query()
	opts := docstore.WriteOptions{
		IndexedFields: listParam(qValues(q, "index", "indexedField")),
		SearchFields:  listParam(qValues(q, "searchField", "search")),
	}
	if raw := q.Get("ttl"); raw != "" {
		if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
			opts.TTL = time.Duration(sec) * time.Second
		}
	}
	return opts
}

func qValues(q map[string][]string, names ...string) []string {
	var out []string
	for _, name := range names {
		out = append(out, q[name]...)
	}
	return out
}

func listParam(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func decodeJSONBody(r *http.Request, maxBytes int64, out any) error {
	defer r.Body.Close()
	reader := http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(reader)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%w: invalid JSON body", docstore.ErrInvalidRequest)
	}
	return nil
}

func canonicalJSON(doc map[string]any) ([]byte, error) {
	normalized := normalizeJSONNumbers(doc)
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", docstore.ErrInvalidRequest, err)
	}
	return data, nil
}

func normalizeJSONNumbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = normalizeJSONNumbers(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = normalizeJSONNumbers(v)
		}
		return out
	case json.Number:
		if strings.ContainsAny(string(x), ".eE") {
			if f, err := x.Float64(); err == nil {
				return f
			}
		}
		if n, err := x.Int64(); err == nil {
			return n
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return string(x)
	default:
		return x
	}
}

func decodeDoc(value []byte) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(value, &doc); err != nil {
		return nil, fmt.Errorf("%w: stored document is invalid", docstore.ErrInvalidRequest)
	}
	return doc, nil
}

func docEnvelope(doc docstore.Document) map[string]any {
	body, err := decodeDoc(doc.Value)
	if err != nil {
		body = map[string]any{}
	}
	return map[string]any{
		"collection": doc.Collection,
		"id":         doc.ID,
		"version":    doc.Version,
		"created_at": doc.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": doc.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"doc":        body,
	}
}

func stringField(doc map[string]any, field string) string {
	if v, ok := doc[field].(string); ok && validName(v) {
		return v
	}
	return ""
}

func newDocID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "doc_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "doc_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func validName(v string) bool {
	if v == "" || len(v) > 191 {
		return false
	}
	for _, r := range v {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, docstore.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, docstore.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, docstore.ErrInvalidRequest), errors.Is(err, docstore.ErrInvalidIndex):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) ipAllowed(ip string) bool {
	if len(s.allowed) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	for _, rule := range s.allowed {
		if rule.exact != "" && rule.exact == ip {
			return true
		}
		if rule.ip != nil && parsed != nil && rule.ip.Equal(parsed) {
			return true
		}
		if rule.cidr != nil && parsed != nil && rule.cidr.Contains(parsed) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func rateLimitKey(r *http.Request) string {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); token != "" {
		return "token:" + token
	}
	return "ip:" + clientIP(r)
}

type ipRule struct {
	exact string
	ip    net.IP
	cidr  *net.IPNet
}

func parseIPRules(raw []string) []ipRule {
	out := make([]ipRule, 0, len(raw))
	for _, item := range raw {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if ip := net.ParseIP(item); ip != nil {
			out = append(out, ipRule{ip: ip})
			continue
		}
		if _, cidr, err := net.ParseCIDR(item); err == nil {
			out = append(out, ipRule{cidr: cidr})
			continue
		}
		out = append(out, ipRule{exact: item})
	}
	return out
}

func originAllowed(origin string, allowed []string) bool {
	for _, item := range allowed {
		item = strings.TrimSpace(item)
		if item == "*" || strings.EqualFold(item, origin) {
			return true
		}
	}
	return false
}

type rateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	clients map[string]*rateBucket
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rps, burst int) *rateLimiter {
	return &rateLimiter{rate: float64(rps), burst: float64(burst), clients: map[string]*rateBucket{}}
}

func (l *rateLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.clients[key]
	if b == nil {
		l.clients[key] = &rateBucket{tokens: l.burst - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type Event struct {
	SubID      string         `json:"subId,omitempty"`
	Event      string         `json:"event"`
	Collection string         `json:"collection"`
	ID         string         `json:"id"`
	Doc        map[string]any `json:"doc,omitempty"`
}

type Hub struct {
	mu   sync.Mutex
	subs map[string]*subscription
}

type subscription struct {
	collection string
	ch         chan Event
}

func NewHub() *Hub {
	return &Hub{subs: map[string]*subscription{}}
}

func (h *Hub) Subscribe(collection string) (string, <-chan Event) {
	id := newDocID()
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.subs[id] = &subscription{collection: collection, ch: ch}
	h.mu.Unlock()
	return id, ch
}

func (h *Hub) Unsubscribe(id string) {
	h.mu.Lock()
	sub := h.subs[id]
	delete(h.subs, id)
	h.mu.Unlock()
	if sub != nil {
		close(sub.ch)
	}
}

func (h *Hub) Publish(event Event) {
	h.mu.Lock()
	targets := make(map[string]chan Event)
	for id, sub := range h.subs {
		if sub.collection == event.Collection {
			targets[id] = sub.ch
		}
	}
	h.mu.Unlock()
	for id, ch := range targets {
		event.SubID = id
		select {
		case ch <- event:
		default:
		}
	}
}

type wsRequest struct {
	ID           string         `json:"id"`
	Op           string         `json:"op"`
	Collection   string         `json:"collection"`
	Key          string         `json:"key"`
	DocID        string         `json:"docId"`
	IDValue      string         `json:"idValue"`
	Doc          map[string]any `json:"doc"`
	Patch        map[string]any `json:"patch"`
	Where        string         `json:"where"`
	Search       string         `json:"search"`
	Limit        int            `json:"limit"`
	Cursor       string         `json:"cursor"`
	Index        []string       `json:"index"`
	SearchFields []string       `json:"searchFields"`
	SubID        string         `json:"subId"`
}

func (r wsRequest) docKey() string {
	for _, value := range []string{r.Key, r.DocID, r.IDValue} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type wsClient struct {
	ctx    context.Context
	conn   *websocket.Conn
	writeM sync.Mutex
}

func (c *wsClient) write(v any) {
	data, _ := json.Marshal(v)
	c.writeM.Lock()
	defer c.writeM.Unlock()
	_ = c.conn.Write(c.ctx, websocket.MessageText, data)
}

func (s *Server) wsAcceptOptions() *websocket.AcceptOptions {
	opts := &websocket.AcceptOptions{}
	if !s.opts.CORSEnabled {
		return opts
	}
	for _, origin := range s.opts.CORSOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if origin == "*" {
			opts.InsecureSkipVerify = true
			return opts
		}
		host := origin
		if strings.Contains(origin, "://") {
			if u, err := http.NewRequest(http.MethodGet, origin, nil); err == nil && u.URL.Host != "" {
				host = u.URL.Host
			}
		}
		opts.OriginPatterns = append(opts.OriginPatterns, host)
	}
	return opts
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.opts.WSEnabled {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "websocket disabled"})
		return
	}
	conn, err := websocket.Accept(w, r, s.wsAcceptOptions())
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(s.opts.WSMaxMessageBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	client := &wsClient{ctx: ctx, conn: conn}
	subs := map[string]struct{}{}
	defer func() {
		for id := range subs {
			s.hub.Unsubscribe(id)
		}
	}()
	for {
		readCtx, readCancel := context.WithTimeout(ctx, s.opts.WSIdleTimeout)
		_, data, err := conn.Read(readCtx)
		readCancel()
		if err != nil {
			return
		}
		var req wsRequest
		if err := json.Unmarshal(data, &req); err != nil {
			client.write(map[string]any{"ok": false, "error": "invalid JSON message"})
			continue
		}
		s.dispatchWS(client, req, subs)
	}
}

func (s *Server) dispatchWS(client *wsClient, req wsRequest, subs map[string]struct{}) {
	op := strings.ToLower(strings.TrimSpace(req.Op))
	if req.ID == "" && op != "sub" && op != "subscribe" && op != "unsub" && op != "unsubscribe" {
		client.write(map[string]any{"ok": false, "error": "missing request id"})
		return
	}
	if !validName(req.Collection) && op != "unsub" && op != "unsubscribe" {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": "invalid collection"})
		return
	}
	switch op {
	case "insert":
		s.wsInsert(client, req)
	case "get":
		s.wsGet(client, req)
	case "upsert":
		s.wsUpsert(client, req)
	case "patch":
		s.wsPatch(client, req)
	case "delete":
		s.wsDelete(client, req)
	case "find":
		s.wsFind(client, req)
	case "sub", "subscribe":
		if len(subs) >= s.opts.WSMaxSubscriptions {
			client.write(map[string]any{"id": req.ID, "ok": false, "error": "subscription limit exceeded"})
			return
		}
		subID, ch := s.hub.Subscribe(req.Collection)
		subs[subID] = struct{}{}
		client.write(map[string]any{"id": req.ID, "ok": true, "subId": subID})
		go func() {
			for {
				select {
				case <-client.ctx.Done():
					return
				case event, ok := <-ch:
					if !ok {
						return
					}
					client.write(event)
				}
			}
		}()
	case "unsub", "unsubscribe":
		if req.SubID != "" {
			s.hub.Unsubscribe(req.SubID)
			delete(subs, req.SubID)
		}
		client.write(map[string]any{"id": req.ID, "ok": true})
	default:
		client.write(map[string]any{"id": req.ID, "ok": false, "error": "unknown op"})
	}
}

func (s *Server) wsInsert(client *wsClient, req wsRequest) {
	id := stringField(req.Doc, "id")
	if id == "" {
		id = newDocID()
	}
	s.wsWriteDoc(client, req, id, req.Doc, true)
}

func (s *Server) wsUpsert(client *wsClient, req wsRequest) {
	s.wsWriteDoc(client, req, req.docKey(), req.Doc, false)
}

func (s *Server) wsWriteDoc(client *wsClient, req wsRequest, id string, doc map[string]any, createOnly bool) {
	if !validName(id) {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": "invalid document id"})
		return
	}
	value, err := canonicalJSON(doc)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	stored, created, err := s.opts.Store.PutDoc(client.ctx, 0, req.Collection, id, value, docstore.WriteOptions{IndexedFields: req.Index, SearchFields: req.SearchFields}, createOnly)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	event := "update"
	if createOnly || created {
		event = "insert"
	}
	s.hub.Publish(Event{Event: event, Collection: req.Collection, ID: id, Doc: doc})
	client.write(map[string]any{"id": req.ID, "ok": true, "result": docEnvelope(stored)})
}

func (s *Server) wsGet(client *wsClient, req wsRequest) {
	doc, err := s.opts.Store.GetDoc(client.ctx, 0, req.Collection, req.docKey())
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	client.write(map[string]any{"id": req.ID, "ok": true, "result": docEnvelope(doc)})
}

func (s *Server) wsPatch(client *wsClient, req wsRequest) {
	key := req.docKey()
	current, err := s.opts.Store.GetDoc(client.ctx, 0, req.Collection, key)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	base, err := decodeDoc(current.Value)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	for key, value := range req.Patch {
		base[key] = value
	}
	value, err := canonicalJSON(base)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	stored, _, err := s.opts.Store.PutDoc(client.ctx, 0, req.Collection, key, value, docstore.WriteOptions{IndexedFields: req.Index, SearchFields: req.SearchFields}, false)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	s.hub.Publish(Event{Event: "update", Collection: req.Collection, ID: key, Doc: base})
	client.write(map[string]any{"id": req.ID, "ok": true, "result": docEnvelope(stored)})
}

func (s *Server) wsDelete(client *wsClient, req wsRequest) {
	key := req.docKey()
	deleted, err := s.opts.Store.DeleteDoc(client.ctx, 0, req.Collection, key)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	if deleted {
		s.hub.Publish(Event{Event: "delete", Collection: req.Collection, ID: key})
	}
	client.write(map[string]any{"id": req.ID, "ok": true, "deleted": deleted})
}

func (s *Server) wsFind(client *wsClient, req wsRequest) {
	opts := docstore.QueryOptions{Search: req.Search, Limit: req.Limit, Cursor: req.Cursor}
	if req.Where != "" {
		parts := strings.SplitN(req.Where, ":", 3)
		if len(parts) == 3 {
			opts.Where = &docstore.WhereFilter{Field: parts[0], Op: parts[1], Value: parts[2]}
		}
	}
	result, err := s.opts.Store.QueryDocs(client.ctx, 0, req.Collection, opts)
	if err != nil {
		client.write(map[string]any{"id": req.ID, "ok": false, "error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(result.Documents))
	for _, doc := range result.Documents {
		items = append(items, docEnvelope(doc))
	}
	client.write(map[string]any{"id": req.ID, "ok": true, "docs": items, "next_cursor": result.NextCursor, "has_more": result.HasMore})
}
