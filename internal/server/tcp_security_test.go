package server

import (
	"net"
	"testing"
)

func TestIPAllowedEmptyAllowlistAllowsAll(t *testing.T) {
	tcp := NewTCP(Options{})
	addr := &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 12345}

	if !tcp.ipAllowed(addr) {
		t.Fatal("expected empty allowlist to allow any address")
	}
}

func TestIPAllowedExactIP(t *testing.T) {
	tcp := NewTCP(Options{AllowedIPs: []string{"127.0.0.1"}})

	if !tcp.ipAllowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345}) {
		t.Fatal("expected exact IP to be allowed")
	}
	if tcp.ipAllowed(&net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 12345}) {
		t.Fatal("expected non-matching IP to be rejected")
	}
}

func TestIPAllowedCIDR(t *testing.T) {
	tcp := NewTCP(Options{AllowedIPs: []string{"10.0.0.0/8"}})

	if !tcp.ipAllowed(&net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 12345}) {
		t.Fatal("expected CIDR IP to be allowed")
	}
	if tcp.ipAllowed(&net.TCPAddr{IP: net.ParseIP("192.168.1.10"), Port: 12345}) {
		t.Fatal("expected outside CIDR IP to be rejected")
	}
}

func TestMaxConnectionsSemaphoreConfigured(t *testing.T) {
	tcp := NewTCP(Options{MaxConnections: 1})
	if tcp.sem == nil {
		t.Fatal("expected semaphore to be configured")
	}
	tcp.sem <- struct{}{}
	select {
	case tcp.sem <- struct{}{}:
		t.Fatal("expected full semaphore to reject another connection")
	default:
	}
}
