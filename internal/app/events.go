package app

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
	Text       string    `json:"text"` // "POST /payments/pay_.../captures", "payment_captured", "GET /payments/pay_..."
	HTTPStatus int       `json:"http_status,omitempty"`
	Detail     string    `json:"detail,omitempty"` // "ps_...", "MIT ORD-1042-W2", "Void", "no response"
	Status     string    `json:"status,omitempty"` // payment status, for reads and the MIT
	EventID    string    `json:"event_id,omitempty"`
	PaymentID  string    `json:"payment_id,omitempty"`
	OrderID    string    `json:"order_id,omitempty"`
	ErrorCodes []string  `json:"error_codes,omitempty"` // when Checkout.com refuses an action
	Simulated  bool      `json:"simulated,omitempty"`   // a webhook from our simulator (D-042)
}

const logSize = 50

// eventLog keeps the newest 50 entries, every webhook event ID seen, and
// the open live log streams. One mutex covers all three: handlers, the
// webhook goroutines and the streams all use it.
type eventLog struct {
	mu          sync.Mutex
	entries     []logEntry // oldest first
	seen        map[string]bool
	subscribers map[chan logEntry]struct{}
}

func newEventLog() *eventLog {
	return &eventLog{seen: map[string]bool{}, subscribers: map[chan logEntry]struct{}{}}
}

// subscriberBuffer is how far a stream may fall behind before it misses
// lines. Plenty for a demo: a click makes three or four.
const subscriberBuffer = 32

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

// Add appends an entry, drops the oldest beyond 50, and sends it to every
// open stream. A nil log does nothing, so tests that don't look at the log
// needn't build one.
//
// The send never waits: a browser that stops reading would otherwise hold
// the lock, and with it every handler that logs. A full stream misses the
// line instead. The page can reload to get the backlog again.
func (l *eventLog) Add(e logEntry) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e.ErrorCodes = slices.Clone(e.ErrorCodes)
	l.entries = append(l.entries, e)
	if n := len(l.entries); n > logSize {
		l.entries = slices.Delete(l.entries, 0, n-logSize)
	}
	for ch := range l.subscribers {
		out := e
		out.ErrorCodes = slices.Clone(e.ErrorCodes)
		select {
		case ch <- out:
		default:
		}
	}
}

// Subscribe opens a stream: the entries so far, oldest first, then every new
// one on the channel. Both happen under one lock, so no line falls between
// the backlog and the stream. Call cancel when the browser leaves.
func (l *eventLog) Subscribe() (backlog []logEntry, entries <-chan logEntry, cancel func()) {
	ch := make(chan logEntry, subscriberBuffer)
	l.mu.Lock()
	defer l.mu.Unlock()
	backlog = make([]logEntry, len(l.entries))
	for i, e := range l.entries {
		e.ErrorCodes = slices.Clone(e.ErrorCodes)
		backlog[i] = e
	}
	l.subscribers[ch] = struct{}{}
	return backlog, ch, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.subscribers, ch)
	}
}

// visibleTo says whether an order's page shows a line: its own lines, plus
// bad signatures. A forged webhook has no order (its body is never read),
// and showing it everywhere is the point: it proves the check works.
func visibleTo(e logEntry, orderID string) bool {
	if e.OrderID == orderID {
		return true
	}
	return e.OrderID == "" && e.Kind == entryWebhook && e.HTTPStatus == 401
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
