package s7

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// newTestDriver builds a fresh S7Driver via the public constructor and
// type-asserts it to *S7Driver so the tests (which live in the s7 package)
// can inspect the parsed configuration fields directly.
func newTestDriver(t *testing.T, name string) *S7Driver {
	t.Helper()
	driver, err := NewS7Driver(core.DriverConfig{Name: name, Type: "s7"})
	if err != nil {
		t.Fatalf("NewS7Driver(%q) returned error: %v", name, err)
	}
	d, ok := driver.(*S7Driver)
	if !ok {
		t.Fatalf("NewS7Driver returned %T, want *S7Driver", driver)
	}
	return d
}

// TestNewS7Driver verifies the constructor produces a driver with the
// configured name, a disconnected initial state, and initialized (empty)
// tag/address maps.
func TestNewS7Driver(t *testing.T) {
	config := core.DriverConfig{
		Name:     "plc-1",
		Type:     "s7",
		Settings: map[string]any{"host": "10.0.0.1"},
	}

	driver, err := NewS7Driver(config)
	if err != nil {
		t.Fatalf("NewS7Driver returned error: %v", err)
	}

	if got := driver.Name(); got != "plc-1" {
		t.Errorf("Name() = %q, want %q", got, "plc-1")
	}

	d, ok := driver.(*S7Driver)
	if !ok {
		t.Fatalf("NewS7Driver returned %T, want *S7Driver", driver)
	}

	// A brand-new driver must start disconnected with empty, non-nil maps.
	if d.state != core.StateDisconnected {
		t.Errorf("initial state = %v, want StateDisconnected", d.state)
	}
	if d.tags == nil || d.addrs == nil {
		t.Errorf("tag/address maps must be initialized, got tags=%v addrs=%v", d.tags, d.addrs)
	}
	if len(d.tags) != 0 || len(d.addrs) != 0 {
		t.Errorf("tag maps must be empty, got tags=%d addrs=%d", len(d.tags), len(d.addrs))
	}

	// Status() must agree with the raw state.
	st := driver.Status()
	if st.Name != "plc-1" {
		t.Errorf("Status().Name = %q, want %q", st.Name, "plc-1")
	}
	if st.State != core.StateDisconnected {
		t.Errorf("Status().State = %v, want StateDisconnected", st.State)
	}
	if st.TagCount != 0 {
		t.Errorf("Status().TagCount = %d, want 0", st.TagCount)
	}
}

