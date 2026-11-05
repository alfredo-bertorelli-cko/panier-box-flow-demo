package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/payments"
	paymentsnas "github.com/checkout/checkout-sdk-go/v3/payments/nas"
)

// loadPayment decodes a fixture through the SDK's own types, including its
// custom source unmarshalling, so the test sees what a real GET would give.
func loadPayment(t *testing.T, path string) *paymentsnas.GetPaymentResponse {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var resp paymentsnas.GetPaymentResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return &resp
}

// loadActions decodes a GET payment actions fixture (a bare JSON array, as the
// API sends it) through the SDK's own type. A missing file means no actions:
// nil, the same as a payment the actions call told us nothing about.
func loadActions(t *testing.T, path string) []paymentsnas.PaymentAction {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var resp paymentsnas.GetPaymentActionsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return resp.Actions
}

var mapNow = time.Date(2026, 11, 5, 10, 5, 0, 0, time.UTC)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Proves each state maps to the exact status card data. Fixtures in
// testdata/payments/ are built from the API reference with fake IDs, one per
// state, and shaped like the real sandbox responses (step 3.1b):
// ref_<state>.json is GET /payments/{id} (no actions, uppercase
// processing.scheme) and ref_<state>_actions.json is GET /payments/{id}/actions.
func TestMapPayment(t *testing.T) {
	visa := PaymentMethod{Type: "card", Scheme: "visa", CardSchemes: []string{"visa"}, Last4: "4242", CardType: "CREDIT"}
	tests := []struct {
		fixture string
		want    Payment
	}{
		{"ref_cit_authorized", Payment{
			PaymentID: "pay_fakecitaaaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Authorized", Method: visa, Balances: Balances{Authorized: 5000},
			ThreeDS: "Y", ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-12T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_cit_declined", Payment{
			PaymentID: "pay_fakecitaaaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Declined", Method: visa, ThreeDS: "Y",
			ResponseCode: "20051", ResponseSummary: "Insufficient Funds", UpdatedAt: mapNow,
		}},
		{"ref_cit_captured", Payment{
			PaymentID: "pay_fakecitaaaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Captured", Method: visa, Balances: Balances{Authorized: 5000, Captured: 5000},
			ThreeDS: "Y", ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-12T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_cit_voided", Payment{
			PaymentID: "pay_fakecitaaaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Voided", Method: visa, Balances: Balances{Authorized: 5000, Voided: 5000},
			ThreeDS: "Y", ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-12T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_cit_refunded", Payment{
			PaymentID: "pay_fakecitaaaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Refunded", Method: visa, Balances: Balances{Authorized: 5000, Captured: 5000, Refunded: 5000},
			ThreeDS: "Y", ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-12T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_cit_googlepay", Payment{
			PaymentID: "pay_fakegpayaaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Authorized", Balances: Balances{Authorized: 5000},
			Method:  PaymentMethod{Type: "card", Scheme: "mastercard", CardSchemes: []string{"mastercard"}, Last4: "4444", CardType: "DEBIT", Wallet: "googlepay"},
			ThreeDS: "Y", ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-12T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_mit_cobadged_cb", Payment{
			PaymentID: "pay_fakemitaaaaaaaaaaaaaaaaaaa", Kind: kindMIT, Reference: "ORD-1042-W2", Amount: 3000,
			Status: "Authorized", Balances: Balances{Authorized: 3000},
			// The card says Visa, but the payment ran on CB: processing.scheme wins.
			Method:       PaymentMethod{Type: "card", Scheme: "cartes_bancaires", CardSchemes: []string{"visa", "cartes_bancaires"}, Last4: "4242", CardType: "CREDIT"},
			ResponseCode: "10000", ResponseSummary: "Approved",
			ExpiresOn: at("2026-11-17T10:00:00Z"), UpdatedAt: mapNow,
		}},
		{"ref_cit_3ds_failed", Payment{
			PaymentID: "pay_fake3dsfailaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1042", Amount: 5000,
			Status: "Declined", ThreeDS: "N",
			// Never reached authorisation: no processing.scheme, but the card's
			// brands still show (D-026).
			Method:       PaymentMethod{Type: "card", CardSchemes: []string{"visa", "cartes_bancaires"}, Last4: "0027", CardType: "CREDIT"},
			ResponseCode: "20151", ResponseSummary: "Cardholder failed 3D-Secure authentication",
			UpdatedAt: mapNow,
		}},
		{"ref_minimal", Payment{
			PaymentID: "pay_fakeminaaaaaaaaaaaaaaaaaaa", Reference: "someone-elses-test", Amount: 4500,
			Status: "Pending", UpdatedAt: mapNow,
		}},
		{"ref_oneoff_mbway", Payment{
			PaymentID: "pay_fakembwayaaaaaaaaaaaaaaaaa", Kind: kindCIT, Reference: "ORD-1043", Amount: 4500,
			// MB WAY captures at payment, even on a capture false session (S-07).
			Status: "Captured", Balances: Balances{Authorized: 4500, Captured: 4500},
			// Not a card: the SDK gives a plain map, the type still shows.
			Method: PaymentMethod{Type: "mbway"},
			// Its only action is a Capture, no Authorization (sandbox, 2026-11-05).
			ResponseCode: "10000", ResponseSummary: "Approved",
			UpdatedAt: mapNow,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			dir := filepath.Join("testdata", "payments")
			resp := loadPayment(t, filepath.Join(dir, tt.fixture+".json"))
			actions := loadActions(t, filepath.Join(dir, tt.fixture+"_actions.json"))
			got := mapPayment(resp, actions, mapNow)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mapPayment:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// Proves the renewal inputs come from the right fields: source.id for the
// stored card, processing.scheme (not source.scheme) for the scheme.
func TestCITDetails(t *testing.T) {
	tests := []struct {
		fixture, wantSource, wantScheme string
	}{
		{"ref_cit_authorized", "src_fakecardaaaaaaaaaaaaaaaaaa", "visa"},
		{"ref_cit_googlepay", "src_fakecardaaaaaaaaaaaaaaaaaa", "mastercard"},
		{"ref_mit_cobadged_cb", "src_fakecardaaaaaaaaaaaaaaaaaa", "cartes_bancaires"},
		{"ref_cit_3ds_failed", "src_fake3dsfailaaaaaaaaaaaaaaa", ""}, // card attributes never drive renewals
		{"ref_minimal", "", ""},      // no source: renewals must be disabled, not crash (P-8)
		{"ref_oneoff_mbway", "", ""}, // not a card: nothing to renew on, and a one-off never renews
	}
	for _, tt := range tests {
		resp := loadPayment(t, filepath.Join("testdata", "payments", tt.fixture+".json"))
		src, scheme := citDetails(resp)
		if src != tt.wantSource || scheme != tt.wantScheme {
			t.Errorf("%s: got %q %q, want %q %q", tt.fixture, src, scheme, tt.wantSource, tt.wantScheme)
		}
	}
}

// Proves CIT vs MIT comes from the references we build, and anything else is unknown.
func TestKindFromReference(t *testing.T) {
	for ref, want := range map[string]string{
		"ORD-1042": kindCIT, "ORD-1042-W2": kindMIT, "ORD-1042-W13": kindMIT,
		"ORD-1042-W1": "", "other-test": "", "": "",
	} {
		if got := kindFromReference(ref); got != want {
			t.Errorf("kindFromReference(%q) = %q, want %q", ref, got, want)
		}
	}
}

// Proves card brands are normalised and de-duplicated, scheme first.
func TestCardSchemes(t *testing.T) {
	tests := []struct {
		scheme string
		local  []string
		want   []string
	}{
		{"Visa", nil, []string{"visa"}},
		{"Visa", []string{"cartes_bancaires"}, []string{"visa", "cartes_bancaires"}},
		{"Visa", []string{"Cartes Bancaires", "cartes_bancaires"}, []string{"visa", "cartes_bancaires"}},
		{"", nil, nil},
	}
	for _, tt := range tests {
		got := cardSchemes(&paymentsnas.ResponseCardSource{Scheme: tt.scheme, LocalSchemes: tt.local})
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("cardSchemes(%q, %v) = %v, want %v", tt.scheme, tt.local, got, tt.want)
		}
	}
}

// Proves processing.scheme is normalised to the preferred_scheme spelling.
func TestProcessedSchemeNormalised(t *testing.T) {
	for raw, want := range map[string]string{
		"visa": "visa", "Mastercard": "mastercard", " cartes_bancaires ": "cartes_bancaires", "": "",
		// What the sandbox really sends (step 3.1b).
		"VISA": "visa", "MASTERCARD": "mastercard", "CARTES_BANCAIRES": "cartes_bancaires",
	} {
		resp := &paymentsnas.GetPaymentResponse{ProcessingData: &payments.ProcessingData{Scheme: raw}}
		if got := processedScheme(resp); got != want {
			t.Errorf("processedScheme(%q) = %q, want %q", raw, got, want)
		}
	}
}

// Proves every real sandbox response in testdata/sandbox/ maps without
// surprises. Skips when there are none yet. As Alfredo adds files with
// TestSandboxDumpPayment, they're checked here automatically.
func TestMapSandboxFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "sandbox", "payment_*.json"))
	if len(files) == 0 {
		t.Skip("no sandbox fixtures yet")
	}
	known := map[string]bool{"": true, "visa": true, "mastercard": true, "cartes_bancaires": true}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			resp, actions := loadSandbox(t, sandboxName(f))
			p := mapPayment(resp, actions, mapNow)
			if !strings.HasPrefix(p.PaymentID, "pay_") || p.Status == "" {
				t.Errorf("payment ID %q, status %q", p.PaymentID, p.Status)
			}
			if !known[p.Method.Scheme] {
				t.Errorf("processing.scheme %q is not a preferred_scheme value", p.Method.Scheme)
			}
			// An approved card payment is what renewals run on: it must give
			// both renewal inputs (P-8).
			if resp.Approved && p.Method.Type == "card" {
				if src, scheme := citDetails(resp); !strings.HasPrefix(src, "src_") || scheme == "" {
					t.Errorf("approved card payment: source %q, scheme %q", src, scheme)
				}
			}
			// Once actions are dumped, every payment that reached the issuer has a code.
			if actions != nil && p.Method.Scheme != "" && p.ResponseCode == "" {
				t.Error("actions saved but no Authorization response code")
			}
		})
	}
}

