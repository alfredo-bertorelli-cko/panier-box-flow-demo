package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// paymentTestApp is an app with an empty fake PaymentsAPI and a temp store.
// Each test loads the payments it needs with addRefPayment or addSandboxPayment.
func paymentTestApp(t *testing.T) (http.Handler, *fakePayments, *Store) {
	t.Helper()
	store, _ := newTestStore(t)
	fake := &fakePayments{
		details: map[string]*paymentsnas.GetPaymentResponse{},
		actions: map[string]*paymentsnas.GetPaymentActionsResponse{},
	}
	a := &app{
		cfg: sdkTestConfig(t), store: store, api: fake,
		ids: newOrderIDGen(nil), now: func() time.Time { return fixedNow },
	}
	return newRouter(a, fstest.MapFS{}), fake, store
}

// addPayment makes the fake answer GET details and GET actions for one
// payment, from fixture files. A missing actions file means no actions.
func addPayment(t *testing.T, fake *fakePayments, detailsFile, actionsFile string) *paymentsnas.GetPaymentResponse {
	t.Helper()
	d := loadPayment(t, detailsFile)
	fake.details[d.Id] = d
	fake.actions[d.Id] = &paymentsnas.GetPaymentActionsResponse{Actions: loadActions(t, actionsFile)}
	return d
}

func addRefPayment(t *testing.T, fake *fakePayments, name string) *paymentsnas.GetPaymentResponse {
	dir := filepath.Join("testdata", "payments")
	return addPayment(t, fake, filepath.Join(dir, name+".json"), filepath.Join(dir, name+"_actions.json"))
}

func addSandboxPayment(t *testing.T, fake *fakePayments, name string) *paymentsnas.GetPaymentResponse {
	t.Helper()
	dir := filepath.Join("testdata", "sandbox")
	path := filepath.Join(dir, "payment_"+name+".json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no %s yet", path)
	}
	return addPayment(t, fake, path, filepath.Join(dir, "actions_"+name+".json"))
}

// createOrder stores the order a payment's reference points at, as the
// session handler would have at checkout.
func createOrder(t *testing.T, store *Store, id, basket, country string) {
	t.Helper()
	if err := store.CreateOrder(NewOrder(id, mustLookup(t, basket, country), country, fixedNow)); err != nil {
		t.Fatal(err)
	}
}

func getPayment(h http.Handler, id string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/payments/"+id, nil))
	return rec
}

func decodePaymentResponse(t *testing.T, rec *httptest.ResponseRecorder) paymentResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var got paymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	return got
}

// Proves the happy path of the success page: an approved subscription CIT is
// stored, its card and scheme are recorded for renewals, and the reply has
// the order, the status card, renewals_ready and the stored card for the
// back office band.
func TestGetPaymentRecordsApprovedCIT(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	d := addRefPayment(t, fake, "ref_cit_authorized")

	rec := getPayment(h, d.Id)
	got := decodePaymentResponse(t, rec)

	if p := got.Payment; p.Status != "Authorized" || p.Kind != kindCIT || p.ResponseCode != "10000" {
		t.Errorf("payment %+v", p)
	}
	o := got.Order
	if o.ID != "ORD-1042" || o.CITPaymentID != d.Id || o.Scheme != "visa" || !o.RenewalsReady || len(o.Payments) != 1 || o.RenewalAmount != 3000 {
		t.Errorf("order %+v", o)
	}
	stored, _ := store.Order("ORD-1042")
	if stored.SourceID != "src_fakecardaaaaaaaaaaaaaaaaaa" {
		t.Errorf("stored source %q", stored.SourceID)
	}
	if o.SourceID != "src_fakecardaaaaaaaaaaaaaaaaaa" || o.NextRenewalWeek != 2 {
		t.Errorf("view source %q, next week %d", o.SourceID, o.NextRenewalWeek)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q, want no-store: the page polls this", cc)
	}
}

