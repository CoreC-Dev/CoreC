// Package modbus implements the Modbus TCP and RTU protocol drivers for CoreC.
package modbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
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

// modbusBase contains the state and logic shared by all Modbus drivers
// (TCP, RTU).  Transport-specific behaviour is injected via initFunc and
// connectFunc; the driverType string selects log messages and Status/Type
// output.
type modbusBase struct {
	mu sync.RWMutex

	name       string
	config     core.DriverConfig
	driverType string // "modbus-tcp" or "modbus-rtu"

	// Connection
	client  *mb.ModbusClient
	slaveID uint8
	timeout time.Duration

	// Retry settings
	maxRetry            int
	retryBackoff        time.Duration
	maxReconnectBackoff time.Duration

	// Tag mapping: name → TagConfig
	tags map[string]core.TagConfig
	// Parsed addresses: name → addrInfo
	addrs map[string]addrInfo

	// State
	state     core.ConnState
	lastRead  time.Time
	lastError string

	// Counters
	readCount  atomic.Uint64
	errorCount atomic.Uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks the reconnectLoop goroutine

	// Transport-specific hooks, wired by the concrete driver constructor.
	initFunc    func(context.Context, core.DriverConfig) error
	connectFunc func() error
}

// initCommon parses the settings shared by every Modbus transport
// (slave-id, timeout, retry, reconnect back-off, tag addresses) and
// populates the corresponding base fields.  It is called from each
// concrete driver's Init after transport-specific fields have been set.
func (b *modbusBase) initCommon(settings map[string]any, config core.DriverConfig) error {
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
	b.retryBackoff = util.GetDurationSetting(settings, "reconnect-interval", core.DefaultReconnectBackoff)
	b.maxReconnectBackoff = util.GetDurationSetting(settings, "reconnect-max-interval", core.DefaultMaxReconnectBackoff)

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

	b.mu.Lock()
	b.client = client
	b.state = core.StateConnected
	b.lastError = ""
	b.mu.Unlock()

	slog.Info("modbus connected", "driver", b.driverType, "name", b.name, "url", cfg.URL)
	return nil
}

// Start is shared by all Modbus drivers.
func (b *modbusBase) Start(ctx context.Context) error {
	b.ctx, b.cancel = context.WithCancel(ctx)

	if err := b.connectFunc(); err != nil {
		// Don't fail start — schedule reconnect in background
		slog.Warn("modbus initial connect failed, will retry",
			"driver", b.driverType, "name", b.name, "error", err)
		b.mu.Lock()
		b.state = core.StateConnecting
		b.lastError = err.Error()
		b.mu.Unlock()

		b.startReconnectLoop()
		return nil
	}

	slog.Info("modbus driver started", "driver", b.driverType, "name", b.name)
	return nil
}

func (b *modbusBase) reconnectLoop() {
	util.ReconnectLoop(b.ctx, b.name, b.connectFunc, b.retryBackoff, b.maxReconnectBackoff)
}

// startReconnectLoop launches the reconnect goroutine tracked by the
// WaitGroup so that Stop() can wait for any in-flight connect() to finish
// before closing the client. This prevents orphaned connections on shutdown.
func (b *modbusBase) startReconnectLoop() {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.reconnectLoop()
	}()
}

func (b *modbusBase) Stop() error {
	if b.cancel != nil {
		b.cancel()
	}
	// Wait for the reconnectLoop goroutine to exit so it cannot complete a
	// connect() after we close the client below (which would leak a connection).
	b.wg.Wait()

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.client != nil {
		b.client.Close()
		b.client = nil
	}
	b.state = core.StateDisconnected

	slog.Info("modbus driver stopped", "driver", b.driverType, "name", b.name)
	return nil
}

func (b *modbusBase) Restart(ctx context.Context, config core.DriverConfig) error {
	if err := b.Stop(); err != nil {
		return err
	}
	if err := b.initFunc(ctx, config); err != nil {
		return err
	}
	return b.Start(ctx)
}

