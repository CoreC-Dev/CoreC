package opcua

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// startSubLoop launches d.subscriptionLoop in a tracked goroutine and returns
// the notification channel plus a stop func that cancels the loop's context
// and waits for it to exit. Callers must defer stop().
func startSubLoop(t *testing.T, d *OPCUADriver,
	handleToTag map[uint32]string, tags map[string]core.TagConfig,
) (notifyCh chan *opcua.PublishNotificationData, stop func()) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	notifyCh = make(chan *opcua.PublishNotificationData, 8)
	d.subWg.Add(1)
	go d.subscriptionLoop(ctx, notifyCh, handleToTag, tags)
	return notifyCh, func() {
		cancelFn()
		d.subWg.Wait()
	}
}

// dataChangeMsg builds a PublishNotificationData carrying a single
// DataChangeNotification for the given client handle and variant value.
func dataChangeMsg(handle uint32, val *ua.Variant, ts time.Time) *opcua.PublishNotificationData {
	return &opcua.PublishNotificationData{
		Value: &ua.DataChangeNotification{
			MonitoredItems: []*ua.MonitoredItemNotification{
				{ClientHandle: handle, Value: &ua.DataValue{Value: val, SourceTimestamp: ts}},
			},
		},
	}
}

// drainSubChannel asserts that no data point arrives on the driver's
// subscription channel within a short window (used for negative assertions).
func drainSubChannel(t *testing.T, ch <-chan core.DataPoint) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	select {
	case dp := <-ch:
		t.Errorf("expected no data point, got %+v", dp)
	default:
	}
}

// ---------------------------------------------------------------------------
// subscriptionLoop
// ---------------------------------------------------------------------------