// TestInit_ConfigParsing verifies Init parses every supported setting and
// applies the documented defaults when settings are absent or malformed.
func TestInit_ConfigParsing(t *testing.T) {
	tests := []struct {
		name             string
		settings         map[string]any
		tags             []core.TagConfig
		wantHost         string
		wantPort         int
		wantRack         int
		wantSlot         int
		wantTimeout      time.Duration
		wantIdleTimeout  time.Duration
		wantReconnect    time.Duration
		wantMaxReconnect time.Duration
		wantTagCount     int
	}{
		{
			name:             "defaults when only host is set",
			settings:         map[string]any{"host": "10.0.0.1"},
			wantHost:         "10.0.0.1",
			wantPort:         102,
			wantRack:         0,
			wantSlot:         2,
			wantTimeout:      5 * time.Second,
			wantIdleTimeout:  60 * time.Second,
			wantReconnect:    core.DefaultReconnectBackoff,
			wantMaxReconnect: core.DefaultMaxReconnectBackoff,
			wantTagCount:     0,
		},
		{
			name: "all settings customized",
			settings: map[string]any{
				"host":                   "192.168.1.10",
				"port":                   200,
				"rack":                   1,
				"slot":                   1,
				"timeout":                "10s",
				"idle-timeout":           "120s",
				"reconnect-interval":     "5s",
				"reconnect-max-interval": "60s",
			},
			wantHost:         "192.168.1.10",
			wantPort:         200,
			wantRack:         1,
			wantSlot:         1,
			wantTimeout:      10 * time.Second,
			wantIdleTimeout:  120 * time.Second,
			wantReconnect:    5 * time.Second,
			wantMaxReconnect: 60 * time.Second,
			wantTagCount:     0,
		},
		{
			// YAML unmarshalling emits float64 for unquoted numbers, so the
			// port helper must accept float64 in addition to int.
			name:             "port as float64 (YAML style)",
			settings:         map[string]any{"host": "10.0.0.1", "port": float64(200)},
			wantHost:         "10.0.0.1",
			wantPort:         200,
			wantRack:         0,
			wantSlot:         2,
			wantTimeout:      5 * time.Second,
			wantIdleTimeout:  60 * time.Second,
			wantReconnect:    core.DefaultReconnectBackoff,
			wantMaxReconnect: core.DefaultMaxReconnectBackoff,
			wantTagCount:     0,
		},
		{
			// An unparseable timeout string is ignored, and because the
			// resulting duration is zero it falls back to the 5s default.
			name:             "invalid timeout string falls back to default",
			settings:         map[string]any{"host": "10.0.0.1", "timeout": "not-a-duration"},
			wantHost:         "10.0.0.1",
			wantPort:         102,
			wantRack:         0,
			wantSlot:         2,
			wantTimeout:      5 * time.Second,
			wantIdleTimeout:  60 * time.Second,
			wantReconnect:    core.DefaultReconnectBackoff,
			wantMaxReconnect: core.DefaultMaxReconnectBackoff,
			wantTagCount:     0,
		},
		{
			// A non-numeric port is not an error; it falls back to 102.
			name:             "invalid port type falls back to default",
			settings:         map[string]any{"host": "10.0.0.1", "port": "abc"},
			wantHost:         "10.0.0.1",
			wantPort:         102,
			wantRack:         0,
			wantSlot:         2,
			wantTimeout:      5 * time.Second,
			wantIdleTimeout:  60 * time.Second,
			wantReconnect:    core.DefaultReconnectBackoff,
			wantMaxReconnect: core.DefaultMaxReconnectBackoff,
			wantTagCount:     0,
		},
		{
			name:     "tags are parsed into the address map",
			settings: map[string]any{"host": "10.0.0.1"},
			tags: []core.TagConfig{
				{Name: "temp", Address: "DB1.DBD0", Type: "float32"},
				{Name: "flag", Address: "M0.0", Type: "bool"},
			},
			wantHost:         "10.0.0.1",
			wantPort:         102,
			wantRack:         0,
			wantSlot:         2,
			wantTimeout:      5 * time.Second,
			wantIdleTimeout:  60 * time.Second,
			wantReconnect:    core.DefaultReconnectBackoff,
			wantMaxReconnect: core.DefaultMaxReconnectBackoff,
			wantTagCount:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDriver(t, "test")
			cfg := core.DriverConfig{
				Name:     "test",
				Type:     "s7",
				Settings: tt.settings,
				Tags:     tt.tags,
			}

			if err := d.Init(context.Background(), cfg); err != nil {
				t.Fatalf("Init returned error: %v", err)
			}

			if d.host != tt.wantHost {
				t.Errorf("host = %q, want %q", d.host, tt.wantHost)
			}
			if d.port != tt.wantPort {
				t.Errorf("port = %d, want %d", d.port, tt.wantPort)
			}
			if d.rack != tt.wantRack {
				t.Errorf("rack = %d, want %d", d.rack, tt.wantRack)
			}
			if d.slot != tt.wantSlot {
				t.Errorf("slot = %d, want %d", d.slot, tt.wantSlot)
			}
			if d.timeout != tt.wantTimeout {
				t.Errorf("timeout = %v, want %v", d.timeout, tt.wantTimeout)
			}
			if d.idleTimeout != tt.wantIdleTimeout {
				t.Errorf("idleTimeout = %v, want %v", d.idleTimeout, tt.wantIdleTimeout)
			}
			if d.reconnectBackoff != tt.wantReconnect {
				t.Errorf("reconnectBackoff = %v, want %v", d.reconnectBackoff, tt.wantReconnect)
			}
			if d.maxReconnectBackoff != tt.wantMaxReconnect {
				t.Errorf("maxReconnectBackoff = %v, want %v", d.maxReconnectBackoff, tt.wantMaxReconnect)
			}

			// Every configured tag must land in both the config map and the
			// parsed-address map; Status().TagCount must agree.
			if len(d.tags) != tt.wantTagCount {
				t.Errorf("len(tags) = %d, want %d", len(d.tags), tt.wantTagCount)
			}
			if len(d.addrs) != tt.wantTagCount {
				t.Errorf("len(addrs) = %d, want %d", len(d.addrs), tt.wantTagCount)
			}
			if got := d.Status().TagCount; got != tt.wantTagCount {
				t.Errorf("Status().TagCount = %d, want %d", got, tt.wantTagCount)
			}
			for _, tag := range tt.tags {
				if _, ok := d.tags[tag.Name]; !ok {
					t.Errorf("tag %q missing from tags map", tag.Name)
				}
				if _, ok := d.addrs[tag.Name]; !ok {
					t.Errorf("tag %q missing from addrs map", tag.Name)
				}
			}
		})
	}
}

