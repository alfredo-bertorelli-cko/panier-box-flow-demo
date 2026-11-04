package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// decodeJSON turns bytes into generic Go values (maps, slices, float64...),
// so two bodies compare by content, not by key order or spacing.
func decodeJSON(t *testing.T, label string, data []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("%s: not valid JSON: %v", label, err)
	}
	return v
}

// diffKeys lists top-level keys that are missing, extra or different, so a
// failure says what changed instead of dumping two whole bodies.
func diffKeys(want, got map[string]any) []string {
	var out []string
	for k, w := range want {
		g, ok := got[k]
		switch {
		case !ok:
			out = append(out, "missing "+k)
		case !reflect.DeepEqual(w, g):
			out = append(out, "different "+k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			out = append(out, "extra "+k)
		}
	}
	slices.Sort(out)
	return out
}

// Contract test: the exact JSON body the SDK sends for each session equals the
// golden file in testdata/. Any extra, missing or changed field fails, which
// catches a silently dropped capture false or an added zero value (D-016).
func TestSessionRequestContract(t *testing.T) {
	tests := []struct {
		basket, country, golden string
	}{
		{basketSubscription, "FR", "session_subscription_fr.json"},
		{basketOneOff, "FR", "session_oneoff_fr.json"},
		{basketOneOff, "PT", "session_oneoff_pt.json"},
	}
	for _, tt := range tests {
		t.Run(tt.golden, func(t *testing.T) {
			fake := newFakeCheckout(t, http.StatusCreated, `{"id":"ps_fake"}`)
			api := fake.client(t, nil)
			o, b := orderFor(t, tt.basket, tt.country)
			req, err := buildSessionRequest(o, b, testSettings)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := api.CreateSession(context.Background(), req); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}

			reqs := fake.recorded()
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want 1", len(reqs))
			}
			r := reqs[0]
			if r.Method != http.MethodPost || r.Path != "/payment-sessions" || r.Host != "fakesub1.api.test" {
				t.Errorf("got %s %s on %s", r.Method, r.Path, r.Host)
			}
			// No idempotency key on sessions: each page load is a new order and session.
			assertOnlyHeaders(t, r.Header, "Accept", "Accept-Encoding", "Authorization", "Content-Length", "Content-Type", "User-Agent")
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type %q", ct)
			}

			goldenBytes, err := os.ReadFile(filepath.Join("testdata", tt.golden))
			if err != nil {
				t.Fatal(err)
			}
			want := decodeJSON(t, tt.golden, goldenBytes)
			got := decodeJSON(t, "sent body", r.Body)
			if d := diffKeys(want, got); len(d) > 0 {
				t.Errorf("body differs from %s: %v\nsent: %s", tt.golden, d, r.Body)
			}

			// The fields that matter most, by name, so a failure is easy to read.
			if c, ok := got["capture"]; !ok || c != false {
				t.Errorf(`"capture" must be present and false, got %v (present %v)`, c, ok)
			}
			for _, k := range []string{"items", "merchant_initiated", "amount_allocations"} {
				if _, ok := got[k]; ok {
					t.Errorf("%q must not be sent", k)
				}
			}
		})
	}
}

// Proves the SDK maps the session response, and keeps the exact raw bytes,
// including a field its struct doesn't know. The handler (step 2.4) returns
// those bytes so the browser gets the session unchanged.
func TestSessionResponseKeptRaw(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "session_response.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeCheckout(t, http.StatusCreated, string(raw))
	api := fake.client(t, nil)
	o, b := orderFor(t, basketSubscription, "FR")
	req, _ := buildSessionRequest(o, b, testSettings)

	resp, err := api.CreateSession(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if resp.Id != "ps_fake2a1b3c4d5e6f7g8h9i0j1k2l" || resp.PaymentSessionToken == "" || resp.PaymentSessionSecret == "" {
		t.Errorf("typed fields not mapped: id %q", resp.Id)
	}
	if resp.HttpMetadata.StatusCode != http.StatusCreated {
		t.Errorf("status %d, want 201", resp.HttpMetadata.StatusCode)
	}
	if !bytes.Equal(resp.HttpMetadata.ResponseBody, raw) {
		t.Error("raw response body is not byte-for-byte what the server sent")
	}

	// Re-encoding the typed struct would lose the unknown field. This is why
	// the handler must return the raw bytes, not the struct.
	reencoded, _ := json.Marshal(resp)
	if bytes.Contains(reencoded, []byte("field_the_sdk_does_not_know")) {
		t.Error("expected the typed struct to drop unknown fields; if not, revisit the 2.4 design")
	}
}
