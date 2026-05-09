package server

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/pubflow/redge/internal/command"
	"github.com/pubflow/redge/internal/resp"
	"go.uber.org/zap"
)

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr            string
	Password        string
	MaxRequestBytes int64
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	Router          *command.Router
	Logger          *zap.Logger
}

type TCP struct {
	opts Options
	ln   net.Listener
	mu   sync.Mutex
	done chan struct{}
}

func NewTCP(opts Options) *TCP {
	return &TCP{opts: opts, done: make(chan struct{})}
}

func (s *TCP) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.opts.Logger.Info("redis protocol listener ready", zap.String("addr", s.opts.Addr))
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return ErrClosed
			default:
				return err
			}
		}
		go s.handle(conn)
	}
}

func (s *TCP) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (s *TCP) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	session := &command.Session{Authed: s.opts.Password == ""}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.opts.ReadTimeout))
		v, err := resp.Read(reader, s.opts.MaxRequestBytes)
		if err != nil {
			return
		}
		args, err := resp.ArrayToStrings(v)
		if err != nil {
			_, _ = conn.Write(resp.Error("ERR " + err.Error()))
			continue
		}
		out := s.opts.Router.Handle(context.Background(), session, args)
		_ = conn.SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout))
		if _, err := conn.Write(out); err != nil {
			return
		}
		if len(args) > 0 && (args[0] == "QUIT" || args[0] == "quit") {
			return
		}
	}
}
