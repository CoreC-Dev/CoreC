package opcua

import (
	"context"
	"fmt"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/CoreC-Dev/CoreC/driverbase"
	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

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
		cfg := d.tags[cmd.Tag]
		d.RUnlock()

		if !ok {
			results[i] = core.WriteResult{
				Success: false,
				Error:   fmt.Sprintf("tag not found: %s", cmd.Tag),
			}
			continue
		}

		// Convert the engineering value back to the raw device
		// representation unless the command explicitly requests a raw
		// write (reads apply raw×scale+offset, so
		// an un-inverted write lands off by the scale factor).
		cmd.Value = driverbase.WriteValueForTag(cmd, cfg)

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
		d.handleWriteError(err, client)
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

// handleWriteError records a failed batch write and, when the failure is
// connection-class, tears the connection down so the reconnect loop
// restores it — mirroring the Read path.
// The handleConnLostIfCurrent guard keeps a stale client from tearing
// down a connection that has already been replaced.
func (d *OPCUADriver) handleWriteError(err error, client *opcua.Client) {
	d.RecordError()
	d.Lock()
	d.SetLastErrorLocked(err.Error())
	d.Unlock()
	if util.IsConnectionError(err) {
		d.handleConnLostIfCurrent(client)
	}
}
