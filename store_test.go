package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

// newTestStore opens a store in a fresh temp folder. The data/ subfolder
// doesn't exist yet, so the first save also proves it gets created.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "orders.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	return s, path
}

// subscriptionOrder is ORD-1042 built from the real subscription basket.
func subscriptionOrder(t *testing.T) Order {
	t.Helper()
	return NewOrder("ORD-1042", mustLookup(t, basketSubscription, "FR"), "FR", fixedNow)
}

// samplePayment is an Authorized CIT with every field set, so round trips
// exercise them all.
func samplePayment(id string) Payment {
	return Payment{
		PaymentID: id,
		Kind:      kindCIT,
		Reference: "ORD-1042",
		Amount:    5000,
		Status:    "Authorized",
		Method:    PaymentMethod{Type: "card", Scheme: "visa", Last4: "4242", CardType: "Credit"},
		Balances:  Balances{Authorized: 5000},
		ThreeDS:   "Y", ResponseCode: "10000", ResponseSummary: "Approved",
		ExpiresOn: fixedNow.Add(7 * 24 * time.Hour),
		UpdatedAt: fixedNow,
	}
}

func mustCreate(t *testing.T, s *Store, o Order) {
	t.Helper()
	if err := s.CreateOrder(o); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
}

// Proves NewOrder takes amount and items from the basket and sets the renewal week.
func TestNewOrder(t *testing.T) {
	sub := subscriptionOrder(t)
	if sub.Amount != 5000 || len(sub.Items) != 4 || sub.NextRenewalWeek != 2 || sub.Currency != "EUR" {
		t.Errorf("subscription order: %+v", sub)
	}
	one := NewOrder("ORD-1043", mustLookup(t, basketOneOff, "PT"), "PT", fixedNow)
	if one.Amount != 4500 || one.NextRenewalWeek != 0 || one.Country != "PT" {
		t.Errorf("one-off order: %+v", one)
	}
}

// Proves upsert by payment_id: same ID updates in place, a new ID appends,
// an unknown order is an error.
func TestUpsertPayment(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))

	p := samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")
	if _, err := s.UpsertPayment("ORD-1042", p); err != nil {
		t.Fatal(err)
	}
	p.Status = "Captured"
	p.Balances.Captured = 5000
	o, err := s.UpsertPayment("ORD-1042", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Payments) != 1 || o.Payments[0].Status != "Captured" || o.Payments[0].Balances.Captured != 5000 {
		t.Errorf("same ID twice: got %d payments, first %+v", len(o.Payments), o.Payments[0])
	}

	mit := samplePayment("pay_fakebbbbbbbbbbbbbbbbbbbbbb")
	mit.Kind, mit.Reference = kindMIT, "ORD-1042-W2"
	if o, _ = s.UpsertPayment("ORD-1042", mit); len(o.Payments) != 2 {
		t.Errorf("new ID: got %d payments, want 2", len(o.Payments))
	}

	if _, err := s.UpsertPayment("ORD-9999", p); err == nil {
		t.Error("unknown order: want error")
	}
	if _, err := s.UpsertPayment("ORD-1042", Payment{}); err == nil {
		t.Error("empty payment ID: want error")
	}
}

// Proves save then reopen gives exactly the same data, every field included.
func TestSaveAndReload(t *testing.T) {
	s, path := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	mustCreate(t, s, NewOrder("ORD-1043", mustLookup(t, basketOneOff, "FR"), "FR", fixedNow))
	if _, err := s.UpsertPayment("ORD-1042", samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordCIT("ORD-1042", "pay_fakeaaaaaaaaaaaaaaaaaaaaaa", "src_fakeaaaaaaaaaaaaaaaaaaaaaaa", "cartes_bancaires"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdvanceRenewalWeek("ORD-1042", 2); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, id := range []string{"ORD-1042", "ORD-1043"} {
		before, _ := s.Order(id)
		after, ok := reopened.Order(id)
		if !ok || !reflect.DeepEqual(before, after) {
			t.Errorf("%s changed on reload:\nbefore %+v\nafter  %+v", id, before, after)
		}
	}
}

// Proves a missing file is an empty store, and a corrupt one is an error
// that leaves the file untouched.
func TestOpenStoreMissingAndCorrupt(t *testing.T) {
	s, _ := newTestStore(t)
	if ids := s.OrderIDs(); len(ids) != 0 {
		t.Errorf("missing file: got %v orders", ids)
	}

	path := filepath.Join(t.TempDir(), "orders.json")
	corrupt := []byte(`{"orders": [ {"id": "ORD-1042", `)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err == nil {
		t.Error("corrupt file: want error")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, corrupt) {
		t.Error("corrupt file was changed")
	}
}

// Proves the write leaves no temp files behind and the file is private (0600).
func TestSaveIsCleanAndPrivate(t *testing.T) {
	s, path := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "orders.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("folder holds %v, want only orders.json", names)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode %o, want 600", mode)
	}
}

