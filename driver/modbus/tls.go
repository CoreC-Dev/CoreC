package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// ModbusTLSDriver implements core.Driver for Modbus TCP over TLS (tcp+tls).
// The simonvetter/modbus library requires mutual-TLS (mTLS): a client
// certificate/key pair and a CA certificate pool for server validation.
type ModbusTLSDriver struct {
	modbusBase
	host     string
	port     int
	certFile string // client certificate PEM file
	keyFile  string // client private key PEM file
	caFile   string // CA / server certificate PEM file

	// Loaded at Init time, reused for every connection.
	clientCert *tls.Certificate
	rootCAs    *x509.CertPool
}

// NewModbusTLSDriver creates a new Modbus TCP-over-TLS driver.
func NewModbusTLSDriver(config core.DriverConfig) (core.Driver, error) {
	d := &ModbusTLSDriver{
		modbusBase: modbusBase{
			name:       config.Name,
			config:     config,
			driverType: "modbus-tls",
			tags:       make(map[string]core.TagConfig),
			addrs:      make(map[string]addrInfo),
			state:      core.StateDisconnected,
		},
	}
	d.initFunc = d.Init
	d.connectFunc = d.connect
	return d, nil
}

func (d *ModbusTLSDriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	settings := config.Settings

	if host, ok := settings["host"].(string); ok {
		d.host = host
	} else {
		return fmt.Errorf("modbus-tls: host is required")
	}

	d.port = util.GetIntSetting(settings, "port", 502)

	// TLS certificate paths (all required for mTLS)
	if v, ok := settings["cert-file"].(string); ok {
		d.certFile = v
	} else {
		return fmt.Errorf("modbus-tls: cert-file is required (client certificate PEM)")
	}
	if v, ok := settings["key-file"].(string); ok {
		d.keyFile = v
	} else {
		return fmt.Errorf("modbus-tls: key-file is required (client private key PEM)")
	}
	if v, ok := settings["ca-file"].(string); ok {
		d.caFile = v
	} else {
		return fmt.Errorf("modbus-tls: ca-file is required (CA / server certificate PEM)")
	}

	// Load client certificate + key pair
	cert, err := tls.LoadX509KeyPair(d.certFile, d.keyFile)
	if err != nil {
		return fmt.Errorf("modbus-tls: failed to load client key pair: %w", err)
	}
	d.clientCert = &cert

	// Load CA certificate pool for server validation
	rootCAs, err := mb.LoadCertPool(d.caFile)
	if err != nil {
		return fmt.Errorf("modbus-tls: failed to load CA certificate: %w", err)
	}
	d.rootCAs = rootCAs

	if err := d.initCommon(settings, config); err != nil {
		return err
	}

	slog.Info("modbus-tls driver initialized",
		"name", d.name,
		"host", d.host,
		"port", d.port,
		"slave-id", d.slaveID,
		"tags", len(d.tags),
	)

	return nil
}

func (d *ModbusTLSDriver) connect() error {
	return d.openClient(&mb.ClientConfiguration{
		URL:           fmt.Sprintf("tcp+tls://%s:%d", d.host, d.port),
		Speed:         1, // ignored for TCP but required by library > 0
		Timeout:       d.timeout,
		TLSClientCert: d.clientCert,
		TLSRootCAs:    d.rootCAs,
	})
}
