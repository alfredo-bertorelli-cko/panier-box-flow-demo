package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// Proves the mapping (D-042): each action Checkout.com accepted gives the
// event it would send, and the cases it wouldn't send one give none.
func TestSimulatedEventMapping(t *testing.T) {
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"

	t.Run("capture", func(t *testing.T) {
		ev := captureEvent(pay, paymentsnas.CaptureRequest{Amount: 5000, Reference: "ORD-1042"}, &payments.CaptureResponse{ActionId: "act_c"})
		if ev.Type != "payment_captured" || ev.PaymentID != pay || ev.ActionID != "act_c" || ev.Reference != "ORD-1042" || ev.Amount != 5000 {
			t.Errorf("%+v", ev)
		}
	})
	t.Run("renewal", func(t *testing.T) {
		req := paymentsnas.PaymentRequest{Reference: "ORD-1042-W2"}
		for _, c := range []struct {
			resp paymentsnas.PaymentResponse
			want string // "" = no event
		}{
			{paymentsnas.PaymentResponse{Id: "pay_m", Approved: true, Status: payments.Authorized}, "payment_approved"},
			{paymentsnas.PaymentResponse{Id: "pay_m", Approved: false, Status: payments.Declined}, "payment_declined"},
			{paymentsnas.PaymentResponse{Id: "pay_m", Status: payments.Pending}, ""},
		} {
			ev, ok := renewalEvent(req, &c.resp)
			if got := map[bool]string{true: ev.Type}[ok]; got != c.want {
				t.Errorf("%s: got %q, want %q", c.resp.Status, got, c.want)
			}
			if ok && (ev.PaymentID != "pay_m" || ev.Reference != "ORD-1042-W2") {
				t.Errorf("%+v", ev)
			}
		}
	})
	t.Run("reverse", func(t *testing.T) {
		req := payments.PaymentReversalRequest{Reference: "ORD-1042"}
		for actionType, want := range map[string]string{"Void": "payment_voided", "Refund": "payment_refunded", "": ""} {
			ev, ok := reverseEvent(pay, req, &payments.PaymentReversalResponse{ActionType: actionType})
			if got := map[bool]string{true: ev.Type}[ok]; got != want {
				t.Errorf("%q: got %q, want %q", actionType, got, want)
			}
		}
	})
	t.Run("first payment", func(t *testing.T) {
		o := Order{Currency: "EUR"}
		for status, want := range map[string]string{
			"Authorized": "payment_approved", "Captured": "payment_approved", // MB WAY
			"Declined": "payment_declined", "Pending": "",
		} {
			p := samplePayment(pay)
			p.Status = status
			ev, ok := citEvent(o, p)
			if got := map[bool]string{true: ev.Type}[ok]; got != want {
				t.Errorf("%s: got %q, want %q", status, got, want)
			}
		}
		mit := samplePayment(pay)
		mit.Kind = kindMIT
		if _, ok := citEvent(o, mit); ok {
			t.Error("an MIT on first sight: renewals get their event from the action")
		}
	})
}

// Proves the event ID: stable for one action, different for another, and
// marked as ours.
func TestSimulatedEventID(t *testing.T) {
	a := simEvent{Type: "payment_captured", PaymentID: "pay_x", ActionID: "act_1"}
	replay := simEvent{Type: "payment_captured", PaymentID: "pay_x", ActionID: "act_1"} // a double click
	b := a
	b.ActionID = "act_2"
	if simulatedEventID(a) != simulatedEventID(replay) {
		t.Error("same action, different IDs")
	}
	if simulatedEventID(a) == simulatedEventID(b) {
		t.Error("different actions, same ID")
	}
	id := simulatedEventID(a)
	if !strings.HasPrefix(id, simulatedEventPrefix) || len(id) != len(simulatedEventPrefix)+26 {
		t.Errorf("ID %q", id)
	}
}

// postRecorder is a stand-in webhook endpoint: it records every post and
// checks its signature with the same code as the real handler.
type postRecorder struct {
	mu     sync.Mutex
	events []webhookEvent
	badSig int
}

func (r *postRecorder) serve(key string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		if !verifySignature(body, req.Header.Get(signatureHeader), key) {
			r.badSig++
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var ev webhookEvent
		json.Unmarshal(body, &ev)
		r.events = append(r.events, ev)
	}))
}

func (r *postRecorder) got() []webhookEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]webhookEvent(nil), r.events...)
}

const simTestKey = "fake-webhook-signature-key"

