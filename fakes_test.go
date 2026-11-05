package main

import (
	"context"
	"errors"
	"sync"

	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// Compile-time checks: both the real client and the fake satisfy the interface.
var (
	_ PaymentsAPI = (*sdkPayments)(nil)
	_ PaymentsAPI = (*fakePayments)(nil)
)

// fakePayments is a test double for PaymentsAPI. It records every call and
// returns whatever the test configured. No network, no SDK.
type fakePayments struct {
	mu          sync.Mutex
	sessionReqs []sessions.PaymentSessionsRequest
	sessionRaw  []byte // raw response body to hand back, as the SDK would
	sessionErr  error
}

func (f *fakePayments) CreateSession(_ context.Context, req sessions.PaymentSessionsRequest) (*sessions.PaymentSessionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionReqs = append(f.sessionReqs, req)
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	resp := &sessions.PaymentSessionsResponse{Id: "ps_fake"}
	resp.HttpMetadata.StatusCode = 201
	resp.HttpMetadata.ResponseBody = f.sessionRaw
	return resp, nil
}

func (f *fakePayments) sessionCalls() []sessions.PaymentSessionsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sessions.PaymentSessionsRequest(nil), f.sessionReqs...)
}

var errNotInFake = errors.New("not implemented in this fake yet")

func (f *fakePayments) GetPayment(context.Context, string) (*paymentsnas.GetPaymentResponse, error) {
	return nil, errNotInFake
}

func (f *fakePayments) GetActions(context.Context, string) (*paymentsnas.GetPaymentActionsResponse, error) {
	return nil, errNotInFake
}

func (f *fakePayments) Capture(context.Context, string, paymentsnas.CaptureRequest, string) (*payments.CaptureResponse, error) {
	return nil, errNotInFake
}

func (f *fakePayments) Reverse(context.Context, string, payments.PaymentReversalRequest, string) (*payments.PaymentReversalResponse, error) {
	return nil, errNotInFake
}

func (f *fakePayments) RequestMIT(context.Context, paymentsnas.PaymentRequest, string) (*paymentsnas.PaymentResponse, error) {
	return nil, errNotInFake
}
