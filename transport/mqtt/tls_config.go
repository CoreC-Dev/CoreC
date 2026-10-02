package mqtt

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"

	"github.com/CoreC-Dev/CoreC/common/tlsutil"
)

// tlsSchemes are the broker URL schemes that the paho MQTT client routes
// over TLS (see paho netconn.go).  Using one of these schemes is what
// actually enables TLS on the wire; SetTLSConfig only supplies the
// configuration that the TLS dial then uses.
var tlsSchemes = map[string]bool{
	"ssl": true, "tls": true, "mqtts": true, "mqtt+ssl": true, "tcps": true, "wss": true,
}

// buildTLSConfig constructs the *tls.Config for the MQTT client when TLS is
// required.  TLS is required when the broker URL uses a TLS scheme or when
// any TLS file setting (CA / client cert / key) is provided.  Returns nil
// (no TLS) when neither condition holds, preserving backward compatibility
// with plaintext mqtt:// / tcp:// brokers.
//
//   - tls-ca-file:     loads a CA certificate pool for server verification.
//     When omitted, Go uses the system root certificates.
//   - tls-cert-file + tls-key-file: loads a client certificate/key pair for
//     mutual TLS (mTLS).  Both must be set together.
//   - ServerName is derived from the broker URL host so that certificate
//     hostname verification works correctly.
func (t *MQTTTransport) buildTLSConfig() (*tls.Config, error) {
	// Normalize the broker the same way paho's AddBroker does so that any
	// broker paho accepts (including schemeless "host:port" forms) is
	// accepted here too, and we derive the same scheme paho would use.
	broker := t.broker
	if broker != "" && broker[0] == ':' {
		broker = "127.0.0.1" + broker
	}
	if !strings.Contains(broker, "://") {
		broker = "tcp://" + broker
	}

	u, err := url.Parse(broker)
	schemeTLS := err == nil && tlsSchemes[strings.ToLower(u.Scheme)]
	anyFile := t.tlsCAFile != "" || t.tlsCertFile != "" || t.tlsKeyFile != ""

	// Plaintext (or unparseable plaintext) with no TLS files: build no TLS
	// config.  This preserves the historical behaviour where Init accepts
	// any broker string and connection failures surface only at
	// Start/Connect time, so existing non-TLS setups are unaffected.
	if !schemeTLS && !anyFile {
		return nil, nil //nolint:nilnil // a nil *tls.Config with no error intentionally signals "no TLS needed"
	}

	// TLS is required (TLS scheme or TLS files set).  A valid parsed URL is
	// needed to derive ServerName for certificate hostname verification.
	if err != nil {
		return nil, fmt.Errorf("mqtt: invalid broker URL %q: %w", t.broker, err)
	}

	cfg := &tls.Config{
		ServerName: u.Hostname(),
		MinVersion: tls.VersionTLS12,
	}

	// CA certificate pool for server verification.  When omitted, Go falls
	// back to the system root certificates.
	if t.tlsCAFile != "" {
		pool, err := tlsutil.LoadCertPool(t.tlsCAFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt: %w", err)
		}
		cfg.RootCAs = pool
	}

	// Client certificate + key for mutual TLS.  Both must be provided
	// together; specifying only one is a configuration error.
	if t.tlsCertFile != "" || t.tlsKeyFile != "" {
		cert, err := tlsutil.LoadClientCert(t.tlsCertFile, t.tlsKeyFile)
		if err != nil {
			return nil, fmt.Errorf("mqtt: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	// Fail if TLS files were provided but the broker scheme is plaintext:
	// paho only applies the TLS config to TLS-scheme brokers, so the config
	// would silently have no effect on the wire. Failing here prevents a
	// misconfiguration where the operator thinks TLS is enabled but
	// credentials/commands are actually sent in plaintext.
	if !schemeTLS && anyFile {
		return nil, fmt.Errorf("mqtt: TLS files configured but broker scheme %q is not TLS; use mqtts:// or ssl:// scheme to enable TLS", u.Scheme)
	}

	return cfg, nil
}
