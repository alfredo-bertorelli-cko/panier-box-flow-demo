package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
)

func postCapture(h http.Handler, orderID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/orders/"+orderID+"/capture", nil))
	return rec
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) errorBody {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body)
	}
	return e
}

// Contract test: the real SDK sends POST /payments/{id}/captures with exactly
// {"amount", "reference"} and our key in Cko-Idempotency-Key. A dropped
// amount would still capture (the API defaults to the full amount), and a
// dropped key would still work until someone double-clicks, so only the real
// request shows either.
func TestCaptureContract(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusAccepted, `{"action_id":"act_fakecaptureaaaaaaaaaaaaaaaa","reference":"ORD-1042"}`)
	api := fake.client(t, nil)
	o := subscriptionOrder(t)
	p := Payment{PaymentID: fakePaymentID, Kind: kindCIT, Status: "Authorized", Balances: Balances{Authorized: 5000}}
	key, _ := CaptureKey(p.PaymentID)

	resp, err := api.Capture(context.Background(), p.PaymentID, buildCaptureRequest(o, p), key)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if resp.ActionId != "act_fakecaptureaaaaaaaaaaaaaaaa" {
		t.Errorf("action ID %q", resp.ActionId)
	}

	reqs := fake.recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodPost || r.Path != "/payments/"+fakePaymentID+"/captures" {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
	if got := r.Header.Get("Cko-Idempotency-Key"); got != "capture-"+fakePaymentID {
		t.Errorf("Cko-Idempotency-Key %q, want capture-%s", got, fakePaymentID)
	}
	want := map[string]any{"amount": float64(5000), "reference": "ORD-1042"}
	if d := diffKeys(want, decodeJSON(t, "capture body", r.Body)); len(d) > 0 {
		t.Errorf("capture body: %v\nsent: %s", d, r.Body)
	}
	assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Cko-Idempotency-Key", "Content-Length", "Content-Type", "User-Agent")
}

// Proves which payment Dispatch captures, and the reason the button shows
// when there's none. Built from Payment values, so retries with different
// payment IDs on one order are easy to set up.
func TestCaptureTarget(t *testing.T) {
	authorized := Payment{PaymentID: "pay_new", Kind: kindCIT, Status: "Authorized", Method: PaymentMethod{Type: "card"}, Balances: Balances{Authorized: 5000}}
	declined := Payment{PaymentID: "pay_old", Kind: kindCIT, Status: "Declined", Method: PaymentMethod{Type: "card"}}
	mit := Payment{PaymentID: "pay_mit", Kind: kindMIT, Status: "Authorized", Balances: Balances{Authorized: 3000}}
	tests := []struct {
		name       string
		payments   []Payment
		wantID     string
		wantReason string
	}{
		{"authorised CIT", []Payment{authorized}, "pay_new", ""},
		// Decline, then a retry in the same session: the retry is the one to ship.
		{"retry after a decline", []Payment{declined, authorized}, "pay_new", ""},
		// An older attempt that ended (voided) must not hide a newer authorised one.
		{"voided attempt, then a new one", []Payment{{PaymentID: "pay_old", Kind: kindCIT, Status: "Voided", Method: PaymentMethod{Type: "card"}}, authorized}, "pay_new", ""},
		// Dispatch is the first box only. A renewal is never captured from here.
		{"only a renewal is authorised", []Payment{{PaymentID: "pay_cit", Kind: kindCIT, Status: "Captured", Method: PaymentMethod{Type: "card"}}, mit}, "", "First payment captured."},
		{"MB WAY captured at payment", []Payment{{Kind: kindCIT, Status: "Captured", Method: PaymentMethod{Type: "mbway"}}}, "", "Captured at payment. Nothing to dispatch."},
		{"voided", []Payment{{Kind: kindCIT, Status: "Voided", Method: PaymentMethod{Type: "card"}}}, "", "First payment voided. Nothing to capture."},
		{"refunded", []Payment{{Kind: kindCIT, Status: "Refunded", Method: PaymentMethod{Type: "card"}}}, "", "First payment refunded. Nothing to capture."},
		{"declined only", []Payment{declined}, "", "No authorised first payment yet."},
		{"no payments", nil, "", "No authorised first payment yet."},
		// Authorized, but the balances say it's all voided or captured already.
		{"nothing left on the balances", []Payment{{Kind: kindCIT, Status: "Authorized", Balances: Balances{Authorized: 5000, Voided: 5000}}}, "", "No authorised first payment yet."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, reason, ok := captureTarget(Order{ID: "ORD-1042", Payments: tt.payments, NextRenewalWeek: 2})
			if ok != (tt.wantID != "") || p.PaymentID != tt.wantID || reason != tt.wantReason {
				t.Errorf("got %q %q %v, want %q %q", p.PaymentID, reason, ok, tt.wantID, tt.wantReason)
			}
		})
	}
}

// Proves a one-off's reasons say "payment", not "first payment": it has only
// one (Alfredo, 4.1b).
func TestCaptureTargetOneOffWording(t *testing.T) {
	card := PaymentMethod{Type: "card"}
	for status, want := range map[string]string{
		"Captured": "Payment captured.",
		"Voided":   "Payment voided. Nothing to capture.",
		"Refunded": "Payment refunded. Nothing to capture.",
		"Declined": "No authorised payment yet.",
	} {
		o := Order{ID: "ORD-1043", Payments: []Payment{{Kind: kindCIT, Status: status, Method: card}}} // NextRenewalWeek 0: one-off
		if _, reason, _ := captureTarget(o); reason != want {
			t.Errorf("%s: reason %q, want %q", status, reason, want)
		}
	}
}

