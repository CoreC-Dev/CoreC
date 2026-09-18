package route

import (
	"context"
	"net/http"
	"runtime"
	"runtime/metrics"
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
			// runtime/metrics.Read collects runtime statistics without
			// triggering a Stop-The-World pause. The previous
			// runtime.ReadMemStats call forced a STW on every push (default
			// once per second) per connected client, producing latency
			// spikes proportional to the client count. Each metric below is
			// the exact runtime.ReadMemStats analog: heap objects bytes ==
			// HeapAlloc, total allocs == TotalAlloc, total memory class ==
			// Sys, completed GC cycles == NumGC.
			samples := []metrics.Sample{
				{Name: "/memory/classes/heap/objects:bytes"},
				{Name: "/gc/heap/allocs:bytes"},
				{Name: "/memory/classes/total:bytes"},
				{Name: "/gc/cycles/total:gc-cycles"},
			}
			metrics.Read(samples)
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			if err := wsjson.Write(wctx, c, map[string]any{
				"alloc":       samples[0].Value.Uint64(),
				"total_alloc": samples[1].Value.Uint64(),
				"sys":         samples[2].Value.Uint64(),
				"num_gc":      samples[3].Value.Uint64(),
				"goroutines":  runtime.NumGoroutine(),
			}); err != nil {
				cancel()
				return
			}
			cancel()
		}
	}
}