// TestInit_ErrorPaths verifies Init rejects malformed configurations with
// descriptive errors and never starts a connection.
func TestInit_ErrorPaths(t *testing.T) {
	tests := []struct {
		name    string
		config  core.DriverConfig
		wantErr string // substring expected in the error message
	}{
		{
			name:    "missing host key",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{}},
			wantErr: "host is required",
		},
		{
			name:    "host is not a string",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{"host": 123}},
			wantErr: "host is required",
		},
		{
			name:    "host is nil",
			config:  core.DriverConfig{Name: "t", Settings: map[string]any{"host": nil}},
			wantErr: "host is required",
		},
		{
			name: "invalid tag address",
			config: core.DriverConfig{
				Name:     "t",
				Settings: map[string]any{"host": "10.0.0.1"},
				Tags:     []core.TagConfig{{Name: "bad", Address: "ZZ123", Type: "int16"}},
			},
			wantErr: "invalid s7 address",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newTestDriver(t, "t")
			err := d.Init(context.Background(), tt.config)
			if err == nil {
				t.Fatalf("Init did not return an error for %q", tt.name)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
			// A failed Init must not leave the driver in a connecting state.
			if d.state == core.StateConnecting || d.state == core.StateConnected {
				t.Errorf("state = %v after failed Init; must not be connecting/connected", d.state)
			}
		})
	}
}

// TestNameTypeStatus verifies the metadata accessors report the configured
// name, the fixed "s7" type, and an accurate status snapshot after Init.
func TestNameTypeStatus(t *testing.T) {
	d := newTestDriver(t, "plc-7")

	cfg := core.DriverConfig{
		Name:     "plc-7",
		Type:     "s7",
		Settings: map[string]any{"host": "10.0.0.1"},
		Tags: []core.TagConfig{
			{Name: "a", Address: "MW10", Type: "uint16"},
			{Name: "b", Address: "DB1.DBD0", Type: "float32"},
		},
	}
	if err := d.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	if got := d.Name(); got != "plc-7" {
		t.Errorf("Name() = %q, want %q", got, "plc-7")
	}
	if got := d.Type(); got != "s7" {
		t.Errorf("Type() = %q, want %q", got, "s7")
	}

	st := d.Status()
	if st.Name != "plc-7" {
		t.Errorf("Status().Name = %q, want %q", st.Name, "plc-7")
	}
	if st.Type != "s7" {
		t.Errorf("Status().Type = %q, want %q", st.Type, "s7")
	}
	// Init parses config but does not connect.
	if st.State != core.StateDisconnected {
		t.Errorf("Status().State = %v, want StateDisconnected", st.State)
	}
	if st.TagCount != 2 {
		t.Errorf("Status().TagCount = %d, want 2", st.TagCount)
	}
	if st.ReadCount != 0 {
		t.Errorf("Status().ReadCount = %d, want 0", st.ReadCount)
	}
	if st.ErrorCount != 0 {
		t.Errorf("Status().ErrorCount = %d, want 0", st.ErrorCount)
	}
	if st.LastError != "" {
		t.Errorf("Status().LastError = %q, want empty", st.LastError)
	}
	if !st.LastRead.IsZero() {
		t.Errorf("Status().LastRead = %v, want zero time before any read", st.LastRead)
	}
}

// TestCapabilities verifies the driver advertises the S7 protocol's
// capabilities, including the PDU-derived max batch size.
func TestCapabilities(t *testing.T) {
	d := newTestDriver(t, "c")
	caps := d.Capabilities()

	if !caps.CanRead {
		t.Error("CanRead = false, want true")
	}
	if !caps.CanWrite {
		t.Error("CanWrite = false, want true")
	}
	if caps.CanSubscribe {
		t.Error("CanSubscribe = true, want false")
	}
	if !caps.BatchRead {
		t.Error("BatchRead = false, want true")
	}
	if caps.MaxBatchSize != s7MaxBatchSize {
		t.Errorf("MaxBatchSize = %d, want const s7MaxBatchSize (%d)", caps.MaxBatchSize, s7MaxBatchSize)
	}
	if caps.MaxBatchSize != 220 {
		t.Errorf("MaxBatchSize = %d, want 220 (standard S7 PDU limit)", caps.MaxBatchSize)
	}
}

