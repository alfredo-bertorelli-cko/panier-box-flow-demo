package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Our own record of an order and its payments, shaped for the screen
// (PRD.md section 6). Never the raw SDK response.

type PaymentMethod struct {
	Type string `json:"type"`
	// Scheme is processing.scheme: the network the payment actually ran on.
	// Empty if it never reached authorisation (for example, 3DS failed).
	Scheme string `json:"scheme"`
	// CardSchemes are the card's own brands (source.scheme plus
	// source.local_schemes), for display only. Never used for renewals.
	CardSchemes []string `json:"card_schemes,omitempty"`
	Last4       string   `json:"last4"`
	CardType    string   `json:"card_type"`
	Wallet      string   `json:"wallet"`
}

type Balances struct {
	Authorized int64 `json:"authorized"`
	Captured   int64 `json:"captured"`
	Voided     int64 `json:"voided"`
	Refunded   int64 `json:"refunded"`
}

type Payment struct {
	PaymentID       string        `json:"payment_id"`
	Kind            string        `json:"kind"` // "CIT" or "MIT"
	Reference       string        `json:"reference"`
	Amount          int64         `json:"amount"`
	Status          string        `json:"status"`
	Method          PaymentMethod `json:"method"`
	Balances        Balances      `json:"balances"`
	ThreeDS         string        `json:"three_ds"`
	ResponseCode    string        `json:"response_code"`
	ResponseSummary string        `json:"response_summary"`
	ExpiresOn       time.Time     `json:"expires_on,omitzero"`
	UpdatedAt       time.Time     `json:"updated_at,omitzero"`
}

type Order struct {
	ID           string    `json:"id"`
	Basket       string    `json:"basket"`
	Country      string    `json:"country"`
	Currency     string    `json:"currency"`
	Items        []Item    `json:"items"`
	Amount       int64     `json:"amount"`
	CreatedAt    time.Time `json:"created_at"`
	SourceID     string    `json:"source_id"`
	Scheme       string    `json:"scheme"`
	CITPaymentID string    `json:"cit_payment_id"`
	Payments     []Payment `json:"payments"`
	// NextRenewalWeek is 2 for a new subscription (week 1 is the CIT) and 0
	// for a one-off order, which never renews.
	NextRenewalWeek int `json:"next_renewal_week"`
}

const (
	kindCIT = "CIT"
	kindMIT = "MIT"
)

// NewOrder builds an order from server-side basket data. The amount comes
// from the basket, never from the browser.
func NewOrder(id string, b Basket, country string, now time.Time) Order {
	o := Order{
		ID:        id,
		Basket:    b.ID,
		Country:   country,
		Currency:  b.Currency,
		Items:     slices.Clone(b.Items),
		Amount:    b.Total(),
		CreatedAt: now.UTC(),
	}
	if b.Recurring {
		o.NextRenewalWeek = 2
	}
	return o
}

type storeFile struct {
	Orders []Order `json:"orders"`
}

// Store keeps orders in memory and writes them all to one JSON file after
// every change. One mutex covers everything: handlers and webhook goroutines
// both write.
type Store struct {
	mu     sync.Mutex
	path   string
	orders []Order
}

// OpenStore loads the file if it exists. A missing file is an empty store.
// A corrupt file is an error: starting empty would overwrite it on the next save.
func OpenStore(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Store{path: path}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("open store %s: not valid JSON, fix or delete the file: %w", path, err)
	}
	return &Store{path: path, orders: f.Orders}, nil
}

// CreateOrder adds a new order. Duplicate IDs are refused.
func (s *Store) CreateOrder(o Order) error {
	if o.ID == "" {
		return errors.New("create order: empty order ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(func() error {
		if s.indexOf(o.ID) >= 0 {
			return fmt.Errorf("create order: %s already exists", o.ID)
		}
		s.orders = append(s.orders, cloneOrder(o))
		return nil
	})
}

// Order returns a copy of one order.
func (s *Store) Order(id string) (Order, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.indexOf(id)
	if i < 0 {
		return Order{}, false
	}
	return cloneOrder(s.orders[i]), true
}

// OrderForReference finds the order a payment belongs to, from the payment's
// reference: ORD-1042 for the CIT, ORD-1042-W2 for a renewal.
func (s *Store) OrderForReference(ref string) (Order, bool) {
	id, ok := OrderIDFromReference(ref)
	if !ok {
		return Order{}, false
	}
	return s.Order(id)
}

