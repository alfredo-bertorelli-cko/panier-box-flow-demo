//go:build sandbox

// Sandbox tests make real calls to the Checkout.com sandbox with the keys in
// .env. They only build with -tags sandbox, so `go test ./...` never runs
// them. Alfredo runs them by hand. They log IDs and statuses only, never keys,
// session tokens or secrets.

package main

import (
	"context"
	"errors"
	"os"
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
