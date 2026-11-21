package opcua

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/CoreC-Dev/CoreC/common/util"
	"github.com/CoreC-Dev/CoreC/core"
	"github.com/gopcua/opcua/ua"
)

// Read splits the nodes into requests of at most maxBatchSize nodes,
// sent sequentially with the same ctx. A chunk failure fails the whole
// read and results of earlier chunks are discarded; the read counters
// advance only after every chunk succeeds.
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

	d.RLock()
	batchSize := d.maxBatchSize
	d.RUnlock()

	chunks := chunkReadValues(readValues, batchSize)
	results := make([]core.TagValue, 0, len(readValues))
	offset := 0
	for _, chunk := range chunks {
		req := &ua.ReadRequest{
			NodesToRead:        chunk,
			TimestampsToReturn: ua.TimestampsToReturnBoth,
		}

		resp, err := client.Read(ctx, req)
		if err != nil {
			d.RecordError()
			d.Lock()
			d.SetLastErrorLocked(err.Error())
			d.Unlock()

			// Check if connection is lost and trigger reconnect. The guard
			// ignores errors from a client that has already been replaced by
			// a reconnect.
			if util.IsConnectionError(err) {
				d.handleConnLostIfCurrent(client)
			}
			return nil, fmt.Errorf("opcua batch read failed: %w", err)
		}

		results = append(results, d.assembleReadResults(resp, validTagNames[offset:offset+len(chunk)], now)...)
		offset += len(chunk)
	}

	d.AddReadCount(uint64(len(results)))
	d.Lock()
	d.SetLastRead(now)
	d.Unlock()

	return results, nil
}

// chunkReadValues splits readValues into chunks of at most size nodes,
// preserving order. A non-positive size yields a single chunk; empty
// input yields no chunks.
func chunkReadValues(readValues []*ua.ReadValueID, size int) [][]*ua.ReadValueID {
	if size <= 0 || len(readValues) <= size {
		if len(readValues) == 0 {
			return nil
		}
		return [][]*ua.ReadValueID{readValues}
	}
	return slices.Collect(slices.Chunk(readValues, size))
}

// assembleReadResults maps a ReadResponse to TagValues: timestamps are the
// local receive time `now` (good and bad points alike), and non-OK statuses
// map to QualityBad and drop the value.
func (d *OPCUADriver) assembleReadResults(resp *ua.ReadResponse, validTagNames []string, now time.Time) []core.TagValue {
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
			Timestamp: now,
		})
	}
	return results
}
