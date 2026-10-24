package route

import (
	"context"
	"net/http"
	"time"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// getTraffic only depends on the StatsProvider role of the engine.
func getTraffic(w http.ResponseWriter, r *http.Request) {
	c, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := c.CloseRead(r.Context())
	interval := wsPushInterval
	if v := r.URL.Query().Get("interval"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	// mode=totals keeps the cumulative counters; the default
	// is rates (README promises "实时吞吐量统计": lifetime totals would
	// force clients to diff, and a restart-to-zero would produce a
	// negative first frame).
	modeTotals := r.URL.Query().Get("mode") == "totals"
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var prevRead, prevPublish, prevDropped uint64
	havePrev := false
	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
			var sp core.StatsProvider = getEngine()
			stats := sp.Stats()
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			if modeTotals {
				err = wsjson.Write(wctx, c, map[string]any{
					"read":    stats.TotalRead,
					"publish": stats.TotalPublish,
					"dropped": stats.TotalDropped,
				})
			} else {
				// First frame after (re)connect: report the engine's
				// current windowed rate (60s) instead of diffing
				// against a zero baseline — no bogus negative spike,
				// no "zero until the second tick" dead air.
				readRate := stats.PointsPerSec
				publishRate, droppedRate := 0.0, 0.0
				if havePrev {
					secs := interval.Seconds()
					readRate = float64(stats.TotalRead-prevRead) / secs
					publishRate = float64(stats.TotalPublish-prevPublish) / secs
					droppedRate = float64(stats.TotalDropped-prevDropped) / secs
				}
				prevRead, prevPublish, prevDropped = stats.TotalRead, stats.TotalPublish, stats.TotalDropped
				havePrev = true
				err = wsjson.Write(wctx, c, map[string]any{
					"read_rate":    readRate,
					"publish_rate": publishRate,
					"dropped_rate": droppedRate,
				})
			}
			cancel()
			if err != nil {
				return
			}
		}
	}
}
