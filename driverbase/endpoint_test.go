package driverbase

import (
	"testing"
)

func TestConnectionEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		typ      string
		settings map[string]any
		want     string
	}{
		{"modbus tcp", "modbus-tcp", map[string]any{"host": "plc", "port": float64(502)}, "plc:502"},
		{"modbus tcp no port", "modbus-tcp", map[string]any{"host": "plc"}, "plc"},
		{"modbus rtu", "modbus-rtu", map[string]any{"serial-device": "COM3"}, "COM3"},
		{"s7", "s7", map[string]any{"ip": "1.2.3.4", "rack": 0, "slot": 1}, "1.2.3.4 (rack 0, slot 1)"},
		{"opcua", "opcua", map[string]any{"endpoint": "opc.tcp://x:4840"}, "opc.tcp://x:4840"},
		{"generic broker", "mqtt", map[string]any{"broker": "tcp://b:1883"}, "tcp://b:1883"},
		{"empty", "opcua", map[string]any{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := connectionEndpoint(tc.typ, coreDriverConfig(tc.typ, tc.settings))
			if got != tc.want {
				t.Fatalf("connectionEndpoint(%q) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}
