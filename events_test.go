package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// Proves dedupe: the first time an event ID is new, every later time it's
// seen. A different ID is new.
func TestSeenWebhook(t *testing.T) {
	l := newEventLog()
	if l.SeenWebhook("evt_a") {
		t.Error("first evt_a: seen, want new")
	}
	if !l.SeenWebhook("evt_a") {
		t.Error("second evt_a: new, want seen")
	}
	if l.SeenWebhook("evt_b") {
		t.Error("evt_b: seen, want new")
	}
}

// Proves two copies of one event arriving together are processed once. 50
// goroutines start at the same moment with the same ID, and exactly one gets
// "new". Run with -race: a check and a record in two steps would fail here.
func TestSeenWebhookConcurrent(t *testing.T) {
	l := newEventLog()
	var fresh atomic.Int32
	var start, done sync.WaitGroup
	start.Add(1)
	for range 50 {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			if !l.SeenWebhook("evt_same") {
				fresh.Add(1)
			}
		}()
	}
	start.Done()
	done.Wait()
	if n := fresh.Load(); n != 1 {
		t.Errorf("%d goroutines processed the event, want 1", n)
	}
}

// Proves the log keeps the newest 50, newest first.
func TestEventLogKeepsNewest50(t *testing.T) {
	l := newEventLog()
	for i := 1; i <= 60; i++ {
		l.Add(logEntry{Kind: entryAction, Text: fmt.Sprintf("entry %d", i)})
	}
	got := l.Recent()
	if len(got) != 50 {
		t.Fatalf("%d entries, want 50", len(got))
	}
	if got[0].Text != "entry 60" || got[49].Text != "entry 11" {
		t.Errorf("first %q, last %q; want entry 60 and entry 11", got[0].Text, got[49].Text)
	}
}

// Proves Recent is a copy: changing it, or the slice passed to Add, leaves
// the log as it was.
func TestEventLogRecentIsACopy(t *testing.T) {
	l := newEventLog()
	codes := []string{"capture_not_allowed"}
	l.Add(logEntry{Kind: entryAction, Text: "POST /payments/pay_x/captures", ErrorCodes: codes})
	codes[0] = "changed by the caller"

	got := l.Recent()
	got[0].Text = "changed"
	got[0].ErrorCodes[0] = "changed too"

	again := l.Recent()
	if again[0].Text != "POST /payments/pay_x/captures" || again[0].ErrorCodes[0] != "capture_not_allowed" {
		t.Errorf("log changed through a copy: %+v", again[0])
	}
}

// Proves Add and Recent are safe together, as the webhook goroutines and the
// page will use them. Meaningful with -race.
func TestEventLogConcurrent(t *testing.T) {
	l := newEventLog()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); l.Add(logEntry{Kind: entryRead, Text: fmt.Sprint(i)}) }()
		go func() { defer wg.Done(); _ = l.Recent() }()
	}
	wg.Wait()
	if n := len(l.Recent()); n != 20 {
		t.Errorf("%d entries, want 20", n)
	}
}
