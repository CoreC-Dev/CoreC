package s7

import (
	"errors"
	"sync"
	"time"

	"github.com/robinson/gos7"
)

// mockAreaCall records a single AG read or write call to a memory area.
type mockAreaCall struct {
	area     string // "DB", "MB", "EB", "AB"
	dbNumber int
	start    int
	size     int
	data     []byte // copy of the buffer (read-into or write-from)
}

// mockS7Client is a test double for gos7.Client. It lets the S7 driver's
// Read/Write paths be exercised in-process without a real PLC.
//
//   - Read calls copy readData into the caller's buffer (truncated to the
//     requested size) and are recorded in readCalls. If readErr is set it is
//     returned instead.
//   - Write calls record a copy of the caller's buffer in writeCalls. If
//     writeErr is set it is returned instead.
//
// The bit-write path performs a read-modify-write: it reads one byte, flips
// the bit, then writes one byte back. mockS7Client supports this naturally
// because the read returns readData[0] and the subsequent write records the
// modified byte.
type mockS7Client struct {
	mu sync.Mutex

	// Data returned by AGRead* calls (copied into the caller's buffer).
	readData []byte
	// Error returned by AGRead* calls; takes precedence over readData.
	readErr error

	// Error returned by AGWrite* calls.
	writeErr error

	// Recorded call history (most recent last).
	readCalls  []mockAreaCall
	writeCalls []mockAreaCall
}

// lastWrite returns the most recently recorded write call, or false if none.
func (m *mockS7Client) lastWrite() (mockAreaCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.writeCalls) == 0 {
		return mockAreaCall{}, false
	}
	return m.writeCalls[len(m.writeCalls)-1], true
}

// lastRead returns the most recently recorded read call, or false if none.
func (m *mockS7Client) lastRead() (mockAreaCall, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.readCalls) == 0 {
		return mockAreaCall{}, false
	}
	return m.readCalls[len(m.readCalls)-1], true
}

// readInto copies m.readData into buffer (up to len(buffer)) and records the
// call. It returns m.readErr if set.
func (m *mockS7Client) readInto(area string, dbNumber, start, size int, buffer []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readCalls = append(m.readCalls, mockAreaCall{
		area: area, dbNumber: dbNumber, start: start, size: size, data: append([]byte(nil), buffer...),
	})
	if m.readErr != nil {
		return m.readErr
	}
	n := len(buffer)
	if n > len(m.readData) {
		n = len(m.readData)
	}
	copy(buffer, m.readData[:n])
	return nil
}

// writeFrom records a copy of buffer and returns m.writeErr if set.
func (m *mockS7Client) writeFrom(area string, dbNumber, start, size int, buffer []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writeCalls = append(m.writeCalls, mockAreaCall{
		area: area, dbNumber: dbNumber, start: start, size: size, data: append([]byte(nil), buffer...),
	})
	return m.writeErr
}

// ----- AG read/write methods (the ones the driver actually uses) -----

func (m *mockS7Client) AGReadDB(dbNumber, start, size int, buffer []byte) error {
	return m.readInto("DB", dbNumber, start, size, buffer)
}
func (m *mockS7Client) AGWriteDB(dbNumber, start, size int, buffer []byte) error {
	return m.writeFrom("DB", dbNumber, start, size, buffer)
}
func (m *mockS7Client) AGReadMB(start, size int, buffer []byte) error {
	return m.readInto("MB", 0, start, size, buffer)
}
func (m *mockS7Client) AGWriteMB(start, size int, buffer []byte) error {
	return m.writeFrom("MB", 0, start, size, buffer)
}
func (m *mockS7Client) AGReadEB(start, size int, buffer []byte) error {
	return m.readInto("EB", 0, start, size, buffer)
}
func (m *mockS7Client) AGWriteEB(start, size int, buffer []byte) error {
	return m.writeFrom("EB", 0, start, size, buffer)
}
func (m *mockS7Client) AGReadAB(start, size int, buffer []byte) error {
	return m.readInto("AB", 0, start, size, buffer)
}
func (m *mockS7Client) AGWriteAB(start, size int, buffer []byte) error {
	return m.writeFrom("AB", 0, start, size, buffer)
}

// ----- Remaining interface methods: stubbed, never used by the driver -----

func (m *mockS7Client) AGReadTM(start, size int, buffer []byte) error  { return nil }
func (m *mockS7Client) AGWriteTM(start, size int, buffer []byte) error { return nil }
func (m *mockS7Client) AGReadCT(start, size int, buffer []byte) error  { return nil }
func (m *mockS7Client) AGWriteCT(start, size int, buffer []byte) error { return nil }
func (m *mockS7Client) AGReadMulti(dataItems []gos7.S7DataItem, itemsCount int) error {
	return nil
}
func (m *mockS7Client) AGWriteMulti(dataItems []gos7.S7DataItem, itemsCount int) error {
	return nil
}
func (m *mockS7Client) DBFill(dbnumber, fillchar int) error { return nil }
func (m *mockS7Client) DBGet(dbnumber int, usrdata []byte, size int) error {
	return nil
}
func (m *mockS7Client) Read(variable string, buffer []byte) (interface{}, error) {
	return nil, nil //nolint:nilnil // mock: nil result with no error is the intended stub behaviour
}
func (m *mockS7Client) GetAgBlockInfo(blocktype, blocknum int) (gos7.S7BlockInfo, error) {
	return gos7.S7BlockInfo{}, nil
}
func (m *mockS7Client) PLCHotStart() error                    { return nil }
func (m *mockS7Client) PLCColdStart() error                   { return nil }
func (m *mockS7Client) PLCStop() error                        { return nil }
func (m *mockS7Client) PLCGetStatus() (status int, err error) { return 0, nil }
func (m *mockS7Client) PGListBlocks() (gos7.S7BlocksList, error) {
	return gos7.S7BlocksList{}, nil
}
func (m *mockS7Client) SetSessionPassword(password string) error { return nil }
func (m *mockS7Client) ClearSessionPassword() error              { return nil }
func (m *mockS7Client) GetProtection() (gos7.S7Protection, error) {
	return gos7.S7Protection{}, nil
}
func (m *mockS7Client) GetOrderCode() (gos7.S7OrderCode, error) {
	return gos7.S7OrderCode{}, nil
}
func (m *mockS7Client) GetCPUInfo() (gos7.S7CpuInfo, error) {
	return gos7.S7CpuInfo{}, nil
}
func (m *mockS7Client) GetCPInfo() (gos7.S7CpInfo, error) {
	return gos7.S7CpInfo{}, nil
}
func (m *mockS7Client) PGClockRead(datetime time.Time) error { return nil }
func (m *mockS7Client) PGClockWrite() (time.Time, error)     { return time.Time{}, nil }

// Compile-time assertion that mockS7Client satisfies gos7.Client.
var _ gos7.Client = (*mockS7Client)(nil)

// errMockPlc is a sentinel error used by tests that want to assert error
// wrapping without depending on a specific library error type.
var errMockPlc = errors.New("mock plc: simulated I/O failure")
