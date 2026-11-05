package main

import (
	"errors"
	"log"
	"net/http"

	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// Ops actions from the back office band (PRD Phase 4). Each one works on
// stored server data only: the browser sends an order ID, never an amount
// or a payment ID to act on.

// captureTarget finds the payment Dispatch captures: the order's newest
// first payment (CIT) that is Authorized with something left to capture.
// Newest, because a decline and a retry in the same session leave two CITs
// on one order. ok false gives the reason the button shows instead.
func captureTarget(o Order) (p Payment, reason string, ok bool) {
	// A subscription has a first payment and renewals. A one-off has just one.
	first, subject := "first ", "First payment"
	if o.NextRenewalWeek < 2 {
		first, subject = "", "Payment"
	}
	for i := len(o.Payments) - 1; i >= 0; i-- {
		p := o.Payments[i]
		if p.Kind != kindCIT {
			continue
		}
		b := p.Balances
		switch p.Status {
		case "Authorized":
			if b.Authorized-b.Captured-b.Voided > 0 {
				return p, "", true
			}
		case "Captured", "Partially Captured":
			// MB WAY captures at payment, even on a capture false session (S-07).
			if p.Method.Type != "card" {
				return Payment{}, "Already captured at payment.", false
			}
			return Payment{}, subject + " captured.", false
		case "Voided":
			return Payment{}, subject + " voided. Nothing to capture.", false
		case "Refunded", "Partially Refunded":
			return Payment{}, subject + " refunded. Nothing to capture.", false
		}
		// Declined, Pending or anything else: an earlier attempt may still be
		// the one that matters, so keep looking.
	}
	return Payment{}, "No authorised " + first + "payment yet.", false
}

// buildCaptureRequest captures the full authorised amount. Amount and
// reference are the only fields: capture_type defaults to Final, which is
// right for a box that ships in one go. The amount is sent even though the
// API would default to the full amount, so the request says what it does.
func buildCaptureRequest(o Order, p Payment) paymentsnas.CaptureRequest {
	return paymentsnas.CaptureRequest{
		Amount:    p.Balances.Authorized - p.Balances.Captured - p.Balances.Voided,
		Reference: o.ID,
	}
}

// handleCapture is "Dispatch first box" (F5). Checkout.com answers 202 and
// captures asynchronously, so the GET right after often still says
// Authorized. The page polls until the status changes (step 4.2).
func (a *app) handleCapture(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Order(r.PathValue("orderId"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown order.")
		return
	}
	p, reason, ok := captureTarget(o)
	if !ok {
		writeError(w, http.StatusConflict, reason)
		return
	}
	// capture-{payment_id}: a double click, or a retry after a timeout, sends
	// the same key, so Checkout.com captures once and replays the answer.
	key, err := CaptureKey(p.PaymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "We couldn't dispatch this order.")
		return
	}
	if _, err := a.api.Capture(r.Context(), p.PaymentID, buildCaptureRequest(o, p), key); err != nil {
		writeAPIError(w, err, "Checkout.com refused the capture.")
		return
	}
	o, p, err = a.syncPayment(r.Context(), p.PaymentID)
	if err != nil {
		// The capture went through. Only the refresh failed: say so, so ops
		// reloads instead of clicking Dispatch again.
		writeSyncError(w, err, "Capture sent. We couldn't refresh the payment, please reload.")
		return
	}
	writePayment(w, o, p)
}

// renewalBlock is why an order can't renew, or "" if it can. The texts are
// the button's disabled reasons from the mockup.
func renewalBlock(o Order) string {
	switch {
	case o.NextRenewalWeek < 2:
		return "One-off orders have no renewals."
	case o.CITPaymentID == "" || o.SourceID == "" || o.Scheme == "":
		// No approved first payment with a reusable card yet (P-8, D-029).
		return "No stored card on the first payment, renewals off (P-8)."
	}
	return ""
}

// handleRenewal is "Run next week's renewal" (F6): one MIT for the order's
// next week, authorise only. The week moves on only when the MIT is approved.
// A decline keeps the week, and a retry sends the same key, so Checkout.com
// replays the same answer. Real retries (another day, a new key) are P-3.
func (a *app) handleRenewal(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Order(r.PathValue("orderId"))
	if !ok {
		writeError(w, http.StatusNotFound, "Unknown order.")
		return
	}
	if reason := renewalBlock(o); reason != "" {
		writeError(w, http.StatusConflict, reason)
		return
	}
	week := o.NextRenewalWeek
	req, err := buildRenewalRequest(o, sessionSettingsFrom(a.cfg))
	if err != nil {
		log.Printf("build renewal: %v", err)
		writeError(w, http.StatusInternalServerError, "We couldn't run the renewal.")
		return
	}
	// renewal-ORD-1042-W2: one key per order and week. A double click sends
	// the same key, so Checkout.com charges week 2 once.
	key, err := RenewalKey(o.ID, week)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "We couldn't run the renewal.")
		return
	}
	resp, err := a.api.RequestMIT(r.Context(), req, key)
	if err != nil {
		writeAPIError(w, err, "Checkout.com refused the renewal.")
		return
	}
	// Store the MIT from GET, like every payment (D-009). Its reference
	// (ORD-1042-W2) puts it on this order.
	o, p, err := a.syncPayment(r.Context(), resp.Id)
	if err != nil {
		writeSyncError(w, err, "Renewal sent. We couldn't refresh it, please reload.")
		return
	}
	if resp.Approved {
		switch o2, err := a.store.AdvanceRenewalWeek(o.ID, week); {
		case err == nil:
			o = o2
		case errors.Is(err, errWeekMoved):
			// The other click of a double click advanced it. Show that order.
			o, _ = a.store.Order(o.ID)
		default:
			log.Printf("advance renewal week %s: %v", o.ID, err)
			writeError(w, http.StatusInternalServerError, "Renewal approved. We couldn't save the next week, please reload.")
			return
		}
	}
	writePayment(w, o, p)
}

