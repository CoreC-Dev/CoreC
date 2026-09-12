package route

import (
	"context"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func getTraffic(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := c.CloseRead(context.Background())
	interval := wsPushInterval
	if v := r.URL.Query().Get("interval"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
			stats := getEngine().Stats()
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			err = wsjson.Write(wctx, c, map[string]any{
				"read":    stats.TotalRead,
				"publish": stats.TotalPublish,
				"dropped": stats.TotalDropped,
			})
			cancel()
			if err != nil {
				return
			}
		}
	}
}