// Proves a second GET updates the same status card, never adds one, and
// never changes the recorded CIT. Shaped like the dispatch poll: Authorized,
// then Captured. The second answer also claims another scheme, to show
// RecordCIT keeps the first.
func TestGetPaymentTwiceUpdatesNotDuplicates(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	d := addRefPayment(t, fake, "ref_cit_authorized")
	decodePaymentResponse(t, getPayment(h, d.Id))

	captured := addRefPayment(t, fake, "ref_cit_captured") // same payment ID
	captured.ProcessingData.Scheme = "MASTERCARD"
	got := decodePaymentResponse(t, getPayment(h, d.Id))

	if len(got.Order.Payments) != 1 || got.Order.Payments[0].Status != "Captured" {
		t.Errorf("payments %+v, want one, Captured", got.Order.Payments)
	}
	if got.Order.Scheme != "visa" {
		t.Errorf("scheme %q: the CIT's scheme must never be overwritten", got.Order.Scheme)
	}
}

// Proves payments that must not drive renewals are stored but not recorded:
// renewals_ready stays false and the stored order has no card.
func TestGetPaymentDoesNotRecordCIT(t *testing.T) {
	tests := []struct {
		name, fixture, order, basket, country, wantStatus, wantCode string
	}{
		// 3DS failed: source.id is set, but the card was never authenticated.
		{"failed 3DS", "ref_cit_3ds_failed", "ORD-1042", basketSubscription, "FR", "Declined", "20151"},
		{"declined", "ref_cit_declined", "ORD-1042", basketSubscription, "FR", "Declined", "20051"},
		// A one-off never renews, and MB WAY has no card at all.
		{"one-off MB WAY", "ref_oneoff_mbway", "ORD-1043", basketOneOff, "PT", "Captured", "10000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fake, store := paymentTestApp(t)
			createOrder(t, store, tt.order, tt.basket, tt.country)
			d := addRefPayment(t, fake, tt.fixture)

			got := decodePaymentResponse(t, getPayment(h, d.Id))

			if got.Payment.Status != tt.wantStatus || got.Payment.ResponseCode != tt.wantCode {
				t.Errorf("payment %s %s, want %s %s", got.Payment.Status, got.Payment.ResponseCode, tt.wantStatus, tt.wantCode)
			}
			if o := got.Order; len(o.Payments) != 1 || o.CITPaymentID != "" || o.Scheme != "" || o.RenewalsReady {
				t.Errorf("order %+v, want the payment stored and no CIT recorded", o)
			}
			if stored, _ := store.Order(tt.order); stored.SourceID != "" {
				t.Errorf("stored source %q, want none", stored.SourceID)
			}
		})
	}
}

// Proves an approved card on a one-off order isn't recorded either: there
// is nothing to renew. Real sandbox shape: the MB WAY run gave no card, so
// this uses the reference Visa CIT on a one-off order.
func TestGetPaymentOneOffCardNotRecorded(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketOneOff, "FR")
	d := addRefPayment(t, fake, "ref_cit_authorized")

	got := decodePaymentResponse(t, getPayment(h, d.Id))
	if o := got.Order; o.CITPaymentID != "" || o.RenewalsReady || o.RenewalAmount != 0 {
		t.Errorf("order %+v, want no CIT and no renewals on a one-off", o)
	}
}

// Proves a renewal lands on its order by reference (ORD-1042-W2), next to the
// CIT, and doesn't touch the recorded card or scheme.
func TestGetPaymentMITJoinsItsOrder(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	cit := addRefPayment(t, fake, "ref_cit_authorized")
	decodePaymentResponse(t, getPayment(h, cit.Id))
	mit := addRefPayment(t, fake, "ref_mit_cobadged_cb")

	got := decodePaymentResponse(t, getPayment(h, mit.Id))

	if got.Payment.Kind != kindMIT || len(got.Order.Payments) != 2 {
		t.Errorf("kind %q, %d payments, want MIT and 2", got.Payment.Kind, len(got.Order.Payments))
	}
	if got.Order.CITPaymentID != cit.Id || got.Order.Scheme != "visa" {
		t.Errorf("CIT %q scheme %q: a renewal must not change them", got.Order.CITPaymentID, got.Order.Scheme)
	}
}

