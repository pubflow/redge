package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pubflow/redge/internal/config"
)

func TestLoadRedisTLSCertificateFromPEMEnv(t *testing.T) {
	certPEM, keyPEM := newTestTLSPEM(t)
	cfg := &config.Config{
		TLSEnabled:    true,
		TLSCertPEM:    string(certPEM),
		TLSKeyPEM:     string(keyPEM),
		TLSMinVersion: "1.2",
	}

	cert, err := loadRedisTLSCertificate(cfg)
	if err != nil {
		t.Fatalf("load TLS cert from PEM env: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatal("expected parsed certificate chain")
	}
}

func TestLoadRedisTLSCertificateFromEscapedPEMEnv(t *testing.T) {
	certPEM, keyPEM := newTestTLSPEM(t)
	cfg := &config.Config{
		TLSEnabled:    true,
		TLSCertPEM:    escapeNewlines(string(certPEM)),
		TLSKeyPEM:     escapeNewlines(string(keyPEM)),
		TLSMinVersion: "1.2",
	}

	if _, err := loadRedisTLSCertificate(cfg); err != nil {
		t.Fatalf("load TLS cert from escaped PEM env: %v", err)
	}
}

func TestLoadRedisTLSCertificateFromBase64Env(t *testing.T) {
	certPEM, keyPEM := newTestTLSPEM(t)
	cfg := &config.Config{
		TLSEnabled:    true,
		TLSCertB64:    base64.StdEncoding.EncodeToString(certPEM),
		TLSKeyB64:     base64.StdEncoding.EncodeToString(keyPEM),
		TLSMinVersion: "1.2",
	}

	if _, err := loadRedisTLSCertificate(cfg); err != nil {
		t.Fatalf("load TLS cert from base64 env: %v", err)
	}
}

func TestLoadRedisTLSCertificateFromFiles(t *testing.T) {
	certPEM, keyPEM := newTestTLSPEM(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "fullchain.pem")
	keyFile := filepath.Join(dir, "privkey.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert file: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	cfg := &config.Config{
		TLSEnabled:    true,
		TLSCertFile:   certFile,
		TLSKeyFile:    keyFile,
		TLSMinVersion: "1.2",
	}

	if _, err := loadRedisTLSCertificate(cfg); err != nil {
		t.Fatalf("load TLS cert from files: %v", err)
	}
}

func TestNewRedisTLSConfigUsesBase64Env(t *testing.T) {
	certPEM, keyPEM := newTestTLSPEM(t)
	cfg := &config.Config{
		TLSEnabled:    true,
		TLSCertB64:    base64.StdEncoding.EncodeToString(certPEM),
		TLSKeyB64:     base64.StdEncoding.EncodeToString(keyPEM),
		TLSMinVersion: "1.3",
	}

	tlsConfig, err := newRedisTLSConfig(cfg)
	if err != nil {
		t.Fatalf("new Redis TLS config: %v", err)
	}
	if tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3 min version, got %d", tlsConfig.MinVersion)
	}
	if len(tlsConfig.Certificates) != 1 {
		t.Fatalf("expected one certificate, got %d", len(tlsConfig.Certificates))
	}
}

func newTestTLSPEM(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	return serial
}

func escapeNewlines(raw string) string {
	return strings.ReplaceAll(raw, "\n", `\n`)
}