func recordingSimulator(t *testing.T) (*webhookSimulator, *fakePayments, *postRecorder) {
	t.Helper()
	rec := &postRecorder{}
	srv := rec.serve(simTestKey)
	t.Cleanup(srv.Close)
	fake := &fakePayments{
		details: map[string]*paymentsnas.GetPaymentResponse{},
		actions: map[string]*paymentsnas.GetPaymentActionsResponse{},
	}
	sim := newWebhookSimulator(fake, simTestKey, srv.URL+webhookPath, 0, func() time.Time { return fixedNow })
	return sim, fake, rec
}

// Proves one webhook per action: a double click (same action_id replayed
// through the idempotency key), a second first sight, and reads send
// nothing more. The signature checks out with the handler's own code.
func TestSimulatorSendsOncePerAction(t *testing.T) {
	sim, fake, rec := recordingSimulator(t)
	ctx := context.Background()
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"
	addRefPayment(t, fake, "ref_cit_authorized")

	req := paymentsnas.CaptureRequest{Amount: 5000, Reference: "ORD-1042"}
	sim.Capture(ctx, pay, req, "capture-"+pay)
	sim.Capture(ctx, pay, req, "capture-"+pay) // the fake replays the same action_id
	p := samplePayment(pay)
	sim.CITSeen(Order{Currency: "EUR"}, p)
	sim.CITSeen(Order{Currency: "EUR"}, p)
	sim.GetPayment(ctx, pay)
	sim.GetActions(ctx, pay)
	sim.Wait()

	got := rec.got()
	types := map[string]int{}
	for _, ev := range got {
		types[ev.Type]++
		if !strings.HasPrefix(ev.ID, simulatedEventPrefix) || ev.Data.ID != pay || ev.Data.Reference != "ORD-1042" {
			t.Errorf("event %+v", ev)
		}
	}
	if len(got) != 2 || types["payment_captured"] != 1 || types["payment_approved"] != 1 {
		t.Errorf("posted %v, want one capture and one approved", types)
	}
	if rec.badSig != 0 {
		t.Errorf("%d posts failed the signature check", rec.badSig)
	}
}

// Proves a refused action sends nothing: Checkout.com sends no event for a
// request it rejected.
func TestSimulatorSkipsRefusals(t *testing.T) {
	sim, fake, rec := recordingSimulator(t)
	fake.captureErr = ckoerrors.CheckoutAPIError{StatusCode: 422}
	fake.reverseErr = ckoerrors.CheckoutAPIError{StatusCode: 422}
	fake.mitErr = ckoerrors.CheckoutAPIError{StatusCode: 422}
	ctx := context.Background()
	sim.Capture(ctx, "pay_x", paymentsnas.CaptureRequest{}, "k")
	sim.Reverse(ctx, "pay_x", payments.PaymentReversalRequest{}, "k")
	sim.RequestMIT(ctx, paymentsnas.PaymentRequest{}, "k")
	sim.Wait()
	if got := rec.got(); len(got) != 0 {
		t.Errorf("posted %+v", got)
	}
}

// Proves the action never waits for its webhook: Capture returns while
// the webhook is still held back.
func TestSimulatorIsAsync(t *testing.T) {
	sim, _, rec := recordingSimulator(t)
	release := make(chan time.Time)
	sim.after = func(time.Duration) <-chan time.Time { return release }

	done := make(chan struct{})
	go func() {
		sim.Capture(context.Background(), "pay_x", paymentsnas.CaptureRequest{Reference: "ORD-1042"}, "k")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Capture waited for the webhook")
	}
	if n := len(rec.got()); n != 0 {
		t.Fatalf("posted %d before the delay", n)
	}
	close(release)
	sim.Wait()
	if n := len(rec.got()); n != 1 {
		t.Errorf("posted %d after the delay, want 1", n)
	}
}

// simRoundTrip is the full app on a real server, with the simulator posting
// to that server's own /webhooks/checkout, as in main. key "" means the
// app's own webhook key, as main passes it.
func simRoundTrip(t *testing.T, key string) (*app, *httptest.Server, *fakePayments, *Store, *webhookSimulator, chan time.Time) {
	t.Helper()
	a, h, fake, store := webhookApp(t)
	if key == "" {
		key = a.cfg.WebhookSignatureKey
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	release := make(chan time.Time)
	sim := newWebhookSimulator(newLoggedPayments(fake, a.events, a.now), key, srv.URL+webhookPath, 0, a.now)
	sim.after = func(time.Duration) <-chan time.Time { return release }
	a.api, a.sim = sim, sim
	return a, srv, fake, store, sim, release
}

// firstRead is the success page's GET, which stores the first payment.
func firstRead(t *testing.T, srv *httptest.Server, pay string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/api/payments/" + pay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET payment: %d", resp.StatusCode)
	}
}

func post(t *testing.T, srv *httptest.Server, path string) {
	t.Helper()
	resp, err := srv.Client().Post(srv.URL+path, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %d", path, resp.StatusCode)
	}
}

