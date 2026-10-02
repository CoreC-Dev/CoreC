// Package opcua implements the OPC UA client driver for CoreC.
package opcua

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/driverbase"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// TypeName is the protocol type identifier for the OPC UA driver.
const TypeName = "opcua"

// OPCUADriver implements core.Driver for OPC UA protocol.
type OPCUADriver struct {
	driverbase.BaseDriver

	// OPC UA settings
	endpoint      string
	securityPolicy string
	securityMode   string
	username       string
	password       string
	certFile       string
	keyFile        string
	timeout        time.Duration
	subBufferSize  int
	maxBatchSize   int

	// OPC UA client
	client *opcua.Client

	// Tag mappings: name -> TagConfig, name -> *ua.NodeID
	tags    map[string]core.TagConfig
	nodeIDs map[string]*ua.NodeID

	// Subscription state
	subMode      bool
	subInterval  time.Duration
	subChannel   chan core.DataPoint
	subscription *opcua.Subscription
	handleToTag  map[uint32]string
	subNotifyCh  chan *opcua.PublishNotificationData
	subWg        sync.WaitGroup // tracks the subscription notification goroutine
}

// NewOPCUADriver creates a new OPC UA driver instance.
func NewOPCUADriver(config core.DriverConfig) (core.Driver, error) {
	bufSize := util.GetIntSetting(config.Settings, "subscription-buffer", 1024)
	if bufSize <= 0 {
		bufSize = 1024
	}
	d := &OPCUADriver{
		tags:          make(map[string]core.TagConfig),
		nodeIDs:       make(map[string]*ua.NodeID),
		subChannel:    make(chan core.DataPoint, bufSize),
		subBufferSize: bufSize,
	}
	d.SetMeta(config.Name, TypeName, config)
	return d, nil
}

func (d *OPCUADriver) Init(ctx context.Context, config core.DriverConfig) error {
	d.Lock()
	defer d.Unlock()

	d.SetMeta(config.Name, TypeName, config)
	settings := config.Settings

	// Parse endpoint
	if ep, ok := settings["endpoint"].(string); ok && ep != "" {
		d.endpoint = ep
	} else {
		return fmt.Errorf("opcua: endpoint is required (e.g. opc.tcp://192.168.1.10:4840)")
	}

	// Security settings
	if sp, ok := settings["security-policy"].(string); ok {
		d.securityPolicy = sp
	}
	if sm, ok := settings["security-mode"].(string); ok {
		d.securityMode = sm
	}
	if u, ok := settings["username"].(string); ok {
		d.username = u
	}
	if p, ok := settings["password"].(string); ok {
		d.password = p
	}
	if cert, ok := settings["cert-file"].(string); ok {
		d.certFile = cert
	}
	if key, ok := settings["key-file"].(string); ok {
		d.keyFile = key
	}

	// Mode
	if mode, ok := settings["mode"].(string); ok && mode == "subscription" {
		d.subMode = true
	}
	d.subInterval = util.GetDurationSetting(settings, "subscription-interval", 500*time.Millisecond)

	// Timeout
	d.timeout = util.GetDurationSetting(settings, "timeout", core.DefaultDriverTimeout)

	// Reconnect and batch settings
	d.ParseReconnectSettings(settings)
	d.maxBatchSize = util.GetIntSetting(settings, "max-batch-size", 1000)

	// Parse tags and node IDs
	for _, tag := range config.Tags {
		d.tags[tag.Name] = tag
		nodeID, err := ua.ParseNodeID(tag.Address)
		if err != nil {
			return fmt.Errorf("opcua tag %s: invalid NodeID %q: %w", tag.Name, tag.Address, err)
		}
		d.nodeIDs[tag.Name] = nodeID
	}

	// Lifecycle hooks for BaseDriver.
	d.SetConnectFunc(func() error { return d.connect(d.Context()) })
	d.SetInitFunc(d.Init)
	d.SetCloseConnFunc(func() {
		if d.client != nil {
			d.client.Close(context.Background())
			d.client = nil
		}
	})
	d.SetExtraShutdown(func() {
		d.stopSubscription()
		d.subWg.Wait()
	})
	d.SetOnConnLost(func() {
		d.stopSubscription()
	})
	d.SetTagCountFunc(func() int { return len(d.tags) })

	slog.Info("opcua driver initialized",
		"name", d.Name(),
		"endpoint", d.endpoint,
		"tags", len(d.tags),
		"subscription_mode", d.subMode,
	)

	return nil
}

