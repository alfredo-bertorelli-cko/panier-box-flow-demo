package main

import (
	"slices"
	"testing"
)

// mustLookup is a test helper: a basket the test expects to exist.
func mustLookup(t *testing.T, id, country string) Basket {
	t.Helper()
	b, err := LookupBasket(id, country)
	if err != nil {
		t.Fatalf("LookupBasket(%q, %q): %v", id, country, err)
	}
	return b
}

// Proves each basket totals the PRD amount, is in EUR, and has the right recurring flag.
func TestBasketTotalsAndFlags(t *testing.T) {
	tests := []struct {
		name          string
		basket        Basket
		wantTotal     int64
		wantRecurring bool
	}{
		{"subscription first box", mustLookup(t, basketSubscription, "FR"), 5000, true},
		{"one-off seasonal box", mustLookup(t, basketOneOff, "FR"), 4500, false},
		{"weekly renewal", RenewalBasket(), 3000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.basket.Total(); got != tt.wantTotal {
				t.Errorf("Total() = %d, want %d", got, tt.wantTotal)
			}
			if tt.basket.Recurring != tt.wantRecurring {
				t.Errorf("Recurring = %v, want %v", tt.basket.Recurring, tt.wantRecurring)
			}
			if tt.basket.Currency != "EUR" {
				t.Errorf("Currency = %q, want EUR", tt.basket.Currency)
			}
		})
	}
}

// Proves the subscription items match PRD section 7 line by line. A wrong
// price would still be a valid number, so check each one, not only the total.
func TestSubscriptionItemsMatchPRD(t *testing.T) {
	want := []Item{
		{Name: "Weekly box, 2 people, 3 recipes", UnitPrice: 3000, Quantity: 1},
		{Name: "Sourdough loaf", UnitPrice: 600, Quantity: 1},
		{Name: "Comté 200 g", UnitPrice: 800, Quantity: 1},
		{Name: "Seasonal fruit", UnitPrice: 600, Quantity: 1},
	}
	got := mustLookup(t, basketSubscription, "FR").Items
	if !slices.Equal(got, want) {
		t.Errorf("items = %+v, want %+v", got, want)
	}
}

// Proves Total multiplies by quantity, not only sums unit prices.
func TestTotalUsesQuantity(t *testing.T) {
	b := Basket{Items: []Item{{UnitPrice: 600, Quantity: 2}, {UnitPrice: 800, Quantity: 1}}}
	if got := b.Total(); got != 2000 {
		t.Errorf("Total() = %d, want 2000", got)
	}
}

// Proves only known checkout baskets can be looked up. "renewal" is not one:
// the browser must never start a session for it.
func TestLookupUnknownBasket(t *testing.T) {
	for _, id := range []string{"", "renewal", "Subscription", "oneOff", "christmas"} {
		t.Run(id, func(t *testing.T) {
			if _, err := LookupBasket(id, "FR"); err == nil {
				t.Errorf("LookupBasket(%q) gave no error", id)
			}
		})
	}
}

// Proves the country rules: subscriptions FR only, one-off FR or PT.
func TestLookupCountry(t *testing.T) {
	tests := []struct {
		basket  string
		country string
		wantOK  bool
	}{
		{basketSubscription, "FR", true},
		{basketSubscription, "PT", false},
		{basketOneOff, "FR", true},
		{basketOneOff, "PT", true},
		{basketOneOff, "ES", false},
		{basketOneOff, "", false},
		{basketOneOff, "fr", false}, // codes are upper case, as Checkout.com expects
	}
	for _, tt := range tests {
		t.Run(tt.basket+"/"+tt.country, func(t *testing.T) {
			_, err := LookupBasket(tt.basket, tt.country)
			if (err == nil) != tt.wantOK {
				t.Errorf("err = %v, want ok=%v", err, tt.wantOK)
			}
		})
	}
}

// Proves a looked-up basket is a copy: editing it doesn't change the next lookup.
func TestLookupReturnsCopy(t *testing.T) {
	first := mustLookup(t, basketSubscription, "FR")
	first.Items[0].UnitPrice = 1
	first.Items = append(first.Items, Item{Name: "Extra", UnitPrice: 999, Quantity: 1})
	first.Countries[0] = "PT"

	second := mustLookup(t, basketSubscription, "FR")
	if second.Total() != 5000 || len(second.Items) != 4 || second.Countries[0] != "FR" {
		t.Errorf("shared demo data was changed by an earlier caller")
	}
}