// Proves the round trip (D-042): Dispatch, then the simulated
// payment_captured goes through the real handler (signature, live log,
// background GET), and that GET is what moves the card. Here Checkout.com
// still said Authorized right after the capture, as it can (async capture),
// so the webhook's read is the one that shows Captured.
func TestSimulatedWebhookRoundTrip(t *testing.T) {
	a, srv, fake, store, sim, release := simRoundTrip(t, "")
	const pay = "pay_fakecitaaaaaaaaaaaaaaaaaaa"
	addRefPayment(t, fake, "ref_cit_authorized")
	firstRead(t, srv, pay)

	post(t, srv, "/api/orders/ORD-1042/capture")
	// Checkout.com has captured by the time the webhook is sent.
	fake.mu.Lock()
	fake.details[pay] = loadPayment(t, "testdata/payments/ref_cit_captured.json")
	fake.mu.Unlock()
	close(release)
	sim.Wait()
	a.background.Wait()

	if _, p, _ := store.OrderWithPayment(pay); p.Status != "Captured" {
		t.Errorf("stored %q, want Captured from the webhook's GET", p.Status)
	}
	var captured, approved *logEntry
	for _, e := range a.events.Recent() {
		if e.Kind == entryWebhook {
			e := e
			switch e.Text {
			case "payment_captured":
				captured = &e
			case "payment_approved":
				approved = &e
			}
		}
	}
	if captured == nil || !captured.Simulated || captured.OrderID != "ORD-1042" || !strings.HasPrefix(captured.EventID, simulatedEventPrefix) {
		t.Errorf("payment_captured line %+v", captured)
	}
	if approved == nil || !approved.Simulated {
		t.Errorf("payment_approved line for the first sight %+v", approved)
	}
	if got := readLines(a.events); len(got) == 0 || got[0].Status != "Captured" {
		t.Errorf("read lines %+v, want the webhook's GET showing Captured", got)
	}
}

// Proves the simulator goes through the same signature check as anything
// else: signed with the wrong key, it's a 401 and no simulated line.
func TestSimulatedWebhookWrongKey(t *testing.T) {
	a, srv, fake, _, sim, release := simRoundTrip(t, "not-the-handlers-key")
	addRefPayment(t, fake, "ref_cit_authorized")
	firstRead(t, srv, "pay_fakecitaaaaaaaaaaaaaaaaaaa")
	post(t, srv, "/api/orders/ORD-1042/capture")
	close(release)
	sim.Wait()
	a.background.Wait()
	for _, e := range a.events.Recent() {
		if e.Simulated {
			t.Errorf("simulated line despite a bad signature: %+v", e)
		}
	}
	if got := webhookLines(a.events); len(got) == 0 || got[0].HTTPStatus != http.StatusUnauthorized {
		t.Errorf("webhook lines %+v, want a bad signature", got)
	}
}

// Proves a forged event with our prefix is still a 401: the tag can only
// come with the key.
func TestForgedSimulatedEventRejected(t *testing.T) {
	a, h, _, _ := webhookApp(t)
	body := `{"id":"evt_sim_forgedaaaaaaaaaaaaaaaaaaaa","type":"payment_captured","data":{"id":"pay_fakecitaaaaaaaaaaaaaaaaaaa","reference":"ORD-1042"}}`
	if rec := postWebhook(h, body, sign("wrong-key", body)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	for _, e := range a.events.Recent() {
		if e.Simulated {
			t.Errorf("forged event tagged simulated: %+v", e)
		}
	}
}

// Proves the off switch: SIMULATE_WEBHOOKS=false builds no simulator, only
// the live log wrapper.
func TestBuildAPISwitch(t *testing.T) {
	cfg := sdkTestConfig(t)
	fake := &fakePayments{}
	cfg.SimulateWebhooks = false
	api, sim := buildAPI(cfg, fake, newEventLog(), time.Now)
	if sim != nil {
		t.Error("simulator built while switched off")
	}
	if _, ok := api.(*loggedPayments); !ok {
		t.Errorf("api is %T, want *loggedPayments", api)
	}
	cfg.SimulateWebhooks = true
	cfg.Port = "8080"
	api, sim = buildAPI(cfg, fake, newEventLog(), time.Now)
	if sim == nil || api != PaymentsAPI(sim) || sim.target != "http://127.0.0.1:8080/webhooks/checkout" {
		t.Errorf("on: api %T, sim %v", api, sim)
	}
	if _, ok := sim.next.(*loggedPayments); !ok {
		t.Errorf("simulator wraps %T, want the live log wrapper", sim.next)
	}
	// A nil simulator is safe to call: syncPayment does when it's off.
	var off *webhookSimulator
	off.CITSeen(Order{}, samplePayment("pay_x"))
	off.Wait()
}
