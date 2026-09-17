package opcua

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua/ua"
)

// validConfig returns a minimal, fully-valid driver config that Init will
// accept without contacting a server. Tests mutate copies of it as needed.
func validConfig(name string) core.DriverConfig {
	return core.DriverConfig{
		Name:     name,
		Type:     "opcua",
		Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
		Tags: []core.TagConfig{
			{Name: "tag1", Address: "ns=2;s=Sensor.Value", Type: "float64"},
		},
	}
}

// mustInit is a helper that builds and inits a driver, failing the test on error.
func mustInit(t *testing.T, cfg core.DriverConfig) *OPCUADriver {
	t.Helper()
	drv, err := NewOPCUADriver(cfg)
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	return drv.(*OPCUADriver)
}

// ---------------------------------------------------------------------------
// NewOPCUADriver
// ---------------------------------------------------------------------------

func TestNewOPCUADriver(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "opcua-1",
		Type:     "opcua",
		Settings: map[string]any{},
	}
	drv, err := NewOPCUADriver(cfg)
	if err != nil {
		t.Fatalf("NewOPCUADriver returned error: %v", err)
	}
	if drv == nil {
		t.Fatal("NewOPCUADriver returned nil driver")
	}
	d := drv.(*OPCUADriver)

	// Name and type are derived from the config immediately.
	if d.name != "opcua-1" {
		t.Errorf("expected name %q, got %q", "opcua-1", d.name)
	}
	if d.Name() != "opcua-1" {
		t.Errorf("Name() = %q, want %q", d.Name(), "opcua-1")
	}
	if d.Type() != "opcua" {
		t.Errorf("Type() = %q, want %q", d.Type(), "opcua")
	}

	// A freshly-created driver is disconnected with no client and empty maps.
	if d.state != core.StateDisconnected {
		t.Errorf("expected initial state StateDisconnected, got %s", d.state)
	}
	if d.client != nil {
		t.Error("expected nil client before Start")
	}
	if d.tags == nil || d.nodeIDs == nil {
		t.Error("tags/nodeIDs maps should be initialized, not nil")
	}
	if len(d.tags) != 0 || len(d.nodeIDs) != 0 {
		t.Errorf("expected empty tag maps, got tags=%d nodeIDs=%d", len(d.tags), len(d.nodeIDs))
	}

	// The subscription channel must be ready (buffered) with the default size.
	if d.subChannel == nil {
		t.Error("subChannel should be initialized")
	}
	if d.subBufferSize != 1024 {
		t.Errorf("default subBufferSize = %d, want 1024", d.subBufferSize)
	}
	if cap(d.subChannel) != 1024 {
		t.Errorf("default subChannel capacity = %d, want 1024", cap(d.subChannel))
	}

	// Counters start at zero.
	if d.readCount.Load() != 0 || d.errorCount.Load() != 0 {
		t.Errorf("counters should start at zero, got read=%d err=%d", d.readCount.Load(), d.errorCount.Load())
	}
}

