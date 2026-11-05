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

	// GET payment and GET actions: answers by payment ID, or an error.
	details    map[string]*paymentsnas.GetPaymentResponse
	actions    map[string]*paymentsnas.GetPaymentActionsResponse
	getErr     error
	actionsErr error
	getCalls   int
	getHook    func() // runs at the start of GetPayment, unlocked: lets a test hold the GET

	captures   []captureCall
	captureErr error

	mits    []mitCall
	onMIT   func() // runs inside RequestMIT, unlocked: lets a test overlap two requests
	mitResp *paymentsnas.PaymentResponse
	mitErr  error

	reverses   []reverseCall
	reverseErr error
}

// mitCall is one RequestMIT the fake received.
type mitCall struct {
	req paymentsnas.PaymentRequest
	key string
}

// captureCall is one Capture the fake received.
type captureCall struct {
	paymentID string
	req       paymentsnas.CaptureRequest
	key       string
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

func (f *fakePayments) GetPayment(_ context.Context, id string) (*paymentsnas.GetPaymentResponse, error) {
	f.mu.Lock()
	hook := f.getHook
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if d, ok := f.details[id]; ok {
		return d, nil
	}
	return nil, errNotInFake
}

func (f *fakePayments) GetActions(_ context.Context, id string) (*paymentsnas.GetPaymentActionsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.actionsErr != nil {
		return nil, f.actionsErr
	}
	if a, ok := f.actions[id]; ok {
		return a, nil
	}
	// A payment with no actions fixture: an empty list, like no actions yet.
	return &paymentsnas.GetPaymentActionsResponse{}, nil
}

func (f *fakePayments) getPaymentCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls
}

func (f *fakePayments) Capture(_ context.Context, id string, req paymentsnas.CaptureRequest, key string) (*payments.CaptureResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures = append(f.captures, captureCall{id, req, key})
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	return &payments.CaptureResponse{ActionId: "act_fakecaptureaaaaaaaaaaaaaaaa", Reference: req.Reference}, nil
}

func (f *fakePayments) captureCalls() []captureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]captureCall(nil), f.captures...)
}

func (f *fakePayments) Reverse(_ context.Context, id string, req payments.PaymentReversalRequest, key string) (*payments.PaymentReversalResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reverses = append(f.reverses, reverseCall{id, req, key})
	if f.reverseErr != nil {
		return nil, f.reverseErr
	}
	return &payments.PaymentReversalResponse{ActionId: "act_fakereverseaaaaaaaaaaaaaaaa", ActionType: "Void"}, nil
}

func (f *fakePayments) reverseCalls() []reverseCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reverseCall(nil), f.reverses...)
}

// reverseCall is one Reverse the fake received.
type reverseCall struct {
	paymentID string
	req       payments.PaymentReversalRequest
	key       string
}

func (f *fakePayments) RequestMIT(_ context.Context, req paymentsnas.PaymentRequest, key string) (*paymentsnas.PaymentResponse, error) {
	f.mu.Lock()
	f.mits = append(f.mits, mitCall{req, key})
	hook := f.onMIT
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mitErr != nil {
		return nil, f.mitErr
	}
	if f.mitResp == nil {
		return nil, errNotInFake
	}
	return f.mitResp, nil
}

func (f *fakePayments) mitCalls() []mitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mitCall(nil), f.mits...)
}
