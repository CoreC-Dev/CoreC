package modbus

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

// ModbusRTUDriver implements core.Driver for Modbus RTU over a serial line.
type ModbusRTUDriver struct {
	modbusBase

	// Serial port parameters
	serialDevice string // e.g. "/dev/ttyUSB0", "COM3"
	baudRate     uint   // e.g. 9600, 19200, 115200
	dataBits     uint   // 7 or 8 (default 8)
	parity       uint   // PARITY_NONE / PARITY_EVEN / PARITY_ODD
	stopBits     uint   // 1 or 2 (0 → let the library decide)
}

// NewModbusRTUDriver creates a new Modbus RTU driver from configuration.
func NewModbusRTUDriver(config core.DriverConfig) (core.Driver, error) {
	d := &ModbusRTUDriver{
		modbusBase: modbusBase{
			name:       config.Name,
			config:     config,
			driverType: "modbus-rtu",
			tags:       make(map[string]core.TagConfig),
			addrs:      make(map[string]addrInfo),
			state:      core.StateDisconnected,
		},
	}
	d.initFunc = d.Init
	d.connectFunc = d.connect
	return d, nil
}

func (d *ModbusRTUDriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	settings := config.Settings

	// Parse serial device path (required)
	if dev, ok := settings["serial-device"].(string); ok && dev != "" {
		d.serialDevice = dev
	} else {
		return fmt.Errorf("modbus-rtu: serial-device is required (e.g. /dev/ttyUSB0 or COM3)")
	}

	// Parse baud rate (default 9600)
	d.baudRate = uint(util.GetIntSetting(settings, "baud-rate", 9600))
	if d.baudRate == 0 {
		d.baudRate = 9600
	}

	// Parse data bits (default 8)
	d.dataBits = uint(util.GetIntSetting(settings, "data-bits", 8))

	// Parse parity (default "none")
	d.parity = parseParity(settings)

	// Parse stop bits (default 0 → library auto-selects based on parity)
	d.stopBits = uint(util.GetIntSetting(settings, "stop-bits", 0))

	if err := d.initCommon(settings, config); err != nil {
		return err
	}

	slog.Info("modbus-rtu driver initialized",
		"name", d.name,
		"serial-device", d.serialDevice,
		"baud-rate", d.baudRate,
		"data-bits", d.dataBits,
		"parity", parityString(d.parity),
		"stop-bits", d.stopBits,
		"slave-id", d.slaveID,
		"tags", len(d.tags),
	)

	return nil
}

func (d *ModbusRTUDriver) connect() error {
	return d.openClient(&mb.ClientConfiguration{
		URL:      fmt.Sprintf("rtu://%s", d.serialDevice),
		Speed:    d.baudRate,
		DataBits: d.dataBits,
		Parity:   d.parity,
		StopBits: d.stopBits,
		Timeout:  d.timeout,
	})
}

// parseParity converts a parity setting string ("none"/"even"/"odd") to the
// corresponding simonvetter/modbus constant.  Defaults to PARITY_NONE.
func parseParity(settings map[string]any) uint {
	if v, ok := settings["parity"].(string); ok {
		switch strings.ToLower(v) {
		case "even":
			return mb.PARITY_EVEN
		case "odd":
			return mb.PARITY_ODD
		default:
			return mb.PARITY_NONE
		}
	}
	return mb.PARITY_NONE
}

// parityString returns a human-readable parity label for log output.
func parityString(p uint) string {
	switch p {
	case mb.PARITY_EVEN:
		return "even"
	case mb.PARITY_ODD:
		return "odd"
	default:
		return "none"
	}
}