// TestStop_NotConnected verifies Stop is a clean no-op on a driver that was
// never started, and remains clean after Init (which does not connect).
func TestStop_NotConnected(t *testing.T) {
	d := newTestDriver(t, "s")

	// Stop on a fresh driver: cancel is nil and no goroutines are running,
	// so this must succeed without panicking.
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop on fresh driver returned error: %v", err)
	}
	if d.state != core.StateDisconnected {
		t.Errorf("state after Stop = %v, want StateDisconnected", d.state)
	}
	if d.handler != nil {
		t.Error("handler should be nil after stopping a never-started driver")
	}

	// Stop after Init (still not connected) must also be clean.
	cfg := core.DriverConfig{Name: "s", Type: "s7", Settings: map[string]any{"host": "10.0.0.1"}}
	if err := d.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop after Init returned error: %v", err)
	}
	if d.state != core.StateDisconnected {
		t.Errorf("state after Stop = %v, want StateDisconnected", d.state)
	}
}

// TestRestart_Reinit verifies Restart stops the driver, re-initializes it
// with the supplied config, and starts it. With no PLC reachable the start
// fails to connect and the driver transitions to StateConnecting rather than
// reporting an error. No real PLC is required.
func TestRestart_Reinit(t *testing.T) {
	d := newTestDriver(t, "r")
	// Always tear down the background reconnect loop so the test never leaks
	// a goroutine even if an assertion fails.
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("cleanup Stop returned error: %v", err)
		}
	})

	// 127.0.0.1:1 is a closed loopback port: connect fails instantly with
	// "connection refused". A short timeout keeps the dial bounded, and a
	// large reconnect interval prevents the background loop from making any
	// network attempt before Stop() cancels its context.
	newCfg := core.DriverConfig{
		Name:     "r",
		Type:     "s7",
		Settings: map[string]any{"host": "127.0.0.1", "port": 1, "timeout": "200ms", "reconnect-interval": "60s"},
		Tags:     []core.TagConfig{{Name: "x", Address: "MW0", Type: "uint16"}},
	}

	if err := d.Restart(context.Background(), newCfg); err != nil {
		t.Fatalf("Restart returned error: %v", err)
	}

	// Init must have applied the new configuration.
	if d.host != "127.0.0.1" {
		t.Errorf("host after Restart = %q, want %q", d.host, "127.0.0.1")
	}
	if d.port != 1 {
		t.Errorf("port after Restart = %d, want 1", d.port)
	}
	if len(d.tags) != 1 {
		t.Errorf("tag count after Restart = %d, want 1", len(d.tags))
	}
	if _, ok := d.addrs["x"]; !ok {
		t.Errorf("tag %q missing from addrs map after Restart", "x")
	}

	// Start ran and the connect failed gracefully, so the driver must be
	// retrying in the background rather than connected or erroring out.
	st := d.Status()
	if st.State != core.StateConnecting {
		t.Errorf("state after Restart = %v, want StateConnecting", st.State)
	}
	if st.LastError == "" {
		t.Error("LastError should record the connect failure after Restart")
	}
}

// TestRestart_InitError verifies Restart surfaces an Init failure (and never
// starts the driver) when the new configuration is invalid.
func TestRestart_InitError(t *testing.T) {
	d := newTestDriver(t, "r")

	// Missing host -> Init fails -> Restart must return that error and must
	// not have started the driver (no reconnect loop, still disconnected).
	badCfg := core.DriverConfig{
		Name:     "r",
		Type:     "s7",
		Settings: map[string]any{}, // no host
	}
	err := d.Restart(context.Background(), badCfg)
	if err == nil {
		t.Fatalf("Restart should fail when Init fails, got nil")
	}
	if !strings.Contains(err.Error(), "host is required") {
		t.Errorf("error = %q, want substring %q", err.Error(), "host is required")
	}
	if d.state != core.StateDisconnected {
		t.Errorf("state after failed Restart = %v, want StateDisconnected", d.state)
	}
}