// Proves what the real sandbox sends for each test in the step 3.1b run
// (2026-11-05). Each case skips if its file isn't there.
func TestSandboxFixtureShapes(t *testing.T) {
	type shape struct {
		Status, Type, Scheme string
		CardSchemes          []string
		Wallet, ThreeDS      string
		Last4                string
	}
	visa, mc := []string{"visa"}, []string{"mastercard"}
	cbVisa := []string{"visa", "cartes_bancaires"}
	tests := []struct {
		name     string
		want     shape
		wantCode string // checked only once actions_<name>.json is saved
	}{
		{"cit_visa", shape{"Authorized", "card", "visa", visa, "", "Y", "4242"}, "10000"},
		{"cit_mc", shape{"Authorized", "card", "mastercard", mc, "", "Y", "6378"}, "10000"},
		// Same co-badged card, two choices: processing.scheme follows Marie's
		// pick, the card's own brands don't move (P-11).
		{"cit_cb_chose_cb", shape{"Authorized", "card", "cartes_bancaires", cbVisa, "", "A", "0001"}, "10000"},
		{"cit_cb_chose_visa", shape{"Authorized", "card", "visa", cbVisa, "", "A", "0001"}, "10000"},
		{"cit_cb_mc_chose_cb", shape{"Authorized", "card", "cartes_bancaires", []string{"mastercard", "cartes_bancaires"}, "", "Y", "0018"}, "10000"},
		{"cit_googlepay", shape{"Authorized", "card", "visa", visa, "googlepay", "Y", "1387"}, "10000"},
		{"cit_declined", shape{"Declined", "card", "visa", visa, "", "Y", "3670"}, "20051"},
		// Never reached the issuer: no processing.scheme, the brands still show.
		// Checkout.com still records an Authorization action with the 3DS code.
		{"cit_3ds_failed", shape{"Declined", "card", "", cbVisa, "", "N", "0027"}, "20151"},
		{"cit_captured", shape{"Captured", "card", "visa", visa, "", "Y", "4242"}, "10000"},
		// Actions: Refund 10000, Refund 50003 (a failed first try), Capture,
		// Authorization. The card must still show the authorisation's 10000.
		{"cit_refunded", shape{"Refunded", "card", "visa", visa, "", "Y", "4242"}, "10000"},
		{"cit_voided", shape{"Voided", "card", "mastercard", mc, "", "Y", "6378"}, "10000"},
		// MB WAY has one action, a Capture, and no Authorization: the code
		// comes from the newest action instead.
		{"oneoff_mbway", shape{"Captured", "mbway", "", nil, "", "", ""}, "10000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, actions := loadSandbox(t, tt.name)
			p := mapPayment(resp, actions, mapNow)
			got := shape{p.Status, p.Method.Type, p.Method.Scheme, p.Method.CardSchemes, p.Method.Wallet, p.ThreeDS, p.Method.Last4}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("\n got %+v\nwant %+v", got, tt.want)
			}
			if actions != nil && p.ResponseCode != tt.wantCode {
				t.Errorf("response code %q, want %q", p.ResponseCode, tt.wantCode)
			}
		})
	}
}

