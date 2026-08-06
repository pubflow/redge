package storeapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pubflow/redge/internal/cache"
	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr             string
	Token            string
	MaxBodyBytes     int64
	AllowedIPs       []string
	IPCheck          bool
	CORSEnabled      bool
	CORSOrigins      []string
	RateLimitEnabled bool
	RateLimitRPS     int
	RateLimitBurst   int
	Store            store.Store
	Cache            *cache.Memory
	Logger           *zap.Logger
}

type Server struct {
	opts    Options
	srv     *http.Server
	allowed []ipRule
	limiter *rateLimiter
}

func New(opts Options) *Server {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = 1 << 20
	}
	if opts.RateLimitRPS <= 0 {
		opts.RateLimitRPS = 30
	}
	if opts.RateLimitBurst <= 0 {
		opts.RateLimitBurst = 60
	}
	s := &Server{opts: opts, allowed: parseIPRules(opts.AllowedIPs)}
	if opts.RateLimitEnabled {
		s.limiter = newRateLimiter(opts.RateLimitRPS, opts.RateLimitBurst)
	}
	mux := http.NewServeMux()
	mux.Handle("/health", s.secure(http.HandlerFunc(s.health)))
	mux.Handle("/ready", s.secure(http.HandlerFunc(s.ready)))
	mux.Handle("/v1/kv", s.secure(s.auth(http.HandlerFunc(s.routeKV))))
	mux.Handle("/v1/kv/", s.secure(s.auth(http.HandlerFunc(s.routeKV))))
	mux.Handle("/v1/zsets/", s.secure(s.auth(http.HandlerFunc(s.routeZSet))))
	s.srv = &http.Server{Addr: opts.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) Mount(mux *http.ServeMux) {
	mux.Handle("/v1/kv", s.secure(s.auth(http.HandlerFunc(s.routeKV))))
	mux.Handle("/v1/kv/", s.secure(s.auth(http.HandlerFunc(s.routeKV))))
	mux.Handle("/v1/zsets/", s.secure(s.auth(http.HandlerFunc(s.routeZSet))))
}

func (s *Server) ListenAndServe() error {
	s.opts.Logger.Info("store api listener ready", zap.String("addr", s.opts.Addr))
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
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "redge-storeapi"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", err.Error())
		return
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
			writeError(w, http.StatusForbidden, "ip_not_allowed", "ip not allowed")
			return
		}
		if s.limiter != nil && !s.limiter.allow(rateLimitKey(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
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
		writeError(w, http.StatusForbidden, "origin_not_allowed", "origin not allowed")
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Vary", "Origin")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	return true
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != s.opts.Token {
				writeError(w, http.StatusUnauthorized, "unauthorized", "store api token required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) routeKV(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/kv" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		s.scan(w, r)
		return
	}
	raw := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/kv/")
	switch {
	case raw == "batch/get":
		s.batchGet(w, r)
	case raw == "batch/set":
		s.batchSet(w, r)
	case raw == "batch/exists":
		s.batchExists(w, r)
	case raw == "batch/delete":
		s.batchDelete(w, r)
	default:
		s.keyRoute(w, r, raw)
	}
}

func (s *Server) keyRoute(w http.ResponseWriter, r *http.Request, raw string) {
	key, action, ok := splitKeyAction(raw, "getdel", "incr", "expire", "persist", "ttl", "type", "exists")
	if !ok || key == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "invalid key")
		return
	}
	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			s.get(w, r, key)
		case http.MethodPut:
			s.set(w, r, key)
		case http.MethodDelete:
			s.delete(w, r, key)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case "getdel":
		requireMethod(w, r, http.MethodPost, func() { s.getdel(w, r, key) })
	case "incr":
		requireMethod(w, r, http.MethodPost, func() { s.incr(w, r, key) })
	case "expire":
		requireMethod(w, r, http.MethodPost, func() { s.expire(w, r, key) })
	case "persist":
		requireMethod(w, r, http.MethodPost, func() { s.persist(w, r, key) })
	case "ttl":
		requireMethod(w, r, http.MethodGet, func() { s.ttl(w, r, key) })
	case "type":
		requireMethod(w, r, http.MethodGet, func() { s.keyType(w, r, key) })
	case "exists":
		requireMethod(w, r, http.MethodGet, func() { s.keyExists(w, r, key) })
	}
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	v, err := s.opts.Store.Get(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if v == nil {
		writeError(w, http.StatusNotFound, "not_found", "key not found")
		return
	}
	writeJSON(w, http.StatusOK, s.valueEnvelope(r.Context(), db, key, v))
}

