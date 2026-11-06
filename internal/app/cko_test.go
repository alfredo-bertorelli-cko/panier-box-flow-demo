package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/configuration"
	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// sdkFakeSecretKey is obviously fake but matches the SDK's key pattern
// (sk_sbox_ plus 27 characters from a-z and 2-7), so Build() accepts it.
var sdkFakeSecretKey = "sk_sbox_" + strings.Repeat("fake", 6) + "fak"

const fakePaymentID = "pay_fakeaaaaaaaaaaaaaaaaaaaaaa"

// sdkTestConfig is the fake config with an SDK-valid secret key.
func sdkTestConfig(t *testing.T) Config {
	t.Helper()
	env := validEnv()
	env[envSecretKey] = sdkFakeSecretKey
	cfg, err := LoadConfig(fakeGetenv(env))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}

// recordedRequest is what the fake Checkout.com server saw.
type recordedRequest struct {
	Method string
	Path   string
	Host   string
	Header http.Header
	Body   []byte
}

// fakeCheckout is an httptest server standing in for Checkout.com. It records
// every request and answers with a fixed status and body.
type fakeCheckout struct {
	srv      *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	status   int
	body     string
}

func newFakeCheckout(t *testing.T, status int, body string) *fakeCheckout {
	t.Helper()
	f := &fakeCheckout{status: status, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{r.Method, r.URL.Path, r.Host, r.Header.Clone(), b})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCheckout) recorded() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

// client builds the real SDK client pointed at this fake server.
//
// The SDK prepends the subdomain to the host of any environment, so it calls
// http://fakesub1.api.test. That name doesn't resolve, so our HTTP client
// dials the fake server for every connection. The SDK's own code is
// unchanged, and the test can check the subdomain reached the Host.
func (f *fakeCheckout) client(t *testing.T, logger configuration.StdLogger) PaymentsAPI {
	t.Helper()
	addr := f.srv.Listener.Addr().String()
	httpClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
	if logger == nil {
		logger = newSafeLogger(io.Discard)
	}
	api, err := newPaymentsAPI(sdkTestConfig(t), testEnvironment(true), httpClient, logger)
	if err != nil {
		t.Fatalf("newPaymentsAPI: %v", err)
	}
	return api
}

func testEnvironment(sandbox bool) configuration.Environment {
	return configuration.NewEnvironment(
		"http://api.test", "http://access.api.test/connect/token", "http://files.api.test",
		"http://transfers.api.test", "http://balances.api.test", "http://forward.api.test",
		"http://identity.api.test", sandbox)
}

// Proves the client builds with fake keys and the real sandbox environment.
func TestNewPaymentsAPIBuilds(t *testing.T) {
	if _, err := newPaymentsAPI(sdkTestConfig(t), configuration.Sandbox(), nil, newSafeLogger(io.Discard)); err != nil {
		t.Fatalf("newPaymentsAPI: %v", err)
	}
}

