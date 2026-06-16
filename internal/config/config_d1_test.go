package config

import (
	"strings"
	"testing"
)

func TestD1DatabaseURLSeparatesTokenAndBaseURL(t *testing.T) {
	cfg := Config{DatabaseURL: "d1://acct/db?apiToken=secret-token&baseUrl=https://example.test/client/v4"}

	safeURL := cfg.GetResolvedDatabaseURL()
	if strings.Contains(safeURL, "secret-token") || strings.Contains(safeURL, "apiToken") || strings.Contains(safeURL, "baseUrl") {
		t.Fatalf("expected safe URL without D1 token/baseUrl params, got %q", safeURL)
	}
	if safeURL != "d1://acct/db" {
		t.Fatalf("expected sanitized D1 URL, got %q", safeURL)
	}
	if token := cfg.GetD1APIToken(); token != "secret-token" {
		t.Fatalf("expected extracted D1 token, got %q", token)
	}
	if baseURL := cfg.GetD1BaseURL(); baseURL != "https://example.test/client/v4" {
		t.Fatalf("expected extracted D1 base URL, got %q", baseURL)
	}
	accountID, databaseID, err := cfg.D1Parts()
	if err != nil {
		t.Fatalf("expected D1 parts to parse: %v", err)
	}
	if accountID != "acct" || databaseID != "db" {
		t.Fatalf("expected acct/db, got %q/%q", accountID, databaseID)
	}
}

func TestD1DatabaseURLTokenAliases(t *testing.T) {
	for _, key := range []string{"apiToken", "token", "authToken", "auth_token", "jwt"} {
		t.Run(key, func(t *testing.T) {
			cfg := Config{DatabaseURL: "d1://acct/db?" + key + "=secret"}

			if token := cfg.GetD1APIToken(); token != "secret" {
				t.Fatalf("expected token from %s, got %q", key, token)
			}
			if safeURL := cfg.GetResolvedDatabaseURL(); strings.Contains(safeURL, "secret") || strings.Contains(safeURL, key) {
				t.Fatalf("expected safe URL without %s, got %q", key, safeURL)
			}
		})
	}
}

func TestD1DatabaseURLUsesFallbackTokenAndBaseURL(t *testing.T) {
	cfg := Config{
		DatabaseURL: "d1://acct/db",
		D1APIToken:  "separate-token",
		D1BaseURL:   "https://fallback.example/client/v4",
	}

	if token := cfg.GetD1APIToken(); token != "separate-token" {
		t.Fatalf("expected fallback token, got %q", token)
	}
	if baseURL := cfg.GetD1BaseURL(); baseURL != "https://fallback.example/client/v4" {
		t.Fatalf("expected fallback base URL, got %q", baseURL)
	}
	if safeURL := cfg.GetResolvedDatabaseURL(); strings.Contains(safeURL, "separate-token") {
		t.Fatalf("expected safe URL without fallback token, got %q", safeURL)
	}
}

func TestD1DatabaseURLKeepsNonSensitiveQueryParams(t *testing.T) {
	cfg := Config{DatabaseURL: "d1://acct/db?apiToken=secret&region=wnam"}

	safeURL := cfg.GetResolvedDatabaseURL()
	if strings.Contains(safeURL, "secret") || strings.Contains(safeURL, "apiToken") {
		t.Fatalf("expected safe URL without secret params, got %q", safeURL)
	}
	if !strings.Contains(safeURL, "region=wnam") {
		t.Fatalf("expected non-secret query params to be preserved, got %q", safeURL)
	}
}