func (s *Server) set(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req writeValueRequest
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	value, ok := requestBytes(w, req.Value, req.ValueBase64)
	if !ok {
		return
	}
	opts := setOptionsFromRequest(r, req)
	old, wrote, err := s.opts.Store.Set(r.Context(), db, key, value, opts)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	if opts.Get {
		if old == nil {
			writeJSON(w, http.StatusOK, map[string]any{"key": key, "written": wrote, "previous": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "written": wrote, "previous": s.valueEnvelope(r.Context(), db, key, old)})
		return
	}
	if !wrote {
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "written": false})
		return
	}
	v, _ := s.opts.Store.Get(r.Context(), db, key)
	writeJSON(w, http.StatusOK, s.valueEnvelope(r.Context(), db, key, v))
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	n, err := s.opts.Store.Delete(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "deleted": n})
}

func (s *Server) keyType(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	typ, err := s.opts.Store.Type(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "type": typ})
}

func (s *Server) keyExists(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	n, err := s.opts.Store.Exists(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "exists": n > 0})
}

func (s *Server) batchExists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if len(req.Keys) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "keys is limited to 1000 items")
		return
	}
	items := make([]map[string]any, 0, len(req.Keys))
	var count int64
	for _, key := range req.Keys {
		n, err := s.opts.Store.Exists(r.Context(), db, key)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		exists := n > 0
		if exists {
			count++
		}
		items = append(items, map[string]any{"key": key, "exists": exists})
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "count": count, "items": items})
}

func (s *Server) batchDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if len(req.Keys) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "keys is limited to 1000 items")
		return
	}
	n, err := s.opts.Store.Delete(r.Context(), db, req.Keys...)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	for _, key := range req.Keys {
		s.delCache(db, key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "deleted": n})
}

func (s *Server) batchGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if len(req.Keys) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "keys is limited to 1000 items")
		return
	}
	values, err := s.opts.Store.MGet(r.Context(), db, req.Keys...)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	items := make([]any, 0, len(req.Keys))
	for i, key := range req.Keys {
		if values[i] == nil {
			items = append(items, map[string]any{"key": key, "missing": true})
			continue
		}
		items = append(items, s.valueEnvelope(r.Context(), db, key, values[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "items": items})
}

func (s *Server) batchSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Items []writeBatchItem `json:"items"`
		TTL   int64            `json:"ttl_seconds"`
		NX    bool             `json:"nx"`
		XX    bool             `json:"xx"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if len(req.Items) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "items is limited to 1000 items")
		return
	}
	pairs := make([]store.KeyValue, 0, len(req.Items))
	for _, item := range req.Items {
		if item.Key == "" {
			writeError(w, http.StatusBadRequest, "invalid_key", "invalid key")
			return
		}
		value, ok := requestBytes(w, item.Value, item.ValueBase64)
		if !ok {
			return
		}
		pairs = append(pairs, store.KeyValue{Key: item.Key, Value: value})
	}
	opts := store.SetOptions{NX: req.NX, XX: req.XX}
	if req.TTL > 0 {
		opts.TTL = time.Duration(req.TTL) * time.Second
	}
	if err := s.opts.Store.MSet(r.Context(), db, pairs, opts); err != nil {
		writeStoreError(w, err)
		return
	}
	for _, pair := range pairs {
		s.delCache(db, pair.Key)
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "written": len(pairs)})
}

func (s *Server) getdel(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	v, deleted, err := s.opts.Store.GetDel(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	if v == nil {
		writeError(w, http.StatusNotFound, "not_found", "key not found")
		return
	}
	out := s.valueEnvelope(r.Context(), db, key, v)
	out["deleted"] = deleted
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) incr(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Delta int64 `json:"delta"`
		By    int64 `json:"by"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if req.By != 0 {
		req.Delta = req.By
	}
	if req.Delta == 0 {
		req.Delta = 1
	}
	n, err := s.opts.Store.IncrBy(r.Context(), db, key, req.Delta)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "value": n})
}

