package httppush

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// newWebhookTransport builds, Inits, and Starts an HTTP transport with a
// webhook on a free port and the given shared secret (empty disables
// auth).  The caller must defer transport.Stop().
func newWebhookTransport(t *testing.T, name, secret string) (transport core.Transport, addr string) {
	t.Helper()
	addr = freePort(t)
	settings := map[string]any{
		"url":          "http://localhost:9999/no-such-server",
		"webhook-addr": addr,
		"webhook-path": "/data",
	}
	if secret != "" {
		settings["webhook-secret"] = secret
	}
	cfg := core.TransportConfig{
		Name:     name,
		Type:     "http",
		Settings: settings,
	}
	tr, err := NewHTTPTransport(cfg)
	if err != nil {
		t.Fatalf("NewHTTPTransport: %v", err)
	}
	if err := tr.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForServer(t, addr)
	return tr, addr
}

func postWebhook(t *testing.T, addr, authHeader, xSecret string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/data", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if xSecret != "" {
		req.Header.Set("X-Webhook-Secret", xSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

func validDataPoint() []byte {
	dp := core.DataPoint{Driver: "up", Tag: "temp", Value: 1.0, Type: core.TypeFloat64, Timestamp: time.Now()}
	b, _ := json.Marshal(dp)
	return b
}

// TestWebhookAuthBearer verifies that a configured webhook-secret
// accepts a request carrying the correct Bearer token.
func TestWebhookAuthBearer(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-bearer", "s3cr3t")
	defer tr.Stop()

	resp := postWebhook(t, addr, "Bearer s3cr3t", "", validDataPoint())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 with valid Bearer, got %d", resp.StatusCode)
	}
}

// TestWebhookAuthXSecret verifies the X-Webhook-Secret header path.
func TestWebhookAuthXSecret(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-xsecret", "s3cr3t")
	defer tr.Stop()

	resp := postWebhook(t, addr, "", "s3cr3t", validDataPoint())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 with valid X-Webhook-Secret, got %d", resp.StatusCode)
	}
}

// TestWebhookAuthReject verifies that missing or wrong credentials are
// rejected with 401 and the body is never processed.
func TestWebhookAuthReject(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-reject", "s3cr3t")
	defer tr.Stop()

	cases := []struct {
		name   string
		auth   string
		secret string
	}{
		{"no credentials", "", ""},
		{"wrong bearer", "Bearer nope", ""},
		{"wrong x-secret", "", "nope"},
		{"malformed bearer", "s3cr3t", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := postWebhook(t, addr, tc.auth, tc.secret, validDataPoint())
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", resp.StatusCode)
			}
		})
	}

	// No data should have been ingested.
	if got := tr.Status().Received; got != 0 {
		t.Errorf("expected Received=0 after all-401 requests, got %d", got)
	}
}

// TestWebhookNoSecretBackwardCompat verifies that without a webhook-secret
// requests are accepted unauthenticated (backward compatibility).
func TestWebhookNoSecretBackwardCompat(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-nosecret", "")
	defer tr.Stop()

	resp := postWebhook(t, addr, "", "", validDataPoint())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 without auth when no secret set, got %d", resp.StatusCode)
	}
}

// TestWebhookBodySizeLimit verifies that an oversized body is rejected
// (H1: OOM protection via http.MaxBytesReader).
func TestWebhookBodySizeLimit(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-sizelimit", "")
	defer tr.Stop()

	// webhookMaxBodyBytes is 10 MiB; send one byte more than the limit.
	oversized := bytes.Repeat([]byte("x"), webhookMaxBodyBytes+1)
	resp := postWebhook(t, addr, "", "", oversized)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized body, got %d", resp.StatusCode)
	}
}

// TestWebhookBodyUnderLimit verifies a body just under the limit is
// accepted (the limit does not reject legitimate payloads).
func TestWebhookBodyUnderLimit(t *testing.T) {
	tr, addr := newWebhookTransport(t, "wh-underlimit", "")
	defer tr.Stop()

	// Send a valid JSON array of small points whose total size is well
	// under 10 MiB but non-trivial.  Keep the count within the default
	// data-channel buffer (100) so nothing is dropped.
	points := make([]core.DataPoint, 50)
	for i := range points {
		points[i] = core.DataPoint{Driver: "d", Tag: "t", Value: float64(i), Type: core.TypeFloat64, Timestamp: time.Now()}
	}
	body, _ := json.Marshal(points)
	if len(body) >= webhookMaxBodyBytes {
		t.Fatalf("test body too large: %d bytes", len(body))
	}
	resp := postWebhook(t, addr, "", "", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 for under-limit body, got %d", resp.StatusCode)
	}
}
