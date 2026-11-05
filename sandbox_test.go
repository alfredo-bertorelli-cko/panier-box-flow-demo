//go:build sandbox

// Sandbox tests make real calls to the Checkout.com sandbox with the keys in
// .env. They only build with -tags sandbox, so `go test ./...` never runs
// them. Alfredo runs them by hand. They log IDs and statuses only, never keys,
// session tokens or secrets.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/checkout/checkout-sdk-go/v3/configuration"
	ckoerrors "github.com/checkout/checkout-sdk-go/v3/errors"
)

// sandboxAPI loads .env and builds the real client on the sandbox.
func sandboxAPI(t *testing.T) (PaymentsAPI, Config) {
	t.Helper()
	if err := loadDotEnv(".env"); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(os.Getenv)
	if err != nil {
		t.Fatal(err) // names missing or invalid variables only
	}
	api, err := newPaymentsAPI(cfg, configuration.Sandbox(), nil, newSafeLogger(os.Stderr))
	if err != nil {
		t.Fatal(err)
	}
	return api, cfg
}

// failWithAPIError reports a failed call with status and error codes only.
func failWithAPIError(t *testing.T, err error) {
	t.Helper()
	var apiErr ckoerrors.CheckoutAPIError
	if errors.As(err, &apiErr) && apiErr.Data != nil {
		t.Fatalf("%v, error_type %q, error_codes %v, request_id %s",
			err, apiErr.Data.ErrorType, apiErr.Data.ErrorCodes, apiErr.Data.RequestID)
	}
	t.Fatal(err)
}

// Proves the sandbox accepts our subscription session as built (step 2.5):
// the keys, subdomain and processing channel work, and the body from the
// golden file is valid, including boolean 3ds.enabled and capture false (D-022).
//
// Run: go test -tags sandbox -run TestSandboxCreateSession -v ./...
func TestSandboxCreateSession(t *testing.T) {
	api, cfg := sandboxAPI(t)

	b := mustLookup(t, basketSubscription, "FR")
	// A reference that can't clash with demo orders in data/orders.json.
	o := NewOrder("ORD-SANDBOX-"+time.Now().UTC().Format("20060102T150405"), b, "FR", time.Now())
	req, err := buildSessionRequest(o, b, sessionSettingsFrom(cfg))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := api.CreateSession(ctx, req)
	if err != nil {
		failWithAPIError(t, err)
	}

	if !strings.HasPrefix(resp.Id, "ps_") {
		t.Errorf("session ID %q, want a ps_ prefix", resp.Id)
	}
	// Token and secret go to Flow in the browser. Check they exist, don't print them.
	if resp.PaymentSessionToken == "" || resp.PaymentSessionSecret == "" {
		t.Error("session token or secret is empty")
	}
	if _, ok := resp.Links["self"]; !ok {
		t.Error("no _links.self in the response")
	}
	t.Logf("session %s created for %s, HTTP %d", resp.Id, o.ID, resp.HttpMetadata.StatusCode)
}

// Saves one real payment as fixtures for the mapping tests: GET payment
// details and GET payment actions (steps 3.1 and 3.1b). It refuses to write a
// file holding anything key-like.
//
// Run: PAYMENT_ID=pay_... FIXTURE=cit_visa go test -tags sandbox -run TestSandboxDumpPayment -v ./...
// Writes testdata/sandbox/payment_<FIXTURE>.json and actions_<FIXTURE>.json
// (FIXTURE defaults to the payment ID).
func TestSandboxDumpPayment(t *testing.T) {
	id := os.Getenv("PAYMENT_ID")
	if err := checkPaymentID(id); err != nil {
		t.Fatalf("set PAYMENT_ID to a pay_ ID: %v", err)
	}
	name := os.Getenv("FIXTURE")
	if name == "" {
		name = id
	}
	if strings.ContainsAny(name, `/\. `) {
		t.Fatalf("FIXTURE %q: use letters, digits, - and _ only", name)
	}
	api, _ := sandboxAPI(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := api.GetPayment(ctx, id)
	if err != nil {
		failWithAPIError(t, err)
	}
	path := saveFixture(t, "payment_"+name, resp.HttpMetadata.ResponseBody)
	t.Logf("saved %s: status %s, processing.scheme %q", path, resp.Status, processedScheme(resp))

	// The response code is only on the actions (D-027). A payment that never
	// reached the issuer may have none: log it and keep the details file.
	acts, err := api.GetActions(ctx, id)
	if err != nil {
		var apiErr ckoerrors.CheckoutAPIError
		if errors.As(err, &apiErr) {
			t.Logf("no actions saved: HTTP %d", apiErr.StatusCode)
			return
		}
		t.Fatal(err)
	}
	path = saveFixture(t, "actions_"+name, acts.HttpMetadata.ResponseBody)
	code, summary := authorizationResult(acts.Actions)
	t.Logf("saved %s: %d actions, authorization %s %s", path, len(acts.Actions), code, summary)
}

// saveFixture writes a pretty-printed response body to testdata/sandbox/.
func saveFixture(t *testing.T, base string, raw []byte) string {
	t.Helper()
	// Same pattern the safe logger masks: sk_ or pk_ at a word start, or "Bearer ...".
	if keyShaped.Match(raw) {
		t.Fatalf("%s: response contains something key-shaped, not saving it", base)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.WriteString("\n")
	path := filepath.Join("testdata", "sandbox", base+".json")
	if err := os.WriteFile(path, pretty.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
