package route

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/CoreC-Dev/CoreC/core"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// getAlerts serves the engine's first-class alert state (audit findings
// OBS-4/HUB-6): the currently-active alerts (fired within the active
// window, acknowledged or not) plus the bounded lifecycle history
// (fired/acked/cleared events, newest first). Alerts previously
// degenerated to a WARN log line the Dashboard had to reconstruct by
// filtering log levels.
//
// Only depends on the optional core.AlertProvider role; engines that do
// not implement it answer 501 so clients can distinguish "alerts not
// supported" from "no alerts".
func getAlerts(w http.ResponseWriter, r *http.Request) {
	provider, ok := getEngine().(core.AlertProvider)
	if !ok || provider == nil {
		renderError(w, r, http.StatusNotImplemented, "alerts not supported by this engine")
		return
	}

	active := provider.ActiveAlerts()
	history := provider.AlertHistory()
	acked := 0
	for _, a := range active {
		if a.Acked {
			acked++
		}
	}
	render(w, r, http.StatusOK, map[string]any{
		"alerts":       active,
		"history":      history,
		"active_count": len(active),
		"acked_count":  acked,
	})
}

// ackAlertRequest is the body of POST /alerts/ack.
type ackAlertRequest struct {
	// Key is the alert identity "rule|driver|tag" as served by
	// GET /alerts. Acknowledgement sticks until the alert clears
	// (a full active-window without a re-fire); a re-fire does not
	// reopen it.
	Key string `json:"key"`
}

// ackAlert acknowledges an active alert (audit OBS-4/HUB-6). Only
// depends on the optional core.AlertProvider role. Key comes in the
// body rather than the URL path because keys contain "|".
func ackAlert(w http.ResponseWriter, r *http.Request) {
	var req ackAlertRequest
	if err := json.NewDecoder(limitedBody(r).Body).Decode(&req); err != nil {
		renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if req.Key == "" {
		renderError(w, r, http.StatusBadRequest, "key is required")
		return
	}

	provider, ok := getEngine().(core.AlertProvider)
	if !ok || provider == nil {
		renderError(w, r, http.StatusNotImplemented, "alerts not supported by this engine")
		return
	}
	if err := provider.AckAlert(req.Key); err != nil {
		// Unknown key = no active alert carries it: 404 so the client
		// can tell a stale key from a transport failure.
		renderError(w, r, http.StatusNotFound, err.Error())
		return
	}
	slog.Info("alert acknowledged",
		"method", r.Method,
		"path", r.URL.Path,
		"alert_key", req.Key,
		"remote", r.RemoteAddr)
	renderNoContent(w)
}

// streamAlerts serves the real-time alert lifecycle stream
// (WS /alerts/stream): one JSON AlertEvent per fired/acked/cleared
// transition. Mirrors the /logs stream's slow-subscriber semantics —
// a full subscriber buffer drops events (counted by the engine's
// observability) rather than blocking the processing pipeline.
func streamAlerts(w http.ResponseWriter, r *http.Request) {
	streamer, ok := getEngine().(core.AlertStreamer)
	if !ok || streamer == nil {
		renderError(w, r, http.StatusNotImplemented, "alert stream not supported by this engine")
		return
	}

	c, err := acceptWS(w, r)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusInternalError, "closed")

	ctx := c.CloseRead(r.Context())

	ch, unsub := streamer.SubscribeAlerts()
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
