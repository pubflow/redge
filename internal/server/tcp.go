package server

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pubflow/redge/internal/command"
	"github.com/pubflow/redge/internal/resp"
	"go.uber.org/zap"
)

var ErrClosed = errors.New("server closed")

type Options struct {
	Addr             string
	Password         string
	MaxRequestBytes  int64
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	AllowedIPs       []string
	MaxConnections   int
	AuthFailureDelay time.Duration
	Router           *command.Router
	Logger           *zap.Logger
}

type TCP struct {
	opts    Options
	ln      net.Listener
	allowed []ipRule
	sem     chan struct{}
	mu      sync.Mutex
	done    chan struct{}
}

func NewTCP(opts Options) *TCP {
	t := &TCP{opts: opts, allowed: parseIPRules(opts.AllowedIPs), done: make(chan struct{})}
	if opts.MaxConnections > 0 {
		t.sem = make(chan struct{}, opts.MaxConnections)
	}
	return t
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
		if !s.ipAllowed(conn.RemoteAddr()) {
			s.opts.Logger.Warn("redis connection rejected by ip allowlist", zap.String("remote_addr", conn.RemoteAddr().String()))
			_ = conn.Close()
			continue
		}
		if s.sem != nil {
			select {
			case s.sem <- struct{}{}:
			default:
				s.opts.Logger.Warn("redis connection rejected by max connections", zap.String("remote_addr", conn.RemoteAddr().String()))
				_ = conn.Close()
				continue
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
	if s.sem != nil {
		defer func() { <-s.sem }()
	}
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
		if s.opts.AuthFailureDelay > 0 && len(out) > 0 && strings.HasPrefix(string(out), "-WRONGPASS ") {
			time.Sleep(s.opts.AuthFailureDelay)
		}
		_ = conn.SetWriteDeadline(time.Now().Add(s.opts.WriteTimeout))
		if _, err := conn.Write(out); err != nil {
			return
		}
		if len(args) > 0 && (args[0] == "QUIT" || args[0] == "quit") {
			return
		}
	}
}

func (s *TCP) ipAllowed(addr net.Addr) bool {
	if len(s.allowed) == 0 {
		return true
	}
	host := addr.String()
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		host = tcpAddr.IP.String()
	} else if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	for _, rule := range s.allowed {
		if rule.exact != "" && rule.exact == host {
			return true
		}
		if rule.ip != nil && ip != nil && rule.ip.Equal(ip) {
			return true
		}
		if rule.cidr != nil && ip != nil && rule.cidr.Contains(ip) {
			return true
		}
	}
	return false
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
