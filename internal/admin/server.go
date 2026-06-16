package admin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/status"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr         string
	Token        string
	AllowedIPs   []string
	IPCheck      bool
	ReadOnly     bool
	Store        store.Store
	Cache        *cache.Memory
	DatabaseType string
	Logger       *zap.Logger
}

type Server struct {
	opts Options
	srv  *http.Server
}

func New(opts Options) *Server {
	s := &Server{opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/ready", s.ready)
	mux.Handle("/admin/v1/info", s.auth(http.HandlerFunc(s.info)))
	mux.Handle("/admin/v1/cache", s.auth(http.HandlerFunc(s.cacheStats)))
	mux.Handle("/admin/v1/cleanup", s.auth(s.readWrite(http.HandlerFunc(s.cleanup))))
	mux.Handle("/admin/v1/keys", s.auth(http.HandlerFunc(s.keys)))
	mux.Handle("/admin/v1/keys/", s.auth(http.HandlerFunc(s.key)))
	mux.Handle("/admin/v1/zsets/", s.auth(http.HandlerFunc(s.zset)))
	s.srv = &http.Server{Addr: opts.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) ListenAndServe() error {
	s.opts.Logger.Info("admin listener ready", zap.String("addr", s.opts.Addr))
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "redge"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	stats, _ := s.opts.Store.Stats(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"service":        "redge",
		"version":        status.Version,
		"database":       s.opts.DatabaseType,
		"keys":           stats.Keys,
		"admin_readonly": s.opts.ReadOnly,
	})
}

func (s *Server) cacheStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.opts.Cache.Stats())
}

func (s *Server) cleanup(w http.ResponseWriter, r *http.Request) {
	n, err := s.opts.Store.CleanupExpired(r.Context(), 1000)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	count, ok := queryInt(w, r, "count", 100, 1, 1000)
	if !ok {
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		cursor = "0"
	}
	match := r.URL.Query().Get("match")
	if match == "" {
		match = "*"
	}
	result, err := s.opts.Store.Scan(r.Context(), db, cursor, match, count)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"db":          db,
		"match":       match,
		"count":       count,
		"cursor":      cursor,
		"next_cursor": result.Cursor,
		"keys":        result.Keys,
	})
}

func (s *Server) key(w http.ResponseWriter, r *http.Request) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	key, err := pathKey(r, "/admin/v1/keys/")
	if err != nil || key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid key"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getKey(w, r, db, key)
	case http.MethodDelete:
		if s.opts.ReadOnly {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "admin API is readonly"})
			return
		}
		n, err := s.opts.Store.Delete(r.Context(), db, key)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		s.opts.Cache.Del(cacheKey(db, key))
		writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "deleted": n})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
	}
}

func (s *Server) getKey(w http.ResponseWriter, r *http.Request, db int, key string) {
	exists, err := s.opts.Store.Exists(r.Context(), db, key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if exists == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "key not found"})
		return
	}
	ttlSeconds, ttlState, err := s.keyTTL(r.Context(), db, key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	value, err := s.opts.Store.Get(r.Context(), db, key)
	if err == nil && value != nil {
		body := map[string]any{
			"db":           db,
			"key":          key,
			"type":         "string",
			"ttl_state":    ttlState,
			"ttl_seconds":  ttlSeconds,
			"value_base64": base64.StdEncoding.EncodeToString(value.Data),
			"value_size":   len(value.Data),
			"version":      value.Version,
		}
		if utf8.Valid(value.Data) {
			body["value"] = string(value.Data)
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	if err != nil && !errors.Is(err, store.ErrWrongType) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	card, err := s.opts.Store.ZCard(r.Context(), db, key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"db":          db,
		"key":         key,
		"type":        "zset",
		"ttl_state":   ttlState,
		"ttl_seconds": ttlSeconds,
		"zcard":       card,
	})
}

func (s *Server) zset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	start, ok := queryInt64(w, r, "start", 0, -1_000_000_000, 1_000_000_000)
	if !ok {
		return
	}
	stop, ok := queryInt64(w, r, "stop", 99, -1_000_000_000, 1_000_000_000)
	if !ok {
		return
	}
	if stop >= start && stop-start+1 > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "range is limited to 1000 members"})
		return
	}
	key, err := pathKey(r, "/admin/v1/zsets/")
	if err != nil || key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid key"})
		return
	}
	members, err := s.opts.Store.ZRange(r.Context(), db, key, start, stop)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrWrongType) {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]any{"error": err.Error()})
		return
	}
	items := make([]map[string]any, 0, len(members))
	for _, member := range members {
		item := map[string]any{
			"member_base64": base64.StdEncoding.EncodeToString(member.Member),
			"score":         member.Score,
		}
		if utf8.Valid(member.Member) {
			item["member"] = string(member.Member)
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"db":      db,
		"key":     key,
		"start":   start,
		"stop":    stop,
		"members": items,
	})
}

func (s *Server) keyTTL(ctx context.Context, db int, key string) (int64, string, error) {
	ttl, exists, hasTTL, err := s.opts.Store.TTL(ctx, db, key)
	if err != nil {
		return 0, "", err
	}
	if !exists {
		return -2, "missing", nil
	}
	if !hasTTL {
		return -1, "persistent", nil
	}
	return int64(ttl / time.Second), "volatile", nil
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.IPCheck && !s.ipAllowed(clientIP(r)) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "ip not allowed"})
			return
		}
		if s.opts.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != s.opts.Token {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "admin token required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func queryInt(w http.ResponseWriter, r *http.Request, name string, fallback, min, max int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": name + " is out of range"})
		return 0, false
	}
	return n, true
}

func queryInt64(w http.ResponseWriter, r *http.Request, name string, fallback, min, max int64) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < min || n > max {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": name + " is out of range"})
		return 0, false
	}
	return n, true
}

func pathKey(r *http.Request, prefix string) (string, error) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), prefix)
	return url.PathUnescape(escaped)
}

func cacheKey(db int, key string) string {
	return strconv.Itoa(db) + ":" + key
}

func (s *Server) readWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.ReadOnly {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "admin API is readonly"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ipAllowed(ip string) bool {
	if len(s.opts.AllowedIPs) == 0 {
		return true
	}
	parsed := net.ParseIP(ip)
	for _, allowed := range s.opts.AllowedIPs {
		if allowed == ip {
			return true
		}
		if _, cidr, err := net.ParseCIDR(allowed); err == nil && parsed != nil && cidr.Contains(parsed) {
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
