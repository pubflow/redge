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
