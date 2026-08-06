package config

import "testing"

func TestProtectedModeRejectsPublicProductionWithoutPassword(t *testing.T) {
	t.Setenv("REDGE_ENV", "production")
	t.Setenv("REDGE_ADDR", "0.0.0.0:6379")
	t.Setenv("REDGE_PASSWORD", "")
	t.Setenv("REDGE_REQUIRE_AUTH", "false")
	t.Setenv("REDGE_ADMIN_ENABLED", "false")

	_, err := Load()
	if err == nil {
		t.Fatal("expected protected mode to reject public production without password")
	}
}

func TestProtectedModeAllowsPublicProductionWithPassword(t *testing.T) {
	t.Setenv("REDGE_ENV", "production")
	t.Setenv("REDGE_ADDR", "0.0.0.0:6379")
	t.Setenv("REDGE_PASSWORD", "strong-password")
	t.Setenv("REDGE_ADMIN_ENABLED", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected protected production config to load: %v", err)
	}
	if !cfg.ProtectedMode {
		t.Fatal("expected protected mode default to be true")
	}
}

func TestProtectedModeAllowsLocalDevelopmentWithoutPassword(t *testing.T) {
	t.Setenv("REDGE_ENV", "development")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")
	t.Setenv("REDGE_PASSWORD", "")
	t.Setenv("REDGE_REQUIRE_AUTH", "false")

	if _, err := Load(); err != nil {
		t.Fatalf("expected local development without password to load: %v", err)
	}
}

func TestAllowedIPsParsing(t *testing.T) {
	cfg := Config{AllowedIPs: "127.0.0.1, 10.0.0.0/8\n192.168.1.1"}
	got := cfg.GetAllowedIPs()
	want := []string{"127.0.0.1", "10.0.0.0/8", "192.168.1.1"}
	if len(got) != len(want) {
		t.Fatalf("expected %d entries, got %d: %#v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestAPITokenIsSingleAppToken(t *testing.T) {
	cfg := Config{Password: "redis-password", APIToken: "app-token"}
	if got := cfg.GetAPIToken(); got != "app-token" {
		t.Fatalf("expected API token, got %q", got)
	}
}

func TestProductionAppAPIsRequireAPIToken(t *testing.T) {
	t.Setenv("REDGE_ENV", "production")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")
	t.Setenv("REDGE_ADMIN_ENABLED", "false")
	t.Setenv("REDGE_DOCAPI_ENABLED", "true")
	t.Setenv("REDGE_API_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected production Document API without REDGE_API_TOKEN to fail")
	}
}

func TestProductionAppAPIsDoNotUseRedisPassword(t *testing.T) {
	t.Setenv("REDGE_ENV", "production")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")
	t.Setenv("REDGE_ADMIN_ENABLED", "false")
	t.Setenv("REDGE_STOREAPI_ENABLED", "true")
	t.Setenv("REDGE_PASSWORD", "redis-password")
	t.Setenv("REDGE_API_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected REDGE_PASSWORD not to satisfy HTTP app API auth")
	}
}

func TestProductionAppAPIsLoadWithAPIToken(t *testing.T) {
	t.Setenv("REDGE_ENV", "production")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")
	t.Setenv("REDGE_ADMIN_ENABLED", "false")
	t.Setenv("REDGE_DOCAPI_ENABLED", "true")
	t.Setenv("REDGE_STOREAPI_ENABLED", "true")
	t.Setenv("REDGE_API_TOKEN", "app-token")

	if _, err := Load(); err != nil {
		t.Fatalf("expected app APIs with REDGE_API_TOKEN to load: %v", err)
	}
}

func TestAppAPIsRequireHTTPServer(t *testing.T) {
	t.Setenv("REDGE_HTTP_ENABLED", "false")
	t.Setenv("REDGE_STOREAPI_ENABLED", "true")

	if _, err := Load(); err == nil {
		t.Fatal("expected Store API without HTTP server to fail")
	}
}

func TestTLSEnabledRequiresCertAndKey(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")

	_, err := Load()
	if err == nil {
		t.Fatal("expected TLS config without cert/key to fail")
	}
}

func TestTLSEnabledLoadsWithCertAndKeyPaths(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")
	t.Setenv("REDGE_TLS_CERT_FILE", "/certs/fullchain.pem")
	t.Setenv("REDGE_TLS_KEY_FILE", "/certs/privkey.pem")
	t.Setenv("REDGE_TLS_MIN_VERSION", "1.3")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected TLS config to load: %v", err)
	}
	if !cfg.TLSEnabled || cfg.TLSMinVersion != "1.3" {
		t.Fatalf("unexpected TLS config: %#v", cfg)
	}
}

func TestTLSEnabledLoadsWithCertAndKeyPEM(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")
	t.Setenv("REDGE_TLS_CERT_PEM", "-----BEGIN CERTIFICATE-----\nexample\n-----END CERTIFICATE-----")
	t.Setenv("REDGE_TLS_KEY_PEM", "-----BEGIN PRIVATE KEY-----\nexample\n-----END PRIVATE KEY-----")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected TLS PEM config to load: %v", err)
	}
	if source, err := cfg.GetTLSMaterialSource(); err != nil || source != "pem" {
		t.Fatalf("expected pem source, got %q err=%v", source, err)
	}
}

func TestTLSEnabledLoadsWithCertAndKeyBase64(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")
	t.Setenv("REDGE_TLS_CERT_B64", "Y2VydA==")
	t.Setenv("REDGE_TLS_KEY_B64", "a2V5")
	t.Setenv("REDGE_ADDR", "127.0.0.1:6379")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected TLS base64 config to load: %v", err)
	}
	if source, err := cfg.GetTLSMaterialSource(); err != nil || source != "base64" {
		t.Fatalf("expected base64 source, got %q err=%v", source, err)
	}
}

func TestTLSEnabledRejectsIncompletePEM(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")
	t.Setenv("REDGE_TLS_CERT_PEM", "-----BEGIN CERTIFICATE-----\nexample\n-----END CERTIFICATE-----")

	_, err := Load()
	if err == nil {
		t.Fatal("expected incomplete TLS PEM config to fail")
	}
}

func TestTLSMinVersionRejectsInvalidValue(t *testing.T) {
	t.Setenv("REDGE_TLS_ENABLED", "true")
	t.Setenv("REDGE_TLS_CERT_FILE", "/certs/fullchain.pem")
	t.Setenv("REDGE_TLS_KEY_FILE", "/certs/privkey.pem")
	t.Setenv("REDGE_TLS_MIN_VERSION", "1.1")

	_, err := Load()
	if err == nil {
		t.Fatal("expected invalid TLS min version to fail")
	}
}