// Start, Stop, Restart, Status, Name, Type, reconnectLoop, startReconnectLoop,
// and HandleConnectionLost are provided by the embedded driverbase.BaseDriver.

func (d *OPCUADriver) connect(ctx context.Context) error {
	opts := []opcua.Option{
		opcua.RequestTimeout(d.timeout),
	}

	if d.securityPolicy != "" {
		opts = append(opts, opcua.SecurityPolicy(d.securityPolicy))
	}
	if d.securityMode != "" {
		opts = append(opts, opcua.SecurityModeString(d.securityMode))
	}
	if d.username != "" {
		opts = append(opts, opcua.AuthUsername(d.username, d.password))
	} else {
		opts = append(opts, opcua.AuthAnonymous())
	}
	if d.certFile != "" && d.keyFile != "" {
		opts = append(opts, opcua.CertificateFile(d.certFile), opcua.PrivateKeyFile(d.keyFile))
	}

	client, err := opcua.NewClient(d.endpoint, opts...)
	if err != nil {
		return fmt.Errorf("failed to create opcua client: %w", err)
	}

	connCtx, connCancel := context.WithTimeout(ctx, d.timeout)
	defer connCancel()

	if err := client.Connect(connCtx); err != nil {
		return fmt.Errorf("failed to connect to opcua endpoint: %w", err)
	}

	d.Lock()
	d.client = client
	d.SetStateLocked(core.StateConnected)
	d.SetLastErrorLocked("")
	d.Unlock()

	slog.Info("opcua connected successfully", "name", d.Name(), "endpoint", d.endpoint)

	if d.subMode {
		if err := d.startSubscription(ctx); err != nil {
			slog.Warn("opcua subscription setup failed, falling back to polling reads",
				"name", d.Name(), "error", err)
		}
	}

	return nil
}

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
		_ = sub.Cancel(context.Background())
	}
	// Closing notifyCh is handled by the library on Cancel; the goroutine
	// exits via channel close or context cancellation. We don't wait here
	// because the goroutine is tracked by subWg and waited on in Stop().
	_ = notifyCh
}

// startReconnectLoop and reconnectLoop are provided by BaseDriver.

func (d *OPCUADriver) Read(ctx context.Context, tags []string) ([]core.TagValue, error) {
	d.RLock()
	client := d.client
	state := d.GetStateLocked()
	d.RUnlock()

	if state != core.StateConnected || client == nil {
		d.RecordError()
		return nil, fmt.Errorf("driver %s is not connected", d.Name())
	}

	readValues := make([]*ua.ReadValueID, 0, len(tags))
	validTagNames := make([]string, 0, len(tags))
	now := time.Now()

	for _, tagName := range tags {
		d.RLock()
		nodeID, ok := d.nodeIDs[tagName]
		d.RUnlock()

		if !ok {
			continue
		}
		readValues = append(readValues, &ua.ReadValueID{
			NodeID:       nodeID,
			AttributeID:  ua.AttributeIDValue,
			DataEncoding: &ua.QualifiedName{},
		})
		validTagNames = append(validTagNames, tagName)
	}

	if len(readValues) == 0 {
		return nil, fmt.Errorf("no valid tags found in request")
	}

	req := &ua.ReadRequest{
		NodesToRead:        readValues,
		TimestampsToReturn: ua.TimestampsToReturnBoth,
	}

	resp, err := client.Read(ctx, req)
	if err != nil {
		d.RecordError()
		d.Lock()
		d.SetLastErrorLocked(err.Error())
		d.Unlock()

		// Check if connection is lost and trigger reconnect
		if util.IsConnectionError(err) {
			d.HandleConnectionLost()
		}
		return nil, fmt.Errorf("opcua batch read failed: %w", err)
	}

	results := make([]core.TagValue, 0, len(resp.Results))
	for i, r := range resp.Results {
		tagName := validTagNames[i]
		d.RLock()
		tagCfg := d.tags[tagName]
		d.RUnlock()

		dt, _ := core.ParseDataType(tagCfg.Type)

		if r.Status != ua.StatusOK {
			results = append(results, core.TagValue{
				Tag:       tagName,
				Quality:   core.QualityBad,
				Timestamp: now,
				Error:     fmt.Errorf("opcua status: %v", r.Status),
			})
			continue
		}

		val := r.Value.Value()
		val = util.ApplyTransform(val, tagCfg.Scale, tagCfg.Offset)

		results = append(results, core.TagValue{
			Tag:       tagName,
			Value:     val,
			Type:      dt,
			Quality:   core.QualityGood,
			Timestamp: r.SourceTimestamp,
		})
	}

	d.AddReadCount(uint64(len(results)))
	d.Lock()
	d.SetLastRead(now)
	d.Unlock()

	return results, nil
}

