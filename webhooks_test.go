package main

import (
	"strings"
	"testing"
)

// A fake key and a webhook-shaped body. The expected signature was computed
// outside Go, with Python's hmac module:
//
//	hmac.new(b"fake-webhook-signature-key", body, hashlib.sha256).hexdigest()
//
// so this checks our code against an independent implementation, not Go's
// crypto against itself.
const (
	testWebhookKey  = "fake-webhook-signature-key"
	testWebhookBody = `{"id":"evt_fakeaaaaaaaaaaaaaaaaaaaaaaaa","type":"payment_captured","data":{"id":"pay_fakeaaaaaaaaaaaaaaaaaaaaaa","reference":"ORD-1042"}}`
	testWebhookSig  = "64eafa711f8ed9e4b6acf09e2d45026198c35605e71b93d93d563fb77f6fe202"
)

// Proves a genuine webhook passes, in either hex case: we compare decoded
// bytes, not strings.
func TestVerifySignatureValid(t *testing.T) {
	for _, header := range []string{testWebhookSig, strings.ToUpper(testWebhookSig), " " + testWebhookSig + " "} {
		if !verifySignature([]byte(testWebhookBody), header, testWebhookKey) {
			t.Errorf("valid signature %q refused", header)
		}
	}
}

// Proves anything else fails. A one-character body change is the important
// one: the handler must verify the raw bytes, never re-encoded JSON.
func TestVerifySignatureRejects(t *testing.T) {
	tests := []struct {
		name, body, header, key string
	}{
		{"body changed by one character", strings.Replace(testWebhookBody, "1042", "1043", 1), testWebhookSig, testWebhookKey},
		{"body with an added space", testWebhookBody + " ", testWebhookSig, testWebhookKey},
		{"body re-encoded with spaces", strings.ReplaceAll(testWebhookBody, `":`, `": `), testWebhookSig, testWebhookKey},
		{"wrong key", testWebhookBody, testWebhookSig, "another-fake-key"},
		{"empty key", testWebhookBody, testWebhookSig, ""},
		{"missing header", testWebhookBody, "", testWebhookKey},
		{"not hex", testWebhookBody, "not-a-signature", testWebhookKey},
		{"too short", testWebhookBody, testWebhookSig[:62], testWebhookKey},
		{"base64 of something", testWebhookBody, "ZmFrZQ==", testWebhookKey},
	}
	for _, tt := range tests {
		if verifySignature([]byte(tt.body), tt.header, tt.key) {
			t.Errorf("%s: accepted", tt.name)
		}
	}
}