func (b *modbusBase) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	b.mu.RLock()
	client := b.client
	state := b.state
	b.mu.RUnlock()

	if state != core.StateConnected || client == nil {
		b.errorCount.Add(1)
		return nil, fmt.Errorf("driver %s is not connected (state: %s)", b.name, state)
	}

	results := make([]core.TagValue, 0, len(tags))
	now := time.Now()

	for _, tagName := range tags {
		b.mu.RLock()
		tagCfg, tagOk := b.tags[tagName]
		ai, addrOk := b.addrs[tagName]
		b.mu.RUnlock()

		if !tagOk || !addrOk {
			results = append(results, core.TagValue{
				Tag:       tagName,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     fmt.Errorf("tag not found: %s", tagName),
			})
			continue
		}

		dt, _ := core.ParseDataType(tagCfg.Type)
		var value any
		var err error
		// Retry individual tag reads up to maxRetry times, but only for
		// connection-class errors (e.g. EOF/reset/timeout). Non-connection
		// errors such as Modbus exception responses ("illegal data address")
		// are not retried, since repeating the same request will not succeed.
		for attempt := 0; attempt <= b.maxRetry; attempt++ {
			value, err = b.readTag(client, ai, dt)
			if err == nil {
				break
			}
			if attempt < b.maxRetry && util.IsConnectionError(err) {
				time.Sleep(b.retryBackoff)
				// Refresh client in case reconnection happened
				b.mu.RLock()
				client = b.client
				b.mu.RUnlock()
				if client == nil {
					break
				}
				continue
			}
			break
		}
		if err != nil {
			b.errorCount.Add(1)
			b.mu.Lock()
			b.lastError = err.Error()
			b.mu.Unlock()

			results = append(results, core.TagValue{
				Tag:       tagName,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     err,
			})

			// Check if connection is lost
			if util.IsConnectionError(err) {
				b.handleConnectionLost()
			}
			continue
		}

		// Apply scale and offset
		value = util.ApplyTransform(value, tagCfg.Scale, tagCfg.Offset)

		results = append(results, core.TagValue{
			Tag:       tagName,
			Value:     value,
			Type:      dt,
			Quality:   core.QualityGood,
			Timestamp: now,
		})
	}

	b.readCount.Add(uint64(len(tags)))
	b.mu.Lock()
	b.lastRead = now
	b.mu.Unlock()

	return results, nil
}

// readTag reads a single tag value from the Modbus device.
func (b *modbusBase) readTag(client *mb.ModbusClient, ai addrInfo, dt core.DataType) (any, error) {
	switch dt {
	case core.TypeBool:
		if ai.area == areaCoil {
			val, err := client.ReadCoil(ai.addr)
			if err != nil {
				return nil, fmt.Errorf("read coil: %w", err)
			}
			return val, nil
		}
		if ai.area == areaDiscreteInput {
			val, err := client.ReadDiscreteInput(ai.addr)
			if err != nil {
				return nil, fmt.Errorf("read discrete input: %w", err)
			}
			return val, nil
		}
		// Read from register
		val, err := client.ReadRegister(ai.addr, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read bool register: %w", err)
		}
		return val != 0, nil

	case core.TypeUint16:
		val, err := client.ReadRegister(ai.addr, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read uint16: %w", err)
		}
		return val, nil

	case core.TypeInt16:
		val, err := client.ReadRegister(ai.addr, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read int16: %w", err)
		}
		return int16(val), nil

	case core.TypeUint32:
		regs, err := client.ReadRegisters(ai.addr, 2, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read uint32: %w", err)
		}
		return uint32(regs[0])<<16 | uint32(regs[1]), nil

	case core.TypeInt32:
		regs, err := client.ReadRegisters(ai.addr, 2, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read int32: %w", err)
		}
		val := uint32(regs[0])<<16 | uint32(regs[1])
		return int32(val), nil

	case core.TypeFloat32:
		val, err := client.ReadFloat32(ai.addr, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read float32: %w", err)
		}
		return val, nil

	case core.TypeFloat64:
		regs, err := client.ReadRegisters(ai.addr, 4, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read float64: %w", err)
		}
		buf := make([]byte, 8)
		binary.BigEndian.PutUint16(buf[0:2], regs[0])
		binary.BigEndian.PutUint16(buf[2:4], regs[1])
		binary.BigEndian.PutUint16(buf[4:6], regs[2])
		binary.BigEndian.PutUint16(buf[6:8], regs[3])
		return math.Float64frombits(binary.BigEndian.Uint64(buf)), nil

	case core.TypeUint64:
		regs, err := client.ReadRegisters(ai.addr, 4, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read uint64: %w", err)
		}
		return uint64(regs[0])<<48 | uint64(regs[1])<<32 | uint64(regs[2])<<16 | uint64(regs[3]), nil

	case core.TypeInt64:
		regs, err := client.ReadRegisters(ai.addr, 4, ai.regType())
		if err != nil {
			return nil, fmt.Errorf("read int64: %w", err)
		}
		val := uint64(regs[0])<<48 | uint64(regs[1])<<32 | uint64(regs[2])<<16 | uint64(regs[3])
		return int64(val), nil

	default:
		return nil, fmt.Errorf("unsupported data type: %s", dt)
	}
}

