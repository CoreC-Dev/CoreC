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
