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

// --- Step 3.3: success page and failure banner --------------------------

// Payments shaped like GET /api/payments/{id} returns them, with the methods
// the sandbox run gave (D-027).
const card = (over = {}) => ({ type: "card", scheme: "visa", card_schemes: ["visa"], last4: "4242", card_type: "CREDIT", wallet: "", ...over });
const payment = (over = {}) => ({ payment_id: "pay_fakecitaaaaaaaaaaaaaaaaaaa", kind: "CIT", amount: 5000, status: "Authorized", method: card(), response_code: "10000", ...over });

test("paymentParams reads cko-payment-id and the failed flag", () => {
  assert.deepEqual(lib.paymentParams("?cko-payment-id=pay_abc"), { paymentId: "pay_abc", failed: false });
  assert.deepEqual(lib.paymentParams("?payment=failed&cko-payment-id=pay_abc"), { paymentId: "pay_abc", failed: true });
  assert.deepEqual(lib.paymentParams(""), { paymentId: "", failed: false });
});

test("startChoice and checkoutUrl round-trip, and bad input falls back", () => {
  const url = lib.checkoutUrl("oneoff", "PT");
  assert.equal(url, "/?basket=oneoff&country=PT");
  assert.deepEqual(lib.startChoice(url.slice(1)), { basket: "oneoff", country: "PT" });
  assert.deepEqual(lib.startChoice(""), { basket: "subscription", country: "FR" });
  assert.deepEqual(lib.startChoice("?basket=subscription&country=PT"), { basket: "subscription", country: "FR" });
});

test("API and failure URLs encode the payment ID", () => {
  assert.equal(lib.paymentApiUrl("pay_a/../b"), "/api/payments/pay_a%2F..%2Fb");
  assert.equal(lib.failureUrl("pay_abc"), "/?payment=failed&cko-payment-id=pay_abc");
});

test("readPaymentResponse refuses a malformed body", () => {
  const order = subscription;
  assert.deepEqual(lib.readPaymentResponse({ order, payment: payment() }).order, order);
  assert.throws(() => lib.readPaymentResponse({ order }), /Unexpected response/);
  assert.throws(() => lib.readPaymentResponse(null), /Unexpected response/);
});

test("pageState: failed statuses go back to checkout, reversed ones still show the order", () => {
  for (const s of ["Declined", "Expired", "Canceled"]) assert.equal(lib.pageState(s), "failed", s);
  assert.equal(lib.pageState("Pending"), "pending");
  for (const s of ["Authorized", "Captured", "Voided", "Refunded"]) assert.equal(lib.pageState(s), "paid", s);
});

test("methodLabel: processing.scheme first, card brands as fallback, wallets and MB WAY named", () => {
  assert.equal(lib.methodLabel(payment()), "Visa ···· 4242");
  // Co-badged, Marie chose CB: the processed scheme wins over source.scheme.
  assert.equal(lib.methodLabel(payment({ method: card({ scheme: "cartes_bancaires", card_schemes: ["visa", "cartes_bancaires"], last4: "0001" }) })), "CB ···· 0001");
  // Nothing processed (3DS failed): the card's own first brand.
  assert.equal(lib.methodLabel(payment({ method: card({ scheme: "", card_schemes: ["visa", "cartes_bancaires"], last4: "0027" }) })), "Visa ···· 0027");
  assert.equal(lib.methodLabel(payment({ method: card({ last4: "1387", wallet: "googlepay" }) })), "Google Pay, Visa ···· 1387");
  assert.equal(lib.methodLabel(payment({ method: { type: "mbway" } })), "MB WAY");
});

test("success title uses Marie's first name and the order ID", () => {
  assert.equal(lib.successTitle(subscription), "Thanks Marie, your order ORD-1042 is confirmed");
});

test("hero: authorised is reserved, not charged, as in the spec", () => {
  assert.deepEqual(lib.heroSegments(subscription, payment()), [
    { text: "We reserved " },
    { text: "€50.00", strong: true },
    { text: " on your Visa ending 4242. You're charged when your box ships. A confirmation is on its way to marie.dubois@example.com." },
  ]);
});

test("hero: Google Pay names the wallet, MB WAY says paid (captured at payment)", () => {
  const gpay = lib.heroSegments(subscription, payment({ method: card({ last4: "1387", wallet: "googlepay" }) }));
  assert.equal(gpay[2].text.startsWith(" on your Visa ending 1387 in Google Pay."), true);
  const mbway = lib.heroSegments(oneOffPT, payment({ amount: 4500, status: "Captured", method: { type: "mbway" } }));
  assert.deepEqual(mbway.slice(0, 2), [{ text: "You paid " }, { text: "€45.00", strong: true }]);
  assert.equal(mbway[2].text, " with MB WAY. A confirmation is on its way to marie.dubois@example.com.");
});

