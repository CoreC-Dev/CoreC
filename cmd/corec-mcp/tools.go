package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ─── tool definitions ───────────────────────────────────────────────

// toolDef is one MCP tool: a name, a description, and a JSON Schema for its
// arguments. write marks control verbs that are hidden entirely in read-only
// mode -- the listing itself is the authorization.
type toolDef struct {
	name        string
	description string
	schema      map[string]any
	write       bool
}

func obj(props map[string]any, required []string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func (s *server) toolDefs() []map[string]any {
	defs := []toolDef{
		{
			name:        "list_drivers",
			description: "List every configured southbound driver with its type, connection state and tag count.",
		},
		{
			name:        "driver_status",
			description: "Report one driver's status, including whether collection is paused and its scheduler health.",
			schema:      obj(map[string]any{"name": strProp("driver name")}, []string{"name"}),
		},
		{
			name:        "list_tags",
			description: "List the latest collected values (value, type, quality, timestamp) of configured points, optionally filtered by driver or tag name substring.",
			schema: obj(map[string]any{
				"driver": strProp("optional driver name to filter by"),
				"tag":    strProp("optional tag name substring to filter by"),
			}, nil),
		},
		{
			name:        "read_tag",
			description: "Return the latest cached value of one point on one driver.",
			schema: obj(map[string]any{
				"driver": strProp("driver name"),
				"tag":    strProp("tag (point) name"),
			}, []string{"driver", "tag"}),
		},
		{
			name:        "list_alerts",
			description: "List active alerts with their key, message, occurrence count and acknowledgement state, optionally filtered by driver.",
			schema: obj(map[string]any{
				"driver": strProp("optional driver name to filter by"),
			}, nil),
		},
		{
			name:        "list_paused_drivers",
			description: "List drivers whose collection is currently paused.",
		},
		{
			// Read-only on purpose: the agent can inspect and dry-run a
			// configuration before proposing a change, but applying one stays
			// a human action through the dashboard / PUT /configs. Config
			// writes need a permission model (can it touch secret? whose
			// audit entry?) that is not this adapter's to invent.
			name:        "get_configs",
			description: "Read the running configuration as YAML text (GET /configs/raw). Read-only: applying a change is not exposed here.",
		},
		{
			name:        "validate_configs",
			description: "Dry-run a candidate configuration against CoreC's validator without applying it (POST /configs/validate). Reports valid/invalid plus the server's own error message.",
			schema: obj(map[string]any{
				"payload": strProp("full configuration as YAML text"),
			}, []string{"payload"}),
		},
	}

	if s.write {
		defs = append(defs,
			toolDef{
				name:        "write_tag",
				description: "Write a value to a point. The engine enforces per-tag write protection and device write pacing: a read-only point, or a write inside the pacing window, is refused with the reason rather than queued.",
				schema: obj(map[string]any{
					"driver": strProp("driver name"),
					"tag":    strProp("tag (point) name"),
					"type":   strProp("data type: bool, int8, int16, int32, int64, uint8, uint16, uint32, uint64, float32, float64, string, bytes"),
					"value":  map[string]any{"description": "value to write, matching the declared type"},
					"raw":    map[string]any{"type": "boolean", "description": "optional: skip scale/offset inversion and write the raw device value"},
				}, []string{"driver", "tag", "type", "value"}),
				write: true,
			},
			toolDef{
				name:        "pause_driver",
				description: "Pause a driver's collection. The connection is kept; no data points are produced until resumed.",
				schema:      obj(map[string]any{"name": strProp("driver name")}, []string{"name"}),
				write:       true,
			},
			toolDef{
				name:        "resume_driver",
				description: "Resume a paused driver's collection.",
				schema:      obj(map[string]any{"name": strProp("driver name")}, []string{"name"}),
				write:       true,
			},
			toolDef{
				name:        "ack_alert",
				description: "Acknowledge an active alert by key (the key is returned by list_alerts). Acknowledgement does not clear it; only silence for the active window does.",
				schema:      obj(map[string]any{"key": strProp("alert key")}, []string{"key"}),
				write:       true,
			},
		)
	}

	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		in := d.schema
		if in == nil {
			in = obj(nil, nil)
		}
		out = append(out, map[string]any{
			"name":        d.name,
			"description": d.description,
			"inputSchema": in,
		})
	}
	return out
}

func (s *server) hasTool(name string) bool {
	for _, t := range s.toolDefs() {
		if t["name"] == name {
			return true
		}
	}
	return false
}

// handleToolCall validates the envelope, then dispatches. Unknown tool names
// (including control verbs while read-only) are invalid-params errors, so a
// read-only server is indistinguishable from one that never had them.
func (s *server) handleToolCall(req rpcRequest) any {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		return s.fail(req.ID, codeInvalidParams, "tools/call requires name and arguments")
	}
	args := map[string]any{}
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return s.fail(req.ID, codeInvalidParams, "arguments must be an object: "+err.Error())
		}
	}
	if !s.hasTool(p.Name) {
		return s.fail(req.ID, codeInvalidParams, fmt.Sprintf("unknown tool %q", p.Name))
	}

	text, isErr := s.callTool(p.Name, args)
	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
	if isErr {
		result["isError"] = true
	}
	return s.ok(req.ID, result)
}

