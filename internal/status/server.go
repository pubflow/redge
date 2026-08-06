package status

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pubflow/redge/internal/store"
	"go.uber.org/zap"
)

const Version = "0.1.0"

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr         string
	Store        store.Store
	DatabaseType string
	Logger       *zap.Logger
	Mount        func(*http.ServeMux)
}

type Server struct {
	opts Options
	srv  *http.Server
}

func New(opts Options) *Server {
	s := &Server{opts: opts}
	mux := http.NewServeMux()
	mux.Handle("/health", s.secure(http.HandlerFunc(s.health)))
	mux.Handle("/ready", s.secure(http.HandlerFunc(s.ready)))
	mux.Handle("/version", s.secure(http.HandlerFunc(s.version)))
	if opts.Mount != nil {
		opts.Mount(mux)
	}
	s.srv = &http.Server{Addr: opts.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return s
}

func (s *Server) ListenAndServe() error {
	s.opts.Logger.Info("http status listener ready", zap.String("addr", s.opts.Addr))
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

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		next.ServeHTTP(w, r)
	})
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

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service":  "redge",
		"version":  Version,
		"database": s.opts.DatabaseType,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
