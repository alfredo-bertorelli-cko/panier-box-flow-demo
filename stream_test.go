package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// Proves "changed" is true for a new payment and a new status, false for the
// same status again. That's the whole rule for a read line.
func TestUpsertPaymentChanged(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, NewOrder("ORD-1042", mustLookup(t, basketSubscription, "FR"), "FR", fixedNow))
	p := samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")

	steps := []struct {
		status string
		want   bool
	}{
		{"Authorized", true},  // first sight
		{"Authorized", false}, // a poll, nothing new
		{"Captured", true},
		{"Captured", false},
	}
	for i, step := range steps {
		p.Status = step.status
		_, changed, err := s.UpsertPaymentChanged("ORD-1042", p)
		if err != nil {
			t.Fatal(err)
		}
		if changed != step.want {
			t.Errorf("step %d (%s): changed %v, want %v", i, step.status, changed, step.want)
		}
	}
}

// readLines is the log's read lines only, newest first.
func readLines(l *eventLog) []logEntry {
	var out []logEntry
	for _, e := range l.Recent() {
		if e.Kind == entryRead {
			out = append(out, e)
		}
	}
	return out
}

// Proves the read line rule end to end through GET /api/payments/{id}: the
// first read logs, the same status again doesn't, a new status does.
func TestSyncLogsStatusChangesOnly(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	const id = "pay_fakecitaaaaaaaaaaaaaaaaaaa"

	addRefPayment(t, fake, "ref_cit_authorized")
	getPayment(h, id)
	getPayment(h, id)
	if got := readLines(a.events); len(got) != 1 || got[0].Status != "Authorized" {
		t.Fatalf("after two reads of Authorized: %+v", got)
	}

	addRefPayment(t, fake, "ref_cit_captured") // same payment, now captured
	getPayment(h, id)
	got := readLines(a.events)
	if len(got) != 2 {
		t.Fatalf("after the capture: %+v", got)
	}
	if e := got[0]; e.Text != "GET /payments/"+id || e.HTTPStatus != 200 || e.Status != "Captured" || e.OrderID != "ORD-1042" || e.Dir != dirOut {
		t.Errorf("read line %+v", e)
	}
}

