package modbus

import (
	"context"
	"fmt"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

func (b *modbusBase) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	b.RLock()
	client := b.client
	state := b.GetState()
	b.RUnlock()

	if state != core.StateConnected || client == nil {
		return nil, fmt.Errorf("driver %s is not connected", b.Name())
	}

	results := make([]core.WriteResult, len(commands))
	for i, cmd := range commands {
		b.RLock()
		ai, ok := b.addrs[cmd.Tag]
		b.RUnlock()

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
			b.RecordError()
			results[i] = core.WriteResult{Success: false, Error: err.Error()}
			b.Logger().Error("modbus write failed", "tag", cmd.Tag, "error", err)
		} else {
			results[i] = core.WriteResult{Success: true}
			b.Logger().Info("modbus write success", "tag", cmd.Tag, "value", cmd.Value)
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
