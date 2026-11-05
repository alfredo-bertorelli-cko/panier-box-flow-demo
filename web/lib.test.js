// Tests for web/lib.js. Run with: node --test web/
// Node's built-in runner, no npm. Expected strings are the UI spec's exact copy.
import { test } from "node:test";
import assert from "node:assert/strict";
import * as lib from "./lib.js";

// Server-shaped orders, as POST /api/payment-sessions returns them.
const subscription = {
  id: "ORD-1042",
  basket: "subscription",
  country: "FR",
  currency: "EUR",
  amount: 5000,
  renewal_amount: 3000,
  items: [
    { name: "Weekly box", unit_price: 3000, quantity: 1 },
    { name: "Sourdough loaf", unit_price: 600, quantity: 1, add_on: true },
    { name: "Comté 200 g", unit_price: 800, quantity: 1, add_on: true },
    { name: "Seasonal fruit", unit_price: 600, quantity: 1, add_on: true },
  ],
  customer: {
    name: "Marie Dubois",
    email: "marie.dubois@example.com",
    address: { address_line1: "12 rue Oberkampf", city: "Paris", zip: "75011", country: "FR" },
  },
};
const oneOffPT = {
  id: "ORD-1043",
  basket: "oneoff",
  country: "PT",
  currency: "EUR",
  amount: 4500,
  items: [{ name: "Christmas box", unit_price: 4500, quantity: 1 }],
  customer: {
    name: "Marie Dubois",
    email: "marie.dubois@example.com",
    address: { address_line1: "Rua Augusta 100", city: "Lisboa", zip: "1100-053", country: "PT" },
  },
};

// Joins segments back into plain text, to compare with the spec's copy.
const plain = (segments) => segments.map((s) => s.text).join("");
const bold = (segments) => segments.filter((s) => s.strong).map((s) => s.text);

test("formatAmount gives the spec's euro format from minor units", () => {
  assert.equal(lib.formatAmount(5000, "EUR"), "€50.00");
  assert.equal(lib.formatAmount(600, "EUR"), "€6.00");
  assert.equal(lib.formatAmount(0, "EUR"), "€0.00");
  assert.equal(lib.formatAmount(123456, "EUR"), "€1,234.56");
});

test("addOnTotal sums only add-ons, times quantity", () => {
  assert.equal(lib.addOnTotal(subscription.items), 2000);
  assert.equal(lib.addOnTotal(oneOffPT.items), 0);
  assert.equal(lib.addOnTotal([{ unit_price: 600, quantity: 2, add_on: true }]), 1200);
});

test("banner matches the spec, amount in bold", () => {
  const s = lib.bannerSegments(subscription);
  assert.equal(plain(s), "We reserve €50.00 on your card today. You're only charged when your box ships.");
  assert.deepEqual(bold(s), ["€50.00"]);
  assert.equal(plain(lib.bannerSegments(oneOffPT)), "We reserve €45.00 on your card today. You're only charged when your box ships.");
});

test("recurring terms match PRD section 10 and the spec, bold parts included", () => {
  const s = lib.termsSegments(subscription);
  assert.equal(
    plain(s),
    "You pay €50.00 today for your first box, including €20.00 of add-ons. Then €30.00 every week until you cancel. " +
      "Your card is authorised when you order and charged when your box ships. Skip, pause or cancel any time.",
  );
  assert.deepEqual(bold(s), ["€50.00", "€30.00 every week", "Skip, pause or cancel any time."]);
});

test("terms without add-ons drop the add-on clause", () => {
  const noAddOns = { ...subscription, amount: 3000, items: [subscription.items[0]] };
  assert.match(plain(lib.termsSegments(noAddOns)), /^You pay €30\.00 today for your first box\. Then €30\.00 every week/);
});

test("one-off orders have no terms, no weekly legal text, no 'then' line", () => {
  assert.equal(lib.termsSegments(oneOffPT), null);
  assert.equal(lib.legalWeekly(oneOffPT), ".");
  assert.equal(lib.summaryNext(oneOffPT), "");
});

test("legal and summary lines for subscriptions match the spec", () => {
  assert.equal(
    lib.legalWeekly(subscription),
    ", and to weekly payments of €30.00 until you cancel. You can cancel in your account up to 3 days before a delivery.",
  );
  assert.equal(lib.summaryNext(subscription), "Then €30.00 a week for the box. Add-ons are one‑off.");
});

test("addressLine matches the spec for FR and names PT in English", () => {
  assert.equal(lib.addressLine(subscription.customer.address), "12 rue Oberkampf, 75011 Paris, France");
  assert.equal(lib.addressLine(oneOffPT.customer.address), "Rua Augusta 100, 1100-053 Lisboa, Portugal");
});

test("readSessionResponse returns order and the untouched session", () => {
  const session = { id: "ps_fake", payment_session_secret: "pss_fake", extra: { kept: true } };
  const { order, paymentSession } = lib.readSessionResponse({ order: subscription, payment_session: session });
  assert.equal(order.id, "ORD-1042");
  assert.equal(paymentSession, session); // same object, not a copy
});

test("readSessionResponse refuses a malformed body with a plain message", () => {
  for (const bad of [null, {}, { order: subscription }, { payment_session: { id: "ps_x" } },
    { order: { ...subscription, amount: "5000" }, payment_session: { id: "ps_x" } }]) {
    assert.throws(() => lib.readSessionResponse(bad), { message: "Unexpected response from the server." });
  }
});

test("successUrl encodes the payment ID", () => {
  assert.equal(lib.successUrl("pay_abc123"), "/success?cko-payment-id=pay_abc123");
  assert.equal(lib.successUrl("pay_a&b=c"), "/success?cko-payment-id=pay_a%26b%3Dc");
});

test("demo strip: subscription is FR only, one-off FR or PT, bad input falls back", () => {
  assert.deepEqual(lib.countriesFor("subscription"), ["FR"]);
  assert.deepEqual(lib.countriesFor("oneoff"), ["FR", "PT"]);
  assert.deepEqual(lib.demoSelection("subscription", "PT"), { basket: "subscription", country: "FR" });
  assert.deepEqual(lib.demoSelection("oneoff", "PT"), { basket: "oneoff", country: "PT" });
  assert.deepEqual(lib.demoSelection("oneoff", "ES"), { basket: "oneoff", country: "FR" });
  assert.deepEqual(lib.demoSelection("renewal", "FR"), { basket: "subscription", country: "FR" });
});

test("flowOptions passes config, session and appearance through, locale en-GB", () => {
  const session = { id: "ps_fake" };
  const onPaymentCompleted = () => {};
  const onError = () => {};
  const appearance = { colorAction: "#1E5B36" };
  const opts = lib.flowOptions({ publicKey: "pk_sbox_fake", environment: "sandbox" }, session, { onPaymentCompleted, onError }, appearance);
  assert.deepEqual(Object.keys(opts).sort(), ["appearance", "environment", "locale", "onError", "onPaymentCompleted", "paymentSession", "publicKey"]);
  assert.equal(opts.appearance, appearance);
  assert.equal(opts.publicKey, "pk_sbox_fake");
  assert.equal(opts.environment, "sandbox");
  assert.equal(opts.locale, "en-GB");
  assert.equal(opts.paymentSession, session);
  assert.equal(opts.onPaymentCompleted, onPaymentCompleted);
});
