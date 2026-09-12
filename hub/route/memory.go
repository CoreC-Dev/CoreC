package route

import (
	"context"
	"net/http"
	"runtime"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func getMemory(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
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
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case <-ticker.C:
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			if err := wsjson.Write(wctx, c, map[string]any{
				"alloc":       mem.Alloc,
				"total_alloc": mem.TotalAlloc,
				"sys":         mem.Sys,
				"num_gc":      mem.NumGC,
				"goroutines":  runtime.NumGoroutine(),
			}); err != nil {
				cancel()
				return
			}
			cancel()
		}
	}
}
