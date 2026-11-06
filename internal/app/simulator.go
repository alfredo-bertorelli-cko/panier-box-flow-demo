package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// Simulated webhooks (D-042). No tunnel, so Checkout.com can't reach this
// laptop. Instead, after each action that worked, the simulator signs the
// webhook Checkout.com would send and posts it to our own
// /webhooks/checkout, over real HTTP. The handler runs exactly as it would
// for a real one: signature, dedupe, live log, background GET. Only the
// sender changes. Status still comes only from that GET (D-009).

// simulatedEventPrefix marks our own events. Real event IDs are evt_ plus 26
// characters, so the two never collide. The handler tags these "simulated"
// in the live log, and only after the signature check, so a forged
// "simulated" event is still a 401.
const simulatedEventPrefix = "evt_sim_"

const simulatorPostTimeout = 5 * time.Second

// simEvent is one webhook to send: which type, about which payment and action.
type simEvent struct {
	Type      string
	PaymentID string
	ActionID  string // "" for the first payment: Flow made it, we only saw it
	Reference string
	Amount    int64
	Currency  string
}

// The mapping, one function per source. Each returns ok false when
// Checkout.com wouldn't send one of the events we simulate.

// captureEvent: a capture Checkout.com accepted (202).
func captureEvent(paymentID string, req paymentsnas.CaptureRequest, resp *payments.CaptureResponse) simEvent {
	return simEvent{Type: "payment_captured", PaymentID: paymentID, ActionID: resp.ActionId, Reference: req.Reference, Amount: req.Amount}
}

// renewalEvent: an MIT's answer. Approved or declined at once. A Pending
// MIT would get its event later, so none here.
func renewalEvent(req paymentsnas.PaymentRequest, resp *paymentsnas.PaymentResponse) (simEvent, bool) {
	ev := simEvent{PaymentID: resp.Id, ActionID: resp.ActionId, Reference: req.Reference, Amount: resp.Amount, Currency: string(resp.Currency)}
	switch {
	case resp.Status == payments.Pending:
		return simEvent{}, false
	case resp.Approved:
		ev.Type = "payment_approved"
	default:
		ev.Type = "payment_declined"
	}
	return ev, true
}

// reverseEvent: Checkout.com's own answer says what the reversal did. Void
// if it wasn't captured, refund if it was (UC-9). We use that, not a guess.
func reverseEvent(paymentID string, req payments.PaymentReversalRequest, resp *payments.PaymentReversalResponse) (simEvent, bool) {
	ev := simEvent{PaymentID: paymentID, ActionID: resp.ActionId, Reference: req.Reference}
	switch {
	case strings.EqualFold(resp.ActionType, "Void"):
		ev.Type = "payment_voided"
	case strings.EqualFold(resp.ActionType, "Refund"):
		ev.Type = "payment_refunded"
	default:
		return simEvent{}, false
	}
	return ev, true
}

// citEvent: the first payment, the one action that isn't ours (Flow sends
// it from the browser). Sent when our server first sees it.
func citEvent(o Order, p Payment) (simEvent, bool) {
	if p.Kind != kindCIT {
		return simEvent{}, false
	}
	ev := simEvent{PaymentID: p.PaymentID, Reference: p.Reference, Amount: p.Amount, Currency: o.Currency}
	switch p.Status {
	case "Declined":
		ev.Type = "payment_declined"
	case "Authorized", "Captured", "Card Verified": // MB WAY is Captured at once (D-044)
		ev.Type = "payment_approved"
	default:
		return simEvent{}, false // Pending: no answer yet
	}
	return ev, true
}

// simulatedEventID is the same for the same action, every time: type,
// payment and action ID. A double click replays the same action_id
// (idempotency key), so it gets the same event ID, and the simulator sends
// it once. Base32 of a SHA-256, cut to 26 characters like a real ID.
func simulatedEventID(ev simEvent) string {
	sum := sha256.Sum256([]byte(ev.Type + "|" + ev.PaymentID + "|" + ev.ActionID))
	enc := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return simulatedEventPrefix + strings.ToLower(enc[:26])
}

// simulatedWebhook is the body: Checkout.com's shape, cut to what a reader
// of this demo needs. The handler reads id, type, data.id and data.reference.
type simulatedWebhook struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedOn time.Time `json:"created_on"`
	Data      struct {
		ID        string `json:"id"`
		ActionID  string `json:"action_id,omitempty"`
		Reference string `json:"reference"`
		Amount    int64  `json:"amount,omitempty"`
		Currency  string `json:"currency,omitempty"`
	} `json:"data"`
}

