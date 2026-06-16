package d1store

import (
	"strings"
	"testing"

	"github.com/pubflow/redge/internal/config"
	"go.uber.org/zap"
)

func TestNewAcceptsD1TokenFromDatabaseURL(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL:       "d1://acct/db?apiToken=secret-token",
		D1RetryMax:        3,
		D1RetryMinBackoff: 0,
		D1RetryMaxBackoff: 0,
	}

	st, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("expected D1 store to initialize with URL token: %v", err)
	}
	if st == nil {
		t.Fatal("expected D1 store")
	}
}

func TestNewAcceptsD1SeparateToken(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL:       "d1://acct/db",
		D1APIToken:        "separate-token",
		D1RetryMax:        3,
		D1RetryMinBackoff: 0,
		D1RetryMaxBackoff: 0,
	}

	st, err := New(cfg, zap.NewNop())
	if err != nil {
		t.Fatalf("expected D1 store to initialize with separate token: %v", err)
	}
	if st == nil {
		t.Fatal("expected D1 store")
	}
}

func TestNewMissingD1TokenErrorDoesNotLeakDatabaseURL(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL:       "d1://acct/db?region=wnam",
		D1RetryMax:        3,
		D1RetryMinBackoff: 0,
		D1RetryMaxBackoff: 0,
	}

	_, err := New(cfg, zap.NewNop())
	if err == nil {
		t.Fatal("expected missing D1 token error")
	}
	msg := err.Error()
	if strings.Contains(msg, "acct") || strings.Contains(msg, "db") || strings.Contains(msg, "region") {
		t.Fatalf("expected error not to include database URL details, got %q", msg)
	}
}
