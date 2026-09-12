package modbus

import (
	"context"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	mb "github.com/simonvetter/modbus"
)

func TestModbusRTUInitValid(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "rtu-plc",
		Type: "modbus-rtu",
		Settings: map[string]any{
			"serial-device": "/dev/ttyUSB0",
			"baud-rate":     19200,
			"data-bits":     8,
			"parity":        "even",
			"stop-bits":     1,
			"slave-id":      2,
			"timeout":       "2s",
		},
		Tags: []core.TagConfig{
			{Name: "temp", Address: "40001", Type: "uint16"},
			{Name: "pump", Address: "00001", Type: "bool"},
		},
	}

	drv, err := NewModbusRTUDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUDriver error: %v", err)
	}

	ctx := context.Background()
	if err := drv.Init(ctx, cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}

	d := drv.(*ModbusRTUDriver)

	if d.serialDevice != "/dev/ttyUSB0" {
		t.Errorf("serialDevice: expected /dev/ttyUSB0, got %s", d.serialDevice)
	}
	if d.baudRate != 19200 {
		t.Errorf("baudRate: expected 19200, got %d", d.baudRate)
	}
	if d.dataBits != 8 {
		t.Errorf("dataBits: expected 8, got %d", d.dataBits)
	}
	if d.parity != mb.PARITY_EVEN {
		t.Errorf("parity: expected PARITY_EVEN, got %d", d.parity)
	}
	if d.stopBits != 1 {
		t.Errorf("stopBits: expected 1, got %d", d.stopBits)
	}
	if d.slaveID != 2 {
		t.Errorf("slaveID: expected 2, got %d", d.slaveID)
	}
	if drv.Type() != "modbus-rtu" {
		t.Errorf("Type: expected modbus-rtu, got %s", drv.Type())
	}
}

func TestModbusRTUInitDefaults(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "rtu-defaults",
		Type: "modbus-rtu",
		Settings: map[string]any{
			"serial-device": "/dev/ttyS0",
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusRTUDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUDriver error: %v", err)
	}

	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}

	d := drv.(*ModbusRTUDriver)

	// Defaults: baud 9600, dataBits 8, parity none, stopBits 0 (auto), slaveID 1
	if d.baudRate != 9600 {
		t.Errorf("default baudRate: expected 9600, got %d", d.baudRate)
	}
	if d.dataBits != 8 {
		t.Errorf("default dataBits: expected 8, got %d", d.dataBits)
	}
	if d.parity != mb.PARITY_NONE {
		t.Errorf("default parity: expected PARITY_NONE, got %d", d.parity)
	}
	if d.slaveID != 1 {
		t.Errorf("default slaveID: expected 1, got %d", d.slaveID)
	}
}

func TestModbusRTUInitMissingDevice(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "rtu-no-dev",
		Type:     "modbus-rtu",
		Settings: map[string]any{},
		Tags:     []core.TagConfig{},
	}

	drv, err := NewModbusRTUDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUDriver error: %v", err)
	}

	if err := drv.Init(context.Background(), cfg); err == nil {
		t.Fatal("expected error for missing serial-device, got nil")
	}
}

func TestModbusRTUParityParsing(t *testing.T) {
	tests := []struct {
		input string
		want  uint
	}{
		{"none", mb.PARITY_NONE},
		{"even", mb.PARITY_EVEN},
		{"odd", mb.PARITY_ODD},
		{"EVEN", mb.PARITY_EVEN}, // case-insensitive
		{"bogus", mb.PARITY_NONE},
	}
	for _, tt := range tests {
		got := parseParity(map[string]any{"parity": tt.input})
		if got != tt.want {
			t.Errorf("parseParity(%q): expected %d, got %d", tt.input, tt.want, got)
		}
	}

	// Missing parity key → default none
	if got := parseParity(map[string]any{}); got != mb.PARITY_NONE {
		t.Errorf("parseParity(missing): expected PARITY_NONE, got %d", got)
	}
}

func TestModbusRTUCapabilities(t *testing.T) {
	drv, err := NewModbusRTUDriver(core.DriverConfig{Name: "rtu-cap"})
	if err != nil {
		t.Fatalf("NewModbusRTUDriver error: %v", err)
	}
	caps := drv.Capabilities()
	if !caps.CanRead || !caps.CanWrite || caps.CanSubscribe {
		t.Errorf("unexpected capabilities: %+v", caps)
	}
	if caps.MaxBatchSize != 125 {
		t.Errorf("MaxBatchSize: expected 125, got %d", caps.MaxBatchSize)
	}
}