// Proves the happy path of Dispatch: the authorised CIT is captured with the
// stored amount and the capture-{id} key, then re-read and stored. The fake
// answers the GET with the captured fixture, as the sandbox does once the
// async capture lands.
func TestCaptureHandler(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	cit := addRefPayment(t, fake, "ref_cit_authorized")
	if got := decodePaymentResponse(t, getPayment(h, cit.Id)); !got.Order.CanCapture {
		t.Fatalf("can_capture false before dispatch: %+v", got.Order)
	}
	addRefPayment(t, fake, "ref_cit_captured") // same payment ID, now Captured

	got := decodePaymentResponse(t, postCapture(h, "ORD-1042"))

	calls := fake.captureCalls()
	if len(calls) != 1 {
		t.Fatalf("%d capture calls, want 1", len(calls))
	}
	c := calls[0]
	if c.paymentID != cit.Id || c.req.Amount != 5000 || c.req.Reference != "ORD-1042" || c.key != "capture-"+cit.Id {
		t.Errorf("capture call %+v", c)
	}
	if got.Payment.Status != "Captured" || got.Order.CanCapture || got.Order.CaptureReason != "First payment captured." {
		t.Errorf("after dispatch: status %q, can_capture %v, reason %q", got.Payment.Status, got.Order.CanCapture, got.Order.CaptureReason)
	}
	if stored, _ := store.Order("ORD-1042"); stored.Payments[0].Status != "Captured" {
		t.Errorf("stored status %q", stored.Payments[0].Status)
	}
}

// Proves a double click is safe. The capture is async, so the GET after the
// first click can still say Authorized and the button stays on: the second
// click sends the same key, and Checkout.com captures once.
func TestCaptureDoubleClickSameKey(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	cit := addRefPayment(t, fake, "ref_cit_authorized") // stays Authorized
	decodePaymentResponse(t, getPayment(h, cit.Id))

	decodePaymentResponse(t, postCapture(h, "ORD-1042"))
	decodePaymentResponse(t, postCapture(h, "ORD-1042"))

	calls := fake.captureCalls()
	if len(calls) != 2 || calls[0].key != calls[1].key || calls[0].key != "capture-"+cit.Id {
		t.Errorf("calls %+v, want two with the same capture key", calls)
	}
}

// Proves a one-off card order can be dispatched too (Alfredo, 4.1b): the
// Christmas box ships as well. Only renewals are subscription only.
func TestCaptureOneOffCard(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketOneOff, "FR")
	cit := addRefPayment(t, fake, "ref_cit_authorized")
	decodePaymentResponse(t, getPayment(h, cit.Id))

	decodePaymentResponse(t, postCapture(h, "ORD-1042"))
	if calls := fake.captureCalls(); len(calls) != 1 {
		t.Errorf("%d capture calls, want 1", len(calls))
	}
}

// Proves the refusals: nothing to capture is a 409 with the reason and no API
// call. An unknown order is a 404.
func TestCaptureRefused(t *testing.T) {
	t.Run("MB WAY captured at payment", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1043", basketOneOff, "PT")
		decodePaymentResponse(t, getPayment(h, addRefPayment(t, fake, "ref_oneoff_mbway").Id))

		rec := postCapture(h, "ORD-1043")
		if rec.Code != http.StatusConflict || errorMessage(t, rec).Error != "Captured at payment. Nothing to dispatch." {
			t.Errorf("status %d, body %s", rec.Code, rec.Body)
		}
		if n := len(fake.captureCalls()); n != 0 {
			t.Errorf("%d capture calls, want 0", n)
		}
	})
	t.Run("failed 3DS only", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1042", basketSubscription, "FR")
		decodePaymentResponse(t, getPayment(h, addRefPayment(t, fake, "ref_cit_3ds_failed").Id))

		if rec := postCapture(h, "ORD-1042"); rec.Code != http.StatusConflict {
			t.Errorf("status %d, want 409", rec.Code)
		}
		if n := len(fake.captureCalls()); n != 0 {
			t.Errorf("%d capture calls, want 0", n)
		}
	})
	t.Run("unknown order", func(t *testing.T) {
		h, _, _ := paymentTestApp(t)
		if rec := postCapture(h, "ORD-9999"); rec.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", rec.Code)
		}
	})
}

// Proves a refused capture (say, already voided at Checkout.com) shows the
// status and error codes, and the stored payment doesn't change.
func TestCaptureAPIError(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	decodePaymentResponse(t, getPayment(h, addRefPayment(t, fake, "ref_cit_authorized").Id))
	fake.captureErr = ckoerrors.CheckoutAPIError{
		StatusCode: 403, Status: "403 Forbidden",
		Data: &ckoerrors.ErrorDetails{ErrorCodes: []string{"capture_not_allowed"}},
	}

	rec := postCapture(h, "ORD-1042")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	if e := errorMessage(t, rec); e.CkoStatus != 403 || len(e.ErrorCodes) != 1 || e.ErrorCodes[0] != "capture_not_allowed" {
		t.Errorf("error body %+v", e)
	}
	if o, _ := store.Order("ORD-1042"); o.Payments[0].Status != "Authorized" {
		t.Errorf("stored status %q, want Authorized", o.Payments[0].Status)
	}
}
