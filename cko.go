package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"time"

	checkout "github.com/checkout/checkout-sdk-go/v3"
	"github.com/checkout/checkout-sdk-go/v3/configuration"
	"github.com/checkout/checkout-sdk-go/v3/nas"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
	// Package name is payment_sessions, the folder is sessions. Alias for clarity.
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// PaymentsAPI is every Checkout.com call the app makes. Handlers only use
// this interface, so tests can swap in a fake.
type PaymentsAPI interface {
	CreateSession(ctx context.Context, req sessions.PaymentSessionsRequest) (*sessions.PaymentSessionsResponse, error)
	GetPayment(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentResponse, error)
	GetActions(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentActionsResponse, error)
	Capture(ctx context.Context, paymentID string, req paymentsnas.CaptureRequest, idempotencyKey string) (*payments.CaptureResponse, error)
	Reverse(ctx context.Context, paymentID string, req payments.PaymentReversalRequest, idempotencyKey string) (*payments.PaymentReversalResponse, error)
	RequestMIT(ctx context.Context, req paymentsnas.PaymentRequest, idempotencyKey string) (*paymentsnas.PaymentResponse, error)
}

// sdkPayments is the real PaymentsAPI, on the Checkout.com Go SDK v3.
type sdkPayments struct {
	api *nas.Api
}

const sdkTimeout = 30 * time.Second

// newPaymentsAPI builds the SDK client. env is configuration.Sandbox() in
// main and a fake-server environment in contract tests. Anything that isn't
// a sandbox environment is refused: this demo never points at production.
func newPaymentsAPI(cfg Config, env configuration.Environment, httpClient *http.Client, logger configuration.StdLogger) (PaymentsAPI, error) {
	if env == nil || !env.IsSandbox() {
		return nil, errors.New("build Checkout.com client: sandbox environment only")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: sdkTimeout}
	}
	// No public key: the server never needs it. It only goes to the browser.
	api, err := checkout.Builder().
		StaticKeys().
		WithEnvironment(env).
		WithEnvironmentSubdomain(cfg.Subdomain).
		WithSecretKey(cfg.SecretKey).
		WithHttpClient(httpClient).
		WithLogger(logger).
		Build()
	if err != nil {
		// SDK builder errors name the problem ("Invalid secret key"), never the value.
		return nil, fmt.Errorf("build Checkout.com client: %w", err)
	}
	return &sdkPayments{api: api}, nil
}

var errNoIdempotencyKey = errors.New("missing idempotency key")

// CreateSession creates a Flow payment session. Note: api.PaymentSessions is
// Flow. api.Sessions is a different product (standalone 3DS sessions).
func (s *sdkPayments) CreateSession(ctx context.Context, req sessions.PaymentSessionsRequest) (*sessions.PaymentSessionsResponse, error) {
	resp, err := s.api.PaymentSessions.RequestPaymentSessionsWithContext(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("create payment session %s: %w", req.Reference, err)
	}
	return resp, nil
}

func (s *sdkPayments) GetPayment(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentResponse, error) {
	resp, err := s.api.Payments.GetPaymentDetailsWithContext(ctx, paymentID)
	if err != nil {
		return nil, fmt.Errorf("get payment %s: %w", paymentID, err)
	}
	return resp, nil
}

// GetActions lists the payment's actions, newest first. GET payment details by
// pay_ ID has no actions, so this is where the response code lives (D-027).
func (s *sdkPayments) GetActions(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentActionsResponse, error) {
	resp, err := s.api.Payments.GetPaymentActionsWithContext(ctx, paymentID)
	if err != nil {
		return nil, fmt.Errorf("get payment actions %s: %w", paymentID, err)
	}
	return resp, nil
}

func (s *sdkPayments) Capture(ctx context.Context, paymentID string, req paymentsnas.CaptureRequest, idempotencyKey string) (*payments.CaptureResponse, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("capture %s: %w", paymentID, errNoIdempotencyKey)
	}
	resp, err := s.api.Payments.CapturePaymentWithContext(ctx, paymentID, req, &idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("capture %s: %w", paymentID, err)
	}
	return resp, nil
}

func (s *sdkPayments) Reverse(ctx context.Context, paymentID string, req payments.PaymentReversalRequest, idempotencyKey string) (*payments.PaymentReversalResponse, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("reverse %s: %w", paymentID, errNoIdempotencyKey)
	}
	resp, err := s.api.Payments.ReversePaymentWithContext(ctx, paymentID, &req, &idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("reverse %s: %w", paymentID, err)
	}
	return resp, nil
}

func (s *sdkPayments) RequestMIT(ctx context.Context, req paymentsnas.PaymentRequest, idempotencyKey string) (*paymentsnas.PaymentResponse, error) {
	if idempotencyKey == "" {
		return nil, fmt.Errorf("renewal %s: %w", req.Reference, errNoIdempotencyKey)
	}
	resp, err := s.api.Payments.RequestPaymentWithContext(ctx, req, &idempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("renewal %s: %w", req.Reference, err)
	}
	return resp, nil
}

// safeLogger is the SDK's logger. The SDK v3.8.0 only logs method and path
// (checked 2026-11-04), but we mask anything key-shaped anyway, in case a
// later line ever carries one.
type safeLogger struct {
	l *log.Logger
}

func newSafeLogger(w io.Writer) *safeLogger {
	return &safeLogger{l: log.New(w, "cko-sdk: ", log.LstdFlags)}
}

var keyShaped = regexp.MustCompile(`\b(sk|pk)_[A-Za-z0-9_*#$=-]+|(?i)bearer\s+\S+`)

func maskKeys(s string) string { return keyShaped.ReplaceAllString(s, "[redacted]") }

func (s *safeLogger) Print(v ...interface{})            { s.l.Print(maskKeys(fmt.Sprint(v...))) }
func (s *safeLogger) Printf(f string, v ...interface{}) { s.l.Print(maskKeys(fmt.Sprintf(f, v...))) }
func (s *safeLogger) Println(v ...interface{})          { s.l.Print(maskKeys(fmt.Sprintln(v...))) }
func (s *safeLogger) Fatal(v ...interface{})            { s.l.Fatal(maskKeys(fmt.Sprint(v...))) }
func (s *safeLogger) Fatalf(f string, v ...interface{}) { s.l.Fatal(maskKeys(fmt.Sprintf(f, v...))) }
func (s *safeLogger) Fatalln(v ...interface{})          { s.l.Fatal(maskKeys(fmt.Sprintln(v...))) }
func (s *safeLogger) Panic(v ...interface{})            { s.l.Panic(maskKeys(fmt.Sprint(v...))) }
func (s *safeLogger) Panicf(f string, v ...interface{}) { s.l.Panic(maskKeys(fmt.Sprintf(f, v...))) }
func (s *safeLogger) Panicln(v ...interface{})          { s.l.Panic(maskKeys(fmt.Sprintln(v...))) }
