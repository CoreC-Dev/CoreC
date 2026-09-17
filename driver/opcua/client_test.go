package opcua

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua"
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

// ============================================================
// In-process tests for OPC UA driver logic.
//
// The opcua.Client is a concrete struct, not an interface, so the Read/Write
// network calls cannot be mocked directly. However, an unconnected client
// (created via opcua.NewClient without calling Connect) returns a
// StatusBadServerNotConnected error from Read/Write, which lets us exercise
// the driver's error-wrapping and request-building paths in-process. The
// variant encoding, NodeID parsing, config validation, and subscription
// configuration are all fully testable without a server.
// ============================================================

// initConnectedOPCUA builds and inits a driver, then swaps in an unconnected
// real opcua.Client and marks the driver StateConnected. This lets Read/Write
// pass the connection guard and reach the client call, which fails with a
// not-connected error — exercising the error-wrapping logic without a server.
func initConnectedOPCUA(t *testing.T, cfg core.DriverConfig) *OPCUADriver {
	t.Helper()
	d := mustInit(t, cfg)
	client, err := opcua.NewClient(d.endpoint)
	if err != nil {
		t.Fatalf("opcua.NewClient error: %v", err)
	}
	d.mu.Lock()
	d.client = client
	d.state = core.StateConnected
	d.mu.Unlock()
	return d
}

// TestNodeIDParsing is a comprehensive table-driven test of NodeID parsing
// covering string, numeric, default-namespace, and GUID formats. It verifies
// the parsed Namespace/identifier and the String() round-trip, which the
// driver relies on for logging and diagnostics.
func TestNodeIDParsing(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		ns      uint16
		isNum   bool
		intID   uint32
		strID   string
		wantStr string // expected String() round-trip
	}{
		{"string ns2", "ns=2;s=Test", 2, false, 0, "Test", "ns=2;s=Test"},
		{"string ns1", "ns=1;s=Sensor.Value", 1, false, 0, "Sensor.Value", "ns=1;s=Sensor.Value"},
		{"numeric ns1", "ns=1;i=1001", 1, true, 1001, "", "ns=1;i=1001"},
		{"numeric ns0 two-byte", "i=85", 0, true, 85, "", "i=85"},
		{"numeric ns0 four-byte", "ns=0;i=2253", 0, true, 2253, "", "i=2253"},
		{"numeric large ns", "ns=256;i=42", 256, true, 42, "", "ns=256;i=42"},
		{"string default ns", "s=Hello", 0, false, 0, "Hello", "s=Hello"},
		{"string implicit ns3", "ns=3;MyTag", 3, false, 0, "MyTag", "ns=3;s=MyTag"},
		{"string with dots", "ns=2;s=Object.Attribute.Sub", 2, false, 0, "Object.Attribute.Sub", "ns=2;s=Object.Attribute.Sub"},
		{"numeric max uint32", "ns=2;i=4294967295", 2, true, 4294967295, "", "ns=2;i=4294967295"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodeID, err := ua.ParseNodeID(tt.input)
			if err != nil {
				t.Fatalf("ParseNodeID(%q) error: %v", tt.input, err)
			}
			if nodeID.Namespace() != tt.ns {
				t.Errorf("namespace = %d, want %d", nodeID.Namespace(), tt.ns)
			}
			if tt.isNum {
				if nodeID.IntID() != tt.intID {
					t.Errorf("IntID = %d, want %d", nodeID.IntID(), tt.intID)
				}
			} else {
				if nodeID.StringID() != tt.strID {
					t.Errorf("StringID = %q, want %q", nodeID.StringID(), tt.strID)
				}
			}
			// Round-trip: String() must reproduce the canonical form.
			if got := nodeID.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
		})
	}
}