// Proves the co-badged card is stored once and the scheme is a per-payment
// choice: same source.id, different processing.scheme. Renewals send the
// CIT's processing.scheme as preferred_scheme, so they follow Marie's pick.
func TestSandboxCoBadgedChoiceKeepsCard(t *testing.T) {
	cb, _ := loadSandbox(t, "cit_cb_chose_cb")
	visa, _ := loadSandbox(t, "cit_cb_chose_visa")
	cbSrc, cbScheme := citDetails(cb)
	visaSrc, visaScheme := citDetails(visa)
	if cbSrc != visaSrc {
		t.Errorf("source IDs differ: %s and %s", cbSrc, visaSrc)
	}
	if cbScheme != "cartes_bancaires" || visaScheme != "visa" {
		t.Errorf("schemes %q and %q, want cartes_bancaires and visa", cbScheme, visaScheme)
	}
}

// loadSandbox reads testdata/sandbox/payment_<name>.json and, if saved,
// actions_<name>.json. Skips the test when the payment file is missing. Fails
// on anything key-shaped: those files are committed.
func loadSandbox(t *testing.T, name string) (*paymentsnas.GetPaymentResponse, []paymentsnas.PaymentAction) {
	t.Helper()
	dir := filepath.Join("testdata", "sandbox")
	paymentFile := filepath.Join(dir, "payment_"+name+".json")
	actionsFile := filepath.Join(dir, "actions_"+name+".json")
	if _, err := os.Stat(paymentFile); err != nil {
		t.Skipf("no %s yet", paymentFile)
	}
	for _, f := range []string{paymentFile, actionsFile} {
		if data, err := os.ReadFile(f); err == nil && keyShaped.Match(data) {
			t.Fatalf("%s contains something key-shaped: remove it from testdata", f)
		}
	}
	return loadPayment(t, paymentFile), loadActions(t, actionsFile)
}

func sandboxName(paymentFile string) string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(paymentFile), "payment_"), ".json")
}

// Proves the code comes from the Authorization action, not the newest one.
// A declined capture lists first, but the card was approved: show 10000.
func TestAuthorizationResult(t *testing.T) {
	tests := []struct {
		name     string
		actions  []paymentsnas.PaymentAction
		wantCode string
	}{
		{"auth after a declined capture", []paymentsnas.PaymentAction{
			{Type: payments.Capture, ResponseCode: "20087", ResponseSummary: "Invalid capture"},
			{Type: payments.AuthorizationYes, ResponseCode: "10000", ResponseSummary: "Approved"},
		}, "10000"},
		{"no Authorization action: newest", []paymentsnas.PaymentAction{
			{Type: payments.CardVerification, ResponseCode: "10000", ResponseSummary: "Approved"},
		}, "10000"},
		{"no actions", nil, ""},
	}
	for _, tt := range tests {
		if code, _ := authorizationResult(tt.actions); code != tt.wantCode {
			t.Errorf("%s: code %q, want %q", tt.name, code, tt.wantCode)
		}
	}
}
