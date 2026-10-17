package route

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// The /drivers/test endpoint builds a throwaway driver instance from the
// request, drives it through init+start, and always stops it. The fake
// drivers below use deliberately unique type names because the plugin
// registry is process-global and shared with every other test in the
// package; unique names avoid collisions with the engine package's mocks
// and with future tests.

// fakeTestDriver is a minimal core.Driver for connectivity-test exercises.
// failStart makes Start (the "connect" step) fail; stopCalled records
// that the handler stopped the temporary instance.
type fakeTestDriver struct {
	name       string
	failStart  bool
	stopCalled atomic.Bool
}

var errFakeDeviceUnreachable = errors.New("fake device unreachable")

func (d *fakeTestDriver) Init(ctx context.Context, cfg core.DriverConfig) error {
	d.name = cfg.Name
	return nil
}

func (d *fakeTestDriver) Start(ctx context.Context) error {
	if d.failStart {
		return errFakeDeviceUnreachable
	}
	return nil
}

func (d *fakeTestDriver) Stop() error {
	d.stopCalled.Store(true)
	return nil
}

func (d *fakeTestDriver) Restart(ctx context.Context, cfg core.DriverConfig) error { return nil }
func (d *fakeTestDriver) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	return nil, nil
}
func (d *fakeTestDriver) Write(ctx context.Context, cmds []core.WriteCommand) ([]core.WriteResult, error) {
	return nil, nil
}
func (d *fakeTestDriver) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	return nil, core.ErrSubscribeNotSupported
}
func (d *fakeTestDriver) Name() string { return d.name }
func (d *fakeTestDriver) Type() string { return "route-test-fake" }
func (d *fakeTestDriver) Status() core.DriverStatus {
	return core.DriverStatus{Name: d.name, State: core.StateConnected}
}
func (d *fakeTestDriver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{CanRead: true, CanWrite: true}
}

// Compile-time guard for the fake.
var _ core.Driver = (*fakeTestDriver)(nil)

// The handler creates instances inside its own call, so the factories
// record the last instance each test created for stop-verification.
var (
	lastOKInstance   atomic.Pointer[fakeTestDriver]
	lastFailInstance atomic.Pointer[fakeTestDriver]
)

// registerFakeTestDrivers registers the fake driver types lazily — from
// inside each test, NOT from an init() — mirroring the engine package's
// registerMockTypes pattern. This matters: the plugin registry is
// process-global, and config validation (config/validate.go, M10) skips
// driver-type checks only while the registry is EMPTY.
// configs_raw_test.go's TestValidateConfigs parses a config with the real
// type "modbus-tcp" and relies on that empty-registry escape (this test
// binary deliberately does not import driver/all). Registering from
// init() would pollute the registry before that test runs and make it fail
// with `unknown type "modbus-tcp"`. Lazy in-test registration confines the
// pollution to these tests, which run after configs_raw_test.go (tests in
// this package are sequential — none use t.Parallel).
func registerFakeTestDrivers() {
	core.RegisterDriver("route-test-fake-ok", func(cfg core.DriverConfig) (core.Driver, error) {
		d := &fakeTestDriver{}
		lastOKInstance.Store(d)
		return d, nil
	})
	core.RegisterDriver("route-test-fake-fail", func(cfg core.DriverConfig) (core.Driver, error) {
		d := &fakeTestDriver{failStart: true}
		lastFailInstance.Store(d)
		return d, nil
	})
}

// waitForCond polls check until it returns true or the timeout expires.
// Poll+timeout (never a bare sleep) per the test contract in
// CONTRIBUTING.md §4.
func waitForCond(t *testing.T, timeout time.Duration, check func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

func postDriverTest(t *testing.T, ts *httptest.Server, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+"/drivers/test", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /drivers/test failed: %v", err)
	}
	return resp
}

func TestDriverTestConnects(t *testing.T) {
	registerFakeTestDrivers()

	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postDriverTest(t, ts, `{"type": "route-test-fake-ok", "settings": {"host": "127.0.0.1", "port": 502}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		OK         bool   `json:"ok"`
		Connected  bool   `json:"connected"`
		LatencyMs  int64  `json:"latency_ms"`
		DriverType string `json:"driver_type"`
	}
	decodeJSON(t, resp, &result)
	if !result.OK || !result.Connected {
		t.Errorf("expected ok=true connected=true, got ok=%v connected=%v", result.OK, result.Connected)
	}
	if result.LatencyMs < 0 {
		t.Errorf("latency_ms must be >= 0, got %d", result.LatencyMs)
	}
	if result.DriverType != "route-test-fake-ok" {
		t.Errorf("driver_type: got %q, want route-test-fake-ok", result.DriverType)
	}
	// The temporary instance must always be stopped — even on success.
	waitForCond(t, 2*time.Second, func() bool { return lastOKInstance.Load().stopCalled.Load() },
		"temporary driver instance was not stopped after a successful test")
}

func TestDriverTestStartFailure(t *testing.T) {
	registerFakeTestDrivers()

	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postDriverTest(t, ts, `{"type": "route-test-fake-fail", "settings": {}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (a completed negative test is a successful run), got %d", resp.StatusCode)
	}
	var result struct {
		OK         bool   `json:"ok"`
		Connected  bool   `json:"connected"`
		Error      string `json:"error"`
		LatencyMs  int64  `json:"latency_ms"`
		DriverType string `json:"driver_type"`
	}
	decodeJSON(t, resp, &result)
	if result.OK || result.Connected {
		t.Errorf("expected ok=false connected=false, got ok=%v connected=%v", result.OK, result.Connected)
	}
	if result.Error != errFakeDeviceUnreachable.Error() {
		t.Errorf("error: got %q, want %q", result.Error, errFakeDeviceUnreachable.Error())
	}
	if result.DriverType != "route-test-fake-fail" {
		t.Errorf("driver_type: got %q, want route-test-fake-fail", result.DriverType)
	}
	// A failed connect must not leak the instance either.
	waitForCond(t, 2*time.Second, func() bool { return lastFailInstance.Load().stopCalled.Load() },
		"temporary driver instance was not stopped after a failed test")
}

func TestDriverTestUnknownType(t *testing.T) {
	registerFakeTestDrivers()

	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postDriverTest(t, ts, `{"type": "no-such-driver-type"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] != "unknown driver type: no-such-driver-type" {
		t.Errorf("error: got %q, want %q", errResp["error"], "unknown driver type: no-such-driver-type")
	}
}

func TestDriverTestEmptyType(t *testing.T) {
	registerFakeTestDrivers()

	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postDriverTest(t, ts, `{"settings": {}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestDriverTestBadJSON(t *testing.T) {
	registerFakeTestDrivers()

	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postDriverTest(t, ts, `not json`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestDriverTestDefaultName(t *testing.T) {
	registerFakeTestDrivers()

	// Name is optional and defaults to "driver-test"; the instance must
	// still be created and stopped when the field is omitted.
	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"type": "route-test-fake-ok", "settings": map[string]any{}})
	resp := postDriverTest(t, ts, string(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	waitForCond(t, 2*time.Second, func() bool { return lastOKInstance.Load().stopCalled.Load() },
		"temporary driver instance was not stopped")
}