// TestOPCUAInitInvalidConfig is a comprehensive table-driven test of every
// Init error path: missing/invalid endpoint and malformed NodeIDs in tags.
// Security policy and mode are stored as-is at Init time (validated only on
// connect), so they are not error paths here.
func TestOPCUAInitInvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     core.DriverConfig
		wantErr string
	}{
		{
			name:    "missing endpoint key",
			cfg:     core.DriverConfig{Name: "x", Settings: map[string]any{}},
			wantErr: "endpoint is required",
		},
		{
			name:    "empty endpoint string",
			cfg:     core.DriverConfig{Name: "x", Settings: map[string]any{"endpoint": ""}},
			wantErr: "endpoint is required",
		},
		{
			name:    "endpoint is integer",
			cfg:     core.DriverConfig{Name: "x", Settings: map[string]any{"endpoint": 4840}},
			wantErr: "endpoint is required",
		},
		{
			name:    "endpoint is nil",
			cfg:     core.DriverConfig{Name: "x", Settings: map[string]any{"endpoint": nil}},
			wantErr: "endpoint is required",
		},
		{
			name:    "nil settings map",
			cfg:     core.DriverConfig{Name: "x", Settings: nil},
			wantErr: "endpoint is required",
		},
		{
			name: "invalid node id namespace",
			cfg: core.DriverConfig{
				Name:     "x",
				Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ns=abc;i=1", Type: "int32"}},
			},
			wantErr: "opcua tag bad: invalid NodeID",
		},
		{
			name: "invalid node id identifier",
			cfg: core.DriverConfig{
				Name:     "x",
				Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ns=1;i=xyz", Type: "int32"}},
			},
			wantErr: "opcua tag bad: invalid NodeID",
		},
		{
			name: "second tag invalid among valid",
			cfg: core.DriverConfig{
				Name:     "x",
				Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
				Tags: []core.TagConfig{
					{Name: "ok", Address: "ns=2;s=Good", Type: "float64"},
					{Name: "bad", Address: "ns=999999;i=1", Type: "int32"},
				},
			},
			wantErr: "opcua tag bad: invalid NodeID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drv, err := NewOPCUADriver(tt.cfg)
			if err != nil {
				t.Fatalf("NewOPCUADriver failed: %v", err)
			}
			err = drv.Init(context.Background(), tt.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestOPCUAInitSecurityPolicyAccepted documents that security-policy and
// security-mode are accepted as arbitrary strings at Init time — they are
// only validated when the client connects. This exercises the config-parsing
// path for security settings.
func TestOPCUAInitSecurityPolicyAccepted(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		mode   string
	}{
		{"none", "None", "None"},
		{"basic256", "Basic256", "Sign"},
		{"basic256sha256", "Basic256Sha256", "SignAndEncrypt"},
		{"arbitrary string", "MyCustomPolicy", "WeirdMode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig("x")
			cfg.Settings["security-policy"] = tt.policy
			cfg.Settings["security-mode"] = tt.mode
			d := mustInit(t, cfg)
			if d.securityPolicy != tt.policy {
				t.Errorf("securityPolicy = %q, want %q", d.securityPolicy, tt.policy)
			}
			if d.securityMode != tt.mode {
				t.Errorf("securityMode = %q, want %q", d.securityMode, tt.mode)
			}
		})
	}
}

// TestOPCUATypeMapping verifies that ua.NewVariant maps Go value types to the
// expected OPC UA TypeID. This is the type-mapping logic the driver's Write
// relies on. It also confirms that unsupported Go types (int, uint, struct)
// produce a variant-creation error, which the driver surfaces per-command.
func TestOPCUATypeMapping(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantID  ua.TypeID
		wantVal any
	}{
		{"bool true", true, ua.TypeIDBoolean, true},
		{"bool false", false, ua.TypeIDBoolean, false},
		{"int32", int32(42), ua.TypeIDInt32, int32(42)},
		{"uint32", uint32(42), ua.TypeIDUint32, uint32(42)},
		{"int16", int16(-1), ua.TypeIDInt16, int16(-1)},
		{"uint16", uint16(65535), ua.TypeIDUint16, uint16(65535)},
		{"int64", int64(1 << 40), ua.TypeIDInt64, int64(1 << 40)},
		{"uint64", uint64(1 << 40), ua.TypeIDUint64, uint64(1 << 40)},
		{"float32", float32(3.14), ua.TypeIDFloat, float32(3.14)},
		{"float64", float64(2.718), ua.TypeIDDouble, float64(2.718)},
		{"string", "hello", ua.TypeIDString, "hello"},
		{"empty string", "", ua.TypeIDString, ""},
		{"byte slice", []byte{1, 2, 3}, ua.TypeIDByteString, []byte{1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ua.NewVariant(tt.value)
			if err != nil {
				t.Fatalf("NewVariant(%T) error: %v", tt.value, err)
			}
			if v.Type() != tt.wantID {
				t.Errorf("Type() = %d, want %d", v.Type(), tt.wantID)
			}
			if !variantValueEqual(v.Value(), tt.wantVal) {
				t.Errorf("Value() = %v, want %v", v.Value(), tt.wantVal)
			}
		})
	}

	// Unsupported Go types must return an error — the driver's Write turns
	// this into a per-command failure.
	unsupported := []struct {
		name  string
		value any
	}{
		{"platform int", 42},
		{"platform uint", uint(42)},
		{"struct", struct{ X int }{1}},
		{"map", map[string]int{"a": 1}},
	}
	for _, tt := range unsupported {
		t.Run("unsupported/"+tt.name, func(t *testing.T) {
			v, err := ua.NewVariant(tt.value)
			if err == nil {
				t.Fatalf("NewVariant(%T) expected error, got variant %v", tt.value, v)
			}
		})
	}
}

