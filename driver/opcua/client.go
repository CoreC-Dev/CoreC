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
	endpoint       string
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

// startReconnectLoop and reconnectLoop are provided by BaseDriver.

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