// UpsertPayment replaces the payment with the same payment_id, or appends it.
// A second GET or a duplicate webhook updates, never duplicates.
func (s *Store) UpsertPayment(orderID string, p Payment) (Order, error) {
	if p.PaymentID == "" {
		return Order{}, errors.New("upsert payment: empty payment ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Order
	err := s.update(func() error {
		i := s.indexOf(orderID)
		if i < 0 {
			return fmt.Errorf("upsert payment: unknown order %s", orderID)
		}
		o := &s.orders[i]
		if j := slices.IndexFunc(o.Payments, func(x Payment) bool { return x.PaymentID == p.PaymentID }); j >= 0 {
			o.Payments[j] = p
		} else {
			o.Payments = append(o.Payments, p)
		}
		out = cloneOrder(*o)
		return nil
	})
	return out, err
}

// RecordCIT sets the first payment's ID, source and scheme, once. Renewals
// use them for every MIT: source.id to charge the stored card,
// previous_payment_id to link the series, preferred_scheme for the scheme
// Marie chose. Later calls never overwrite them.
func (s *Store) RecordCIT(orderID, paymentID, sourceID, scheme string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Order
	err := s.update(func() error {
		i := s.indexOf(orderID)
		if i < 0 {
			return fmt.Errorf("record CIT: unknown order %s", orderID)
		}
		o := &s.orders[i]
		if o.CITPaymentID == "" {
			o.CITPaymentID = paymentID
		}
		if o.SourceID == "" {
			o.SourceID = sourceID
		}
		if o.Scheme == "" {
			o.Scheme = scheme
		}
		out = cloneOrder(*o)
		return nil
	})
	return out, err
}

// errWeekMoved: the order is no longer on the week the caller renewed.
var errWeekMoved = errors.New("renewal week already moved on")

// AdvanceRenewalWeek moves from fromWeek to the next week, after an approved
// MIT for fromWeek. If the order has moved on already (a double click: both
// requests renewed W2 with the same key, Checkout.com charged once), it
// refuses with errWeekMoved instead of skipping a week.
func (s *Store) AdvanceRenewalWeek(orderID string, fromWeek int) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Order
	err := s.update(func() error {
		i := s.indexOf(orderID)
		if i < 0 {
			return fmt.Errorf("advance renewal week: unknown order %s", orderID)
		}
		o := &s.orders[i]
		if o.NextRenewalWeek < 2 {
			return fmt.Errorf("advance renewal week: order %s has no renewals", orderID)
		}
		if o.NextRenewalWeek != fromWeek {
			return fmt.Errorf("advance renewal week: order %s is on week %d, not %d: %w", orderID, o.NextRenewalWeek, fromWeek, errWeekMoved)
		}
		o.NextRenewalWeek++
		out = cloneOrder(*o)
		return nil
	})
	return out, err
}

// OrderWithPayment finds the order that holds a payment, and that payment.
// Ops actions on one card use it: a payment ID that isn't on one of our
// orders (another test on the same sandbox) is never acted on.
func (s *Store) OrderWithPayment(paymentID string) (Order, Payment, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orders {
		for _, p := range o.Payments {
			if p.PaymentID == paymentID {
				return cloneOrder(o), p, true
			}
		}
	}
	return Order{}, Payment{}, false
}

// OrderIDs lists every saved order ID, to seed the order ID generator.
func (s *Store) OrderIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, len(s.orders))
	for i, o := range s.orders {
		ids[i] = o.ID
	}
	return ids
}

// update runs change, then saves. If either fails, memory goes back to how
// it was, so memory and disk always match. The caller holds s.mu.
func (s *Store) update(change func() error) error {
	before := cloneOrders(s.orders)
	if err := change(); err != nil {
		s.orders = before
		return err
	}
	if err := s.save(); err != nil {
		s.orders = before
		return fmt.Errorf("save store: %w", err)
	}
	return nil
}

// save writes a temp file in the same folder, syncs it, then renames it over
// the real file. A rename in one folder is atomic, so a crash leaves the old
// file or the new one, never half of one.
func (s *Store) save() error {
	orders := s.orders
	if orders == nil {
		orders = []Order{}
	}
	data, err := json.MarshalIndent(storeFile{Orders: orders}, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".orders-*.tmp") // created with mode 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func (s *Store) indexOf(id string) int {
	return slices.IndexFunc(s.orders, func(o Order) bool { return o.ID == id })
}

// cloneOrder copies the slices too, so callers can't change stored data
// through a returned order (same reason as Basket.clone).
func cloneOrder(o Order) Order {
	o.Items = slices.Clone(o.Items)
	o.Payments = slices.Clone(o.Payments)
	return o
}

func cloneOrders(orders []Order) []Order {
	if orders == nil {
		return nil
	}
	out := make([]Order, len(orders))
	for i, o := range orders {
		out[i] = cloneOrder(o)
	}
	return out
}
