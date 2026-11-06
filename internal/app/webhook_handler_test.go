package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// webhookApp is an app with a fake API, a temp store with ORD-1042, and the
// event log. Returns the app too, so a test can wait for background work.
func webhookApp(t *testing.T) (*app, http.Handler, *fakePayments, *Store) {
	t.Helper()
	store, _ := newTestStore(t)
	createOrder(t, store, "ORD-1042", basketSubscription, "FR")
	fake := &fakePayments{
		details: map[string]*paymentsnas.GetPaymentResponse{},
		actions: map[string]*paymentsnas.GetPaymentActionsResponse{},
	}
	a := &app{
		cfg: sdkTestConfig(t), store: store, api: fake, events: newEventLog(),
		ids: newOrderIDGen(nil), now: func() time.Time { return fixedNow },
	}
	return a, newRouter(a, fstest.MapFS{}), fake, store
}

// sign is the test's own HMAC, as Checkout.com would compute it. The 5.1
// test checks verifySignature against an independent Python vector.
func sign(key, body string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func capturedEvent(eventID string) string {
	return `{"id":"` + eventID + `","type":"payment_captured","data":{"id":"pay_fakecitaaaaaaaaaaaaaaaaaaa","reference":"ORD-1042"}}`
}

func postWebhook(h http.Handler, body, signature string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/checkout", strings.NewReader(body))
	if signature != "" {
		req.Header.Set(signatureHeader, signature)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// Proves the happy path: a signed payment_captured gets 200, then the
// background GET stores the payment as Captured, and the live log has it.
func TestWebhookUpdatesPayment(t *testing.T) {
	a, h, fake, store := webhookApp(t)
	addRefPayment(t, fake, "ref_cit_captured")
	body := capturedEvent("evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa")

	rec := postWebhook(h, body, sign(a.cfg.WebhookSignatureKey, body))
	a.background.Wait()

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if _, p, ok := store.OrderWithPayment("pay_fakecitaaaaaaaaaaaaaaaaaaa"); !ok || p.Status != "Captured" {
		t.Errorf("stored %v %q, want Captured", ok, p.Status)
	}
	// Newest first: the background GET's read line (a first sight, so it
	// counts as a change), then the webhook itself, both on ORD-1042.
	got := a.events.Recent()
	if len(got) != 2 {
		t.Fatalf("live log %+v, want a webhook then a read", got)
	}
	// A Checkout.com event ID: not tagged simulated (D-042).
	if w := got[1]; w.Kind != entryWebhook || w.Text != "payment_captured" || w.EventID != "evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa" || w.OrderID != "ORD-1042" || w.Simulated {
		t.Errorf("webhook line %+v", w)
	}
	if r := got[0]; r.Kind != entryRead || r.Status != "Captured" || r.OrderID != "ORD-1042" {
		t.Errorf("read line %+v", r)
	}
}

// webhookLines is the log's webhook lines only, newest first. The read line
// from the background GET can land between them, in either order.
func webhookLines(l *eventLog) []logEntry {
	var out []logEntry
	for _, e := range l.Recent() {
		if e.Kind == entryWebhook {
			out = append(out, e)
		}
	}
	return out
}

// Proves the 200 goes out before the GET: the fake's GET is held, and the
// handler has still answered. Checkout.com needs a 2XX within 10 seconds,
// and our GET must never be what delays it.
func TestWebhookAnswersBeforeTheGET(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	addRefPayment(t, fake, "ref_cit_captured")
	release := make(chan struct{})
	fake.getHook = func() { <-release }
	body := capturedEvent("evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa")

	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- postWebhook(h, body, sign(a.cfg.WebhookSignatureKey, body)) }()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Errorf("status %d", rec.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no answer while the GET was held: the handler waits for it")
	}
	close(release)
	a.background.Wait()
	if n := fake.getPaymentCalls(); n != 1 {
		t.Errorf("%d GETs, want 1", n)
	}
}

// Proves a forged or unsigned webhook is refused with 401, nothing is
// fetched, and the live log shows it. The log line carries no request data.
func TestWebhookBadSignature(t *testing.T) {
	for name, sig := range map[string]string{
		"wrong key":      sign("another-fake-key", capturedEvent("evt_x")),
		"missing header": "",
		"not hex":        "forged",
	} {
		t.Run(name, func(t *testing.T) {
			a, h, fake, _ := webhookApp(t)
			rec := postWebhook(h, capturedEvent("evt_x"), sig)
			a.background.Wait()
			if rec.Code != http.StatusUnauthorized || rec.Body.Len() != 0 {
				t.Errorf("status %d, body %q; want 401 and no body", rec.Code, rec.Body)
			}
			if n := fake.getPaymentCalls(); n != 0 {
				t.Errorf("%d GETs after a bad signature", n)
			}
			got := a.events.Recent()
			if len(got) != 1 || got[0].Text != "bad signature, ignored" || got[0].EventID != "" || got[0].PaymentID != "" {
				t.Errorf("live log %+v", got)
			}
		})
	}
}

// Proves a body changed after signing fails: the check is on the raw bytes.
func TestWebhookChangedBody(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	signed := capturedEvent("evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa")
	sig := sign(a.cfg.WebhookSignatureKey, signed)
	if rec := postWebhook(h, strings.Replace(signed, "ORD-1042", "ORD-1043", 1), sig); rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
	a.background.Wait()
	if n := fake.getPaymentCalls(); n != 0 {
		t.Errorf("%d GETs", n)
	}
}

// Proves a retried webhook is processed once: the second copy gets 200, one
// GET in total, and the live log shows the repeat.
func TestWebhookDuplicate(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	addRefPayment(t, fake, "ref_cit_captured")
	body := capturedEvent("evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa")
	sig := sign(a.cfg.WebhookSignatureKey, body)

	first := postWebhook(h, body, sig)
	second := postWebhook(h, body, sig)
	a.background.Wait()

	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Errorf("status %d then %d, want 200 twice", first.Code, second.Code)
	}
	if n := fake.getPaymentCalls(); n != 1 {
		t.Errorf("%d GETs, want 1", n)
	}
	if got := webhookLines(a.events); len(got) != 2 || got[0].Text != "payment_captured (duplicate, ignored)" {
		t.Errorf("live log %+v", got)
	}
}

// Proves a webhook for someone else's payment (other tests share the
// sandbox) gets 200 and costs no GET.
func TestWebhookNotOurPayment(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	body := `{"id":"evt_other","type":"payment_approved","data":{"id":"pay_fakeotheraaaaaaaaaaaaaaaaa","reference":"someone-elses-test"}}`

	rec := postWebhook(h, body, sign(a.cfg.WebhookSignatureKey, body))
	a.background.Wait()
	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200", rec.Code)
	}
	if n := fake.getPaymentCalls(); n != 0 {
		t.Errorf("%d GETs for a payment that isn't ours", n)
	}
}

