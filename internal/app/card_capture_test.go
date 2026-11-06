package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
)

func postCardCapture(h http.Handler, paymentID string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/payments/"+paymentID+"/capture", nil))
	return rec
}

// Contract test: capturing a renewal sends its own reference (ORD-1042-W2),
// what's left to capture, and capture-{id}.
func TestCardCaptureContract(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusAccepted, `{"action_id":"act_fakecaptureaaaaaaaaaaaaaaaa","reference":"ORD-1042-W2"}`)
	api := fake.client(t, nil)
	mit := Payment{PaymentID: fakePaymentID, Kind: kindMIT, Reference: "ORD-1042-W2", Status: "Authorized", Balances: Balances{Authorized: 3000}}
	key, _ := CaptureKey(mit.PaymentID)

	if _, err := api.Capture(context.Background(), mit.PaymentID, buildCaptureRequest(mit), key); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	r := fake.recorded()[0]
	if r.Path != "/payments/"+fakePaymentID+"/captures" || r.Header.Get("Cko-Idempotency-Key") != "capture-"+fakePaymentID {
		t.Errorf("path %s, key %q", r.Path, r.Header.Get("Cko-Idempotency-Key"))
	}
	want := map[string]any{"amount": float64(3000), "reference": "ORD-1042-W2"}
	if d := diffKeys(want, decodeJSON(t, "capture body", r.Body)); len(d) > 0 {
		t.Errorf("capture body: %v\nsent: %s", d, r.Body)
	}
}

// Proves which cards get a Capture button: only Authorized with something
// left. Declined, voided, refunded, captured and MB WAY get none (Alfredo).
func TestCanCapture(t *testing.T) {
	tests := []struct {
		name string
		p    Payment
		want bool
	}{
		{"authorised renewal", Payment{Status: "Authorized", Balances: Balances{Authorized: 3000}}, true},
		{"authorised, nothing left", Payment{Status: "Authorized", Balances: Balances{Authorized: 3000, Voided: 3000}}, false},
		{"captured", Payment{Status: "Captured", Balances: Balances{Authorized: 5000, Captured: 5000}}, false},
		{"MB WAY captured at payment", Payment{Status: "Captured", Method: PaymentMethod{Type: "mbway"}, Balances: Balances{Authorized: 4500, Captured: 4500}}, false},
		{"declined", Payment{Status: "Declined"}, false},
		{"voided", Payment{Status: "Voided", Balances: Balances{Authorized: 3000, Voided: 3000}}, false},
		{"refunded", Payment{Status: "Refunded", Balances: Balances{Authorized: 5000, Captured: 5000, Refunded: 5000}}, false},
	}
	for _, tt := range tests {
		if got := canCapture(tt.p); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Proves a renewal can be captured from its card: one call on the MIT with
// its reference, then re-read and stored. The first box is untouched.
func TestCardCaptureRenewal(t *testing.T) {
	h, fake, store, mit := renewalApp(t)
	got := decodePaymentResponse(t, postRenewal(h, "ORD-1042"))
	// Both are Authorized here (the first box isn't dispatched yet), so both
	// cards offer Capture.
	for _, p := range got.Order.Payments {
		if !p.CanCapture {
			t.Errorf("%s: can_capture false before, want true", p.PaymentID)
		}
	}
	captured := addRefPayment(t, fake, "ref_mit_cobadged_cb")
	captured.Status = "Captured"
	captured.Balances.TotalCaptured = 3000

	got = decodePaymentResponse(t, postCardCapture(h, mit.Id))

	calls := fake.captureCalls()
	if len(calls) != 1 || calls[0].paymentID != mit.Id || calls[0].req.Reference != "ORD-1042-W2" || calls[0].req.Amount != 3000 || calls[0].key != "capture-"+mit.Id {
		t.Fatalf("capture calls %+v", calls)
	}
	if got.Payment.Status != "Captured" {
		t.Errorf("status %q", got.Payment.Status)
	}
	for _, p := range got.Order.Payments {
		if p.PaymentID == mit.Id && p.CanCapture {
			t.Error("captured renewal still offers Capture")
		}
	}
	if _, cit, _ := store.OrderWithPayment("pay_fakecitaaaaaaaaaaaaaaaaaaa"); cit.Status != "Authorized" {
		t.Errorf("first box status %q, want it untouched", cit.Status)
	}
}

// Proves Dispatch and the first box's card button are one capture: both send
// capture-{the CIT's id}, so Checkout.com captures once.
func TestDispatchAndCardCaptureShareKey(t *testing.T) {
	h, fake, store := paymentTestApp(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	cit := addRefPayment(t, fake, "ref_cit_authorized") // stays Authorized: async capture
	decodePaymentResponse(t, getPayment(h, cit.Id))

	decodePaymentResponse(t, postCapture(h, "ORD-1042"))
	decodePaymentResponse(t, postCardCapture(h, cit.Id))

	calls := fake.captureCalls()
	if len(calls) != 2 || calls[0].key != calls[1].key || calls[0].req.Amount != calls[1].req.Amount || calls[0].req.Reference != calls[1].req.Reference {
		t.Errorf("calls %+v, want the same request and key twice", calls)
	}
}

// Proves the refusals, each with no API call.
func TestCardCaptureRefused(t *testing.T) {
	t.Run("MB WAY, already captured", func(t *testing.T) {
		h, fake, store := paymentTestApp(t)
		createOrder(t, store, "ORD-1043", basketOneOff, "PT")
		mb := addRefPayment(t, fake, "ref_oneoff_mbway")
		decodePaymentResponse(t, getPayment(h, mb.Id))
		if rec := postCardCapture(h, mb.Id); rec.Code != http.StatusConflict {
			t.Errorf("status %d, want 409", rec.Code)
		}
		if n := len(fake.captureCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
	t.Run("not on our orders", func(t *testing.T) {
		h, fake, _ := paymentTestApp(t)
		if rec := postCardCapture(h, fakePaymentID); rec.Code != http.StatusNotFound {
			t.Errorf("status %d, want 404", rec.Code)
		}
		if n := len(fake.captureCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
	t.Run("bad ID", func(t *testing.T) {
		h, fake, _ := paymentTestApp(t)
		if rec := postCardCapture(h, "pay_short"); rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
		if n := len(fake.captureCalls()); n != 0 {
			t.Errorf("%d calls", n)
		}
	})
}

// Proves a refused capture shows status and codes and changes nothing.
func TestCardCaptureAPIError(t *testing.T) {
	h, fake, store, mit := renewalApp(t)
	decodePaymentResponse(t, postRenewal(h, "ORD-1042"))
	fake.captureErr = ckoerrors.CheckoutAPIError{StatusCode: 403, Status: "403 Forbidden"}

	rec := postCardCapture(h, mit.Id)
	if rec.Code != http.StatusBadGateway || errorMessage(t, rec).CkoStatus != 403 {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if _, p, _ := store.OrderWithPayment(mit.Id); p.Status != "Authorized" {
		t.Errorf("stored status %q", p.Status)
	}
}