// Proves a non-sandbox environment is refused, Production included.
func TestNewPaymentsAPIRefusesProduction(t *testing.T) {
	for name, env := range map[string]configuration.Environment{
		"production":       configuration.Production(),
		"custom, not sbox": testEnvironment(false),
		"nil":              nil,
	} {
		if _, err := newPaymentsAPI(sdkTestConfig(t), env, nil, newSafeLogger(io.Discard)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

// Proves a secret key the SDK rejects gives an error without the value.
func TestNewPaymentsAPIBadKeyHidesValue(t *testing.T) {
	cfg := sdkTestConfig(t)
	cfg.SecretKey = "sk_sbox_fake_WRONG_shape_123"
	_, err := newPaymentsAPI(cfg, configuration.Sandbox(), nil, newSafeLogger(io.Discard))
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), cfg.SecretKey) {
		t.Error("error contains the secret key")
	}
}

// Contract test: GetPayment sends exactly the request we expect. Path, host
// with subdomain, auth, user agent, no body, and no headers we didn't expect.
func TestGetPaymentContract(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusOK, `{"id":"`+fakePaymentID+`","status":"Authorized"}`)
	api := fake.client(t, nil)

	resp, err := api.GetPayment(context.Background(), fakePaymentID)
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if resp.Id != fakePaymentID || resp.Status != "Authorized" {
		t.Errorf("response not mapped: id %q status %q", resp.Id, resp.Status)
	}

	reqs := fake.recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodGet || r.Path != "/payments/"+fakePaymentID {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
	if r.Host != "fakesub1.api.test" {
		t.Errorf("Host %q, want the subdomain on it: fakesub1.api.test", r.Host)
	}
	// Compare only. Never print the header: it carries the key.
	if r.Header.Get("Authorization") != "Bearer "+sdkFakeSecretKey {
		t.Error("Authorization header is not Bearer + secret key")
	}
	if ua := r.Header.Get("User-Agent"); ua != "checkout-sdk-go/3.8.0" {
		t.Errorf("User-Agent %q, want checkout-sdk-go/3.8.0", ua)
	}
	if len(r.Body) != 0 {
		t.Errorf("GET sent a body of %d bytes: %q", len(r.Body), r.Body)
	}
	assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Content-Type", "User-Agent")
}

// Contract test: GetActions calls GET /payments/{id}/actions and decodes the
// bare JSON array the API returns (swagger PaymentActionsResponse is an
// array, not an object). If the SDK ever stopped unwrapping it, the response
// code would go silently empty.
func TestGetActionsContract(t *testing.T) {
	body := `[
		{"id":"act_fakecaptureaaaaaaaaaaaaaaaa","type":"Capture","amount":5000,"approved":true,"response_code":"10000","response_summary":"Approved"},
		{"id":"act_fakeauthaaaaaaaaaaaaaaaaaa","type":"Authorization","amount":5000,"approved":true,"response_code":"10000","response_summary":"Approved"}
	]`
	fake := newFakeCheckout(t, http.StatusOK, body)
	api := fake.client(t, nil)

	resp, err := api.GetActions(context.Background(), fakePaymentID)
	if err != nil {
		t.Fatalf("GetActions: %v", err)
	}
	if len(resp.Actions) != 2 || resp.Actions[1].Type != payments.AuthorizationYes || resp.Actions[1].ResponseCode != "10000" {
		t.Errorf("actions not decoded: %+v", resp.Actions)
	}

	reqs := fake.recorded()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodGet || r.Path != "/payments/"+fakePaymentID+"/actions" {
		t.Errorf("got %s %s", r.Method, r.Path)
	}
	if r.Host != "fakesub1.api.test" {
		t.Errorf("Host %q, want fakesub1.api.test", r.Host)
	}
	if len(r.Body) != 0 {
		t.Errorf("GET sent a body of %d bytes", len(r.Body))
	}
	assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Content-Type", "User-Agent")
}

// assertOnlyHeaders flags any header we didn't expect. Names only: values may hold keys.
func assertOnlyHeaders(t *testing.T, h http.Header, allowed ...string) {
	t.Helper()
	for name := range h {
		if !slices.Contains(allowed, name) {
			t.Errorf("unexpected header %q", name)
		}
	}
}

// Proves the telemetry header from the second request holds only IDs and a
// duration, never a key.
func TestTelemetryHeaderHasNoKey(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusOK, `{"id":"`+fakePaymentID+`"}`)
	api := fake.client(t, nil)
	for range 2 {
		if _, err := api.GetPayment(context.Background(), fakePaymentID); err != nil {
			t.Fatal(err)
		}
	}
	tel := fake.recorded()[1].Header.Get("Cko-Sdk-Telemetry")
	if tel == "" {
		t.Fatal("no telemetry header on the second request")
	}
	if strings.Contains(tel, "sk_") || strings.Contains(tel, "Bearer") {
		t.Error("telemetry header carries key material")
	}
	for _, want := range []string{"prev_request_id", "request_id", "prev_request_duration"} {
		if !strings.Contains(tel, want) {
			t.Errorf("telemetry %q missing %q", tel, want)
		}
	}
}