func TestNewOPCUADriverSubBufferSize(t *testing.T) {
	tests := []struct {
		name    string
		setting any // value for "subscription-buffer"; nil means omit the key
		want    int
	}{
		{"key missing", nil, 1024},
		{"int positive", 2048, 2048},
		{"uint64 positive (goccy/go-yaml style)", uint64(2048), 2048},
		{"int zero falls back to default", 0, 1024},
		{"uint64 zero falls back to default", uint64(0), 1024},
		{"int negative falls back to default", -100, 1024},
		{"float64 positive (yaml.v3 style)", 4096.0, 4096},
		{"float64 zero falls back to default", 0.0, 1024},
		{"float64 negative falls back to default", -1.0, 1024},
		{"string ignored", "512", 1024},
		{"bool ignored", true, 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := map[string]any{}
			if tt.setting != nil {
				settings["subscription-buffer"] = tt.setting
			}
			drv, err := NewOPCUADriver(core.DriverConfig{Name: "x", Settings: settings})
			if err != nil {
				t.Fatalf("NewOPCUADriver failed: %v", err)
			}
			d := drv.(*OPCUADriver)
			if d.subBufferSize != tt.want {
				t.Errorf("subBufferSize = %d, want %d", d.subBufferSize, tt.want)
			}
			if cap(d.subChannel) != tt.want {
				t.Errorf("subChannel capacity = %d, want %d", cap(d.subChannel), tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Init — config parsing
// ---------------------------------------------------------------------------

func TestInitConfigParsing(t *testing.T) { //nolint:gocyclo // exhaustive config-parsing table test; complexity 22.
	cfg := core.DriverConfig{
		Name: "opcua-full",
		Type: "opcua",
		Settings: map[string]any{
			"endpoint":               "opc.tcp://192.168.1.10:4840",
			"security-policy":        "Basic256Sha256",
			"security-mode":          "SignAndEncrypt",
			"username":               "admin",
			"password":               "secret",
			"cert-file":              "/path/cert.pem",
			"key-file":               "/path/key.pem",
			"mode":                   "subscription",
			"subscription-interval":  "250ms",
			"timeout":                "3s",
			"reconnect-interval":     "1s",
			"reconnect-max-interval": "10s",
			"max-batch-size":         uint64(500),
		},
		Tags: []core.TagConfig{
			{Name: "speed", Address: "ns=2;s=Conveyor.Speed", Type: "float64"},
			{Name: "count", Address: "ns=1;i=1001", Type: "uint32"},
		},
	}
	d := mustInit(t, cfg)

	// Endpoint and security credentials.
	if d.endpoint != "opc.tcp://192.168.1.10:4840" {
		t.Errorf("endpoint = %q", d.endpoint)
	}
	if d.securityPolicy != "Basic256Sha256" {
		t.Errorf("securityPolicy = %q", d.securityPolicy)
	}
	if d.securityMode != "SignAndEncrypt" {
		t.Errorf("securityMode = %q", d.securityMode)
	}
	if d.username != "admin" {
		t.Errorf("username = %q", d.username)
	}
	if d.password != "secret" {
		t.Errorf("password = %q", d.password)
	}
	if d.certFile != "/path/cert.pem" {
		t.Errorf("certFile = %q", d.certFile)
	}
	if d.keyFile != "/path/key.pem" {
		t.Errorf("keyFile = %q", d.keyFile)
	}

	// Mode and durations.
	if !d.subMode {
		t.Error("subscription mode should be enabled for mode=subscription")
	}
	if d.subInterval != 250*time.Millisecond {
		t.Errorf("subInterval = %v, want 250ms", d.subInterval)
	}
	if d.timeout != 3*time.Second {
		t.Errorf("timeout = %v, want 3s", d.timeout)
	}
	if d.reconnectBackoff != 1*time.Second {
		t.Errorf("reconnectBackoff = %v, want 1s", d.reconnectBackoff)
	}
	if d.maxReconnectBackoff != 10*time.Second {
		t.Errorf("maxReconnectBackoff = %v, want 10s", d.maxReconnectBackoff)
	}
	if d.maxBatchSize != 500 {
		t.Errorf("maxBatchSize = %d, want 500", d.maxBatchSize)
	}

	// Tags and parsed NodeIDs.
	if len(d.tags) != 2 || len(d.nodeIDs) != 2 {
		t.Fatalf("expected 2 tags/nodeIDs, got tags=%d nodeIDs=%d", len(d.tags), len(d.nodeIDs))
	}
	speedID, ok := d.nodeIDs["speed"]
	if !ok {
		t.Fatal("missing nodeID for tag 'speed'")
	}
	if speedID.Namespace() != 2 || speedID.StringID() != "Conveyor.Speed" {
		t.Errorf("speed nodeID = ns=%d;s=%s", speedID.Namespace(), speedID.StringID())
	}
	countID, ok := d.nodeIDs["count"]
	if !ok {
		t.Fatal("missing nodeID for tag 'count'")
	}
	if countID.Namespace() != 1 || countID.IntID() != 1001 {
		t.Errorf("count nodeID = ns=%d;i=%d", countID.Namespace(), countID.IntID())
	}
}

func TestInitDefaults(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "opcua-defaults",
		Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
	}
	d := mustInit(t, cfg)

	if d.timeout != 5*time.Second {
		t.Errorf("default timeout = %v, want 5s", d.timeout)
	}
	if d.subInterval != 500*time.Millisecond {
		t.Errorf("default subInterval = %v, want 500ms", d.subInterval)
	}
	if d.reconnectBackoff != core.DefaultReconnectBackoff {
		t.Errorf("default reconnectBackoff = %v, want %v", d.reconnectBackoff, core.DefaultReconnectBackoff)
	}
	if d.maxReconnectBackoff != core.DefaultMaxReconnectBackoff {
		t.Errorf("default maxReconnectBackoff = %v, want %v", d.maxReconnectBackoff, core.DefaultMaxReconnectBackoff)
	}
	if d.maxBatchSize != 1000 {
		t.Errorf("default maxBatchSize = %d, want 1000", d.maxBatchSize)
	}
	if d.subMode {
		t.Error("default mode should be polling (subMode=false)")
	}
	// Unset security/credential fields stay empty.
	for _, v := range []string{d.securityPolicy, d.securityMode, d.username, d.password, d.certFile, d.keyFile} {
		if v != "" {
			t.Errorf("expected empty security/credential field, got %q", v)
		}
	}
}

// TestInitYAMLNumberBatchSize verifies that max-batch-size supplied as
// numeric types produced by YAML unmarshalling is coerced to int.
// goccy/go-yaml parses non-negative integers as uint64 and floats as
// float64; both must be handled correctly.
func TestInitYAMLNumberBatchSize(t *testing.T) {
	t.Run("uint64 (goccy/go-yaml non-negative int)", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["max-batch-size"] = uint64(250)
		d := mustInit(t, cfg)
		if d.maxBatchSize != 250 {
			t.Errorf("maxBatchSize from uint64 = %d, want 250", d.maxBatchSize)
		}
	})
	t.Run("float64 (yaml.v3 / goccy float)", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["max-batch-size"] = 250.0
		d := mustInit(t, cfg)
		if d.maxBatchSize != 250 {
			t.Errorf("maxBatchSize from float64 = %d, want 250", d.maxBatchSize)
		}
	})
}

// ---------------------------------------------------------------------------
// Init — error paths
// ---------------------------------------------------------------------------

func TestInitErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		cfg     core.DriverConfig
		wantErr string // substring expected in the error message
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
			name:    "non-string endpoint (int)",
			cfg:     core.DriverConfig{Name: "x", Settings: map[string]any{"endpoint": 4840}},
			wantErr: "endpoint is required",
		},
		{
			name: "invalid node id in tags",
			cfg: core.DriverConfig{
				Name:     "x",
				Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ns=abc;i=1", Type: "int32"}},
			},
			wantErr: "opcua tag bad: invalid NodeID",
		},
		{
			name: "node id numeric out of range",
			cfg: core.DriverConfig{
				Name:     "x",
				Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ns=1;i=4294967296", Type: "int32"}},
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

// ---------------------------------------------------------------------------
// Name / Type / Status
// ---------------------------------------------------------------------------

func TestName(t *testing.T) {
	drv, err := NewOPCUADriver(core.DriverConfig{Name: "plant-opcua", Settings: map[string]any{}})
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}
	if got := drv.Name(); got != "plant-opcua" {
		t.Errorf("Name() = %q, want %q", got, "plant-opcua")
	}
}

func TestType(t *testing.T) {
	drv, err := NewOPCUADriver(core.DriverConfig{Name: "x", Settings: map[string]any{}})
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}
	if got := drv.Type(); got != "opcua" {
		t.Errorf("Type() = %q, want %q", got, "opcua")
	}
}

func TestStatus(t *testing.T) {
	cfg := validConfig("status-opcua")
	drv, err := NewOPCUADriver(cfg)
	if err != nil {
		t.Fatalf("NewOPCUADriver failed: %v", err)
	}

	// Before Init: disconnected, zero tags, zero counters.
	st := drv.Status()
	if st.Name != "status-opcua" {
		t.Errorf("Status().Name = %q", st.Name)
	}
	if st.Type != "opcua" {
		t.Errorf("Status().Type = %q", st.Type)
	}
	if st.State != core.StateDisconnected {
		t.Errorf("Status().State = %s, want disconnected", st.State)
	}
	if st.TagCount != 0 {
		t.Errorf("Status().TagCount = %d, want 0", st.TagCount)
	}
	if st.ReadCount != 0 || st.ErrorCount != 0 {
		t.Errorf("counters should be zero, got read=%d err=%d", st.ReadCount, st.ErrorCount)
	}
	if !st.LastRead.IsZero() {
		t.Errorf("LastRead should be zero before any read, got %v", st.LastRead)
	}

	// After Init: tag count reflects parsed tags; state stays disconnected.
	if err := drv.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	st = drv.Status()
	if st.TagCount != 1 {
		t.Errorf("Status().TagCount = %d, want 1", st.TagCount)
	}
	if st.State != core.StateDisconnected {
		t.Errorf("Status().State = %s, want disconnected (Init must not connect)", st.State)
	}

	// A read while not connected must fail and bump the error counter, but it
	// does not set LastError (that is only set on real I/O errors).
	_, rerr := drv.Read(context.Background(), []string{"tag1"})
	if rerr == nil {
		t.Fatal("expected Read to fail when not connected")
	}
	st = drv.Status()
	if st.ErrorCount != 1 {
		t.Errorf("Status().ErrorCount = %d, want 1", st.ErrorCount)
	}
	if st.ReadCount != 0 {
		t.Errorf("Status().ReadCount = %d, want 0 (no successful reads)", st.ReadCount)
	}
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func TestCapabilities(t *testing.T) {
	t.Run("feature flags always enabled", func(t *testing.T) {
		drv, err := NewOPCUADriver(core.DriverConfig{Name: "x", Settings: map[string]any{}})
		if err != nil {
			t.Fatalf("NewOPCUADriver failed: %v", err)
		}
		caps := drv.Capabilities()
		if !caps.CanRead {
			t.Error("CanRead should be true")
		}
		if !caps.CanWrite {
			t.Error("CanWrite should be true")
		}
		if !caps.CanSubscribe {
			t.Error("CanSubscribe should be true")
		}
		if !caps.BatchRead {
			t.Error("BatchRead should be true")
		}
	})

	t.Run("default max batch size after init", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		if caps := d.Capabilities(); caps.MaxBatchSize != 1000 {
			t.Errorf("MaxBatchSize = %d, want 1000", caps.MaxBatchSize)
		}
	})

	t.Run("custom max batch size after init", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["max-batch-size"] = uint64(250)
		d := mustInit(t, cfg)
		if caps := d.Capabilities(); caps.MaxBatchSize != 250 {
			t.Errorf("MaxBatchSize = %d, want 250", caps.MaxBatchSize)
		}
	})
}

