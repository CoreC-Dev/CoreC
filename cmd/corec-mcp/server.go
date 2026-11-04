package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const (
	protoVersion  = "2025-06-18"
	serverName    = "corec"
	serverVersion = "1.0.0"
)

// server is a stateless MCP adapter over the CoreC control-plane REST API.
// It owns no device state: every tool call is one authenticated HTTP request.
type server struct {
	base   string // CoreC base URL without a trailing slash
	secret string // API secret, sent as a Bearer token
	client *http.Client
	write  bool // whether write-class tools are exposed
}

// ─── JSON-RPC 2.0 wire types ─────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// JSON-RPC error codes used by this server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
)

// serve runs the newline-delimited JSON-RPC loop until stdin closes. Each line
// is one request; each response is written as one line and flushed.
func (s *server) serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20) // 4 MiB max request line
	w := bufio.NewWriter(out)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if resp := s.handleLine(line); resp != nil {
			if err := writeJSON(w, resp); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func writeJSON(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.Flush()
}

// handleLine dispatches one request. A nil result means "notification: no
// response" (JSON-RPC 2.0 requires silence for notifications).
func (s *server) handleLine(line []byte) any {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: codeParseError, Message: "parse error: " + err.Error()}}
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return s.fail(req.ID, codeInvalidRequest, `jsonrpc must be "2.0"`)
	}
	if req.Method == "" {
		return s.fail(req.ID, codeInvalidRequest, "method is required")
	}

	switch req.Method {
	case "initialize":
		return s.ok(req.ID, s.initializeResult(protoVersion))
	case "notifications/initialized", "initialized", "notifications/cancelled":
		return nil
	case "ping":
		return s.ok(req.ID, map[string]any{})
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.toolDefs()})
	case "tools/call":
		return s.handleToolCall(req)
	case "resources/list":
		return s.ok(req.ID, map[string]any{"resources": []any{}})
	case "prompts/list":
		return s.ok(req.ID, map[string]any{"prompts": []any{}})
	default:
		return s.fail(req.ID, codeMethodNotFound, "method not found: "+req.Method)
	}
}

func (s *server) ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *server) fail(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// initializeResult builds the very first response: protocol version, server
// identity, and the capabilities this adapter actually implements.
func (s *server) initializeResult(version string) map[string]any {
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    serverName,
			"version": serverVersion,
		},
	}
}
