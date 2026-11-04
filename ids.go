package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

const (
	orderIDPrefix    = "ORD-"
	firstOrderNumber = 1042 // matches the examples in PRD.md
)

// orderIDGen hands out ORD-1042, ORD-1043 and so on. Safe for concurrent use.
type orderIDGen struct {
	mu   sync.Mutex
	next int
}

// newOrderIDGen continues after the highest saved order number, so a restart
// never reuses an ID that's still in data/orders.json.
func newOrderIDGen(existing []string) *orderIDGen {
	next := firstOrderNumber
	for _, id := range existing {
		if n, ok := parseOrderNumber(id); ok && n >= next {
			next = n + 1
		}
	}
	return &orderIDGen{next: next}
}

func (g *orderIDGen) Next() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := orderIDPrefix + strconv.Itoa(g.next)
	g.next++
	return id
}

// parseOrderNumber accepts only "ORD-" plus digits. Renewal refs like
// "ORD-1042-W2" and anything else are ignored.
func parseOrderNumber(id string) (int, bool) {
	digits, ok := strings.CutPrefix(id, orderIDPrefix)
	if !ok || digits == "" {
		return 0, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

// RenewalReference is the payment reference for a renewal MIT: ORD-1042-W2.
// Week 1 is the first CIT itself, so renewals start at week 2.
func RenewalReference(orderID string, week int) (string, error) {
	if orderID == "" {
		return "", errors.New("renewal reference: empty order ID")
	}
	if week < 2 {
		return "", fmt.Errorf("renewal reference: week %d, renewals start at week 2", week)
	}
	return fmt.Sprintf("%s-W%d", orderID, week), nil
}

// Idempotency keys come from stable IDs, never random values. A double click
// or a retry sends the same key, so Checkout.com runs the action once and
// returns the first result.

// CaptureKey is the idempotency key for capturing a payment.
func CaptureKey(paymentID string) (string, error) {
	if err := checkPaymentID(paymentID); err != nil {
		return "", fmt.Errorf("capture key: %w", err)
	}
	return "capture-" + paymentID, nil
}

// ReverseKey is the idempotency key for reversing a payment.
func ReverseKey(paymentID string) (string, error) {
	if err := checkPaymentID(paymentID); err != nil {
		return "", fmt.Errorf("reverse key: %w", err)
	}
	return "reverse-" + paymentID, nil
}

// RenewalKey is the idempotency key for a renewal MIT. It's built from the
// renewal reference, so the key and the reference can't drift apart.
func RenewalKey(orderID string, week int) (string, error) {
	ref, err := RenewalReference(orderID, week)
	if err != nil {
		return "", fmt.Errorf("renewal key: %w", err)
	}
	return "renewal-" + ref, nil
}

// checkPaymentID refuses empty or odd IDs. Without it a bug upstream could
// produce the key "capture-" for every payment, and Checkout.com would answer
// later captures with the cached result of the first one.
func checkPaymentID(id string) error {
	if !strings.HasPrefix(id, "pay_") || len(id) == len("pay_") || strings.ContainsAny(id, " \t\r\n") {
		return fmt.Errorf("invalid payment ID %q", id)
	}
	return nil
}
