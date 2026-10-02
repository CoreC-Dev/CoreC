package opcua

import (
	"context"
	"fmt"

	"github.com/CoreC-Dev/CoreC/core"
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
