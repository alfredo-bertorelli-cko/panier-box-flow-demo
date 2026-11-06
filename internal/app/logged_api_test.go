package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// The wrapper is tested on its own, around the fake: each call in, one line
// (or none) out. Handlers are tested elsewhere, so here the question is only
// "what does the log say about this call".

func loggedTestAPI() (*loggedPayments, *fakePayments, *eventLog) {
	fake := &fakePayments{
		details: map[string]*paymentsnas.GetPaymentResponse{},
		actions: map[string]*paymentsnas.GetPaymentActionsResponse{},
	}
	events := newEventLog()
	return newLoggedPayments(fake, events, func() time.Time { return fixedNow }), fake, events
}

// onlyLine fails unless the log has exactly one line, and returns it.
func onlyLine(t *testing.T, l *eventLog) logEntry {
	t.Helper()
	got := l.Recent()
	if len(got) != 1 {
		t.Fatalf("%d lines, want 1: %+v", len(got), got)
	}
	e := got[0]
	if e.Kind != entryAction || e.Dir != dirOut || !e.Time.Equal(fixedNow) {
		t.Errorf("kind %q dir %q time %v, want an outbound action now", e.Kind, e.Dir, e.Time)
	}
	return e
}

// Proves each action gets one line with its path, Checkout.com's status, its
// IDs and its order, which is what the demo log shows.
func TestLoggedActions(t *testing.T) {
	ctx := context.Background()
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"

	t.Run("session", func(t *testing.T) {
		api, _, events := loggedTestAPI()
		if _, err := api.CreateSession(ctx, sessions.PaymentSessionsRequest{Reference: "ORD-1042"}); err != nil {
			t.Fatal(err)
		}
		e := onlyLine(t, events)
		if e.Text != "POST /payment-sessions" || e.HTTPStatus != 201 || e.Detail != "ps_fake" || e.OrderID != "ORD-1042" {
			t.Errorf("%+v", e)
		}
	})
	t.Run("capture", func(t *testing.T) {
		api, _, events := loggedTestAPI()
		if _, err := api.Capture(ctx, pay, paymentsnas.CaptureRequest{Amount: 5000, Reference: "ORD-1042"}, "capture-"+pay); err != nil {
			t.Fatal(err)
		}
		e := onlyLine(t, events)
		if e.Text != "POST /payments/"+pay+"/captures" || e.HTTPStatus != 202 || e.PaymentID != pay || e.OrderID != "ORD-1042" {
			t.Errorf("%+v", e)
		}
	})
	t.Run("reverse", func(t *testing.T) {
		api, _, events := loggedTestAPI()
		if _, err := api.Reverse(ctx, pay, payments.PaymentReversalRequest{Reference: "ORD-1042-W3"}, "reverse-"+pay); err != nil {
			t.Fatal(err)
		}
		// A renewal's reference still lands on its order.
		e := onlyLine(t, events)
		if e.Text != "POST /payments/"+pay+"/reversals" || e.HTTPStatus != 202 || e.Detail != "Void" || e.OrderID != "ORD-1042" {
			t.Errorf("%+v", e)
		}
	})
	t.Run("renewal", func(t *testing.T) {
		api, fake, events := loggedTestAPI()
		fake.mitResp = &paymentsnas.PaymentResponse{Id: "pay_fakemitaaaaaaaaaaaaaaaaaaa", Status: payments.Declined}
		fake.mitResp.HttpMetadata.StatusCode = 201
		if _, err := api.RequestMIT(ctx, paymentsnas.PaymentRequest{Reference: "ORD-1042-W2"}, "renewal-ORD-1042-W2"); err != nil {
			t.Fatal(err)
		}
		// A declined MIT is a 201 with a Declined status, so both show.
		e := onlyLine(t, events)
		if e.Text != "POST /payments" || e.HTTPStatus != 201 || e.Detail != "MIT ORD-1042-W2" ||
			e.Status != "Declined" || e.PaymentID != "pay_fakemitaaaaaaaaaaaaaaaaaaa" || e.OrderID != "ORD-1042" {
			t.Errorf("%+v", e)
		}
	})
}

// Proves a refusal is logged with Checkout.com's status and error codes, and
// a call that never got an answer says so instead of showing a status.
func TestLoggedActionFailures(t *testing.T) {
	ctx := context.Background()
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"

	api, fake, events := loggedTestAPI()
	fake.captureErr = ckoerrors.CheckoutAPIError{
		StatusCode: 422, Status: "422 Unprocessable Entity",
		Data: &ckoerrors.ErrorDetails{ErrorCodes: []string{"amount_invalid"}},
	}
	if _, err := api.Capture(ctx, pay, paymentsnas.CaptureRequest{Reference: "ORD-1042"}, "capture-"+pay); err == nil {
		t.Fatal("want the fake's error back")
	}
	e := onlyLine(t, events)
	if e.HTTPStatus != 422 || !slices.Equal(e.ErrorCodes, []string{"amount_invalid"}) || e.OrderID != "ORD-1042" {
		t.Errorf("refusal %+v", e)
	}

	api, fake, events = loggedTestAPI()
	fake.reverseErr = errors.New("dial tcp: i/o timeout")
	if _, err := api.Reverse(ctx, pay, payments.PaymentReversalRequest{Reference: "ORD-1042"}, "reverse-"+pay); err == nil {
		t.Fatal("want the fake's error back")
	}
	if e := onlyLine(t, events); e.HTTPStatus != 0 || e.Detail != "no response" {
		t.Errorf("no answer %+v", e)
	}
}

// Proves reads are never logged by the wrapper: polling makes no noise.
// syncPayment decides when a read is worth a line.
func TestLoggedReadsPassThrough(t *testing.T) {
	api, fake, events := loggedTestAPI()
	addRefPayment(t, fake, "ref_cit_authorized")
	ctx := context.Background()
	if _, err := api.GetPayment(ctx, "pay_fakecitaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetActions(ctx, "pay_fakecitaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if got := events.Recent(); len(got) != 0 {
		t.Errorf("reads logged: %+v", got)
	}
}

// Proves the idempotency key never reaches the log. It's not a secret, but
// the log has no reason to carry it, and this is the line a header or key
// would cross if a future field ever tried.
func TestLoggedActionsCarryNoKeys(t *testing.T) {
	api, fake, events := loggedTestAPI()
	fake.mitResp = &paymentsnas.PaymentResponse{Id: "pay_fakemitaaaaaaaaaaaaaaaaaaa", Status: payments.Authorized}
	ctx := context.Background()
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"
	api.CreateSession(ctx, sessions.PaymentSessionsRequest{Reference: "ORD-1042"})
	api.Capture(ctx, pay, paymentsnas.CaptureRequest{Reference: "ORD-1042"}, "capture-"+pay)
	api.Reverse(ctx, pay, payments.PaymentReversalRequest{Reference: "ORD-1042"}, "reverse-"+pay)
	api.RequestMIT(ctx, paymentsnas.PaymentRequest{Reference: "ORD-1042-W2"}, "renewal-ORD-1042-W2")

	b, err := json.Marshal(events.Recent())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"capture-", "reverse-", "renewal-", "sk_", "Bearer", "Authorization"} {
		if strings.Contains(string(b), bad) {
			t.Errorf("log contains %q: %s", bad, b)
		}
	}
	if n := len(events.Recent()); n != 4 {
		t.Errorf("%d lines, want 4", n)
	}
}
