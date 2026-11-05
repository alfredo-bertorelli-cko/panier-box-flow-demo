package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
)

func postReverse(h http.Handler, paymentID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/payments/"+paymentID+"/reverse", nil))
	return rec
}

// Contract test: the real SDK sends POST /payments/{id}/reversals with only
// the reference and our key. No amount: a reversal is the full payment, and
// the API reference request has no amount field (the SDK struct does, D-035).
func TestReverseContract(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusAccepted, `{"action_id":"act_fakereverseaaaaaaaaaaaaaaaa","reference":"ORD-1042-W2","action_type":"Void"}`)
	api := fake.client(t, nil)
	key, _ := ReverseKey(fakePaymentID)

	resp, err := api.Reverse(context.Background(), fakePaymentID, payments.PaymentReversalRequest{Reference: "ORD-1042-W2"}, key)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if resp.ActionType != "Void" {
		t.Errorf("action_type %q", resp.ActionType)
	}

	reqs := fake.recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/payments/"+fakePaymentID+"/reversals" {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Cko-Idempotency-Key"); got != "reverse-"+fakePaymentID {
		t.Errorf("Cko-Idempotency-Key %q", got)
	}
	if d := diffKeys(map[string]any{"reference": "ORD-1042-W2"}, decodeJSON(t, "reverse body", r.Body)); len(d) > 0 {
		t.Errorf("reverse body: %v\nsent: %s", d, r.Body)
	}
	assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Cko-Idempotency-Key", "Content-Length", "Content-Type", "User-Agent")
}

// Proves which button each card gets, from its own balances.
func TestReverseAction(t *testing.T) {
	tests := []struct {
		name               string
		p                  Payment
		wantAction, reason string
	}{
		{"authorised MIT: void", Payment{Status: "Authorized", Balances: Balances{Authorized: 3000}}, reverseVoid, ""},
		{"captured CIT: refund", Payment{Status: "Captured", Balances: Balances{Authorized: 5000, Captured: 5000}}, reverseRefund, ""},
		{"MB WAY captured at payment: refund", Payment{Status: "Captured", Method: PaymentMethod{Type: "mbway"}, Balances: Balances{Authorized: 4500, Captured: 4500}}, reverseRefund, ""},
		{"partly refunded: refund the rest", Payment{Status: "Partially Refunded", Balances: Balances{Authorized: 5000, Captured: 5000, Refunded: 2000}}, reverseRefund, ""},
		{"voided", Payment{Status: "Voided", Balances: Balances{Authorized: 3000, Voided: 3000}}, "", "Voided."},
		{"refunded", Payment{Status: "Refunded", Balances: Balances{Authorized: 5000, Captured: 5000, Refunded: 5000}}, "", "Refunded."},
		{"declined", Payment{Status: "Declined"}, "", "Nothing to reverse."},
		{"pending", Payment{Status: "Pending"}, "", "Nothing to reverse."},
		// Status not caught up yet, but the balances say it's all voided.
		{"authorised, nothing left", Payment{Status: "Authorized", Balances: Balances{Authorized: 3000, Voided: 3000}}, "", "Nothing to reverse."},
	}
	for _, tt := range tests {
		if action, reason := reverseAction(tt.p); action != tt.wantAction || reason != tt.reason {
			t.Errorf("%s: got %q %q, want %q %q", tt.name, action, reason, tt.wantAction, tt.reason)
		}
	}
}

// Proves Reverse on an authorised renewal: one call with reverse-{id} and the
// renewal's reference, then the card re-read and stored (here as Voided).
func TestReverseHandlerVoidsMIT(t *testing.T) {
	h, fake, store, mit := renewalApp(t)
	decodePaymentResponse(t, postRenewal(h, "ORD-1042"))
	voided := addRefPayment(t, fake, "ref_mit_cobadged_cb")
	voided.Status = "Voided"
	voided.Balances.TotalVoided = 3000

	got := decodePaymentResponse(t, postReverse(h, mit.Id))

	calls := fake.reverseCalls()
	if len(calls) != 1 || calls[0].paymentID != mit.Id || calls[0].key != "reverse-"+mit.Id || calls[0].req.Reference != "ORD-1042-W2" || calls[0].req.Amount != 0 {
		t.Fatalf("reverse calls %+v", calls)
	}
	if got.Payment.Status != "Voided" {
		t.Errorf("status %q", got.Payment.Status)
	}
	// The card now has no Reverse button, and says why.
	for _, p := range got.Order.Payments {
		if p.PaymentID == mit.Id && (p.ReverseAction != "" || p.ReverseReason != "Voided.") {
			t.Errorf("view after void: %q %q", p.ReverseAction, p.ReverseReason)
		}
	}
	// Reversing the CIT's renewal leaves renewals on (Alfredo, 4.4a).
	if o, _ := store.Order("ORD-1042"); renewalBlock(o) != "" {
		t.Errorf("renewals blocked after a reverse: %q", renewalBlock(o))
	}
}