// Proves a failed save keeps both the old file and the old memory, so the
// two never disagree.
func TestFailedSaveRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores folder permissions")
	}
	s, path := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	fileBefore, _ := os.ReadFile(path)

	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil { // read and enter, no writes
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) }) // so TempDir can be removed

	if _, err := s.UpsertPayment("ORD-1042", samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")); err == nil {
		t.Fatal("want a save error in a read-only folder")
	}
	if o, _ := s.Order("ORD-1042"); len(o.Payments) != 0 {
		t.Errorf("memory kept the failed change: %d payments", len(o.Payments))
	}
	if fileAfter, _ := os.ReadFile(path); !bytes.Equal(fileBefore, fileAfter) {
		t.Error("file changed after a failed save")
	}
}

// Proves CIT details are set once: a later call can't overwrite them.
func TestRecordCITSetsOnce(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))

	if _, err := s.RecordCIT("ORD-1042", "pay_fakeaaaaaaaaaaaaaaaaaaaaaa", "src_fakeaaaaaaaaaaaaaaaaaaaaaaa", "cartes_bancaires"); err != nil {
		t.Fatal(err)
	}
	o, err := s.RecordCIT("ORD-1042", "pay_fakebbbbbbbbbbbbbbbbbbbbbb", "src_fakebbbbbbbbbbbbbbbbbbbbbbb", "visa")
	if err != nil {
		t.Fatal(err)
	}
	if o.CITPaymentID != "pay_fakeaaaaaaaaaaaaaaaaaaaaaa" || o.SourceID != "src_fakeaaaaaaaaaaaaaaaaaaaaaaa" || o.Scheme != "cartes_bancaires" {
		t.Errorf("CIT details overwritten: %s %s %s", o.CITPaymentID, o.SourceID, o.Scheme)
	}
	if _, err := s.RecordCIT("ORD-9999", "pay_x", "src_x", "visa"); err == nil {
		t.Error("unknown order: want error")
	}
}

// Proves a missing source on the first call can still be filled by a later
// GET (D-044: source.id may arrive late), while set fields stay put.
func TestRecordCITFillsOnlyEmptyFields(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	s.RecordCIT("ORD-1042", "pay_fakeaaaaaaaaaaaaaaaaaaaaaa", "", "visa")
	o, _ := s.RecordCIT("ORD-1042", "pay_fakeaaaaaaaaaaaaaaaaaaaaaa", "src_fakeaaaaaaaaaaaaaaaaaaaaaaa", "mastercard")
	if o.SourceID != "src_fakeaaaaaaaaaaaaaaaaaaaaaaa" || o.Scheme != "visa" {
		t.Errorf("got source %q scheme %q", o.SourceID, o.Scheme)
	}
}

// Proves the renewal week moves up by one, and one-off orders can't renew.
// A second advance from the same week (a double click) is refused, so a week
// is never skipped.
func TestAdvanceRenewalWeek(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	mustCreate(t, s, NewOrder("ORD-1043", mustLookup(t, basketOneOff, "FR"), "FR", fixedNow))

	o, err := s.AdvanceRenewalWeek("ORD-1042", 2)
	if err != nil || o.NextRenewalWeek != 3 {
		t.Errorf("got week %d, err %v; want 3", o.NextRenewalWeek, err)
	}
	if _, err := s.AdvanceRenewalWeek("ORD-1042", 2); !errors.Is(err, errWeekMoved) {
		t.Errorf("second advance from week 2: got %v, want errWeekMoved", err)
	}
	if o, _ := s.Order("ORD-1042"); o.NextRenewalWeek != 3 {
		t.Errorf("week %d after the refused advance, want 3", o.NextRenewalWeek)
	}
	if _, err := s.AdvanceRenewalWeek("ORD-1043", 0); err == nil {
		t.Error("one-off order: want error")
	}
	if _, err := s.AdvanceRenewalWeek("ORD-9999", 2); err == nil {
		t.Error("unknown order: want error")
	}
}

