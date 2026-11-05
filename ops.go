package main

import (
	"net/http"

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
				return Payment{}, "Captured at payment. Nothing to dispatch.", false
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