// webhookSimulator wraps PaymentsAPI like loggedPayments does: after an
// action Checkout.com accepted, it schedules the matching webhook. Reads and
// refusals send nothing.
type webhookSimulator struct {
	next   PaymentsAPI
	key    string // the webhook signature key from .env, as the handler uses
	target string // http://127.0.0.1:{PORT}/webhooks/checkout
	delay  time.Duration
	client *http.Client
	now    func() time.Time
	// after waits the delay. time.After normally. A test passes its own
	// channel, to decide exactly when the webhook leaves.
	after func(time.Duration) <-chan time.Time

	mu   sync.Mutex
	sent map[string]bool // event IDs already sent: one webhook per action
	wg   sync.WaitGroup  // posts still on their way, so tests can wait
}

func newWebhookSimulator(next PaymentsAPI, key, target string, delay time.Duration, now func() time.Time) *webhookSimulator {
	return &webhookSimulator{
		next: next, key: key, target: target, delay: delay, now: now,
		client: &http.Client{Timeout: simulatorPostTimeout},
		after:  time.After,
		sent:   map[string]bool{},
	}
}

var _ PaymentsAPI = (*webhookSimulator)(nil)

func (s *webhookSimulator) CreateSession(ctx context.Context, req sessions.PaymentSessionsRequest) (*sessions.PaymentSessionsResponse, error) {
	return s.next.CreateSession(ctx, req) // no webhook for a session
}

func (s *webhookSimulator) GetPayment(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentResponse, error) {
	return s.next.GetPayment(ctx, paymentID)
}

func (s *webhookSimulator) GetActions(ctx context.Context, paymentID string) (*paymentsnas.GetPaymentActionsResponse, error) {
	return s.next.GetActions(ctx, paymentID)
}

func (s *webhookSimulator) Capture(ctx context.Context, paymentID string, req paymentsnas.CaptureRequest, idempotencyKey string) (*payments.CaptureResponse, error) {
	resp, err := s.next.Capture(ctx, paymentID, req, idempotencyKey)
	if err == nil {
		s.send(captureEvent(paymentID, req, resp))
	}
	return resp, err
}

func (s *webhookSimulator) Reverse(ctx context.Context, paymentID string, req payments.PaymentReversalRequest, idempotencyKey string) (*payments.PaymentReversalResponse, error) {
	resp, err := s.next.Reverse(ctx, paymentID, req, idempotencyKey)
	if err == nil {
		if ev, ok := reverseEvent(paymentID, req, resp); ok {
			s.send(ev)
		}
	}
	return resp, err
}

func (s *webhookSimulator) RequestMIT(ctx context.Context, req paymentsnas.PaymentRequest, idempotencyKey string) (*paymentsnas.PaymentResponse, error) {
	resp, err := s.next.RequestMIT(ctx, req, idempotencyKey)
	if err == nil {
		if ev, ok := renewalEvent(req, resp); ok {
			s.send(ev)
		}
	}
	return resp, err
}

// CITSeen is called by syncPayment the first time our server stores a
// payment. For a first payment it sends payment_approved or
// payment_declined. A nil simulator (switched off) does nothing.
func (s *webhookSimulator) CITSeen(o Order, p Payment) {
	if s == nil {
		return
	}
	if ev, ok := citEvent(o, p); ok {
		s.send(ev)
	}
}

// send schedules one webhook, once per event ID, and returns at once: the
// action's response never waits for it.
func (s *webhookSimulator) send(ev simEvent) {
	id := simulatedEventID(ev)
	s.mu.Lock()
	if s.sent[id] {
		s.mu.Unlock()
		return
	}
	s.sent[id] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-s.after(s.delay)
		s.post(id, ev)
	}()
}

// post signs the exact bytes it sends, as Checkout.com does: HMAC-SHA256 of
// the raw body with the webhook signature key, hex, in Cko-Signature
// (D-037). Logs IDs and type only, never the key or the signature.
func (s *webhookSimulator) post(id string, ev simEvent) {
	var w simulatedWebhook
	w.ID, w.Type, w.CreatedOn = id, ev.Type, s.now().UTC()
	w.Data.ID, w.Data.ActionID, w.Data.Reference = ev.PaymentID, ev.ActionID, ev.Reference
	w.Data.Amount, w.Data.Currency = ev.Amount, ev.Currency
	body, err := json.Marshal(w)
	if err != nil {
		log.Printf("simulated webhook %s %s: %v", id, ev.Type, err)
		return
	}
	mac := hmac.New(sha256.New, []byte(s.key))
	mac.Write(body)

	req, err := http.NewRequest(http.MethodPost, s.target, bytes.NewReader(body))
	if err != nil {
		log.Printf("simulated webhook %s %s: %v", id, ev.Type, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(signatureHeader, hex.EncodeToString(mac.Sum(nil)))
	resp, err := s.client.Do(req)
	if err != nil {
		log.Printf("simulated webhook %s %s %s: post failed: %v", id, ev.Type, ev.PaymentID, err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("simulated webhook %s %s %s: answered %d", id, ev.Type, ev.PaymentID, resp.StatusCode)
	}
}

// Wait blocks until every scheduled webhook has been posted. Tests use it.
func (s *webhookSimulator) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}
