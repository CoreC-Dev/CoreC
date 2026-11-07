package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeCoreC stands in for the control plane: it records the requests the
// adapter makes and answers with canned payloads.
func fakeCoreC(t *testing.T, status int, body string) (srv *httptest.Server, paths *[]string) {
	t.Helper()
	var seen []string
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sekrit" {
			t.Errorf("Authorization = %q, want the bearer secret", got)
		}
		seen = append(seen, r.URL.RequestURI())
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestInitializeAndToolsListReadOnly(t *testing.T) {
	srv, _ := fakeCoreC(t, 200, `{}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: false}

	init := s.handleLine([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	var initResp rpcResponse
	b, _ := json.Marshal(init)
	if err := json.Unmarshal(b, &initResp); err != nil {
		t.Fatalf("initialize response not a rpcResponse: %v", err)
	}
	if initResp.Error != nil {
		t.Fatalf("initialize failed: %+v", initResp.Error)
	}

	names := toolNames(t, s.toolDefs())
	want := []string{"list_drivers", "driver_status", "list_tags", "read_tag", "list_alerts", "list_paused_drivers"}
	for _, w := range want {
		if !names[w] {
			t.Errorf("read-only tools missing %q", w)
		}
	}
	for _, forbidden := range []string{"write_tag", "pause_driver", "resume_driver", "ack_alert"} {
		if names[forbidden] {
			t.Errorf("read-only server must not expose %q", forbidden)
		}
	}
}

func TestWriteToolsExposedWithAllowWrite(t *testing.T) {
	srv, _ := fakeCoreC(t, 200, `{}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: true}
	names := toolNames(t, s.toolDefs())
	for _, w := range []string{"write_tag", "pause_driver", "resume_driver", "ack_alert"} {
		if !names[w] {
			t.Errorf("write mode missing tool %q", w)
		}
	}
}

func TestCallToolForwardsToRest(t *testing.T) {
	srv, paths := fakeCoreC(t, 200, `{"drivers":[]}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: false}

	text, isErr := s.callTool("list_tags", map[string]any{"driver": "plc 1", "tag": "temp"})
	if isErr {
		t.Fatalf("list_tags failed: %s", text)
	}
	if len(*paths) != 1 {
		t.Fatalf("expected one HTTP request, got %d", len(*paths))
	}
	got := (*paths)[0]
	if !strings.HasPrefix(got, "/tags?") {
		t.Fatalf("path = %q, want /tags with a query", got)
	}
	if !strings.Contains(got, "driver=plc+1") {
		t.Errorf("driver not escaped in %q (want plc+1)", got)
	}
}

// TestCallToolSurfacesErrorStatus: the engine's 429 (write paced) and 400
// (write protected) must reach the agent with their status and reason intact,
// otherwise it cannot tell "back off" from "fix the command".
func TestCallToolSurfacesErrorStatus(t *testing.T) {
	srv, _ := fakeCoreC(t, 429, `{"error":"tag \"sp\" is read-only (protect: read-only)"}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: true}

	text, isErr := s.callTool("write_tag", map[string]any{
		"driver": "plc", "tag": "sp", "type": "int16", "value": 1, "raw": true,
	})
	if !isErr {
		t.Fatal("a 429 must be reported as a tool error")
	}
	if !strings.Contains(text, "HTTP 429") || !strings.Contains(text, "read-only") {
		t.Errorf("error text should carry status and reason, got %q", text)
	}
}

