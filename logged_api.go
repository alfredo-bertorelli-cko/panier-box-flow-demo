package main

import (
	"context"
	"errors"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// loggedPayments puts every action Panier Box sends to Checkout.com on the
// live log (D-038): create a session, capture, reverse, renew. It wraps the
// real client in main, so no handler can call Checkout.com without the log
// seeing it. Reads (GET payment, GET actions) pass through unlogged: they
// show up as a read line only when they change a status (syncPayment).
//
// Each line has the method, the path, Checkout.com's HTTP status, IDs, and on
// a refusal the error codes. Never a header, a body or a key.
type loggedPayments struct {
	next   PaymentsAPI
	events *eventLog
	now    func() time.Time
}

func newLoggedPayments(next PaymentsAPI, events *eventLog, now func() time.Time) *loggedPayments {
	return &loggedPayments{next: next, events: events, now: now}
}

// add fills in what every action line shares, and on a failure what
// Checkout.com said: its HTTP status and error codes, or "no response" when
// the call never got an answer (a timeout, the network).
func (l *loggedPayments) add(e logEntry, err error) {
	e.Time = l.now()
	e.Kind = entryAction
	e.Dir = dirOut
	if err != nil {
		var apiErr ckoerrors.CheckoutAPIError
		if errors.As(err, &apiErr) {
			e.HTTPStatus = apiErr.StatusCode
			if apiErr.Data != nil {
				e.ErrorCodes = apiErr.Data.ErrorCodes
			}
		} else {
			e.Detail = "no response"
		}
	}
	l.events.Add(e)
}

// orderOf is the order a reference belongs to, so each line lands on its
// order's page. ORD-1042-W2 is on ORD-1042.
func orderOf(ref string) string {
	id, _ := OrderIDFromReference(ref)
	return id
}

func (l *loggedPayments) CreateSession(ctx context.Context, req sessions.PaymentSessionsRequest) (*sessions.PaymentSessionsResponse, error) {
	resp, err := l.next.CreateSession(ctx, req)
	e := logEntry{Text: "POST /payment-sessions", OrderID: orderOf(req.Reference)}
	if err == nil {
		// The session ID only. The session token and secret stay out: Flow
		// needs them, the log doesn't.
		e.HTTPStatus, e.Detail = resp.HttpMetadata.StatusCode, resp.Id
	}
	l.add(e, err)
	return resp, err
}

func (l *loggedPayments) GetPayment(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentResponse, error) {
	return l.next.GetPayment(ctx, paymentID)
}

func (l *loggedPayments) GetActions(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentActionsResponse, error) {
	return l.next.GetActions(ctx, paymentID)
}

func (l *loggedPayments) Capture(ctx context.Context, paymentID string, req paymentsnas.CaptureRequest, idempotencyKey string) (*payments.CaptureResponse, error) {
	resp, err := l.next.Capture(ctx, paymentID, req, idempotencyKey)
	e := logEntry{Text: "POST /payments/" + paymentID + "/captures", PaymentID: paymentID, OrderID: orderOf(req.Reference)}
	if err == nil {
		e.HTTPStatus = resp.HttpMetadata.StatusCode
	}
	l.add(e, err)
	return resp, err
}

func (l *loggedPayments) Reverse(ctx context.Context, paymentID string, req payments.PaymentReversalRequest, idempotencyKey string) (*payments.PaymentReversalResponse, error) {
	resp, err := l.next.Reverse(ctx, paymentID, req, idempotencyKey)
	e := logEntry{Text: "POST /payments/" + paymentID + "/reversals", PaymentID: paymentID, OrderID: orderOf(req.Reference)}
	if err == nil {
		// Void or Refund: Checkout.com picks from the payment's state (UC-9).
		// 200 instead of 202 means it was already reversed.
		e.HTTPStatus, e.Detail = resp.HttpMetadata.StatusCode, resp.ActionType
	}
	l.add(e, err)
	return resp, err
}

func (l *loggedPayments) RequestMIT(ctx context.Context, req paymentsnas.PaymentRequest, idempotencyKey string) (*paymentsnas.PaymentResponse, error) {
	resp, err := l.next.RequestMIT(ctx, req, idempotencyKey)
	e := logEntry{Text: "POST /payments", Detail: "MIT " + req.Reference, OrderID: orderOf(req.Reference)}
	if err == nil {
		// A declined MIT is still a 201: the decline is in the status.
		e.HTTPStatus, e.PaymentID, e.Status = resp.HttpMetadata.StatusCode, resp.Id, string(resp.Status)
	}
	l.add(e, err)
	return resp, err
}

var _ PaymentsAPI = (*loggedPayments)(nil)