// TestOPCUASubscriptionLoopForwardsDataChange verifies that a well-formed
// DataChangeNotification is decoded, transformed, and forwarded to the
// driver's subscription channel as a core.DataPoint.
func TestOPCUASubscriptionLoopForwardsDataChange(t *testing.T) {
	d := mustInit(t, validConfig("fwd"))
	handleToTag := map[uint32]string{0: "tag1"}
	tags := map[string]core.TagConfig{"tag1": {Name: "tag1", Type: "float64"}}
	notifyCh, stop := startSubLoop(t, d, handleToTag, tags)
	defer stop()

	ts := time.Now()
	notifyCh <- dataChangeMsg(0, ua.MustVariant(float64(42.0)), ts)

	select {
	case dp := <-d.subChannel:
		if dp.Tag != "tag1" {
			t.Errorf("Tag: expected tag1, got %s", dp.Tag)
		}
		if dp.Value != float64(42.0) {
			t.Errorf("Value: expected 42, got %v", dp.Value)
		}
		if dp.Quality != core.QualityGood {
			t.Errorf("Quality: expected good, got %v", dp.Quality)
		}
		if dp.Driver != "fwd" {
			t.Errorf("Driver: expected fwd, got %s", dp.Driver)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded data point")
	}
}

// TestOPCUASubscriptionLoopErrorNotification verifies that a notification
// carrying an Error is recorded via RecordError and does not produce a data
// point.
func TestOPCUASubscriptionLoopErrorNotification(t *testing.T) {
	d := mustInit(t, validConfig("err"))
	notifyCh, stop := startSubLoop(t, d, map[uint32]string{}, map[string]core.TagConfig{})
	defer stop()

	before := d.ErrorCount()
	notifyCh <- &opcua.PublishNotificationData{Error: errors.New("publish failed")}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.ErrorCount() > before {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d.ErrorCount() <= before {
		t.Error("expected ErrorCount to increment for error notification")
	}
	drainSubChannel(t, d.subChannel)
}

// TestOPCUASubscriptionLoopUnknownHandle verifies that a notification whose
// client handle is not in the handle→tag map is silently skipped.
func TestOPCUASubscriptionLoopUnknownHandle(t *testing.T) {
	d := mustInit(t, validConfig("unk"))
	handleToTag := map[uint32]string{0: "tag1"}
	tags := map[string]core.TagConfig{"tag1": {Name: "tag1", Type: "float64"}}
	notifyCh, stop := startSubLoop(t, d, handleToTag, tags)
	defer stop()

	notifyCh <- dataChangeMsg(999, ua.MustVariant(float64(1.0)), time.Now())
	drainSubChannel(t, d.subChannel)
}

// TestOPCUASubscriptionLoopNilValue covers the nil-value guard: a monitored
// item with a nil DataValue (and one with a nil Variant) must be skipped.
func TestOPCUASubscriptionLoopNilValue(t *testing.T) {
	d := mustInit(t, validConfig("nil"))
	handleToTag := map[uint32]string{0: "tag1"}
	tags := map[string]core.TagConfig{"tag1": {Name: "tag1", Type: "float64"}}
	notifyCh, stop := startSubLoop(t, d, handleToTag, tags)
	defer stop()

	// item.Value itself is nil.
	notifyCh <- &opcua.PublishNotificationData{
		Value: &ua.DataChangeNotification{
			MonitoredItems: []*ua.MonitoredItemNotification{{ClientHandle: 0}},
		},
	}
	drainSubChannel(t, d.subChannel)

	// item.Value is non-nil but its Variant is nil.
	notifyCh <- &opcua.PublishNotificationData{
		Value: &ua.DataChangeNotification{
			MonitoredItems: []*ua.MonitoredItemNotification{
				{ClientHandle: 0, Value: &ua.DataValue{}},
			},
		},
	}
	drainSubChannel(t, d.subChannel)
}

// TestOPCUASubscriptionLoopNonDataChange verifies that a notification whose
// Value is not a *ua.DataChangeNotification is skipped.
func TestOPCUASubscriptionLoopNonDataChange(t *testing.T) {
	d := mustInit(t, validConfig("ndc"))
	notifyCh, stop := startSubLoop(t, d, map[uint32]string{}, map[string]core.TagConfig{})
	defer stop()

	notifyCh <- &opcua.PublishNotificationData{Value: "not a data-change notification"}
	drainSubChannel(t, d.subChannel)
}

// TestOPCUASubscriptionLoopChannelClosed verifies that closing the
// notification channel causes subscriptionLoop to exit cleanly.
func TestOPCUASubscriptionLoopChannelClosed(t *testing.T) {
	d := mustInit(t, validConfig("closed"))
	ctx, cancelFn := context.WithCancel(context.Background())
	defer cancelFn()
	notifyCh := make(chan *opcua.PublishNotificationData, 8)
	d.subWg.Add(1)
	go d.subscriptionLoop(ctx, notifyCh, map[uint32]string{}, map[string]core.TagConfig{})

	close(notifyCh)
	done := make(chan struct{})
	go func() {
		d.subWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriptionLoop did not exit after notifyCh closed")
	}
}

// TestOPCUASubscriptionLoopContextCancelled verifies that cancelling the
// context causes subscriptionLoop to exit cleanly.
func TestOPCUASubscriptionLoopContextCancelled(t *testing.T) {
	d := mustInit(t, validConfig("cancel"))
	ctx, cancelFn := context.WithCancel(context.Background())
	notifyCh := make(chan *opcua.PublishNotificationData, 8)
	d.subWg.Add(1)
	go d.subscriptionLoop(ctx, notifyCh, map[uint32]string{}, map[string]core.TagConfig{})

	cancelFn()
	done := make(chan struct{})
	go func() {
		d.subWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriptionLoop did not exit after context cancel")
	}
}

// TestOPCUASubscriptionLoopChannelFullDrops verifies that when the driver's
// subscription channel is full, a notification is dropped (RecordError) rather
// than blocking the loop.
func TestOPCUASubscriptionLoopChannelFullDrops(t *testing.T) {
	d := mustInit(t, validConfig("full"))
	// Fill the subscription channel to capacity so the next send cannot block.
	for i := 0; i < cap(d.subChannel); i++ {
		d.subChannel <- core.DataPoint{}
	}
	handleToTag := map[uint32]string{0: "tag1"}
	tags := map[string]core.TagConfig{"tag1": {Name: "tag1", Type: "float64"}}
	notifyCh, stop := startSubLoop(t, d, handleToTag, tags)
	defer stop()

	before := d.ErrorCount()
	notifyCh <- dataChangeMsg(0, ua.MustVariant(float64(1.0)), time.Now())

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if d.ErrorCount() > before {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if d.ErrorCount() <= before {
		t.Error("expected ErrorCount to increment when subscription channel is full")
	}
}

// ---------------------------------------------------------------------------
// startSubscription
// ---------------------------------------------------------------------------

// TestOPCUAStartSubscriptionNoClient verifies the early guard: with no client
// connected, startSubscription returns an error without touching the network.
func TestOPCUAStartSubscriptionNoClient(t *testing.T) {
	d := mustInit(t, validConfig("noclient")) // client is nil
	err := d.startSubscription(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no client or tags") {
		t.Errorf("error = %q, want substring 'no client or tags'", err.Error())
	}
}

// TestOPCUAStartSubscriptionNoTags verifies the early guard: with a client but
// no configured tags, startSubscription returns an error.
func TestOPCUAStartSubscriptionNoTags(t *testing.T) {
	cfg := core.DriverConfig{
		Name:     "notags",
		Type:     "opcua",
		Settings: map[string]any{"endpoint": "opc.tcp://127.0.0.1:4840"},
	}
	d := mustInit(t, cfg) // no tags → empty nodeIDs
	client, err := opcua.NewClient(d.endpoint)
	if err != nil {
		t.Fatalf("opcua.NewClient: %v", err)
	}
	d.Lock()
	d.client = client
	d.Unlock()

	err = d.startSubscription(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no client or tags") {
		t.Errorf("error = %q, want substring 'no client or tags'", err.Error())
	}
}

// TestOPCUAStartSubscriptionUnconnectedClient verifies that startSubscription
// surfaces the error from client.Subscribe when the client is not connected to
// a real server.
func TestOPCUAStartSubscriptionUnconnectedClient(t *testing.T) {
	d := initConnectedOPCUA(t, validConfig("unsub")) // unconnected real client
	err := d.startSubscription(context.Background())
	if err == nil {
		t.Fatal("expected error from unconnected client, got nil")
	}
	if !strings.Contains(err.Error(), "create subscription") {
		t.Errorf("error = %q, want substring 'create subscription'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// HandleConnectionLost
// ---------------------------------------------------------------------------

// TestOPCUAHandleConnectionLost verifies that HandleConnectionLost transitions
// the driver to StateError and starts the reconnect loop, and that a second
// call is a no-op once the driver is already in an error state.
func TestOPCUAHandleConnectionLost(t *testing.T) {
	cfg := validConfig("hcl")
	cfg.Settings["endpoint"] = "opc.tcp://127.0.0.1:1" // dead port
	cfg.Settings["timeout"] = "200ms"
	cfg.Settings["reconnect-interval"] = "30ms"
	cfg.Settings["reconnect-max-interval"] = "60ms"
	d := mustInit(t, cfg)

	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})

	// Start failed against the dead port, so the driver is in StateConnecting
	// with a background reconnect loop running. Move to StateConnected so
	// HandleConnectionLost takes the active branch.
	d.SetState(core.StateConnected)

	// First call: transitions to StateError and starts a reconnect loop.
	d.HandleConnectionLost()
	if d.GetState() != core.StateError {
		t.Errorf("expected StateError, got %v", d.GetState())
	}

	// The reconnect loops are running against the dead port, so the counter
	// must advance.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.ReconnectCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if d.ReconnectCount() == 0 {
		t.Error("expected ReconnectCount > 0 after HandleConnectionLost")
	}

	// Second call: no-op because the state is already StateError.
	d.HandleConnectionLost()
	if d.GetState() != core.StateError {
		t.Errorf("expected StateError to persist, got %v", d.GetState())
	}
}