// Proves a payment with a reference we don't know (another test on the
// same sandbox, or an order lost from data/) is a clean 404 and nothing is stored.
func TestGetPaymentUnknownReference(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	d := addRefPayment(t, fake, "ref_minimal") // reference "someone-elses-test"
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")

	rec := getPayment(h, d.Id)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if o, _ := store.Order("ORD-1042"); len(o.Payments) != 0 {
		t.Errorf("payment stored on an unrelated order: %+v", o.Payments)
	}
}

// Proves a bad ID never reaches Checkout.com. %2F decodes to a slash in the
// path value: without the check the SDK would call another endpoint.
func TestGetPaymentBadID(t *testing.T) {
	for _, id := range []string{"abc", "pay_short", "pay_fakeaaaaaaaaaa%2F..%2Factionsx"} {
		h, fake, _ := paymentTestApp(t)
		rec := getPayment(h, id)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", id, rec.Code)
		}
		if n := fake.getPaymentCalls(); n != 0 {
			t.Errorf("%s: %d API calls, want 0", id, n)
		}
	}
}

// Proves a failed read of either GET answers 502 with Checkout.com's status
// and codes, and stores nothing, so the card is never half filled.
func TestGetPaymentAPIErrors(t *testing.T) {
	notFound := ckoerrors.CheckoutAPIError{StatusCode: 404, Status: "404 Not Found"}
	tests := []struct {
		name               string
		getErr, actionsErr error
	}{
		{"details fail", notFound, nil},
		{"actions fail", nil, notFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fake, store := paymentTestApp(t)
			createOrder(t, store, "ORD-1042", basketSubscription, "FR")
			d := addRefPayment(t, fake, "ref_cit_authorized")
			fake.getErr, fake.actionsErr = tt.getErr, tt.actionsErr

			rec := getPayment(h, d.Id)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status %d, want 502", rec.Code)
			}
			var e errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.CkoStatus != 404 || e.Error == "" {
				t.Errorf("error body %s", rec.Body)
			}
			if o, _ := store.Order("ORD-1042"); len(o.Payments) != 0 || o.CITPaymentID != "" {
				t.Errorf("stored after a failed read: %+v", o)
			}
		})
	}
}

// Proves the handler on real sandbox payments (step 3.1b dumps): the CB
// choice is what gets recorded for renewals, a failed 3DS records nothing,
// and MB WAY is stored as Captured. Skips any file that isn't there.
func TestGetPaymentSandboxFixtures(t *testing.T) {
	tests := []struct {
		fixture, basket, country string
		wantScheme               string // recorded on the order, "" for none
		wantReady                bool
	}{
		{"cit_cb_chose_cb", basketSubscription, "FR", "cartes_bancaires", true},
		{"cit_cb_chose_visa", basketSubscription, "FR", "visa", true},
		{"cit_googlepay", basketSubscription, "FR", "visa", true},
		{"cit_3ds_failed", basketSubscription, "FR", "", false},
		{"cit_declined", basketSubscription, "FR", "", false},
		{"oneoff_mbway", basketOneOff, "PT", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			h, fake, store := paymentTestApp(t)
			d := addSandboxPayment(t, fake, tt.fixture)
			createOrder(t, store, d.Reference, tt.basket, tt.country)

			got := decodePaymentResponse(t, getPayment(h, d.Id))
			if got.Order.Scheme != tt.wantScheme || got.Order.RenewalsReady != tt.wantReady {
				t.Errorf("scheme %q ready %v, want %q %v", got.Order.Scheme, got.Order.RenewalsReady, tt.wantScheme, tt.wantReady)
			}
		})
	}
}

// Proves only the first payment can set the renewal card. If a renewal is
// fetched before its CIT ever was (say the success page never loaded), it
// must not become the CIT: previous_payment_id would point at the wrong payment.
func TestGetPaymentMITFirstIsNotRecorded(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	mit := addRefPayment(t, fake, "ref_mit_cobadged_cb")

	got := decodePaymentResponse(t, getPayment(h, mit.Id))
	if o := got.Order; o.CITPaymentID != "" || o.Scheme != "" || o.RenewalsReady {
		t.Errorf("order %+v, want no CIT recorded from a renewal", o)
	}
}
