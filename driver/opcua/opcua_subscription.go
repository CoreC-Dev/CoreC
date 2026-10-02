package opcua

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// startSubscription creates a real OPC UA subscription and monitored items for
// all configured tags, then launches a goroutine that forwards value-change
// notifications into d.subChannel as core.DataPoint values. This is the actual
// implementation behind Subscribe(); it is called automatically after a
// successful connect when the driver is in subscription mode.
func (d *OPCUADriver) startSubscription(ctx context.Context) error {
	d.RLock()
	client := d.client
	nodeIDs := d.nodeIDs
	tags := d.tags
	d.RUnlock()

	if client == nil || len(nodeIDs) == 0 {
		return fmt.Errorf("no client or tags to subscribe")
	}

	// Clean up any previous subscription (e.g. after reconnect).
	d.stopSubscription()

	notifyCh := make(chan *opcua.PublishNotificationData, 256)

	params := &opcua.SubscriptionParameters{
		Interval: d.subInterval,
	}
	sub, err := client.Subscribe(ctx, params, notifyCh)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}

	// Build monitored-item requests with stable client handles and a
	// handle→tag-name map so we can resolve notifications back to tags.
	handleToTag := make(map[uint32]string, len(nodeIDs))
	items := make([]*ua.MonitoredItemCreateRequest, 0, len(nodeIDs))
	var handle uint32
	for tagName, nodeID := range nodeIDs {
		req := opcua.NewMonitoredItemCreateRequestWithDefaults(nodeID, ua.AttributeIDValue, handle)
		items = append(items, req)
		handleToTag[handle] = tagName
		handle++
	}

	resp, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, items...)
	if err != nil {
		_ = sub.Cancel(ctx)
		return fmt.Errorf("monitor items: %w", err)
	}
	for _, r := range resp.Results {
		if r.StatusCode != ua.StatusOK {
			_ = sub.Cancel(ctx)
			return fmt.Errorf("monitor item status: %v", r.StatusCode)
		}
	}

	d.Lock()
	d.subscription = sub
	d.handleToTag = handleToTag
	d.subNotifyCh = notifyCh
	d.Unlock()

	// Notification forwarding goroutine.
	d.subWg.Add(1)
	go d.subscriptionLoop(ctx, notifyCh, handleToTag, tags)

	slog.Info("opcua subscription active",
		"name", d.Name(), "subscription_id", sub.SubscriptionID, "items", len(items))
	return nil
}

// subscriptionLoop reads OPC UA publish notifications and forwards value
// changes to the driver's subChannel as core.DataPoint values. It exits when
// the driver context is cancelled or the notification channel is closed.
func (d *OPCUADriver) subscriptionLoop(ctx context.Context,
	notifyCh <-chan *opcua.PublishNotificationData,
	handleToTag map[uint32]string,
	tags map[string]core.TagConfig) {
	defer d.subWg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-notifyCh:
			if !ok {
				return
			}
			if msg.Error != nil {
				d.RecordError()
				slog.Warn("opcua subscription error", "name", d.Name(), "error", msg.Error)
				continue
			}
			notif, ok := msg.Value.(*ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range notif.MonitoredItems {
				tagName, found := handleToTag[item.ClientHandle]
				if !found {
					continue
				}
				if item.Value == nil || item.Value.Value == nil {
					continue
				}
				val := item.Value.Value.Value()
				tagCfg := tags[tagName]
				val = util.ApplyTransform(val, tagCfg.Scale, tagCfg.Offset)
				dt, _ := core.ParseDataType(tagCfg.Type)

				dp := core.DataPoint{
					Driver:    d.Name(),
					Tag:       tagName,
					Value:     val,
					Type:      dt,
					Quality:   core.QualityGood,
					Timestamp: item.Value.SourceTimestamp,
				}
				select {
				case d.subChannel <- dp:
				default:
					// subChannel full; drop to avoid blocking the notification loop.
					d.RecordError()
					slog.Warn("opcua subscription channel full, dropping data point",
						"name", d.Name(),
						"tag", dp.Tag,
						"driver", dp.Driver,
					)
				}
			}
		}
	}
}

// stopSubscription tears down an active OPC UA subscription and waits for the
// notification goroutine to exit. Safe to call when no subscription is active.
func (d *OPCUADriver) stopSubscription() {
	d.Lock()
	sub := d.subscription
	notifyCh := d.subNotifyCh
	d.subscription = nil
	d.subNotifyCh = nil
	d.handleToTag = nil
	d.Unlock()

	if sub != nil {
		cancelCtx, cancelFn := context.WithTimeout(context.Background(), d.timeout)
		defer cancelFn()
		_ = sub.Cancel(cancelCtx)
	}
	// Closing notifyCh is handled by the library on Cancel; the goroutine
	// exits via channel close or context cancellation. We don't wait here
	// because the goroutine is tracked by subWg and waited on in Stop().
	_ = notifyCh
}
