package route

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CoreC-Dev/CoreC/core"
)

// mockEngineWithPreview adds the optional RulePreviewer role to the base
// mock, so the route layer's type assertion succeeds.
type mockEngineWithPreview struct {
	mockEngineV2
	previewMatched bool
	previewErr     error
	previewGot     string
}

func (m *mockEngineWithPreview) PreviewRule(match string, point core.DataPoint) (bool, error) {
	m.previewGot = match
	return m.previewMatched, m.previewErr
}

// postJSONBody posts a JSON body and returns the un-closed response.
func postJSONBody(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", ts.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s failed: %v", path, err)
	}
	return resp
}

func TestPreviewRuleMatched(t *testing.T) {
	eng := &mockEngineWithPreview{previewMatched: true}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `{
		"match": "tag == 'temperature' && value > 95",
		"point": {"driver": "plc1", "tag": "temperature", "value": 96.5, "type": "float64", "quality": 0, "timestamp": "2024-09-08T10:30:00Z"}
	}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Matched bool `json:"matched"`
	}
	decodeJSON(t, resp, &result)
	if !result.Matched {
		t.Error("expected matched=true")
	}
	if want := "tag == 'temperature' && value > 95"; eng.previewGot != want {
		t.Errorf("engine received match: got %q, want %q", eng.previewGot, want)
	}
}

func TestPreviewRuleNotMatched(t *testing.T) {
	eng := &mockEngineWithPreview{previewMatched: false}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `{"match": "value > 900", "point": {"tag": "t", "value": 1}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result struct {
		Matched bool `json:"matched"`
	}
	decodeJSON(t, resp, &result)
	if result.Matched {
		t.Error("expected matched=false")
	}
}

func TestPreviewRuleCompileError(t *testing.T) {
	eng := &mockEngineWithPreview{previewErr: errors.New("failed to create rule preview: unexpected token EOF")}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `{"match": "tag ==", "point": {"tag": "t"}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a compile error, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] == "" {
		t.Error("expected the compile error message in the response body so the frontend can display it")
	}
}

func TestPreviewRuleNotImplemented(t *testing.T) {
	// mockEngineV2 does not implement core.RulePreviewer → 501.
	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `{"match": "value > 1", "point": {"tag": "t", "value": 2}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] == "" {
		t.Error("expected a non-empty error message in the 501 body")
	}
}

func TestPreviewRuleBadJSON(t *testing.T) {
	ts := newTestServer("", &mockEngineV2{})
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `not json`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestPreviewRuleEmptyMatch(t *testing.T) {
	eng := &mockEngineWithPreview{}
	ts := newTestServer("", eng)
	defer ts.Close()

	resp := postJSONBody(t, ts, "/rules/preview", `{"match": "", "point": {"tag": "t", "value": 1}}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for an empty match, got %d", resp.StatusCode)
	}
	var errResp map[string]string
	decodeJSON(t, resp, &errResp)
	if errResp["error"] == "" {
		t.Error("expected a non-empty error message")
	}
	if eng.previewGot != "" {
		t.Error("engine must not be consulted when the match expression is empty")
	}
}