// Proves duplicate and empty order IDs are refused, and OrderIDs lists all orders.
func TestCreateOrder(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	if err := s.CreateOrder(subscriptionOrder(t)); err == nil {
		t.Error("duplicate ID: want error")
	}
	if err := s.CreateOrder(Order{}); err == nil {
		t.Error("empty ID: want error")
	}
	mustCreate(t, s, NewOrder("ORD-1043", mustLookup(t, basketOneOff, "FR"), "FR", fixedNow))
	if got := s.OrderIDs(); !reflect.DeepEqual(got, []string{"ORD-1042", "ORD-1043"}) {
		t.Errorf("OrderIDs = %v", got)
	}
}

// Proves returned orders are copies: editing one doesn't change the store.
func TestOrderReturnsCopy(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	s.UpsertPayment("ORD-1042", samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa"))

	o, _ := s.Order("ORD-1042")
	o.Items[0].UnitPrice = 1
	o.Payments[0].Status = "Hacked"

	again, _ := s.Order("ORD-1042")
	if again.Items[0].UnitPrice != 3000 || again.Payments[0].Status != "Authorized" {
		t.Error("store data changed through a returned copy")
	}
}

// Proves both CIT and renewal references find their order.
func TestOrderForReference(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	for _, ref := range []string{"ORD-1042", "ORD-1042-W2"} {
		if o, ok := s.OrderForReference(ref); !ok || o.ID != "ORD-1042" {
			t.Errorf("%q: got %q, %v", ref, o.ID, ok)
		}
	}
	for _, ref := range []string{"ORD-9999", "junk"} {
		if _, ok := s.OrderForReference(ref); ok {
			t.Errorf("%q: want not found", ref)
		}
	}
}

// Proves concurrent writes and reads are safe: 50 upserts from 50 goroutines
// all land, on disk too. Run with -race.
func TestConcurrentUpserts(t *testing.T) {
	s, path := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := s.UpsertPayment("ORD-1042", samplePayment(fmt.Sprintf("pay_fake%022d", i)))
			errs <- err
		}()
		go func() {
			defer wg.Done()
			s.Order("ORD-1042")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if o, _ := s.Order("ORD-1042"); len(o.Payments) != n {
		t.Errorf("memory: %d payments, want %d", len(o.Payments), n)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if o, _ := reopened.Order("ORD-1042"); len(o.Payments) != n {
		t.Errorf("disk: %d payments, want %d", len(o.Payments), n)
	}
}

// Proves the file uses the key names from PRD.md section 6. A renamed Go
// field would otherwise change orders.json without any test failing.
func TestStoreJSONKeys(t *testing.T) {
	o := subscriptionOrder(t)
	o.Payments = []Payment{samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")}
	data, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	json.Unmarshal(data, &top)
	for _, k := range []string{"id", "basket", "country", "currency", "items", "amount", "created_at",
		"source_id", "scheme", "cit_payment_id", "payments", "next_renewal_week"} {
		if _, ok := top[k]; !ok {
			t.Errorf("order JSON missing %q", k)
		}
	}

	var payments []map[string]json.RawMessage
	json.Unmarshal(top["payments"], &payments)
	for _, k := range []string{"payment_id", "kind", "reference", "amount", "status", "method",
		"balances", "three_ds", "response_code", "response_summary", "expires_on", "updated_at"} {
		if _, ok := payments[0][k]; !ok {
			t.Errorf("payment JSON missing %q", k)
		}
	}
	for _, k := range []string{`"last4"`, `"card_type"`, `"authorized"`, `"captured"`, `"voided"`, `"refunded"`, `"unit_price"`} {
		if !strings.Contains(string(data), k) {
			t.Errorf("JSON missing key %s", k)
		}
	}
}

// Proves empty times are left out of the file, not written as year 0001.
func TestStoreOmitsZeroTimes(t *testing.T) {
	data, _ := json.Marshal(Payment{PaymentID: "pay_fakeaaaaaaaaaaaaaaaaaaaaaa"})
	if strings.Contains(string(data), "expires_on") || strings.Contains(string(data), "0001") {
		t.Errorf("zero time written: %s", data)
	}
}