test("hero: voided and refunded say what happened to the money", () => {
  assert.equal(lib.heroSegments(subscription, payment({ status: "Voided" }))[2].text, " reserved on your Visa ending 4242. Nothing was charged.");
  assert.equal(lib.heroSegments(subscription, payment({ status: "Refunded" }))[0].text, "We refunded ");
});

test("facts: subscription, no delivery date, next payment without a date", () => {
  assert.deepEqual(lib.successFacts(subscription, payment()), [
    { label: "Plan", value: "Weekly box" },
    { label: "Next payment", value: "€30.00 a week, from your next box" },
    { label: "Paid with", value: "Visa ···· 4242" },
    { label: "Delivering to", value: "75011 Paris" },
  ]);
  assert.equal(lib.planTag(subscription), "Weekly subscription");
});

test("facts: one-off says so, as in the spec's one-off variant", () => {
  const facts = lib.successFacts(oneOffPT, payment({ method: { type: "mbway" } }));
  assert.equal(facts[0].value, "Christmas box, one-off");
  assert.equal(facts[1].value, "None, this is a one-off order");
  assert.equal(facts[2].value, "MB WAY");
  assert.equal(lib.planTag(oneOffPT), "One-off order");
});

test("failure banner after a 3DS redirect matches failure.html", () => {
  const failed = payment({ payment_id: "pay_u3bk27ybd2mynoxzz55bduhigu", status: "Declined", response_code: "20151" });
  assert.deepEqual(lib.failureBanner(failed, { order: { id: "ORD-1073" } }), {
    headline: "Your payment didn't go through.",
    detail: "Your bank couldn't confirm it was you, so nothing was reserved on your card.",
    help: "Try again below, or use another card or payment method.",
    attempt: "Previous attempt ORD-1073 · pay_u3bk27ybd2mynoxzz55bduhigu · Declined",
  });
});

test("failure banner: other declines, and Flow onError with nothing to look up", () => {
  const declined = lib.failureBanner(payment({ status: "Declined", response_code: "20051" }));
  assert.equal(declined.detail, "Your bank declined the payment, so nothing was reserved on your card.");
  // onError: no redirect, so nothing to look up and no attempt line (failure.html comment).
  assert.deepEqual(lib.failureBanner(null), {
    headline: "Your payment didn't go through.",
    detail: "Nothing was reserved on your card.",
    help: "Try again below, or use another card or payment method.",
    attempt: "",
  });
});

// --- Step 4.1a: back office band ----------------------------------------

const subscriptionView = {
  ...subscription,
  cit_payment_id: "pay_fakecitaaaaaaaaaaaaaaaaaaa",
  source_id: "src_fakecardaaaaaaaaaaaaaaaaaa",
  scheme: "cartes_bancaires",
  next_renewal_week: 3,
  renewals_ready: true,
};

test("formatDateTime shows Paris time in the mockup's format", () => {
  // 09:01 UTC is 10:01 in Paris in November (CET, UTC+1).
  assert.equal(lib.formatDateTime("2026-11-12T09:01:00Z"), "12 Nov 2026, 10:01");
  assert.equal(lib.formatDateTime(""), "");
});

test("order bar shows the stored card renewals reuse", () => {
  assert.deepEqual(lib.orderBar(subscriptionView), [
    { label: "Order", value: "ORD-1042", mono: true },
    { label: "Basket", value: "Subscription" },
    { label: "Stored card", value: "src_fakecardaaaaaaaaaaaaaaaaaa", mono: true },
    { label: "Scheme", value: "CB" },
    { label: "Next renewal", value: "Week 3" },
  ]);
});

test("order bar: no stored card turns renewals off, a one-off has none", () => {
  const noCard = lib.orderBar({ ...subscriptionView, source_id: "", scheme: "", renewals_ready: false });
  assert.deepEqual(noCard.slice(2), [
    { label: "Stored card", value: "None", mono: false },
    { label: "Scheme", value: "None" },
    { label: "Next renewal", value: "Off, no stored card" },
  ]);
  assert.equal(lib.orderBar({ ...oneOffPT, source_id: "", scheme: "" })[4].value, "None, one-off order");
});

test("order bar: a one-off shows the scheme its paid payment ran on, never a failed one", () => {
  // One-offs never record order.scheme (renewals only), so the bar reads the payment.
  const oneOff = (payments) => lib.orderBar({ ...oneOffPT, source_id: "", scheme: "", payments })[3].value;
  const visa = payment({ reference: "ORD-1043" });
  const cbDeclined = payment({ payment_id: "pay_b", reference: "ORD-1043", status: "Declined", method: card({ scheme: "cartes_bancaires" }) });
  assert.equal(oneOff([visa]), "Visa");
  assert.equal(oneOff([cbDeclined]), "None");
  assert.equal(oneOff([visa, cbDeclined]), "Visa");
  assert.equal(oneOff([payment({ status: "Captured", method: { type: "mbway" } })]), "None");
});

