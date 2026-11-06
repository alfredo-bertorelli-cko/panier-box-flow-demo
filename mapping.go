package main

import (
	"slices"
	"strings"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// mapPayment turns GET payment details plus GET payment actions into our
// status card data. Pure: no store, no network. Status always comes from these
// GETs, never from a webhook body (D-009). actions can be nil: the card then
// shows no response code.
func mapPayment(resp *paymentsnas.GetPaymentResponse, actions []paymentsnas.PaymentAction, now time.Time) Payment {
	p := Payment{
		PaymentID: resp.Id,
		Kind:      kindFromReference(resp.Reference),
		Reference: resp.Reference,
		Amount:    resp.Amount,
		Status:    string(resp.Status),
		UpdatedAt: now.UTC(),
	}
	if resp.ExpiresOn != nil {
		p.ExpiresOn = resp.ExpiresOn.UTC()
	}
	if b := resp.Balances; b != nil {
		p.Balances = Balances{
			Authorized: b.TotalAuthorized,
			Captured:   b.TotalCaptured,
			Voided:     b.TotalVoided,
			Refunded:   b.TotalRefunded,
		}
	}
	if t := resp.ThreeDs; t != nil {
		p.ThreeDS = t.AuthenticationResponse
	}
	if c := cardSource(resp); c != nil {
		p.Method = PaymentMethod{
			Type:        string(c.Type),
			CardSchemes: cardSchemes(c),
			Last4:       c.Last4,
			CardType:    string(c.CardType),
			Wallet:      string(c.CardWalletType),
		}
	} else {
		p.Method.Type = alternativeSourceType(resp)
	}
	p.Method.Scheme = processedScheme(resp)
	p.ResponseCode, p.ResponseSummary = authorizationResult(actions)
	return p
}

// citDetails gives what every renewal needs from the first payment: the stored
// card (source.id) and the scheme it was authorised on. An empty source ID is
// returned as empty, not an error: the success page then shows that renewals
// can't run (D-044).
func citDetails(resp *paymentsnas.GetPaymentResponse) (sourceID, scheme string) {
	if c := cardSource(resp); c != nil {
		sourceID = c.Id
	}
	return sourceID, processedScheme(resp)
}

// processedScheme is processing.scheme: the network the payment was actually
// authorised on. On a co-badged card this is the scheme Marie chose, so it is
// what renewals send as processing.preferred_scheme. source.scheme and
// source.local_schemes only describe the card, not her choice. Values are
// cartes_bancaires, visa or mastercard, the same spelling preferred_scheme uses.
func processedScheme(resp *paymentsnas.GetPaymentResponse) string {
	if resp.ProcessingData == nil {
		return ""
	}
	return normaliseScheme(resp.ProcessingData.Scheme)
}

// cardSchemes lists the card's own brands for the badges: source.scheme
// ("Visa") plus source.local_schemes (a co-badged card adds
// "cartes_bancaires"). Display only: when processing.scheme is empty (say,
// 3DS failed) the card still shows its brands.
func cardSchemes(c *paymentsnas.ResponseCardSource) []string {
	var out []string
	for _, s := range append([]string{c.Scheme}, c.LocalSchemes...) {
		if n := normaliseScheme(s); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// normaliseScheme gives the preferred_scheme spelling: "Cartes Bancaires"
// becomes cartes_bancaires, "Visa" becomes visa.
func normaliseScheme(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(s)
}

func cardSource(resp *paymentsnas.GetPaymentResponse) *paymentsnas.ResponseCardSource {
	if resp.Source == nil {
		return nil
	}
	return resp.Source.ResponseCardSource
}

// alternativeSourceType names a non-card source, like "mbway". The SDK decodes
// any source it has no struct for into a plain map, so read "type" from it.
func alternativeSourceType(resp *paymentsnas.GetPaymentResponse) string {
	if resp.Source == nil || resp.Source.AlternativeResponse == nil {
		return ""
	}
	t, _ := (*resp.Source.AlternativeResponse)["type"].(string)
	return t
}

// kindFromReference: we build every reference ourselves, so it tells CIT from
// MIT. ORD-1042 is the first payment, ORD-1042-W2 a renewal. Anything else
// (another test on the same sandbox) is unknown.
func kindFromReference(ref string) string {
	if _, ok := parseOrderNumber(ref); ok {
		return kindCIT
	}
	if _, ok := OrderIDFromReference(ref); ok {
		return kindMIT
	}
	return ""
}

// authorizationResult takes the response code and summary from the
// Authorization action in GET payment actions. Payment details have no
// top-level response_code, and by pay_ ID no actions either (D-027). Actions
// list newest first, so after a capture or refund the first action is not
// the authorisation.
func authorizationResult(actions []paymentsnas.PaymentAction) (code, summary string) {
	for _, a := range actions {
		// The SDK names the "Authorization" action type AuthorizationYes.
		if a.Type == payments.AuthorizationYes {
			return a.ResponseCode, a.ResponseSummary
		}
	}
	if len(actions) > 0 {
		return actions[0].ResponseCode, actions[0].ResponseSummary
	}
	return "", ""
}
