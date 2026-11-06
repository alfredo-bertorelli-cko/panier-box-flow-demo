package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
)

// sessionTestApp is an app with a fake PaymentsAPI and a store in a temp
// folder. Returns the router, the fake, the store and its file path.
func sessionTestApp(t *testing.T) (http.Handler, *fakePayments, *Store, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "session_response.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, path := newTestStore(t)
	fake := &fakePayments{sessionRaw: raw}
	a := &app{
		cfg:   sdkTestConfig(t),
		store: store,
		api:   fake,
		ids:   newOrderIDGen(nil),
		now:   func() time.Time { return fixedNow },
	}
	return newRouter(a, fstest.MapFS{}), fake, store, path
}

func postSession(h http.Handler, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/payment-sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

// decodeSessionResponse reads the handler's reply, keeping payment_session raw.
func decodeSessionResponse(t *testing.T, rec *httptest.ResponseRecorder) createSessionResponse {
	t.Helper()
	var got createSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	return got
}

// Proves the happy path: 201, the order summary from server data, and the
// session passed through as the same JSON value, unknown field included.
// Not the same bytes: encoding/json compacts a RawMessage (drops whitespace,
// escapes & as &). Flow parses JSON, so the value is what must not change.
func TestCreateSessionHappyPath(t *testing.T) {
	h, fake, _, _ := sessionTestApp(t)
	rec := postSession(h, `{"basket":"subscription","country":"FR"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	got := decodeSessionResponse(t, rec)

	want := decodeJSON(t, "fake session", fake.sessionRaw)
	sent := decodeJSON(t, "payment_session", got.PaymentSession)
	if d := diffKeys(want, sent); len(d) > 0 {
		t.Errorf("payment_session changed on the way through: %v", d)
	}
	if _, ok := sent["field_the_sdk_does_not_know"]; !ok {
		t.Error("unknown field was dropped: the handler must pass the raw body, not the SDK struct")
	}
	o := got.Order
	if o.ID != "ORD-1042" || o.Amount != 5000 || o.Currency != "EUR" || len(o.Items) != 4 || o.RenewalAmount != 3000 {
		t.Errorf("order summary %+v", o)
	}
}

// Proves the handler sends the request the builder makes: amount from the
// basket, reference = order ID, Recurring, capture false.
func TestCreateSessionSendsBuilderRequest(t *testing.T) {
	h, fake, _, _ := sessionTestApp(t)
	postSession(h, `{"basket":"subscription","country":"FR"}`)
	calls := fake.sessionCalls()
	if len(calls) != 1 {
		t.Fatalf("%d session calls, want 1", len(calls))
	}
	req := calls[0]
	if req.Amount != 5000 || req.Currency != "EUR" || req.Reference != "ORD-1042" ||
		req.PaymentType != payments.Recurring || req.Capture == nil || *req.Capture {
		t.Errorf("request amount %d %s ref %q type %q", req.Amount, req.Currency, req.Reference, req.PaymentType)
	}
}

// Proves the browser can't set the amount: extra fields in the body are ignored.
func TestCreateSessionIgnoresBrowserAmount(t *testing.T) {
	h, fake, _, _ := sessionTestApp(t)
	rec := postSession(h, `{"basket":"subscription","country":"FR","amount":1,"currency":"USD","items":[{"name":"x","unit_price":1}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	req := fake.sessionCalls()[0]
	if req.Amount != 5000 || req.Currency != "EUR" {
		t.Errorf("browser fields leaked: amount %d currency %s", req.Amount, req.Currency)
	}
	if got := decodeSessionResponse(t, rec).Order; got.Amount != 5000 || len(got.Items) != 4 {
		t.Errorf("summary took browser values: %+v", got)
	}
}

// Proves the one-off PT session gets MB WAY and PT billing, and no weekly price.
func TestCreateSessionOneOffPT(t *testing.T) {
	h, fake, _, _ := sessionTestApp(t)
	rec := postSession(h, `{"basket":"oneoff","country":"PT"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	req := fake.sessionCalls()[0]
	if req.Billing.Address.Country != "PT" || req.EnabledPaymentMethods[len(req.EnabledPaymentMethods)-1] != "mbway" {
		t.Errorf("billing %s methods %v", req.Billing.Address.Country, req.EnabledPaymentMethods)
	}
	if got := decodeSessionResponse(t, rec).Order; got.Amount != 4500 || got.RenewalAmount != 0 {
		t.Errorf("one-off summary %+v", got)
	}
}

// Proves bad input gives 400, never reaches Checkout.com, and stores nothing.
func TestCreateSessionBadInput(t *testing.T) {
	tests := []struct{ name, body string }{
		{"unknown basket", `{"basket":"renewal","country":"FR"}`},
		{"country we don't sell in", `{"basket":"subscription","country":"ES"}`},
		{"missing country", `{"basket":"oneoff"}`},
		{"not JSON", `basket=subscription`},
		{"empty body", ``},
		{"too big", `{"basket":"subscription","country":"FR","pad":"` + strings.Repeat("x", 2000) + `"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fake, store, _ := sessionTestApp(t)
			rec := postSession(h, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", rec.Code)
			}
			if n := len(fake.sessionCalls()); n != 0 {
				t.Errorf("%d calls reached Checkout.com", n)
			}
			if ids := store.OrderIDs(); len(ids) != 0 {
				t.Errorf("orders stored: %v", ids)
			}
			var e errorBody
			if json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Error == "" {
				t.Errorf("want a plain JSON error, got %s", rec.Body)
			}
		})
	}
}

// Proves a Checkout.com error gives 502 with status and error codes, a plain
// message, no stored order, and nothing key-like in the body.
func TestCreateSessionAPIError(t *testing.T) {
	h, fake, store, _ := sessionTestApp(t)
	fake.sessionErr = ckoerrors.CheckoutAPIError{
		StatusCode: 422,
		Status:     "422 Unprocessable Entity",
		Data:       &ckoerrors.ErrorDetails{ErrorCodes: []string{"processing_channel_id_invalid"}},
	}
	rec := postSession(h, `{"basket":"subscription","country":"FR"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", rec.Code)
	}
	var e errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if e.CkoStatus != 422 || len(e.ErrorCodes) != 1 || e.ErrorCodes[0] != "processing_channel_id_invalid" || e.Error == "" {
		t.Errorf("error body %+v", e)
	}
	if ids := store.OrderIDs(); len(ids) != 0 {
		t.Errorf("order stored after a failed session: %v", ids)
	}
	if strings.Contains(rec.Body.String(), "sk_") || strings.Contains(rec.Body.String(), "Bearer") {
		t.Error("error body carries key material")
	}
}

// Proves the order is saved to memory and disk, with its items, after success.
func TestCreateSessionStoresOrder(t *testing.T) {
	h, _, store, path := sessionTestApp(t)
	postSession(h, `{"basket":"subscription","country":"FR"}`)

	o, ok := store.Order("ORD-1042")
	if !ok || o.Amount != 5000 || len(o.Items) != 4 || o.NextRenewalWeek != 2 {
		t.Fatalf("stored order %+v (found %v)", o, ok)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Order("ORD-1042"); !ok {
		t.Error("order not on disk")
	}
}

// Proves every call is a new order and session (F11): no reuse.
func TestCreateSessionNewOrderEachTime(t *testing.T) {
	h, fake, store, _ := sessionTestApp(t)
	first := decodeSessionResponse(t, postSession(h, `{"basket":"subscription","country":"FR"}`)).Order.ID
	second := decodeSessionResponse(t, postSession(h, `{"basket":"subscription","country":"FR"}`)).Order.ID
	if first == second || first != "ORD-1042" || second != "ORD-1043" {
		t.Errorf("order IDs %q then %q", first, second)
	}
	if len(fake.sessionCalls()) != 2 || len(store.OrderIDs()) != 2 {
		t.Error("want two sessions and two orders")
	}
}

// Proves GET on the session route doesn't create anything. It falls through
// to the static server ("GET /"), which has no such file: 404.
func TestCreateSessionRejectsGet(t *testing.T) {
	h, fake, _, _ := sessionTestApp(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/payment-sessions", nil))
	if rec.Code != http.StatusNotFound || len(fake.sessionCalls()) != 0 {
		t.Errorf("status %d, calls %d", rec.Code, len(fake.sessionCalls()))
	}
}

// Proves the summary carries what the page shows: add-on flags on the items,
// and the customer block from the same demo data as the session billing.
func TestCreateSessionSummaryForPage(t *testing.T) {
	tests := []struct {
		body, wantCity, wantCountry string
		wantAddOns                  int
	}{
		{`{"basket":"subscription","country":"FR"}`, "Paris", "FR", 3},
		{`{"basket":"oneoff","country":"PT"}`, "Lisboa", "PT", 0},
	}
	for _, tt := range tests {
		t.Run(tt.wantCountry+"/"+tt.wantCity, func(t *testing.T) {
			h, fake, _, _ := sessionTestApp(t)
			o := decodeSessionResponse(t, postSession(h, tt.body)).Order

			addOns := 0
			for _, it := range o.Items {
				if it.AddOn {
					addOns++
				}
			}
			if addOns != tt.wantAddOns {
				t.Errorf("%d add-ons, want %d", addOns, tt.wantAddOns)
			}
			c := o.Customer
			if c.Name != "Marie Dubois" || c.Email != "marie.dubois@example.com" ||
				c.Address.City != tt.wantCity || c.Address.Country != tt.wantCountry {
				t.Errorf("customer %+v", c)
			}
			// The page and Checkout.com must show the same address.
			billing := fake.sessionCalls()[0].Billing.Address
			if billing.AddressLine1 != c.Address.Line1 || billing.City != c.Address.City || billing.Zip != c.Address.Zip {
				t.Errorf("summary address %+v differs from session billing %+v", c.Address, billing)
			}
		})
	}
}
