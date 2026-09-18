// Package modbus implements the Modbus TCP and RTU protocol drivers for CoreC.
package modbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"
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
	maxRetry             int
	retryBackoff         time.Duration
	maxReconnectBackoff  time.Duration
	maxReconnectFailures int // circuit breaker threshold; 0 = disabled

	// Tag mapping: name → TagConfig
	tags map[string]core.TagConfig
	// Parsed addresses: name → addrInfo
	addrs map[string]addrInfo

	// State
	state     core.ConnState
	lastRead  time.Time
	lastError string

	// Counters
	readCount      atomic.Uint64
	errorCount     atomic.Uint64
	reconnectCount atomic.Uint64

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
	b.maxReconnectFailures = util.GetIntSetting(settings, "max-reconnect-failures", core.DefaultMaxReconnectFailures)

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
	util.ReconnectLoopWithBreakerCounted(b.ctx, b.name, b.connectFunc, b.retryBackoff, b.maxReconnectBackoff, b.maxReconnectFailures, &b.reconnectCount)
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

	now := time.Now()

	// Phase 1: resolve every requested tag against the configured tag
	// table and classify it as batchable or not. Tags that don't resolve
	// are marked invalid and reported as bad-quality below; batchable tags
	// are grouped by Modbus area and read in a single round-trip per
	// contiguous range (Phase 2). Non-batchable tags and any tags whose
	// batch read failed fall back to the per-tag readTag path (Phase 3),
	// which preserves the original retry/error semantics exactly.
	reqs := make([]batchTagReq, len(tags))
	valid := make([]bool, len(tags))
	for i, tagName := range tags {
		b.mu.RLock()
		tagCfg, tagOk := b.tags[tagName]
		ai, addrOk := b.addrs[tagName]
		b.mu.RUnlock()

		if !tagOk || !addrOk {
			valid[i] = false
			continue
		}
		dt, _ := core.ParseDataType(tagCfg.Type)
		qty, ok := batchQuantity(ai, dt)
		reqs[i] = batchTagReq{name: tagName, cfg: tagCfg, ai: ai, dt: dt, qty: qty, batchable: ok}
		valid[i] = true
	}

	// Phase 2: batch reads. Returns a map of tag index → raw value for
	// tags that were read successfully as part of a batch. Tags absent
	// from the map are handled individually in Phase 3.
	batchValues, client := b.performBatchReads(client, reqs, valid)

	// Phase 3: assemble results in input order. Batched tags use the
	// value from Phase 2; everything else goes through the per-tag
	// readTag path with the original retry/transform/error handling.
	results := make([]core.TagValue, 0, len(tags))
	for i, tagName := range tags {
		if !valid[i] {
			results = append(results, core.TagValue{
				Tag:       tagName,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     fmt.Errorf("tag not found: %s", tagName),
			})
			continue
		}

		req := reqs[i]

		// Use the batched value if Phase 2 produced one.
		if rv, ok := batchValues[i]; ok {
			value := util.ApplyTransform(rv, req.cfg.Scale, req.cfg.Offset)
			results = append(results, core.TagValue{
				Tag:       tagName,
				Value:     value,
				Type:      req.dt,
				Quality:   core.QualityGood,
				Timestamp: now,
			})
			continue
		}

		// Individual read with retry (original behaviour).
		var value any
		var err error
		// Retry individual tag reads up to maxRetry times, but only for
		// connection-class errors (e.g. EOF/reset/timeout). Non-connection
		// errors such as Modbus exception responses ("illegal data address")
		// are not retried, since repeating the same request will not succeed.
		for attempt := 0; attempt <= b.maxRetry; attempt++ {
			value, err = b.readTag(client, req.ai, req.dt, tagName)
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
		value = util.ApplyTransform(value, req.cfg.Scale, req.cfg.Offset)

		results = append(results, core.TagValue{
			Tag:       tagName,
			Value:     value,
			Type:      req.dt,
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
func (b *modbusBase) readTag(client *mb.ModbusClient, ai addrInfo, dt core.DataType, tagName string) (any, error) { //nolint:gocyclo // per-datatype Modbus decode dispatch; complexity 26. Splitting risks subtle bit-packing regressions.
	// Coils (0xxxx) and discrete inputs (1xxxx) are single-bit areas. Only
	// bool reads are meaningful there; any other type would be routed to a
	// holding/input-register read via regType() (which returns HOLDING_REGISTER
	// for coil areas), silently reading from the wrong memory area. Reject the
	// misconfiguration explicitly instead.
	if (ai.area == areaCoil || ai.area == areaDiscreteInput) && dt != core.TypeBool {
		return nil, fmt.Errorf("tag %s: type %s is not supported on %s area (only bool is supported on coil/discrete-input areas)", tagName, dt, areaName(ai.area))
	}

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

// ============================================================
// Batch reading
// ============================================================

// batchTagReq is a resolved tag request used by the batch-read path.
type batchTagReq struct {
	name      string
	cfg       core.TagConfig
	ai        addrInfo
	dt        core.DataType
	qty       uint16 // register count (register areas) or bit count (coil/discrete)
	batchable bool
}

// batchQuantity returns the number of Modbus registers (for holding/input
// register areas) or bits (for coil/discrete-input areas) a tag occupies,
// and whether the tag can participate in a batched read. Tags that readTag
// would reject — non-bool types on coil/discrete-input areas, or types the
// driver doesn't support — report batchable=false so they fall back to the
// per-tag readTag path and surface the same error as before.
func batchQuantity(ai addrInfo, dt core.DataType) (uint16, bool) {
	if ai.area == areaCoil || ai.area == areaDiscreteInput {
		if dt != core.TypeBool {
			return 0, false
		}
		return 1, true
	}
	switch dt {
	case core.TypeBool, core.TypeUint16, core.TypeInt16:
		return 1, true
	case core.TypeUint32, core.TypeInt32, core.TypeFloat32:
		return 2, true
	case core.TypeFloat64, core.TypeUint64, core.TypeInt64:
		return 4, true
	default:
		return 0, false
	}
}

// readBatch is a contiguous range of tags in a single Modbus area that can
// be fetched with one protocol request.
type readBatch struct {
	area    areaType
	start   uint16 // first register/bit address
	qty     uint16 // number of registers (register areas) or bits (coil/discrete)
	indices []int  // indices into the caller's reqs slice
}

func (rb readBatch) regType() mb.RegType {
	if rb.area == areaInputRegister {
		return mb.INPUT_REGISTER
	}
	return mb.HOLDING_REGISTER
}

// performBatchReads groups batchable tags by area, merges contiguous/
// overlapping ranges (bounded by MaxBatchSize), and issues one Modbus
// request per merged range. It returns a map of tag index → raw value for
// tags read successfully. Tags whose batch read fails (after retries) are
// omitted so Phase 3 handles them individually. The possibly-refreshed
// client is returned so subsequent individual reads reuse a reconnected
// client.
func (b *modbusBase) performBatchReads(client *mb.ModbusClient, reqs []batchTagReq, valid []bool) (map[int]any, *mb.ModbusClient) {
	batchValues := make(map[int]any)

	maxBatch := uint16(b.Capabilities().MaxBatchSize)
	if maxBatch == 0 {
		maxBatch = modbusMaxBatchSize
	}

	// Group batchable tag indices by area.
	groups := make(map[areaType][]int)
	for i := range reqs {
		if valid[i] && reqs[i].batchable {
			groups[reqs[i].ai.area] = append(groups[reqs[i].ai.area], i)
		}
	}

	for area, indices := range groups {
		// Sort by start address so contiguous ranges are adjacent.
		sort.SliceStable(indices, func(a, c int) bool {
			return reqs[indices[a]].ai.addr < reqs[indices[c]].ai.addr
		})

		for _, batch := range mergeBatches(area, indices, reqs, maxBatch) {
			values, refreshedClient, err := b.readBatchWithRetry(client, batch, reqs)
			client = refreshedClient
			if err != nil {
				// Batch failed after retries: leave these tags for
				// individual fallback (Phase 3). Don't touch errorCount
				// here — the per-tag reads account for it.
				continue
			}
			for idx, v := range values {
				batchValues[idx] = v
			}
		}
	}

	return batchValues, client
}

// readBatchWithRetry issues a single batch read, retrying on
// connection-class errors (mirroring the per-tag retry policy in Read).
func (b *modbusBase) readBatchWithRetry(client *mb.ModbusClient, batch readBatch, reqs []batchTagReq) (map[int]any, *mb.ModbusClient, error) {
	var values map[int]any
	var err error
	for attempt := 0; attempt <= b.maxRetry; attempt++ {
		values, err = b.readBatch(client, batch, reqs)
		if err == nil {
			break
		}
		if attempt < b.maxRetry && util.IsConnectionError(err) {
			time.Sleep(b.retryBackoff)
			// Refresh client in case reconnection happened.
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
	return values, client, err
}

// readBatch issues one Modbus request for the merged range and decodes each
// tag's value from the response at the correct offset.
func (b *modbusBase) readBatch(client *mb.ModbusClient, batch readBatch, reqs []batchTagReq) (map[int]any, error) {
	values := make(map[int]any, len(batch.indices))

	if batch.area == areaCoil || batch.area == areaDiscreteInput {
		var bits []bool
		var err error
		if batch.area == areaCoil {
			bits, err = client.ReadCoils(batch.start, batch.qty)
		} else {
			bits, err = client.ReadDiscreteInputs(batch.start, batch.qty)
		}
		if err != nil {
			return nil, fmt.Errorf("batch read %s: %w", areaName(batch.area), err)
		}
		for _, idx := range batch.indices {
			offset := int(reqs[idx].ai.addr - batch.start)
			values[idx] = bits[offset]
		}
		return values, nil
	}

	regs, err := client.ReadRegisters(batch.start, batch.qty, batch.regType())
	if err != nil {
		return nil, fmt.Errorf("batch read %s: %w", areaName(batch.area), err)
	}
	for _, idx := range batch.indices {
		offset := int(reqs[idx].ai.addr - batch.start)
		v, derr := extractRegisterValue(regs, offset, reqs[idx].dt)
		if derr != nil {
			return nil, fmt.Errorf("batch decode tag %s: %w", reqs[idx].name, derr)
		}
		values[idx] = v
	}
	return values, nil
}

// mergeBatches greedily merges sorted tag indices into contiguous or
// overlapping ranges, splitting whenever a gap appears or the merged range
// would exceed maxBatch registers/bits.
func mergeBatches(area areaType, indices []int, reqs []batchTagReq, maxBatch uint16) []readBatch {
	if len(indices) == 0 {
		return nil
	}
	var batches []readBatch
	cur := readBatch{
		area:    area,
		start:   reqs[indices[0]].ai.addr,
		qty:     reqs[indices[0]].qty,
		indices: []int{indices[0]},
	}
	curEnd := cur.start + cur.qty
	for _, idx := range indices[1:] {
		tStart := reqs[idx].ai.addr
		tEnd := tStart + reqs[idx].qty
		mergedEnd := curEnd
		if tEnd > mergedEnd {
			mergedEnd = tEnd
		}
		mergedQty := mergedEnd - cur.start
		// Merge when the next range is contiguous or overlapping with
		// the current batch AND the combined range stays within the
		// protocol batch limit.
		if tStart <= curEnd && mergedQty <= maxBatch {
			cur.indices = append(cur.indices, idx)
			cur.qty = mergedQty
			curEnd = mergedEnd
		} else {
			batches = append(batches, cur)
			cur = readBatch{
				area:    area,
				start:   tStart,
				qty:     reqs[idx].qty,
				indices: []int{idx},
			}
			curEnd = tEnd
		}
	}
	batches = append(batches, cur)
	return batches
}

// extractRegisterValue decodes a single tag value from a slice of 16-bit
// registers at the given offset, mirroring the per-type decoding in
// readTag. The driver uses the modbus library's default encoding
// (big-endian, high-word-first), so this manual reconstruction matches
// readTag's output for every supported type.
func extractRegisterValue(regs []uint16, offset int, dt core.DataType) (any, error) {
	switch dt {
	case core.TypeBool:
		return regs[offset] != 0, nil
	case core.TypeUint16:
		return regs[offset], nil
	case core.TypeInt16:
		return int16(regs[offset]), nil
	case core.TypeUint32:
		return uint32(regs[offset])<<16 | uint32(regs[offset+1]), nil
	case core.TypeInt32:
		return int32(uint32(regs[offset])<<16 | uint32(regs[offset+1])), nil
	case core.TypeFloat32:
		buf := make([]byte, 4)
		binary.BigEndian.PutUint16(buf[0:2], regs[offset])
		binary.BigEndian.PutUint16(buf[2:4], regs[offset+1])
		return math.Float32frombits(binary.BigEndian.Uint32(buf)), nil
	case core.TypeFloat64:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint16(buf[0:2], regs[offset])
		binary.BigEndian.PutUint16(buf[2:4], regs[offset+1])
		binary.BigEndian.PutUint16(buf[4:6], regs[offset+2])
		binary.BigEndian.PutUint16(buf[6:8], regs[offset+3])
		return math.Float64frombits(binary.BigEndian.Uint64(buf)), nil
	case core.TypeUint64:
		return uint64(regs[offset])<<48 | uint64(regs[offset+1])<<32 | uint64(regs[offset+2])<<16 | uint64(regs[offset+3]), nil
	case core.TypeInt64:
		val := uint64(regs[offset])<<48 | uint64(regs[offset+1])<<32 | uint64(regs[offset+2])<<16 | uint64(regs[offset+3])
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
	// Coils (0xxxx) and discrete inputs (1xxxx) are single-bit areas. Only
	// bool writes are meaningful there; any other type would be routed to a
	// holding-register write via WriteRegister/WriteRegisters, silently writing
	// to the wrong memory area. Reject the misconfiguration explicitly instead.
	// (Discrete inputs are also rejected as read-only in Write(); this guard
	// covers the coil case and any direct writeTag caller.)
	if (ai.area == areaCoil || ai.area == areaDiscreteInput) && cmd.Type != core.TypeBool {
		return fmt.Errorf("tag %s: type %s is not supported on %s area (only bool is supported on coil/discrete-input areas)", cmd.Tag, cmd.Type, areaName(ai.area))
	}

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

func (b *modbusBase) Name() string { return b.name }
func (b *modbusBase) Type() string { return b.driverType }

func (b *modbusBase) Status() core.DriverStatus {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return core.DriverStatus{
		Name:           b.name,
		Type:           b.driverType,
		State:          b.state,
		LastRead:       b.lastRead,
		LastError:      b.lastError,
		TagCount:       len(b.tags),
		ReadCount:      b.readCount.Load(),
		ErrorCount:     b.errorCount.Load(),
		ReconnectCount: b.reconnectCount.Load(),
	}
}

func (b *modbusBase) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{
		CanRead:      true,
		CanWrite:     true,
		CanSubscribe: false,
		BatchRead:    true,
		MaxBatchSize: modbusMaxBatchSize, // Modbus FC03 max 125 registers per request (protocol limit)
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
	case num >= modbusHoldingRegStart && num <= modbusHoldingRegEnd:
		return addrInfo{area: areaHoldingRegister, addr: uint16(num - modbusHoldingRegStart)}, nil
	case num >= modbusInputRegisterStart && num <= modbusInputRegisterEnd:
		return addrInfo{area: areaInputRegister, addr: uint16(num - modbusInputRegisterStart)}, nil
	case num >= modbusDiscreteInputStart && num <= modbusDiscreteInputEnd:
		return addrInfo{area: areaDiscreteInput, addr: uint16(num - modbusDiscreteInputStart)}, nil
	case num >= modbusCoilStart && num <= modbusCoilEnd:
		return addrInfo{area: areaCoil, addr: uint16(num - modbusCoilStart)}, nil
	default:
		// Also support raw register addresses (0-based)
		return addrInfo{area: areaHoldingRegister, addr: uint16(num)}, nil
	}
}
