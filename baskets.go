package main

import (
	"fmt"
	"slices"
)

// Item is one basket line. Prices are in minor units (3000 = EUR 30.00).
// AddOn marks one-off grocery extras on top of the box: charged once with the
// first order, never part of the weekly renewal.
type Item struct {
	Name      string `json:"name"`
	UnitPrice int64  `json:"unit_price"`
	Quantity  int    `json:"quantity"`
	AddOn     bool   `json:"add_on,omitempty"`
}

// Basket is server-side demo data. The browser only names a basket and a
// country. Every amount we send to Checkout.com comes from here.
type Basket struct {
	ID       string
	Currency string
	Items    []Item
	// Recurring marks the first payment of a subscription series. It decides
	// payment_type on the session (Recurring vs Regular) and whether renewals
	// are allowed for the order.
	Recurring bool
	// Countries the basket can be bought from. Subscriptions are FR only.
	// The one-off box also allows PT, for MB WAY (D-007).
	Countries []string
}

// Total is always computed from the items, so it can't drift from them.
func (b Basket) Total() int64 {
	var total int64
	for _, it := range b.Items {
		total += it.UnitPrice * int64(it.Quantity)
	}
	return total
}

const (
	basketSubscription = "subscription"
	basketOneOff       = "oneoff"
)

// checkoutBaskets are the baskets the browser can ask for by ID.
// The renewal is deliberately not here: it's never a checkout session.
var checkoutBaskets = map[string]Basket{
	basketSubscription: {
		ID:       basketSubscription,
		Currency: "EUR",
		Items: []Item{
			{Name: "Weekly box", UnitPrice: 3000, Quantity: 1},
			{Name: "Sourdough loaf", UnitPrice: 600, Quantity: 1, AddOn: true},
			{Name: "Comté 200 g", UnitPrice: 800, Quantity: 1, AddOn: true},
			{Name: "Seasonal fruit", UnitPrice: 600, Quantity: 1, AddOn: true},
		},
		Recurring: true,
		Countries: []string{"FR"},
	},
	basketOneOff: {
		ID:       basketOneOff,
		Currency: "EUR",
		Items: []Item{
			{Name: "Christmas box", UnitPrice: 4500, Quantity: 1},
		},
		Recurring: false,
		Countries: []string{"FR", "PT"},
	},
}

// LookupBasket returns a copy of a checkout basket, checked against the country.
func LookupBasket(id, country string) (Basket, error) {
	b, ok := checkoutBaskets[id]
	if !ok {
		return Basket{}, fmt.Errorf("unknown basket %q", id)
	}
	if !slices.Contains(b.Countries, country) {
		return Basket{}, fmt.Errorf("basket %q is not sold in country %q", id, country)
	}
	return b.clone(), nil
}

// RenewalBasket is next week's box for the MIT. Add-ons were a one-time
// extra on the first order, so the renewal is the weekly box alone.
func RenewalBasket() Basket {
	return Basket{
		ID:       "renewal",
		Currency: "EUR",
		Items: []Item{
			{Name: "Weekly box", UnitPrice: 3000, Quantity: 1},
		},
		Recurring: true,
		Countries: []string{"FR"},
	}
}

// clone copies the slices too. Slices share their backing array, so without
// this a caller editing an item would change the demo data for every later request.
func (b Basket) clone() Basket {
	b.Items = slices.Clone(b.Items)
	b.Countries = slices.Clone(b.Countries)
	return b
}
