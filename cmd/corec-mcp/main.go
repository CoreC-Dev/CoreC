// Package main implements corec-mcp: a stateless Model Context Protocol
// adapter that exposes a running CoreC instance to AI agents.
//
// Command corec-mcp exposes a running CoreC instance to AI agents over the
// Model Context Protocol.
//
// It is a SEPARATE executable, deliberately: MCP brings a client SDK, stdio
// framing and a second long-lived protocol stack, none of which belong in the
// single-binary industrial core (core-beliefs: no non-internalisable
// dependencies; keep the cross-compile matrix intact). This binary is a thin,
// stateless adapter: it holds no device state of its own and performs no
// writes unless explicitly started with -allow-write.
//
// Every tool delegates to CoreC's control-plane REST API, so the existing
// guarantees apply unchanged: the API secret authenticates each call,
// per-tag write protection rejects read-only points, write pacing refuses
// writes that arrive too soon, and each write lands in the audit history.
//
// Usage:
//
//	corec-mcp -url http://127.0.0.1:8080 -secret $COREC_API_SECRET
//	corec-mcp -url ... -secret ... -allow-write
//
// The secret may also come from COREC_API_SECRET. Transport is stdio
// (newline-delimited JSON-RPC 2.0), which is what local MCP clients use.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// defaultTimeout bounds one control-plane round trip. Writes legitimately take
// longer than reads (driver retries), so the timeout is generous rather than
// tight; the engine's own deadlines do the fine-grained enforcement.
const defaultTimeout = 30 * time.Second

func main() {
	var (
		baseURL    = flag.String("url", "", "CoreC API base URL, e.g. http://127.0.0.1:8080 (required)")
		secret     = flag.String("secret", "", "CoreC API secret (or set COREC_API_SECRET)")
		allowWrite = flag.Bool("allow-write", false, "expose write tools (default: read-only)")
		timeout    = flag.Duration("timeout", defaultTimeout, "per-request HTTP timeout")
	)
	flag.Parse()

	if *baseURL == "" {
		fmt.Fprintln(os.Stderr, "corec-mcp: -url is required (e.g. http://127.0.0.1:8080)")
		os.Exit(2)
	}
	if u, err := url.Parse(*baseURL); err != nil || u.Scheme == "" || u.Host == "" {
		fmt.Fprintf(os.Stderr, "corec-mcp: invalid -url %q (want scheme://host:port)\n", *baseURL)
		os.Exit(2)
	}

	key := *secret
	if key == "" {
		key = os.Getenv("COREC_API_SECRET")
	}
	if key == "" {
		fmt.Fprintln(os.Stderr, "corec-mcp: no API secret: pass -secret or set COREC_API_SECRET")
		os.Exit(2)
	}

	srv := &server{
		base:   strings.TrimRight(*baseURL, "/"),
		secret: key,
		client: &http.Client{Timeout: *timeout},
		write:  *allowWrite,
	}
	if !*allowWrite {
		fmt.Fprintln(os.Stderr, "corec-mcp: read-only mode (pass -allow-write to enable control tools)")
	}
	if err := srv.serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "corec-mcp: %v\n", err)
		os.Exit(1)
	}
}
