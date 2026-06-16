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
