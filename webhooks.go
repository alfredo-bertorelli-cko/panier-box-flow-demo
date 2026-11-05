package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// Webhooks from Checkout.com (PRD F8, step 5.x).

// signatureHeader carries the webhook signature. Checkout.com sends it on
// every webhook ("Verify a webhook was sent by Checkout.com").
const signatureHeader = "Cko-Signature"

// verifySignature tells a real Checkout.com webhook from a forged one: the
// header must be the HMAC-SHA256 of the raw body, keyed with the webhook's
// own signature key (set on the webhook in the Dashboard, not the account
// secret key).
//
// body must be the exact bytes received. Decoding and re-encoding the JSON
// can change them (key order, number precision, characters like ©), and
// then no signature matches. That's the most common cause of failures in
// Checkout.com's support article.
//
// The docs don't name the encoding. Hex, per PRD F8, is confirmed with a
// real webhook in step 5.5 (D-037). The comparison is on decoded bytes with
// hmac.Equal, which takes the same time whatever matches, so the response
// time can't leak how close a forged signature was. Never log the header,
// the key or the body: a failure is only "bad signature".
func verifySignature(body []byte, header, key string) bool {
	if key == "" {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// webhookEvent is the part of a Checkout.com webhook we read. Everything
// else stays in the raw body: status always comes from our own GET (D-009),
// never from the webhook.
type webhookEvent struct {
	ID   string `json:"id"`   // evt_...
	Type string `json:"type"` // payment_captured, payment_approved...
	Data struct {
		ID        string `json:"id"`        // pay_...
		Reference string `json:"reference"` // ORD-1042 or ORD-1042-W2
	} `json:"data"`
}

const (
	maxWebhookBody = 1 << 20          // 1 MB (F8)
	webhookSyncTTL = 30 * time.Second // the background GET, after we answered
)

// handleWebhook receives Checkout.com webhooks (F8):
//  1. read the raw body (1 MB limit) and verify the signature on those exact
//     bytes, before any JSON decoding (D-037). Bad: 401, no body.
//  2. dedupe by event ID. A repeat: 200 and stop.
//  3. log it and answer 200 at once. Checkout.com needs a 2XX within 10
//     seconds, or it retries (S-03).
//  4. in the background, if the payment is on one of our orders, GET it and
//     store it through syncPayment, like every other read.
func (a *app) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if !verifySignature(body, r.Header.Get(signatureHeader), a.cfg.WebhookSignatureKey) {
		// Nothing about the request: not the header, not the body.
		log.Print("webhook: bad signature, ignored")
		a.events.Add(logEntry{Time: a.now(), Kind: entryWebhook, Dir: dirIn, Text: "bad signature, ignored", HTTPStatus: http.StatusUnauthorized})
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var ev webhookEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		log.Print("webhook: signed but not a webhook event, ignored")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	entry := logEntry{Time: a.now(), Kind: entryWebhook, Dir: dirIn, Text: ev.Type, EventID: ev.ID, PaymentID: ev.Data.ID, OrderID: a.webhookOrder(ev), HTTPStatus: http.StatusOK}
	// Our simulator's events (D-042). Trusted only because the signature
	// passed: nobody without the key can get this tag on the page.
	entry.Simulated = strings.HasPrefix(ev.ID, simulatedEventPrefix)
	// Event ID, type and payment ID only (CLAUDE.md).
	if a.events.SeenWebhook(ev.ID) {
		log.Printf("webhook %s %s %s: duplicate, ignored", ev.ID, ev.Type, ev.Data.ID)
		entry.Text = ev.Type + " (duplicate, ignored)"
		a.events.Add(entry)
		w.WriteHeader(http.StatusOK)
		return
	}
	log.Printf("webhook %s %s %s", ev.ID, ev.Type, ev.Data.ID)
	a.events.Add(entry)
	w.WriteHeader(http.StatusOK)

	a.background.Add(1)
	go func() {
		defer a.background.Done()
		a.processWebhook(ev)
	}()
}

// webhookOrder is the order a webhook is about, for the live log: from its
// reference, else from the stored payment. "" for a payment that isn't ours
// (other tests share the sandbox), so no order's page shows it.
func (a *app) webhookOrder(ev webhookEvent) string {
	if o, ok := a.store.OrderForReference(ev.Data.Reference); ok {
		return o.ID
	}
	if o, _, ok := a.store.OrderWithPayment(ev.Data.ID); ok {
		return o.ID
	}
	return ""
}

// processWebhook runs after the 200. The request's context is already
// cancelled by then, so it has its own, with a timeout.
func (a *app) processWebhook(ev webhookEvent) {
	id := ev.Data.ID
	if checkPaymentID(id) != nil {
		return // not a payment event
	}
	// Other tests share the sandbox. Ignore payments that aren't ours before
	// spending a GET on them: known payment, or a reference to one of our
	// orders (a payment_approved can arrive before the success page's GET).
	if _, _, ok := a.store.OrderWithPayment(id); !ok {
		if _, ok := a.store.OrderForReference(ev.Data.Reference); !ok {
			log.Printf("webhook %s %s %s: not a Panier Box payment, ignored", ev.ID, ev.Type, id)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookSyncTTL)
	defer cancel()
	if _, _, err := a.syncPayment(ctx, id); err != nil {
		log.Printf("webhook %s: refresh %s: %v", ev.ID, id, err)
	}
}