func (s *Server) expire(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		TTL int64 `json:"ttl_seconds"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if req.TTL <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "ttl_seconds must be greater than 0")
		return
	}
	changed, err := s.opts.Store.Expire(r.Context(), db, key, time.Duration(req.TTL)*time.Second)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "changed": changed})
}

func (s *Server) persist(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	changed, err := s.opts.Store.Persist(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "changed": changed})
}

func (s *Server) ttl(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	ttl, exists, hasTTL, err := s.opts.Store.TTL(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ttlEnvelope(db, key, ttl, exists, hasTTL))
}

func (s *Server) scan(w http.ResponseWriter, r *http.Request) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	limit, ok := queryInt(w, r, "limit", 100, 1, 1000)
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
	result, err := s.opts.Store.Scan(r.Context(), db, cursor, match, limit)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"db":          db,
		"match":       match,
		"limit":       limit,
		"cursor":      cursor,
		"next_cursor": result.Cursor,
		"keys":        result.Keys,
	})
}

func (s *Server) routeZSet(w http.ResponseWriter, r *http.Request) {
	key, action, tail, ok := parseZSetPath(r.URL.EscapedPath())
	if !ok || key == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "invalid key")
		return
	}
	switch action {
	case "members":
		if tail == "" {
			switch r.Method {
			case http.MethodPost:
				s.zadd(w, r, key)
			case http.MethodGet:
				s.zrange(w, r, key)
			default:
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			}
			return
		}
		if tail == "remove" {
			requireMethod(w, r, http.MethodPost, func() { s.zremMany(w, r, key) })
			return
		}
		requireMethod(w, r, http.MethodDelete, func() { s.zrem(w, r, key, tail) })
	case "byscore":
		switch r.Method {
		case http.MethodGet:
			s.zrangeByScore(w, r, key)
		case http.MethodDelete:
			s.zremByScore(w, r, key)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
	case "score":
		requireMethod(w, r, http.MethodGet, func() { s.zscore(w, r, key, tail) })
	case "count":
		requireMethod(w, r, http.MethodGet, func() { s.zcount(w, r, key) })
	case "card":
		requireMethod(w, r, http.MethodGet, func() { s.zcard(w, r, key) })
	case "incrby":
		requireMethod(w, r, http.MethodPost, func() { s.zincrby(w, r, key) })
	case "popmin":
		requireMethod(w, r, http.MethodPost, func() { s.zpop(w, r, key, false) })
	case "popmax":
		requireMethod(w, r, http.MethodPost, func() { s.zpop(w, r, key, true) })
	default:
		writeError(w, http.StatusNotFound, "not_found", "not found")
	}
}

func (s *Server) zadd(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Score        float64 `json:"score"`
		Member       string  `json:"member"`
		MemberBase64 string  `json:"member_base64"`
		Members      []struct {
			Score        float64 `json:"score"`
			Member       string  `json:"member"`
			MemberBase64 string  `json:"member_base64"`
		} `json:"members"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	type scoredMember struct {
		score  float64
		member []byte
	}
	entries := make([]scoredMember, 0)
	if len(req.Members) > 0 {
		if len(req.Members) > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "members is limited to 1000 items")
			return
		}
		for _, item := range req.Members {
			member, ok := requestBytes(w, item.Member, item.MemberBase64)
			if !ok {
				return
			}
			if len(member) == 0 && item.Member == "" && item.MemberBase64 == "" {
				writeError(w, http.StatusBadRequest, "invalid_request", "member is required")
				return
			}
			entries = append(entries, scoredMember{score: item.Score, member: member})
		}
	} else {
		member, ok := requestBytes(w, req.Member, req.MemberBase64)
		if !ok {
			return
		}
		entries = append(entries, scoredMember{score: req.Score, member: member})
	}
	var added int64
	for _, entry := range entries {
		n, err := s.opts.Store.ZAdd(r.Context(), db, key, entry.score, entry.member)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		added += n
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "added": added})
}

