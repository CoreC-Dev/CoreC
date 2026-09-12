package modbus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// ModbusNetDriver implements core.Driver for the host:port-based Modbus
// transports that do not need TLS: RTU-over-TCP, UDP, and RTU-over-UDP.
// The only difference between them is the URL scheme, supplied via
// urlScheme at construction time.
type ModbusNetDriver struct {
	modbusBase
	host      string
	port      int
	urlScheme string // "rtuovertcp", "udp", "rtuoverudp"
}

// newModbusNetDriver is the shared constructor for all non-TLS network
// Modbus variants.  driverType is the public type string (e.g.
// "modbus-rtuovertcp"); urlScheme is the simonvetter/modbus URL prefix
// (e.g. "rtuovertcp").
func newModbusNetDriver(config core.DriverConfig, driverType, urlScheme string) (core.Driver, error) {
	d := &ModbusNetDriver{
		modbusBase: modbusBase{
			name:       config.Name,
			config:     config,
			driverType: driverType,
			tags:       make(map[string]core.TagConfig),
			addrs:      make(map[string]addrInfo),
			state:      core.StateDisconnected,
		},
		urlScheme: urlScheme,
	}
	d.initFunc = d.Init
	d.connectFunc = d.connect
	return d, nil
}

// NewModbusRTUOverTCPDriver creates a driver for Modbus RTU frames
// encapsulated in a TCP connection (common with serial-to-Ethernet gateways).
func NewModbusRTUOverTCPDriver(config core.DriverConfig) (core.Driver, error) {
	return newModbusNetDriver(config, "modbus-rtuovertcp", "rtuovertcp")
}

// NewModbusUDPDriver creates a driver for Modbus TCP-over-UDP.
func NewModbusUDPDriver(config core.DriverConfig) (core.Driver, error) {
	return newModbusNetDriver(config, "modbus-udp", "udp")
}

// NewModbusRTUOverUDPDriver creates a driver for Modbus RTU frames
// encapsulated in a UDP connection.
func NewModbusRTUOverUDPDriver(config core.DriverConfig) (core.Driver, error) {
	return newModbusNetDriver(config, "modbus-rtuoverudp", "rtuoverudp")
}

func (d *ModbusNetDriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	settings := config.Settings

	if host, ok := settings["host"].(string); ok {
		d.host = host
	} else {
		return fmt.Errorf("%s: host is required", d.driverType)
	}

	d.port = util.GetIntSetting(settings, "port", 502)

	if err := d.initCommon(settings, config); err != nil {
		return err
	}

	slog.Info("modbus network driver initialized",
		"driver", d.driverType,
		"name", d.name,
		"host", d.host,
		"port", d.port,
		"slave-id", d.slaveID,
		"tags", len(d.tags),
	)

	return nil
}

func (d *ModbusNetDriver) connect() error {
	return d.openClient(&mb.ClientConfiguration{
		URL:     fmt.Sprintf("%s://%s:%d", d.urlScheme, d.host, d.port),
		Speed:   1, // ignored for network transports but required by library > 0
		Timeout: d.timeout,
	})
}