const (
	reverseVoid   = "void"
	reverseRefund = "refund"
)

// reverseAction is what Reverse would do to one payment, from its stored
// balances: void what's authorised and not captured, refund what's captured
// and not refunded. Checkout.com's reversal endpoint makes the same choice
// (UC-9). "" means no button, and reason says why.
func reverseAction(p Payment) (action, reason string) {
	b := p.Balances
	switch {
	case p.Status == "Authorized" && b.Authorized-b.Captured-b.Voided > 0:
		return reverseVoid, ""
	case b.Captured-b.Refunded > 0:
		// Captured or Partially Refunded. MB WAY lands here: captured at
		// payment, so a cancel is a refund (S-07).
		return reverseRefund, ""
	case p.Status == "Voided":
		return "", "Voided."
	case p.Status == "Refunded":
		return "", "Refunded."
	}
	return "", "Nothing to reverse."
}

// handleReverse is a card's "Reverse" button (F7): void if not captured,
// refund if captured. One endpoint, one key per payment.
func (a *app) handleReverse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := checkPaymentID(id); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid payment ID.")
		return
	}
	o, p, ok := a.store.OrderWithPayment(id)
	if !ok {
		writeError(w, http.StatusNotFound, "This payment isn't on a Panier Box order.")
		return
	}
	if action, reason := reverseAction(p); action == "" {
		writeError(w, http.StatusConflict, reason)
		return
	}
	// reverse-{payment_id}: a double click sends the same key, and an
	// already reversed payment answers 200, so it reverses once.
	key, err := ReverseKey(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "We couldn't reverse this payment.")
		return
	}
	// Reference only: a reversal is always the full payment, so no amount
	// (the API reference request has none, D-035).
	resp, err := a.api.Reverse(r.Context(), id, payments.PaymentReversalRequest{Reference: p.Reference}, key)
	if err != nil {
		writeAPIError(w, err, "Checkout.com refused the reversal.")
		return
	}
	log.Printf("reverse %s on %s: %s", id, o.ID, resp.ActionType) // IDs and action type only
	o, p, err = a.syncPayment(r.Context(), id)
	if err != nil {
		writeSyncError(w, err, "Reversal sent. We couldn't refresh the payment, please reload.")
		return
	}
	writePayment(w, o, p)
}
