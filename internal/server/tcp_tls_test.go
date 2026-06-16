package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/command"
	"github.com/pubflow/redge/internal/resp"
	"go.uber.org/zap"
)

func TestTLSListenerAcceptsTLSClient(t *testing.T) {
	cert := newTestTLSCertificate(t)
	addr := freeLocalAddr(t)
	tcp := NewTCP(Options{
		Addr:            addr,
		MaxRequestBytes: 1024,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Router:          command.NewRouter(command.RouterOptions{}),
		Logger:          zap.NewNop(),
	})
	runTestTCP(t, tcp)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(resp.Array(resp.Bulk([]byte("PING")))); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if got := string(buf[:n]); got != "+PONG\r\n" {
		t.Fatalf("expected PONG, got %q", got)
	}
}

func TestTLSListenerRejectsPlaintextClient(t *testing.T) {
	cert := newTestTLSCertificate(t)
	addr := freeLocalAddr(t)
	tcp := NewTCP(Options{
		Addr:            addr,
		MaxRequestBytes: 1024,
		ReadTimeout:     200 * time.Millisecond,
		WriteTimeout:    200 * time.Millisecond,
		TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		Router:          command.NewRouter(command.RouterOptions{}),
		Logger:          zap.NewNop(),
	})
	runTestTCP(t, tcp)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(resp.Array(resp.Bulk([]byte("PING")))); err != nil {
		t.Fatalf("write plaintext ping: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err == nil && n > 0 && string(buf[:n]) == "+PONG\r\n" {
		t.Fatal("expected plaintext client not to receive PONG from TLS listener")
	}
}

func TestPlaintextListenerStillWorksWhenTLSDisabled(t *testing.T) {
	addr := freeLocalAddr(t)
	tcp := NewTCP(Options{
		Addr:            addr,
		MaxRequestBytes: 1024,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		Router:          command.NewRouter(command.RouterOptions{}),
		Logger:          zap.NewNop(),
	})
	runTestTCP(t, tcp)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(resp.Array(resp.Bulk([]byte("PING")))); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if got := string(buf[:n]); got != "+PONG\r\n" {
		t.Fatalf("expected PONG, got %q", got)
	}
}

func runTestTCP(t *testing.T, tcp *TCP) {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- tcp.ListenAndServe() }()
	t.Cleanup(func() {
		_ = tcp.Shutdown(context.Background())
		select {
		case err := <-errCh:
			if err != nil && err != ErrClosed {
				t.Fatalf("tcp server stopped with error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("tcp server did not stop")
		}
	})
	waitForPort(t, tcp.opts.Addr)
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", addr)
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func newTestTLSCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}
