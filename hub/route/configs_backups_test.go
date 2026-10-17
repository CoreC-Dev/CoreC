package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newBackupsTestServer spins up a test server with the given secret for the
// backup endpoints.
func newBackupsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(router(context.Background(), "secret-123", nil, 0, true))
	t.Cleanup(ts.Close)
	return ts
}

// authedRequest builds a request against ts with the Bearer token set.
func authedRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("failed to build request %s %s: %v", method, url, err)
	}
	req.Header.Set("Authorization", "Bearer secret-123")
	return req
}

// TestGetConfigBackups verifies GET /configs/backups: metadata-only list,
// newest first, auth required.
func TestGetConfigBackups(t *testing.T) {
	defer func() { ListConfigBackupsFunc = nil }()
	ListConfigBackupsFunc = func() ([]ConfigBackupInfo, error) {
		return []ConfigBackupInfo{
			{Name: "2000000000000000002.yaml", Timestamp: time.Unix(0, 2000000000000000002).UTC(), Size: 120},
			{Name: "1000000000000000001.yaml", Timestamp: time.Unix(0, 1000000000000000001).UTC(), Size: 90},
		}, nil
	}
	ts := newBackupsTestServer(t)

	// 1. Without auth → 401.
	resp, err := http.Get(ts.URL + "/configs/backups")
	if err != nil {
		t.Fatalf("GET /configs/backups: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth GET /configs/backups: got %d, want 401", resp.StatusCode)
	}

	// 2. With auth → 200 with newest-first metadata list.
	resp2, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups", ""))
	if err != nil {
		t.Fatalf("authed GET /configs/backups: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /configs/backups: got %d, want 200", resp2.StatusCode)
	}
	var payload struct {
		Backups []ConfigBackupInfo `json:"backups"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&payload); err != nil {
		t.Fatalf("failed to decode backup list: %v", err)
	}
	if len(payload.Backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(payload.Backups))
	}
	if payload.Backups[0].Name != "2000000000000000002.yaml" {
		t.Errorf("newest-first order broken: first entry = %q", payload.Backups[0].Name)
	}
	if payload.Backups[1].Size != 90 {
		t.Errorf("backups[1].size = %d, want 90", payload.Backups[1].Size)
	}
	if payload.Backups[0].Timestamp.IsZero() {
		t.Error("backups[0].timestamp should be set")
	}
}

// TestGetConfigBackups_NoConfigPath verifies the empty-config startup story:
// the endpoint answers 503 with a clear message, not an empty list and not a
// panic.
func TestGetConfigBackups_NoConfigPath(t *testing.T) {
	defer func() { ListConfigBackupsFunc = nil }()
	ListConfigBackupsFunc = func() ([]ConfigBackupInfo, error) { return nil, ErrNoConfigPath }
	ts := newBackupsTestServer(t)

	resp, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups", ""))
	if err != nil {
		t.Fatalf("GET /configs/backups: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no-config-path list: got %d, want 503", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if !strings.Contains(body["error"], "no active config path") {
		t.Errorf("error body = %q, want a clear no-config-path message", body["error"])
	}
}

// backupNameForTests is one concrete well-formed backup name used by the
// content/restore tests.
const backupNameForTests = "1234567890123456789.yaml"

// mockGetConfigBackupFunc mimics the executor's GetConfigBackup semantics
// for the route tests: exact-name match, strict name validation, typed
// errors.
func mockGetConfigBackupFunc() func(string) (string, error) {
	return func(name string) (string, error) {
		switch name {
		case backupNameForTests:
			return "global:\n  log-level: info\n", nil
		default:
			switch {
			case strings.Contains(name, "/") || strings.Contains(name, "..") || !strings.HasSuffix(name, ".yaml"):
				return "", fmt.Errorf("%w: %q", ErrInvalidBackupName, name)
			default:
				return "", fmt.Errorf("%w: %s", ErrBackupNotFound, name)
			}
		}
	}
}

// TestGetConfigBackupContent verifies GET /configs/backups/{name}: raw YAML
// body with application/yaml content type (mirroring GET /configs/raw), 404
// for unknown names, auth required.
func TestGetConfigBackupContent(t *testing.T) {
	defer func() { GetConfigBackupFunc = nil }()
	GetConfigBackupFunc = mockGetConfigBackupFunc()
	ts := newBackupsTestServer(t)

	// Without auth → 401.
	resp, err := http.Get(ts.URL + "/configs/backups/" + backupNameForTests)
	if err != nil {
		t.Fatalf("GET backup content: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth GET backup content: got %d, want 401", resp.StatusCode)
	}

	// With auth → 200 + application/yaml + the stored YAML text.
	resp2, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups/"+backupNameForTests, ""))
	if err != nil {
		t.Fatalf("authed GET backup content: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET backup content: got %d, want 200", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {
		t.Errorf("Content-Type: got %q, want application/yaml*", ct)
	}
	body, _ := io.ReadAll(resp2.Body)
	if string(body) != "global:\n  log-level: info\n" {
		t.Errorf("backup content = %q, want the stored YAML text", string(body))
	}

	// Unknown but well-formed name → 404.
	resp3, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups/1111111111111111111.yaml", ""))
	if err != nil {
		t.Fatalf("GET unknown backup: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("GET unknown backup: got %d, want 404", resp3.StatusCode)
	}

	// No config path → 503 with a clear message.
	GetConfigBackupFunc = func(string) (string, error) { return "", ErrNoConfigPath }
	resp4, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups/"+backupNameForTests, ""))
	if err != nil {
		t.Fatalf("GET backup (no config path): %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET backup (no config path): got %d, want 503", resp4.StatusCode)
	}
}

// TestGetConfigBackupContent_TraversalRejected verifies path-traversal
// attempts in the {name} slot are rejected: single-segment traversal
// fragments reach the handler and are answered 400 (invalid backup name),
// and multi-segment escapes do not resolve to any backup route.
func TestGetConfigBackupContent_TraversalRejected(t *testing.T) {
	defer func() { GetConfigBackupFunc = nil }()
	GetConfigBackupFunc = mockGetConfigBackupFunc()
	ts := newBackupsTestServer(t)

	// ".." is a single route segment: it reaches the handler and must be
	// classified as an invalid backup name → 400.
	resp, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups/..", ""))
	if err != nil {
		t.Fatalf("GET /configs/backups/..: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("GET /configs/backups/..: got %d, want 400", resp.StatusCode)
	}

	// A name smuggling extra path segments must never resolve to a backup
	// file — assert the request is rejected outright (chi's 404) or caught
	// by the handler's 400; never 200.
	resp2, err := ts.Client().Do(authedRequest(t, http.MethodGet, ts.URL+"/configs/backups/..%2F..%2Fsecret.yaml", ""))
	if err != nil {
		t.Fatalf("GET traversal backup: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("traversal request returned 200 — content escape!")
	}
	if resp2.StatusCode != http.StatusBadRequest && resp2.StatusCode != http.StatusNotFound {
		t.Errorf("traversal request: got %d, want 400 or 404 (rejected)", resp2.StatusCode)
	}
}

// TestRestoreConfigBackup verifies POST /configs/backups/{name}/restore:
// 204 on success, 404 for unknown backups, 400 with the validation message
// when the backup no longer validates, masked 500 on apply failure, and
// auth required.
func TestRestoreConfigBackup(t *testing.T) {
	defer func() { RestoreConfigBackupFunc = nil }()
	restored := ""
	RestoreConfigBackupFunc = func(name string) error {
		restored = name
		switch name {
		case backupNameForTests:
			return nil
		case "1111111111111111111.yaml":
			return fmt.Errorf("%w: config validation failed: api.secret must be at least 8 characters", ErrBackupInvalid)
		case "1222222222222222222.yaml":
			return fmt.Errorf("%w: %s", ErrBackupNotFound, name)
		default:
			return errors.New("driver rebuild failed")
		}
	}
	ts := newBackupsTestServer(t)

	// Without auth → 401.
	resp, err := http.Post(ts.URL+"/configs/backups/"+backupNameForTests+"/restore", "", nil)
	if err != nil {
		t.Fatalf("POST restore (no auth): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth restore: got %d, want 401", resp.StatusCode)
	}

	// Success → 204, and the exact {name} was passed through.
	resp2, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups/"+backupNameForTests+"/restore", ""))
	if err != nil {
		t.Fatalf("POST restore: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNoContent {
		t.Errorf("restore: got %d, want 204", resp2.StatusCode)
	}
	if restored != backupNameForTests {
		t.Errorf("restore was called with %q, want %q", restored, backupNameForTests)
	}

	// Unknown backup → 404.
	resp3, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups/1222222222222222222.yaml/restore", ""))
	if err != nil {
		t.Fatalf("POST restore (missing): %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("restore(missing): got %d, want 404", resp3.StatusCode)
	}

	// Backup that no longer validates → 400 with the underlying message,
	// proving restore never bypasses validation.
	resp4, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups/1111111111111111111.yaml/restore", ""))
	if err != nil {
		t.Fatalf("POST restore (invalid): %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusBadRequest {
		t.Fatalf("restore(invalid): got %d, want 400", resp4.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp4.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if !strings.Contains(body["error"], "validation") {
		t.Errorf("restore(invalid) error = %q, want a validation message", body["error"])
	}

	// Apply failure → masked 500, like PUT /configs.
	resp5, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups/1333333333333333333.yaml/restore", ""))
	if err != nil {
		t.Fatalf("POST restore (apply failure): %v", err)
	}
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusInternalServerError {
		t.Errorf("restore(apply failure): got %d, want 500", resp5.StatusCode)
	}
	var masked map[string]string
	if err := json.NewDecoder(resp5.Body).Decode(&masked); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if masked["error"] != "internal server error" {
		t.Errorf("restore(apply failure) body = %q, want masked internal server error", masked["error"])
	}
}

// TestCreateConfigBackup verifies POST /configs/backups: manual snapshot
// returns 201 with the new backup name; no-config-path → 503; missing disk
// file → 404; auth required.
func TestCreateConfigBackup(t *testing.T) {
	defer func() { CreateConfigBackupFunc = nil }()
	CreateConfigBackupFunc = func() (string, error) {
		return "1555555555555555555.yaml", nil
	}
	ts := newBackupsTestServer(t)

	// Without auth → 401.
	resp, err := http.Post(ts.URL+"/configs/backups", "", nil)
	if err != nil {
		t.Fatalf("POST backup (no auth): %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no-auth create backup: got %d, want 401", resp.StatusCode)
	}

	// Success → 201 {"name": "..."}.
	resp2, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups", ""))
	if err != nil {
		t.Fatalf("POST backup: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("create backup: got %d, want 201", resp2.StatusCode)
	}
	var created map[string]string
	if err := json.NewDecoder(resp2.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode created response: %v", err)
	}
	if created["name"] != "1555555555555555555.yaml" {
		t.Errorf("created backup name = %q, want 1555555555555555555.yaml", created["name"])
	}

	// No config path → 503.
	CreateConfigBackupFunc = func() (string, error) { return "", ErrNoConfigPath }
	resp3, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups", ""))
	if err != nil {
		t.Fatalf("POST backup (no path): %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("create backup (no config path): got %d, want 503", resp3.StatusCode)
	}

	// Config path set but no file on disk → 404.
	CreateConfigBackupFunc = func() (string, error) { return "", ErrConfigFileMissing }
	resp4, err := ts.Client().Do(authedRequest(t, http.MethodPost, ts.URL+"/configs/backups", ""))
	if err != nil {
		t.Fatalf("POST backup (missing file): %v", err)
	}
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusNotFound {
		t.Errorf("create backup (missing file): got %d, want 404", resp4.StatusCode)
	}
}

// TestConfigBackupEndpoints_NotWired verifies the endpoints fail closed
// (masked 500) when the executor has not been initialized — no panic, no
// open surface.
func TestConfigBackupEndpoints_NotWired(t *testing.T) {
	// All four func vars nil (package default before executor.Init).
	ts := newBackupsTestServer(t)

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/configs/backups"},
		{http.MethodGet, "/configs/backups/" + backupNameForTests},
		{http.MethodPost, "/configs/backups"},
		{http.MethodPost, "/configs/backups/" + backupNameForTests + "/restore"},
	} {
		resp, err := ts.Client().Do(authedRequest(t, tc.method, ts.URL+tc.path, ""))
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s %s (unwired): got %d, want 500", tc.method, tc.path, resp.StatusCode)
		}
		if !strings.Contains(string(body), "internal server error") {
			t.Errorf("%s %s (unwired): body = %q, want masked internal error", tc.method, tc.path, string(body))
		}
	}
}
