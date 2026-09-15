package mqtt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// generateCertFiles creates a self-signed certificate and matching private
// key as PEM files in a temp directory, returning their paths.  The
// certificate is marked as a CA and valid for both client and server auth,
// so it can serve as the CA file, the client cert, and the client key in
// tests.
func generateCertFiles(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:         true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certFile = filepath.Join(dir, "client.crt")
	keyFile = filepath.Join(dir, "client.key")
	if err := os.WriteFile(certFile,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile,
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// newTLSConfig builds a core.TransportConfig with the given broker and TLS
// file settings, suitable for driving MQTTTransport.Init in tests.
func newTLSConfig(name, broker string, tls map[string]any) core.TransportConfig {
	settings := map[string]any{
		"broker": broker,
	}
	for k, v := range tls {
		settings[k] = v
	}
	return core.TransportConfig{
		Name:     name,
		Type:     "mqtt",
		Settings: settings,
	}
}

// initTransport builds and inits an MQTTTransport from cfg, returning the
// concrete transport so tests can inspect unexported TLS state.
func initTransport(t *testing.T, cfg core.TransportConfig) *MQTTTransport {
	t.Helper()
	tr, err := NewMQTTTransport(cfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport: %v", err)
	}
	if err := tr.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return tr.(*MQTTTransport)
}

// TestMQTTTLSBackwardCompatPlain ensures a plaintext broker with no TLS
// settings builds no TLS config — existing non-TLS setups are unaffected.
func TestMQTTTLSBackwardCompatPlain(t *testing.T) {
	for _, broker := range []string{
		"tcp://broker.emqx.io:1883",
		"mqtt://broker.emqx.io:1883",
		"broker.emqx.io:1883", // schemeless, normalized by paho to tcp://
		"192.168.1.5:1883",    // schemeless IP:port, normalized by paho to tcp://
	} {
		t.Run(broker, func(t *testing.T) {
			tr := initTransport(t, newTLSConfig("plain", broker, nil))
			if tr.tlsConfig != nil {
				t.Fatalf("expected no TLS config for plaintext broker %q, got %+v", broker, tr.tlsConfig)
			}
		})
	}
}

// TestMQTTTLSSchemeNoFiles verifies an mqtts:// broker with no cert files
// builds a TLS config with ServerName set and no custom CA or client cert
// (system roots are used for server verification).
func TestMQTTTLSSchemeNoFiles(t *testing.T) {
	tr := initTransport(t, newTLSConfig("tls", "mqtts://broker.example.com:8883", nil))
	if tr.tlsConfig == nil {
		t.Fatal("expected TLS config for mqtts:// broker")
	}
	if got := tr.tlsConfig.ServerName; got != "broker.example.com" {
		t.Errorf("ServerName: expected broker.example.com, got %q", got)
	}
	if tr.tlsConfig.RootCAs != nil {
		t.Error("RootCAs should be nil when no tls-ca-file is set (system roots)")
	}
	if len(tr.tlsConfig.Certificates) != 0 {
		t.Error("Certificates should be empty when no client cert is set")
	}
}

// TestMQTTTLSWithCA verifies an mqtts:// broker with a CA file loads the
// CA pool for server verification.
func TestMQTTTLSWithCA(t *testing.T) {
	certFile, _ := generateCertFiles(t)
	tr := initTransport(t, newTLSConfig("tls-ca", "mqtts://broker.example.com:8883", map[string]any{
		"tls-ca-file": certFile,
	}))
	if tr.tlsConfig == nil {
		t.Fatal("expected TLS config")
	}
	if tr.tlsConfig.RootCAs == nil {
		t.Error("RootCAs should be loaded from tls-ca-file")
	}
	if len(tr.tlsConfig.Certificates) != 0 {
		t.Error("Certificates should be empty without client cert")
	}
}

// TestMQTTMTLS verifies mutual TLS: an mqtts:// broker with CA + client
// cert + key loads both the CA pool and the client certificate.
func TestMQTTMTLS(t *testing.T) {
	certFile, keyFile := generateCertFiles(t)
	tr := initTransport(t, newTLSConfig("mtls", "mqtts://broker.example.com:8883", map[string]any{
		"tls-ca-file":   certFile,
		"tls-cert-file": certFile,
		"tls-key-file":  keyFile,
	}))
	if tr.tlsConfig == nil {
		t.Fatal("expected TLS config")
	}
	if tr.tlsConfig.RootCAs == nil {
		t.Error("RootCAs should be loaded")
	}
	if len(tr.tlsConfig.Certificates) != 1 {
		t.Fatalf("Certificates: expected 1, got %d", len(tr.tlsConfig.Certificates))
	}
	if got := tr.tlsConfig.ServerName; got != "broker.example.com" {
		t.Errorf("ServerName: expected broker.example.com, got %q", got)
	}
}

// TestMQTTTLSAllSchemes verifies every TLS scheme paho recognises triggers
// TLS config building.
func TestMQTTTLSAllSchemes(t *testing.T) {
	for _, scheme := range []string{"ssl", "tls", "mqtts", "mqtt+ssl", "tcps", "wss"} {
		broker := scheme + "://broker.example.com:8883"
		t.Run(scheme, func(t *testing.T) {
			tr := initTransport(t, newTLSConfig("s", broker, nil))
			if tr.tlsConfig == nil {
				t.Fatalf("expected TLS config for %s:// broker", scheme)
			}
		})
	}
}

// TestMQTTTLSFilesOnPlaintextBroker verifies that setting TLS files on a
// plaintext broker still builds a config (and logs a warning) rather than
// silently dropping it — the user explicitly asked for tlsCAFile to trigger
// TLS handling.
func TestMQTTTLSFilesOnPlaintextBroker(t *testing.T) {
	certFile, keyFile := generateCertFiles(t)
	tr := initTransport(t, newTLSConfig("plain-tlsfiles", "tcp://broker.example.com:1883", map[string]any{
		"tls-ca-file":   certFile,
		"tls-cert-file": certFile,
		"tls-key-file":  keyFile,
	}))
	if tr.tlsConfig == nil {
		t.Fatal("expected TLS config to be built when TLS files are set, even on a plaintext scheme")
	}
	if tr.tlsConfig.RootCAs == nil {
		t.Error("RootCAs should be loaded")
	}
	if len(tr.tlsConfig.Certificates) != 1 {
		t.Fatalf("Certificates: expected 1, got %d", len(tr.tlsConfig.Certificates))
	}
}

// TestMQTTTLSErrors covers misconfiguration that should fail Init.
func TestMQTTTLSErrors(t *testing.T) {
	certFile, keyFile := generateCertFiles(t)

	tests := []struct {
		name    string
		broker  string
		tls     map[string]any
		wantErr string
	}{
		{
			name:    "cert without key",
			broker:  "mqtts://broker.example.com:8883",
			tls:     map[string]any{"tls-cert-file": certFile},
			wantErr: "must both be set",
		},
		{
			name:    "key without cert",
			broker:  "mqtts://broker.example.com:8883",
			tls:     map[string]any{"tls-key-file": keyFile},
			wantErr: "must both be set",
		},
		{
			name:    "nonexistent CA file",
			broker:  "mqtts://broker.example.com:8883",
			tls:     map[string]any{"tls-ca-file": "/nonexistent/ca.pem"},
			wantErr: "failed to read CA file",
		},
		{
			name:    "nonexistent cert file",
			broker:  "mqtts://broker.example.com:8883",
			tls:     map[string]any{"tls-cert-file": "/nonexistent/cert.pem", "tls-key-file": keyFile},
			wantErr: "failed to load client key pair",
		},
		{
			name:    "invalid CA PEM",
			broker:  "mqtts://broker.example.com:8883",
			tls:     map[string]any{"tls-ca-file": writeJunkFile(t)},
			wantErr: "failed to parse CA certificate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := newTLSConfig("err", tt.broker, tt.tls)
			tr, err := NewMQTTTransport(cfg)
			if err != nil {
				t.Fatalf("NewMQTTTransport: %v", err)
			}
			err = tr.Init(context.Background(), cfg)
			if err == nil {
				t.Fatal("expected Init error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestMQTTTLSInvalidBrokerWithFiles verifies that when TLS files are set
// but the broker URL is unparseable, Init fails (we need a host for
// ServerName). A plaintext unparseable broker without files must NOT fail
// (backward compatible — paho would surface the failure at connect time).
func TestMQTTTLSInvalidBrokerWithFiles(t *testing.T) {
	certFile, _ := generateCertFiles(t)

	// TLS files + unparseable broker → error.
	cfg := newTLSConfig("bad", "://", map[string]any{"tls-ca-file": certFile})
	tr, err := NewMQTTTransport(cfg)
	if err != nil {
		t.Fatalf("NewMQTTTransport: %v", err)
	}
	if err := tr.Init(context.Background(), cfg); err == nil {
		t.Fatal("expected error for unparseable broker with TLS files, got nil")
	}

	// Unparseable broker, no TLS files → no error (backward compatible).
	cfg2 := newTLSConfig("bad-plain", "://", nil)
	tr2, err := NewMQTTTransport(cfg2)
	if err != nil {
		t.Fatalf("NewMQTTTransport: %v", err)
	}
	if err := tr2.Init(context.Background(), cfg2); err != nil {
		t.Fatalf("expected no error for unparseable plaintext broker (backward compat), got: %v", err)
	}
	if tr2.(*MQTTTransport).tlsConfig != nil {
		t.Error("expected no TLS config for unparseable plaintext broker")
	}
}

// TestMQTTTLSInterface ensures the transport still satisfies core.Transport.
func TestMQTTTLSInterface(t *testing.T) {
	var _ core.Transport = (*MQTTTransport)(nil)
}

// writeJunkFile writes a temp file containing non-PEM bytes and returns
// its path, to exercise the "failed to parse CA certificate" path.
func writeJunkFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "junk.crt")
	if err := os.WriteFile(p, []byte("not a certificate"), 0o644); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	return p
}