func TestUnknownMethodAndNotification(t *testing.T) {
	srv, _ := fakeCoreC(t, 200, `{}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client()}

	std := s.handleLine([]byte(`{"jsonrpc":"2.0","id":9,"method":"nope"}`))
	resp, ok := std.(rpcResponse)
	if !ok || resp.Error == nil || resp.Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method should be %d, got %+v", codeMethodNotFound, std)
	}

	// A notification (no id) must produce no response at all.
	if got := s.handleLine([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); got != nil {
		t.Fatalf("notification must not be answered, got %+v", got)
	}
}

func TestServeLoop(t *testing.T) {
	srv, _ := fakeCoreC(t, 200, `{"drivers":[]}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: true}

	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	var out bytes.Buffer
	if err := s.serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 { // notification produced no line
		t.Fatalf("expected 2 response lines, got %d: %v", len(lines), lines)
	}
	for _, ln := range lines {
		var raw map[string]any
		if err := json.Unmarshal([]byte(ln), &raw); err != nil {
			t.Fatalf("line is not JSON: %v", err)
		}
		if raw["jsonrpc"] != "2.0" {
			t.Errorf("missing jsonrpc version: %v", raw)
		}
	}
	_ = fmt.Sprint
}

func toolNames(t *testing.T, defs []map[string]any) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(defs))
	for _, d := range defs {
		name, ok := d["name"].(string)
		if !ok {
			t.Fatalf("tool definition without a name: %v", d)
		}
		out[name] = true
	}
	return out
}

// TestReadTagNarrowsSubstringMatches: REST matches ?tag= as a substring, so a
// plain pass-through would hand the agent temp_in/temp_out when it asked for
// temp. The tool must return only the named point.
func TestReadTagNarrowsSubstringMatches(t *testing.T) {
	body := `{"tags":{"temp":{"tag":"temp","driver":"plc","value":21.5},` +
		`"temp_in":{"tag":"temp_in","driver":"plc","value":1},` +
		`"temp_out":{"tag":"temp_out","driver":"plc","value":2}}}`
	srv, _ := fakeCoreC(t, 200, body)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: false}

	text, isErr := s.callTool("read_tag", map[string]any{"driver": "plc", "tag": "temp"})
	if isErr {
		t.Fatalf("read_tag failed: %s", text)
	}
	var out struct {
		Tags map[string]any `json:"tags"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, text)
	}
	if len(out.Tags) != 1 {
		t.Fatalf("exact match should keep 1 tag, got %d: %s", len(out.Tags), text)
	}
	if _, ok := out.Tags["temp"]; !ok {
		t.Errorf("the named point must survive, got %v", out.Tags)
	}
}

// get_configs must ask for YAML, not JSON: the control plane content-negotiates
// on /configs/raw, and a JSON answer is the config wrapped in a quoted string
// with its newlines escaped -- unreadable to a model and useless to
// validate_configs without a decode step on the caller's side.
func TestGetConfigsAsksForYAML(t *testing.T) {
	var gotAccept, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("global:\n  engine:\n    workers: 4\n"))
	}))
	t.Cleanup(srv.Close)

	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client()}
	text, isErr := s.callTool("get_configs", nil)
	if isErr {
		t.Fatalf("get_configs failed: %s", text)
	}
	if gotPath != "/configs/raw" {
		t.Errorf("path = %q, want /configs/raw", gotPath)
	}
	if gotAccept != "application/yaml" {
		t.Errorf("Accept = %q, want application/yaml", gotAccept)
	}
	if !strings.Contains(text, "workers: 4") {
		t.Errorf("YAML did not come through as text: %q", text)
	}
}

// validate_configs posts the candidate under "payload" -- the field
// POST /configs/validate decodes -- and must not be callable with an empty one,
// which the server would reject as a malformed request rather than an invalid
// config; the two are different answers for the agent.
func TestValidateConfigsPostsPayload(t *testing.T) {
	var gotBody map[string]any
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"valid":false,"error":"driver x: tag y: address cannot be empty"}`))
	}))
	t.Cleanup(srv.Close)

	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client()}
	text, isErr := s.callTool("validate_configs", map[string]any{"payload": "drivers: []"})
	if isErr {
		t.Fatalf("validate_configs: the engine's verdict is an answer, not a transport error: %s", text)
	}
	if gotPath != "/configs/validate" {
		t.Errorf("path = %q, want /configs/validate", gotPath)
	}
	if gotBody["payload"] != "drivers: []" {
		t.Errorf("payload sent as %v, want the candidate verbatim", gotBody["payload"])
	}
	if !strings.Contains(text, "address cannot be empty") {
		t.Errorf("validator's reason missing from %q", text)
	}
}

