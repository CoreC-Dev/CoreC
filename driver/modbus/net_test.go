package modbus

import (
	"context"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

func TestModbusNetRTUOverTCPInit(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "rtu-tcp-gw",
		Type: "modbus-rtuovertcp",
		Settings: map[string]any{
			"host":     "192.168.1.50",
			"port":     502,
			"slave-id": 1,
			"timeout":  "2s",
		},
		Tags: []core.TagConfig{
			{Name: "temp", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusRTUOverTCPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUOverTCPDriver error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if drv.Type() != "modbus-rtuovertcp" {
		t.Errorf("Type: expected modbus-rtuovertcp, got %s", drv.Type())
	}

	d := drv.(*ModbusNetDriver)
	if d.urlScheme != "rtuovertcp" {
		t.Errorf("urlScheme: expected rtuovertcp, got %s", d.urlScheme)
	}
	if d.host != "192.168.1.50" {
		t.Errorf("host: expected 192.168.1.50, got %s", d.host)
	}
}

func TestModbusUDPInit(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "modbus-udp",
		Type: "modbus-udp",
		Settings: map[string]any{
			"host": "192.168.1.60",
			"port": 502,
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusUDPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusUDPDriver error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if drv.Type() != "modbus-udp" {
		t.Errorf("Type: expected modbus-udp, got %s", drv.Type())
	}

	d := drv.(*ModbusNetDriver)
	if d.urlScheme != "udp" {
		t.Errorf("urlScheme: expected udp, got %s", d.urlScheme)
	}
}

func TestModbusRTUOverUDPInit(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "rtu-udp",
		Type: "modbus-rtuoverudp",
		Settings: map[string]any{
			"host": "192.168.1.70",
			"port": 502,
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusRTUOverUDPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusRTUOverUDPDriver error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}
	if drv.Type() != "modbus-rtuoverudp" {
		t.Errorf("Type: expected modbus-rtuoverudp, got %s", drv.Type())
	}

	d := drv.(*ModbusNetDriver)
	if d.urlScheme != "rtuoverudp" {
		t.Errorf("urlScheme: expected rtuoverudp, got %s", d.urlScheme)
	}
}

func TestModbusNetInitMissingHost(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "no-host",
		Type:     "modbus-udp",
		Settings: map[string]any{},
		Tags:     []core.TagConfig{},
	}

	drv, err := NewModbusUDPDriver(cfg)
	if err != nil {
		t.Fatalf("NewModbusUDPDriver error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err == nil {
		t.Fatal("expected error for missing host, got nil")
	}
}

func TestModbusNetDefaultPort(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "default-port",
		Type: "modbus-rtuovertcp",
		Settings: map[string]any{
			"host": "192.168.1.50",
		},
		Tags: []core.TagConfig{
			{Name: "val", Address: "40001", Type: "uint16"},
		},
	}

	drv, err := NewModbusRTUOverTCPDriver(cfg)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init error: %v", err)
	}

	d := drv.(*ModbusNetDriver)
	if d.port != 502 {
		t.Errorf("default port: expected 502, got %d", d.port)
	}
}