func (b *modbusBase) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	b.mu.RLock()
	client := b.client
	state := b.state
	b.mu.RUnlock()

	if state != core.StateConnected || client == nil {
		return nil, fmt.Errorf("driver %s is not connected", b.name)
	}

	results := make([]core.WriteResult, len(commands))
	for i, cmd := range commands {
		b.mu.RLock()
		ai, ok := b.addrs[cmd.Tag]
		b.mu.RUnlock()

		if !ok {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("tag not found: %s", cmd.Tag),
			}
			continue
		}

		// Reject writes to read-only areas (discrete inputs and input registers)
		if ai.area == areaDiscreteInput || ai.area == areaInputRegister {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("tag %s is in a read-only area (discrete input/input register)", cmd.Tag),
			}
			continue
		}

		err := b.writeTag(client, ai, cmd)
		if err != nil {
			b.errorCount.Add(1)
			results[i] = core.WriteResult{Success: false, Error: err.Error()}
			slog.Error("modbus write failed", "tag", cmd.Tag, "error", err)
		} else {
			results[i] = core.WriteResult{Success: true}
			slog.Info("modbus write success", "tag", cmd.Tag, "value", cmd.Value)
		}
	}
	return results, nil
}

func (b *modbusBase) writeTag(client *mb.ModbusClient, ai addrInfo, cmd core.WriteCommand) error {
	switch cmd.Type {
	case core.TypeBool:
		val, ok := cmd.Value.(bool)
		if !ok {
			return fmt.Errorf("expected bool value for tag %s", cmd.Tag)
		}
		if ai.area == areaCoil {
			return client.WriteCoil(ai.addr, val)
		}
		var regVal uint16
		if val {
			regVal = 1
		}
		return client.WriteRegister(ai.addr, regVal)

	case core.TypeUint16, core.TypeInt16:
		val := util.ToUint16(cmd.Value)
		return client.WriteRegister(ai.addr, val)

	case core.TypeFloat32:
		val := util.ToFloat32(cmd.Value)
		return client.WriteFloat32(ai.addr, val)

	case core.TypeUint32, core.TypeInt32:
		val := util.ToUint32(cmd.Value)
		regs := []uint16{uint16(val >> 16), uint16(val & 0xFFFF)}
		return client.WriteRegisters(ai.addr, regs)

	default:
		return fmt.Errorf("unsupported write type: %s", cmd.Type)
	}
}

func (b *modbusBase) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}

func (b *modbusBase) Name() string  { return b.name }
func (b *modbusBase) Type() string  { return b.driverType }

func (b *modbusBase) Status() core.DriverStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return core.DriverStatus{
		Name:       b.name,
		Type:       b.driverType,
		State:      b.state,
		LastRead:   b.lastRead,
		LastError:  b.lastError,
		TagCount:   len(b.tags),
		ReadCount:  b.readCount.Load(),
		ErrorCount: b.errorCount.Load(),
	}
}

func (b *modbusBase) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{
		CanRead:      true,
		CanWrite:     true,
		CanSubscribe: false,
		BatchRead:    true,
		MaxBatchSize: 125, // Modbus FC03 max 125 registers per request (protocol limit)
	}
}

func (b *modbusBase) handleConnectionLost() {
	b.mu.Lock()
	if b.state == core.StateConnecting || b.state == core.StateError {
		b.mu.Unlock()
		return
	}
	b.state = core.StateError
	if b.client != nil {
		b.client.Close()
		b.client = nil
	}
	b.mu.Unlock()

	slog.Warn("modbus connection lost, starting reconnect", "driver", b.driverType, "name", b.name)
	b.startReconnectLoop()
}

// ============================================================
// Helper functions
// ============================================================

// parseModbusAddress parses a Modbus address string (e.g. "40001") into
// register type and zero-based address.
func parseModbusAddress(addr string) (addrInfo, error) {
	num, err := strconv.ParseUint(addr, 10, 32)
	if err != nil {
		return addrInfo{}, fmt.Errorf("invalid address: %s", addr)
	}

	switch {
	case num >= 40001 && num <= 49999:
		return addrInfo{area: areaHoldingRegister, addr: uint16(num - 40001)}, nil
	case num >= 30001 && num <= 39999:
		return addrInfo{area: areaInputRegister, addr: uint16(num - 30001)}, nil
	case num >= 10001 && num <= 19999:
		return addrInfo{area: areaDiscreteInput, addr: uint16(num - 10001)}, nil
	case num >= 1 && num <= 9999:
		return addrInfo{area: areaCoil, addr: uint16(num - 1)}, nil
	default:
		// Also support raw register addresses (0-based)
		return addrInfo{area: areaHoldingRegister, addr: uint16(num)}, nil
	}
}