// ---------------------------------------------------------------------------
// Stop
// ---------------------------------------------------------------------------

func TestStop(t *testing.T) {
	t.Run("fresh driver never started", func(t *testing.T) {
		drv, err := NewOPCUADriver(core.DriverConfig{Name: "x", Settings: map[string]any{}})
		if err != nil {
			t.Fatalf("NewOPCUADriver failed: %v", err)
		}
		d := drv.(*OPCUADriver)
		if err := drv.Stop(); err != nil {
			t.Errorf("Stop() returned error: %v", err)
		}
		if d.state != core.StateDisconnected {
			t.Errorf("state = %s, want disconnected", d.state)
		}
	})

	t.Run("after init but not started", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		if err := d.Stop(); err != nil {
			t.Errorf("Stop() returned error: %v", err)
		}
		if d.state != core.StateDisconnected {
			t.Errorf("state = %s, want disconnected", d.state)
		}
		if d.client != nil {
			t.Error("client should be nil after Stop")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		drv, err := NewOPCUADriver(core.DriverConfig{Name: "x", Settings: map[string]any{}})
		if err != nil {
			t.Fatalf("NewOPCUADriver failed: %v", err)
		}
		if err := drv.Stop(); err != nil {
			t.Fatalf("first Stop() failed: %v", err)
		}
		if err := drv.Stop(); err != nil {
			t.Fatalf("second Stop() failed: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Restart
// ---------------------------------------------------------------------------

func TestRestart(t *testing.T) {
	t.Run("re-init updates endpoint and tags", func(t *testing.T) {
		cfgA := validConfig("x")
		cfgA.Settings["timeout"] = "1s"
		d := mustInit(t, cfgA)
		if d.endpoint != "opc.tcp://127.0.0.1:4840" {
			t.Fatalf("initial endpoint = %q", d.endpoint)
		}

		// A second config pointing at a different endpoint and a different
		// address for the same tag name. Reusing the tag name lets us verify
		// that Init re-parsed and overwrote the nodeID cleanly.
		cfgB := core.DriverConfig{
			Name:     "x",
			Type:     "opcua",
			Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4841", "timeout": "1s"},
			Tags:     []core.TagConfig{{Name: "tag1", Address: "ns=3;s=Reactor.Temp", Type: "float64"}},
		}

		// Use a pre-cancelled context so Start's connect fails fast and the
		// background reconnect goroutine exits immediately without ever
		// dialling a real server. This keeps the test server-free and
		// deterministic.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := d.Restart(ctx, cfgB); err != nil {
			t.Fatalf("Restart failed: %v", err)
		}

		// Init must have re-parsed cfgB.
		if d.endpoint != "opc.tcp://127.0.0.1:4841" {
			t.Errorf("endpoint after restart = %q, want 4841", d.endpoint)
		}
		if len(d.nodeIDs) != 1 {
			t.Fatalf("expected 1 nodeID after restart, got %d", len(d.nodeIDs))
		}
		nodeID, ok := d.nodeIDs["tag1"]
		if !ok {
			t.Fatal("missing nodeID for tag 'tag1' after restart")
		}
		if nodeID.Namespace() != 3 || nodeID.StringID() != "Reactor.Temp" {
			t.Errorf("tag1 nodeID = ns=%d;s=%s, want ns=3;s=Reactor.Temp", nodeID.Namespace(), nodeID.StringID())
		}
		// Start failed to connect (no server) so the driver is left connecting.
		if d.state != core.StateConnecting {
			t.Errorf("state after restart = %s, want connecting", d.state)
		}

		// Tear down the reconnect goroutine and verify a clean stop.
		if err := d.Stop(); err != nil {
			t.Fatalf("Stop after restart failed: %v", err)
		}
		if d.state != core.StateDisconnected {
			t.Errorf("state after stop = %s, want disconnected", d.state)
		}
	})

	t.Run("returns error when re-init config lacks endpoint", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		bad := core.DriverConfig{Name: "x", Settings: map[string]any{}} // no endpoint
		err := d.Restart(context.Background(), bad)
		if err == nil {
			t.Fatal("expected error from Restart with missing endpoint, got nil")
		}
		if !strings.Contains(err.Error(), "endpoint is required") {
			t.Errorf("error = %q, want substring 'endpoint is required'", err.Error())
		}
	})

	t.Run("returns error when re-init config has bad node id", func(t *testing.T) {
		d := mustInit(t, validConfig("x"))
		bad := core.DriverConfig{
			Name:     "x",
			Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
			Tags:     []core.TagConfig{{Name: "bad", Address: "ns=abc;i=1", Type: "int32"}},
		}
		err := d.Restart(context.Background(), bad)
		if err == nil {
			t.Fatal("expected error from Restart with invalid NodeID, got nil")
		}
		if !strings.Contains(err.Error(), "invalid NodeID") {
			t.Errorf("error = %q, want substring 'invalid NodeID'", err.Error())
		}
	})
}

// ---------------------------------------------------------------------------
// parseNodeID edge cases (exercises ua.ParseNodeID, which Init relies on)
// ---------------------------------------------------------------------------

func TestParseNodeIDEdgeCases(t *testing.T) {
	valid := []struct {
		name      string
		input     string
		namespace uint16
		wantStr   string // expected StringID() when non-numeric
		wantInt   uint32 // expected IntID() when numeric
		isNumeric bool
		hasStrID  bool // expect a non-empty StringID()
	}{
		{"string in ns2", "ns=2;s=Conveyor.Speed", 2, "Conveyor.Speed", 0, false, true},
		{"numeric in ns1", "ns=1;i=1001", 1, "", 1001, true, false},
		{"numeric ns0 two-byte", "i=85", 0, "", 85, true, false},
		{"numeric ns0 four-byte", "ns=0;i=2253", 0, "", 2253, true, false},
		{"numeric large namespace", "ns=256;i=42", 256, "", 42, true, false},
		{"numeric max uint32", "ns=2;i=4294967295", 2, "", 4294967295, true, false},
		{"string default namespace", "s=Hello", 0, "Hello", 0, false, true},
		{"string implicit (no s= prefix)", "ns=3;MyTag", 3, "MyTag", 0, false, true},
		{"string containing semicolons", "ns=1;s=foo;bar;", 1, "foo;bar;", 0, false, true},
		{"string with special characters", "ns=5;s=a.b/c@d", 5, "a.b/c@d", 0, false, true},
		// GUID identifiers are normalised to upper-case hex by the library.
		{"guid", "ns=1;g=5eac051c-c313-43d7-b790-24aa2c3cfd37", 1, "5EAC051C-C313-43D7-B790-24AA2C3CFD37", 0, false, true},
		{"bytestring", "ns=1;b=YWJj", 1, "YWJj", 0, false, true},
		{"empty string is null node id", "", 0, "", 0, true, false},
	}
	for _, tt := range valid {
		t.Run("valid/"+tt.name, func(t *testing.T) {
			nodeID, err := ua.ParseNodeID(tt.input)
			if err != nil {
				t.Fatalf("ParseNodeID(%q) error: %v", tt.input, err)
			}
			if nodeID == nil {
				t.Fatalf("ParseNodeID(%q) returned nil node id", tt.input)
			}
			if nodeID.Namespace() != tt.namespace {
				t.Errorf("namespace = %d, want %d", nodeID.Namespace(), tt.namespace)
			}
			if tt.isNumeric && nodeID.IntID() != tt.wantInt {
				t.Errorf("IntID = %d, want %d", nodeID.IntID(), tt.wantInt)
			}
			if tt.hasStrID {
				if nodeID.StringID() != tt.wantStr {
					t.Errorf("StringID = %q, want %q", nodeID.StringID(), tt.wantStr)
				}
			}
		})
	}

	invalid := []struct {
		name  string
		input string
	}{
		{"bad namespace prefix", "abc=0;i=2"},
		{"namespace id out of range", "ns=65536;i=1"},
		{"non-numeric namespace", "ns=abc;i=1"},
		{"non-numeric identifier", "ns=1;i=abc"},
		{"numeric id out of range", "ns=1;i=4294967296"},
		{"invalid guid", "ns=1;g=x"},
		{"invalid bytestring", "ns=1;b=aW52YWxp%ZA=="},
		{"namespace without identifier", "ns=0"},
		{"namespace uri not supported", "nsu=abc;i=1"},
		{"mixed identifier prefixes", "ns=0;i=1;s=2"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			nodeID, err := ua.ParseNodeID(tt.input)
			if err == nil {
				t.Fatalf("ParseNodeID(%q) expected error, got node id %v", tt.input, nodeID)
			}
			if nodeID != nil {
				t.Errorf("ParseNodeID(%q) expected nil node id on error, got %v", tt.input, nodeID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Server-free data-operation behaviour (bonus: real behaviour, no server)
// ---------------------------------------------------------------------------

func TestReadNotConnected(t *testing.T) {
	d := mustInit(t, validConfig("x"))

	// Reading a known tag while disconnected fails with a not-connected error
	// and increments the error counter.
	vals, err := d.Read(context.Background(), []string{"tag1"})
	if err == nil {
		t.Fatal("expected Read to fail when not connected")
	}
	if vals != nil {
		t.Errorf("expected nil values, got %v", vals)
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want substring 'not connected'", err.Error())
	}
	if d.errorCount.Load() != 1 {
		t.Errorf("errorCount = %d, want 1", d.errorCount.Load())
	}

	// An unknown tag still hits the not-connected guard first.
	_, err = d.Read(context.Background(), []string{"does-not-exist"})
	if err == nil {
		t.Fatal("expected Read to fail when not connected")
	}
	if d.errorCount.Load() != 2 {
		t.Errorf("errorCount = %d, want 2", d.errorCount.Load())
	}
}

func TestWriteNotConnected(t *testing.T) {
	d := mustInit(t, validConfig("x"))

	res, err := d.Write(context.Background(), []core.WriteCommand{{Tag: "tag1", Value: 1.0}})
	if err == nil {
		t.Fatal("expected Write to fail when not connected")
	}
	if res != nil {
		t.Errorf("expected nil results, got %v", res)
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("error = %q, want substring 'not connected'", err.Error())
	}
}

func TestSubscribeMode(t *testing.T) {
	t.Run("polling mode rejects subscribe", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["mode"] = "polling"
		d := mustInit(t, cfg)
		if d.subMode {
			t.Fatal("subMode should be false for polling mode")
		}
		ch, err := d.Subscribe(context.Background(), []string{"tag1"})
		if err != core.ErrSubscribeNotSupported {
			t.Errorf("error = %v, want ErrSubscribeNotSupported", err)
		}
		if ch != nil {
			t.Errorf("expected nil channel, got %v", ch)
		}
	})

	t.Run("subscription mode returns the data channel", func(t *testing.T) {
		cfg := validConfig("x")
		cfg.Settings["mode"] = "subscription"
		d := mustInit(t, cfg)
		if !d.subMode {
			t.Fatal("subMode should be true for subscription mode")
		}
		ch, err := d.Subscribe(context.Background(), []string{"tag1"})
		if err != nil {
			t.Fatalf("Subscribe error = %v, want nil", err)
		}
		if ch == nil {
			t.Fatal("expected non-nil subscription channel")
		}
		// The returned channel is the driver's buffered subChannel; it must be
		// empty because no real server is pushing notifications.
		select {
		case dp := <-ch:
			t.Errorf("expected empty channel, received data point %+v", dp)
		default:
		}
	})
}