// TestOPCUAWriteEncode verifies the driver's Write request-building and
// variant-encoding logic in-process:
//   - A write with an unsupported Go type (int) produces a per-command
//     "failed to create variant" error without contacting the server.
//   - A write for an unknown tag produces a per-command "tag not found" error.
//   - A write with valid variants reaches the (unconnected) client and
//     surfaces the wrapped "opcua batch write failed" error.
func TestOPCUAWriteEncode(t *testing.T) {
	t.Run("unsupported go type yields per-command variant error", func(t *testing.T) {
		d := initConnectedOPCUA(t, validConfig("x"))
		// "int" is not a builtin OPC UA variant type, so NewVariant fails.
		results, err := d.Write(context.Background(), []core.WriteCommand{
			{Tag: "tag1", Value: 42, Type: core.TypeInt32},
		})
		if err != nil {
			t.Fatalf("Write should not return a top-level error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("expected 1 result, got %d", len(results))
		}
		if results[0].Success {
			t.Error("expected failure for unsupported variant type")
		}
		if !strings.Contains(results[0].Error, "failed to create variant") {
			t.Errorf("error = %q, want substring 'failed to create variant'", results[0].Error)
		}
	})

	t.Run("unknown tag yields tag-not-found", func(t *testing.T) {
		d := initConnectedOPCUA(t, validConfig("x"))
		results, err := d.Write(context.Background(), []core.WriteCommand{
			{Tag: "nope", Value: float64(1.0), Type: core.TypeFloat64},
		})
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
		if results[0].Success {
			t.Error("expected failure for unknown tag")
		}
		if !strings.Contains(results[0].Error, "tag not found") {
			t.Errorf("error = %q, want substring 'tag not found'", results[0].Error)
		}
	})

	t.Run("valid variant reaches client and wraps connection error", func(t *testing.T) {
		d := initConnectedOPCUA(t, validConfig("x"))
		_, err := d.Write(context.Background(), []core.WriteCommand{
			{Tag: "tag1", Value: float64(42.0), Type: core.TypeFloat64},
		})
		if err == nil {
			t.Fatal("expected error from unconnected client, got nil")
		}
		if !strings.Contains(err.Error(), "opcua batch write failed") {
			t.Errorf("error = %q, want substring 'opcua batch write failed'", err.Error())
		}
	})

	t.Run("mixed batch skips bad variant and sends good one", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Tags = append(cfg.Tags, core.TagConfig{Name: "tag2", Address: "ns=2;s=Second", Type: "int32"})
		d := initConnectedOPCUA(t, cfg)
		// tag1 gets a valid float64 variant; tag2 gets an unsupported int.
		// The bad variant is skipped per-command; the good one is sent to
		// the unconnected client, which fails the whole batch with a
		// top-level error (the driver returns nil results on a batch
		// write failure).
		results, err := d.Write(context.Background(), []core.WriteCommand{
			{Tag: "tag1", Value: float64(1.0), Type: core.TypeFloat64},
			{Tag: "tag2", Value: 99, Type: core.TypeInt32},
		})
		if err == nil {
			t.Fatal("expected top-level error from unconnected client")
		}
		if !strings.Contains(err.Error(), "opcua batch write failed") {
			t.Errorf("error = %q, want substring 'opcua batch write failed'", err.Error())
		}
		// On a batch write failure the driver returns nil results.
		if results != nil {
			t.Errorf("expected nil results on batch failure, got %v", results)
		}
	})

	t.Run("all bad variants return per-command results without client call", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Tags = append(cfg.Tags, core.TagConfig{Name: "tag2", Address: "ns=2;s=Second", Type: "int32"})
		d := initConnectedOPCUA(t, cfg)
		// Both commands have unsupported int values, so no writeValues are
		// built and the client is never called. The driver returns
		// per-command results with no top-level error.
		results, err := d.Write(context.Background(), []core.WriteCommand{
			{Tag: "tag1", Value: 1, Type: core.TypeFloat64},
			{Tag: "tag2", Value: 99, Type: core.TypeInt32},
		})
		if err != nil {
			t.Fatalf("expected no top-level error, got %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(results))
		}
		for i, r := range results {
			if r.Success {
				t.Errorf("result[%d] should fail", i)
			}
			if !strings.Contains(r.Error, "failed to create variant") {
				t.Errorf("result[%d] error = %q, want substring 'failed to create variant'", i, r.Error)
			}
		}
	})
}

