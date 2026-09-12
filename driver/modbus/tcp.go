package modbus

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// ModbusTCPDriver implements core.Driver for Modbus TCP protocol.
type ModbusTCPDriver struct {
	modbusBase
	host string
	port int
}

// NewModbusTCPDriver creates a new Modbus TCP driver from configuration.
func NewModbusTCPDriver(config core.DriverConfig) (core.Driver, error) {
	d := &ModbusTCPDriver{
		modbusBase: modbusBase{
			name:       config.Name,
			config:     config,
			driverType: "modbus-tcp",
			tags:       make(map[string]core.TagConfig),
			addrs:      make(map[string]addrInfo),
			state:      core.StateDisconnected,
		},
	}
	d.initFunc = d.Init
	d.connectFunc = d.connect
	return d, nil
}

func (d *ModbusTCPDriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	settings := config.Settings

	// Parse host
	if host, ok := settings["host"].(string); ok {
		d.host = host
	} else {
		return fmt.Errorf("modbus-tcp: host is required")
	}

	// Parse port
	d.port = util.GetIntSetting(settings, "port", 502)

	if err := d.initCommon(settings, config); err != nil {
		return err
	}

	slog.Info("modbus-tcp driver initialized",
		"name", d.name,
		"host", d.host,
		"port", d.port,
		"slave-id", d.slaveID,
		"tags", len(d.tags),
	)

	return nil
}

func (d *ModbusTCPDriver) connect() error {
	return d.openClient(&mb.ClientConfiguration{
		URL:     fmt.Sprintf("tcp://%s:%d", d.host, d.port),
		Speed:   1, // ignored for TCP but required by library > 0
		Timeout: d.timeout,
	})
}