test("status cards: newest renewal first, first box last, retries newest first", () => {
  const order = {
    payments: [
      { payment_id: "pay_declined", reference: "ORD-1042" },
      { payment_id: "pay_cit", reference: "ORD-1042" },
      { payment_id: "pay_w3", reference: "ORD-1042-W3" },
      { payment_id: "pay_w2", reference: "ORD-1042-W2" },
    ],
  };
  assert.deepEqual(lib.paymentsNewestFirst(order).map((p) => p.payment_id), ["pay_w3", "pay_w2", "pay_cit", "pay_declined"]);
  assert.deepEqual(lib.paymentsNewestFirst({}), []);
});

test("card labels match the mockup", () => {
  assert.equal(lib.cardLabel(subscriptionView, { kind: "CIT", reference: "ORD-1042" }), "First box");
  assert.equal(lib.cardLabel(subscriptionView, { kind: "MIT", reference: "ORD-1042-W2" }), "Week 2 renewal");
  assert.equal(lib.cardLabel(oneOffPT, { kind: "CIT", reference: "ORD-1043" }), "One-off box");
});

test("status badges use the mockup's classes", () => {
  assert.equal(lib.statusClass("Authorized"), "authorized");
  assert.equal(lib.statusClass("Partially Captured"), "captured");
  assert.equal(lib.statusClass("Canceled"), "declined");
  assert.equal(lib.statusClass("Something new"), "pending");
});

test("method line: processed scheme badge, last 4 and type, G Pay, MB WAY", () => {
  assert.deepEqual(lib.methodLine(payment()), { badge: "VISA", text: "···· 4242 · Credit", wallet: "" });
  const cb = payment({ method: card({ scheme: "cartes_bancaires", card_schemes: ["visa", "cartes_bancaires"], last4: "0001" }) });
  assert.equal(lib.methodLine(cb).badge, "CB");
  assert.equal(lib.methodLine(payment({ method: card({ wallet: "googlepay", card_type: "DEBIT" }) })).wallet, "G Pay");
  assert.deepEqual(lib.methodLine(payment({ method: { type: "mbway" } })), { badge: "", text: "MB WAY", wallet: "" });
});

test("balances: four cells, zero ones greyed", () => {
  const cells = lib.balanceCells(payment({ balances: { authorized: 5000, captured: 5000, voided: 0, refunded: 0 } }), "EUR");
  assert.deepEqual(cells, [
    { label: "Authorised", value: "€50.00", zero: false },
    { label: "Captured", value: "€50.00", zero: false },
    { label: "Voided", value: "€0.00", zero: true },
    { label: "Refunded", value: "€0.00", zero: true },
  ]);
  assert.equal(lib.balanceCells(payment({ balances: undefined }), "EUR")[0].zero, true);
});

test("details: 3DS in words, response, expiry, and a renewal's link to the CIT", () => {
  assert.deepEqual(lib.paymentDetails(subscriptionView, payment({ three_ds: "Y", response_summary: "Approved", expires_on: "2026-11-12T09:01:00Z" })), [
    { term: "3DS", value: "Y, authenticated" },
    { term: "Response", value: "10000 Approved" },
    { term: "Auth expires", value: "12 Nov 2026, 10:01" },
  ]);
  const mit = lib.paymentDetails(subscriptionView, payment({ kind: "MIT", reference: "ORD-1042-W2", three_ds: "", response_summary: "Approved" }));
  assert.equal(mit[0].value, "Not applicable (MIT)");
  assert.deepEqual(mit.at(-1), { term: "Linked to", value: "pay_fakecitaaaaaaaaaaaaaaaaaaa (first CIT)", mono: true });
  const failed = lib.paymentDetails(subscriptionView, payment({ three_ds: "N", response_code: "20151", response_summary: "Cardholder failed 3D-Secure authentication" }));
  assert.deepEqual(failed.slice(0, 2).map((r) => r.value), ["N, not authenticated", "20151 Cardholder failed 3D-Secure authentication"]);
  assert.equal(lib.paymentDetails(oneOffPT, payment({ three_ds: "", method: { type: "mbway" } }))[0].value, "Not applicable");
});

// --- Step 4.1b: Dispatch ------------------------------------------------

test("dispatch: enabled from the server's can_capture, mockup copy", () => {
  assert.deepEqual(lib.dispatchAction({ ...subscriptionView, can_capture: true, capture_reason: "" }), {
    title: "Dispatch first box",
    what: "Captures the €50.00 first payment, like a warehouse scan would.",
    enabled: true,
    reason: "",
  });
});

