package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// streamHeartbeat keeps an idle live log open. Proxies and tunnels tend to
// drop a connection that sends nothing for a minute or so. A variable so a
// test can shorten it.
var streamHeartbeat = 20 * time.Second

// handleEvents is the live log stream (D-038): GET /api/events?order=ORD-1042,
// as server-sent events. One "data:" line of JSON per log entry: first the
// order's lines from the last 50, oldest first, then each new one as it
// happens. The browser's EventSource reconnects by itself if it drops, and
// the backlog then fills the gap.
//
// Lines carry IDs, statuses and HTTP codes only (logEntry has no field for
// anything else), so the stream can't leak a header or a key.
func (a *app) handleEvents(w http.ResponseWriter, r *http.Request) {
	orderID := r.URL.Query().Get("order")
	if _, ok := parseOrderNumber(orderID); !ok {
		writeError(w, http.StatusBadRequest, "Invalid order.")
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	backlog, entries, cancel := a.events.Subscribe()
	defer cancel()

	send := func(e logEntry) error {
		if !visibleTo(e, orderID) {
			return nil
		}
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return err
		}
		return rc.Flush()
	}
	for _, e := range backlog {
		if err := send(e); err != nil {
			return
		}
	}
	// Flush now, so the browser sees the stream open even with no backlog.
	if err := rc.Flush(); err != nil {
		log.Printf("live log stream: %v", err)
		return
	}

	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return // the browser left
		case e := <-entries:
			if err := send(e); err != nil {
				return
			}
		case <-heartbeat.C:
			// A comment line: EventSource ignores it, the connection stays up.
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// handleGetOrder returns one stored order, with no call to Checkout.com. The
// page re-reads it after a webhook: the webhook's own GET has already stored
// the new status, so asking Checkout.com again would only add calls.
func (a *app) handleGetOrder(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Order(r.PathValue("orderId"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown order.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		Order orderView `json:"order"`
	}{viewFor(o)})
}
