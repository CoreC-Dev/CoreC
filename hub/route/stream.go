package route

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func streamTags(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := c.CloseRead(r.Context())

	// Optional driver filter via query parameter
	filter := r.URL.Query().Get("driver")

	// Subscribe to real-time data points
	ch, unsub := getEngine().Subscribe(filter)
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			c.Close(websocket.StatusNormalClosure, "")
			return
		case point, ok := <-ch:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			if err := wsjson.Write(wctx, c, point); err != nil {
				cancel()
				return
			}
			cancel()
		}
	}
}
