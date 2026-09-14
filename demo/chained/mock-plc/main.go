// mock-plc: a simulated Modbus TCP PLC for chained-core demos.
//
// Holds a float32 "temperature" in holding register 0 (CoreC address "40001")
// that oscillates around 25°C every second, plus a uint16 "counter" in
// holding register 2 (CoreC address "40003") that increments each tick.
// Supports writes (for command-downlink testing in scenario 7).
package main

import (
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/simonvetter/modbus"
)

func main() {
	h := &handler{}

	server, err := modbus.NewServer(&modbus.ServerConfiguration{
		URL:        "tcp://0.0.0.0:502",
		Timeout:    30 * time.Second,
		MaxClients: 10,
	}, h)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create server: %v\n", err)
		os.Exit(1)
	}
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("mock-plc: Modbus TCP server listening on :502 (slave-id 1)")

	ticker := time.NewTicker(1 * time.Second)
	t := 0.0
	for range ticker.C {
		if !h.isHolding() {
			temp := float32(25.0 + 5.0*math.Sin(t))
			h.setTemp(temp)
		}
		h.incCounter()
		t += 0.5
	}
}

type handler struct {
	mu        sync.RWMutex
	temp      float32   // holding reg 0-1 (big-endian float32)
	counter   uint16    // holding reg 2
	holdUntil time.Time // pause sine wave after a write
}

func (h *handler) setTemp(v float32) {
	h.mu.Lock()
	h.temp = v
	h.mu.Unlock()
}

func (h *handler) isHolding() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return time.Now().Before(h.holdUntil)
}

func (h *handler) incCounter() {
	h.mu.Lock()
	h.counter++
	h.mu.Unlock()
}

// tempRegs returns the two uint16 registers for the float32 temperature (big-endian).
func (h *handler) tempRegs() [2]uint16 {
	bits := math.Float32bits(h.temp)
	return [2]uint16{uint16(bits >> 16), uint16(bits & 0xFFFF)}
}

func (h *handler) HandleCoils(req *modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

func (h *handler) HandleDiscreteInputs(req *modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}

func (h *handler) HandleHoldingRegisters(req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if req.UnitId != 1 {
		return nil, modbus.ErrIllegalFunction
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if req.IsWrite {
		// Accept writes to register 0 (temperature) for command-downlink demo.
		for i, v := range req.Args {
			off := int(req.Addr) + i
			if off == 0 {
				// Reconstruct float32 from big-endian pair
				if len(req.Args) >= 2 {
					bits := uint32(req.Args[0])<<16 | uint32(req.Args[1])
					h.temp = math.Float32frombits(bits)
					h.holdUntil = time.Now().Add(10 * time.Second) // hold written value 10s
					fmt.Printf("mock-plc: WRITE temperature = %.2f (holding 10s)\n", h.temp)
				}
			}
			if off == 2 {
				h.counter = v
				fmt.Printf("mock-plc: WRITE counter = %d\n", h.counter)
			}
		}
		return nil, nil
	}

	// Read: serve registers 0-2 (temp hi, temp lo, counter)
	regs := make([]uint16, req.Quantity)
	tr := h.tempRegs()
	for i := 0; i < int(req.Quantity); i++ {
		addr := int(req.Addr) + i
		switch addr {
		case 0:
			regs[i] = tr[0]
		case 1:
			regs[i] = tr[1]
		case 2:
			regs[i] = h.counter
		default:
			regs[i] = 0
		}
	}
	return regs, nil
}

func (h *handler) HandleInputRegisters(req *modbus.InputRegistersRequest) ([]uint16, error) {
	return nil, modbus.ErrIllegalFunction
}
