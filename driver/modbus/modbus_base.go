// Package modbus implements the Modbus TCP and RTU protocol drivers for CoreC.
package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/driverbase"
	mb "github.com/simonvetter/modbus"
)

// Modbus address conventions:
// 0xxxx → Coils            (FC 01/05/15)
// 1xxxx → Discrete Inputs  (FC 02)
// 3xxxx → Input Registers  (FC 04)
// 4xxxx → Holding Registers (FC 03/06/16)

// areaType distinguishes the four Modbus data areas.
type areaType int

const (
	areaCoil            areaType = iota // 0xxxx
	areaDiscreteInput                   // 1xxxx
	areaInputRegister                   // 3xxxx
	areaHoldingRegister                 // 4xxxx
)

// Modbus address range boundaries per the traditional 5-digit addressing
// convention: 0xxxx coils, 1xxxx discrete inputs, 3xxxx input registers,
// 4xxxx holding registers.
const (
	modbusCoilStart          = 1
	modbusCoilEnd            = 9999
	modbusDiscreteInputStart = 10001
	modbusDiscreteInputEnd   = 19999
	modbusInputRegisterStart = 30001
	modbusInputRegisterEnd   = 39999
	modbusHoldingRegStart    = 40001
	modbusHoldingRegEnd      = 49999
)

// modbusMaxBatchSize is the Modbus protocol limit of 125 registers per
// FC03/FC04 read request.
const modbusMaxBatchSize = 125

type addrInfo struct {
	area areaType
	addr uint16
}

// regType returns the simonvetter/modbus RegType for register areas.
func (a addrInfo) regType() mb.RegType {
	if a.area == areaInputRegister {
		return mb.INPUT_REGISTER
	}
	return mb.HOLDING_REGISTER
}

// areaName returns a human-readable name for a Modbus data area, used in
// configuration error messages.
func areaName(a areaType) string {
	switch a {
	case areaCoil:
		return "coil"
	case areaDiscreteInput:
		return "discrete-input"
	case areaInputRegister:
		return "input-register"
	case areaHoldingRegister:
		return "holding-register"
	default:
		return "unknown"
	}
}

// modbusBase contains the state and logic shared by all Modbus drivers
// (TCP, RTU).  Transport-specific behaviour is injected via the BaseDriver
// hooks (connectFunc, initFunc); the driverType string selects log messages
// and Status/Type output.
type modbusBase struct {
	driverbase.BaseDriver

	// Injected metrics. Defaults to NoopMetrics so the driver works without
	// explicit injection; the engine wires real implementations via
	// SetMetrics during AddDriver.
	metrics core.Metrics

	// Connection
	client  *mb.ModbusClient
	slaveID uint8
	timeout time.Duration

	// Retry settings
	maxRetry int

	// Tag mapping: name → TagConfig
	tags map[string]core.TagConfig
	// Parsed addresses: name → addrInfo
	addrs map[string]addrInfo
}

// initCommon parses the settings shared by every Modbus transport
// (slave-id, timeout, retry, reconnect back-off, tag addresses) and
// populates the corresponding base fields.  It is called from each
// concrete driver's Init after transport-specific fields have been set.
func (b *modbusBase) initCommon(driverType string, settings map[string]any, config core.DriverConfig) error {
	// Set meta (name, type, config, logger default) and parse reconnect settings.
	b.SetMeta(config.Name, driverType, config)
	if b.metrics == nil {
		b.metrics = core.NoopMetrics{}
	}
	b.ParseReconnectSettings(settings)

	b.slaveID = uint8(util.GetIntSetting(settings, "slave-id", 1))
	b.maxRetry = util.GetIntSetting(settings, "retry", 3)

	if timeoutStr, ok := settings["timeout"].(string); ok {
		if t, err := time.ParseDuration(timeoutStr); err == nil {
			b.timeout = t
		}
	}
	if b.timeout == 0 {
		b.timeout = 3 * time.Second
	}

	// Set lifecycle hooks for BaseDriver.
	b.SetCloseConnFunc(func() {
		if b.client != nil {
			b.client.Close()
			b.client = nil
		}
	})
	b.SetTagCountFunc(func() int { return len(b.tags) })

	// Register tags and parse addresses
	for _, tag := range config.Tags {
		b.tags[tag.Name] = tag
		ai, err := parseModbusAddress(tag.Address)
		if err != nil {
			return fmt.Errorf("tag %s: invalid address %q: %w", tag.Name, tag.Address, err)
		}
		b.addrs[tag.Name] = ai
	}
	return nil
}

// SetLogger injects a structured logger into the driver. Called by the
// engine during AddDriver to wire the slog backend; tests can inject a
// capture logger to assert on log output.
func (b *modbusBase) SetLogger(l core.Logger) {
	if l == nil {
		l = core.NoopLogger{}
	}
	b.BaseDriver.SetLogger(l)
}

// SetMetrics injects a metrics backend into the driver. Called by the
// engine during AddDriver to wire the Prometheus backend; tests can
// inject a capture to assert on metric values.
func (b *modbusBase) SetMetrics(m core.Metrics) {
	if m == nil {
		m = core.NoopMetrics{}
	}
	b.Lock()
	b.metrics = m
	b.Unlock()
}

// openClient creates, configures and opens a Modbus client from the given
// library configuration, then stores it in the base.  The transport-specific
// connect() methods build the ClientConfiguration and delegate to this
// helper so that error handling and state transitions are shared.
func (b *modbusBase) openClient(cfg *mb.ClientConfiguration) error {
	client, err := mb.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create modbus client: %w", err)
	}

	if err := client.SetUnitId(b.slaveID); err != nil {
		return fmt.Errorf("failed to set unit id: %w", err)
	}

	if err := client.Open(); err != nil {
		return fmt.Errorf("failed to connect to %s: %w", cfg.URL, err)
	}

	b.Lock()
	b.client = client
	b.SetStateLocked(core.StateConnected)
	b.SetLastErrorLocked("")
	b.Unlock()

	b.Logger().Info("modbus connected", "driver", b.Type(), "name", b.Name(), "url", cfg.URL)
	return nil
}

// Start, Stop, Restart, Status, Name, Type, reconnectLoop, startReconnectLoop,
// and HandleConnectionLost are provided by the embedded driverbase.BaseDriver.

func (b *modbusBase) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}

// Name, Type, and Status are provided by the embedded driverbase.BaseDriver.

func (b *modbusBase) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{
		CanRead:      true,
		CanWrite:     true,
		CanSubscribe: false,
		BatchRead:    true,
		MaxBatchSize: modbusMaxBatchSize, // Modbus FC03 max 125 registers per request (protocol limit)
	}
}

// HandleConnectionLost is provided by the embedded driverbase.BaseDriver.
