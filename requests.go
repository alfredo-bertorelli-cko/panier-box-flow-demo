package main

import (
	"fmt"

	"github.com/checkout/checkout-sdk-go/v3/common"
	"github.com/checkout/checkout-sdk-go/v3/payments"
	sessions "github.com/checkout/checkout-sdk-go/v3/payments/sessions"
)

// sessionSettings is the slice of Config a request builder needs. Never the
// secret key: builders don't call anything.
type sessionSettings struct {
	ProcessingChannelID string
	PublicBaseURL       string
}

func sessionSettingsFrom(cfg Config) sessionSettings {
	return sessionSettings{ProcessingChannelID: cfg.ProcessingChannelID, PublicBaseURL: cfg.PublicBaseURL}
}

// Demo customer (PRD.md section 7).
var demoCustomer = common.CustomerRequest{Name: "Marie Dubois", Email: "marie.dubois@example.com"}

// demoBilling is Marie's billing address per country. MB WAY needs billing
// country PT, so the PT one-off uses a Lisbon address (D-007).
var demoBilling = map[string]common.Address{
	"FR": {AddressLine1: "12 rue Oberkampf", City: "Paris", Zip: "75011", Country: common.FR},
	"PT": {AddressLine1: "Rua Augusta 100", City: "Lisboa", Zip: "1100-053", Country: common.PT},
}

// buildSessionRequest turns an order into a Flow payment session request.
// Every amount comes from the server-side order, never from the browser.
func buildSessionRequest(o Order, b Basket, s sessionSettings) (sessions.PaymentSessionsRequest, error) {
	if o.Basket != b.ID {
		return sessions.PaymentSessionsRequest{}, fmt.Errorf("session for %s: order basket %q, got basket %q", o.ID, o.Basket, b.ID)
	}
	if total := (Basket{Items: o.Items}).Total(); total != o.Amount {
		return sessions.PaymentSessionsRequest{}, fmt.Errorf("session for %s: items add up to %d, order amount is %d", o.ID, total, o.Amount)
	}
	billing, ok := demoBilling[o.Country]
	if !ok {
		return sessions.PaymentSessionsRequest{}, fmt.Errorf("session for %s: no billing address for country %q", o.ID, o.Country)
	}

	// Local copies: the request holds pointers, and pointing at the package
	// vars would let one request change the demo data for every later one.
	customer := demoCustomer
	notCaptured := false
	req := sessions.PaymentSessionsRequest{
		Amount:              o.Amount,
		Currency:            common.Currency(o.Currency),
		Reference:           o.ID,
		PaymentType:         payments.Regular,
		ProcessingChannelId: s.ProcessingChannelID,
		// Authorise only. The API default is capture true, so false must be
		// sent explicitly. Capture happens at dispatch (F5).
		Capture:               &notCaptured,
		ThreeDsRequest:        &payments.ThreeDsRequestFlowHostedLinks{Enabled: true},
		Locale:                payments.EnGBLT,
		Billing:               &payments.BillingInformation{Address: &billing},
		Customer:              &customer,
		EnabledPaymentMethods: paymentMethods(b, o.Country),
		SuccessUrl:            s.PublicBaseURL + "/success",
		FailureUrl:            s.PublicBaseURL + "/?payment=failed",
		DisplayName:           "Panier Box",
		// No items: optional, not shown by Flow, and the SDK adds an undefined
		// "price": 0 to each one (D-016). The order keeps them for the page.
	}
	if b.Recurring {
		// First payment of the series. Customer-initiated is the default, so
		// there's no merchant_initiated field (D-003).
		req.PaymentType = payments.Recurring
		// Ask the issuer for a challenge on the CIT that sets up the mandate.
		// Without a strongly authenticated first payment, issuers can decline
		// later MITs with 20154, 3DS required (D-022).
		req.ThreeDsRequest.ChallengeIndicator = common.ChallengeRequestedMandate
	}
	return req, nil
}

// paymentMethods are method names, not card schemes: Cartes Bancaires comes
// through "card". Alma and MB WAY don't support recurring, so they're one-off only.
func paymentMethods(b Basket, country string) []sessions.PaymentMethodsType {
	methods := []sessions.PaymentMethodsType{sessions.CardPMT, sessions.GooglepayPMT}
	if b.Recurring {
		return methods
	}
	switch country {
	case "FR":
		methods = append(methods, sessions.AlmaPMT)
	case "PT":
		methods = append(methods, sessions.MbwayPMT)
	}
	return methods
}