// Proves Reverse on a captured first box sends the same call (the API picks
// refund), and an MB WAY one-off, captured at payment, can be refunded too.
func TestReverseHandlerRefunds(t *testing.T) {
	t.Run("captured CIT", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1042", basketSubscription, "FR")
		cit := addRefPayment(t, fake, "ref_cit_captured")
		if got := decodePaymentResponse(t, getPayment(h, cit.Id)); got.Order.Payments[0].ReverseAction != reverseRefund {
			t.Fatalf("view before: %q", got.Order.Payments[0].ReverseAction)
		}
		addRefPayment(t, fake, "ref_cit_refunded") // same ID, as the GET after the refund

		got := decodePaymentResponse(t, postReverse(h, cit.Id))
		if got.Payment.Status != "Refunded" || len(fake.reverseCalls()) != 1 {
			t.Errorf("status %q, %d calls", got.Payment.Status, len(fake.reverseCalls()))
		}
	})
	t.Run("MB WAY one-off", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1043", basketOneOff, "PT")
		mb := addRefPayment(t, fake, "ref_oneoff_mbway")
		decodePaymentResponse(t, getPayment(h, mb.Id))

		decodePaymentResponse(t, postReverse(h, mb.Id))
		if calls := fake.reverseCalls(); len(calls) != 1 || calls[0].req.Reference != "ORD-1043" {
			t.Errorf("calls %+v", calls)
		}
	})
}

// Proves a double click is safe: the reversal is async, so the GET after the
// first click can still show the old status, and the second click sends the
// same key. Checkout.com reverses once.
func TestReverseDoubleClickSameKey(t *testing.T) {
	h, fake, _, mit := renewalApp(t)
	decodePaymentResponse(t, postRenewal(h, "ORD-1042"))

	decodePaymentResponse(t, postReverse(h, mit.Id))
	decodePaymentResponse(t, postReverse(h, mit.Id))

	calls := fake.reverseCalls()
	if len(calls) != 2 || calls[0].key != calls[1].key {
		t.Errorf("calls %+v, want two with the same key", calls)
	}
}

// Proves the refusals, each with no API call.
func TestReverseRefused(t *testing.T) {
	t.Run("declined: nothing to reverse", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1042", basketSubscription, "FR")
		d := addRefPayment(t, fake, "ref_cit_declined")
		decodePaymentResponse(t, getPayment(h, d.Id))
		rec := postReverse(h, d.Id)
		if rec.Code != http.StatusConflict || errorMessage(t, rec).Error != "Nothing to reverse." {
			t.Errorf("status %d, body %s", rec.Code, rec.Body)
		}
		if n := len(fake.reverseCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
	t.Run("not on our orders", func(t *testing.T) {
		h, fake, _ := paymentTestApp(t)
		if rec := postReverse(h, fakePaymentID); rec.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", rec.Code)
		}
		if n := len(fake.reverseCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
	t.Run("bad ID", func(t *testing.T) {
		h, fake, _ := paymentTestApp(t)
		if rec := postReverse(h, "pay_short"); rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
		if n := len(fake.reverseCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
}

// Proves a refused reversal shows status and codes and changes nothing.
func TestReverseAPIError(t *testing.T) {
	h, fake, store, mit := renewalApp(t)
	decodePaymentResponse(t, postRenewal(h, "ORD-1042"))
	fake.reverseErr = ckoerrors.CheckoutAPIError{StatusCode: 403, Status: "403 Forbidden"}

	rec := postReverse(h, mit.Id)
	if rec.Code != http.StatusBadGateway || errorMessage(t, rec).CkoStatus != 403 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	_, p, _ := store.OrderWithPayment(mit.Id)
	if p.Status != "Authorized" {
		t.Errorf("stored status %q, want Authorized", p.Status)
	}
}

// Proves the store finds the order a payment is on, and nothing else.
func TestOrderWithPayment(t *testing.T) {
	s, _ := newTestStore(t)
	mustCreate(t, s, subscriptionOrder(t))
	if _, err := s.UpsertPayment("ORD-1042", samplePayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")); err != nil {
		t.Fatal(err)
	}
	o, p, ok := s.OrderWithPayment("pay_fakeaaaaaaaaaaaaaaaaaaaaaa")
	if !ok || o.ID != "ORD-1042" || p.PaymentID != "pay_fakeaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("got %v %q %q", ok, o.ID, p.PaymentID)
	}
	if _, _, ok := s.OrderWithPayment("pay_fakebbbbbbbbbbbbbbbbbbbbbb"); ok {
		t.Error("found a payment that isn't stored")
	}
}
