package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// generateSelfSignedCert creates a self-signed certificate and key PEM file
// pair in a temp directory, returning the file paths.  Used to test the
// TLS driver's certificate loading path.
func generateSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()

	// Generate a minimal self-signed cert using the standard library.
	// We use an ECDSA P-256 key for speed.
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:         true,
	}

	cert, key, err := generateCertKey(template)
	if err != nil {
		t.Fatalf("failed to generate cert: %v", err)
	}

	certFile = filepath.Join(dir, "client.crt")
	keyFile = filepath.Join(dir, "client.key")
	if err := os.WriteFile(certFile, cert, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, key, 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// generateCertKey generates a self-signed certificate and private key in PEM
// encoding using an ECDSA P-256 key.
func generateCertKey(template *x509.Certificate) (certPEM, keyPEM []byte, err error) {
	// Use crypto/ecdsa + crypto/x509 to create a self-signed cert.
	// We avoid importing ecdsa directly by using tls.X509KeyPair round-trip
	// via a temporary in-memory approach.
	return generateCertKeyRaw(template)
}

func TestModbusTLSInitValid(t *testing.T) {
	certFile, keyFile := generateSelfSignedCert(t)

	cfg := core.DriverConfig{
		Name: "modbus-tls",
		Type: "modbus-tls",
		Settings: map[string]any{
			"host":      "192.168.1.100",
			"port":      802,
			"cert-file": certFile,
			"key-file":  keyFile,
			"ca-file":   certFile, // self-signed → same cert as CA
			"slave-id":  1,
			"timeout":   "3s",
		},
		Tags: []core.TagConfig{
			{Name: "temp", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusTLSDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusTLSDriver error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if drv.Type() != "modbus-tls" {
		t.Errorf("Type: expected modbus-tls, got %s", drv.Type())
	}

	d := drv.(*ModbusTLSDriver)
	if d.clientCert == nil {
		t.Error("clientCert should be loaded")
	}
	if d.rootCAs == nil {
		t.Error("rootCAs should be loaded")
	}
}

func TestModbusTLSInitMissingCerts(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]any
	}{
		{
			"missing cert-file",
			map[string]any{"host": "1.2.3.4", "key-file": "k", "ca-file": "c"},
		},
		{
			"missing key-file",
			map[string]any{"host": "1.2.3.4", "cert-file": "c", "ca-file": "c"},
		},
		{
			"missing ca-file",
			map[string]any{"host": "1.2.3.4", "cert-file": "c", "key-file": "k"},
		},
		{
			"missing host",
			map[string]any{"cert-file": "c", "key-file": "k", "ca-file": "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := core.DriverConfig{
				Name:     "tls-invalid",
				Type:     "modbus-tls",
				Settings: tt.settings,
				Tags:     []core.TagConfig{},
			}
			drv, err := NewModbusTLSDriver(cfg)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if err := drv.Init(context.Background(), cfg); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestModbusTLSInitBadCertFiles(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "tls-bad",
		Type: "modbus-tls",
		Settings: map[string]any{
			"host":      "1.2.3.4",
			"cert-file": "/nonexistent/cert.pem",
			"key-file":  "/nonexistent/key.pem",
			"ca-file":   "/nonexistent/ca.pem",
		},
		Tags: []core.TagConfig{},
	}

	drv, err := NewModbusTLSDriver(cfg)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err == nil {
		t.Fatal("expected error for nonexistent cert files, got nil")
	}
}

// Ensure the TLS driver satisfies the core.Driver interface at compile time.
func TestModbusTLSInterface(t *testing.T) {
	var _ core.Driver = (*ModbusTLSDriver)(nil)
	var _ core.Driver = (*ModbusNetDriver)(nil)
}

// Ensure the library's LoadCertPool and tls.LoadX509KeyPair are usable
// with our generated certs (guards against test helper bugs).
func TestModbusTLSLibHelpers(t *testing.T) {
	certFile, keyFile := generateSelfSignedCert(t)

	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	if _, err := mb.LoadCertPool(certFile); err != nil {
		t.Fatalf("LoadCertPool: %v", err)
	}
}
