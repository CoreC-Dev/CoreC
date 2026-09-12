package opcua

import (
	"context"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua/ua"
)

func TestOPCUANodeIDParsing(t *testing.T) {
	tests := []struct {
		input       string
		namespace   uint16
		isString    bool
		stringValue string
		numericVal  uint32
	}{
		{
			input:       "ns=2;s=Conveyor.Speed",
			namespace:   2,
			isString:    true,
			stringValue: "Conveyor.Speed",
		},
		{
			input:      "ns=1;i=1001",
			namespace:  1,
			isString:   false,
			numericVal: 1001,
		},
	}

	for _, tt := range tests {
		nodeID, err := ua.ParseNodeID(tt.input)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", tt.input, err)
		}
		if nodeID.Namespace() != tt.namespace {
			t.Errorf("%s: expected namespace %d, got %d", tt.input, tt.namespace, nodeID.Namespace())
		}
		if tt.isString {
			if nodeID.StringID() != tt.stringValue {
				t.Errorf("%s: expected string ID %s, got %s", tt.input, tt.stringValue, nodeID.StringID())
			}
		} else {
			if nodeID.IntID() != tt.numericVal {
				t.Errorf("%s: expected int ID %d, got %d", tt.input, tt.numericVal, nodeID.IntID())
			}
		}
	}
}

func TestOPCUAInitValidation(t *testing.T) {
	cfg := core.DriverConfig{
		Name: "test-opcua",
		Type: "opcua",
		Settings: map[string]any{
			"endpoint": "opc.tcp://127.0.0.1:4840",
			"mode":     "polling",
		},
		Tags: []core.TagConfig{
			{Name: "speed", Address: "ns=2;s=Conveyor.Speed", Type: "float64"},
			{Name: "count", Address: "ns=1;i=1001", Type: "uint32"},
		},
	}

	drv, err := NewOPCUADriver(cfg)
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}

	ctx := context.Background()
	if err := drv.Init(ctx, cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	opcDrv := drv.(*OPCUADriver)
	if len(opcDrv.nodeIDs) != 2 {
		t.Errorf("expected 2 parsed node IDs, got %d", len(opcDrv.nodeIDs))
	}
	if opcDrv.endpoint != "opc.tcp://127.0.0.1:4840" {
		t.Errorf("expected endpoint 'opc.tcp://127.0.0.1:4840', got %s", opcDrv.endpoint)
	}
}
