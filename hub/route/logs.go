package route

import (
	"context"
	"net/http"

	"github.com/CoreC-Dev/CoreC/log"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func getLogs(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := c.CloseRead(r.Context())

	// Subscribe to the log bus
	ch, unsub := log.Subscribe()
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case ev, ok := <-ch:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			if err := wsjson.Write(wctx, c, ev); err != nil {
				cancel()
				return
			}
			cancel()
		}
	}
}
