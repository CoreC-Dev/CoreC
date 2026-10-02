// Package s7 implements the Siemens S7 protocol driver for CoreC.
// Supports S7-200, S7-300, S7-400, S7-1200, and S7-1500 PLCs.
package s7

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/driverbase"
	"github.com/robinson/gos7"
)

// s7Area defines the Siemens memory areas
type s7Area int

const (
	areaDB s7Area = iota // Data Block (DB)
	areaM                // Merker / Flags (M / MK)
	areaI                // Process Inputs (I / E / PE)
	areaQ                // Process Outputs (Q / A / PA)
)

// s7MaxBatchSize is the maximum number of tags processed per Read batch,
// matching the PDU limit advertised via Capabilities().MaxBatchSize.
const s7MaxBatchSize = 220

// TypeName is the protocol type identifier for the S7 driver.
const TypeName = "s7"

// s7Address holds the parsed S7 address
type s7Address struct {
	area     s7Area
	dbNumber int  // DB number (for areaDB)
	start    int  // Byte offset
	bit      uint // Bit offset (0..7 for booleans)
	size     int  // Number of bytes to read
	isBit    bool
}

// S7Driver implements core.Driver for Siemens S7 PLC communication.
type S7Driver struct {
	driverbase.BaseDriver

	// writeMu serializes bit-write read-modify-write sequences. Writing a
	// single bit requires reading the containing byte, modifying one bit, and
	// writing the byte back; without serialization, concurrent bit writes to
	// the same byte can lose updates. It is separate from mu (which protects
	// config/state) so non-bit writes and reads are not blocked by it.
	writeMu sync.Mutex

	// Connection settings
	host        string
	port        int
	rack        int
	slot        int
	timeout     time.Duration
	idleTimeout time.Duration

	// Connection instances
	handler *gos7.TCPClientHandler
	client  gos7.Client
	helper  gos7.Helper

	// Tag mappings
	tags  map[string]core.TagConfig
	addrs map[string]s7Address
}

// NewS7Driver creates a new S7 driver from configuration.
func NewS7Driver(config core.DriverConfig) (core.Driver, error) {
	d := &S7Driver{
		tags:  make(map[string]core.TagConfig),
		addrs: make(map[string]s7Address),
	}
	d.SetMeta(config.Name, TypeName, config)
	d.SetConnectFunc(d.connect)
	d.SetInitFunc(d.Init)
	d.SetCloseConnFunc(func() {
		if d.handler != nil {
			d.handler.Close()
			d.handler = nil
			d.client = nil
		}
	})
	d.SetTagCountFunc(func() int { return len(d.tags) })
	return d, nil
}

func (d *S7Driver) Init(ctx context.Context, config core.DriverConfig) error {
	d.Lock()
	defer d.Unlock()

	d.SetMeta(config.Name, TypeName, config)
	settings := config.Settings

	if host, ok := settings["host"].(string); ok {
		d.host = host
	} else {
		return fmt.Errorf("s7: host is required")
	}

	d.port = util.GetIntSetting(settings, "port", 102) // S7 default ISO-on-TCP port
	d.rack = util.GetIntSetting(settings, "rack", 0)
	d.slot = util.GetIntSetting(settings, "slot", 2) // Common slot: 2 for S7-300, 1 for S7-1200/1500

	if timeoutStr, ok := settings["timeout"].(string); ok {
		if t, err := time.ParseDuration(timeoutStr); err == nil {
			d.timeout = t
		}
	}
	if d.timeout == 0 {
		d.timeout = core.DefaultDriverTimeout
	}
	d.idleTimeout = util.GetDurationSetting(settings, "idle-timeout", core.DefaultIdleTimeout)
	d.ParseReconnectSettings(settings)

	// Parse tags and addresses
	for _, tag := range config.Tags {
		d.tags[tag.Name] = tag
		dt, _ := core.ParseDataType(tag.Type)
		addr, err := parseS7Address(tag.Address, dt)
		if err != nil {
			return fmt.Errorf("tag %s: invalid s7 address %q: %w", tag.Name, tag.Address, err)
		}
		d.addrs[tag.Name] = addr
	}

	slog.Info("s7 driver initialized",
		"name", d.Name(),
		"host", d.host,
		"rack", d.rack,
		"slot", d.slot,
		"tags", len(d.tags),
	)

	return nil
}

// Start, Stop, Restart, Status, Name, Type, reconnectLoop, startReconnectLoop,
// and HandleConnectionLost are provided by the embedded driverbase.BaseDriver.

func (d *S7Driver) connect() error {
	addr := fmt.Sprintf("%s:%d", d.host, d.port)
	handler := gos7.NewTCPClientHandler(addr, d.rack, d.slot)
	handler.Timeout = d.timeout
	handler.IdleTimeout = d.idleTimeout

	if err := handler.Connect(); err != nil {
		return fmt.Errorf("failed to connect to s7 plc at %s: %w", addr, err)
	}

	client := gos7.NewClient(handler)

	d.Lock()
	d.handler = handler
	d.client = client
	d.SetStateLocked(core.StateConnected)
	d.SetLastErrorLocked("")
	d.Unlock()

	slog.Info("s7 connected", "name", d.Name(), "address", addr)
	return nil
}

// reconnectLoop and startReconnectLoop are provided by BaseDriver.

