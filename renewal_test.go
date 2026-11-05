package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// renewableOrder is ORD-1042 after an approved CIT on a co-badged card where
// Marie chose CB: what RecordCIT stores (D-029).
func renewableOrder(t *testing.T, week int) Order {
	t.Helper()
	o := subscriptionOrder(t)
	o.CITPaymentID = "pay_fakecitaaaaaaaaaaaaaaaaaaa"
	o.SourceID = "src_fakecardaaaaaaaaaaaaaaaaaa"
	o.Scheme = "cartes_bancaires"
	o.NextRenewalWeek = week
	return o
}

// Contract test: the real SDK's POST /payments body for the week 2 renewal
// matches testdata/mit_renewal_w2.json field by field. This is where a
// dropped capture false or merchant_initiated would show. It also records
// what SDK v3.8.0 adds on its own (D-034): an empty payment_plan and six zero
// amounts in processing.
func TestRenewalRequestContract(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusCreated, `{"id":"pay_fakemitaaaaaaaaaaaaaaaaaaa","approved":true,"status":"Authorized"}`)
	api := fake.client(t, nil)
	req, err := buildRenewalRequest(renewableOrder(t, 2), testSettings)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := api.RequestMIT(context.Background(), req, "renewal-ORD-1042-W2")
	if err != nil {
		t.Fatalf("RequestMIT: %v", err)
	}
	if resp.Id != "pay_fakemitaaaaaaaaaaaaaaaaaaa" || !resp.Approved {
		t.Errorf("response %+v", resp)
	}

	reqs := fake.recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/payments" {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Cko-Idempotency-Key"); got != "renewal-ORD-1042-W2" {
		t.Errorf("Cko-Idempotency-Key %q", got)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "mit_renewal_w2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if d := diffKeys(decodeJSON(t, "golden", golden), decodeJSON(t, "sent body", r.Body)); len(d) > 0 {
		t.Errorf("body differs from mit_renewal_w2.json: %v\nsent: %s", d, r.Body)
	}
	assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Cko-Idempotency-Key", "Content-Length", "Content-Type", "User-Agent")
}

// Proves the series rules for every week: the reference follows the week,
// previous_payment_id is always the first CIT (never the last renewal), and
// the scheme is the one Marie chose on the CIT.
func TestBuildRenewalRequest(t *testing.T) {
	for _, week := range []int{2, 3, 13} {
		req, err := buildRenewalRequest(renewableOrder(t, week), testSettings)
		if err != nil {
			t.Fatalf("week %d: %v", week, err)
		}
		if req.PreviousPaymentId != "pay_fakecitaaaaaaaaaaaaaaaaaaa" {
			t.Errorf("week %d: previous_payment_id %q, want the first CIT", week, req.PreviousPaymentId)
		}
		if want := "ORD-1042-W" + strconv.Itoa(week); req.Reference != want {
			t.Errorf("week %d: reference %q, want %q", week, req.Reference, want)
		}
		if !req.MerchantInitiated || req.Capture || req.PaymentType != payments.Recurring || req.Amount != 3000 {
			t.Errorf("week %d: mit %v capture %v type %q amount %d", week, req.MerchantInitiated, req.Capture, req.PaymentType, req.Amount)
		}
		if req.Processing == nil || req.Processing.PreferredScheme != payments.CartesBancaires {
			t.Errorf("week %d: preferred_scheme %+v, want cartes_bancaires", week, req.Processing)
		}
	}
}