func (s *Server) zremMany(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Members         []string `json:"members"`
		MembersBase64   []string `json:"members_base64"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	members := make([][]byte, 0, len(req.Members)+len(req.MembersBase64))
	for _, member := range req.Members {
		members = append(members, []byte(member))
	}
	for _, encoded := range req.MembersBase64 {
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base64", "members_base64 is invalid")
			return
		}
		members = append(members, data)
	}
	if len(members) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "members is required")
		return
	}
	if len(members) > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "members is limited to 1000 items")
		return
	}
	removed, err := s.opts.Store.ZRem(r.Context(), db, key, members...)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "removed": removed})
}

func (s *Server) zrange(w http.ResponseWriter, r *http.Request, key string) {
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
		writeError(w, http.StatusBadRequest, "invalid_request", "range is limited to 1000 members")
		return
	}
	members, err := s.opts.Store.ZRange(r.Context(), db, key, start, stop)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "start": start, "stop": stop, "members": zitems(members)})
}

func (s *Server) zrangeByScore(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	min, max, ok := scoreBoundsFromQuery(w, r)
	if !ok {
		return
	}
	offset, ok := queryInt64(w, r, "offset", 0, 0, 1_000_000_000)
	if !ok {
		return
	}
	limit, ok := queryInt64(w, r, "limit", 100, 1, 1000)
	if !ok {
		return
	}
	rev := parseBool(r.URL.Query().Get("rev"))
	members, err := s.opts.Store.ZRangeByScore(r.Context(), db, key, min, max, offset, limit, rev)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "members": zitems(members)})
}

func (s *Server) zrem(w http.ResponseWriter, r *http.Request, key, rawMember string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	member, err := url.PathUnescape(rawMember)
	if err != nil || member == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid member")
		return
	}
	removed, err := s.opts.Store.ZRem(r.Context(), db, key, []byte(member))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "removed": removed})
}

func (s *Server) zremByScore(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	min, max, ok := scoreBoundsFromQuery(w, r)
	if !ok {
		return
	}
	removed, err := s.opts.Store.ZRemRangeByScore(r.Context(), db, key, min, max)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "removed": removed})
}

func (s *Server) zscore(w http.ResponseWriter, r *http.Request, key, rawMember string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	member, err := url.PathUnescape(rawMember)
	if err != nil || member == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid member")
		return
	}
	score, found, err := s.opts.Store.ZScore(r.Context(), db, key, []byte(member))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "member not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "member": member, "score": score})
}

func (s *Server) zcount(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	min, max, ok := scoreBoundsFromQuery(w, r)
	if !ok {
		return
	}
	n, err := s.opts.Store.ZCount(r.Context(), db, key, min, max)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "count": n})
}

func (s *Server) zcard(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	n, err := s.opts.Store.ZCard(r.Context(), db, key)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "card": n})
}

func (s *Server) zincrby(w http.ResponseWriter, r *http.Request, key string) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Member       string  `json:"member"`
		MemberBase64 string  `json:"member_base64"`
		By           float64 `json:"by"`
	}
	if !decodeBody(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	member, ok := requestBytes(w, req.Member, req.MemberBase64)
	if !ok {
		return
	}
	if len(member) == 0 && req.Member == "" && req.MemberBase64 == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "member is required")
		return
	}
	score, err := s.opts.Store.ZIncrBy(r.Context(), db, key, member, req.By)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	out := map[string]any{"db": db, "key": key, "score": score}
	if utf8.Valid(member) {
		out["member"] = string(member)
	} else {
		out["member_base64"] = base64.StdEncoding.EncodeToString(member)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) zpop(w http.ResponseWriter, r *http.Request, key string, max bool) {
	db, ok := queryInt(w, r, "db", 0, 0, 1024)
	if !ok {
		return
	}
	var req struct {
		Count int64 `json:"count"`
	}
	if !decodeBodyOptional(w, r, s.opts.MaxBodyBytes, &req) {
		return
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 1000 {
		writeError(w, http.StatusBadRequest, "invalid_request", "count is limited to 1000")
		return
	}
	var (
		members []store.ZMember
		err     error
	)
	if max {
		members, err = s.opts.Store.ZPopMax(r.Context(), db, key, req.Count)
	} else {
		members, err = s.opts.Store.ZPopMin(r.Context(), db, key, req.Count)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.delCache(db, key)
	writeJSON(w, http.StatusOK, map[string]any{"db": db, "key": key, "members": zitems(members)})
}

type writeValueRequest struct {
	Value       string `json:"value"`
	ValueBase64 string `json:"value_base64"`
	TTL         int64  `json:"ttl_seconds"`
	NX          bool   `json:"nx"`
	XX          bool   `json:"xx"`
	KeepTTL     bool   `json:"keep_ttl"`
	Get         bool   `json:"get"`
}

type writeBatchItem struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	ValueBase64 string `json:"value_base64"`
}

func requestBytes(w http.ResponseWriter, value, valueBase64 string) ([]byte, bool) {
	if valueBase64 != "" {
		data, err := base64.StdEncoding.DecodeString(valueBase64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_base64", "value_base64 is invalid")
			return nil, false
		}
		return data, true
	}
	return []byte(value), true
}

func setOptionsFromRequest(r *http.Request, req writeValueRequest) store.SetOptions {
	opts := store.SetOptions{NX: req.NX, XX: req.XX, KeepTTL: req.KeepTTL, Get: req.Get}
	if req.TTL > 0 {
		opts.TTL = time.Duration(req.TTL) * time.Second
	}
	if raw := r.URL.Query().Get("ttl"); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
			opts.TTL = time.Duration(n) * time.Second
		}
	}
	if parseBool(r.URL.Query().Get("nx")) {
		opts.NX = true
	}
	if parseBool(r.URL.Query().Get("xx")) {
		opts.XX = true
	}
	return opts
}

func (s *Server) valueEnvelope(ctx context.Context, db int, key string, value *store.Value) map[string]any {
	body := map[string]any{
		"db":           db,
		"key":          key,
		"type":         "string",
		"value_base64": base64.StdEncoding.EncodeToString(value.Data),
		"value_size":   len(value.Data),
		"version":      value.Version,
	}
	if utf8.Valid(value.Data) {
		body["value"] = string(value.Data)
	}
	ttl, exists, hasTTL, err := s.opts.Store.TTL(ctx, db, key)
	if err == nil {
		for k, v := range ttlEnvelope(db, key, ttl, exists, hasTTL) {
			body[k] = v
		}
	}
	return body
}

func ttlEnvelope(db int, key string, ttl time.Duration, exists, hasTTL bool) map[string]any {
	state := "volatile"
	seconds := int64(ttl / time.Second)
	if !exists {
		state = "missing"
		seconds = -2
	} else if !hasTTL {
		state = "persistent"
		seconds = -1
	}
	return map[string]any{"db": db, "key": key, "ttl_state": state, "ttl_seconds": seconds}
}

func zitems(members []store.ZMember) []map[string]any {
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
	return items
}

func splitKeyAction(raw string, actions ...string) (string, string, bool) {
	for _, action := range actions {
		suffix := "/" + action
		if strings.HasSuffix(raw, suffix) {
			key, err := url.PathUnescape(strings.TrimSuffix(raw, suffix))
			return key, action, err == nil
		}
	}
	key, err := url.PathUnescape(raw)
	return key, "", err == nil
}

func parseZSetPath(path string) (key, action, tail string, ok bool) {
	raw := strings.TrimPrefix(path, "/v1/zsets/")
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return "", "", "", false
	}
	key, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", "", false
	}
	action = parts[1]
	if len(parts) > 2 {
		tail = strings.Join(parts[2:], "/")
	}
	return key, action, tail, true
}

func scoreBoundsFromQuery(w http.ResponseWriter, r *http.Request) (store.ScoreBound, store.ScoreBound, bool) {
	min, err := parseScoreBound(r.URL.Query().Get("min"), false)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_score_bound", "invalid min score")
		return store.ScoreBound{}, store.ScoreBound{}, false
	}
	max, err := parseScoreBound(r.URL.Query().Get("max"), true)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_score_bound", "invalid max score")
		return store.ScoreBound{}, store.ScoreBound{}, false
	}
	return min, max, true
}

func parseScoreBound(raw string, upper bool) (store.ScoreBound, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if upper {
			return store.ScoreBound{Infinite: 1}, nil
		}
		return store.ScoreBound{Infinite: -1}, nil
	}
	exclusive := strings.HasPrefix(raw, "(")
	raw = strings.TrimPrefix(raw, "(")
	switch strings.ToLower(raw) {
	case "-inf":
		return store.ScoreBound{Infinite: -1, Exclusive: exclusive}, nil
	case "+inf", "inf":
		return store.ScoreBound{Infinite: 1, Exclusive: exclusive}, nil
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(n) {
		return store.ScoreBound{}, fmt.Errorf("invalid score")
	}
	return store.ScoreBound{Value: n, Exclusive: exclusive}, nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string, fn func()) {
	if r.Method != method {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	fn()
}

func decodeBody(w http.ResponseWriter, r *http.Request, maxBytes int64, out any) bool {
	defer r.Body.Close()
	reader := http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(reader)
	if err := dec.Decode(out); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return false
	}
	return true
}

func decodeBodyOptional(w http.ResponseWriter, r *http.Request, maxBytes int64, out any) bool {
	defer r.Body.Close()
	reader := http.MaxBytesReader(nil, r.Body, maxBytes)
	dec := json.NewDecoder(reader)
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid JSON body")
		return false
	}
	return true
}

func queryInt(w http.ResponseWriter, r *http.Request, name string, fallback, min, max int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		writeError(w, http.StatusBadRequest, "invalid_request", name+" is out of range")
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
		writeError(w, http.StatusBadRequest, "invalid_request", name+" is out of range")
		return 0, false
	}
	return n, true
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrWrongType):
		writeError(w, http.StatusConflict, "wrong_type", err.Error())
	case errors.Is(err, store.ErrNotInt):
		writeError(w, http.StatusBadRequest, "not_integer", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"code": code, "error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) delCache(db int, keys ...string) {
	if s.opts.Cache == nil {
		return
	}
	for _, key := range keys {
		s.opts.Cache.Del(strconv.Itoa(db) + ":" + key)
	}
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

func parseBool(raw string) bool {
	v, _ := strconv.ParseBool(raw)
	return v
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