test("dispatch: off with the server's reason, one-off wording", () => {
  const a = lib.dispatchAction({ ...oneOffPT, can_capture: false, capture_reason: "Captured at payment. Nothing to dispatch." });
  assert.equal(a.title, "Dispatch box");
  assert.equal(a.what, "Captures the €45.00 payment, like a warehouse scan would.");
  assert.equal(a.enabled, false);
  assert.equal(a.reason, "Captured at payment. Nothing to dispatch.");
});

test("dispatch: after a click it stays off while the async capture lands", () => {
  // Still Authorized, so the server says can_capture. The page must not invite a second click.
  const sentAt = new Date("2026-11-05T09:03:00Z"); // 10:03 in Paris
  const a = lib.dispatchAction({ ...subscriptionView, can_capture: true }, { sentAt });
  assert.equal(a.enabled, false);
  assert.equal(a.reason, "Capture sent at 10:03. Checkout.com captures in a few seconds.");
  // Once the GET says Captured, the server's reason wins.
  const done = lib.dispatchAction({ ...subscriptionView, can_capture: false, capture_reason: "First payment captured." }, { sentAt });
  assert.equal(done.reason, "First payment captured.");
});

test("actionError shows the message, Checkout.com's status and codes", () => {
  const err = Object.assign(new Error("Checkout.com refused the capture."), { ckoStatus: 403, errorCodes: ["capture_not_allowed"] });
  assert.equal(lib.actionError(err), "Checkout.com refused the capture. · HTTP 403 · capture_not_allowed");
  assert.equal(lib.actionError(new Error("Unknown order.")), "Unknown order.");
});

// --- Step 4.2: polling --------------------------------------------------

// A scripted fetch: returns the statuses in order, and a fake sleep that
// records waits. No real timers, so the tests are instant and exact.
function scripted(statuses) {
  const calls = { fetches: 0, sleeps: [] };
  return {
    calls,
    fetchOnce: async () => ({ payment: { status: statuses[Math.min(calls.fetches++, statuses.length - 1)] } }),
    sleep: async (ms) => { calls.sleeps.push(ms); },
  };
}

test("pollUntil stops on the first try where the status changed", async () => {
  const s = scripted(["Authorized", "Authorized", "Captured", "Captured"]);
  const out = await lib.pollUntil({ fetchOnce: s.fetchOnce, isDone: lib.statusChangedFrom("Authorized"), sleep: s.sleep });
  assert.equal(out.done, true);
  assert.equal(out.tries, 3);
  assert.equal(out.result.payment.status, "Captured");
  assert.equal(s.calls.fetches, 3);
  assert.deepEqual(s.calls.sleeps, [1000, 1000]); // between tries only
});

test("pollUntil stops after 10 tries and returns the last result", async () => {
  const s = scripted(["Authorized"]);
  const out = await lib.pollUntil({ fetchOnce: s.fetchOnce, isDone: lib.statusChangedFrom("Authorized"), sleep: s.sleep });
  assert.deepEqual([out.done, out.tries, out.result.payment.status], [false, 10, "Authorized"]);
  assert.equal(s.calls.fetches, 10);
  assert.equal(s.calls.sleeps.length, 9); // no wait after the last try
});

test("pollUntil done on the first try never sleeps, nor does tries 1", async () => {
  const s = scripted(["Captured"]);
  await lib.pollUntil({ fetchOnce: s.fetchOnce, isDone: lib.statusChangedFrom("Authorized"), sleep: s.sleep });
  const one = scripted(["Authorized"]);
  await lib.pollUntil({ fetchOnce: one.fetchOnce, isDone: () => false, tries: 1, sleep: one.sleep });
  assert.deepEqual([s.calls.sleeps.length, one.calls.fetches, one.calls.sleeps.length], [0, 1, 0]);
});

test("pollUntil stops on a fetch error and rethrows it with its codes", async () => {
  let fetches = 0;
  const err = Object.assign(new Error("We couldn't load this payment."), { ckoStatus: 502, errorCodes: [] });
  const fetchOnce = async () => { if (++fetches === 2) throw err; return { payment: { status: "Authorized" } }; };
  await assert.rejects(
    lib.pollUntil({ fetchOnce, isDone: () => false, sleep: async () => {} }),
    (e) => e === err,
  );
  assert.equal(fetches, 2);
});

test("statusChangedFrom compares the payment status only", () => {
  const changed = lib.statusChangedFrom("Authorized");
  assert.equal(changed({ payment: { status: "Authorized" } }), false);
  assert.equal(changed({ payment: { status: "Captured" } }), true);
});