// An empty payload is refused locally: asking the server would produce a
// malformed-request error that reads like "your config is wrong".
func TestValidateConfigsRefusesEmptyPayload(t *testing.T) {
	srv, paths := fakeCoreC(t, 200, `{}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: true}

	text, isErr := s.callTool("validate_configs", map[string]any{})
	if !isErr {
		t.Fatalf("empty payload accepted: %q", text)
	}
	if len(*paths) != 0 {
		t.Errorf("sent %d request(s) to the server, want none", len(*paths))
	}
}

// TestToolsCallEnvelope covers the path an agent actually takes:
// tools/call over the JSON-RPC line, not callTool directly. It is the branch
// hasTool() guards -- unknown names must be invalid-params here, before any
// HTTP request is made, and a tool's isErr must land in the result's isError
// field rather than in the error object, or the agent cannot tell "this verb
// failed" from "this server does not speak JSON-RPC".
func TestToolsCallEnvelope(t *testing.T) {
	srv, paths := fakeCoreC(t, 200, `{"drivers":[]}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client(), write: false}

	cases := []struct {
		name     string
		params   string
		wantErr  bool
		wantPath int // number of HTTP requests expected
	}{
		{name: "known tool", params: `{"name":"list_drivers","arguments":{}}`, wantPath: 1},
		{name: "no arguments field", params: `{"name":"list_drivers"}`, wantPath: 1},
		{name: "control verb hidden in read-only", params: `{"name":"write_tag","arguments":{}}`, wantErr: true, wantPath: 0},
		{name: "unknown tool", params: `{"name":"drop_database","arguments":{}}`, wantErr: true, wantPath: 0},
		{name: "missing name", params: `{"arguments":{}}`, wantErr: true, wantPath: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			*paths = nil
			req := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":%s}`, tc.params)
			resp := s.handleLine([]byte(req))

			b, err := json.Marshal(resp)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if tc.wantErr {
				if !strings.Contains(string(b), `"error"`) {
					t.Fatalf("want a JSON-RPC error, got %s", b)
				}
			} else if strings.Contains(string(b), `"error"`) {
				t.Fatalf("want a result, got %s", b)
			}
			if len(*paths) != tc.wantPath {
				t.Errorf("made %d HTTP request(s) %v, want %d", len(*paths), *paths, tc.wantPath)
			}
		})
	}
}

// An invalid config is this verb's ANSWER, not its failure. corec's validator
// reports "invalid" as 400 + a JSON body; post() maps every non-2xx to isErr, so
// without the downgrade in callConfig the agent is told its tool call broke --
// and an agent that thinks a call broke retries it or apologises to the operator,
// instead of fixing the config. 401 and friends are NOT downgraded: those mean
// the call did not happen, which is a different sentence to a model.
func TestValidateConfigsReports400AsVerdict(t *testing.T) {
	srv, _ := fakeCoreC(t, 400, `{"error":"api.secret is required when api.listen is set","valid":false}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client()}

	text, isErr := s.callTool("validate_configs", map[string]any{"payload": "global: {}"})
	if isErr {
		t.Fatalf("an invalid config must not be reported as a failed call: %q", text)
	}
	if !strings.Contains(text, "api.secret is required") {
		t.Errorf("validator's reason lost: %q", text)
	}
}

func TestValidateConfigsKeepsAuthFailureAsError(t *testing.T) {
	srv, _ := fakeCoreC(t, 401, `{"error":"unauthorized"}`)
	s := &server{base: srv.URL, secret: "sekrit", client: srv.Client()}

	text, isErr := s.callTool("validate_configs", map[string]any{"payload": "global: {}"})
	if !isErr {
		t.Fatalf("a 401 must stay an error: %q", text)
	}
}