// Proves a payment_approved for a payment we haven't stored yet, but on one
// of our orders, is fetched and stored: the webhook can beat the success page.
func TestWebhookBeforeTheSuccessPage(t *testing.T) {
	a, h, fake, store := webhookApp(t)
	addRefPayment(t, fake, "ref_cit_authorized")
	body := `{"id":"evt_fakebbbbbbbbbbbbbbbbbbbbbbbb","type":"payment_approved","data":{"id":"pay_fakecitaaaaaaaaaaaaaaaaaaa","reference":"ORD-1042"}}`

	postWebhook(h, body, sign(a.cfg.WebhookSignatureKey, body))
	a.background.Wait()
	o, _ := store.Order("ORD-1042")
	if len(o.Payments) != 1 || o.CITPaymentID != "pay_fakecitaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("order %+v, want the CIT stored and recorded", o)
	}
}

// Proves the edges: a body over 1 MB is refused, a signed body that isn't
// an event is a 400, and neither is processed.
func TestWebhookEdges(t *testing.T) {
	a, h, fake, _ := webhookApp(t)
	big := strings.Repeat("x", maxWebhookBody+1)
	if rec := postWebhook(h, big, sign(a.cfg.WebhookSignatureKey, big)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("1 MB + 1: status %d, want 413", rec.Code)
	}
	notEvent := `{"hello":"world"}`
	if rec := postWebhook(h, notEvent, sign(a.cfg.WebhookSignatureKey, notEvent)); rec.Code != http.StatusBadRequest {
		t.Errorf("not an event: status %d, want 400", rec.Code)
	}
	a.background.Wait()
	if n := fake.getPaymentCalls(); n != 0 {
		t.Errorf("%d GETs", n)
	}
}

// Proves the server log never holds the signature, the key or the body:
// event ID, type and payment ID only (CLAUDE.md).
func TestWebhookLogsNoSecrets(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	a, h, fake, _ := webhookApp(t)
	addRefPayment(t, fake, "ref_cit_captured")
	body := capturedEvent("evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa")
	sig := sign(a.cfg.WebhookSignatureKey, body)

	postWebhook(h, body, sig)
	postWebhook(h, body, "forged")
	a.background.Wait()

	out := buf.String()
	for _, secret := range []string{sig, a.cfg.WebhookSignatureKey, `"reference"`, "forged"} {
		if strings.Contains(out, secret) {
			t.Errorf("server log contains %q", secret)
		}
	}
	if !strings.Contains(out, "evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa payment_captured pay_fakecitaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("server log missing the event line: %s", out)
	}
}
