package mqtt

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
)

// closedPort returns a host:port that is guaranteed to be closed (no
// listener) by binding to an OS-chosen port and immediately closing it.
// Connecting to it will be refused, which is what we want for negative
// connect tests.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to get free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// TestMQTTStartFailsWhenNoReconnect verifies H10: when the initial MQTT
// connection fails and auto-reconnect is disabled (both auto-reconnect
// and connect-retry are false), Start() surfaces the error instead of
// returning nil and leaving the transport silently stuck.
func TestMQTTStartFailsWhenNoReconnect(t *testing.T) {
	addr := closedPort(t)
	mtr := newInitdTransport(t, "no-reconnect", map[string]any{
		"broker":          "tcp://" + addr,
		"auto-reconnect":  false,
		"connect-retry":   false,
		"connect-timeout": "1s",
	})

	err := mtr.Start(context.Background())
	if err == nil {
		// Clean up any background state before failing.
		_ = mtr.Stop()
		t.Fatal("expected Start to return an error when connect fails and auto-reconnect is disabled")
	}
	if !strings.Contains(err.Error(), "connect") {
		t.Errorf("error should mention connect failure, got %q", err.Error())
	}

	st := mtr.Status()
	if st.State != core.StateError {
		t.Errorf("expected state StateError after failed Start, got %v", st.State)
	}

	// Stop must still be safe after a failed Start.
	if err := mtr.Stop(); err != nil {
		t.Errorf("Stop after failed Start: got error %v, want nil", err)
	}
}

// TestMQTTStartReturnsNilWhenReconnectEnabled verifies that when a retry
// mechanism is enabled, Start() preserves the historical behaviour of
// returning nil and letting the background retry recover the connection.
func TestMQTTStartReturnsNilWhenReconnectEnabled(t *testing.T) {
	addr := closedPort(t)
	mtr := newInitdTransport(t, "reconnect-on", map[string]any{
		"broker":          "tcp://" + addr,
		"auto-reconnect":  true,
		"connect-retry":   true,
		"connect-timeout": "300ms",
	})

	err := mtr.Start(context.Background())
	// The background retry goroutine is now running; ensure it is stopped
	// regardless of the assertion outcome.
	defer func() {
		if mtr.client != nil {
			mtr.client.Disconnect(100)
		}
		_ = mtr.Stop()
	}()

	if err != nil {
		t.Fatalf("expected Start to return nil when auto-reconnect is enabled, got %v", err)
	}
}

// Ensure imports are used.
var _ = time.Second
