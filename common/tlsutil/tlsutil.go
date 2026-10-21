// Package tlsutil provides shared TLS configuration helpers used by
// transports (mqtt, httppush) and drivers (modbus-tls) to avoid
// duplicating certificate-loading boilerplate.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadCertPool reads a PEM-encoded CA certificate file and returns a
// CertPool containing the parsed certificates. The pool is suitable for
// assignment to tls.Config.RootCAs.
func LoadCertPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read CA file %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("failed to parse CA certificate(s) from %s", caFile)
	}
	return pool, nil
}

// LoadClientCert loads a PEM-encoded client certificate and private key
// pair. Both files must be provided.
func LoadClientCert(certFile, keyFile string) (tls.Certificate, error) {
	if certFile == "" || keyFile == "" {
		return tls.Certificate{}, fmt.Errorf("cert-file and key-file must both be set for mutual TLS")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to load client key pair: %w", err)
	}
	return cert, nil
}
