package modbus

import (
	"fmt"
	"strconv"
)

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