// Proves an API error keeps the status and error codes and carries no key.
func TestAPIErrorKeepsCodes(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusUnprocessableEntity,
		`{"request_id":"req_fake","error_type":"request_invalid","error_codes":["payment_id_invalid"]}`)
	api := fake.client(t, nil)

	_, err := api.GetPayment(context.Background(), fakePaymentID)
	if err == nil {
		t.Fatal("want error")
	}
	var apiErr ckoerrors.CheckoutAPIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T is not a CheckoutAPIError", err)
	}
	if apiErr.StatusCode != 422 || apiErr.Data == nil || !slices.Contains(apiErr.Data.ErrorCodes, "payment_id_invalid") {
		t.Errorf("status %d, data %+v", apiErr.StatusCode, apiErr.Data)
	}
	if !strings.HasPrefix(err.Error(), "get payment "+fakePaymentID) {
		t.Errorf("error lacks context: %q", err.Error())
	}
	if strings.Contains(err.Error(), "sk_") {
		t.Error("error carries key material")
	}
}

// Proves calls that change money refuse an empty idempotency key, and send nothing.
func TestEmptyIdempotencyKeyIsRefused(t *testing.T) {
	fake := newFakeCheckout(t, http.StatusAccepted, `{}`)
	api := fake.client(t, nil)
	ctx := context.Background()

	_, errCapture := api.Capture(ctx, fakePaymentID, paymentsnas.CaptureRequest{}, "")
	_, errReverse := api.Reverse(ctx, fakePaymentID, payments.PaymentReversalRequest{}, "")
	_, errMIT := api.RequestMIT(ctx, paymentsnas.PaymentRequest{Reference: "ORD-1042-W2"}, "")
	for name, err := range map[string]error{"capture": errCapture, "reverse": errReverse, "renewal": errMIT} {
		if !errors.Is(err, errNoIdempotencyKey) {
			t.Errorf("%s: got %v, want errNoIdempotencyKey", name, err)
		}
	}
	if n := len(fake.recorded()); n != 0 {
		t.Errorf("%d requests reached the server, want 0", n)
	}
}

// Proves what the SDK really logs for a call: method and path, no key.
func TestSDKLogLine(t *testing.T) {
	var buf bytes.Buffer
	fake := newFakeCheckout(t, http.StatusOK, `{"id":"`+fakePaymentID+`"}`)
	api := fake.client(t, newSafeLogger(&buf))
	if _, err := api.GetPayment(context.Background(), fakePaymentID); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "cko-sdk: ") || !strings.Contains(out, "GET: /payments/"+fakePaymentID) {
		t.Errorf("log line %q, want prefix and method plus path", out)
	}
	if strings.Contains(out, sdkFakeSecretKey) || strings.Contains(out, "Bearer") {
		t.Error("log line carries key material")
	}
}

// Proves the safe logger masks key-shaped text, a second line of defence.
func TestMaskKeys(t *testing.T) {
	tests := []struct{ in, want string }{
		{"key " + sdkFakeSecretKey + " end", "key [redacted] end"},
		{"pk_sbox_fakefakefake", "[redacted]"},
		{"Authorization: Bearer abc.def", "Authorization: [redacted]"},
		{"bearer xyz", "[redacted]"},
		{"GET: /payments/" + fakePaymentID, "GET: /payments/" + fakePaymentID},
		{"task_runner and desk_lamp", "task_runner and desk_lamp"}, // \b stops mid-word matches
	}
	for _, tt := range tests {
		if got := maskKeys(tt.in); got != tt.want {
			t.Errorf("maskKeys(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
