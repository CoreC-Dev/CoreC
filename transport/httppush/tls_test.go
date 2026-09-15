package httppush

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// generateWebhookCertFiles creates a self-signed certificate and matching
// private key as PEM files in a temp directory, suitable for serving the
// webhook over HTTPS in tests.  The certificate is valid for localhost and
// 127.0.0.1 and marked for both server and client auth.
func generateWebhookCertFiles(t *testing.T) (certFile, keyFile string) {
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
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	certFile = filepath.Join(dir, "webhook.crt")
	keyFile = filepath.Join(dir, "webhook.key")
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

// initHTTPTransport builds and Inits an HTTP transport from the given
// settings, returning the concrete transport so tests can inspect the
// unexported TLS state.  It does not Start the transport.
func initHTTPTransport(t *testing.T, name string, settings map[string]any) *HTTPTransport {
	t.Helper()
	cfg := core.TransportConfig{
		Name:     name,
		Type:     "http",
		Settings: settings,
	}
	tr, err := NewHTTPTransport(cfg)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	if err := tr.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return tr.(*HTTPTransport)
}

// TestWebhookTLSConfigParsed verifies that the tls-cert-file and
// tls-key-file settings are read into the transport's webhook TLS fields
// during Init.
func TestWebhookTLSConfigParsed(t *testing.T) {
	certFile, keyFile := generateWebhookCertFiles(t)
	htr := initHTTPTransport(t, "wh-tls-cfg", map[string]any{
		"url":           "http://localhost:9999/x",
		"webhook-addr":  "127.0.0.1:0",
		"webhook-path":  "/data",
		"tls-cert-file": certFile,
		"tls-key-file":  keyFile,
	})
	if htr.webhookTLSCert != certFile {
		t.Errorf("webhookTLSCert: got %q, want %q", htr.webhookTLSCert, certFile)
	}
	if htr.webhookTLSKey != keyFile {
		t.Errorf("webhookTLSKey: got %q, want %q", htr.webhookTLSKey, keyFile)
	}
}

// TestWebhookTLSBackwardCompatPlaintext verifies that when no TLS files are
// configured the webhook TLS fields stay empty — existing plaintext
// setups are unaffected.
func TestWebhookTLSBackwardCompatPlaintext(t *testing.T) {
	htr := initHTTPTransport(t, "wh-plain-cfg", map[string]any{
		"url":          "http://localhost:9999/x",
		"webhook-addr": "127.0.0.1:0",
		"webhook-path": "/data",
	})
	if htr.webhookTLSCert != "" {
		t.Errorf("webhookTLSCert should be empty, got %q", htr.webhookTLSCert)
	}
	if htr.webhookTLSKey != "" {
		t.Errorf("webhookTLSKey should be empty, got %q", htr.webhookTLSKey)
	}
}

// TestWebhookPlaintextStillWorks verifies the end-to-end plaintext webhook
// path is unchanged when no TLS files are set (backward compatibility).
func TestWebhookPlaintextStillWorks(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-plain-e2e", "")
	defer tr.Stop()

	resp := postWebhook(t, addr, "", "", validDataPoint())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 over plaintext, got %d", resp.StatusCode)
	}
}

// TestWebhookHTTPSEndToEnd verifies that when TLS files are configured the
// webhook server serves over HTTPS and a request over TLS is accepted and
// ingested.  The client skips certificate verification because the test
// certificate is self-signed.
func TestWebhookHTTPSEndToEnd(t *testing.T) {
	certFile, keyFile := generateWebhookCertFiles(t)
	addr := freePort(t)
	htr := initHTTPTransport(t, "wh-https", map[string]any{
		"url":           "http://localhost:9999/x",
		"webhook-addr":  addr,
		"webhook-path":  "/data",
		"tls-cert-file": certFile,
		"tls-key-file":  keyFile,
	})
	if err := htr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer htr.Stop()

	dataCh := htr.OnData()
	if dataCh == nil {
		t.Fatal("OnData returned nil despite webhook-addr being configured")
	}

	// Wait for the HTTPS webhook server to be ready.
	waitForServer(t, addr)

	dp := core.DataPoint{
		Driver:    "upstream",
		Tag:       "temp",
		Value:     25.5,
		Type:      core.TypeFloat64,
		Quality:   core.QualityGood,
		Timestamp: time.Now(),
	}
	body, _ := json.Marshal(dp)

	// TLS client that skips verification (self-signed test cert).
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	resp, err := client.Post("https://"+addr+"/data", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST over HTTPS failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 over HTTPS, got %d", resp.StatusCode)
	}

	select {
	case got := <-dataCh:
		if got.Tag != "temp" {
			t.Errorf("expected tag 'temp', got %q", got.Tag)
		}
		if v, ok := got.Value.(float64); !ok || v != 25.5 {
			t.Errorf("expected value 25.5, got %v", got.Value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no data point received from OnData over HTTPS")
	}

	if got := htr.Status().Received; got != 1 {
		t.Errorf("expected Received=1, got %d", got)
	}
}

// TestWebhookHTTPSRejectsPlaintext verifies that once TLS is enabled, a
// plaintext HTTP request to the same port is not ingested as a webhook
// (the server only speaks TLS on the wire).  Go's TLS server responds to
// a plaintext request with a 400 "Client sent an HTTP request to an HTTPS
// server" error rather than processing the webhook, which confirms the
// server is not silently serving plaintext.
func TestWebhookHTTPSRejectsPlaintext(t *testing.T) {
	certFile, keyFile := generateWebhookCertFiles(t)
	addr := freePort(t)
	htr := initHTTPTransport(t, "wh-https-only", map[string]any{
		"url":           "http://localhost:9999/x",
		"webhook-addr":  addr,
		"webhook-path":  "/data",
		"tls-cert-file": certFile,
		"tls-key-file":  keyFile,
	})
	if err := htr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer htr.Stop()

	waitForServer(t, addr)

	// A plaintext HTTP/1.1 POST to a TLS-only port is not processed as a
	// webhook: the TLS server rejects it with a 4xx error.  Either a
	// client-level error or a non-2xx response is acceptable; the key
	// invariant is that the request is NOT accepted (not 202) and no data
	// is ingested.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://"+addr+"/data", "application/json", bytes.NewReader(validDataPoint()))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusAccepted {
			t.Fatalf("plaintext request to TLS-only webhook was accepted (202); server is not TLS-only")
		}
	}

	if got := htr.Status().Received; got != 0 {
		t.Errorf("expected Received=0 (plaintext request must not be ingested), got %d", got)
	}
}
