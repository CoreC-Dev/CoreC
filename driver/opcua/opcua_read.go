package opcua

import (
	"context"
	"fmt"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua/ua"
)

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
