package database

import (
	"strings"
	"testing"
)

func TestTrimMySQLSchemeConvertsURLStyleDSN(t *testing.T) {
	got := trimMySQLScheme("mysql://user:pass@example.com:3306/defaultdb?ssl-mode=REQUIRED")
	if !strings.HasPrefix(got, "user:pass@tcp(example.com:3306)/defaultdb?") {
		t.Fatalf("unexpected converted dsn: %s", got)
	}
	if !strings.Contains(got, "parseTime=true") {
		t.Fatalf("expected parseTime=true, got %s", got)
	}
	if !strings.Contains(got, "tls=skip-verify") {
		t.Fatalf("expected tls=skip-verify, got %s", got)
	}
	if strings.Contains(got, "ssl-mode") {
		t.Fatalf("expected ssl-mode removed, got %s", got)
	}
}

func TestTrimMySQLSchemeLeavesDriverDSNAlone(t *testing.T) {
	dsn := "user:pass@tcp(example.com:3306)/defaultdb?parseTime=true"
	if got := trimMySQLScheme(dsn); got != dsn {
		t.Fatalf("expected unchanged dsn, got %s", got)
	}
}
