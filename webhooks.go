package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Webhooks from Checkout.com (PRD F8, step 5.x).

// signatureHeader carries the webhook signature. Checkout.com sends it on
// every webhook ("Verify a webhook was sent by Checkout.com").
const signatureHeader = "Cko-Signature"

// verifySignature tells a real Checkout.com webhook from a forged one: the
// header must be the HMAC-SHA256 of the raw body, keyed with the webhook's
// own signature key (set on the webhook in the Dashboard, not the account
// secret key).
//
// body must be the exact bytes received. Decoding and re-encoding the JSON
// can change them (key order, number precision, characters like ©), and
// then no signature matches. That's the most common cause of failures in
// Checkout.com's support article.
//
// The docs don't name the encoding. Hex, per PRD F8, is confirmed with a
// real webhook in step 5.5 (D-037). The comparison is on decoded bytes with
// hmac.Equal, which takes the same time whatever matches, so the response
// time can't leak how close a forged signature was. Never log the header,
// the key or the body: a failure is only "bad signature".
func verifySignature(body []byte, header, key string) bool {
	if key == "" {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(header))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
