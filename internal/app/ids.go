package app

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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

// orderNumberEpoch is the zero point for orderNumberFloor (D-046).
var orderNumberEpoch = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// orderNumberFloor is where a fresh server starts counting: the seconds since
// orderNumberEpoch, never below firstOrderNumber. Renewal idempotency keys come
// from the order ID (renewal-ORD-n-Wk), so after data/orders.json is deleted a
// new order must not reuse a number Checkout.com has seen recently, or it would
// replay that old renewal. A later start always begins higher (D-046).
func orderNumberFloor(now time.Time) int {
	n := int(now.Sub(orderNumberEpoch) / time.Second)
	if n < firstOrderNumber {
		return firstOrderNumber
	}
	return n
}

// newOrderIDGen starts at floor, or after the highest saved order number if
// that's higher, so a restart never reuses an ID still in data/orders.json.
func newOrderIDGen(existing []string, floor int) *orderIDGen {
	next := max(floor, firstOrderNumber)
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

// OrderIDFromReference maps a payment reference back to its order:
// "ORD-1042" is the CIT, "ORD-1042-W2" a renewal of the same order.
func OrderIDFromReference(ref string) (string, bool) {
	if _, ok := parseOrderNumber(ref); ok {
		return ref, true
	}
	i := strings.LastIndex(ref, "-W")
	if i < 0 {
		return "", false
	}
	orderID, week := ref[:i], ref[i+2:]
	if _, ok := parseOrderNumber(orderID); !ok {
		return "", false
	}
	if n, err := strconv.Atoi(week); err != nil || n < 2 || strconv.Itoa(n) != week {
		return "", false
	}
	return orderID, true
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

// paymentIDPattern is the API reference pattern for a payment ID path
// parameter: ^(pay)_(\w{26})$.
var paymentIDPattern = regexp.MustCompile(`^pay_\w{26}$`)

// checkPaymentID refuses anything that isn't a payment ID. Two reasons:
// without it a bug upstream could produce the key "capture-" for every
// payment, and Checkout.com would answer later captures with the cached
// result of the first one. And IDs come from the URL: "pay_x%2F..%2Fother"
// decodes to slashes, which the SDK would put into the Checkout.com path.
func checkPaymentID(id string) error {
	if !paymentIDPattern.MatchString(id) {
		return fmt.Errorf("invalid payment ID %q", id)
	}
	return nil
}
