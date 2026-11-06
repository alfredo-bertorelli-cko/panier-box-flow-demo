package app

import (
	"sync"
	"testing"
)

// Fake payment IDs, shaped like Checkout.com's but obviously not real.
const (
	fakePayA = "pay_fakeaaaaaaaaaaaaaaaaaaaaaa"
	fakePayB = "pay_fakebbbbbbbbbbbbbbbbbbbbbb"
)

// Proves the renewal reference format, and that week 1 (the CIT) and below are refused.
func TestRenewalReference(t *testing.T) {
	tests := []struct {
		orderID string
		week    int
		want    string
		wantErr bool
	}{
		{"ORD-1042", 2, "ORD-1042-W2", false},
		{"ORD-1042", 3, "ORD-1042-W3", false},
		{"ORD-1042", 10, "ORD-1042-W10", false},
		{"ORD-1042", 1, "", true},
		{"ORD-1042", 0, "", true},
		{"ORD-1042", -1, "", true},
		{"", 2, "", true},
	}
	for _, tt := range tests {
		got, err := RenewalReference(tt.orderID, tt.week)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("RenewalReference(%q, %d) = %q, %v; want %q, err=%v",
				tt.orderID, tt.week, got, err, tt.want, tt.wantErr)
		}
	}
}

// Proves a payment reference maps back to its order, and that only
// references we build (ORD-n or ORD-n-Wk with k >= 2) are accepted.
func TestOrderIDFromReference(t *testing.T) {
	tests := []struct {
		ref    string
		want   string
		wantOK bool
	}{
		{"ORD-1042", "ORD-1042", true},
		{"ORD-1042-W2", "ORD-1042", true},
		{"ORD-1042-W13", "ORD-1042", true},
		{"ORD-1042-W1", "", false}, // week 1 is the CIT, never a renewal ref
		{"ORD-1042-W02", "", false},
		{"ORD-1042-W", "", false},
		{"ORD-1042-Wx", "", false},
		{"ORD-x-W2", "", false},
		{"other-merchant-test", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := OrderIDFromReference(tt.ref)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("OrderIDFromReference(%q) = %q, %v; want %q, %v", tt.ref, got, ok, tt.want, tt.wantOK)
		}
	}
}

// Proves each idempotency key has the exact format agreed in CLAUDE.md.
func TestIdempotencyKeyFormats(t *testing.T) {
	capture, err := CaptureKey(fakePayA)
	if err != nil || capture != "capture-"+fakePayA {
		t.Errorf("CaptureKey = %q, %v", capture, err)
	}
	reverse, err := ReverseKey(fakePayA)
	if err != nil || reverse != "reverse-"+fakePayA {
		t.Errorf("ReverseKey = %q, %v", reverse, err)
	}
	renewal, err := RenewalKey("ORD-1042", 2)
	if err != nil || renewal != "renewal-ORD-1042-W2" {
		t.Errorf("RenewalKey = %q, %v", renewal, err)
	}
}

// Proves the double-click property: the same action on the same payment
// always gives the same key.
func TestIdempotencyKeysAreStable(t *testing.T) {
	makers := map[string]func() (string, error){
		"capture": func() (string, error) { return CaptureKey(fakePayA) },
		"reverse": func() (string, error) { return ReverseKey(fakePayA) },
		"renewal": func() (string, error) { return RenewalKey("ORD-1042", 3) },
	}
	for name, keyFor := range makers {
		first, err1 := keyFor()
		second, err2 := keyFor()
		if err1 != nil || err2 != nil || first != second {
			t.Errorf("%s: %q then %q (errs %v, %v)", name, first, second, err1, err2)
		}
	}
}

// Proves different actions, weeks or payments never share a key. A shared key
// would make Checkout.com return the cached result of a different action.
func TestIdempotencyKeysAreDistinct(t *testing.T) {
	must := func(k string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	keys := []string{
		must(CaptureKey(fakePayA)),
		must(CaptureKey(fakePayB)),
		must(ReverseKey(fakePayA)),
		must(ReverseKey(fakePayB)),
		must(RenewalKey("ORD-1042", 2)),
		must(RenewalKey("ORD-1042", 3)),
		must(RenewalKey("ORD-1043", 2)),
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("duplicate key %q", k)
		}
		seen[k] = true
	}
}

// Proves a bad payment ID gives an error, never a shared key like "capture-".
func TestIdempotencyKeysRejectBadPaymentIDs(t *testing.T) {
	for _, id := range []string{
		"", "   ", "pay_", "pay_ fake", "ord_fake", "ORD-1042",
		"pay_fakeaaaaaaaaaaaaaaaaaaaaa",   // 25 characters, one short
		"pay_fakeaaaaaaaaaaaaaaaaaaaaaaa", // 27 characters, one long
		"pay_fakeaaaaaaaaaa/../actionsx",  // 26 characters, but a path
	} {
		if k, err := CaptureKey(id); err == nil {
			t.Errorf("CaptureKey(%q) = %q, want error", id, k)
		}
		if k, err := ReverseKey(id); err == nil {
			t.Errorf("ReverseKey(%q) = %q, want error", id, k)
		}
	}
	if k, err := RenewalKey("", 2); err == nil {
		t.Errorf("RenewalKey with empty order = %q, want error", k)
	}
}

// Proves order IDs start at ORD-1042 and count up, and continue after the
// highest saved ID, ignoring renewal refs and junk.
func TestOrderIDGen(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     []string
	}{
		{"empty store", nil, []string{"ORD-1042", "ORD-1043"}},
		{"continues after max", []string{"ORD-1042", "ORD-1050", "ORD-1045"}, []string{"ORD-1051"}},
		{"ignores renewal refs and junk", []string{"ORD-1042-W9", "ORD-", "ORD-12a", "junk", "ORD-1044"}, []string{"ORD-1045"}},
		{"low saved numbers don't go backwards", []string{"ORD-7"}, []string{"ORD-1042"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newOrderIDGen(tt.existing)
			for _, want := range tt.want {
				if got := g.Next(); got != want {
					t.Errorf("Next() = %q, want %q", got, want)
				}
			}
		})
	}
}

// Proves Next is safe under concurrent calls: 100 goroutines, 100 unique IDs.
// Run with -race so the race detector also checks the mutex.
func TestOrderIDGenConcurrent(t *testing.T) {
	g := newOrderIDGen(nil)
	const n = 100
	ids := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids <- g.Next()
		}()
	}
	wg.Wait()
	close(ids)

	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate order ID %q", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Errorf("got %d unique IDs, want %d", len(seen), n)
	}
}