func (d *OPCUADriver) Write(ctx context.Context, commands []core.WriteCommand) ([]core.WriteResult, error) {
	d.RLock()
	client := d.client
	state := d.GetStateLocked()
	d.RUnlock()

	if state != core.StateConnected || client == nil {
		return nil, fmt.Errorf("driver %s is not connected", d.Name())
	}

	results := make([]core.WriteResult, len(commands))
	writeValues := make([]*ua.WriteValue, 0, len(commands))
	// validIndices tracks the original command index for each entry in
	// writeValues. Skipped commands (tag not found, variant error) are
	// excluded from writeValues, so resp.Results indexes writeValues, not
	// commands. Without this mapping, results would be written to the wrong
	// positions whenever any command is skipped.
	validIndices := make([]int, 0, len(commands))

	for i, cmd := range commands {
		d.RLock()
		nodeID, ok := d.nodeIDs[cmd.Tag]
		d.RUnlock()

		if !ok {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("tag not found: %s", cmd.Tag),
			}
			continue
		}

		variant, err := ua.NewVariant(cmd.Value)
		if err != nil {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("failed to create variant: %v", err),
			}
			continue
		}

		writeValues = append(writeValues, &ua.WriteValue{
			NodeID:      nodeID,
			AttributeID: ua.AttributeIDValue,
			Value: &ua.DataValue{
				EncodingMask: ua.DataValueValue,
				Value:        variant,
			},
		})
		validIndices = append(validIndices, i)
	}

	if len(writeValues) == 0 {
		return results, nil
	}

	req := &ua.WriteRequest{
		NodesToWrite: writeValues,
	}

	resp, err := client.Write(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("opcua batch write failed: %w", err)
	}

	for i, code := range resp.Results {
		origIdx := validIndices[i]
		if code == ua.StatusOK {
			results[origIdx] = core.WriteResult{Success: true}
		} else {
			results[origIdx] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("opcua write status: %v", code),
			}
		}
	}

	return results, nil
}

func (d *OPCUADriver) Subscribe(ctx context.Context, tags []string) (<-chan core.DataPoint, error) {
	if !d.subMode {
		return nil, core.ErrSubscribeNotSupported
	}
	return d.subChannel, nil
}

// Name, Type, and Status are provided by the embedded driverbase.BaseDriver.

func (d *OPCUADriver) Capabilities() core.DriverCapabilities {
	return core.DriverCapabilities{
		CanRead:      true,
		CanWrite:     true,
		CanSubscribe: true,
		BatchRead:    true,
		MaxBatchSize: d.maxBatchSize,
	}
}

// HandleConnectionLost is provided by the embedded driverbase.BaseDriver.

// ============================================================
// Helper functions
// ============================================================