// Proves a browser that stops reading can't hold up the server: Add returns
// even with a full stream, and the stream just misses the extra lines.
func TestAddNeverBlocksOnSlowStream(t *testing.T) {
	l := newEventLog()
	_, entries, cancel := l.Subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		for range subscriberBuffer * 3 {
			l.Add(logEntry{Text: "line"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Add blocked on a stream nobody reads")
	}
	if n := len(entries); n != subscriberBuffer {
		t.Errorf("stream holds %d, want %d (the rest dropped)", n, subscriberBuffer)
	}
}

// Proves Subscribe gives the backlog oldest first, then only new lines on
// the channel, and that cancel stops delivery.
func TestSubscribeBacklogThenNew(t *testing.T) {
	l := newEventLog()
	l.Add(logEntry{Text: "one"})
	l.Add(logEntry{Text: "two"})
	backlog, entries, cancel := l.Subscribe()
	if len(backlog) != 2 || backlog[0].Text != "one" || backlog[1].Text != "two" {
		t.Fatalf("backlog %+v", backlog)
	}
	l.Add(logEntry{Text: "three"})
	if e := <-entries; e.Text != "three" {
		t.Errorf("got %q, want three", e.Text)
	}
	cancel()
	l.Add(logEntry{Text: "four"})
	if n := len(entries); n != 0 {
		t.Errorf("%d lines after cancel", n)
	}
}

// Proves a nil log is a no-op, so handler tests without one still run.
func TestNilEventLogAdd(t *testing.T) {
	var l *eventLog
	l.Add(logEntry{Text: "ignored"})
}

func TestVisibleTo(t *testing.T) {
	cases := []struct {
		name string
		e    logEntry
		want bool
	}{
		{"own order", logEntry{Kind: entryAction, OrderID: "ORD-1042"}, true},
		{"other order", logEntry{Kind: entryAction, OrderID: "ORD-1043"}, false},
		{"bad signature", logEntry{Kind: entryWebhook, HTTPStatus: 401}, true},
		{"someone else's webhook", logEntry{Kind: entryWebhook, HTTPStatus: 200}, false},
	}
	for _, c := range cases {
		if got := visibleTo(c.e, "ORD-1042"); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// openStream connects to the live log like EventSource would, on a real
// server: httptest.NewRecorder can't stream or notice a client leaving.
// Lines arrive on the channel. Cancel the context to leave.
func openStream(t *testing.T, srv *httptest.Server, order string) (context.CancelFunc, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events?order="+order, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := make(chan string, 64)
	go func() {
		defer resp.Body.Close()
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() != "" {
				lines <- sc.Text()
			}
		}
		// A read error ends the stream too. Pass it on as a line, so the
		// next nextEntry fails with it, unless the test left on purpose.
		if err := sc.Err(); err != nil && ctx.Err() == nil {
			lines <- "scan error: " + err.Error()
		}
	}()
	t.Cleanup(cancel)
	return cancel, lines
}

// nextEntry reads the next "data:" line as a log entry.
func nextEntry(t *testing.T, lines <-chan string) logEntry {
	t.Helper()
	select {
	case line, ok := <-lines:
		if !ok {
			t.Fatal("stream closed")
		}
		data, found := strings.CutPrefix(line, "data: ")
		if !found {
			t.Fatalf("not a data line: %q", line)
		}
		var e logEntry
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			t.Fatal(err)
		}
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no line within 2s")
	}
	return logEntry{}
}

func streamServer(t *testing.T) (*app, *httptest.Server) {
	t.Helper()
	a, h, _, _ := webhookApp(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return a, srv
}

// Proves the stream (PRD 5.4): this order's backlog on connect, then a new
// line as it's added, with other orders' lines filtered out and a bad
// signature shown to everyone.
func TestEventStream(t *testing.T) {
	a, srv := streamServer(t)
	a.events.Add(logEntry{Kind: entryAction, Text: "POST /payment-sessions", OrderID: "ORD-1042"})
	a.events.Add(logEntry{Kind: entryAction, Text: "POST /payment-sessions", OrderID: "ORD-1043"})

	_, lines := openStream(t, srv, "ORD-1042")
	if e := nextEntry(t, lines); e.OrderID != "ORD-1042" || e.Text != "POST /payment-sessions" {
		t.Errorf("backlog %+v", e)
	}

	a.events.Add(logEntry{Kind: entryAction, Text: "POST /payments/x/captures", OrderID: "ORD-1043"}) // not ours
	a.events.Add(logEntry{Kind: entryWebhook, Text: "bad signature, ignored", HTTPStatus: 401})
	a.events.Add(logEntry{Kind: entryAction, Text: "POST /payments/x/captures", OrderID: "ORD-1042"})

	if e := nextEntry(t, lines); e.Text != "bad signature, ignored" {
		t.Errorf("want the bad signature next, got %+v", e)
	}
	if e := nextEntry(t, lines); e.OrderID != "ORD-1042" || e.Text != "POST /payments/x/captures" {
		t.Errorf("want ORD-1042's capture, got %+v", e)
	}
}

// Proves the handler ends when the browser leaves: its subscription goes, so
// a closed tab doesn't keep collecting lines.
func TestEventStreamEndsOnDisconnect(t *testing.T) {
	a, srv := streamServer(t)
	leave, _ := openStream(t, srv, "ORD-1042")
	waitFor(t, func() bool { return subscriberCount(a.events) == 1 })
	leave()
	waitFor(t, func() bool { return subscriberCount(a.events) == 0 })
}

// Proves the heartbeat: an idle stream still sends a comment line, which
// EventSource ignores but which keeps a tunnel from closing it.
func TestEventStreamHeartbeat(t *testing.T) {
	prev := streamHeartbeat
	streamHeartbeat = 20 * time.Millisecond
	t.Cleanup(func() { streamHeartbeat = prev })

	_, srv := streamServer(t)
	_, lines := openStream(t, srv, "ORD-1042")
	select {
	case line := <-lines:
		if line != ": ping" {
			t.Errorf("got %q, want the heartbeat", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat")
	}
}

func TestEventStreamRejectsBadOrder(t *testing.T) {
	_, h, _, _ := webhookApp(t)
	for _, q := range []string{"", "?order=", "?order=nope", "?order=ORD-1042%0A"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/events"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, rec.Code)
		}
	}
}

func subscriberCount(l *eventLog) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.subscribers)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Proves GET /api/orders/{id} serves the stored order and never calls
// Checkout.com: after a webhook the page refreshes from what's stored.
func TestGetOrderIsLocal(t *testing.T) {
	store, _ := newTestStore(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	if _, err := store.UpsertPayment("ORD-1042", samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	fake := &fakePayments{details: map[string]*paymentsnas.GetPaymentResponse{}}
	a := &app{cfg: sdkTestConfig(t), store: store, api: fake, now: func() time.Time { return fixedNow }}
	h := newRouter(a, fstest.MapFS{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/orders/ORD-1042", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status %d, cache %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var body struct {
		Order orderView `json:"order"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Order.ID != "ORD-1042" || len(body.Order.Payments) != 1 || !body.Order.Payments[0].CanCapture {
		t.Errorf("order %+v", body.Order)
	}
	if n := fake.getPaymentCalls(); n != 0 {
		t.Errorf("%d GETs to Checkout.com, want 0", n)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/orders/ORD-9999", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown order: status %d, want 404", rec.Code)
	}
}