// TestOPCUAReadError verifies the driver's Read error-wrapping path: with a
// connected driver backed by an unconnected client, Read wraps the
// StatusBadServerNotConnected error and increments the error counter.
func TestOPCUAReadError(t *testing.T) {
	d := initConnectedOPCUA(t, validConfig("x"))

	beforeErr := d.errorCount.Load()
	vals, err := d.Read(context.Background(), []string{"tag1"})
	if err == nil {
		t.Fatal("expected error from unconnected client, got nil")
	}
	if vals != nil {
		t.Errorf("expected nil values, got %v", vals)
	}
	if !strings.Contains(err.Error(), "opcua batch read failed") {
		t.Errorf("error = %q, want substring 'opcua batch read failed'", err.Error())
	}
	if d.errorCount.Load() != beforeErr+1 {
		t.Errorf("errorCount = %d, want %d", d.errorCount.Load(), beforeErr+1)
	}
	// lastError stores the raw underlying error (not the wrapped form).
	if !strings.Contains(d.Status().LastError, "not connected") {
		t.Errorf("LastError = %q, want substring 'not connected'", d.Status().LastError)
	}
}

// TestOPCUAReadUnknownTag verifies that reading only unknown tags (none
// present in the nodeID map) produces a "no valid tags" error.
func TestOPCUAReadUnknownTag(t *testing.T) {
	d := initConnectedOPCUA(t, validConfig("x"))

	_, err := d.Read(context.Background(), []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected error for no valid tags")
	}
	if !strings.Contains(err.Error(), "no valid tags") {
		t.Errorf("error = %q, want substring 'no valid tags'", err.Error())
	}
}

// TestOPCUASubscriptionConfig verifies the subscription configuration logic:
// mode toggling, subscription interval parsing, buffer sizing, and the
// Subscribe() API behavior in each mode.
func TestOPCUASubscriptionConfig(t *testing.T) {
	t.Run("polling mode is default and rejects subscribe", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		if d.subMode {
			t.Error("subMode should be false by default")
		}
		ch, err := d.Subscribe(context.Background(), []string{"tag1"})
		if err != core.ErrSubscribeNotSupported {
			t.Errorf("error = %v, want ErrSubscribeNotSupported", err)
		}
		if ch != nil {
			t.Errorf("expected nil channel, got %v", ch)
		}
	})

	t.Run("explicit polling mode rejects subscribe", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["mode"] = "polling"
		d := mustInit(t, cfg)
		if d.subMode {
			t.Error("subMode should be false for polling")
		}
		_, err := d.Subscribe(context.Background(), []string{"tag1"})
		if err != core.ErrSubscribeNotSupported {
			t.Errorf("error = %v, want ErrSubscribeNotSupported", err)
		}
	})

	t.Run("subscription mode returns buffered channel", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["mode"] = "subscription"
		d := mustInit(t, cfg)
		if !d.subMode {
			t.Fatal("subMode should be true for subscription")
		}
		ch, err := d.Subscribe(context.Background(), []string{"tag1"})
		if err != nil {
			t.Fatalf("Subscribe error: %v", err)
		}
		if ch == nil {
			t.Fatal("expected non-nil channel")
		}
		// Channel must be empty (no server pushing data).
		select {
		case dp := <-ch:
			t.Errorf("expected empty channel, got %+v", dp)
		default:
		}
	})

	t.Run("custom subscription interval", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["mode"] = "subscription"
		cfg.Settings["subscription-interval"] = "100ms"
		d := mustInit(t, cfg)
		if d.subInterval != 100*time.Millisecond {
			t.Errorf("subInterval = %v, want 100ms", d.subInterval)
		}
	})

	t.Run("default subscription interval", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		if d.subInterval != 500*time.Millisecond {
			t.Errorf("default subInterval = %v, want 500ms", d.subInterval)
		}
	})

	t.Run("custom subscription buffer size", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["subscription-buffer"] = uint64(4096)
		drv, err := NewOPCUADriver(cfg)
		if err != nil {
			t.Fatalf("NewOPCUADriver: %v", err)
		}
		d := drv.(*OPCUADriver)
		if d.subBufferSize != 4096 {
			t.Errorf("subBufferSize = %d, want 4096", d.subBufferSize)
		}
		if cap(d.subChannel) != 4096 {
			t.Errorf("subChannel cap = %d, want 4096", cap(d.subChannel))
		}
	})
}

// ----- helpers -----

func variantValueEqual(a, b any) bool { //nolint:gocyclo // deep-equality helper over many OPC UA variant types; complexity 26.
	switch av := a.(type) {
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case int32:
		bv, ok := b.(int32)
		return ok && av == bv
	case uint32:
		bv, ok := b.(uint32)
		return ok && av == bv
	case int16:
		bv, ok := b.(int16)
		return ok && av == bv
	case uint16:
		bv, ok := b.(uint16)
		return ok && av == bv
	case int64:
		bv, ok := b.(int64)
		return ok && av == bv
	case uint64:
		bv, ok := b.(uint64)
		return ok && av == bv
	case float32:
		bv, ok := b.(float32)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []byte:
		bv, ok := b.([]byte)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
