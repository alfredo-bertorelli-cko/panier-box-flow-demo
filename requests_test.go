package main

import (
	"slices"
	"testing"

	"github.com/checkout/checkout-sdk-go/v3/common"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

var testSettings = sessionSettings{ProcessingChannelID: "pc_fake_channel_42", PublicBaseURL: "https://demo.example.dev"}

// orderFor builds an order from a real basket, as the handler will.
func orderFor(t *testing.T, basket, country string) (Order, Basket) {
	t.Helper()
	b := mustLookup(t, basket, country)
	return NewOrder("ORD-1042", b, country, fixedNow), b
}

// Proves each basket and country gets the session PRD F1 and F9 describe.
func TestBuildSessionRequest(t *testing.T) {
	tests := []struct {
		name          string
		basket        string
		country       string
		wantAmount    int64
		wantType      payments.PaymentType
		wantMethods   []sessions.PaymentMethodsType
		wantChallenge common.ChallengeIndicator
		wantCountry   common.Country
	}{
		{"subscription FR", basketSubscription, "FR", 5000, payments.Recurring,
			[]sessions.PaymentMethodsType{"card", "googlepay"}, common.ChallengeRequestedMandate, common.FR},
		{"one-off FR", basketOneOff, "FR", 4500, payments.Regular,
			[]sessions.PaymentMethodsType{"card", "googlepay", "alma"}, "", common.FR},
		{"one-off PT", basketOneOff, "PT", 4500, payments.Regular,
			[]sessions.PaymentMethodsType{"card", "googlepay", "mbway"}, "", common.PT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, b := orderFor(t, tt.basket, tt.country)
			req, err := buildSessionRequest(o, b, testSettings)
			if err != nil {
				t.Fatalf("buildSessionRequest: %v", err)
			}

			// Money: amount from the basket, authorise only.
			if req.Amount != tt.wantAmount || req.Currency != common.EUR {
				t.Errorf("amount %d %s, want %d EUR", req.Amount, req.Currency, tt.wantAmount)
			}
			if req.Capture == nil || *req.Capture {
				t.Error("capture must be set and false: the API default is true")
			}
			if req.PaymentType != tt.wantType {
				t.Errorf("payment_type %q, want %q", req.PaymentType, tt.wantType)
			}

			// 3DS on, and the mandate challenge only on the recurring CIT.
			if req.ThreeDsRequest == nil || !req.ThreeDsRequest.Enabled {
				t.Fatal("3DS must be enabled")
			}
			if req.ThreeDsRequest.ChallengeIndicator != tt.wantChallenge {
				t.Errorf("challenge_indicator %q, want %q", req.ThreeDsRequest.ChallengeIndicator, tt.wantChallenge)
			}

			if !slices.Equal(req.EnabledPaymentMethods, tt.wantMethods) {
				t.Errorf("methods %v, want %v", req.EnabledPaymentMethods, tt.wantMethods)
			}
			if req.Billing == nil || req.Billing.Address == nil || req.Billing.Address.Country != tt.wantCountry {
				t.Errorf("billing country, want %s", tt.wantCountry)
			}

			// Identity and redirects.
			if req.Reference != "ORD-1042" || req.ProcessingChannelId != "pc_fake_channel_42" {
				t.Errorf("reference %q channel %q", req.Reference, req.ProcessingChannelId)
			}
			if req.SuccessUrl != "https://demo.example.dev/success" || req.FailureUrl != "https://demo.example.dev/?payment=failed" {
				t.Errorf("urls %q %q", req.SuccessUrl, req.FailureUrl)
			}
			if req.Locale != "en-GB" || req.DisplayName != "Panier Box" {
				t.Errorf("locale %q display %q", req.Locale, req.DisplayName)
			}
			if req.Customer == nil || req.Customer.Name != "Marie Dubois" || req.Customer.Email != "marie.dubois@example.com" {
				t.Errorf("customer %+v", req.Customer)
			}

			// Items are left out on purpose (D-016). The order still has them.
			if req.Items != nil {
				t.Errorf("items sent: %v", req.Items)
			}
			if len(o.Items) == 0 {
				t.Error("order lost its items")
			}
		})
	}
}

// Proves the FR billing address is Marie's Paris address, line by line.
func TestBuildSessionRequestParisAddress(t *testing.T) {
	o, b := orderFor(t, basketSubscription, "FR")
	req, _ := buildSessionRequest(o, b, testSettings)
	a := req.Billing.Address
	if a.AddressLine1 != "12 rue Oberkampf" || a.City != "Paris" || a.Zip != "75011" {
		t.Errorf("address %+v", a)
	}
}

// Proves a broken order never becomes a request: items not adding up to the
// amount, a mismatched basket, or a country with no billing address.
func TestBuildSessionRequestRefusesBadOrders(t *testing.T) {
	tests := []struct {
		name   string
		change func(o *Order, b *Basket)
	}{
		{"amount tampered", func(o *Order, b *Basket) { o.Amount = 1 }},
		{"item price tampered", func(o *Order, b *Basket) { o.Items[0].UnitPrice = 1 }},
		{"basket mismatch", func(o *Order, b *Basket) { *b = mustLookup(t, basketOneOff, "FR") }},
		{"unknown country", func(o *Order, b *Basket) { o.Country = "ES" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, b := orderFor(t, basketSubscription, "FR")
			tt.change(&o, &b)
			if _, err := buildSessionRequest(o, b, testSettings); err == nil {
				t.Error("want error")
			}
		})
	}
}

// Proves two requests never share the customer or billing data: changing one
// mustn't change the next (they point at package-level demo data).
func TestBuildSessionRequestDoesNotShareDemoData(t *testing.T) {
	o, b := orderFor(t, basketSubscription, "FR")
	first, _ := buildSessionRequest(o, b, testSettings)
	first.Billing.Address.City = "Lyon"
	first.Customer.Name = "Someone Else"

	second, _ := buildSessionRequest(o, b, testSettings)
	if second.Billing.Address.City != "Paris" || second.Customer.Name != "Marie Dubois" {
		t.Errorf("demo data changed through an earlier request")
	}
}