// ─── tool execution ──────────────────────────────────────────────────

func (s *server) callTool(name string, args map[string]any) (string, bool) {
	switch name {
	case "list_drivers":
		return s.get("/drivers")
	case "driver_status":
		return s.get("/drivers/" + url.PathEscape(argStr(args, "name")))
	case "list_tags":
		q := url.Values{}
		if d := argStr(args, "driver"); d != "" {
			q.Set("driver", d)
		}
		if t := argStr(args, "tag"); t != "" {
			q.Set("tag", t)
		}
		return s.get("/tags" + withQuery(q))
	case "read_tag":
		driver, tag := argStr(args, "driver"), argStr(args, "tag")
		if driver == "" || tag == "" {
			return "read_tag requires non-empty driver and tag", true
		}
		q := url.Values{}
		q.Set("driver", driver)
		q.Set("tag", tag)
		text, isErr := s.get("/tags" + withQuery(q))
		if isErr {
			return text, true
		}
		return exactTag(text, driver, tag), false
	case "list_alerts":
		q := url.Values{}
		if d := argStr(args, "driver"); d != "" {
			q.Set("driver", d)
		}
		return s.get("/alerts" + withQuery(q))
	case "list_paused_drivers":
		return s.get("/drivers/paused")
	case "get_configs":
		// /configs/raw answers YAML. The JSON client here would hand back a
		// quoted blob, so fetch it as text like the dashboard does.
		return s.getRaw("/configs/raw", "application/yaml")
	case "validate_configs":
		payload := argStr(args, "payload")
		if payload == "" {
			return "validate_configs requires a non-empty payload", true
		}
		// Dry run: the server parses and checks but keeps no copy. A rejected
		// config comes back as its normal error body, which the agent can act
		// on -- no special casing, same as every other verb.
		return s.post("/configs/validate", map[string]any{"payload": payload})
	case "write_tag":
		body := map[string]any{
			"driver": argStr(args, "driver"),
			"tag":    argStr(args, "tag"),
			"type":   argStr(args, "type"),
			"value":  args["value"],
		}
		if v, ok := args["raw"].(bool); ok {
			body["raw"] = v
		}
		return s.post("/write", body)
	case "pause_driver":
		return s.post("/drivers/"+url.PathEscape(argStr(args, "name"))+"/pause", nil)
	case "resume_driver":
		return s.post("/drivers/"+url.PathEscape(argStr(args, "name"))+"/resume", nil)
	case "ack_alert":
		return s.post("/alerts/ack", map[string]any{"key": argStr(args, "key")})
	}
	return "unknown tool " + name, true
}

func argStr(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

func withQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// ─── HTTP ────────────────────────────────────────────────────────────

func (s *server) get(path string) (string, bool) {
	req, err := http.NewRequest(http.MethodGet, s.base+path, http.NoBody)
	if err != nil {
		return err.Error(), true
	}
	return s.do(req)
}

// getRaw is get() with an Accept header. /configs/raw content-negotiates: the
// default is JSON-looking while the dashboard asks for application/yaml,ullo and a
// config the caller intends to re-submit should come back in the shape it was
// written.
func (s *server) getRaw(path, accept string) (string, bool) {
	req, err := http.NewRequest(http.MethodGet, s.base+path, http.NoBody)
	if err != nil {
		return err.Error(), true
	}
	req.Header.Set("Accept", accept)
	return s.do(req)
}

func (s *server) post(path string, body any) (string, bool) {
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err.Error(), true
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, s.base+path, rdr)
	if err != nil {
		return err.Error(), true
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return s.do(req)
}

// do performs one authenticated request. Non-2xx responses are surfaced to
// the agent verbatim: the status code and the engine's reason are exactly what
// lets an agent distinguish "fix the command" (400/422) from "back off"
// (429) from "misconfigured" (401/500), so they are never swallowed.
func (s *server) do(req *http.Request) (string, bool) {
	req.Header.Set("Authorization", "Bearer "+s.secret)
	resp, err := s.client.Do(req)
	if err != nil {
		return "request failed: " + err.Error(), true
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "read response failed: " + err.Error(), true
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		text = "(empty body)"
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Sprintf("HTTP %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), text), true
	}
	return text, false
}

// exactTag narrows a GET /tags substring result to the single point the caller
// named. The REST endpoint matches the tag parameter as a SUBSTRING, so without
// this filter read_tag("temp") would also return "temp_in" and "temp_out" -- and
// the agent would silently reason about the wrong values.
//
// An unparseable body, or a match whose driver differs, is passed through
// unchanged: the adapter relays what the engine said, it does not turn a
// response into a prettier lie.
func exactTag(raw, driver, tag string) string {
	var body struct {
		Tags map[string]any `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil || body.Tags == nil {
		return raw
	}
	want, ok := body.Tags[tag]
	if !ok {
		return raw
	}
	if m, ok := want.(map[string]any); ok {
		if got, _ := m["driver"].(string); got != "" && got != driver {
			return raw
		}
	}
	out := map[string]any{"tags": map[string]any{tag: want}}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return string(b)
}