func (d *S7Driver) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	d.RLock()
	client := d.client
	state := d.GetStateLocked()
	d.RUnlock()

	if state != core.StateConnected || client == nil {
		d.RecordError()
		return nil, fmt.Errorf("driver %s is not connected", d.Name())
	}

	results := make([]core.TagValue, 0, len(tags))
	now := time.Now()

	// Process tags in batches of s7MaxBatchSize (the PDU limit advertised
	// via Capabilities().MaxBatchSize). Each tag is still an independent
	// gos7 call, but chunking enforces the advertised batch contract so a
	// single Read never silently exceeds the declared limit.
	for start := 0; start < len(tags); start += s7MaxBatchSize {
		end := start + s7MaxBatchSize
		if end > len(tags) {
			end = len(tags)
		}
		batch := tags[start:end]

		for _, tagName := range batch {
			d.RLock()
			tagCfg, tagOk := d.tags[tagName]
			addr, addrOk := d.addrs[tagName]
			d.RUnlock()

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
			val, err := d.readAddress(client, addr, dt)
			if err != nil {
				d.RecordError()
				d.Lock()
				d.SetLastErrorLocked(err.Error())
				d.Unlock()

				results = append(results, core.TagValue{
					Tag:       tagName,
					Quality:   core.QualityBad,
					Timestamp: now,
					Error:     err,
				})

				// Check if connection is lost and trigger reconnect
				if util.IsConnectionError(err) {
					d.HandleConnectionLost()
				}
				continue
			}

			// Apply scale and offset
			val = util.ApplyTransform(val, tagCfg.Scale, tagCfg.Offset)

			results = append(results, core.TagValue{
				Tag:       tagName,
				Value:     val,
				Type:      dt,
				Quality:   core.QualityGood,
				Timestamp: now,
			})
		}
		d.AddReadCount(uint64(len(batch)))
	}

	d.Lock()
	d.SetLastRead(now)
	d.Unlock()

	return results, nil
}

func (d *S7Driver) readAddress(client gos7.Client, addr s7Address, dt core.DataType) (any, error) {
	buf := make([]byte, addr.size)
	var err error

	switch addr.area {
	case areaDB:
		err = client.AGReadDB(addr.dbNumber, addr.start, addr.size, buf)
	case areaM:
		err = client.AGReadMB(addr.start, addr.size, buf)
	case areaI:
		err = client.AGReadEB(addr.start, addr.size, buf)
	case areaQ:
		err = client.AGReadAB(addr.start, addr.size, buf)
	}

	if err != nil {
		return nil, fmt.Errorf("s7 read error: %w", err)
	}

	return decodeS7Buffer(buf, addr, dt, &d.helper)
}

func (d *S7Driver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	d.RLock()
	client := d.client
	state := d.GetStateLocked()
	d.RUnlock()

	if state != core.StateConnected || client == nil {
		return nil, fmt.Errorf("driver %s is not connected", d.Name())
	}

	results := make([]core.WriteResult, len(commands))
	for i, cmd := range commands {
		d.RLock()
		addr, ok := d.addrs[cmd.Tag]
		d.RUnlock()

		if !ok {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("tag not found: %s", cmd.Tag),
			}
			continue
		}

		err := d.writeAddress(client, addr, cmd)
		if err != nil {
			d.RecordError()
			results[i] = core.WriteResult{Success: false, Error: err.Error()}
			slog.Error("s7 write failed", "tag", cmd.Tag, "error", err)
		} else {
			results[i] = core.WriteResult{Success: true}
			slog.Info("s7 write success", "tag", cmd.Tag, "value", cmd.Value)
		}
	}
	return results, nil
}

func (d *S7Driver) writeAddress(client gos7.Client, addr s7Address, cmd core.WriteCommand) error {
	buf, err := encodeS7Value(cmd.Value, addr, cmd.Type, &d.helper)
	if err != nil {
		return fmt.Errorf("s7 encode %s: %w", cmd.Tag, err)
	}

	// If writing a single bit, we must read-modify-write the containing byte.
	// The read and the write are separate PLC operations, so without
	// serialization two concurrent bit writes to the same byte can lose
	// updates (each reads the old byte, sets its own bit, and writes back,
	// clobbering the other). Acquire writeMu for the whole read-modify-write
	// sequence (the deferred unlock releases it after the write below).
	// Non-bit writes do not need this lock.
	if addr.isBit {
		d.writeMu.Lock()
		defer d.writeMu.Unlock()

		currentByte := make([]byte, 1)
		switch addr.area {
		case areaDB:
			err = client.AGReadDB(addr.dbNumber, addr.start, 1, currentByte)
		case areaM:
			err = client.AGReadMB(addr.start, 1, currentByte)
		case areaI:
			err = client.AGReadEB(addr.start, 1, currentByte)
		case areaQ:
			err = client.AGReadAB(addr.start, 1, currentByte)
		}
		if err != nil {
			return fmt.Errorf("failed to read before bit-write: %w", err)
		}

		valBool := false
		if b, ok := cmd.Value.(bool); ok {
			valBool = b
		}
		buf = []byte{d.helper.SetBoolAt(currentByte[0], addr.bit, valBool)}
	}

	switch addr.area {
	case areaDB:
		err = client.AGWriteDB(addr.dbNumber, addr.start, len(buf), buf)
	case areaM:
		err = client.AGWriteMB(addr.start, len(buf), buf)
	case areaI:
		err = client.AGWriteEB(addr.start, len(buf), buf)
	case areaQ:
		err = client.AGWriteAB(addr.start, len(buf), buf)
	}

	return err
}

func (d *S7Driver) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}

// Name, Type, and Status are provided by the embedded driverbase.BaseDriver.

func (d *S7Driver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{
		CanRead:      true,
		CanWrite:     true,
		CanSubscribe: false,
		BatchRead:    true,
		MaxBatchSize: s7MaxBatchSize, // PDU limit for standard S7 requests
	}
}

// HandleConnectionLost is provided by the embedded driverbase.BaseDriver.
