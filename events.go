package main

import (
	"slices"
	"sync"
	"time"
)

// The live log (D-038) and webhook dedupe (F8). In memory only: lost on
// restart, fine for a demo. Panier Box would persist the seen event IDs, so a
// webhook retried after a restart is still recognised.

// Entry kinds and directions for the live log.
const (
	entryAction  = "action"  // a call Panier Box makes to Checkout.com
	entryWebhook = "webhook" // an event Checkout.com sends to Panier Box
	entryRead    = "read"    // a GET that changed a card's status

	dirOut = "→"
	dirIn  = "←"
)

// logEntry is one live log line. IDs, statuses and HTTP codes only: there is
// no field for headers, bodies or keys, so none can end up in the log.
type logEntry struct {
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Dir        string    `json:"dir"`
	Text       string    `json:"text"` // "POST /payments/{id}/captures", "payment_captured", "GET payment"
	HTTPStatus int       `json:"http_status,omitempty"`
	Status     string    `json:"status,omitempty"` // payment status, for reads
	EventID    string    `json:"event_id,omitempty"`
	PaymentID  string    `json:"payment_id,omitempty"`
	OrderID    string    `json:"order_id,omitempty"`
	ErrorCodes []string  `json:"error_codes,omitempty"` // when Checkout.com refuses an action
}

const logSize = 50

// eventLog keeps the newest 50 entries and every webhook event ID seen. One
// mutex covers both: the webhook handler and its goroutines write here.
type eventLog struct {
	mu      sync.Mutex
	entries []logEntry // oldest first
	seen    map[string]bool
}

func newEventLog() *eventLog {
	return &eventLog{seen: map[string]bool{}}
}

// SeenWebhook records an event ID and says whether it was already there.
// Check and record happen under one lock, so two copies of one event
// arriving together can't both be processed. Checkout.com retries a webhook
// that doesn't get a 2XX in time, so duplicates are normal, not an error.
func (l *eventLog) SeenWebhook(eventID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen[eventID] {
		return true
	}
	l.seen[eventID] = true
	return false
}

// Add appends an entry and drops the oldest beyond 50.
func (l *eventLog) Add(e logEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ErrorCodes = slices.Clone(e.ErrorCodes)
	l.entries = append(l.entries, e)
	if n := len(l.entries); n > logSize {
		l.entries = slices.Delete(l.entries, 0, n-logSize)
	}
}

// Recent is a copy of the entries, newest first, safe to change.
func (l *eventLog) Recent() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]logEntry, len(l.entries))
	for i, e := range l.entries {
		e.ErrorCodes = slices.Clone(e.ErrorCodes)
		out[len(l.entries)-1-i] = e
	}
	return out
}