// Proves the builder refuses what renewals can't run on.
func TestBuildRenewalRequestRefuses(t *testing.T) {
	noCard := renewableOrder(t, 2)
	noCard.SourceID = ""
	noScheme := renewableOrder(t, 2)
	noScheme.Scheme = ""
	oneOff := renewableOrder(t, 0)
	for name, o := range map[string]Order{"no stored card": noCard, "no scheme": noScheme, "one-off": oneOff} {
		if _, err := buildRenewalRequest(o, testSettings); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func postRenewal(h http.Handler, orderID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/orders/"+orderID+"/renewals", nil))
	return rec
}

// renewalApp is ORD-1042 with its approved CIT already synced (so the card
// is recorded), and the fake set to answer the MIT with ref_mit_cobadged_cb.
func renewalApp(t *testing.T) (http.Handler, *fakePayments, *Store, *paymentsnas.GetPaymentResponse) {
	t.Helper()
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	decodePaymentResponse(t, getPayment(h, addRefPayment(t, fake, "ref_cit_authorized").Id))
	mit := addRefPayment(t, fake, "ref_mit_cobadged_cb") // reference ORD-1042-W2
	fake.mitResp = &paymentsnas.PaymentResponse{Id: mit.Id, Approved: true, Status: "Authorized"}
	return h, fake, store, mit
}

// Proves the happy path: one MIT with key renewal-ORD-1042-W2, its card
// stored next to the CIT, the week moves to 3, the CIT record is untouched.
func TestRenewalHandler(t *testing.T) {
	h, fake, store, mit := renewalApp(t)

	got := decodePaymentResponse(t, postRenewal(h, "ORD-1042"))

	calls := fake.mitCalls()
	if len(calls) != 1 || calls[0].key != "renewal-ORD-1042-W2" || calls[0].req.Reference != "ORD-1042-W2" {
		t.Fatalf("MIT calls %+v", calls)
	}
	// The CIT ran on Visa in this fixture, so that's what the MIT asks for.
	if calls[0].req.PreviousPaymentId != "pay_fakecitaaaaaaaaaaaaaaaaaaa" || calls[0].req.Processing.PreferredScheme != payments.Visa {
		t.Errorf("MIT request %+v", calls[0].req)
	}
	if got.Payment.PaymentID != mit.Id || got.Payment.Kind != kindMIT || len(got.Order.Payments) != 2 {
		t.Errorf("payment %+v, %d payments", got.Payment, len(got.Order.Payments))
	}
	if got.Order.NextRenewalWeek != 3 || got.Order.CITPaymentID != "pay_fakecitaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("week %d, CIT %q", got.Order.NextRenewalWeek, got.Order.CITPaymentID)
	}
	if stored, _ := store.Order("ORD-1042"); stored.NextRenewalWeek != 3 {
		t.Errorf("stored week %d", stored.NextRenewalWeek)
	}
}

// Proves a declined renewal is shown but doesn't move the week: week 2 was
// never paid. A retry sends the same key, so Checkout.com replays the decline.
func TestRenewalDeclinedKeepsWeek(t *testing.T) {
	h, fake, store, mit := renewalApp(t)
	fake.mitResp = &paymentsnas.PaymentResponse{Id: mit.Id, Approved: false, Status: "Declined"}

	got := decodePaymentResponse(t, postRenewal(h, "ORD-1042"))
	decodePaymentResponse(t, postRenewal(h, "ORD-1042"))

	if got.Order.NextRenewalWeek != 2 {
		t.Errorf("week %d after a decline, want 2", got.Order.NextRenewalWeek)
	}
	calls := fake.mitCalls()
	if len(calls) != 2 || calls[0].key != calls[1].key {
		t.Errorf("calls %+v, want the same key twice", calls)
	}
	if stored, _ := store.Order("ORD-1042"); stored.NextRenewalWeek != 2 {
		t.Errorf("stored week %d", stored.NextRenewalWeek)
	}
}

// Proves a double click renews week 2 once and advances once. The second
// click arrives while the first MIT call is still in flight: it reads the
// order on week 2 too, so both send renewal-ORD-1042-W2 (Checkout.com
// charges once). The second finishes first and advances to 3. The first then
// finds the week moved and must not advance again to 4, skipping week 3.
func TestRenewalDoubleClickOverlap(t *testing.T) {
	h, fake, store, _ := renewalApp(t)
	var second *httptest.ResponseRecorder
	fake.onMIT = func() {
		if second == nil {
			second = httptest.NewRecorder() // mark before re-entering, so it runs once
			h.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/orders/ORD-1042/renewals", nil))
		}
	}

	first := decodePaymentResponse(t, postRenewal(h, "ORD-1042"))

	if second.Code != http.StatusOK {
		t.Fatalf("second click: status %d, body %s", second.Code, second.Body)
	}
	calls := fake.mitCalls()
	if len(calls) != 2 || calls[0].key != "renewal-ORD-1042-W2" || calls[1].key != "renewal-ORD-1042-W2" {
		t.Errorf("calls %+v, want two with renewal-ORD-1042-W2", calls)
	}
	if o, _ := store.Order("ORD-1042"); o.NextRenewalWeek != 3 || len(o.Payments) != 2 {
		t.Errorf("week %d, %d payments; want week 3 and CIT plus one MIT", o.NextRenewalWeek, len(o.Payments))
	}
	if first.Order.NextRenewalWeek != 3 {
		t.Errorf("first click shows week %d, want 3", first.Order.NextRenewalWeek)
	}
}

// Proves the refusals: a one-off or an order without a stored card is a 409
// with the mockup's reason and no API call. An unknown order is a 404.
func TestRenewalRefused(t *testing.T) {
	t.Run("one-off", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1043", basketOneOff, "PT")
		rec := postRenewal(h, "ORD-1043")
		if rec.Code != http.StatusConflict || errorMessage(t, rec).Error != "One-off orders have no renewals." {
			t.Errorf("status %d, body %s", rec.Code, rec.Body)
		}
		if n := len(fake.mitCalls()); n != 0 {
			t.Errorf("%d MIT calls", n)
		}
	})
	t.Run("failed 3DS, no stored card", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1042", basketSubscription, "FR")
		decodePaymentResponse(t, getPayment(h, addRefPayment(t, fake, "ref_cit_3ds_failed").Id))
		rec := postRenewal(h, "ORD-1042")
		if rec.Code != http.StatusConflict || errorMessage(t, rec).Error != "No stored card on the first payment, renewals off (P-8)." {
			t.Errorf("status %d, body %s", rec.Code, rec.Body)
		}
		if n := len(fake.mitCalls()); n != 0 {
			t.Errorf("%d MIT calls", n)
		}
	})
	t.Run("unknown order", func(t *testing.T) {
		h, _, _ := paymentTestApp(t)
		if rec := postRenewal(h, "ORD-9999"); rec.Code != http.StatusNotFound {
			t.Errorf("status %d", rec.Code)
		}
	})
}

