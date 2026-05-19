package config

import (
	"strings"
	"testing"
)

func TestLibSQLDatabaseURLSeparatesAuthToken(t *testing.T) {
	cfg := Config{DatabaseURL: "libsql://example.turso.io?authToken=secret-jwt&tls=1"}

	safeURL := cfg.GetResolvedDatabaseURL()
	if strings.Contains(safeURL, "secret-jwt") || strings.Contains(safeURL, "authToken") {
		t.Fatalf("expected safe URL without auth token, got %q", safeURL)
	}
	if !strings.Contains(safeURL, "tls=1") {
		t.Fatalf("expected non-secret query params to be preserved, got %q", safeURL)
	}
	if token := cfg.GetTursoAuthToken(); token != "secret-jwt" {
		t.Fatalf("expected extracted token, got %q", token)
	}
}

func TestLibSQLDatabaseURLUsesFallbackToken(t *testing.T) {
	cfg := Config{DatabaseURL: "libsql://example.turso.io", TursoAuthToken: "separate-token"}

	if token := cfg.GetTursoAuthToken(); token != "separate-token" {
		t.Fatalf("expected fallback token, got %q", token)
	}
	if safeURL := cfg.GetResolvedDatabaseURL(); strings.Contains(safeURL, "separate-token") {
		t.Fatalf("expected safe URL without fallback token, got %q", safeURL)
	}
}
