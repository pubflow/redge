package command

import (
	"context"
	"strings"
	"testing"
)

func TestAuthAcceptsPasswordOnly(t *testing.T) {
	router := NewRouter(RouterOptions{Password: "secret"})
	session := &Session{}

	out := string(router.Handle(context.Background(), session, []string{"AUTH", "secret"}))

	if out != "+OK\r\n" {
		t.Fatalf("expected OK, got %q", out)
	}
	if !session.Authed {
		t.Fatal("expected session to be authenticated")
	}
}

func TestAuthAcceptsDefaultUser(t *testing.T) {
	router := NewRouter(RouterOptions{Password: "secret"})
	session := &Session{}

	out := string(router.Handle(context.Background(), session, []string{"AUTH", "default", "secret"}))

	if out != "+OK\r\n" {
		t.Fatalf("expected OK, got %q", out)
	}
	if !session.Authed {
		t.Fatal("expected session to be authenticated")
	}
}

func TestAuthRejectsNonDefaultUser(t *testing.T) {
	router := NewRouter(RouterOptions{Password: "secret"})
	session := &Session{}

	out := string(router.Handle(context.Background(), session, []string{"AUTH", "alice", "secret"}))

	if !strings.Contains(out, "WRONGPASS Redge only supports the default Redis user.") {
		t.Fatalf("expected default-user error, got %q", out)
	}
	if session.Authed {
		t.Fatal("expected session to remain unauthenticated")
	}
}

func TestAuthRejectsWrongPassword(t *testing.T) {
	router := NewRouter(RouterOptions{Password: "secret"})
	session := &Session{}

	out := string(router.Handle(context.Background(), session, []string{"AUTH", "wrong"}))

	if !strings.Contains(out, "WRONGPASS invalid username-password pair or user is disabled.") {
		t.Fatalf("expected wrong password error, got %q", out)
	}
	if session.Authed {
		t.Fatal("expected session to remain unauthenticated")
	}
}

func TestAuthRequiresPasswordWhenPasswordUnset(t *testing.T) {
	router := NewRouter(RouterOptions{})
	session := &Session{}

	out := string(router.Handle(context.Background(), session, []string{"AUTH", "anything"}))

	if !strings.Contains(out, "WRONGPASS invalid username-password pair or user is disabled.") {
		t.Fatalf("expected wrong password error, got %q", out)
	}
	if session.Authed {
		t.Fatal("expected session to remain unauthenticated")
	}
}