// Proves a refused MIT (say, a scheme error) shows status and codes, stores
// nothing new and keeps the week.
func TestRenewalAPIError(t *testing.T) {
	h, fake, store, _ := renewalApp(t)
	fake.mitErr = ckoerrors.CheckoutAPIError{
		StatusCode: 422, Status: "422 Unprocessable Entity",
		Data: &ckoerrors.ErrorDetails{ErrorCodes: []string{"previous_payment_id_invalid"}},
	}
	rec := postRenewal(h, "ORD-1042")
	if rec.Code != http.StatusBadGateway || errorMessage(t, rec).CkoStatus != 422 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if o, _ := store.Order("ORD-1042"); o.NextRenewalWeek != 2 || len(o.Payments) != 1 {
		t.Errorf("week %d, %d payments after a refused MIT", o.NextRenewalWeek, len(o.Payments))
	}
}

// Proves the view tells the page when the renewal button is on.
func TestViewRenewalState(t *testing.T) {
	if v := viewFor(renewableOrder(t, 2)); !v.CanRenew || v.RenewReason != "" {
		t.Errorf("renewable: %v %q", v.CanRenew, v.RenewReason)
	}
	if v := viewFor(renewableOrder(t, 0)); v.CanRenew || v.RenewReason != "One-off orders have no renewals." {
		t.Errorf("one-off: %v %q", v.CanRenew, v.RenewReason)
	}
}
